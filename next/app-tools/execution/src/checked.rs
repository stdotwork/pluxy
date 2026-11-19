//! macOS: what the window of a terminal launch runs (`launch.rs`, `attach`): the file the app
//! opened and checked, by the path it has now, under the rule the app holds a launch's program to
//! (`exec_target` and `secure` with `Writers::Admins`, in `next/app/src-tauri/src/owned/unix.rs`,
//! which this ports: the app-tools never link the app; keep the two in step).

use std::fs::File;
use std::io;
use std::os::unix::fs::MetadataExt;
use std::os::unix::io::AsRawFd;
use std::path::{Path, PathBuf};

/// What runs the opened file f: macOS cannot execute a descriptor, so the path f has now
/// (`F_GETPATH`, links resolved), once only root, this user and the `admin` group can change any
/// folder on it, and only root and this user the file (`secure`), and that path still names f
/// (the same device and inode, with the size, mode and times f has now). A file put at the path
/// f was opened from never runs: f runs by its own path, wherever it was renamed to, or is
/// refused. Only a process of this user, or of an admin account, could swap it between this check
/// and the exec.
pub fn exec_target(f: &File) -> Result<PathBuf, String> {
    use std::os::unix::ffi::OsStrExt;
    let mut buf = vec![0u8; libc::MAXPATHLEN as usize];
    // SAFETY: buf has MAXPATHLEN bytes, as F_GETPATH needs.
    if unsafe { libc::fcntl(f.as_raw_fd(), libc::F_GETPATH, buf.as_mut_ptr()) } != 0 {
        return Err(format!(
            "finding the program's path: {}",
            io::Error::last_os_error()
        ));
    }
    let len = buf.iter().position(|b| *b == 0).unwrap_or(buf.len());
    let path = PathBuf::from(std::ffi::OsStr::from_bytes(&buf[..len]));
    secure(&path)?;
    let now = std::fs::symlink_metadata(&path).map_err(|e| format!("{}: {e}", path.display()))?;
    let opened = f.metadata().map_err(|e| e.to_string())?;
    if !same(&now, &opened) {
        return Err(format!(
            "{} is no longer the file that was confirmed",
            path.display()
        ));
    }
    Ok(path)
}

fn same(a: &std::fs::Metadata, b: &std::fs::Metadata) -> bool {
    a.dev() == b.dev()
        && a.ino() == b.ino()
        && a.size() == b.size()
        && a.mode() == b.mode()
        && (a.mtime(), a.mtime_nsec()) == (b.mtime(), b.mtime_nsec())
        && (a.ctime(), a.ctime_nsec()) == (b.ctime(), b.ctime_nsec())
}

/// Refused unless no user but root, this one and the `admin` group's members can change path or
/// any folder above it: each is owned by root or this user and writable by neither its group nor
/// others, except a folder writable by the `admin` group (and not others), as macOS makes
/// `/Applications` and Homebrew its folders, and a sticky folder (`/tmp`), where only the owner
/// of the entry below it (root or this user) can rename or remove it. No part of path may be a
/// link (it is `F_GETPATH`'s, which resolves them).
pub fn secure(path: &Path) -> Result<(), String> {
    // SAFETY: geteuid has no preconditions.
    let me = unsafe { libc::geteuid() };
    let admin = admin_gid();
    let trusted_owner = |m: &std::fs::Metadata| m.uid() == 0 || m.uid() == me;
    let others_write = |m: &std::fs::Metadata| m.mode() & 0o022 != 0;
    let admins_write =
        |m: &std::fs::Metadata| m.mode() & 0o002 == 0 && admin.is_some_and(|g| m.gid() == g);
    let refuse = |p: &Path, why: &str| {
        Err(format!(
            "{} {why}: on macOS the window runs a program by its path, so no other user may be able to change it",
            p.display()
        ))
    };
    if !path.is_absolute() {
        return refuse(path, "is not a full path");
    }
    let parts: Vec<&Path> = path.ancestors().collect();
    // From the file up to `/`; the entry below each folder is the one before it here.
    for (i, p) in parts.iter().enumerate() {
        let m = std::fs::symlink_metadata(p).map_err(|e| format!("{}: {e}", p.display()))?;
        let ft = m.file_type();
        if ft.is_symlink() {
            return refuse(p, "is a link");
        }
        if !trusted_owner(&m) {
            return refuse(p, "belongs to another user");
        }
        if i == 0 {
            if !ft.is_file() {
                return refuse(p, "is not a file");
            }
            if others_write(&m) {
                return refuse(p, "may be written by other users");
            }
            continue;
        }
        if !ft.is_dir() {
            return refuse(p, "is not a folder");
        }
        if others_write(&m) && !admins_write(&m) {
            let sticky = m.mode() & 0o1000 != 0;
            let below = std::fs::symlink_metadata(parts[i - 1]).map_err(|e| e.to_string())?;
            if !(sticky && trusted_owner(&below)) {
                return refuse(p, "may be changed by other users");
            }
        }
    }
    Ok(())
}

/// The `admin` group's number, looked up by its name: None, so that only the stricter rule holds,
/// when this Mac has no such group or it cannot be read. A group record larger than the buffer
/// (`ERANGE`) is read again with twice the room, up to 1 MiB.
fn admin_gid() -> Option<u32> {
    let mut size = 16 << 10;
    loop {
        // SAFETY: an all-zero group is a valid empty one, filled by getgrnam_r.
        let mut g: libc::group = unsafe { std::mem::zeroed() };
        let mut buf = vec![0 as libc::c_char; size];
        let mut found: *mut libc::group = std::ptr::null_mut();
        // SAFETY: the name is NUL-terminated; buf has buf.len() bytes.
        let r = unsafe {
            libc::getgrnam_r(
                c"admin".as_ptr(),
                &mut g,
                buf.as_mut_ptr(),
                buf.len(),
                &mut found,
            )
        };
        if r == libc::ERANGE && size < 1 << 20 {
            size *= 2;
            continue;
        }
        return (r == 0 && !found.is_null()).then_some(g.gr_gid);
    }
}

#[cfg(test)]
#[path = "checked_tests.rs"]
mod tests;
