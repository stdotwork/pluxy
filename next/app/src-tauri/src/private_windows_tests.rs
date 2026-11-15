use super::*;
use std::fs;
use std::os::windows::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};
use std::process::Command;
use windows_sys::Win32::Storage::FileSystem::{
    FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT, FILE_READ_ATTRIBUTES, READ_CONTROL,
    WRITE_DAC,
};

/// A folder of its own under the temporary folder, removed when dropped.
pub(crate) struct Temp(pub(crate) PathBuf);

impl Drop for Temp {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

pub(crate) fn temp() -> Temp {
    let d = std::env::temp_dir().join(format!("pluxy-private-{}", crate::managed::random_key()));
    fs::create_dir(&d).unwrap();
    Temp(d)
}

/// The file or folder at p itself (a link is opened, not followed), to read and change its list.
pub(crate) fn open(p: &Path) -> File {
    fs::OpenOptions::new()
        .access_mode(FILE_READ_ATTRIBUTES | READ_CONTROL | WRITE_DAC)
        .custom_flags(FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT)
        .open(p)
        .unwrap_or_else(|e| panic!("{}: {e}", p.display()))
}

pub(crate) fn private(p: &Path) -> bool {
    is_private(&open(p)).unwrap()
}

/// Changes p's list with icacls (as a person would).
pub(crate) fn icacls(p: &Path, args: &[&str]) -> bool {
    let out = Command::new("icacls").arg(p).args(args).output().unwrap();
    if !out.status.success() {
        eprintln!(
            "icacls {} {args:?}: {}{}",
            p.display(),
            String::from_utf8_lossy(&out.stdout),
            String::from_utf8_lossy(&out.stderr)
        );
    }
    out.status.success()
}

/// Lets everyone read p, by an entry of its own.
pub(crate) fn readable_by_everyone(p: &Path) {
    assert!(icacls(p, &["/grant", "*S-1-1-0:(R)"]));
}

/// Makes link a junction to the folder target (which needs no privilege, unlike a symlink).
pub(crate) fn junction(target: &Path, link: &Path) {
    let out = Command::new("cmd")
        .args(["/C", "mklink", "/J"])
        .arg(link)
        .arg(target)
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "mklink /J: {}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
}

#[test]
fn a_folder_and_a_file_made_private_follow_the_rule() {
    let t = temp();
    let dir = t.0.join("dir");
    fs::create_dir(&dir).unwrap();
    // A new folder takes its list from its parent's: not protected, so not private.
    assert!(!private(&dir));
    make_private_dir(&dir).unwrap();
    assert!(private(&dir));
    // A file made in it takes the folder's entries, but is not protected until made private.
    let file = dir.join("f");
    fs::write(&file, b"x").unwrap();
    assert!(!private(&file));
    make_private(&open(&file), false).unwrap();
    assert!(private(&file));
    assert!(owned(&open(&file)).unwrap());
}

#[test]
fn what_is_made_with_the_private_descriptor_is_private_from_the_start() {
    use windows_sys::Win32::Security::SECURITY_ATTRIBUTES;
    use windows_sys::Win32::Storage::FileSystem::CreateDirectoryW;
    let t = temp();
    let dir = t.0.join("made");
    let p = Private::new(true).unwrap();
    let sa = SECURITY_ATTRIBUTES {
        nLength: std::mem::size_of::<SECURITY_ATTRIBUTES>() as u32,
        lpSecurityDescriptor: p.descriptor(),
        bInheritHandle: 0,
    };
    let name: Vec<u16> = std::os::windows::ffi::OsStrExt::encode_wide(dir.as_os_str())
        .chain([0])
        .collect();
    // SAFETY: name is NUL-terminated; sa and its descriptor outlive the call.
    assert_ne!(unsafe { CreateDirectoryW(name.as_ptr(), &sa) }, 0);
    assert!(private(&dir));
}

#[test]
fn an_allow_entry_for_anyone_else_is_not_private_inherit_only_included() {
    let t = temp();
    for (name, grant) in [
        ("plain", "*S-1-1-0:(R)"),
        ("inherit-only", "*S-1-1-0:(OI)(CI)(IO)(R)"),
        ("users", "*S-1-5-32-545:(RX)"),
    ] {
        let dir = t.0.join(name);
        fs::create_dir(&dir).unwrap();
        make_private_dir(&dir).unwrap();
        assert!(private(&dir), "{name}");
        assert!(icacls(&dir, &["/grant", grant]), "{name}");
        assert!(!private(&dir), "{name}");
        // Making it private again takes the entry away.
        make_private_dir(&dir).unwrap();
        assert!(private(&dir), "{name}");
    }
}

#[test]
fn deny_entries_are_ignored() {
    let t = temp();
    let dir = t.0.join("dir");
    fs::create_dir(&dir).unwrap();
    make_private_dir(&dir).unwrap();
    assert!(icacls(&dir, &["/deny", "*S-1-1-0:(W)"]));
    assert!(private(&dir));
}

#[test]
fn a_list_that_takes_its_folders_entries_is_not_private() {
    let t = temp();
    let dir = t.0.join("dir");
    fs::create_dir(&dir).unwrap();
    make_private_dir(&dir).unwrap();
    assert!(icacls(&dir, &["/inheritance:e"]));
    assert!(!private(&dir));
}

#[test]
fn a_link_is_refused_and_what_it_names_left_alone() {
    let t = temp();
    let target = t.0.join("target");
    fs::create_dir(&target).unwrap();
    let link = t.0.join("link");
    junction(&target, &link);
    let e = make_private_dir(&link).unwrap_err();
    assert_eq!(e.kind(), io::ErrorKind::InvalidInput, "{e}");
    assert!(!private(&target));
    // A file is not a folder.
    let file = t.0.join("file");
    fs::write(&file, b"x").unwrap();
    assert!(make_private_dir(&file).is_err());
}

#[test]
fn another_owner_is_refused_never_taken_over() {
    let t = temp();
    let dir = t.0.join("dir");
    fs::create_dir(&dir).unwrap();
    make_private_dir(&dir).unwrap();
    // LocalService: an account that is not this one, the default owner or SYSTEM. Naming
    // another owner takes the restore privilege, which an administrator has.
    if !icacls(&dir, &["/setowner", "*S-1-5-19"]) {
        eprintln!("SKIPPED: this account cannot give a folder to another owner");
        return;
    }
    assert!(!owned(&open(&dir)).unwrap());
    assert!(!private(&dir));
    let e = make_private(&open(&dir), true).unwrap_err();
    assert_eq!(e.kind(), io::ErrorKind::PermissionDenied, "{e}");
    assert!(make_private_dir(&dir).is_err());
    // Still LocalService's: refused, not taken over.
    assert!(!owned(&open(&dir)).unwrap());
}
