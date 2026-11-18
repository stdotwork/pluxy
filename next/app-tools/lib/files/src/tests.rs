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
fn names_windows_would_read_as_another_file_are_told() {
    for ok in [
        "settings.json",
        ".claude",
        "a b.json",
        "com10",
        "COM0",
        "nulx.txt",
        "lpt",
        "con-fig",
        "x.nul",
    ] {
        assert_eq!(windows_name_problem(ok), None, "{ok}");
    }
    for bad in [
        "settings.json.",
        "settings.json ",
        "dir.",
        "settings.json:stream",
        "a:b",
        "a*b",
        "a?b",
        "a|b",
        "a\u{1}b",
        "NUL",
        "nul.json",
        "Nul .json",
        "CON",
        "aux.txt",
        "PRN",
        "COM1",
        "com9.log",
        "LPT1",
        "lpt²",
        "conin$",
        "CONOUT$.x",
    ] {
        assert!(windows_name_problem(bad).is_some(), "{bad}");
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

/// A file this process has just made is made private through its handle (as the transactions
/// library makes its temporary files before writing a byte).
#[test]
fn a_file_just_made_is_made_private_through_its_handle() {
    let dir = temp("private-open");
    let path = dir.join("f");
    let mut opts = fs::OpenOptions::new();
    opts.write(true).create_new(true);
    #[cfg(windows)]
    {
        use std::os::windows::fs::OpenOptionsExt;
        use windows_sys::Win32::Foundation::{GENERIC_READ, GENERIC_WRITE};
        use windows_sys::Win32::Storage::FileSystem::WRITE_DAC;
        opts.access_mode(GENERIC_READ | GENERIC_WRITE | WRITE_DAC);
    }
    let f = opts.open(&path).unwrap();
    assert!(!is_private(&path).unwrap());
    make_private_open(&f).unwrap();
    assert!(is_private(&path).unwrap());
    drop(f);
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

/// Windows: a folder whose inherited entries deny OWNER RIGHTS (S-1-3-4) the right to change an
/// access list forbids whoever makes something in it to set its list. Windows then refuses any
/// creation that brings its own security descriptor (Access is denied), and a list set afterwards,
/// so nothing private can be made there: making one is refused, and nothing is left behind. In a
/// folder without that entry, the same calls make private ones.
#[cfg(windows)]
#[test]
fn a_folder_that_forbids_setting_access_lists_refuses_private_files() {
    let dir = temp("owner-rights");
    assert!(icacls(&dir, &["/deny", "*S-1-3-4:(OI)(CI)(IO)(WDAC)"]));
    let folder = dir.join("folder");
    let file = dir.join("file");
    let denied = |e: io::Error| assert_eq!(e.kind(), io::ErrorKind::PermissionDenied, "{e}");
    denied(create_private_dir(&folder).unwrap_err());
    denied(create_new_private(&file).unwrap_err());
    assert!(fs::symlink_metadata(&folder).is_err() && fs::symlink_metadata(&file).is_err());
    // Made plainly, a file there is not private, and make_private either refuses it or makes it
    // so: it never says it did when it did not.
    let plain = dir.join("plain");
    fs::write(&plain, b"x").unwrap();
    assert!(!is_private(&plain).unwrap());
    match make_private(&plain) {
        Ok(()) => assert!(is_private(&plain).unwrap()),
        Err(e) => {
            eprintln!("make_private in a folder that forbids setting access lists: {e}");
            assert!(!is_private(&plain).unwrap());
        }
    }
    // The positive control: the same calls in a folder without that entry.
    let open = temp("owner-rights-control");
    create_private_dir(&open.join("folder")).unwrap();
    drop(create_new_private(&open.join("file")).unwrap());
    assert!(is_private(&open.join("folder")).unwrap() && is_private(&open.join("file")).unwrap());
    fs::remove_dir_all(dir).unwrap();
    fs::remove_dir_all(open).unwrap();
}

/// Windows: an access list that is not protected (a later change to the folder's would reach it),
/// or an owner other than this user, the owner this process gives what it makes or SYSTEM (who could
/// change the list), is not private.
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
    // Handed to the Users group (which takes the restore privilege an elevated administrator
    // has): not private, and refused by make_private, never taken over.
    if icacls(&file, &["/setowner", "*S-1-5-32-545"]) {
        assert!(!is_private(&file).unwrap(), "owned by the Users group");
        assert_eq!(
            make_private(&file).unwrap_err().kind(),
            io::ErrorKind::PermissionDenied
        );
        assert!(!is_private(&file).unwrap(), "taken over");
    } else {
        eprintln!("skipped: this account cannot hand a file to the Users group");
    }
    fs::remove_dir_all(dir).unwrap();
}

/// Windows: an access list's text without the `AI` flag, which Windows sets on a list given
/// through SetSecurityInfo (`D:P(…)` reads back `D:PAI(…)`, `D:(…)` as `D:AI(…)`) and which changes no
/// entry, inheritance or protection.
#[cfg(windows)]
fn without_ai(sddl: &str) -> String {
    match sddl.strip_prefix("D:").and_then(|s| s.split_once('(')) {
        Some((flags, rest)) => format!("D:{}({rest}", flags.replace("AI", "")),
        None => sddl.to_string(),
    }
}

/// Windows: a file's access list is read as SDDL and given back as it was: a protected one (the
/// file is then private), or its folder's again (inherited entries, not private). Anything that is
/// not a list, or a NULL one (which allows everyone), is refused and the list kept.
#[cfg(windows)]
#[test]
fn an_access_list_is_read_and_given_back() {
    use std::os::windows::fs::OpenOptionsExt;
    use windows_sys::Win32::Foundation::GENERIC_READ;
    use windows_sys::Win32::Storage::FileSystem::WRITE_DAC;
    let dir = temp("access-list");
    let (plain, private) = (dir.join("plain"), dir.join("private"));
    fs::write(&plain, b"x").unwrap();
    drop(create_new_private(&private).unwrap());
    let open = |p: &Path| {
        fs::OpenOptions::new()
            .access_mode(GENERIC_READ | WRITE_DAC)
            .open(p)
            .unwrap()
    };
    let inherited = access_list(&open(&plain)).unwrap();
    let protected = access_list(&open(&private)).unwrap();
    // The text alone: nothing of the system's buffer after it.
    assert!(
        !inherited.contains('\0') && !protected.contains('\0'),
        "{inherited:?} {protected:?}"
    );
    assert!(
        inherited.starts_with("D:") && !inherited.starts_with("D:P") && inherited.contains(";ID;"),
        "{inherited}"
    );
    assert!(
        protected.starts_with("D:P") && !protected.contains(";ID;"),
        "{protected}"
    );
    set_access_list(&open(&plain), &protected).unwrap();
    assert!(is_private(&plain).unwrap());
    assert_eq!(
        without_ai(&access_list(&open(&plain)).unwrap()),
        without_ai(&protected)
    );
    set_access_list(&open(&plain), &inherited).unwrap();
    assert!(!is_private(&plain).unwrap());
    assert_eq!(
        without_ai(&access_list(&open(&plain)).unwrap()),
        without_ai(&inherited)
    );
    for bad in ["not an access list", "D:NO_ACCESS_CONTROL", "O:SY", "D:P\0"] {
        assert!(set_access_list(&open(&plain), bad).is_err(), "{bad}");
    }
    assert_eq!(
        without_ai(&access_list(&open(&plain)).unwrap()),
        without_ai(&inherited)
    );
    fs::remove_dir_all(dir).unwrap();
}

/// Windows: create_private_in makes a file in a folder's handle that is private from the start and
/// that no other handle can open while it is held; an existing name or a name that is a path is
/// refused, and in a folder that forbids setting access lists, so is the creation.
#[cfg(windows)]
#[test]
fn a_private_file_is_made_in_a_folders_handle() {
    use std::os::windows::fs::OpenOptionsExt;
    use windows_sys::Win32::Storage::FileSystem::FILE_FLAG_BACKUP_SEMANTICS;
    let dir = temp("private-in");
    let open_dir = |d: &Path| {
        fs::OpenOptions::new()
            .read(true)
            .custom_flags(FILE_FLAG_BACKUP_SEMANTICS)
            .open(d)
            .unwrap()
    };
    let folder = open_dir(&dir);
    let mut f = create_private_in(&folder, "tmp".as_ref()).unwrap();
    f.write_all(b"secret").unwrap();
    assert!(is_private(&dir.join("tmp")).unwrap());
    let shared = File::open(dir.join("tmp")).unwrap_err();
    assert_eq!(shared.raw_os_error(), Some(32), "{shared}"); // ERROR_SHARING_VIOLATION
    assert_eq!(
        create_private_in(&folder, "tmp".as_ref())
            .unwrap_err()
            .kind(),
        io::ErrorKind::AlreadyExists
    );
    for bad in ["", ".", "..", r"sub\x", "sub/x"] {
        assert_eq!(
            create_private_in(&folder, bad.as_ref()).unwrap_err().kind(),
            io::ErrorKind::InvalidInput,
            "{bad:?}"
        );
    }
    drop(f);
    assert_eq!(fs::read(dir.join("tmp")).unwrap(), b"secret");
    // A folder that forbids setting access lists: refused, nothing left.
    let forbids = dir.join("forbids");
    fs::create_dir(&forbids).unwrap();
    assert!(icacls(&forbids, &["/deny", "*S-1-3-4:(OI)(CI)(IO)(WDAC)"]));
    let e = create_private_in(&open_dir(&forbids), "tmp".as_ref()).unwrap_err();
    assert_eq!(e.kind(), io::ErrorKind::PermissionDenied, "{e}");
    assert!(fs::symlink_metadata(forbids.join("tmp")).is_err());
    fs::remove_dir_all(dir).unwrap();
}

/// Windows: rename_open renames a file through its own handle (one shared with no one) into the
/// folder open as folder, over a file another handle still has open; a name that is a path, or
/// empty, is refused.
#[cfg(windows)]
#[test]
fn rename_open_replaces_a_file_through_the_handles() {
    use std::os::windows::fs::OpenOptionsExt;
    use windows_sys::Win32::Foundation::{GENERIC_READ, GENERIC_WRITE};
    use windows_sys::Win32::Storage::FileSystem::{DELETE, FILE_FLAG_BACKUP_SEMANTICS};
    let dir = temp("rename-open");
    let target = dir.join("target");
    fs::write(&target, b"old").unwrap();
    let mut held = File::open(&target).unwrap();
    let mut f = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .access_mode(GENERIC_READ | GENERIC_WRITE | DELETE)
        .share_mode(0)
        .open(dir.join("tmp"))
        .unwrap();
    f.write_all(b"new").unwrap();
    let folder = fs::OpenOptions::new()
        .read(true)
        .custom_flags(FILE_FLAG_BACKUP_SEMANTICS)
        .open(&dir)
        .unwrap();
    for bad in ["", ".", "..", r"sub\target", "sub/target"] {
        assert_eq!(
            rename_open(&f, &folder, bad.as_ref()).unwrap_err().kind(),
            io::ErrorKind::InvalidInput,
            "{bad:?}"
        );
    }
    rename_open(&f, &folder, "target".as_ref()).unwrap();
    drop(f);
    assert_eq!(fs::read(&target).unwrap(), b"new");
    assert!(!dir.join("tmp").exists());
    // The handle open on the replaced file still reads it.
    let mut old = String::new();
    held.read_to_string(&mut old).unwrap();
    assert_eq!(old, "old");
    drop(held);
    fs::remove_dir_all(dir).unwrap();
}

/// Windows: a handle on something that is not a file on a disk (the NUL device) is not made
/// private.
#[cfg(windows)]
#[test]
fn make_private_open_refuses_what_is_not_on_a_disk() {
    let nul = fs::OpenOptions::new().write(true).open(r"\\.\NUL").unwrap();
    assert!(make_private_open(&nul).is_err());
}

/// Windows: a folder in the user's temporary folder may be changed by no one but the user, SYSTEM
/// and Administrators. Letting everyone read it changes nothing; letting everyone write it, or
/// handing it to the Users group, lets others change it; an entry only passed down to what it
/// holds does not apply to the folder itself.
#[cfg(windows)]
#[test]
fn a_folder_others_may_change_is_told_apart() {
    let dir = temp("others");
    assert!(!others_may_write(&dir).unwrap(), "the user's own");
    let folder = |name: &str, grant: &[&str]| {
        let d = dir.join(name);
        fs::create_dir(&d).unwrap();
        assert!(icacls(&d, grant));
        d
    };
    let read = folder("read", &["/grant", "*S-1-1-0:(R)"]);
    assert!(!others_may_write(&read).unwrap(), "everyone may read");
    let write = folder("write", &["/grant", "*S-1-1-0:(W)"]);
    assert!(others_may_write(&write).unwrap(), "everyone may write");
    let below = folder("below", &["/grant", "*S-1-1-0:(OI)(CI)(IO)(W)"]);
    assert!(!others_may_write(&below).unwrap(), "only what it holds");
    assert!(others_may_write(&below.join("x")).is_err(), "no such file");
    fs::create_dir(below.join("x")).unwrap();
    assert!(others_may_write(&below.join("x")).unwrap(), "what it holds");
    let owned = dir.join("owned");
    fs::create_dir(&owned).unwrap();
    if icacls(&owned, &["/setowner", "*S-1-5-32-545"]) {
        assert!(
            others_may_write(&owned).unwrap(),
            "owned by the Users group"
        );
    } else {
        eprintln!("skipped: this account cannot hand a folder to the Users group");
    }
    fs::remove_dir_all(dir).unwrap();
}

#[cfg(any(windows, target_os = "macos"))]
#[test]
fn a_lock_is_made_held_and_taken_only_once_released() {
    let dir = temp("lock");
    let path = dir.join("a.lock");
    let held = create_locked(&path).unwrap();
    assert_eq!(
        create_locked(&path).err().unwrap().kind(),
        io::ErrorKind::AlreadyExists
    );
    assert!(take_lock(&path).unwrap().is_none(), "a held lock was taken");
    // On Windows no one else may open a held lock at all, so its access list is not read here.
    #[cfg(target_os = "macos")]
    assert!(is_private(&path).unwrap());
    drop(held);
    assert!(!path.exists(), "a released lock stayed");
    // A lock left behind with no holder (as a crash leaves one on macOS) can be taken, once.
    fs::write(&path, b"").unwrap();
    let taken = take_lock(&path)
        .unwrap()
        .expect("a free lock was not taken");
    assert!(
        take_lock(&path).unwrap().is_none(),
        "a taken lock was taken again"
    );
    drop(taken);
    assert!(!path.exists(), "a released lock stayed");
    fs::remove_dir_all(dir).unwrap();
}

/// Windows: a junction (a folder link any account may make) at path to target.
#[cfg(windows)]
fn junction(target: &Path, path: &Path) {
    let out = std::process::Command::new("cmd")
        .args(["/C", "mklink", "/J"])
        .arg(path)
        .arg(target)
        .output()
        .unwrap();
    assert!(out.status.success(), "{out:?}");
}

#[cfg(windows)]
#[test]
fn a_junction_is_not_opened_or_made_private_and_its_target_is_left_alone() {
    let dir = temp("junction");
    let target = dir.join("target");
    fs::create_dir(&target).unwrap();
    assert!(icacls(&target, &["/grant", "*S-1-1-0:(R)"]));
    let link = dir.join("link");
    junction(&target, &link);
    assert!(open_regular(&link).is_err());
    assert!(make_private(&link).is_err());
    // Nor through a handle open on the junction itself.
    {
        use std::os::windows::fs::OpenOptionsExt;
        use windows_sys::Win32::Storage::FileSystem::{
            FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT, FILE_READ_ATTRIBUTES,
            READ_CONTROL, WRITE_DAC,
        };
        let itself = fs::OpenOptions::new()
            .access_mode(FILE_READ_ATTRIBUTES | READ_CONTROL | WRITE_DAC)
            .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS)
            .open(&link)
            .unwrap();
        assert_eq!(
            make_private_open(&itself).unwrap_err().kind(),
            io::ErrorKind::InvalidInput
        );
    }
    assert!(
        !is_private(&target).unwrap(),
        "the junction's target was changed"
    );
    assert_ne!(id_of_path(&link).unwrap(), id_of_path(&target).unwrap());
    fs::remove_dir(&link).unwrap();
    fs::remove_dir_all(dir).unwrap();
}

/// Linux and macOS: another user's file is not private and is refused, even where this process
/// could change it (as root, the only account that can make one here).
#[cfg(unix)]
#[test]
fn another_users_file_is_not_private_and_is_refused() {
    use std::os::unix::fs::{MetadataExt, PermissionsExt};
    if unsafe { libc::geteuid() } != 0 {
        eprintln!("skipped: only root can give a file to another user");
        return;
    }
    let dir = temp("other-user");
    let file = dir.join("file");
    drop(create_new_private(&file).unwrap());
    std::os::unix::fs::chown(&file, Some(65534), None).unwrap();
    fs::set_permissions(&file, fs::Permissions::from_mode(0o644)).unwrap();
    assert!(!is_private(&file).unwrap());
    assert_eq!(
        make_private(&file).unwrap_err().kind(),
        io::ErrorKind::PermissionDenied
    );
    assert_eq!(fs::metadata(&file).unwrap().mode() & 0o777, 0o644);
    fs::set_permissions(&file, fs::Permissions::from_mode(0o600)).unwrap();
    assert!(!is_private(&file).unwrap(), "0600 but another user's");
    fs::remove_dir_all(dir).unwrap();
}

/// Linux and macOS: a file of the system's that only its owner may use, but whose owner is another
/// user (root's), is not private to this one.
#[cfg(unix)]
#[test]
fn a_file_only_another_user_may_use_is_not_private() {
    use std::os::unix::fs::{MetadataExt, PermissionsExt};
    let me = unsafe { libc::geteuid() };
    let theirs = [
        "/etc/shadow",
        "/etc/gshadow",
        "/etc/sudoers",
        "/var/lib/private",
        "/root",
        "/var/root",
        "/private/var/root",
    ]
    .into_iter()
    .map(Path::new)
    .find(|p| {
        fs::symlink_metadata(p).is_ok_and(|m| m.uid() != me && m.permissions().mode() & 0o077 == 0)
    });
    match theirs {
        Some(p) => assert!(!is_private(p).unwrap(), "{p:?}"),
        None => eprintln!("skipped: no file of another user with no group or other permission"),
    }
}

#[test]
fn a_program_is_a_file_this_system_would_start_by_its_path() {
    let dir = temp("executable");
    let named = |n: &str| {
        let p = dir.join(n);
        fs::write(&p, b"MZ").unwrap();
        p
    };
    // A folder, a missing file and a plain file are never programs.
    fs::create_dir(dir.join("folder.exe")).unwrap();
    assert!(!executable(&dir.join("folder.exe")));
    assert!(!executable(&dir.join("missing.exe")));
    let plain = named("tool");
    assert!(!executable(&plain));
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        // The extension means nothing here; an execute bit does, through a link too.
        assert!(!executable(&named("tool.exe")));
        fs::set_permissions(&plain, fs::Permissions::from_mode(0o700)).unwrap();
        assert!(executable(&plain));
        std::os::unix::fs::symlink(&plain, dir.join("link")).unwrap();
        assert!(executable(&dir.join("link")));
    }
    #[cfg(windows)]
    {
        // A PE image (starting MZ) named by its extension, in any case, and nothing else.
        assert!(executable(&named("tool.exe")));
        assert!(executable(&named("tool.COM")));
        assert!(!executable(&named("tool.cmd")));
        assert!(!executable(&named("tool.exe.txt")));
        // Named a program but not one: a script, an empty file, a single byte.
        for (name, bytes) in [
            ("script.exe", &b"#!/bin/sh"[..]),
            ("empty.exe", b""),
            ("short.com", b"M"),
        ] {
            let p = dir.join(name);
            fs::write(&p, bytes).unwrap();
            assert!(!executable(&p), "{name}");
        }
    }
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn a_folder_is_flushed_and_a_missing_one_is_an_error_on_unix() {
    let dir = temp("sync");
    sync_dir(&dir).unwrap();
    assert_eq!(cfg!(windows), sync_dir(&dir.join("missing")).is_ok());
    fs::remove_dir_all(dir).unwrap();
}
