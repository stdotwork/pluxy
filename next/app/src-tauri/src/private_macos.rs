//! Private files and folders on macOS: the extended access lists that the 0600/0700 modes do not
//! cover. On macOS an access list is independent of the mode bits (a 0600 file can carry
//! `everyone allow read`), and a folder's inheritable entries reach what is made in it; on Linux
//! a POSIX access list's mask is the group bits, so the modes alone shut every named entry out.
//! So, beside the modes:
//!
//! - a private folder has no entries: one with any is given an empty list (`clear_entries`), as a
//!   too-open folder is tightened to 0700, so what is made in it inherits none;
//! - where the app checks a private file's mode before using it (the vault's files, the
//!   remembered launches, the instance folder's lock and address, an installed core's files), a
//!   file with any entry is refused too (`has_entries`);
//! - a file the app writes privately is made, where the volume supports access lists, with an
//!   empty list that takes nothing from its folder (`create_new`), so no entry is on it at any
//!   moment; on a volume without them it is made plainly (`create_new_in`).
//!
//! Files and folders already inside a folder when it is emptied keep what they had. Each check
//! and change acts on an open descriptor, opened as the caller's mode check opens it: the vault,
//! the instance folder, the settings folder and what is inside the app's core folder are opened
//! without following a link; Access's store, the cores store and the device store open their
//! folder by its path, following a link as their mode change does, and make their files in it by
//! path. libc has no bindings for these calls: they are libSystem's (acl(3), `acl_get_fd_np`,
//! `acl_set_fd_np`, filesec(3), `openx_np`), declared here.

use std::ffi::c_void;
use std::fs::File;
use std::io;
use std::os::fd::{AsRawFd, FromRawFd};
use std::os::raw::c_int;

/// `acl_type_t`'s `ACL_TYPE_EXTENDED`, the only type macOS has (`sys/acl.h`).
const ACL_TYPE_EXTENDED: c_int = 0x0000_0100;
/// `acl_entry_id_t`'s `ACL_FIRST_ENTRY`.
const ACL_FIRST_ENTRY: c_int = 0;
/// `acl_flag_t`'s `ACL_FLAG_NO_INHERIT` (`sys/acl.h`), the kernel's `KAUTH_ACL_NO_INHERIT`.
const ACL_FLAG_NO_INHERIT: c_int = 1 << 17;
/// `filesec_property_t`'s `FILESEC_MODE` and `FILESEC_ACL` (`sys/fcntl.h`).
const FILESEC_MODE: c_int = 4;
const FILESEC_ACL: c_int = 5;

extern "C" {
    fn acl_get_fd_np(fd: c_int, ty: c_int) -> *mut c_void;
    fn acl_set_fd_np(fd: c_int, acl: *mut c_void, ty: c_int) -> c_int;
    fn acl_init(count: c_int) -> *mut c_void;
    fn acl_get_entry(acl: *mut c_void, entry_id: c_int, entry: *mut *mut c_void) -> c_int;
    fn acl_get_flagset_np(obj: *mut c_void, flagset: *mut *mut c_void) -> c_int;
    fn acl_add_flag_np(flagset: *mut c_void, flag: c_int) -> c_int;
    fn acl_free(obj: *mut c_void) -> c_int;
    fn filesec_init() -> *mut c_void;
    fn filesec_set_property(fsec: *mut c_void, property: c_int, value: *const c_void) -> c_int;
    fn filesec_free(fsec: *mut c_void);
    fn openx_np(path: *const std::os::raw::c_char, flags: c_int, fsec: *mut c_void) -> c_int;
}

/// Makes a new file at path (failing if anything is there, a link too) with mode and an empty
/// extended access list that inherits nothing from its folder, for writing. The kernel takes
/// no entry from the folder when the list a file is made with is flagged `ACL_FLAG_NO_INHERIT`
/// (`open_extended`, `kauth_acl_inherit`), so no other user's entry is on it at any moment.
pub fn create_new(path: &std::path::Path, mode: libc::mode_t) -> io::Result<File> {
    use std::os::unix::ffi::OsStrExt;
    let p = std::ffi::CString::new(path.as_os_str().as_bytes())
        .map_err(|_| io::Error::from(io::ErrorKind::InvalidInput))?;
    // SAFETY: a new, empty list, freed below.
    let acl = unsafe { acl_init(0) };
    if acl.is_null() {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: a new file security description, freed below.
    let fsec = unsafe { filesec_init() };
    if fsec.is_null() {
        let e = io::Error::last_os_error();
        // SAFETY: acl came from acl_init and is freed once.
        unsafe { acl_free(acl) };
        return Err(e);
    }
    // SAFETY: acl and fsec are valid; filesec_set_property copies what it is given; the
    // descriptor openx_np gives back is owned by the File.
    let made = unsafe {
        let mut flags = std::ptr::null_mut();
        if acl_get_flagset_np(acl, &mut flags) != 0
            || acl_add_flag_np(flags, ACL_FLAG_NO_INHERIT) != 0
            || filesec_set_property(fsec, FILESEC_ACL, (&acl as *const *mut c_void).cast()) != 0
            || filesec_set_property(fsec, FILESEC_MODE, (&mode as *const libc::mode_t).cast()) != 0
        {
            Err(io::Error::last_os_error())
        } else {
            let fd = openx_np(
                p.as_ptr(),
                libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL | libc::O_NOFOLLOW | libc::O_CLOEXEC,
                fsec,
            );
            if fd < 0 {
                Err(io::Error::last_os_error())
            } else {
                Ok(File::from_raw_fd(fd))
            }
        }
    };
    // SAFETY: each came from its init call and is freed once.
    unsafe {
        filesec_free(fsec);
        acl_free(acl);
    }
    made
}

/// Makes a new file at path, in the folder open as folder, for writing with mode: on a volume
/// that keeps access lists by `create_new`; on one that does not (where the kernel refuses a
/// file made with a list) by a plain exclusive open that follows no link. Either way the new
/// file is refused, and removed, when it has an entry all the same (one a file server added).
pub fn create_new_in(
    folder: &impl AsRawFd,
    path: &std::path::Path,
    mode: libc::mode_t,
) -> io::Result<File> {
    // SAFETY: folder is an open descriptor; fpathconf only reads it.
    let lists = unsafe { libc::fpathconf(folder.as_raw_fd(), libc::_PC_EXTENDED_SECURITY_NP) } > 0;
    let f = if lists {
        create_new(path, mode)?
    } else {
        use std::os::unix::fs::OpenOptionsExt;
        std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(mode.into())
            .custom_flags(libc::O_NOFOLLOW)
            .open(path)?
    };
    match has_entries(&f) {
        Ok(false) => Ok(f),
        found => {
            drop(f);
            let _ = std::fs::remove_file(path);
            Err(found.err().unwrap_or_else(|| {
                io::Error::other(format!(
                    "{} has an extended access list entry as it is made",
                    path.display()
                ))
            }))
        }
    }
}

/// Whether the file or folder open as f has an extended access list with any entry, allow or
/// deny. No list is none (`acl_get_fd_np` gives none with `ENOENT`), and so is an empty one.
pub fn has_entries(f: &impl AsRawFd) -> io::Result<bool> {
    // SAFETY: the calling thread's errno, cleared so a missing list is told from a failure.
    unsafe { *libc::__error() = 0 };
    // SAFETY: f is an open descriptor; a list given back is freed below.
    let acl = unsafe { acl_get_fd_np(f.as_raw_fd(), ACL_TYPE_EXTENDED) };
    if acl.is_null() {
        let e = io::Error::last_os_error();
        return match e.raw_os_error() {
            Some(libc::ENOENT) => Ok(false),
            _ => Err(e),
        };
    }
    let mut entry = std::ptr::null_mut();
    // SAFETY: acl is a list from acl_get_fd_np; entry is written only on success.
    let found = unsafe { acl_get_entry(acl, ACL_FIRST_ENTRY, &mut entry) } == 0;
    // SAFETY: acl came from acl_get_fd_np and is freed once.
    unsafe { acl_free(acl) };
    Ok(found)
}

/// Gives the file or folder open as f an empty extended access list when it has any entry, so
/// no one but its owner reaches it beyond its mode and a folder passes nothing on.
pub fn clear_entries(f: &impl AsRawFd) -> io::Result<()> {
    if !has_entries(f)? {
        return Ok(());
    }
    // SAFETY: a new, empty list, freed below.
    let acl = unsafe { acl_init(0) };
    if acl.is_null() {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: f is an open descriptor and acl a valid list.
    let set = unsafe { acl_set_fd_np(f.as_raw_fd(), acl, ACL_TYPE_EXTENDED) };
    let e = io::Error::last_os_error();
    // SAFETY: acl came from acl_init and is freed once.
    unsafe { acl_free(acl) };
    if set != 0 {
        return Err(e);
    }
    Ok(())
}

/// Opens name in the folder open as dir, never through a link: a folder when folder is set,
/// otherwise a plain file. None when nothing of that kind is there (missing, a link, or
/// something else).
pub fn open_beneath(
    dir: &impl AsRawFd,
    name: impl AsRef<std::ffi::OsStr>,
    folder: bool,
) -> io::Result<Option<File>> {
    use std::os::unix::ffi::OsStrExt;
    let n = std::ffi::CString::new(name.as_ref().as_bytes())
        .map_err(|_| io::Error::from(io::ErrorKind::InvalidInput))?;
    let flags = libc::O_RDONLY
        | libc::O_NOFOLLOW
        | libc::O_CLOEXEC
        | libc::O_NONBLOCK
        | libc::O_NOCTTY
        | if folder { libc::O_DIRECTORY } else { 0 };
    // SAFETY: dir is an open descriptor and n a C string; a descriptor given back is owned below.
    let fd = unsafe { libc::openat(dir.as_raw_fd(), n.as_ptr(), flags) };
    if fd < 0 {
        let e = io::Error::last_os_error();
        return match e.raw_os_error() {
            Some(libc::ENOENT | libc::ELOOP | libc::ENOTDIR) => Ok(None),
            _ => Err(e),
        };
    }
    // SAFETY: fd was just opened and nothing else owns it.
    let f = unsafe { File::from_raw_fd(fd) };
    let t = f.metadata()?.file_type();
    Ok((if folder { t.is_dir() } else { t.is_file() }).then_some(f))
}

/// Tests: adds the access list entry `entry` (`chmod +a`'s form, such as `everyone allow read`)
/// to path with macOS's own `chmod`.
#[cfg(test)]
pub(crate) fn add_entry(path: &std::path::Path, entry: &str) {
    let out = std::process::Command::new("/bin/chmod")
        .arg("+a")
        .arg(entry)
        .arg(path)
        .output()
        .expect("/bin/chmod");
    assert!(
        out.status.success(),
        "chmod +a {entry:?} {}: {out:?}",
        path.display()
    );
}

#[cfg(test)]
#[path = "private_macos_tests.rs"]
mod tests;
