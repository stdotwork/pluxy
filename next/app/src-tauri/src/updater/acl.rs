//! macOS: the extended access lists of the folders an update goes through, judged by the rights
//! an entry gives another account rather than refused for having any entry. On macOS a list is
//! read before the mode bits and is independent of them, so a folder of this user's with no group
//! or other write can still let another account add, remove or rename what is in it. Stock home
//! folders carry deny entries (`group:everyone deny delete` on `~`, `~/Library` and
//! `~/Library/Application Support`), which take rights away and are fine.
//!
//! An allow entry is fine only when it is for one of the accounts allowed (this user and root)
//! or gives nothing beyond reading (`READ_ONLY`): every other right, the kernel's generic write
//! and generic all and a bit no one knows of today included, counts as a change. Entries that
//! apply only to what is made inside (inherit-only) count too: they reach what is made there
//! later. Qualifiers are compared as the UUIDs they are stored as, never as numbers, which would
//! take group 0 for user 0.
//!
//! On Linux, the tests' build, nothing is read: a POSIX access list's mask is the group bits, so
//! the modes the callers check already shut every named entry out of writing.
#![cfg_attr(not(target_os = "macos"), allow(dead_code))]

/// The rights an allow entry may give someone else and still let them change nothing: read or
/// list (1), execute or search (3), readattr (7), readextattr (9), readsecurity (11),
/// synchronize (20), and the kernel's generic execute (22) and generic read (24), which it
/// expands to execute and to the four read rights. Any other bit, one no one knows of today too,
/// counts as a change.
pub(super) const READ_ONLY: u64 =
    1 << 1 | 1 << 3 | 1 << 7 | 1 << 9 | 1 << 11 | 1 << 20 | 1 << 22 | 1 << 24;

/// One entry of an extended access list, with its qualifier as the UUID it is stored as.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) struct Entry {
    /// An allow entry; anything else is a deny entry.
    pub allow: bool,
    pub who: [u8; 16],
    pub rights: u64,
    /// It applies only to what is made inside the folder.
    pub only_inherit: bool,
}

/// The first allow entry of entries that gives someone not in allowed a right beyond reading.
pub(super) fn admits_others<'a>(
    entries: &'a [Entry],
    allowed: &[[u8; 16]],
) -> Option<(usize, &'a Entry)> {
    entries
        .iter()
        .enumerate()
        .find(|(_, e)| e.allow && e.rights & !READ_ONLY != 0 && !allowed.contains(&e.who))
}

/// Why entries let another account than those allowed change what holds them, if they do: which
/// entry, as `ls -le` numbers it.
pub(super) fn others(entries: &[Entry], allowed: &[[u8; 16]]) -> Option<String> {
    admits_others(entries, allowed).map(|(i, e)| {
        format!(
            "entry {i} of its access list (`ls -le` shows it) lets another account change {}",
            if e.only_inherit {
                "what is put in it"
            } else {
                "it or what is in it"
            }
        )
    })
}

#[cfg(target_os = "macos")]
pub(super) use sys::{entries, sized, yours};

/// No extended access lists here (Linux, the tests' build): none to read.
#[cfg(not(target_os = "macos"))]
pub(super) fn entries(_: &std::fs::File) -> std::io::Result<Vec<Entry>> {
    Ok(vec![])
}

/// No extended access lists here: no one to compare their entries with.
#[cfg(not(target_os = "macos"))]
pub(super) fn yours() -> Result<Vec<[u8; 16]>, String> {
    Ok(vec![])
}

#[cfg(target_os = "macos")]
mod sys {
    use super::Entry;
    use std::ffi::c_void;
    use std::fs::File;
    use std::io;
    use std::os::fd::AsRawFd;
    use std::os::raw::c_int;

    /// `acl_type_t`'s `ACL_TYPE_EXTENDED` (`sys/acl.h`).
    const ACL_TYPE_EXTENDED: c_int = 0x0000_0100;
    /// `acl_entry_id_t`'s `ACL_FIRST_ENTRY` and `ACL_NEXT_ENTRY`.
    const ACL_FIRST_ENTRY: c_int = 0;
    const ACL_NEXT_ENTRY: c_int = -1;
    /// `acl_tag_t`'s `ACL_EXTENDED_DENY`; every other tag is taken as allow.
    const ACL_EXTENDED_DENY: c_int = 2;
    /// `acl_flag_t`'s `ACL_ENTRY_ONLY_INHERIT`.
    const ACL_ENTRY_ONLY_INHERIT: c_int = 1 << 8;

    extern "C" {
        fn acl_get_fd_np(fd: c_int, ty: c_int) -> *mut c_void;
        fn acl_get_entry(acl: *mut c_void, entry_id: c_int, entry: *mut *mut c_void) -> c_int;
        fn acl_get_tag_type(entry: *mut c_void, tag: *mut c_int) -> c_int;
        fn acl_get_permset_mask_np(entry: *mut c_void, mask: *mut u64) -> c_int;
        fn acl_get_flagset_np(obj: *mut c_void, flagset: *mut *mut c_void) -> c_int;
        fn acl_get_flag_np(flagset: *mut c_void, flag: c_int) -> c_int;
        fn acl_get_qualifier(entry: *mut c_void) -> *mut c_void;
        fn acl_free(obj: *mut c_void) -> c_int;
        fn mbr_uid_to_uuid(uid: libc::uid_t, uu: *mut u8) -> c_int;
    }

    /// Every entry of the extended access list of what is open as f, inherit-only ones too: none
    /// when it has no list (`ENOENT`). Any other failure is an error.
    pub(in super::super) fn entries(f: &File) -> io::Result<Vec<Entry>> {
        // SAFETY: the calling thread's errno, cleared so a missing list is told from a failure.
        unsafe { *libc::__error() = 0 };
        // SAFETY: f is an open descriptor; a list given back is freed below.
        let acl = unsafe { acl_get_fd_np(f.as_raw_fd(), ACL_TYPE_EXTENDED) };
        if acl.is_null() {
            let e = io::Error::last_os_error();
            return match e.raw_os_error() {
                Some(libc::ENOENT) => Ok(vec![]),
                _ => Err(e),
            };
        }
        let fail = || Err(io::Error::last_os_error());
        let mut out = vec![];
        let mut entry = std::ptr::null_mut();
        let mut which = ACL_FIRST_ENTRY;
        // SAFETY: acl is a valid list; each entry and flagset points into it; the qualifier
        // acl_get_qualifier gives back is freed once; acl is freed once, after the loop.
        let read = unsafe {
            loop {
                if acl_get_entry(acl, which, &mut entry) != 0 {
                    break Ok(());
                }
                which = ACL_NEXT_ENTRY;
                let mut tag = 0;
                let mut rights = 0u64;
                let mut flags = std::ptr::null_mut();
                if acl_get_tag_type(entry, &mut tag) != 0
                    || acl_get_permset_mask_np(entry, &mut rights) != 0
                    || acl_get_flagset_np(entry, &mut flags) != 0
                {
                    break fail();
                }
                let only_inherit = match acl_get_flag_np(flags, ACL_ENTRY_ONLY_INHERIT) {
                    1 => true,
                    0 => false,
                    _ => break fail(),
                };
                let q = acl_get_qualifier(entry);
                if q.is_null() {
                    break fail();
                }
                let mut who = [0u8; 16];
                std::ptr::copy_nonoverlapping(q.cast::<u8>(), who.as_mut_ptr(), 16);
                acl_free(q);
                out.push(Entry {
                    allow: tag != ACL_EXTENDED_DENY,
                    who,
                    rights,
                    only_inherit,
                });
            }
        };
        // SAFETY: acl came from acl_get_fd_np and is freed once.
        unsafe { acl_free(acl) };
        read.map(|()| out)
    }

    /// Runs call with a buffer of 16 KiB, doubled while call says it is too small (`ERANGE`), up
    /// to 1 MiB, and gives back what call last returned (0 or an error number): for the `get*_r`
    /// lookups.
    pub(in super::super) fn sized(mut call: impl FnMut(&mut [libc::c_char]) -> c_int) -> c_int {
        let mut len = 16 << 10;
        loop {
            let mut buf = vec![0 as libc::c_char; len];
            let r = call(&mut buf);
            if r != libc::ERANGE || len >= 1 << 20 {
                return r;
            }
            len *= 2;
        }
    }

    /// The UUIDs of the accounts whose entries may give any right: this user and root, in that
    /// order. Refused when this user has no account (`getpwuid_r`), since `mbr_uid_to_uuid` makes
    /// one up for an unknown user, or when a lookup fails.
    pub(in super::super) fn yours() -> Result<Vec<[u8; 16]>, String> {
        // SAFETY: geteuid has no preconditions.
        let me = unsafe { libc::geteuid() };
        let mut known = false;
        // SAFETY: getpwuid_r writes only into pw and buf; found points into them or is null.
        let r = sized(|buf| unsafe {
            let mut pw: libc::passwd = std::mem::zeroed();
            let mut found: *mut libc::passwd = std::ptr::null_mut();
            let r = libc::getpwuid_r(me, &mut pw, buf.as_mut_ptr(), buf.len(), &mut found);
            known = r == 0 && !found.is_null();
            r
        });
        if r != 0 {
            return Err(format!(
                "looking up user {me}: {}",
                io::Error::from_raw_os_error(r)
            ));
        }
        if !known {
            return Err(format!("user {me} has no account"));
        }
        let uuid = |uid: libc::uid_t| {
            let mut u = [0u8; 16];
            // SAFETY: u is 16 writable bytes.
            match unsafe { mbr_uid_to_uuid(uid, u.as_mut_ptr()) } {
                0 => Ok(u),
                e => Err(format!(
                    "user {uid}'s UUID: {}",
                    io::Error::from_raw_os_error(e)
                )),
            }
        };
        Ok(vec![uuid(me)?, uuid(0)?])
    }
}

#[cfg(test)]
pub(super) mod tests {
    use super::*;

    pub(in super::super) const ME: [u8; 16] = [7; 16];
    pub(in super::super) const ROOT: [u8; 16] = [
        0xFF, 0xFF, 0xEE, 0xEE, 0xDD, 0xDD, 0xCC, 0xCC, 0xBB, 0xBB, 0xAA, 0xAA, 0, 0, 0, 0,
    ];
    /// The wheel group (gid 0): a qualifier that a number-based check would take for root.
    pub(in super::super) const WHEEL: [u8; 16] = [
        0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0, 0, 0, 0,
    ];
    pub(in super::super) const STAFF: [u8; 16] = [
        0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0, 0, 0, 0x14,
    ];
    pub(in super::super) const EVERYONE: [u8; 16] = [
        0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0, 0, 0, 0x0C,
    ];

    // acl_perm_t's bits for a folder (sys/acl.h), and the kernel's generic ones (kauth.h).
    pub(in super::super) const LIST: u64 = 1 << 1;
    pub(in super::super) const ADD_FILE: u64 = 1 << 2;
    pub(in super::super) const SEARCH: u64 = 1 << 3;
    pub(in super::super) const DELETE: u64 = 1 << 4;
    pub(in super::super) const ADD_SUBDIRECTORY: u64 = 1 << 5;
    pub(in super::super) const DELETE_CHILD: u64 = 1 << 6;
    pub(in super::super) const READATTR: u64 = 1 << 7;
    pub(in super::super) const WRITEATTR: u64 = 1 << 8;
    const READEXTATTR: u64 = 1 << 9;
    pub(in super::super) const WRITEEXTATTR: u64 = 1 << 10;
    const READSECURITY: u64 = 1 << 11;
    pub(in super::super) const WRITESECURITY: u64 = 1 << 12;
    pub(in super::super) const CHOWN: u64 = 1 << 13;
    const SYNCHRONIZE: u64 = 1 << 20;
    pub(in super::super) const GENERIC_ALL: u64 = 1 << 21;
    const GENERIC_EXECUTE: u64 = 1 << 22;
    pub(in super::super) const GENERIC_WRITE: u64 = 1 << 23;
    const GENERIC_READ: u64 = 1 << 24;

    pub(in super::super) fn entry(allow: bool, who: [u8; 16], rights: u64, only: bool) -> Entry {
        Entry {
            allow,
            who,
            rights,
            only_inherit: only,
        }
    }

    // An allow entry for anyone but this user or root that gives a right beyond reading is
    // refused, inherit-only ones too; deny entries (the stock `everyone deny delete`), read-only
    // allow entries and this user's or root's own entries are fine.
    #[test]
    fn entries_are_judged_by_who_and_what_they_allow() {
        let yours = [ME, ROOT];
        let ok = |e: Vec<Entry>| assert_eq!(others(&e, &yours), None, "{e:?}");
        ok(vec![]);
        ok(vec![entry(false, EVERYONE, DELETE, false)]);
        ok(vec![entry(false, EVERYONE, ADD_FILE | DELETE_CHILD, true)]);
        ok(vec![entry(
            true,
            EVERYONE,
            LIST | SEARCH | READATTR | READEXTATTR | READSECURITY | SYNCHRONIZE,
            false,
        )]);
        ok(vec![entry(
            true,
            STAFF,
            GENERIC_READ | GENERIC_EXECUTE,
            true,
        )]);
        ok(vec![entry(true, ME, GENERIC_ALL, false)]);
        ok(vec![entry(true, ROOT, ADD_FILE | CHOWN, true)]);
        for right in [
            ADD_FILE,
            DELETE,
            ADD_SUBDIRECTORY,
            DELETE_CHILD,
            WRITEATTR,
            WRITEEXTATTR,
            WRITESECURITY,
            CHOWN,
            GENERIC_ALL,
            GENERIC_WRITE,
            1 << 30,
        ] {
            for only in [false, true] {
                let e = vec![
                    entry(false, EVERYONE, DELETE, false),
                    entry(true, STAFF, LIST | right, only),
                ];
                let why = others(&e, &yours).unwrap_or_else(|| panic!("{right:#x} {only}"));
                assert!(why.starts_with("entry 1 of its access list"), "{why}");
                assert_eq!(why.ends_with("what is put in it"), only, "{why}");
            }
        }
        // Group 0 is not root: compared by UUID, its entry counts.
        assert!(others(&[entry(true, WHEEL, ADD_FILE, false)], &yours).is_some());
        // Nothing allowed (a lookup gave no one): any write entry counts, even this user's.
        assert!(others(&[entry(true, ME, ADD_FILE, false)], &[]).is_some());
    }
}
