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
/// Linux runs the opened file itself, and lets a terminal run a launch's program by its path only
/// where root and this user alone (and this user's private group) can change it (`path_target`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Writers {
    /// No one: the app's own core and its native tools, which it installs in its own data folder.
    Nobody,
    /// Also the members of the `admin` group, through a folder that group may write, by its mode
    /// or an access list entry (never the file itself): the program of a detached launch or of a
    /// managed runtime, and the program a terminal launch runs (`spawn.rs`, and the execution
    /// program in Terminal's window), which the person confirmed wherever it is installed. macOS
    /// makes `/Applications` root:admin 0775, and Homebrew makes the folders of its prefix the
    /// installing user's, group admin, 0775 when that user is an admin; refusing them would
    /// refuse a direct launch of any program installed with Homebrew or inside an app in
    /// `/Applications`.
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

/// Refused unless path still leads to the opened file f (`same`) and no user but root and this
/// one can change f or where path leads: the program a terminal runs on Linux, which the terminal
/// finds by path as it starts, after this check. Each folder the path's resolution passes
/// through, each link on the way and the file must be root's or this user's and writable by
/// others only in a sticky folder (`/tmp`, `/nix/store`), where only the owner of an entry (root
/// or this user, as checked) can rename or remove it, and by its group only when that group is
/// this user's own private group (`private_group`): Homebrew on Linux makes its folders the
/// user's, in the user's group, group-writable, and a umask of 002 (Debian's and Ubuntu's default
/// for a user whose group is named as they are) makes `~/.local/bin` and what npm or pipx install
/// there 0775. Any other group is refused: Linux has no group like macOS' `admin` whose folders
/// hold programs.
/// Links are followed as the kernel follows them, from the folder that holds each; a path through
/// the process file system (`/proc`, and `/dev/fd` and `/dev/std*`, which lead there) is refused,
/// since `/proc/self` and its links lead elsewhere for the terminal than for the app.
#[cfg(target_os = "linux")]
pub(crate) fn path_target(f: &File, path: &std::path::Path) -> Result<(), String> {
    path_target_by(f, path, &private_group)
}

/// `path_target`, with private saying whether a group is this user's private group.
#[cfg(target_os = "linux")]
pub(crate) fn path_target_by(
    f: &File,
    path: &std::path::Path,
    private: &dyn Fn(u32) -> bool,
) -> Result<(), String> {
    use std::ffi::OsString;
    use std::os::unix::fs::MetadataExt;
    use std::path::{Component, Path};
    const MAX_LINKS: usize = 40;
    // The process file system's `f_type`.
    const PROC_SUPER_MAGIC: libc::c_long = 0x9fa0;
    let me = unsafe { libc::geteuid() };
    let read = |p: &Path| std::fs::symlink_metadata(p).map_err(|e| format!("{}: {e}", p.display()));
    let refuse = |p: &Path, why: &str| {
        Err(format!(
            "{} {why}: a terminal runs a launch's program by its path, so no other user may be able to change it",
            p.display()
        ))
    };
    // A refusal for write permission, with the command that takes it away (`chmod_hint`).
    let writable = |p: &Path, why: &str, owner: u32| {
        Err(format!(
            "{} {why}: a terminal runs a launch's program by its path, so no other user may be able to change it ({})",
            p.display(),
            chmod_hint(p, owner == 0)
        ))
    };
    // Whether a user but root and this one may write what m describes.
    let others_write = |m: &std::fs::Metadata| {
        m.mode() & 0o002 != 0 || (m.mode() & 0o020 != 0 && !private(m.gid()))
    };
    let in_proc = |p: &Path| {
        let Ok(c) = std::ffi::CString::new(p.as_os_str().as_encoded_bytes()) else {
            return true;
        };
        let mut s: libc::statfs = unsafe { std::mem::zeroed() };
        // SAFETY: c is NUL-terminated; s is a statfs to fill.
        unsafe {
            libc::statfs(c.as_ptr(), &mut s) != 0 || s.f_type as libc::c_long == PROC_SUPER_MAGIC
        }
    };
    if !path.is_absolute() {
        return refuse(path, "is not a full path");
    }
    // The names still to resolve, the next one last, and the folder reached so far.
    let names = |p: &Path| -> Vec<OsString> {
        p.components()
            .rev()
            .filter_map(|c| match c {
                Component::Normal(n) => Some(n.to_os_string()),
                Component::ParentDir => Some("..".into()),
                _ => None,
            })
            .collect()
    };
    let mut todo = names(path);
    let mut at = PathBuf::from("/");
    let mut links = 0;
    loop {
        let m = read(&at)?;
        if m.uid() != 0 && m.uid() != me {
            return refuse(&at, "belongs to another user");
        }
        let Some(name) = todo.pop() else {
            if !m.file_type().is_file() {
                return refuse(&at, "is not a file");
            }
            if others_write(&m) {
                return writable(&at, "may be written by other users", m.uid());
            }
            let opened = f.metadata().map_err(|e| e.to_string())?;
            if !same(&m, &opened) {
                return Err(format!(
                    "{} is no longer the file that was confirmed",
                    path.display()
                ));
            }
            return Ok(());
        };
        if !m.is_dir() {
            return refuse(&at, "is not a folder");
        }
        if in_proc(&at) {
            return Err(format!(
                "{} is in the process file system, where a path leads elsewhere for the terminal \
                 than for the app: name the program by its own path",
                at.display()
            ));
        }
        if name == ".." {
            // A folder above one reached was checked on the way down.
            at.pop();
            continue;
        }
        // Its entries are changed only by root and this user, or, in a sticky folder, each by
        // its owner, whose entry this check reads next.
        if others_write(&m) && m.mode() & 0o1000 == 0 {
            return writable(&at, "may be changed by other users", m.uid());
        }
        let next = at.join(&name);
        let n = read(&next)?;
        if n.file_type().is_symlink() {
            if n.uid() != 0 && n.uid() != me {
                return refuse(&next, "belongs to another user");
            }
            links += 1;
            if links > MAX_LINKS {
                return refuse(path, "goes through too many links");
            }
            let target =
                std::fs::read_link(&next).map_err(|e| format!("{}: {e}", next.display()))?;
            if target.is_absolute() {
                at = PathBuf::from("/");
            }
            todo.extend(names(&target));
            continue;
        }
        at = next;
    }
}

/// The command that takes group and others' write permission from p, its path quoted for a
/// shell: run as root when root owns p.
#[cfg(target_os = "linux")]
pub(crate) fn chmod_hint(p: &std::path::Path, root: bool) -> String {
    let quoted = p.to_string_lossy().replace('\'', r"'\''");
    if root {
        format!("as root, `chmod g-w,o-w '{quoted}'` makes it root's alone")
    } else {
        format!("`chmod g-w,o-w '{quoted}'` makes it yours alone")
    }
}

/// What `private` judges a group by.
#[cfg(target_os = "linux")]
pub(crate) struct Group<'a> {
    /// This user's name and primary group.
    pub user: &'a str,
    pub primary: u32,
    /// The group's name, number and members.
    pub name: &'a str,
    pub gid: u32,
    pub members: &'a [String],
    /// The primary group of every other user `/etc/passwd` lists.
    pub others_primary: &'a [u32],
    /// The number of every other line of `/etc/group`.
    pub other_lines: &'a [u32],
}

/// Whether g is this user's private group, no one else's: this user's primary group, named as
/// the user, with no member but the user, no other user's primary group, and the only line of
/// `/etc/group` with its number. A group's name says nothing by itself: `USERGROUPS_ENAB` and
/// `pam_umask` make no promise about who else is in it.
#[cfg(target_os = "linux")]
pub(crate) fn private(g: &Group) -> bool {
    g.gid == g.primary
        && g.name == g.user
        && g.members.iter().all(|m| m == g.user)
        && !g.others_primary.contains(&g.gid)
        && !g.other_lines.contains(&g.gid)
}

/// Whether group gid is this user's private group (`private`): this user and the group as the
/// system's user and group databases give them (`getpwuid_r`, `getgrgid_r`), and every other user
/// and group line of `/etc/passwd` and `/etc/group`, read as files (`getpwent` and `getgrent` keep
/// process-wide state, unsafe in the app's threads). A lookup or read that fails says no. Users
/// and groups from other sources (LDAP, sssd, systemd-userdb) are not scanned, and the group
/// database is trusted to list a group's members.
#[cfg(target_os = "linux")]
pub(crate) fn private_group(gid: u32) -> bool {
    use std::ffi::CStr;
    let me = unsafe { libc::geteuid() };
    let mut size = 16 << 10;
    let (user, primary, name, members) = loop {
        let mut pw: libc::passwd = unsafe { std::mem::zeroed() };
        let mut gr: libc::group = unsafe { std::mem::zeroed() };
        let mut pbuf = vec![0 as libc::c_char; size];
        let mut gbuf = vec![0 as libc::c_char; size];
        let mut pfound: *mut libc::passwd = std::ptr::null_mut();
        let mut gfound: *mut libc::group = std::ptr::null_mut();
        // SAFETY: each buffer has its length's bytes; the results point into them.
        let (rp, rg) = unsafe {
            (
                libc::getpwuid_r(me, &mut pw, pbuf.as_mut_ptr(), pbuf.len(), &mut pfound),
                libc::getgrgid_r(gid, &mut gr, gbuf.as_mut_ptr(), gbuf.len(), &mut gfound),
            )
        };
        if (rp == libc::ERANGE || rg == libc::ERANGE) && size < 1 << 20 {
            size *= 2;
            continue;
        }
        if rp != 0 || rg != 0 || pfound.is_null() || gfound.is_null() {
            return false;
        }
        // SAFETY: both records were filled, their strings NUL-terminated in the buffers, and the
        // member list ends with a null pointer.
        unsafe {
            let text = |p: *const libc::c_char| CStr::from_ptr(p).to_string_lossy().into_owned();
            let mut members = Vec::new();
            let mut m = gr.gr_mem;
            while !m.is_null() && !(*m).is_null() {
                members.push(text(*m));
                m = m.add(1);
            }
            break (text(pw.pw_name), pw.pw_gid, text(gr.gr_name), members);
        }
    };
    // The colon-separated fields of each entry of file: None when it cannot be read or an entry's
    // number cannot. NIS compat entries (`+…`, `-…`) are left out, as other sources are.
    let entries = |file: &str, number: usize| -> Option<Vec<(String, u32)>> {
        let text = std::fs::read_to_string(file).ok()?;
        text.lines()
            .map(str::trim)
            .filter(|l| !l.is_empty() && !l.starts_with('#') && !l.starts_with(['+', '-']))
            .map(|l| {
                let f: Vec<&str> = l.split(':').collect();
                Some((f.first()?.to_string(), f.get(number)?.parse().ok()?))
            })
            .collect()
    };
    let (Some(users), Some(mut lines)) = (entries("/etc/passwd", 3), entries("/etc/group", 2))
    else {
        return false;
    };
    let others_primary: Vec<u32> = users
        .iter()
        .filter(|(u, _)| *u != user)
        .map(|(_, g)| *g)
        .collect();
    // This group's own line, if the file has it, is not another.
    if let Some(i) = lines.iter().position(|(n, g)| *n == name && *g == gid) {
        lines.remove(i);
    }
    let other_lines: Vec<u32> = lines.iter().map(|(_, g)| *g).collect();
    private(&Group {
        user: &user,
        primary,
        name: &name,
        gid,
        members: &members,
        others_primary: &others_primary,
        other_lines: &other_lines,
    })
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

/// Whether a and b are the same file, unchanged: device, inode, size, mode and times.
fn same(a: &std::fs::Metadata, b: &std::fs::Metadata) -> bool {
    use std::os::unix::fs::MetadataExt;
    a.dev() == b.dev()
        && a.ino() == b.ino()
        && a.size() == b.size()
        && a.mode() == b.mode()
        && (a.mtime(), a.mtime_nsec()) == (b.mtime(), b.mtime_nsec())
        && (a.ctime(), a.ctime_nsec()) == (b.ctime(), b.ctime_nsec())
}

/// Why `secure` refuses a program.
#[cfg(target_os = "macos")]
const BY_PATH: &str =
    "on macOS the app runs a program by its path, so no other user may be able to change it";

/// Refused unless no user but root, this one and writers can change path or any folder above it:
/// each is owned by root or this user and writable by neither its group nor others, except a
/// folder writable by the `admin` group (and not others) when writers is `Admins`, and a sticky
/// folder (`/tmp`), where only the owner of the entry below it (root or this user) can rename or
/// remove it. No part of path may be a link (it is `F_GETPATH`'s, which resolves them).
///
/// An extended access list can let others in whatever the mode (`private_macos.rs`), so each
/// part's entries, inherited ones too, are judged by what they allow (`acl_refusal`). Each part's
/// mode and list are read together (`private_macos::status`), from `/` down to the file: a folder
/// is read before what is below it, so a user who could swap something below it is refused for
/// that folder, or, should they give up that right first, the swap is read as it is.
#[cfg(target_os = "macos")]
pub(crate) fn secure(path: &std::path::Path, writers: Writers) -> Result<(), String> {
    use std::os::unix::fs::MetadataExt;
    let me = unsafe { libc::geteuid() };
    let admin = match writers {
        Writers::Nobody => None,
        Writers::Admins => admin_gid(),
    };
    let trusted_owner = |uid: u32| uid == 0 || uid == me;
    let others_write = |sb: &libc::stat| sb.st_mode & 0o022 != 0;
    let admins_write =
        |sb: &libc::stat| sb.st_mode & 0o002 == 0 && admin.is_some_and(|g| sb.st_gid == g);
    let refuse = |p: &std::path::Path, why: &str| Err(format!("{} {why}: {BY_PATH}", p.display()));
    if !path.is_absolute() {
        return refuse(path, "is not a full path");
    }
    let mut parts: Vec<&std::path::Path> = path.ancestors().collect();
    parts.reverse();
    let file = parts.len() - 1;
    // From `/` down to the file; the entry below each folder is the one after it here.
    for (i, p) in parts.iter().enumerate() {
        let (sb, entries) =
            crate::private_macos::status(p).map_err(|e| format!("{}: {e}", p.display()))?;
        let kind = sb.st_mode & libc::S_IFMT;
        if kind == libc::S_IFLNK {
            return refuse(p, "is a link");
        }
        if !trusted_owner(sb.st_uid) {
            return refuse(p, "belongs to another user");
        }
        let folder = i != file;
        if !folder {
            if kind != libc::S_IFREG {
                return refuse(p, "is not a file");
            }
            if others_write(&sb) {
                return refuse(p, "may be written by other users");
            }
        } else {
            if kind != libc::S_IFDIR {
                return refuse(p, "is not a folder");
            }
            if others_write(&sb) && !admins_write(&sb) {
                let sticky = sb.st_mode & 0o1000 != 0;
                let below = std::fs::symlink_metadata(parts[i + 1]).map_err(|e| e.to_string())?;
                if !(sticky && trusted_owner(below.uid())) {
                    return refuse(p, "may be changed by other users");
                }
            }
        }
        let group = if folder { admin } else { None };
        if let Some(e) = acl_refusal(p, &entries, folder, me, group) {
            return Err(e);
        }
    }
    Ok(())
}

/// Why `secure` refuses path for an entry of its access list (a file's, or with folder a
/// folder's) that lets a user other than root and me change it, or None. Only an allow entry
/// can: one that holds any right outside `READ_ONLY`, a bit no one knows of yet too, for anyone
/// but root or me fails, except one for the group admin (given only for a folder when writers is
/// `Admins`, as for its mode), whatever rights it holds. A deny entry never does (a home folder
/// carries `group:everyone deny delete`), nor an entry that applies only to what is made inside
/// its folder: the program and each folder carry their own, inherited, copy of what reaches them,
/// judged like an entry set on them. An allow entry fails even where an earlier deny entry takes
/// the same right away; a folder's `delete` lets that user, below a sticky folder anyone may add
/// to, move the folder aside and put their own in its place; and an `OWNER@` entry (from a
/// network volume) fails too: its UUID reads as a group (number 10), not as the file's owner.
#[cfg(target_os = "macos")]
fn acl_refusal(
    path: &std::path::Path,
    entries: &[crate::private_macos::Entry],
    folder: bool,
    me: u32,
    admin: Option<u32>,
) -> Option<String> {
    use crate::private_macos::{Subject, FILE_CHANGES, FOLDER_CHANGES, READ_ONLY};
    let names = if folder { FOLDER_CHANGES } else { FILE_CHANGES };
    entries.iter().find_map(|e| {
        if !e.allow || e.only_inherit {
            return None;
        }
        let trusted = match e.subject {
            Subject::User(uid) => uid == 0 || uid == me,
            Subject::Group(gid) => admin == Some(gid),
            Subject::Unknown(_) => false,
        };
        let changes = e.rights & !READ_ONLY;
        if trusted || changes == 0 {
            return None;
        }
        let named = names.iter().fold(0, |all, (bit, _)| all | bit);
        let rights: Vec<String> = names
            .iter()
            .filter(|(bit, _)| changes & bit != 0)
            .map(|(_, name)| name.to_string())
            .chain(
                (0..64)
                    .filter(|n| changes & !named & (1u64 << n) != 0)
                    .map(|n| format!("bit {n}")),
            )
            .collect();
        let p = shell_quoted(path);
        Some(format!(
            "{} has an access list entry, #{i}, that lets {who} change it ({rights}): {BY_PATH}; remove that entry with `chmod -a# {i} {p}`, or every entry with `chmod -N {p}`",
            path.display(),
            i = e.index,
            who = e.subject,
            rights = rights.join(","),
        ))
    })
}

/// path as one word for a POSIX shell: in single quotes, each `'` in it written `'\''`.
#[cfg(any(target_os = "macos", test))]
pub(crate) fn shell_quoted(path: &std::path::Path) -> String {
    format!("'{}'", path.display().to_string().replace('\'', r"'\''"))
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
