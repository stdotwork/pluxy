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
    // A file made in a private folder by plain std is private too on Windows (inherited); on
    // Linux and macOS its mode decides.
    #[cfg(windows)]
    {
        let plain = folder.join("plain");
        fs::write(&plain, b"x").unwrap();
        assert!(is_private(&plain).unwrap());
    }
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
        let out = std::process::Command::new("icacls")
            .arg(p)
            .args(["/grant", "*S-1-1-0:(R)"])
            .output()
            .unwrap();
        assert!(out.status.success(), "{out:?}");
    }
    assert!(!is_private(&folder).unwrap());
    assert!(!is_private(&file).unwrap());
    make_private(&folder).unwrap();
    make_private(&file).unwrap();
    assert!(is_private(&folder).unwrap());
    assert!(is_private(&file).unwrap());
    // What is made in the folder afterwards inherits it on Windows.
    #[cfg(windows)]
    {
        fs::write(folder.join("later"), b"x").unwrap();
        assert!(is_private(&folder.join("later")).unwrap());
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
