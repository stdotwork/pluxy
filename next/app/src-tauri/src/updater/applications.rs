//! macOS: the rule that admits a bundle in the stock `/Applications` (README.md, "Application
//! update"). That folder is root's, its group `admin` may write it (`drwxrwxr-x root admin`), so
//! the bundle's folder fails the rule every other folder must pass (this user's, writable by no
//! one else). It is admitted instead when all of these hold, read through the folder's open
//! descriptor (opened without following a link) and decided by `judge`, which the tests run with
//! the values given:
//!
//! - its path is exactly `/Applications` (the bundle's path, links resolved, has no link on it:
//!   `app::provenance` refuses one);
//! - it is a folder owned by root (uid 0), its group is the one `getgrnam("admin")` names now, and
//!   others cannot write to it (group write is what this rule is for; the sticky bit is fine);
//! - no allow entry of its extended access list gives anyone but root or the `admin` group any
//!   right beyond reading, inherit-only entries included (they reach what is copied in later).
//!   Entries are compared by their UUIDs with root's and `admin`'s, never by numbers, which would
//!   take group 0 for user 0;
//! - the app's data folder, where the update is staged, is on the same volume, so the only change
//!   the update makes in `/Applications` is the swap of the bundle's own entry;
//! - this user is a member of `admin` (`mbr_check_membership`). That is a message, not a guard:
//!   without the group the kernel refuses the swap; the check says so first, with what to do.
//!
//! The bundle itself must still be this user's and writable by no one else, and its own access
//! list may give no one but this user and root more than reading (`check_bundle`).
//!
//! Why that is safe, in short (README.md has each step): the folder's other writers are this Mac's
//! administrators, and any of them can already rename the user's bundle aside and put one of
//! their own at its name, which the user's next start runs. The updater adds nothing to that: it
//! unpacks and checks in the user's private data folder, makes one change in `/Applications` (the
//! swap, relative to the descriptor checked), and never lists or deletes anything there.
//!
//! libc has no bindings for acl(3) or membership(3): the few calls used are declared here.
#![cfg_attr(not(target_os = "macos"), allow(dead_code))]
use std::path::Path;

/// The one folder this rule admits.
pub(super) const APPLICATIONS: &str = "/Applications";

/// The rights an allow entry may give someone other than root or `admin` and still let them
/// change nothing: read or list (1), execute or search (3), readattr (7), readextattr (9),
/// readsecurity (11), synchronize (20), and the kernel's generic execute (22) and generic read
/// (24). Any other bit, one no one knows of today too, counts as a change.
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

/// What the rule reads of the bundle's folder, through its descriptor.
#[derive(Clone, Debug)]
pub(super) struct Folder {
    pub folder: bool,
    pub uid: u32,
    pub gid: u32,
    pub mode: u32,
    pub dev: u64,
    pub entries: Vec<Entry>,
}

/// Who root, `admin` and this user are, looked up now.
#[derive(Clone, Copy, Debug)]
pub(super) struct Ids {
    pub root: [u8; 16],
    pub admin_gid: u32,
    pub admin: [u8; 16],
    /// Whether this user is a member of `admin`.
    pub member: bool,
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

/// Advice for every refusal here.
const MOVE: &str = "Move pluxy to Applications in your home folder (~/Applications) with Finder, \
                    open it from there, and update then.";

/// Whether the bundle's folder at path (as resolved, with no link on it), read as f, admits an
/// update under this rule, with this user and the groups as ids say (an error when a lookup
/// failed), and the app's data folder on the volume data_dev. The refusal says why.
pub(super) fn judge(
    path: &Path,
    f: &Folder,
    ids: Result<Ids, String>,
    data_dev: u64,
) -> Result<(), String> {
    let not = |why: String| {
        Err(format!(
            "{} is not a folder of yours that only you can change, nor the stock Applications \
             folder that only root and this Mac's administrators can change: {why}. {MOVE}",
            path.display()
        ))
    };
    if path.as_os_str().as_encoded_bytes() != APPLICATIONS.as_bytes() {
        return not(format!("only {APPLICATIONS} itself is admitted that way"));
    }
    if !f.folder {
        return not("it is not a folder".into());
    }
    if f.uid != 0 {
        return not(format!("its owner is user {}, not root", f.uid));
    }
    let ids = match ids {
        Ok(ids) => ids,
        Err(e) => return not(format!("who root and the admin group are is unknown ({e})")),
    };
    if f.gid != ids.admin_gid {
        return not(format!(
            "its group is {}, not admin ({})",
            f.gid, ids.admin_gid
        ));
    }
    if f.mode & 0o002 != 0 {
        return not("every account can change it".into());
    }
    if let Some((i, e)) = admits_others(&f.entries, &[ids.root, ids.admin]) {
        return not(format!(
            "entry {i} of its access list (`ls -le` shows it) lets another account change {}",
            if e.only_inherit {
                "what is put in it"
            } else {
                "what is in it"
            }
        ));
    }
    if f.dev != data_dev {
        return not(
            "it is on another volume than the app's data folder, where the update is staged".into(),
        );
    }
    if !ids.member {
        return Err(format!(
            "pluxy is in {APPLICATIONS}, which only administrators can change, and this account \
             is not one. {MOVE}"
        ));
    }
    Ok(())
}

/// The bundle's own access list, read as entries: refused when an allow entry gives anyone but
/// this user (me) and root a right beyond reading.
pub(super) fn judge_bundle(
    bundle: &Path,
    entries: &[Entry],
    me: [u8; 16],
    root: [u8; 16],
) -> Result<(), String> {
    match admits_others(entries, &[me, root]) {
        Some((i, _)) => Err(format!(
            "{} is not a folder of yours that only you can change: entry {i} of its access list \
             (`ls -le` shows it) lets another account change it. {MOVE}",
            bundle.display()
        )),
        None => Ok(()),
    }
}

#[cfg(target_os = "macos")]
pub(super) use sys::{bundle_entries, folder, ids, my_uuid};

#[cfg(target_os = "macos")]
mod sys {
    use super::{Entry, Folder, Ids};
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
        fn mbr_gid_to_uuid(gid: libc::gid_t, uu: *mut u8) -> c_int;
        fn mbr_check_membership(user: *const u8, group: *const u8, ismember: *mut c_int) -> c_int;
    }

    /// The folder open as f: its kind, owner, group, mode and volume (`fstat`) and its extended
    /// access list (`acl_get_fd_np`), both through the descriptor.
    pub(in super::super) fn folder(f: &File) -> io::Result<Folder> {
        use std::os::unix::fs::MetadataExt;
        let m = f.metadata()?;
        Ok(Folder {
            folder: m.is_dir(),
            uid: m.uid(),
            gid: m.gid(),
            mode: m.mode(),
            dev: m.dev(),
            entries: entries(f)?,
        })
    }

    /// The extended access list of the bundle open as f.
    pub(in super::super) fn bundle_entries(f: &File) -> io::Result<Vec<Entry>> {
        entries(f)
    }

    /// Every entry of the extended access list of what is open as f, inherit-only ones too: none
    /// when it has no list (`ENOENT`). Any other failure is an error.
    fn entries(f: &File) -> io::Result<Vec<Entry>> {
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

    /// This user's UUID: refused when the effective user has no account (`getpwuid_r`), since
    /// `mbr_uid_to_uuid` makes one up for an unknown user.
    pub(in super::super) fn my_uuid() -> Result<[u8; 16], String> {
        // SAFETY: geteuid has no preconditions.
        let me = unsafe { libc::geteuid() };
        let mut buf = vec![0 as libc::c_char; 16 << 10];
        // SAFETY: getpwuid_r writes only into pw and buf; found points into them or is null.
        let known = unsafe {
            let mut pw: libc::passwd = std::mem::zeroed();
            let mut found: *mut libc::passwd = std::ptr::null_mut();
            libc::getpwuid_r(me, &mut pw, buf.as_mut_ptr(), buf.len(), &mut found) == 0
                && !found.is_null()
        };
        if !known {
            return Err(format!("user {me} has no account"));
        }
        uuid(|u| unsafe { mbr_uid_to_uuid(me, u) }, &format!("user {me}"))
    }

    /// A UUID from look, which writes it and returns 0 on success.
    fn uuid(look: impl FnOnce(*mut u8) -> c_int, what: &str) -> Result<[u8; 16], String> {
        let mut u = [0u8; 16];
        match look(u.as_mut_ptr()) {
            0 => Ok(u),
            e => Err(format!(
                "{what}'s UUID: {}",
                io::Error::from_raw_os_error(e)
            )),
        }
    }

    /// Root's and `admin`'s UUIDs, `admin`'s group number as `getgrnam` gives it now, and whether
    /// this user is a member of `admin`. Any lookup that fails is an error.
    pub(in super::super) fn ids() -> Result<Ids, String> {
        let mut buf = vec![0 as libc::c_char; 16 << 10];
        let name = c"admin";
        // SAFETY: getgrnam_r writes only into g and buf; found points into them or is null.
        let admin_gid = unsafe {
            let mut g: libc::group = std::mem::zeroed();
            let mut found: *mut libc::group = std::ptr::null_mut();
            let r = libc::getgrnam_r(
                name.as_ptr(),
                &mut g,
                buf.as_mut_ptr(),
                buf.len(),
                &mut found,
            );
            if r != 0 || found.is_null() {
                return Err("there is no group named admin".into());
            }
            g.gr_gid
        };
        // SAFETY (each closure): u points to 16 writable bytes.
        let root = uuid(|u| unsafe { mbr_uid_to_uuid(0, u) }, "root")?;
        let admin = uuid(
            |u| unsafe { mbr_gid_to_uuid(admin_gid, u) },
            "the admin group",
        )?;
        let me = my_uuid()?;
        let mut is = 0;
        // SAFETY: me and admin are 16-byte UUIDs; is is written on success.
        let r = unsafe { mbr_check_membership(me.as_ptr(), admin.as_ptr(), &mut is) };
        if r != 0 {
            return Err(format!(
                "whether this user is in admin: {}",
                io::Error::from_raw_os_error(r)
            ));
        }
        Ok(Ids {
            root,
            admin_gid,
            admin,
            member: is != 0,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const ROOT: [u8; 16] = [
        0xFF, 0xFF, 0xEE, 0xEE, 0xDD, 0xDD, 0xCC, 0xCC, 0xBB, 0xBB, 0xAA, 0xAA, 0, 0, 0, 0,
    ];
    const ADMIN: [u8; 16] = [
        0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0, 0, 0, 0x50,
    ];
    /// The wheel group (gid 0): a qualifier that a number-based check would take for root.
    const WHEEL: [u8; 16] = [
        0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0, 0, 0, 0,
    ];
    const STAFF: [u8; 16] = [
        0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0, 0, 0, 0x14,
    ];
    const EVERYONE: [u8; 16] = [
        0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0, 0, 0, 0x0C,
    ];
    const ME: [u8; 16] = [7; 16];
    const DEV: u64 = 0x0100_0004;

    // acl_perm_t's bits for a folder (sys/acl.h).
    const LIST: u64 = 1 << 1;
    const ADD_FILE: u64 = 1 << 2;
    const SEARCH: u64 = 1 << 3;
    const DELETE: u64 = 1 << 4;
    const ADD_SUBDIRECTORY: u64 = 1 << 5;
    const DELETE_CHILD: u64 = 1 << 6;
    const READATTR: u64 = 1 << 7;
    const WRITEATTR: u64 = 1 << 8;
    const WRITEEXTATTR: u64 = 1 << 10;
    const WRITESECURITY: u64 = 1 << 12;
    const CHOWN: u64 = 1 << 13;
    const GENERIC_ALL: u64 = 1 << 21;
    const GENERIC_WRITE: u64 = 1 << 23;

    fn stock() -> Folder {
        Folder {
            folder: true,
            uid: 0,
            gid: 80,
            mode: 0o40775,
            dev: DEV,
            entries: vec![],
        }
    }

    fn ids() -> Result<Ids, String> {
        Ok(Ids {
            root: ROOT,
            admin_gid: 80,
            admin: ADMIN,
            member: true,
        })
    }

    fn allow(who: [u8; 16], rights: u64) -> Entry {
        Entry {
            allow: true,
            who,
            rights,
            only_inherit: false,
        }
    }

    fn apps() -> &'static Path {
        Path::new(APPLICATIONS)
    }

    /// Refused, with the "not a folder of yours" message saying why.
    fn refused(r: Result<(), String>, why: &str) {
        let e = r.unwrap_err();
        assert!(
            e.contains("is not a folder of yours that only you can change") && e.contains(why),
            "{why}: {e}"
        );
        assert!(e.contains("~/Applications"), "{e}");
    }

    #[test]
    fn the_stock_applications_folder_is_admitted_for_an_admin() {
        judge(apps(), &stock(), ids(), DEV).unwrap();
        // Stricter than stock: no group write, or the sticky bit.
        for mode in [0o40755, 0o41775] {
            judge(apps(), &Folder { mode, ..stock() }, ids(), DEV).unwrap();
        }
        // admin's number is whatever getgrnam says, not 80.
        let other = Ids {
            admin_gid: 501,
            ..ids().unwrap()
        };
        judge(
            apps(),
            &Folder {
                gid: 501,
                ..stock()
            },
            Ok(other),
            DEV,
        )
        .unwrap();
    }

    // Each of the rule's conditions failing on its own refuses, and says which.
    #[test]
    fn each_condition_failing_on_its_own_refuses() {
        for (path, why) in [
            ("/Applications/Utilities", "only /Applications itself"),
            ("/Users/me/Applications", "only /Applications itself"),
            ("/Applications/", "only /Applications itself"),
            ("/private/Applications", "only /Applications itself"),
            ("/applications", "only /Applications itself"),
        ] {
            refused(judge(Path::new(path), &stock(), ids(), DEV), why);
        }
        refused(
            judge(
                apps(),
                &Folder {
                    folder: false,
                    ..stock()
                },
                ids(),
                DEV,
            ),
            "not a folder",
        );
        refused(
            judge(
                apps(),
                &Folder {
                    uid: 501,
                    ..stock()
                },
                ids(),
                DEV,
            ),
            "owner is user 501, not root",
        );
        refused(
            judge(apps(), &Folder { gid: 20, ..stock() }, ids(), DEV),
            "group is 20, not admin (80)",
        );
        // gid 0 is wheel, not admin.
        refused(
            judge(apps(), &Folder { gid: 0, ..stock() }, ids(), DEV),
            "not admin",
        );
        for mode in [0o40777, 0o40776, 0o41777] {
            refused(
                judge(apps(), &Folder { mode, ..stock() }, ids(), DEV),
                "every account can change it",
            );
        }
        refused(judge(apps(), &stock(), ids(), DEV + 1), "another volume");
        refused(
            judge(
                apps(),
                &stock(),
                Err("there is no group named admin".into()),
                DEV,
            ),
            "there is no group named admin",
        );
        // Membership: the administrators' message, not "not a folder of yours".
        let not_admin = Ids {
            member: false,
            ..ids().unwrap()
        };
        let e = judge(apps(), &stock(), Ok(not_admin), DEV).unwrap_err();
        assert!(
            e.starts_with(
                "pluxy is in /Applications, which only administrators can change, and this \
                 account is not one. Move pluxy to Applications in your home folder"
            ),
            "{e}"
        );
    }

    // An allow entry giving anyone but root or admin a right beyond reading refuses, inherit-only
    // entries too; deny entries, read-only entries and root's or admin's entries do not.
    #[test]
    fn access_list_entries_are_judged_by_who_and_what_they_allow() {
        let with = |entries: Vec<Entry>| judge(apps(), &Folder { entries, ..stock() }, ids(), DEV);
        // Fine: the usual deny entry, read-only entries, and root's and admin's.
        with(vec![Entry {
            allow: false,
            who: EVERYONE,
            rights: DELETE,
            only_inherit: false,
        }])
        .unwrap();
        with(vec![allow(EVERYONE, LIST | SEARCH | READATTR)]).unwrap();
        with(vec![allow(
            ROOT,
            ADD_FILE | DELETE_CHILD | WRITESECURITY | CHOWN,
        )])
        .unwrap();
        with(vec![allow(ADMIN, GENERIC_ALL)]).unwrap();
        // Refused: each right that changes the folder or what is in it, for staff.
        for right in [
            ADD_FILE,
            ADD_SUBDIRECTORY,
            DELETE_CHILD,
            DELETE,
            WRITESECURITY,
            CHOWN,
            WRITEATTR,
            WRITEEXTATTR,
            GENERIC_WRITE,
            GENERIC_ALL,
            1 << 30,
        ] {
            refused(
                with(vec![allow(STAFF, LIST | right)]),
                "entry 0 of its access list (`ls -le` shows it) lets another account change what is in it",
            );
        }
        // Group 0 (wheel) is not root: compared by UUID, not by number.
        refused(with(vec![allow(WHEEL, ADD_FILE)]), "entry 0");
        // This user's own entry counts as another account's here: the rule admits root and admin.
        refused(with(vec![allow(ME, ADD_FILE)]), "entry 0");
        // An inherit-only entry that gives what is copied in later write (`write`, the file
        // meaning of bit 2), behind a harmless entry.
        refused(
            with(vec![
                allow(EVERYONE, LIST),
                Entry {
                    allow: true,
                    who: STAFF,
                    rights: ADD_FILE,
                    only_inherit: true,
                },
            ]),
            "entry 1 of its access list (`ls -le` shows it) lets another account change what is put in it",
        );
    }

    #[test]
    fn a_bundle_admits_only_this_users_and_roots_entries() {
        let b = Path::new("/Applications/pluxy.app");
        judge_bundle(b, &[], ME, ROOT).unwrap();
        judge_bundle(
            b,
            &[allow(ME, GENERIC_ALL), allow(ROOT, ADD_FILE)],
            ME,
            ROOT,
        )
        .unwrap();
        judge_bundle(b, &[allow(EVERYONE, LIST | SEARCH)], ME, ROOT).unwrap();
        for who in [ADMIN, STAFF, WHEEL, EVERYONE] {
            let e =
                judge_bundle(b, &[allow(ME, LIST), allow(who, ADD_FILE)], ME, ROOT).unwrap_err();
            assert!(
                e.contains("/Applications/pluxy.app is not a folder of yours")
                    && e.contains("entry 1"),
                "{e}"
            );
        }
    }

    /// What a command prints, trimmed; its status and output in the panic if it fails.
    #[cfg(target_os = "macos")]
    fn out(cmd: &str, args: &[&str]) -> String {
        let o = std::process::Command::new(cmd).args(args).output().unwrap();
        let text = format!(
            "{}{}",
            String::from_utf8_lossy(&o.stdout),
            String::from_utf8_lossy(&o.stderr)
        );
        assert!(o.status.success(), "{cmd} {args:?}: {text}");
        text.trim().to_string()
    }

    /// A UUID as `dsmemberutil` prints it.
    #[cfg(target_os = "macos")]
    fn shown(u: &[u8; 16]) -> String {
        let h: String = u.iter().map(|b| format!("{b:02X}")).collect();
        format!(
            "{}-{}-{}-{}-{}",
            &h[0..8],
            &h[8..12],
            &h[12..16],
            &h[16..20],
            &h[20..32]
        )
    }

    // The calls the rule makes, on the runner's real /Applications: what they read matches what
    // the system's own tools show (`stat`, `ls -le`, `dsmemberutil`, `dseditgroup`), and the
    // rule's verdict follows from that. The tools' output is in every failure message.
    #[cfg(target_os = "macos")]
    #[test]
    fn the_real_applications_folder_reads_as_the_systems_tools_show_it() {
        use std::os::unix::fs::OpenOptionsExt;
        let shown_by = format!(
            "ls -lde: {}\nls -ldO: {}\n/System/Volumes/Data: {}",
            out("/bin/ls", &["-lde", APPLICATIONS]),
            out("/bin/ls", &["-ldO", APPLICATIONS]),
            out("/usr/bin/stat", &["-f", "%Su %Sg %Lp", "/System/Volumes/Data"]),
        );
        let dir = std::fs::OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW)
            .open(APPLICATIONS)
            .unwrap();
        let f = sys::folder(&dir).unwrap();
        let stat = out("/usr/bin/stat", &["-f", "%u %g %p", APPLICATIONS]);
        assert_eq!(
            format!("{} {} {:o}", f.uid, f.gid, f.mode),
            stat,
            "{shown_by}"
        );
        // `ls -le` prints one ` <n>: ` line per entry.
        let listed = out("/bin/ls", &["-lde", APPLICATIONS])
            .lines()
            .skip(1)
            .filter(|l| l.trim_start().split_once(": ").is_some())
            .count();
        assert_eq!(f.entries.len(), listed, "{f:?}\n{shown_by}");
        let ids = sys::ids().unwrap();
        assert_eq!(
            shown(&ids.root),
            out("/usr/bin/dsmemberutil", &["getuuid", "-u", "0"]),
            "{shown_by}"
        );
        assert_eq!(
            shown(&ids.admin),
            out("/usr/bin/dsmemberutil", &["getuuid", "-g", "admin"]),
            "{shown_by}"
        );
        assert_eq!(
            ids.admin_gid.to_string(),
            out("/usr/bin/dscl", &[".", "-read", "/Groups/admin", "PrimaryGroupID"])
                .rsplit(' ')
                .next()
                .unwrap()
        );
        let user = out("/usr/bin/id", &["-un"]);
        let member = out(
            "/usr/sbin/dseditgroup",
            &["-o", "checkmember", "-m", &user, "admin"],
        );
        assert_eq!(ids.member, member.starts_with("yes"), "{member}");
        // The verdict: admitted exactly when the stock folder's facts hold for this user.
        let stock = f.uid == 0
            && f.gid == ids.admin_gid
            && f.mode & 0o002 == 0
            && admits_others(&f.entries, &[ids.root, ids.admin]).is_none();
        let verdict = judge(Path::new(APPLICATIONS), &f, Ok(ids), f.dev);
        assert_eq!(
            verdict.is_ok(),
            stock && ids.member,
            "{verdict:?}\n{f:?}\n{member}\n{shown_by}"
        );
        // The folder is root's on the read-only system volume's root, above it.
        let up = out("/usr/bin/stat", &["-f", "%Su %Sg %Lp", "/"]);
        assert!(up.starts_with("root wheel"), "/: {up}");
    }
}
