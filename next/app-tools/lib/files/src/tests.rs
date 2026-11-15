use super::*;
use std::io::{Read, Write};
use std::path::PathBuf;
use std::time::{Duration, SystemTime};

/// A fresh folder under the system's temporary folder.
fn temp(name: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "pluxy-files-{name}-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    fs::create_dir_all(&dir).unwrap();
    dir
}

/// A link at link to the file target, or None where this account may not make one (Windows
/// without the symbolic-link privilege), said on stderr so the skip is visible.
fn file_link(target: &Path, link: &Path) -> Option<()> {
    #[cfg(unix)]
    let made = std::os::unix::fs::symlink(target, link);
    #[cfg(windows)]
    let made = std::os::windows::fs::symlink_file(target, link);
    match made {
        Ok(()) => Some(()),
        Err(e) if cfg!(windows) => {
            eprintln!("skipped: this account cannot make a symbolic link ({e})");
            None
        }
        Err(e) => panic!("{e}"),
    }
}

#[test]
fn mtime_ns_is_the_modification_time() {
    let dir = temp("mtime");
    let path = dir.join("f");
    let f = File::create(&path).unwrap();
    let at = UNIX_EPOCH + Duration::new(1_700_000_000, 123_456_700);
    f.set_modified(at).unwrap();
    let m = fs::metadata(&path).unwrap();
    assert_eq!(mtime_ns(&m), 1_700_000_000_123_456_700);
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt;
        assert_eq!(mtime_ns(&m), m.mtime() * 1_000_000_000 + m.mtime_nsec());
    }
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn open_regular_opens_a_file_and_refuses_a_folder() {
    let dir = temp("open");
    let path = dir.join("f");
    fs::write(&path, b"bytes").unwrap();
    let mut s = String::new();
    open_regular(&path).unwrap().read_to_string(&mut s).unwrap();
    assert_eq!(s, "bytes");
    assert!(open_regular(&dir).is_err());
    assert!(open_regular(&dir.join("missing")).is_err());
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn open_regular_refuses_a_link() {
    let dir = temp("link");
    let target = dir.join("target");
    fs::write(&target, b"bytes").unwrap();
    let link = dir.join("link");
    if file_link(&target, &link).is_some() {
        assert!(open_regular(&link).is_err());
        // The link's own identity is not its target's.
        assert_ne!(id_of_path(&link).unwrap(), id_of_path(&target).unwrap());
    }
    fs::remove_dir_all(dir).unwrap();
}

#[cfg(unix)]
#[test]
fn open_regular_refuses_a_fifo_without_waiting() {
    let dir = temp("fifo");
    let fifo = dir.join("fifo");
    let c = std::ffi::CString::new(fifo.to_str().unwrap()).unwrap();
    assert_eq!(unsafe { libc::mkfifo(c.as_ptr(), 0o600) }, 0);
    assert!(open_regular(&fifo).is_err());
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn id_tells_files_apart() {
    let dir = temp("id");
    let (a, b) = (dir.join("a"), dir.join("b"));
    fs::write(&a, b"a").unwrap();
    fs::write(&b, b"b").unwrap();
    let opened = File::open(&a).unwrap();
    assert_eq!(id_of(&opened).unwrap(), id_of_path(&a).unwrap());
    assert_ne!(id_of_path(&a).unwrap(), id_of_path(&b).unwrap());
    // Replaced by another file under the same name: another identity.
    fs::rename(&b, &a).unwrap();
    assert_ne!(id_of(&opened).unwrap(), id_of_path(&a).unwrap());
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn private_folders_and_files_are_private() {
    let dir = temp("private");
    let folder = dir.join("folder");
    create_private_dir(&folder).unwrap();
    assert!(is_private(&folder).unwrap());
    assert_eq!(
        create_private_dir(&folder).unwrap_err().kind(),
        io::ErrorKind::AlreadyExists
    );
    let file = folder.join("file");
    create_new_private(&file)
        .unwrap()
        .write_all(b"secret")
        .unwrap();
    assert!(is_private(&file).unwrap());
    assert_eq!(fs::read(&file).unwrap(), b"secret");
    assert_eq!(
        create_new_private(&file).unwrap_err().kind(),
        io::ErrorKind::AlreadyExists
    );
    let all = dir.join("a").join("b");
    create_private_dir_all(&all).unwrap();
    assert!(all.is_dir());
    #[cfg(unix)]
    assert!(is_private(&all).unwrap() && is_private(&dir.join("a")).unwrap());
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn an_open_folder_or_file_is_made_private() {
    let dir = temp("tighten");
    let folder = dir.join("folder");
    fs::create_dir(&folder).unwrap();
    let file = dir.join("file");
    fs::write(&file, b"x").unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&folder, fs::Permissions::from_mode(0o755)).unwrap();
        fs::set_permissions(&file, fs::Permissions::from_mode(0o644)).unwrap();
    }
    // On Windows: an entry that lets everyone read, added by the system's own tool.
    #[cfg(windows)]
    for p in [&folder, &file] {
        assert!(icacls(p, &["/grant", "*S-1-1-0:(R)"]));
    }
    assert!(!is_private(&folder).unwrap());
    assert!(!is_private(&file).unwrap());
    make_private(&folder).unwrap();
    make_private(&file).unwrap();
    assert!(is_private(&folder).unwrap());
    assert!(is_private(&file).unwrap());
    // What plain std makes in the folder afterwards inherits its entries on Windows, but not its
    // protection, so it is not private until it is made so.
    #[cfg(windows)]
    {
        let later = folder.join("later");
        fs::write(&later, b"x").unwrap();
        assert!(!is_private(&later).unwrap());
        make_private(&later).unwrap();
        assert!(is_private(&later).unwrap());
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(
            fs::metadata(&folder).unwrap().permissions().mode() & 0o777,
            0o700
        );
        assert_eq!(
            fs::metadata(&file).unwrap().permissions().mode() & 0o777,
            0o600
        );
    }
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn make_private_refuses_a_link() {
    let dir = temp("private-link");
    let target = dir.join("target");
    fs::write(&target, b"x").unwrap();
    let link = dir.join("link");
    if file_link(&target, &link).is_some() {
        assert!(make_private(&link).is_err());
    }
    fs::remove_dir_all(dir).unwrap();
}

/// Windows: runs the system's icacls on path with args; whether it succeeded.
#[cfg(windows)]
fn icacls(path: &Path, args: &[&str]) -> bool {
    let out = std::process::Command::new("icacls")
        .arg(path)
        .args(args)
        .output()
        .unwrap();
    if !out.status.success() {
        eprintln!("icacls {args:?}: {out:?}");
    }
    out.status.success()
}

#[cfg(unix)]
#[test]
fn make_private_refuses_a_fifo_and_leaves_it_as_it_was() {
    use std::os::unix::fs::PermissionsExt;
    let dir = temp("private-fifo");
    let fifo = dir.join("fifo");
    let c = std::ffi::CString::new(fifo.to_str().unwrap()).unwrap();
    assert_eq!(unsafe { libc::mkfifo(c.as_ptr(), 0o644) }, 0);
    fs::set_permissions(&fifo, fs::Permissions::from_mode(0o644)).unwrap();
    assert!(make_private(&fifo).is_err());
    let mode = fs::symlink_metadata(&fifo).unwrap().permissions().mode() & 0o777;
    assert_eq!(mode, 0o644);
    fs::remove_dir_all(dir).unwrap();
}

/// Windows: a private folder or file gets its access list as it is made, so a parent whose
/// inherited entries stop an owner from changing an access list afterwards does not stop it.
#[cfg(windows)]
#[test]
fn a_private_folder_or_file_is_private_from_the_moment_it_is_made() {
    let dir = temp("owner-rights");
    // OWNER RIGHTS (S-1-3-4) may not change the access list of what is made in dir.
    assert!(icacls(&dir, &["/deny", "*S-1-3-4:(OI)(CI)(IO)(WDAC)"]));
    let folder = dir.join("folder");
    create_private_dir(&folder).unwrap();
    assert!(is_private(&folder).unwrap());
    let file = dir.join("file");
    create_new_private(&file)
        .unwrap()
        .write_all(b"secret")
        .unwrap();
    assert!(is_private(&file).unwrap());
    assert_eq!(fs::read(&file).unwrap(), b"secret");
    fs::remove_dir_all(dir).unwrap();
}

/// Windows: an access list that is not protected (a later change to the folder's would reach it),
/// or an owner other than this user or SYSTEM (who could change the list), is not private.
#[cfg(windows)]
#[test]
fn an_inherited_list_or_another_owner_is_not_private() {
    let dir = temp("protected");
    let folder = dir.join("folder");
    create_private_dir(&folder).unwrap();
    let plain = folder.join("plain");
    fs::write(&plain, b"x").unwrap();
    assert!(!is_private(&plain).unwrap(), "an inherited list");
    make_private(&plain).unwrap();
    assert!(is_private(&plain).unwrap());
    let file = folder.join("file");
    drop(create_new_private(&file).unwrap());
    assert!(is_private(&file).unwrap());
    // Handed to the Administrators group, where this account may do so.
    if icacls(&file, &["/setowner", "*S-1-5-32-544"]) {
        assert!(
            !is_private(&file).unwrap(),
            "owned by the Administrators group"
        );
        make_private(&file).unwrap();
        assert!(is_private(&file).unwrap());
    } else {
        eprintln!("skipped: this account cannot hand a file to the Administrators group");
    }
    fs::remove_dir_all(dir).unwrap();
}
