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

/// The `admin` group's number, as Directory Services gives it.
fn admin() -> u32 {
    let out = std::process::Command::new("/usr/bin/dscl")
        .args([".", "-read", "/Groups/admin", "PrimaryGroupID"])
        .output()
        .unwrap();
    let text = String::from_utf8(out.stdout).unwrap();
    text.split_whitespace().last().unwrap().parse().unwrap()
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
