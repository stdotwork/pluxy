use super::*;
use std::fs;
use std::os::unix::fs::PermissionsExt;

/// A new folder for one test, by its real path (the temporary folder is under the link `/var`),
/// removed when dropped.
struct Temp(PathBuf);

impl Temp {
    fn new(name: &str) -> Temp {
        let d = std::env::temp_dir().join(format!("pluxy-checked-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        Temp(fs::canonicalize(d).unwrap())
    }
}

impl Drop for Temp {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn file(p: &Path, mode: u32) {
    fs::write(p, b"#!/bin/sh\n").unwrap();
    fs::set_permissions(p, fs::Permissions::from_mode(mode)).unwrap();
}

fn folder(p: &Path, mode: u32) {
    fs::create_dir(p).unwrap();
    fs::set_permissions(p, fs::Permissions::from_mode(mode)).unwrap();
}

/// The `admin` group's number, looked up by its name with `getgrnam_r`. Not through the product's
/// own `admin_gid`, so these tests do not check the product against itself, and not by running
/// `dscl`, whose output a few CI runs could not parse.
fn admin() -> u32 {
    // SAFETY: an all-zero group is a valid empty one, filled by getgrnam_r.
    let mut g: libc::group = unsafe { std::mem::zeroed() };
    let mut buf = vec![0 as libc::c_char; 1 << 20];
    let mut found: *mut libc::group = std::ptr::null_mut();
    // SAFETY: the name is NUL-terminated; g, buf and found outlive the call.
    let r = unsafe {
        libc::getgrnam_r(
            c"admin".as_ptr(),
            &mut g,
            buf.as_mut_ptr(),
            buf.len(),
            &mut found,
        )
    };
    assert!(
        r == 0 && !found.is_null(),
        "looking up the group admin: getgrnam_r returned {r} ({}), found {}",
        io::Error::from_raw_os_error(r),
        !found.is_null()
    );
    g.gr_gid
}

/// Gives p the group gid, which its owner may do only as a member of that group.
fn set_group(p: &Path, gid: u32) {
    let c = std::ffi::CString::new(p.as_os_str().as_encoded_bytes()).unwrap();
    // SAFETY: c is NUL-terminated.
    let r = unsafe { libc::chown(c.as_ptr(), u32::MAX, gid) };
    assert_eq!(
        r,
        0,
        "giving {} the group {gid}: {} (the test's user must be in that group)",
        p.display(),
        io::Error::last_os_error()
    );
}

/// A refusal that names part.
fn refused(path: &Path, part: &Path) {
    let e = secure(path).expect_err(&path.display().to_string());
    assert!(
        e.starts_with(&format!("{} ", part.display())),
        "{} refused for {e}, not for {}",
        path.display(),
        part.display()
    );
}

#[test]
fn secure_takes_what_only_root_this_user_and_admins_through_a_folder_can_change() {
    let t = Temp::new("secure");
    let p = t.0.join("prog");
    file(&p, 0o700);
    assert_eq!(secure(&p), Ok(()));
    // A sticky folder anyone may write (as /tmp): only an entry's owner renames or removes it.
    let sticky = t.0.join("sticky");
    folder(&sticky, 0o1777);
    file(&sticky.join("prog"), 0o700);
    assert_eq!(secure(&sticky.join("prog")), Ok(()));
    // As /Applications (root:admin 0775) or a Homebrew folder (the user's, admin, 0775).
    let shared = t.0.join("shared");
    folder(&shared, 0o775);
    set_group(&shared, admin());
    file(&shared.join("prog"), 0o700);
    assert_eq!(secure(&shared.join("prog")), Ok(()));
}

#[test]
fn secure_refuses_what_another_user_could_change() {
    let t = Temp::new("insecure");
    let others = t.0.join("others");
    file(&others, 0o702);
    refused(&others, &others);
    // A file the admin group may write: never the file itself.
    let group = t.0.join("group");
    file(&group, 0o720);
    set_group(&group, admin());
    refused(&group, &group);
    // A folder anyone may write, or a group other than admin.
    let open = t.0.join("open");
    folder(&open, 0o777);
    file(&open.join("prog"), 0o700);
    refused(&open.join("prog"), &open);
    let staff = t.0.join("staff");
    folder(&staff, 0o775);
    // SAFETY: getegid has no preconditions.
    let gid = unsafe { libc::getegid() };
    assert!(gid != admin(), "the test runs with group {gid}, admin's");
    set_group(&staff, gid);
    file(&staff.join("prog"), 0o700);
    refused(&staff.join("prog"), &staff);
    // A link.
    let link = t.0.join("link");
    std::os::unix::fs::symlink(&staff, &link).unwrap();
    refused(&link.join("prog"), &link);
    refused(Path::new("prog"), Path::new("prog"));
}

#[test]
fn exec_target_is_the_opened_file_wherever_it_is_renamed_and_never_another() {
    let t = Temp::new("exec");
    let p = t.0.join("prog");
    file(&p, 0o700);
    let f = File::open(&p).unwrap();
    assert_eq!(exec_target(&f).unwrap(), p);
    // Renamed away, with another file put at its path: the opened file, by its new path.
    let aside = t.0.join("aside");
    fs::rename(&p, &aside).unwrap();
    file(&p, 0o700);
    assert_eq!(exec_target(&f).unwrap(), aside);
    // Replaced by another file renamed over it: refused.
    let q = t.0.join("swapped");
    file(&q, 0o700);
    let g = File::open(&q).unwrap();
    let other = t.0.join("other");
    file(&other, 0o700);
    fs::rename(&other, &q).unwrap();
    assert!(
        exec_target(&g).is_err(),
        "a file replaced after it was opened"
    );
    // Below a folder anyone may write: refused, naming it.
    let open = t.0.join("open");
    folder(&open, 0o777);
    let r = open.join("prog");
    file(&r, 0o700);
    let h = File::open(&r).unwrap();
    let e = exec_target(&h).unwrap_err();
    assert!(
        e.starts_with(&format!("{} may be changed by other users", open.display())),
        "{e}"
    );
}

/// The paths in `secure`'s suggested commands are one shell word each, whatever they hold.
#[test]
fn a_path_is_quoted_as_one_shell_word() {
    use std::os::unix::ffi::OsStrExt;
    let p = Path::new("/Users/a b/it's here/$HOME`x`/prog");
    let q = shell_quoted(p);
    assert_eq!(q, r"'/Users/a b/it'\''s here/$HOME`x`/prog'");
    let out = std::process::Command::new("/bin/sh")
        .arg("-c")
        .arg(format!("printf %s {q}"))
        .output()
        .unwrap();
    assert!(out.status.success(), "{out:?}");
    assert_eq!(out.stdout, p.as_os_str().as_bytes());
}

/// Adds the access list entry `entry` (`chmod +a`'s form, such as `everyone allow read`) to path
/// with macOS's own `chmod`.
fn add_entry(path: &Path, entry: &str) {
    chmod(&["+a", entry], path);
}

fn chmod(args: &[&str], path: &Path) {
    let out = std::process::Command::new("/bin/chmod")
        .args(args)
        .arg(path)
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "chmod {args:?} {}: {out:?}",
        path.display()
    );
}

/// The place of the entry that holds text in path's access list, as macOS's own `ls -le`
/// numbers it.
fn entry_index(path: &Path, text: &str) -> usize {
    let out = std::process::Command::new("/bin/ls")
        .arg("-led")
        .arg(path)
        .output()
        .unwrap();
    let listed = String::from_utf8_lossy(&out.stdout).into_owned();
    let line = listed
        .lines()
        .skip(1)
        .find(|l| l.contains(text))
        .unwrap_or_else(|| panic!("no entry {text:?} on {}: {listed}", path.display()));
    line.trim().split(':').next().unwrap().parse().unwrap()
}

/// Gives path, which must have no access list yet, the one entry `<subject> allow` with exactly
/// the right bits rights, any of them, `chmod +a`'s names or not (the kernel's generic rights),
/// and, with uuid, for the account of that UUID instead of subject. It adds `<subject> allow
/// read` with macOS's own `chmod`, takes the list in the form the kernel reads
/// (`FILESEC_ACL_RAW`), sets that entry's rights (and UUID) there and gives it back to the kernel
/// (`chmodx_np`), which takes any rights and any UUID. As the app's tests do
/// (`private_macos::set_raw_entry`).
fn set_raw_entry(path: &Path, subject: &str, rights: u32, uuid: Option<[u8; 16]>) {
    use std::os::unix::ffi::OsStrExt;
    extern "C" {
        fn acl_get_link_np(path: *const std::os::raw::c_char, ty: c_int) -> *mut c_void;
        fn chmodx_np(path: *const std::os::raw::c_char, fsec: *mut c_void) -> c_int;
        fn filesec_set_property(fsec: *mut c_void, property: c_int, value: *const c_void) -> c_int;
    }
    /// `acl_type_t`'s `ACL_TYPE_EXTENDED` (`sys/acl.h`) and `FILESEC_ACL_RAW` (`sys/fcntl.h`).
    const ACL_TYPE_EXTENDED: c_int = 0x0000_0100;
    const FILESEC_ACL_RAW: c_int = 100;
    /// In `struct kauth_filesec` (`sys/kauth.h`): the magic number, two 16-byte GUIDs, then the
    /// list's entry count and flags; each entry is a 16-byte GUID, its flags and its rights.
    const COUNT: usize = 4 + 16 + 16;
    const FIRST_GUID: usize = COUNT + 4 + 4;
    const FIRST_RIGHTS: usize = FIRST_GUID + 16 + 4;
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
        if let Some(u) = uuid {
            std::ptr::copy_nonoverlapping(u.as_ptr(), raw.add(FIRST_GUID), 16);
        }
        let set = chmodx_np(p.as_ptr(), fsec);
        let e = io::Error::last_os_error();
        filesec_free(fsec);
        assert_eq!(set, 0, "{}: {e}", path.display());
    }
    let entries = status(path).unwrap().1;
    assert_eq!(entries.len(), 1, "{}", path.display());
    assert_eq!(entries[0].rights, u64::from(rights), "{}", path.display());
}

/// Gives a new program, or a new folder above one, an entry for user:daemon holding exactly
/// rights, and what `secure` makes of it, a refusal naming that part or Ok.
fn with_raw_entry(dir: &Path, name: &str, on_folder: bool, rights: u32) -> Result<(), String> {
    let f = dir.join(name);
    folder(&f, 0o700);
    let p = f.join("prog");
    file(&p, 0o700);
    let part = if on_folder { &f } else { &p };
    set_raw_entry(part, "user:daemon", rights, None);
    let got = secure(&p);
    if let Err(e) = &got {
        assert!(
            e.starts_with(&format!("{} ", part.display())),
            "{e} is not for {}",
            part.display()
        );
    }
    got
}

#[test]
fn secure_refuses_an_access_list_entry_that_lets_another_user_change_the_program() {
    let t = Temp::new("acl-file");

    // Rights that change nothing pass.
    let reads = t.0.join("reads");
    file(&reads, 0o700);
    add_entry(
        &reads,
        "user:daemon allow read,execute,readattr,readsecurity",
    );
    assert_eq!(secure(&reads), Ok(()));

    // write, with a deny entry beside it, so the allow entry need not come first.
    let p = t.0.join("prog");
    file(&p, 0o700);
    add_entry(&p, "everyone deny delete");
    add_entry(&p, "user:daemon allow write");
    let i = entry_index(&p, "user:daemon allow write");
    refused(&p, &p);
    let e = secure(&p).unwrap_err();
    assert!(e.contains("user:daemon change it (write)"), "{e}");
    let remove = format!("`chmod -a# {i} {}`", shell_quoted(&p));
    assert!(e.contains(&remove), "{e} does not say {remove}");
    // Doing what it says passes.
    chmod(&["-a#", &i.to_string()], &p);
    assert_eq!(secure(&p), Ok(()));

    // An entry for the admin group on the file itself: admins may write only a folder above it.
    let q = t.0.join("admin-entry");
    file(&q, 0o700);
    add_entry(&q, "group:admin allow append");
    refused(&q, &q);

    // An entry for everyone.
    let r = t.0.join("everyone");
    file(&r, 0o700);
    add_entry(&r, "everyone allow write");
    refused(&r, &r);
    let e = secure(&r).unwrap_err();
    assert!(e.contains("group:everyone change it (write)"), "{e}");
}

#[test]
fn secure_refuses_every_right_that_changes_the_program_or_a_folder_above() {
    let t = Temp::new("acl-rights");
    for (on_folder, names) in [(false, FILE_CHANGES), (true, FOLDER_CHANGES)] {
        for (bit, name) in names {
            let e = with_raw_entry(&t.0, &format!("{on_folder}-{name}"), on_folder, *bit as u32)
                .expect_err(name);
            assert!(
                e.contains(&format!("user:daemon change it ({name})")),
                "{e}"
            );
        }
        // Every read-only right at once passes: read, execute, readattr, readextattr,
        // readsecurity, synchronize, generic_execute and generic_read (`sys/acl.h`), written out
        // so that a bit added to `READ_ONLY` by mistake fails here.
        let reads = 1 << 1 | 1 << 3 | 1 << 7 | 1 << 9 | 1 << 11 | 1 << 20 | 1 << 22 | 1 << 24;
        assert_eq!(READ_ONLY, reads);
        let got = with_raw_entry(&t.0, &format!("{on_folder}-reads"), on_folder, reads as u32);
        assert_eq!(got, Ok(()), "on a folder: {on_folder}");
        // A right no one names is refused, named by its number.
        let e = with_raw_entry(&t.0, &format!("{on_folder}-unknown"), on_folder, 1 << 14)
            .expect_err("bit 14");
        assert!(e.contains("user:daemon change it (bit 14)"), "{e}");
    }
}

#[test]
fn secure_refuses_the_generic_write_and_all_rights() {
    let t = Temp::new("acl-generic");
    for on_folder in [false, true] {
        for (bit, name) in [(23, "generic_write"), (21, "generic_all")] {
            let e = with_raw_entry(&t.0, &format!("{on_folder}-{name}"), on_folder, 1 << bit)
                .expect_err(name);
            assert!(e.contains(&format!("({name})")), "{e}");
        }
    }
}

#[test]
fn secure_refuses_an_access_list_entry_that_lets_another_user_change_a_folder_above() {
    let t = Temp::new("acl-folder");

    for right in ["add_file", "delete_child", "add_subdirectory", "delete"] {
        let f = t.0.join(right);
        folder(&f, 0o700);
        let p = f.join("prog");
        file(&p, 0o700);
        assert_eq!(secure(&p), Ok(()), "{right}");
        add_entry(&f, &format!("user:daemon allow {right}"));
        refused(&p, &f);
        let e = secure(&p).unwrap_err();
        assert!(
            e.contains(&format!("user:daemon change it ({right})")),
            "{e}"
        );
        let clear = format!("`chmod -N {}`", shell_quoted(&f));
        assert!(e.contains(&clear), "{e} does not say {clear}");
        chmod(&["-N"], &f);
        assert_eq!(secure(&p), Ok(()), "{right} cleared");
    }

    // A folder is judged before what is below it: with an entry on both, the refusal names the
    // folder. Walked from the program up, another user could swap the program and then remove
    // their entry on the folder before it was read.
    let both = t.0.join("both");
    folder(&both, 0o700);
    let p = both.join("prog");
    file(&p, 0o700);
    add_entry(&both, "user:daemon allow add_file");
    add_entry(&p, "user:daemon allow write");
    refused(&p, &both);

    // A deny entry, as a home folder carries.
    let home = t.0.join("home");
    folder(&home, 0o700);
    let p = home.join("prog");
    file(&p, 0o700);
    add_entry(&home, "group:everyone deny delete");
    add_entry(&p, "group:everyone deny delete");
    assert_eq!(secure(&p), Ok(()));
}

#[test]
fn secure_lets_an_admin_entry_on_a_folder_above_only() {
    let t = Temp::new("acl-admin");
    let shared = t.0.join("shared");
    folder(&shared, 0o755);
    let p = shared.join("prog");
    file(&p, 0o700);
    add_entry(
        &shared,
        "group:admin allow add_file,add_subdirectory,delete_child",
    );
    assert_eq!(secure(&p), Ok(()));

    // Another group's is refused.
    let staff = t.0.join("staff");
    folder(&staff, 0o755);
    file(&staff.join("prog"), 0o700);
    add_entry(&staff, "group:staff allow add_file");
    refused(&staff.join("prog"), &staff);
}

#[test]
fn secure_refuses_an_inherited_access_list_entry() {
    let t = Temp::new("acl-inherit");

    // Entries that reach only what is made later: the folder itself passes.
    let outer = t.0.join("outer");
    folder(&outer, 0o700);
    add_entry(
        &outer,
        "user:daemon allow add_file,directory_inherit,only_inherit",
    );
    // A file made in it takes nothing: the entry reaches folders only.
    let plain = outer.join("plain");
    file(&plain, 0o700);
    assert_eq!(secure(&plain), Ok(()));

    // A folder made in it inherits the entry, and is refused for it.
    let inner = outer.join("inner");
    folder(&inner, 0o700);
    entry_index(&inner, "inherited");
    let p = inner.join("prog");
    file(&p, 0o700);
    refused(&p, &inner);

    // A program made in a folder with a file entry inherits it, and is refused for it.
    let files = t.0.join("files");
    folder(&files, 0o700);
    add_entry(&files, "user:daemon allow write,file_inherit,only_inherit");
    let q = files.join("prog");
    file(&q, 0o700);
    entry_index(&q, "inherited");
    refused(&q, &q);
    let e = secure(&q).unwrap_err();
    assert!(e.contains("user:daemon change it (write)"), "{e}");
}

/// An `OWNER@` entry, as a network volume's list can carry, reads as the group `owner`, number
/// 10, not as the file's owner, so it is refused like any other group's.
#[test]
fn secure_refuses_an_entry_for_the_owner_group() {
    let t = Temp::new("acl-owner");
    let p = t.0.join("prog");
    file(&p, 0o700);
    add_entry(&p, "group:owner allow write");
    let entries = status(&p).unwrap().1;
    assert_eq!(entries.len(), 1, "{entries:?}");
    assert_eq!(entries[0].subject, Subject::Group(10), "{entries:?}");
    refused(&p, &p);
    let e = secure(&p).unwrap_err();
    assert!(e.contains("group:owner change it (write)"), "{e}");
}

/// An entry for a UUID no account has, which macOS cannot turn into a user or group number, is
/// refused, shown by that UUID.
#[test]
fn secure_refuses_an_entry_for_an_unknown_account_naming_its_uuid() {
    let t = Temp::new("acl-unknown");
    let p = t.0.join("prog");
    file(&p, 0o700);
    // A random UUID, of no account.
    let uuid = [
        0x5b, 0x2e, 0x91, 0x0c, 0x7d, 0x43, 0x4a, 0x6f, 0x9e, 0x18, 0x32, 0xc4, 0xa7, 0x55, 0xe0,
        0x1d,
    ];
    set_raw_entry(&p, "user:daemon", 1 << 2, Some(uuid));
    refused(&p, &p);
    let e = secure(&p).unwrap_err();
    assert!(
        e.contains("lets unknown 5B2E910C-7D43-4A6F-9E18-32C4A755E01D change it (write)"),
        "{e}"
    );
}

/// A part that is not there is an error, named by the first part missing from `/` down, never
/// read as a part with no access list.
#[test]
fn secure_refuses_a_part_that_is_gone() {
    let t = Temp::new("gone");
    let p = t.0.join("prog");
    let e = secure(&p).unwrap_err();
    assert!(e.starts_with(&format!("{}: ", p.display())), "{e}");
    let gone = t.0.join("gone");
    let e = secure(&gone.join("prog")).unwrap_err();
    assert!(e.starts_with(&format!("{}: ", gone.display())), "{e}");
}

/// Runs `hdiutil` with args, up to three times (it now and then fails to take a new image's
/// device at once), and panics with its output if it never succeeds.
fn hdiutil(args: &[&std::ffi::OsStr]) {
    let mut last = None;
    for _ in 0..3 {
        let out = std::process::Command::new("/usr/bin/hdiutil")
            .args(args)
            .output()
            .expect("/usr/bin/hdiutil");
        if out.status.success() {
            return;
        }
        last = Some(out);
        std::thread::sleep(std::time::Duration::from_secs(1));
    }
    panic!("hdiutil {args:?}: {last:?}");
}

/// A new disk image of its own, in image_dir, attached at mnt for this user (`hdiutil` needs
/// no root) with ownership kept or ignored (`-owners on` or `off`), its top folder made 0755;
/// detached when dropped, before the folder that holds it is removed.
struct Volume {
    mnt: PathBuf,
}

impl Volume {
    fn attach(image_dir: &Path, mnt: &Path, owners: bool) -> Volume {
        let name = mnt.file_name().unwrap();
        let image = image_dir.join(name).with_extension("dmg");
        let s = |s: &'static str| std::ffi::OsStr::new(s);
        hdiutil(&[
            s("create"),
            s("-size"),
            s("8m"),
            s("-fs"),
            s("HFS+"),
            s("-volname"),
            name,
            image.as_os_str(),
        ]);
        fs::create_dir(mnt).unwrap();
        hdiutil(&[
            s("attach"),
            s("-nobrowse"),
            s("-noautoopen"),
            s("-noverify"),
            s("-owners"),
            s(if owners { "on" } else { "off" }),
            s("-mountpoint"),
            mnt.as_os_str(),
            image.as_os_str(),
        ]);
        let v = Volume {
            mnt: mnt.to_path_buf(),
        };
        // Read back by mount(8), not by the check under test: `noowners` is how it names
        // MNT_IGNORE_OWNERSHIP.
        let out = std::process::Command::new("/sbin/mount").output().unwrap();
        let listed = String::from_utf8_lossy(&out.stdout).into_owned();
        let line = listed
            .lines()
            .find(|l| l.contains(&format!(" on {} (", mnt.display())))
            .unwrap_or_else(|| panic!("{} is not mounted: {listed}", mnt.display()));
        // Only the options in the brackets: the mount point may hold the word too.
        let options = &line[line.rfind(" (").unwrap() + 2..];
        let noowners = options
            .trim_end_matches(')')
            .split(", ")
            .any(|o| o == "noowners");
        assert_eq!(noowners, !owners, "{line}");
        fs::set_permissions(mnt, fs::Permissions::from_mode(0o755)).unwrap();
        v
    }
}

impl Drop for Volume {
    fn drop(&mut self) {
        for force in [false, true] {
            let mut c = std::process::Command::new("/usr/bin/hdiutil");
            c.arg("detach").arg(&self.mnt);
            if force {
                c.arg("-force");
            }
            if c.output().is_ok_and(|o| o.status.success()) {
                return;
            }
        }
    }
}

/// On a volume that ignores ownership, where macOS shows every file as this user's, a program is
/// refused for the volume's top folder, the first part of its path there, and so is a program on a
/// volume that keeps ownership mounted below it. The same kind of image with ownership kept passes.
#[test]
fn secure_refuses_a_program_on_a_volume_that_ignores_ownership() {
    let t = Temp::new("noowners");
    let kept = Volume::attach(&t.0, &t.0.join("kept"), true);
    let p = kept.mnt.join("prog");
    file(&p, 0o700);
    assert_eq!(secure(&p), Ok(()));

    let ignored = Volume::attach(&t.0, &t.0.join("ignored"), false);
    let q = ignored.mnt.join("prog");
    file(&q, 0o700);
    // SAFETY: geteuid has no preconditions.
    assert_eq!(fs::metadata(&q).unwrap().uid(), unsafe { libc::geteuid() });
    let below = Volume::attach(&t.0, &ignored.mnt.join("below"), true);
    let r = below.mnt.join("prog");
    file(&r, 0o700);
    let v = shell_quoted(&ignored.mnt);
    for path in [&q, &r] {
        let e = secure(path).expect_err(&path.display().to_string());
        assert!(
            e.starts_with(&format!(
                "{v} is on the volume at {v}, which ignores ownership, so any user may change it: "
            )),
            "{e}"
        );
        assert!(
            e.contains(&format!("`sudo diskutil enableOwnership {v}`")),
            "{e}"
        );
    }
    let e = exec_target(&fs::File::open(&q).unwrap()).unwrap_err();
    assert!(e.contains("which ignores ownership"), "{e}");
}
