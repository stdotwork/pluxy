//! macOS: what the window of a terminal launch runs (`launch.rs`, `attach`): the file the app
//! opened and checked, by the path it has now, under the rule the app holds a launch's program to
//! (`exec_target`, and `secure` with `Writers::Admins` and its `acl_refusal`, in
//! `next/app/src-tauri/src/owned/unix.rs`; `status` and what it reads, in
//! `next/app/src-tauri/src/private_macos.rs`). This ports them: the app tools never link the app,
//! so keep the two in step.
//!
//! libc has no bindings for the access list calls: they are libSystem's (acl(3), filesec(3),
//! `lstatx_np`, membership(3)), declared here.

use std::ffi::c_void;
use std::fs::File;
use std::io;
use std::os::raw::c_int;
use std::os::unix::fs::MetadataExt;
use std::os::unix::io::AsRawFd;
use std::path::{Path, PathBuf};

/// `acl_entry_id_t`'s `ACL_FIRST_ENTRY` and `ACL_NEXT_ENTRY` (`sys/acl.h`).
const ACL_FIRST_ENTRY: c_int = 0;
const ACL_NEXT_ENTRY: c_int = -1;
/// `acl_tag_t`'s `ACL_EXTENDED_ALLOW`.
const ACL_EXTENDED_ALLOW: c_int = 1;
/// `acl_flag_t`'s `ACL_ENTRY_ONLY_INHERIT`: an entry that applies only to what is made inside.
const ACL_ENTRY_ONLY_INHERIT: c_int = 1 << 8;
/// membership(3)'s `ID_TYPE_UID` and `ID_TYPE_GID`.
const ID_TYPE_UID: c_int = 0;
const ID_TYPE_GID: c_int = 1;
/// `filesec_property_t`'s `FILESEC_ACL` (`sys/fcntl.h`).
const FILESEC_ACL: c_int = 5;

extern "C" {
    fn acl_get_entry(acl: *mut c_void, entry_id: c_int, entry: *mut *mut c_void) -> c_int;
    fn acl_get_flagset_np(obj: *mut c_void, flagset: *mut *mut c_void) -> c_int;
    fn acl_get_flag_np(flagset: *mut c_void, flag: c_int) -> c_int;
    fn acl_get_tag_type(entry: *mut c_void, tag: *mut c_int) -> c_int;
    fn acl_get_permset_mask_np(entry: *mut c_void, mask: *mut u64) -> c_int;
    fn acl_get_qualifier(entry: *mut c_void) -> *mut c_void;
    fn mbr_uuid_to_id(uu: *const u8, id: *mut libc::id_t, id_type: *mut c_int) -> c_int;
    fn acl_free(obj: *mut c_void) -> c_int;
    fn filesec_init() -> *mut c_void;
    fn filesec_get_property(fsec: *mut c_void, property: c_int, value: *mut c_void) -> c_int;
    #[cfg_attr(target_arch = "x86_64", link_name = "lstatx_np$INODE64")]
    fn lstatx_np(
        path: *const std::os::raw::c_char,
        sb: *mut libc::stat,
        fsec: *mut c_void,
    ) -> c_int;
    fn filesec_free(fsec: *mut c_void);
}

/// What runs the opened file f: macOS cannot execute a descriptor, so the path f has now
/// (`F_GETPATH`, links resolved), once only root, this user and the `admin` group can change any
/// folder on it, and only root and this user the file, by its mode or its access list (`secure`),
/// and that path still names f (the same device and inode, with the size, mode and times f has
/// now). A file put at the path f was opened from never runs: f runs by its own path, wherever it
/// was renamed to, or is refused. Only a process of this user, or of an admin account, could swap
/// it between this check and the exec.
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

/// Why `secure` refuses a program.
const BY_PATH: &str =
    "on macOS the window runs a program by its path, so no other user may be able to change it";

/// Refused unless no user but root, this one and the `admin` group's members can change path or
/// any folder above it: each is owned by root or this user and writable by neither its group nor
/// others, except a folder writable by the `admin` group (and not others), as macOS makes
/// `/Applications` and Homebrew its folders, and a sticky folder (`/tmp`), where only the owner
/// of the entry below it (root or this user) can rename or remove it. No part of path may be a
/// link (it is `F_GETPATH`'s, which resolves them).
///
/// An extended access list can let others in whatever the mode, so each part's entries,
/// inherited ones too, are judged by what they allow (`acl_refusal`). Each part's mode and list
/// are read together (`status`), from `/` down to the file: a folder is read before what is below
/// it, so a user who could swap something below it is refused for that folder, or, should they
/// give up that right first, the swap is read as it is. A part that is not there is an error.
pub fn secure(path: &Path) -> Result<(), String> {
    // SAFETY: geteuid has no preconditions.
    let me = unsafe { libc::geteuid() };
    let admin = admin_gid();
    let trusted_owner = |uid: u32| uid == 0 || uid == me;
    let others_write = |sb: &libc::stat| sb.st_mode & 0o022 != 0;
    let admins_write =
        |sb: &libc::stat| sb.st_mode & 0o002 == 0 && admin.is_some_and(|g| sb.st_gid == g);
    let refuse = |p: &Path, why: &str| Err(format!("{} {why}: {BY_PATH}", p.display()));
    if !path.is_absolute() {
        return refuse(path, "is not a full path");
    }
    let mut parts: Vec<&Path> = path.ancestors().collect();
    parts.reverse();
    let file = parts.len() - 1;
    // From `/` down to the file; the entry below each folder is the one after it here.
    for (i, p) in parts.iter().enumerate() {
        let (sb, entries) = status(p).map_err(|e| format!("{}: {e}", p.display()))?;
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
/// but root or me fails, except one for the group admin (given only for a folder, as for its
/// mode), whatever rights it holds. A deny entry never does (a home folder carries
/// `group:everyone deny delete`), nor an entry that applies only to what is made inside its
/// folder: the program and each folder carry their own, inherited, copy of what reaches them,
/// judged like an entry set on them. An allow entry fails even where an earlier deny entry takes
/// the same right away; a folder's `delete` lets that user, below a sticky folder anyone may add
/// to, move the folder aside and put their own in its place; and an `OWNER@` entry (from a
/// network volume) fails too: its UUID reads as a group (number 10), not as the file's owner.
fn acl_refusal(
    path: &Path,
    entries: &[Entry],
    folder: bool,
    me: u32,
    admin: Option<u32>,
) -> Option<String> {
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
fn shell_quoted(path: &Path) -> String {
    format!("'{}'", path.display().to_string().replace('\'', r"'\''"))
}

/// Who an access list entry is for: a user or a group by number (`mbr_uuid_to_id`), or its UUID
/// when that lookup fails, as it does for a UUID no account has (`unknown <UUID>`). A user number
/// with no name shows as `user:<n> (no name)`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Subject {
    User(u32),
    Group(u32),
    Unknown([u8; 16]),
}

impl std::fmt::Display for Subject {
    /// As `chmod +a` writes it: `user:name` or `group:name`, the number when there is no name.
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match *self {
            Subject::User(uid) => match name(uid, false) {
                Some(n) => write!(f, "user:{n}"),
                None => write!(f, "user:{uid} (no name)"),
            },
            Subject::Group(gid) => match name(gid, true) {
                Some(n) => write!(f, "group:{n}"),
                None => write!(f, "group:{gid} (no name)"),
            },
            Subject::Unknown(u) => {
                write!(f, "unknown ")?;
                for (i, b) in u.iter().enumerate() {
                    if matches!(i, 4 | 6 | 8 | 10) {
                        write!(f, "-")?;
                    }
                    write!(f, "{b:02X}")?;
                }
                Ok(())
            }
        }
    }
}

/// The name of user or group id, if it has one.
fn name(id: u32, group: bool) -> Option<String> {
    let mut buf = vec![0 as std::os::raw::c_char; 16 << 10];
    // SAFETY: each call writes only into its record and buf, and found points into them or is
    // null; the name is copied out before they are dropped.
    unsafe {
        let ptr = if group {
            let mut g: libc::group = std::mem::zeroed();
            let mut found: *mut libc::group = std::ptr::null_mut();
            let r = libc::getgrgid_r(id, &mut g, buf.as_mut_ptr(), buf.len(), &mut found);
            if r != 0 || found.is_null() {
                return None;
            }
            g.gr_name
        } else {
            let mut u: libc::passwd = std::mem::zeroed();
            let mut found: *mut libc::passwd = std::ptr::null_mut();
            let r = libc::getpwuid_r(id, &mut u, buf.as_mut_ptr(), buf.len(), &mut found);
            if r != 0 || found.is_null() {
                return None;
            }
            u.pw_name
        };
        (!ptr.is_null()).then(|| std::ffi::CStr::from_ptr(ptr).to_string_lossy().into_owned())
    }
}

/// One entry of an extended access list.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct Entry {
    /// Its place in the list, from 0, as `ls -le` shows it and `chmod -a#` takes it.
    index: usize,
    /// An allow entry; otherwise a deny entry.
    allow: bool,
    subject: Subject,
    /// Every right bit it holds, as stored (`acl_get_permset_mask_np`): `acl_perm_t`'s
    /// (`sys/acl.h`), and also the kernel's generic rights (`kauth.h`, `KAUTH_ACE_GENERIC_*`),
    /// which `chmod +a` cannot write but the kernel accepts and expands as it checks access.
    rights: u64,
    /// It applies only to what is made inside its folder, not to the folder itself.
    only_inherit: bool,
}

/// The rights an entry may hold and still let no one change anything: read (1), execute or
/// search (3), readattr (7), readextattr (9), readsecurity (11), synchronize (20), and the
/// kernel's generic execute (22) and generic read (24), which it expands to execute and to the
/// four read rights. Any other bit, one no one knows of today too, may change something.
const READ_ONLY: u64 = 1 << 1 | 1 << 3 | 1 << 7 | 1 << 9 | 1 << 11 | 1 << 20 | 1 << 22 | 1 << 24;

/// `chmod +a`'s names of the rights that change a file, and the kernel's generic write (23) and
/// generic all (21), which it expands to write, append, delete, delete_child, writeattr,
/// writeextattr and writesecurity (`kauth.h`, `KAUTH_VNODE_GENERIC_WRITE_BITS`).
const FILE_CHANGES: &[(u64, &str)] = &[
    (1 << 2, "write"),
    (1 << 5, "append"),
    (1 << 8, "writeattr"),
    (1 << 10, "writeextattr"),
    (1 << 12, "writesecurity"),
    (1 << 13, "chown"),
    (1 << 4, "delete"),
    (1 << 6, "delete_child"),
    (1 << 23, "generic_write"),
    (1 << 21, "generic_all"),
];

/// The same for a folder, whose bits 2, 5 and 6 add or remove what it holds.
const FOLDER_CHANGES: &[(u64, &str)] = &[
    (1 << 2, "add_file"),
    (1 << 5, "add_subdirectory"),
    (1 << 6, "delete_child"),
    (1 << 12, "writesecurity"),
    (1 << 13, "chown"),
    (1 << 4, "delete"),
    (1 << 8, "writeattr"),
    (1 << 10, "writeextattr"),
    (1 << 23, "generic_write"),
    (1 << 21, "generic_all"),
];

/// The status of path itself, never through a link, and every entry of its extended access
/// list, inherited ones too, read together by one `lstatx_np`: none when it has no list (as on a
/// volume that keeps none). A path that is not there is an error, never an empty list.
fn status(path: &Path) -> io::Result<(libc::stat, Vec<Entry>)> {
    use std::os::unix::ffi::OsStrExt;
    let p = std::ffi::CString::new(path.as_os_str().as_bytes())
        .map_err(|_| io::Error::from(io::ErrorKind::InvalidInput))?;
    // SAFETY: a new file security description, freed below.
    let fsec = unsafe { filesec_init() };
    if fsec.is_null() {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: p is a C string, sb a stat to fill and fsec valid; a list given back by
    // filesec_get_property is a copy, freed below.
    let read = unsafe {
        let mut sb: libc::stat = std::mem::zeroed();
        if lstatx_np(p.as_ptr(), &mut sb, fsec) != 0 {
            Err(io::Error::last_os_error())
        } else {
            let mut acl: *mut c_void = std::ptr::null_mut();
            if filesec_get_property(fsec, FILESEC_ACL, (&mut acl as *mut *mut c_void).cast()) != 0 {
                let e = io::Error::last_os_error();
                match e.raw_os_error() {
                    Some(libc::ENOENT) => Ok((sb, Vec::new())),
                    _ => Err(e),
                }
            } else {
                let entries = read_entries(acl);
                acl_free(acl);
                entries.map(|e| (sb, e))
            }
        }
    };
    // SAFETY: fsec came from filesec_init and is freed once.
    unsafe { filesec_free(fsec) };
    read
}

/// Each entry of acl, a list from acl(3).
fn read_entries(acl: *mut c_void) -> io::Result<Vec<Entry>> {
    let fail = || Err(io::Error::last_os_error());
    let mut out = Vec::new();
    let mut entry = std::ptr::null_mut();
    let mut which = ACL_FIRST_ENTRY;
    // SAFETY: acl is a valid list; each entry, permset and flagset points into it, and the
    // qualifier acl_get_qualifier gives back is freed once.
    unsafe {
        while acl_get_entry(acl, which, &mut entry) == 0 {
            which = ACL_NEXT_ENTRY;
            let mut tag = 0;
            if acl_get_tag_type(entry, &mut tag) != 0 {
                return fail();
            }
            let mut rights = 0u64;
            if acl_get_permset_mask_np(entry, &mut rights) != 0 {
                return fail();
            }
            let mut flags = std::ptr::null_mut();
            if acl_get_flagset_np(entry, &mut flags) != 0 {
                return fail();
            }
            let only_inherit = match acl_get_flag_np(flags, ACL_ENTRY_ONLY_INHERIT) {
                1 => true,
                0 => false,
                _ => return fail(),
            };
            let q = acl_get_qualifier(entry);
            if q.is_null() {
                return fail();
            }
            let mut uuid = [0u8; 16];
            std::ptr::copy_nonoverlapping(q.cast::<u8>(), uuid.as_mut_ptr(), 16);
            acl_free(q);
            let mut id: libc::id_t = 0;
            let mut kind: c_int = -1;
            let subject = match (mbr_uuid_to_id(uuid.as_ptr(), &mut id, &mut kind), kind) {
                (0, ID_TYPE_UID) => Subject::User(id),
                (0, ID_TYPE_GID) => Subject::Group(id),
                _ => Subject::Unknown(uuid),
            };
            out.push(Entry {
                index: out.len(),
                allow: tag == ACL_EXTENDED_ALLOW,
                subject,
                rights,
                only_inherit,
            });
        }
    }
    Ok(out)
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
