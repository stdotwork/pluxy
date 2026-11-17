//! Linux and macOS: a held process is an unreaped `std::process::Child` in a process group of its
//! own (its leader's process ID is the group's number), stopped through that group.

use super::Leader;
use std::fs::File;
use std::io::{self, Read};
use std::os::fd::AsRawFd;
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

pub(crate) use std::process::{Child, ChildStderr, ChildStdin, ChildStdout};

/// How long a pipe reader waits before it looks again whether its process was reaped.
const TICK: Duration = Duration::from_millis(100);

/// The state of child, without reaping it. Never after this host reaped child itself (`wait`):
/// its process ID may then be another child's, whose state this would give.
pub(crate) fn leader(child: &Child) -> Leader {
    let mut info: libc::siginfo_t = unsafe { std::mem::zeroed() };
    let r = unsafe {
        libc::waitid(
            libc::P_PID,
            child.id() as libc::id_t,
            &mut info,
            libc::WEXITED | libc::WNOHANG | libc::WNOWAIT,
        )
    };
    match (r, unsafe { info.si_pid() }) {
        (0, 0) => Leader::Running,
        (0, _) => Leader::Exited,
        _ => Leader::Gone,
    }
}

/// Asks child's process group to stop (SIGTERM), while its leader runs; true once asked, as the
/// caller then waits the stop grace.
pub(crate) fn term_group(child: &Child) -> bool {
    if leader(child) == Leader::Running {
        unsafe { libc::killpg(child.id() as libc::pid_t, libc::SIGTERM) };
    }
    true
}

/// Asks child alone to stop (SIGTERM), while it runs: for a child left in the app's own process
/// group (the app's core), which `term_group` would reach with the app. True once asked.
pub(crate) fn term_process(child: &Child) -> bool {
    if leader(child) == Leader::Running {
        unsafe { libc::kill(child.id() as libc::pid_t, libc::SIGTERM) };
    }
    true
}

/// Ends child's whole process group (SIGKILL), and child itself should it have left its group.
/// Never once it has been reaped, when its group's number could be another's.
pub(crate) fn kill_group(child: &mut Child) {
    if leader(child) != Leader::Gone {
        unsafe { libc::killpg(child.id() as libc::pid_t, libc::SIGKILL) };
        let _ = child.kill();
    }
}

/// Reads from r until it has data, the end, or closed is set (its process was reaped, while some
/// other process may still hold the pipe open): then None.
pub(crate) fn read_some(
    closed: &AtomicBool,
    r: &mut (impl Read + AsRawFd),
    buf: &mut [u8],
) -> Option<usize> {
    let fd = r.as_raw_fd();
    loop {
        if closed.load(Ordering::SeqCst) {
            return None;
        }
        let mut p = libc::pollfd {
            fd,
            events: libc::POLLIN,
            revents: 0,
        };
        let n = unsafe { libc::poll(&mut p, 1, TICK.as_millis() as i32) };
        if n < 0 && io::Error::last_os_error().kind() != io::ErrorKind::Interrupted {
            return None;
        }
        if n > 0 {
            return match r.read(buf) {
                Ok(0) | Err(_) => None,
                Ok(n) => Some(n),
            };
        }
    }
}

/// In a child between fork and exec: every descriptor but 0-2 closes as it executes. Nothing in it
/// may allocate.
///
/// # Safety
/// Only between fork and exec.
pub(crate) unsafe fn close_on_exec_from_3() -> io::Result<()> {
    #[cfg(target_os = "linux")]
    {
        if libc::syscall(
            libc::SYS_close_range,
            3u32,
            u32::MAX,
            libc::CLOSE_RANGE_CLOEXEC,
        ) != 0
        {
            return Err(io::Error::last_os_error());
        }
    }
    // No close_range: each possible descriptor up to the limit on their numbers (the soft limit
    // on open files). One that is not open fails with EBADF, which is what it should be.
    #[cfg(target_os = "macos")]
    {
        let max = libc::getdtablesize();
        let mut fd = 3;
        while fd < max {
            libc::fcntl(fd, libc::F_SETFD, libc::FD_CLOEXEC);
            fd += 1;
        }
    }
    Ok(())
}

/// Who, besides root and the app's user, may be able to change the program the app runs or a
/// folder above it. It matters on macOS only, which runs the program by its path (`exec_target`);
/// Linux runs the opened file itself.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Writers {
    /// No one: the app's own core and its native tools, which it installs in its own data folder.
    Nobody,
    /// Also the members of the `admin` group, through a folder that group may write (never the
    /// file itself): the program of a detached launch or of a managed runtime (`spawn.rs`), which
    /// the person confirmed wherever it is installed. macOS makes `/Applications` root:admin 0775,
    /// and Homebrew makes the folders of its prefix the installing user's, group admin, 0775 when
    /// that user is an admin; refusing them would refuse a direct launch of any program installed
    /// with Homebrew or inside an app in `/Applications`.
    ///
    /// What it costs: another admin account on this Mac, or a process running as one, can
    /// replace the program between this check and the exec (a replacement made before the check
    /// never runs: the check follows the opened file or refuses), and, for a program inside an
    /// app, whatever it later loads or starts from its app (frameworks, plug-ins, helper apps).
    /// Larger, and true with or without this rule: any program that uses folders `admin` may write
    /// (Homebrew's libraries in `Cellar` and `opt`, a script's interpreter and its modules under
    /// `lib`) loads files from them that that account can replace at any time, and nothing here
    /// checks them. Being in `admin` does not make that
    /// account root (sudo asks for its password): it needs only the folder's group write.
    Admins,
}

/// What runs the opened file f: on Linux its descriptor's `/proc/self/fd` path, which the kernel
/// opens as it executes, so a file replaced after the check never runs.
#[cfg(target_os = "linux")]
pub(crate) fn exec_target(f: &File, _: Writers) -> Result<PathBuf, String> {
    Ok(PathBuf::from(format!("/proc/self/fd/{}", f.as_raw_fd())))
}

/// What runs the opened file f: macOS cannot execute a descriptor, so the path f has now
/// (`F_GETPATH`, links resolved), once only root, this user and writers can change any folder on
/// it or the file (`secure`) and that path still names f (the same device and inode, with the
/// size, mode and times f has now). Only a process of this same user, or of one of writers, could
/// swap it between this check and the exec. What the file holds is the callers' to check, as on
/// Linux (`recheck`).
#[cfg(target_os = "macos")]
pub(crate) fn exec_target(f: &File, writers: Writers) -> Result<PathBuf, String> {
    use std::os::unix::ffi::OsStrExt;
    let mut buf = vec![0u8; libc::MAXPATHLEN as usize];
    if unsafe { libc::fcntl(f.as_raw_fd(), libc::F_GETPATH, buf.as_mut_ptr()) } != 0 {
        return Err(format!(
            "finding the opened file's path: {}",
            io::Error::last_os_error()
        ));
    }
    let len = buf.iter().position(|b| *b == 0).unwrap_or(buf.len());
    let path = PathBuf::from(std::ffi::OsStr::from_bytes(&buf[..len]));
    secure(&path, writers)?;
    let now = std::fs::symlink_metadata(&path).map_err(|e| format!("{}: {e}", path.display()))?;
    let opened = f.metadata().map_err(|e| e.to_string())?;
    if !same(&now, &opened) {
        return Err(format!(
            "{} is no longer the file that was opened",
            path.display()
        ));
    }
    Ok(path)
}

#[cfg(target_os = "macos")]
fn same(a: &std::fs::Metadata, b: &std::fs::Metadata) -> bool {
    use std::os::unix::fs::MetadataExt;
    a.dev() == b.dev()
        && a.ino() == b.ino()
        && a.size() == b.size()
        && a.mode() == b.mode()
        && (a.mtime(), a.mtime_nsec()) == (b.mtime(), b.mtime_nsec())
        && (a.ctime(), a.ctime_nsec()) == (b.ctime(), b.ctime_nsec())
}

/// Refused unless no user but root, this one and writers can change path or any folder above it:
/// each is owned by root or this user and writable by neither its group nor others, except a
/// folder writable by the `admin` group (and not others) when writers is `Admins`, and a sticky
/// folder (`/tmp`), where only the owner of the entry below it (root or this user) can rename or
/// remove it. No part of path may be a link (it is `F_GETPATH`'s, which resolves them).
#[cfg(target_os = "macos")]
pub(crate) fn secure(path: &std::path::Path, writers: Writers) -> Result<(), String> {
    use std::os::unix::fs::MetadataExt;
    let me = unsafe { libc::geteuid() };
    let admin = match writers {
        Writers::Nobody => None,
        Writers::Admins => admin_gid(),
    };
    let trusted_owner = |m: &std::fs::Metadata| m.uid() == 0 || m.uid() == me;
    let others_write = |m: &std::fs::Metadata| m.mode() & 0o022 != 0;
    let admins_write =
        |m: &std::fs::Metadata| m.mode() & 0o002 == 0 && admin.is_some_and(|g| m.gid() == g);
    let refuse = |p: &std::path::Path, why: &str| {
        Err(format!(
            "{} {why}: on macOS the app runs a program by its path, so no other user may be able to change it",
            p.display()
        ))
    };
    if !path.is_absolute() {
        return refuse(path, "is not a full path");
    }
    let parts: Vec<&std::path::Path> = path.ancestors().collect();
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
#[cfg(target_os = "macos")]
fn admin_gid() -> Option<u32> {
    let mut size = 16 << 10;
    loop {
        let mut g: libc::group = unsafe { std::mem::zeroed() };
        let mut buf = vec![0 as libc::c_char; size];
        let mut found: *mut libc::group = std::ptr::null_mut();
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
