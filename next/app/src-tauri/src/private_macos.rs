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
//! `status` reads a path's mode and each entry of its list in one call, for the macOS program check
//! (`owned::secure`), which judges the entries by what they allow rather than refusing any.
//!
//! Files and folders already inside a folder when it is emptied keep what they had. Each check
//! and change acts on an open descriptor, opened as the caller's mode check opens it: the vault,
//! the instance folder, the settings folder and what is inside the app's core folder are opened
//! without following a link; Access's store, the cores store and the device store open their
//! folder by its path, following a link as their mode change does, and make their files in it by
//! path. libc has no bindings for these calls: they are libSystem's (acl(3), `acl_get_fd_np`,
//! `acl_set_fd_np`, filesec(3), `openx_np`, `lstatx_np`, membership(3)), declared here.

use std::ffi::c_void;
use std::fs::File;
use std::io;
use std::os::fd::{AsRawFd, FromRawFd};
use std::os::raw::c_int;

/// `acl_type_t`'s `ACL_TYPE_EXTENDED`, the only type macOS has (`sys/acl.h`).
const ACL_TYPE_EXTENDED: c_int = 0x0000_0100;
/// `acl_entry_id_t`'s `ACL_FIRST_ENTRY` and `ACL_NEXT_ENTRY`.
const ACL_FIRST_ENTRY: c_int = 0;
const ACL_NEXT_ENTRY: c_int = -1;
/// `acl_tag_t`'s `ACL_EXTENDED_ALLOW`.
const ACL_EXTENDED_ALLOW: c_int = 1;
/// `acl_flag_t`'s `ACL_ENTRY_ONLY_INHERIT`: an entry that applies only to what is made inside.
const ACL_ENTRY_ONLY_INHERIT: c_int = 1 << 8;
/// membership(3)'s `ID_TYPE_UID` and `ID_TYPE_GID`.
const ID_TYPE_UID: c_int = 0;
const ID_TYPE_GID: c_int = 1;
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
    fn acl_get_flag_np(flagset: *mut c_void, flag: c_int) -> c_int;
    fn acl_get_tag_type(entry: *mut c_void, tag: *mut c_int) -> c_int;
    fn acl_get_permset_mask_np(entry: *mut c_void, mask: *mut u64) -> c_int;
    fn acl_get_qualifier(entry: *mut c_void) -> *mut c_void;
    fn mbr_uuid_to_id(uu: *const u8, id: *mut libc::id_t, id_type: *mut c_int) -> c_int;
    fn acl_free(obj: *mut c_void) -> c_int;
    fn filesec_init() -> *mut c_void;
    fn filesec_set_property(fsec: *mut c_void, property: c_int, value: *const c_void) -> c_int;
    fn filesec_get_property(fsec: *mut c_void, property: c_int, value: *mut c_void) -> c_int;
    #[cfg_attr(target_arch = "x86_64", link_name = "lstatx_np$INODE64")]
    fn lstatx_np(
        path: *const std::os::raw::c_char,
        sb: *mut libc::stat,
        fsec: *mut c_void,
    ) -> c_int;
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

/// Who an access list entry is for: a user or a group by number (`mbr_uuid_to_id`), or its UUID
/// when that lookup fails. A UUID Directory Services does not know (a removed account) still gets
/// a user number, a temporary one, so it shows as `user:<n> (no name)`, not as `Unknown`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Subject {
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
pub struct Entry {
    /// Its place in the list, from 0, as `ls -le` shows it and `chmod -a#` takes it.
    pub index: usize,
    /// An allow entry; otherwise a deny entry.
    pub allow: bool,
    pub subject: Subject,
    /// Every right bit it holds, as stored (`acl_get_permset_mask_np`): `acl_perm_t`'s
    /// (`sys/acl.h`), and also the kernel's generic rights (`kauth.h`, `KAUTH_ACE_GENERIC_*`),
    /// which `chmod +a` cannot write but the kernel accepts and expands as it checks access.
    pub rights: u64,
    /// It applies only to what is made inside its folder, not to the folder itself.
    pub only_inherit: bool,
}

/// The rights an entry may hold and still let no one change anything: read (1), execute or
/// search (3), readattr (7), readextattr (9), readsecurity (11), synchronize (20), and the
/// kernel's generic execute (22) and generic read (24), which it expands to execute and to the
/// four read rights. Any other bit, one no one knows of today too, may change something.
pub const READ_ONLY: u64 =
    1 << 1 | 1 << 3 | 1 << 7 | 1 << 9 | 1 << 11 | 1 << 20 | 1 << 22 | 1 << 24;

/// `chmod +a`'s names of the rights that change a file, and the kernel's generic write (23) and
/// generic all (21), which it expands to write, append, delete, delete_child, writeattr,
/// writeextattr and writesecurity (`kauth.h`, `KAUTH_VNODE_GENERIC_WRITE_BITS`).
pub const FILE_CHANGES: &[(u64, &str)] = &[
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
pub const FOLDER_CHANGES: &[(u64, &str)] = &[
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
pub fn status(path: &std::path::Path) -> io::Result<(libc::stat, Vec<Entry>)> {
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

/// Tests: gives path, which must have no access list yet, the one entry `<subject> allow`
/// with exactly the right bits rights, any of them, `chmod +a`'s names or not (the kernel's
/// generic rights). It adds `<subject> allow read` with macOS's own `chmod`, takes the list in the
/// form the kernel reads (`FILESEC_ACL_RAW`), sets that entry's rights there and gives it back to
/// the kernel (`chmodx_np`), which takes any rights.
#[cfg(test)]
pub(crate) fn set_raw_entry(path: &std::path::Path, subject: &str, rights: u32) {
    use std::os::unix::ffi::OsStrExt;
    extern "C" {
        fn acl_get_link_np(path: *const std::os::raw::c_char, ty: c_int) -> *mut c_void;
        fn chmodx_np(path: *const std::os::raw::c_char, fsec: *mut c_void) -> c_int;
    }
    /// `FILESEC_ACL_RAW` (`sys/fcntl.h`).
    const FILESEC_ACL_RAW: c_int = 100;
    /// In `struct kauth_filesec` (`sys/kauth.h`): the magic number, two 16-byte GUIDs, then the
    /// list's entry count and flags; each entry is a 16-byte GUID, its flags and its rights.
    const COUNT: usize = 4 + 16 + 16;
    const FIRST_RIGHTS: usize = COUNT + 4 + 4 + 16 + 4;
    assert_eq!(status(path).unwrap().1, Vec::new(), "{}", path.display());
    add_entry(path, &format!("{subject} allow read"));
    let p = std::ffi::CString::new(path.as_os_str().as_bytes()).unwrap();
    // SAFETY: each pointer is checked before use; acl and fsec are freed once; raw is fsec's
    // buffer, of at least one entry as its count says, written in place before fsec is freed.
    unsafe {
        let acl = acl_get_link_np(p.as_ptr(), ACL_TYPE_EXTENDED);
        assert!(
            !acl.is_null(),
            "{}: {}",
            path.display(),
            io::Error::last_os_error()
        );
        let fsec = filesec_init();
        assert!(!fsec.is_null());
        assert_eq!(
            filesec_set_property(fsec, FILESEC_ACL, (&acl as *const *mut c_void).cast()),
            0
        );
        acl_free(acl);
        let mut raw: *mut u8 = std::ptr::null_mut();
        assert_eq!(
            filesec_get_property(fsec, FILESEC_ACL_RAW, (&mut raw as *mut *mut u8).cast()),
            0
        );
        assert!(!raw.is_null());
        assert_eq!(
            raw.add(COUNT).cast::<u32>().read_unaligned(),
            1,
            "one entry"
        );
        let at = raw.add(FIRST_RIGHTS).cast::<u32>();
        assert_eq!(at.read_unaligned(), 1 << 1, "the entry's rights are read");
        at.write_unaligned(rights);
        let set = chmodx_np(p.as_ptr(), fsec);
        let e = io::Error::last_os_error();
        filesec_free(fsec);
        assert_eq!(set, 0, "{}: {e}", path.display());
    }
    let entries = status(path).unwrap().1;
    assert_eq!(entries.len(), 1, "{}", path.display());
    assert_eq!(entries[0].rights, u64::from(rights), "{}", path.display());
}

#[cfg(test)]
#[path = "private_macos_tests.rs"]
mod tests;
