use super::*;
use std::fs;
use std::path::PathBuf;

fn temp(name: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!(
        "pluxy-private-macos-{name}-{}-{}",
        std::process::id(),
        crate::managed::random_key()
    ));
    fs::create_dir(&d).unwrap();
    d
}

/// What macOS's own `ls -le` lists of path's access list (its lines after the first).
fn listed(path: &std::path::Path) -> String {
    let out = std::process::Command::new("/bin/ls")
        .arg("-led")
        .arg(path)
        .output()
        .unwrap();
    String::from_utf8_lossy(&out.stdout)
        .lines()
        .skip(1)
        .collect::<Vec<_>>()
        .join("\n")
}

#[test]
fn a_file_with_an_entry_has_one_and_a_plain_file_has_none() {
    let d = temp("file");
    let p = d.join("f");
    fs::write(&p, b"x").unwrap();
    assert!(!has_entries(&fs::File::open(&p).unwrap()).unwrap());
    add_entry(&p, "everyone allow read");
    assert!(listed(&p).contains("everyone allow read"), "{}", listed(&p));
    assert!(has_entries(&fs::File::open(&p).unwrap()).unwrap());
    // A deny entry counts too: the rule is no entries at all.
    let q = d.join("g");
    fs::write(&q, b"x").unwrap();
    add_entry(&q, "everyone deny write");
    assert!(has_entries(&fs::File::open(&q).unwrap()).unwrap());
    fs::remove_dir_all(d).unwrap();
}

#[test]
fn clearing_a_folder_leaves_no_entry_and_nothing_for_new_files_to_inherit() {
    let d = temp("folder");
    let inner = d.join("inner");
    fs::create_dir(&inner).unwrap();
    add_entry(&inner, "everyone allow list,file_inherit,directory_inherit");
    // Before: a file made in it inherits the entry, whatever its mode.
    let before = inner.join("before");
    fs::write(&before, b"x").unwrap();
    assert!(has_entries(&fs::File::open(&before).unwrap()).unwrap());
    let folder = fs::File::open(&inner).unwrap();
    assert!(has_entries(&folder).unwrap());
    clear_entries(&folder).unwrap();
    assert!(!has_entries(&folder).unwrap());
    assert_eq!(listed(&inner), "");
    // After: a new file inherits none.
    let after = inner.join("after");
    fs::write(&after, b"x").unwrap();
    assert!(!has_entries(&fs::File::open(&after).unwrap()).unwrap());
    // Clearing again changes nothing: the empty list counts as none, so it is not set again,
    // which would change the folder's status-change time.
    let changed = |f: &fs::File| {
        use std::os::unix::fs::MetadataExt;
        let m = f.metadata().unwrap();
        (m.ctime(), m.ctime_nsec())
    };
    let before_again = changed(&folder);
    std::thread::sleep(std::time::Duration::from_millis(20));
    clear_entries(&folder).unwrap();
    assert_eq!(changed(&folder), before_again);
    assert_eq!(listed(&inner), "");
    // A file is cleared through its own descriptor too.
    clear_entries(&fs::File::open(&before).unwrap()).unwrap();
    assert!(!has_entries(&fs::File::open(&before).unwrap()).unwrap());
    fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_new_file_takes_no_entry_from_its_folder_at_any_moment() {
    use std::os::unix::fs::PermissionsExt;
    let d = temp("create");
    add_entry(&d, "everyone allow read,write,file_inherit");
    // The control: a file made the plain way inherits the entry.
    let plain = d.join("plain");
    fs::write(&plain, b"x").unwrap();
    assert!(has_entries(&fs::File::open(&plain).unwrap()).unwrap());
    // Checked on the descriptor it is made with, before anything else is done to it.
    let p = d.join("new");
    let f = create_new(&p, 0o600).unwrap();
    assert!(!has_entries(&f).unwrap(), "{}", listed(&p));
    assert_eq!(listed(&p), "");
    assert_eq!(f.metadata().unwrap().permissions().mode() & 0o777, 0o600);
    // Never over what is there, nor through a link.
    assert_eq!(
        create_new(&p, 0o600).unwrap_err().kind(),
        std::io::ErrorKind::AlreadyExists
    );
    let link = d.join("link");
    std::os::unix::fs::symlink(d.join("elsewhere"), &link).unwrap();
    assert!(create_new(&link, 0o600).is_err());
    assert!(!d.join("elsewhere").exists());
    fs::remove_dir_all(d).unwrap();
}

/// A volume that keeps no access lists (FAT, as on a USB stick), in a disk image this test makes
/// and detaches: the kernel refuses a file made with a list there, so `create_new_in` makes the
/// file plainly.
#[test]
fn a_volume_without_access_lists_gets_a_plain_new_file() {
    use std::os::fd::AsRawFd;
    let d = temp("fat");
    let img = d.join("v.dmg");
    let mnt = d.join("mnt");
    fs::create_dir(&mnt).unwrap();
    let hdiutil = |args: &[&std::ffi::OsStr]| {
        let out = std::process::Command::new("/usr/bin/hdiutil")
            .args(args)
            .output()
            .unwrap();
        assert!(out.status.success(), "hdiutil {args:?}: {out:?}");
    };
    hdiutil(&[
        "create".as_ref(),
        "-size".as_ref(),
        "4m".as_ref(),
        "-fs".as_ref(),
        "MS-DOS".as_ref(),
        "-volname".as_ref(),
        "PLUXYTEST".as_ref(),
        img.as_os_str(),
    ]);
    hdiutil(&[
        "attach".as_ref(),
        "-nobrowse".as_ref(),
        "-mountpoint".as_ref(),
        mnt.as_os_str(),
        img.as_os_str(),
    ]);
    let checked = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        let folder = fs::File::open(&mnt).unwrap();
        // SAFETY: folder is an open descriptor.
        let lists = unsafe { libc::fpathconf(folder.as_raw_fd(), libc::_PC_EXTENDED_SECURITY_NP) };
        assert_eq!(lists, 0);
        // What the plain route is for: a file made with a list is refused on this volume.
        assert!(create_new(&mnt.join("listed.json"), 0o600).is_err());
        let f = create_new_in(&folder, &mnt.join("plain.json"), 0o600).unwrap();
        assert!(!has_entries(&f).unwrap());
    }));
    hdiutil(&["detach".as_ref(), mnt.as_os_str()]);
    fs::remove_dir_all(d).unwrap();
    checked.unwrap();
}

#[test]
fn a_new_file_in_a_folder_takes_no_entry_from_it() {
    let d = temp("create-in");
    add_entry(&d, "everyone allow read,write,file_inherit");
    let folder = fs::File::open(&d).unwrap();
    let p = d.join("new");
    let f = create_new_in(&folder, &p, 0o600).unwrap();
    assert!(!has_entries(&f).unwrap(), "{}", listed(&p));
    fs::remove_dir_all(d).unwrap();
}
