//! The confirmations' file identities and their settings file's privacy on Windows: what
//! `std::os::unix` and `libc` give `confirmation.rs` on Linux and macOS.
//!
//! A file's identity is its volume serial number and 64-bit file index (`ino`), as
//! `GetFileInformationByHandle` reports them; its metadata adds size, last-write and change times
//! and attributes. The settings folder and file are private when this user, the owner this process
//! gives what it makes (its user, or for an elevated administrator the Administrators group) or
//! SYSTEM owns them, and their access list is protected (takes nothing from their folder's) and
//! allows no one but this user and SYSTEM: the rule of `private_windows.rs`, the app's one copy
//! of `next/app-tools/lib/files`' Windows rule, which this file's checks call and which makes
//! the settings private.
use std::fs::{File, OpenOptions};
use std::io;
use std::os::windows::fs::OpenOptionsExt;
use std::os::windows::io::AsRawHandle;
use std::path::Path;
use windows::Win32::Foundation::HANDLE;
use windows::Win32::Storage::FileSystem::{
    FileBasicInfo, GetFileInformationByHandle, GetFileInformationByHandleEx,
    BY_HANDLE_FILE_INFORMATION, FILE_ATTRIBUTE_REPARSE_POINT, FILE_BASIC_INFO,
    FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT,
};

/// What `Metadata` holds on Windows: volume, file index, size, last-write time, change time and
/// attributes (in the fields Unix fills with dev, ino, size, mtime, ctime and mode).
pub(super) struct Info {
    pub dev: u64,
    pub ino: u64,
    pub size: u64,
    pub mtime: i64,
    pub ctime: i64,
    pub attributes: u32,
}

fn handle(f: &File) -> HANDLE {
    HANDLE(f.as_raw_handle())
}

pub(super) fn info(f: &File) -> Result<Info, String> {
    let mut by = BY_HANDLE_FILE_INFORMATION::default();
    unsafe { GetFileInformationByHandle(handle(f), &mut by) }.map_err(|e| e.to_string())?;
    let mut basic = FILE_BASIC_INFO::default();
    unsafe {
        GetFileInformationByHandleEx(
            handle(f),
            FileBasicInfo,
            &mut basic as *mut FILE_BASIC_INFO as *mut core::ffi::c_void,
            std::mem::size_of::<FILE_BASIC_INFO>() as u32,
        )
    }
    .map_err(|e| e.to_string())?;
    Ok(Info {
        dev: u64::from(by.dwVolumeSerialNumber),
        ino: (u64::from(by.nFileIndexHigh) << 32) | u64::from(by.nFileIndexLow),
        size: (u64::from(by.nFileSizeHigh) << 32) | u64::from(by.nFileSizeLow),
        mtime: basic.LastWriteTime,
        ctime: basic.ChangeTime,
        attributes: by.dwFileAttributes,
    })
}

/// Opens path (a file or a folder) for reading its identity, following links as Unix's `open`
/// does; the share mode lets others go on writing, renaming and deleting it, as on Unix.
pub(super) fn open_any(path: &Path) -> io::Result<File> {
    OpenOptions::new()
        .read(true)
        .custom_flags(FILE_FLAG_BACKUP_SEMANTICS.0)
        .open(path)
}

/// Opens path without following a final link (the `O_NOFOLLOW` of Unix), for the settings file.
pub(super) fn open_no_follow(path: &Path) -> io::Result<File> {
    let f = OpenOptions::new()
        .read(true)
        .custom_flags(FILE_FLAG_BACKUP_SEMANTICS.0 | FILE_FLAG_OPEN_REPARSE_POINT.0)
        .open(path)?;
    let a = info(&f).map_err(io::Error::other)?.attributes;
    if a & FILE_ATTRIBUTE_REPARSE_POINT.0 != 0 {
        return Err(io::Error::other(
            "links and other reparse points are refused",
        ));
    }
    Ok(f)
}

/// Whether a regular file is a program here: Windows has no execute bit, so a file named `.exe` or
/// `.com` that starts `MZ` (a program image), the only kind the app starts. A batch file runs
/// through cmd, which reads its command line its own way, so it is never one.
pub(super) fn executable(path: &Path) -> bool {
    use std::io::Read;
    let named = path
        .extension()
        .and_then(|e| e.to_str())
        .is_some_and(|e| ["exe", "com"].iter().any(|x| e.eq_ignore_ascii_case(x)));
    let mut head = [0u8; 2];
    named
        && std::fs::File::open(path)
            .and_then(|mut f| f.read_exact(&mut head))
            .is_ok()
        && &head == b"MZ"
}

/// Refused unless f is private: owned by this user, by the owner this process gives what it makes
/// or by SYSTEM, with a protected access list whose allow entries are only this user's and
/// SYSTEM's (what the mode check refuses on Unix: another owner, or group or world bits).
pub(super) fn private(f: &File, what: &str) -> Result<(), String> {
    privacy(f, what)?
}

/// Whether f is private (`private_windows.rs`): an error when its permissions cannot be read,
/// otherwise why it is not private (another owner, or an access list that is not its own or lets
/// someone else in), if it is not.
pub(super) fn privacy(f: &File, what: &str) -> Result<Result<(), String>, String> {
    let read = |e: io::Error| format!("{what}: could not read its permissions ({e})");
    if !crate::private_windows::owned(f).map_err(read)? {
        return Ok(Err(format!("{what} is not owned by this user")));
    }
    if !crate::private_windows::is_private(f).map_err(read)? {
        return Ok(Err(format!(
            "{what} takes its access list from its folder or may be opened by another user or group"
        )));
    }
    Ok(Ok(()))
}

/// Adds an entry letting Everyone write path: for the tests of refusing such a file or folder.
#[cfg(test)]
pub(super) fn let_everyone_write(path: &Path) {
    assert!(crate::private_windows::tests::icacls(
        path,
        &["/grant", "*S-1-1-0:(W)"]
    ));
}
