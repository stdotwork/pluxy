//! The vault's folders on Windows (`vault.rs` has the Linux and macOS ones): each folder is held
//! open and used only relative to its handle, never by a path a link could redirect.
//!
//! - Names inside a held folder are opened with `NtCreateFile` relative to its handle (the
//!   `openat` of Unix), never following a link (`FILE_OPEN_REPARSE_POINT`; a link opened that way
//!   is refused).
//! - Folders are held without `FILE_SHARE_DELETE`, so no one can rename or remove a folder the
//!   vault holds.
//! - What the vault makes is private as it is made (`private_windows.rs`); what it reads must be
//!   private already, and a folder that is not is made so, as Unix tightens one to 0700.
//! - A file is replaced by renaming a new one over it through the new file's handle, relative to
//!   the folder's handle, and removed through its own handle.
//! - Windows has no call that flushes a folder's entries to disk: each file is flushed before it
//!   is renamed into place, and NTFS journals the rename itself.

use crate::private_windows::{is_private, make_private, owned, Private};
use std::fs::{self, File};
use std::io::{self, Read, Write};
use std::mem::{offset_of, size_of};
use std::os::windows::ffi::OsStrExt;
use std::os::windows::fs::{MetadataExt, OpenOptionsExt};
use std::os::windows::io::{AsRawHandle, FromRawHandle};
use std::path::Path;
use std::ptr::{null, null_mut};
use windows_sys::Wdk::Foundation::OBJECT_ATTRIBUTES;
use windows_sys::Wdk::Storage::FileSystem::{
    FileRenameInformation, FileRenameInformationEx, NtCreateFile, NtSetInformationFile,
    FILE_CREATE, FILE_DIRECTORY_FILE, FILE_NON_DIRECTORY_FILE, FILE_OPEN, FILE_OPEN_REPARSE_POINT,
    FILE_RENAME_INFORMATION, FILE_RENAME_POSIX_SEMANTICS, FILE_RENAME_REPLACE_IF_EXISTS,
    FILE_SYNCHRONOUS_IO_NONALERT,
};
use windows_sys::Win32::Foundation::{
    RtlNtStatusToDosError, ERROR_INVALID_PARAMETER, ERROR_NOT_SUPPORTED, ERROR_NO_MORE_FILES,
    HANDLE, NTSTATUS, OBJ_CASE_INSENSITIVE, STATUS_INVALID_INFO_CLASS, STATUS_INVALID_PARAMETER,
    STATUS_NOT_SUPPORTED, UNICODE_STRING,
};
use windows_sys::Win32::Security::SECURITY_ATTRIBUTES;
use windows_sys::Win32::Storage::FileSystem::{
    CreateDirectoryW, FileDispositionInfo, FileDispositionInfoEx, FileFullDirectoryInfo,
    FileFullDirectoryRestartInfo, GetFileInformationByHandleEx, SetFileInformationByHandle, DELETE,
    FILE_ADD_FILE, FILE_ADD_SUBDIRECTORY, FILE_ATTRIBUTE_NORMAL, FILE_ATTRIBUTE_REPARSE_POINT,
    FILE_DELETE_CHILD, FILE_DISPOSITION_FLAG_DELETE, FILE_DISPOSITION_FLAG_POSIX_SEMANTICS,
    FILE_DISPOSITION_INFO, FILE_DISPOSITION_INFO_EX, FILE_FLAG_BACKUP_SEMANTICS,
    FILE_FLAG_OPEN_REPARSE_POINT, FILE_FULL_DIR_INFO, FILE_GENERIC_READ, FILE_GENERIC_WRITE,
    FILE_LIST_DIRECTORY, FILE_READ_ATTRIBUTES, FILE_SHARE_DELETE, FILE_SHARE_READ,
    FILE_SHARE_WRITE, FILE_TRAVERSE, READ_CONTROL, SYNCHRONIZE, WRITE_DAC,
};
use windows_sys::Win32::System::IO::IO_STATUS_BLOCK;

/// What a held folder may be used for: list, open and make inside it, delete inside it, read and
/// change its access list.
const FOLDER_ACCESS: u32 = FILE_LIST_DIRECTORY
    | FILE_TRAVERSE
    | FILE_ADD_FILE
    | FILE_ADD_SUBDIRECTORY
    | FILE_DELETE_CHILD
    | FILE_READ_ATTRIBUTES
    | READ_CONTROL
    | WRITE_DAC
    | SYNCHRONIZE;

/// A vault folder, held open and used only relative to its handle.
pub(super) struct Folder(File);

fn handle(f: &File) -> HANDLE {
    f.as_raw_handle() as HANDLE
}

fn wide(s: &std::ffi::OsStr) -> Vec<u16> {
    s.encode_wide().collect()
}

/// Opens or makes name relative to the folder dir with `NtCreateFile`, never following a link:
/// a folder when folder is set, else a file. create makes it (and fails when it exists), with
/// private's descriptor; otherwise it must exist. Also the device store's opens beneath a
/// folder (`device.rs`'s `open_beneath`).
pub(crate) fn at(
    dir: &File,
    name: &str,
    access: u32,
    share: u32,
    folder: bool,
    create: Option<&Private>,
) -> io::Result<File> {
    let mut name = wide(name.as_ref());
    // A counted string holds at most 32767 characters: a longer name is refused, never cut short
    // to another one.
    let bytes = u16::try_from(name.len() * 2)
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "the name is too long"))?;
    let us = UNICODE_STRING {
        Length: bytes,
        MaximumLength: bytes,
        Buffer: name.as_mut_ptr(),
    };
    let attributes = OBJECT_ATTRIBUTES {
        Length: size_of::<OBJECT_ATTRIBUTES>() as u32,
        RootDirectory: handle(dir),
        ObjectName: &us,
        Attributes: OBJ_CASE_INSENSITIVE,
        SecurityDescriptor: create.map_or(null(), |p| p.descriptor().cast_const().cast()),
        SecurityQualityOfService: null(),
    };
    let mut h: HANDLE = null_mut();
    let mut io = IO_STATUS_BLOCK::default();
    let kind = if folder {
        FILE_DIRECTORY_FILE
    } else {
        FILE_NON_DIRECTORY_FILE
    };
    // SAFETY: every pointer is valid for the call; name and us outlive it; the handle comes back
    // in h, owned by the File made from it.
    let status = unsafe {
        NtCreateFile(
            &mut h,
            access | SYNCHRONIZE,
            &attributes,
            &mut io,
            null(),
            FILE_ATTRIBUTE_NORMAL,
            share,
            if create.is_some() {
                FILE_CREATE
            } else {
                FILE_OPEN
            },
            kind | FILE_OPEN_REPARSE_POINT | FILE_SYNCHRONOUS_IO_NONALERT,
            null(),
            0,
        )
    };
    if status < 0 {
        return Err(nt_error(status));
    }
    // SAFETY: h is the handle NtCreateFile opened, owned by nothing else.
    Ok(unsafe { File::from_raw_handle(h as _) })
}

/// A failed `NTSTATUS` as the Win32 error it stands for.
fn nt_error(status: NTSTATUS) -> io::Error {
    // SAFETY: a plain conversion of a status code.
    let code = unsafe { RtlNtStatusToDosError(status) };
    io::Error::from_raw_os_error(code as i32)
}

fn is_link(f: &File) -> io::Result<bool> {
    Ok(f.metadata()?.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0)
}

fn not_ours(what: &str) -> String {
    format!("{what} is not a folder of this user's")
}

/// Deletes the file open as f (with `DELETE`), through its handle: at once with POSIX semantics
/// where the file system has them, else when its last handle closes. Also the updater's removal
/// of what it recorded (`updater/exe.rs`).
pub(crate) fn delete(f: &File) -> io::Result<()> {
    let ex = FILE_DISPOSITION_INFO_EX {
        Flags: FILE_DISPOSITION_FLAG_DELETE | FILE_DISPOSITION_FLAG_POSIX_SEMANTICS,
    };
    // SAFETY: f is open with DELETE; ex is the structure the class names.
    if unsafe {
        SetFileInformationByHandle(
            handle(f),
            FileDispositionInfoEx,
            (&ex as *const FILE_DISPOSITION_INFO_EX).cast(),
            size_of::<FILE_DISPOSITION_INFO_EX>() as u32,
        )
    } != 0
    {
        return Ok(());
    }
    let e = io::Error::last_os_error();
    if !matches!(e.raw_os_error(), Some(c) if c == ERROR_INVALID_PARAMETER as i32 || c == ERROR_NOT_SUPPORTED as i32)
    {
        return Err(e);
    }
    let plain = FILE_DISPOSITION_INFO { DeleteFile: true };
    // SAFETY: as above, with the older structure.
    if unsafe {
        SetFileInformationByHandle(
            handle(f),
            FileDispositionInfo,
            (&plain as *const FILE_DISPOSITION_INFO).cast(),
            size_of::<FILE_DISPOSITION_INFO>() as u32,
        )
    } != 0
    {
        Ok(())
    } else {
        Err(io::Error::last_os_error())
    }
}

/// Renames the file open as f (with `DELETE`) to name in the folder dir, replacing what is
/// there: through the file's handle, relative to the folder's handle. Through
/// `NtSetInformationFile`: `SetFileInformationByHandle` makes the name a full path from the
/// current folder, which a rename relative to a folder's handle refuses.
fn rename(f: &File, dir: &File, name: &str) -> io::Result<()> {
    let name = wide(name.as_ref());
    let at = offset_of!(FILE_RENAME_INFORMATION, FileName);
    let size = (at + name.len() * 2).max(size_of::<FILE_RENAME_INFORMATION>());
    // u64s keep the structure aligned.
    let mut buf = vec![0u64; size.div_ceil(8)];
    let info = buf.as_mut_ptr() as *mut FILE_RENAME_INFORMATION;
    let set = |class, flags: Option<u32>| -> NTSTATUS {
        let mut io = IO_STATUS_BLOCK::default();
        // SAFETY: buf holds the structure and the name after it; the name is written in bounds.
        unsafe {
            match flags {
                Some(flags) => (*info).Anonymous.Flags = flags,
                None => {
                    (*info).Anonymous.Flags = 0;
                    (*info).Anonymous.ReplaceIfExists = true;
                }
            }
            (*info).RootDirectory = handle(dir);
            (*info).FileNameLength = (name.len() * 2) as u32;
            std::ptr::copy_nonoverlapping(
                name.as_ptr(),
                (info as *mut u8).add(at) as *mut u16,
                name.len(),
            );
            NtSetInformationFile(handle(f), &mut io, info.cast(), size as u32, class)
        }
    };
    let status = set(
        FileRenameInformationEx,
        Some(FILE_RENAME_REPLACE_IF_EXISTS | FILE_RENAME_POSIX_SEMANTICS),
    );
    if status >= 0 {
        return Ok(());
    }
    // A system or file system without the extended rename: the plain one.
    if !matches!(
        status,
        STATUS_INVALID_INFO_CLASS | STATUS_INVALID_PARAMETER | STATUS_NOT_SUPPORTED
    ) {
        return Err(nt_error(status));
    }
    match set(FileRenameInformation, None) {
        s if s >= 0 => Ok(()),
        s => Err(nt_error(s)),
    }
}

impl Folder {
    /// A folder of this user's that no one else may use (made so), from its open handle: not a
    /// link, a folder, an owner the rule accepts; a list that is not private is made private.
    fn checked(f: File, what: &str) -> Result<Folder, String> {
        let m = f.metadata().map_err(|e| format!("{what}: {e}"))?;
        let owner_ok = owned(&f).map_err(|e| format!("{what}: {e}"))?;
        if m.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 || !m.is_dir() || !owner_ok {
            return Err(not_ours(what));
        }
        if !is_private(&f).map_err(|e| format!("{what}: {e}"))? {
            make_private(&f, true).map_err(|e| format!("{what}: {e}"))?;
        }
        Ok(Folder(f))
    }

    /// The vault's own folder at path, made (private) when create is set; None when it is missing.
    /// Its parent is the app's data folder; the folder itself is never a link.
    pub(super) fn root(path: &Path, create: bool) -> Result<Option<Folder>, String> {
        let what = path.display().to_string();
        if create {
            if let Some(parent) = path.parent() {
                fs::create_dir_all(parent).map_err(|e| format!("{}: {e}", parent.display()))?;
            }
            let private = Private::new(true).map_err(|e| format!("{what}: {e}"))?;
            let sa = SECURITY_ATTRIBUTES {
                nLength: size_of::<SECURITY_ATTRIBUTES>() as u32,
                lpSecurityDescriptor: private.descriptor(),
                bInheritHandle: 0,
            };
            let mut name = wide(path.as_os_str());
            name.push(0);
            // SAFETY: name is NUL-terminated; sa and its descriptor live through the call.
            if unsafe { CreateDirectoryW(name.as_ptr(), &sa) } == 0 {
                let e = io::Error::last_os_error();
                if e.kind() != io::ErrorKind::AlreadyExists {
                    return Err(format!("{what}: {e}"));
                }
            }
        }
        match fs::OpenOptions::new()
            .access_mode(FOLDER_ACCESS)
            .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
            .custom_flags(FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT)
            .open(path)
        {
            Ok(f) => Folder::checked(f, &what).map(Some),
            Err(e) if e.kind() == io::ErrorKind::NotFound && !create => Ok(None),
            Err(e) => Err(format!("{what}: {e}")),
        }
    }

    /// The folder name inside it, made (private) when create is set; None when it is missing.
    pub(super) fn folder(&self, name: &str, create: bool) -> Result<Option<Folder>, String> {
        super::check_name(name)?;
        if create {
            let private = Private::new(true).map_err(|e| format!("{name}: {e}"))?;
            match at(&self.0, name, FILE_READ_ATTRIBUTES, 0, true, Some(&private)) {
                Err(e) if e.kind() != io::ErrorKind::AlreadyExists => {
                    return Err(format!("{name}: {e}"))
                }
                _ => {}
            }
        }
        match at(
            &self.0,
            name,
            FOLDER_ACCESS,
            FILE_SHARE_READ | FILE_SHARE_WRITE,
            true,
            None,
        ) {
            Ok(f) => Folder::checked(f, name).map(Some),
            Err(e) if e.kind() == io::ErrorKind::NotFound && !create => Ok(None),
            Err(e) => Err(format!("{name}: {e}")),
        }
    }

    /// The file name inside it, which must be a regular file of this user's that no one else may
    /// read; None when it is missing.
    pub(super) fn read(&self, name: &str) -> Result<Option<Vec<u8>>, String> {
        super::check_name(name)?;
        let mut f = match at(
            &self.0,
            name,
            FILE_GENERIC_READ,
            FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
            false,
            None,
        ) {
            Ok(f) => f,
            Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(None),
            Err(e) => return Err(format!("{name}: {e}")),
        };
        let m = f.metadata().map_err(|e| e.to_string())?;
        if is_link(&f).map_err(|e| e.to_string())?
            || !m.is_file()
            || !is_private(&f).map_err(|e| format!("{name}: {e}"))?
        {
            return Err(format!(
                "{name} must be a file of this user's that no one else may read"
            ));
        }
        let mut b = Vec::new();
        f.read_to_end(&mut b).map_err(|e| e.to_string())?;
        Ok(Some(b))
    }

    /// Writes name (private), replacing it in one step: a new file, flushed, renamed over it.
    pub(super) fn write(&self, name: &str, b: &[u8]) -> Result<(), String> {
        super::check_name(name)?;
        let tmp = format!(".{name}.{}.tmp", &crate::managed::random_key()[..16]);
        super::check_name(&tmp)?;
        let private = Private::new(false).map_err(|e| format!("{name}: {e}"))?;
        let mut f = at(
            &self.0,
            &tmp,
            FILE_GENERIC_WRITE | DELETE,
            FILE_SHARE_READ,
            false,
            Some(&private),
        )
        .map_err(|e| format!("{name}: {e}"))?;
        let done = f
            .write_all(b)
            .and_then(|_| f.sync_all())
            .and_then(|_| rename(&f, &self.0, name))
            .map_err(|e| format!("{name}: {e}"));
        if done.is_err() {
            let _ = delete(&f);
        }
        done
    }

    /// Removes the file name inside it; one already gone is not an error.
    pub(super) fn remove(&self, name: &str) -> Result<(), String> {
        super::check_name(name)?;
        let f = match at(
            &self.0,
            name,
            DELETE | FILE_READ_ATTRIBUTES,
            FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
            false,
            None,
        ) {
            Ok(f) => f,
            Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(()),
            Err(e) => return Err(format!("{name}: {e}")),
        };
        delete(&f).map_err(|e| format!("{name}: {e}"))
    }

    /// The names inside it, read through its handle.
    pub(super) fn names(&self) -> Result<Vec<String>, String> {
        entries(&self.0)
            .map(|v| v.into_iter().map(|(name, _)| name).collect())
            .map_err(|e| e.to_string())
    }
}

/// The names inside the folder open as dir (with `FILE_LIST_DIRECTORY`), each with its file
/// attributes, read through its handle. Also the updater's removal of a folder it recorded
/// (`updater/exe.rs`).
pub(crate) fn entries(dir: &File) -> io::Result<Vec<(String, u32)>> {
    let mut out = Vec::new();
    // u64s keep each entry aligned.
    let mut buf = vec![0u64; 8192];
    let mut class = FileFullDirectoryRestartInfo;
    loop {
        // SAFETY: buf is writable for its whole length.
        let got = unsafe {
            GetFileInformationByHandleEx(
                handle(dir),
                class,
                buf.as_mut_ptr().cast(),
                (buf.len() * 8) as u32,
            )
        };
        if got == 0 {
            let e = io::Error::last_os_error();
            if e.raw_os_error() == Some(ERROR_NO_MORE_FILES as i32) {
                return Ok(out);
            }
            return Err(e);
        }
        class = FileFullDirectoryInfo;
        let base = buf.as_ptr() as *const u8;
        let mut offset = 0usize;
        loop {
            // SAFETY: the system filled buf with a chain of entries, each starting at an
            // aligned offset inside it, with its name of FileNameLength bytes after it.
            let entry = unsafe { &*(base.add(offset) as *const FILE_FULL_DIR_INFO) };
            let name = unsafe {
                std::slice::from_raw_parts(
                    base.add(offset + offset_of!(FILE_FULL_DIR_INFO, FileName)) as *const u16,
                    entry.FileNameLength as usize / 2,
                )
            };
            let name = String::from_utf16_lossy(name);
            if name != "." && name != ".." {
                out.push((name, entry.FileAttributes));
            }
            if entry.NextEntryOffset == 0 {
                break;
            }
            offset += entry.NextEntryOffset as usize;
        }
    }
}
