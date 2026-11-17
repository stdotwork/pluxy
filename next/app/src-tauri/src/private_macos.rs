//! Private files and folders on macOS: the extended access lists that the 0600/0700 modes do not
//! cover. On macOS an access list is independent of the mode bits (a 0600 file can carry
//! `everyone allow read`), and a folder's inheritable entries reach what is made in it; on Linux
//! a POSIX access list's mask is the group bits, so the modes alone shut every named entry out.
//! So, beside the modes:
//!
//! - a private folder has no entries: one with any is given an empty list (`clear_entries`), as a
//!   too-open folder is tightened to 0700, so what is made in it inherits none;
//! - a private file the app reads has none: one with any is refused (`has_entries`);
//! - a file the app has just made gets an empty list, whatever its folder passed on.
//!
//! Every check and change goes through the open descriptor, never a path a link could redirect.
//! libc has no bindings for these calls: they are libSystem's (acl(3), `acl_get_fd_np`,
//! `acl_set_fd_np`), declared here.

use std::ffi::c_void;
use std::io;
use std::os::fd::AsRawFd;
use std::os::raw::c_int;

/// `acl_type_t`'s `ACL_TYPE_EXTENDED`, the only type macOS has (`sys/acl.h`).
const ACL_TYPE_EXTENDED: c_int = 0x0000_0100;
/// `acl_entry_id_t`'s `ACL_FIRST_ENTRY`.
const ACL_FIRST_ENTRY: c_int = 0;

extern "C" {
    fn acl_get_fd_np(fd: c_int, ty: c_int) -> *mut c_void;
    fn acl_set_fd_np(fd: c_int, acl: *mut c_void, ty: c_int) -> c_int;
    fn acl_init(count: c_int) -> *mut c_void;
    fn acl_get_entry(acl: *mut c_void, entry_id: c_int, entry: *mut *mut c_void) -> c_int;
    fn acl_free(obj: *mut c_void) -> c_int;
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
