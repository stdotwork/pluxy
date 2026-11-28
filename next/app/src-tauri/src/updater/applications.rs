#![cfg_attr(not(target_os = "macos"), allow(dead_code))]
use super::acl;
use std::path::Path;

/// The one folder this rule admits.
pub(super) const APPLICATIONS: &str = "/Applications";

/// What the rule reads of the bundle's folder, or of the bundle, through its descriptor.
#[derive(Clone, Debug)]
pub(super) struct Folder {
    pub folder: bool,
    pub uid: u32,
    pub gid: u32,
    pub mode: u32,
    pub dev: u64,
    pub entries: Vec<acl::Entry>,
}

/// Who root, `admin` and this user are, looked up now.
#[derive(Clone, Copy, Debug)]
pub(super) struct Ids {
    pub root: [u8; 16],
    pub admin_gid: u32,
    pub admin: [u8; 16],
    /// This user's UUID.
    pub me: [u8; 16],
    /// Whether this user is a member of `admin`.
    pub member: bool,
}

/// Advice for a refusal of the folder.
const MOVE: &str = "Move pluxy to Applications in your home folder (~/Applications) with Finder, \
                    open it from there, and update then.";

/// Advice for a bundle that is not this user's: a copy made by this user is.
const COPY: &str = "Copy pluxy to Applications in your home folder (~/Applications) with Finder, \
                    open the copy from there, and update then.";

/// error with the advice every refusal here ends with.
pub(super) fn advised(error: impl std::fmt::Display) -> String {
    format!("{error}. {MOVE}")
}

/// The refusal when what the rule reads of path (the folder, the bundle, or who root and `admin`
/// are) could not be read: error says why.
pub(super) fn unknown(path: &Path, error: impl std::fmt::Display) -> String {
    format!(
        "Whether only you and this Mac's administrators can change {} is unknown ({error}). {MOVE}",
        path.display()
    )
}

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
    if let Some(why) = acl::others(&f.entries, &[ids.root, ids.admin]) {
        return not(why);
    }
    if f.dev != data_dev {
        return not(
            "it is on another volume than the app's data folder, where the update is staged".into(),
        );
    }
    // Checked again at every swap through this folder (`app::swap_in`), a rollback's or a
    // put-back's too: a membership change while a new version accepts itself refuses putting the
    // earlier version back, failing closed as the kernel would. Rare, and accepted.
    if !ids.member {
        return Err(format!(
            "pluxy is in {APPLICATIONS}, which only administrators can change, and this account \
             is not one. {MOVE}"
        ));
    }
    Ok(())
}

/// The bundle at path in `/Applications`, read as b through a descriptor opened in that folder:
/// it must be a folder this user (my_uid) owns that no one else can write to, and no allow entry
/// of its access list may give anyone but this user and root (ids) more than reading. The
/// refusal says why and what to do.
pub(super) fn judge_bundle(path: &Path, b: &Folder, my_uid: u32, ids: &Ids) -> Result<(), String> {
    let not = |why: String, advice: String| {
        Err(format!(
            "{} is not a folder of yours that only you can change: {why}. {advice}",
            path.display()
        ))
    };
    if !b.folder {
        return not("it is not a folder".into(), COPY.into());
    }
    if b.uid != my_uid {
        return not(format!("its owner is user {}, not you", b.uid), COPY.into());
    }
    if b.mode & 0o022 != 0 {
        return not(
            "other accounts can change it".into(),
            format!(
                "Remove their write permission (in Terminal: chmod go-w \"{}\") and update then.",
                path.display()
            ),
        );
    }
    if let Some((i, _)) = acl::admits_others(&b.entries, &[ids.me, ids.root]) {
        let why = acl::others(&b.entries, &[ids.me, ids.root]).unwrap_or_default();
        return not(
            why,
            format!(
                "Remove that entry (in Terminal: chmod -a# {i} \"{}\") and update then.",
                path.display()
            ),
        );
    }
    Ok(())
}

#[cfg(target_os = "macos")]
pub(super) use sys::{folder, ids};

#[cfg(target_os = "macos")]
mod sys {
    use super::{acl, Folder, Ids};
    use std::fs::File;
    use std::io;
    use std::os::raw::c_int;

    extern "C" {
        fn mbr_gid_to_uuid(gid: libc::gid_t, uu: *mut u8) -> c_int;
        fn mbr_check_membership(user: *const u8, group: *const u8, ismember: *mut c_int) -> c_int;
    }

    /// What is open as f: its kind, owner, group, mode and volume (`fstat`) and its extended
    /// access list (`acl::entries`), both through the descriptor.
    pub(in super::super) fn folder(f: &File) -> io::Result<Folder> {
        use std::os::unix::fs::MetadataExt;
        let m = f.metadata()?;
        Ok(Folder {
            folder: m.is_dir(),
            uid: m.uid(),
            gid: m.gid(),
            mode: m.mode(),
            dev: m.dev(),
            entries: acl::entries(f)?,
        })
    }

    /// Root's, `admin`'s and this user's UUIDs (`acl::yours` for this user and root), `admin`'s
    /// group number as `getgrnam` gives it now, and whether this user is a member of `admin`. Any
    /// lookup that fails is an error.
    pub(in super::super) fn ids() -> Result<Ids, String> {
        let name = c"admin";
        let mut gid = None;
        // SAFETY: getgrnam_r writes only into g and buf; found points into them or is null.
        let r = acl::sized(|buf| unsafe {
            let mut g: libc::group = std::mem::zeroed();
            let mut found: *mut libc::group = std::ptr::null_mut();
            let r = libc::getgrnam_r(
                name.as_ptr(),
                &mut g,
                buf.as_mut_ptr(),
                buf.len(),
                &mut found,
            );
            gid = (r == 0 && !found.is_null()).then_some(g.gr_gid);
            r
        });
        if r != 0 {
            return Err(format!(
                "looking up the admin group: {}",
                io::Error::from_raw_os_error(r)
            ));
        }
        let admin_gid = gid.ok_or("there is no group named admin")?;
        let mut admin = [0u8; 16];
        // SAFETY: admin is 16 writable bytes.
        let r = unsafe { mbr_gid_to_uuid(admin_gid, admin.as_mut_ptr()) };
        if r != 0 {
            return Err(format!(
                "the admin group's UUID: {}",
                io::Error::from_raw_os_error(r)
            ));
        }
        let (me, root) = match acl::yours()?[..] {
            [me, root] => (me, root),
            _ => return Err("this user's and root's UUIDs are unknown".into()),
        };
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
            me,
            member: is != 0,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use acl::tests::{
        ADD_FILE, ADD_SUBDIRECTORY, CHOWN, DELETE, DELETE_CHILD, EVERYONE, GENERIC_ALL,
        GENERIC_WRITE, LIST, ME, READATTR, ROOT, SEARCH, STAFF, WHEEL, WRITEATTR, WRITEEXTATTR,
        WRITESECURITY,
    };

    const ADMIN: [u8; 16] = [
        0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0xAB, 0xCD, 0xEF, 0, 0, 0, 0x50,
    ];
    const DEV: u64 = 0x0100_0004;

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
            me: ME,
            member: true,
        })
    }

    fn allow(who: [u8; 16], rights: u64) -> acl::Entry {
        acl::Entry {
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
        let with =
            |entries: Vec<acl::Entry>| judge(apps(), &Folder { entries, ..stock() }, ids(), DEV);
        // Fine: the usual deny entry, read-only entries, and root's and admin's.
        with(vec![acl::Entry {
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
                "entry 0 of its access list (`ls -le` shows it) lets another account change it or what is in it",
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
                acl::Entry {
                    allow: true,
                    who: STAFF,
                    rights: ADD_FILE,
                    only_inherit: true,
                },
            ]),
            "entry 1 of its access list (`ls -le` shows it) lets another account change what is put in it",
        );
    }

    // The bundle in /Applications: this user's folder that no one else can write to, with no
    // allow entry for anyone but this user and root beyond reading. Each refusal says why and
    // what to do: copy a bundle that is not yours, remove others' write or the entry.
    #[test]
    fn a_bundle_must_be_yours_alone_with_no_entry_for_others() {
        let p = Path::new("/Applications/pluxy.app");
        let ids = ids().unwrap();
        let mine = Folder {
            folder: true,
            uid: 501,
            gid: 20,
            mode: 0o40755,
            dev: DEV,
            entries: vec![],
        };
        let with = |b: Folder| judge_bundle(p, &b, 501, &ids);
        with(mine.clone()).unwrap();
        with(Folder {
            entries: vec![
                allow(ME, GENERIC_ALL),
                allow(ROOT, ADD_FILE),
                allow(EVERYONE, LIST | SEARCH),
            ],
            ..mine.clone()
        })
        .unwrap();
        let refused = |b: Folder, why: &str, advice: &str| {
            let e = with(b).unwrap_err();
            assert!(
                e.starts_with(
                    "/Applications/pluxy.app is not a folder of yours that only you can change: "
                ) && e.contains(why)
                    && e.contains(advice),
                "{why}: {e}"
            );
        };
        let copy = "Copy pluxy to Applications in your home folder (~/Applications) with Finder";
        refused(
            Folder {
                uid: 0,
                ..mine.clone()
            },
            "its owner is user 0, not you",
            copy,
        );
        refused(
            Folder {
                uid: 502,
                ..mine.clone()
            },
            "its owner is user 502",
            copy,
        );
        refused(
            Folder {
                folder: false,
                ..mine.clone()
            },
            "not a folder",
            copy,
        );
        for mode in [0o40775, 0o40757] {
            refused(
                Folder {
                    mode,
                    ..mine.clone()
                },
                "other accounts can change it",
                "chmod go-w \"/Applications/pluxy.app\"",
            );
        }
        for who in [ADMIN, STAFF, WHEEL, EVERYONE] {
            refused(
                Folder {
                    entries: vec![allow(ME, LIST), allow(who, ADD_FILE)],
                    ..mine.clone()
                },
                "entry 1 of its access list",
                "Remove that entry (in Terminal: chmod -a# 1 \"/Applications/pluxy.app\")",
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

    // The calls the rule makes, on the real /Applications of a GitHub-hosted macOS runner, whose
    // user is an administrator: they read the stock folder (root, admin, 0775, no access list
    // entry), the well-known UUIDs of root and admin, admin's gid 80 and the membership, each as
    // written out here and as the system's own tools show it (`stat`, `ls -le`, `dsmemberutil`,
    // `dscl`, `dseditgroup`), and the rule admits the folder. The tools' output is in every
    // failure message. (The native update session adds and removes entries with sudo.)
    #[cfg(target_os = "macos")]
    #[test]
    fn the_real_applications_folder_reads_as_the_systems_tools_show_it() {
        use std::os::unix::fs::OpenOptionsExt;
        let shown_by = format!(
            "ls -lde: {}\nls -ldO: {}\n/System/Volumes/Data: {}",
            out("/bin/ls", &["-lde", APPLICATIONS]),
            out("/bin/ls", &["-ldO", APPLICATIONS]),
            out(
                "/usr/bin/stat",
                &["-f", "%Su %Sg %Lp", "/System/Volumes/Data"]
            ),
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
            out("/usr/bin/dsmemberutil", &["getuuid", "-G", "admin"]),
            "{shown_by}"
        );
        assert_eq!(
            ids.admin_gid.to_string(),
            out(
                "/usr/bin/dscl",
                &[".", "-read", "/Groups/admin", "PrimaryGroupID"]
            )
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
        // Written out: the stock folder and the well-known identities.
        assert_eq!(
            (f.folder, f.uid, f.gid, f.mode, f.entries.len()),
            (true, 0, 80, 0o40775, 0),
            "{f:?}\n{shown_by}"
        );
        assert_eq!(stat, "0 80 40775", "{shown_by}");
        assert_eq!(listed, 0, "{shown_by}");
        assert_eq!(shown(&ids.root), "FFFFEEEE-DDDD-CCCC-BBBB-AAAA00000000");
        assert_eq!(shown(&ids.admin), "ABCDEFAB-CDEF-ABCD-EFAB-CDEF00000050");
        assert_eq!(ids.admin_gid, 80);
        assert_eq!(
            shown(&ids.me),
            out("/usr/bin/dsmemberutil", &["getuuid", "-U", &user])
        );
        assert!(ids.member, "{member}");
        judge(Path::new(APPLICATIONS), &f, Ok(ids), f.dev)
            .unwrap_or_else(|e| panic!("{e}\n{f:?}\n{shown_by}"));
        // The folder is root's on the read-only system volume's root, above it.
        let up = out("/usr/bin/stat", &["-f", "%Su %Sg %Lp", "/"]);
        assert!(up.starts_with("root wheel"), "/: {up}");
    }
}
