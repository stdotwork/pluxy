use crate::{default_selections, pinned_read, settings_of, MISSING_LIBRARY};
use pluxy_program::Error;
use pluxy_transactions::{sha256_of, Selection};
use serde_json::{json, Value};
use std::cell::Cell;

fn tmp(name: &str) -> std::path::PathBuf {
    let d = std::env::temp_dir().join(format!("claude-desktop-test-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    d
}

#[test]
fn a_missing_library_is_named_for_the_folders_operation_whatever_is_missing_above_it() {
    let home = tmp("missing");
    let threep = home.join(".config/Claude-3p");
    // No ~/.config/Claude-3p at all, then the folder without its library: both name the library.
    for _ in 0..2 {
        let e = default_selections(Some(home.clone().into())).unwrap_err();
        assert!(e.message.contains(MISSING_LIBRARY), "{e}");
        assert!(e.message.contains("configLibrary"), "{e}");
        std::fs::create_dir_all(&threep).unwrap();
    }
    assert!(!threep.join("configLibrary").exists(), "a folder was made");
    // With the library: the three files.
    std::fs::create_dir(threep.join("configLibrary")).unwrap();
    let s = default_selections(Some(home.clone().into())).unwrap();
    assert_eq!(
        s[2],
        Selection::File(threep.join("configLibrary/_meta.json"))
    );
    assert!(default_selections(None).is_err());
    std::fs::remove_dir_all(home).unwrap();
}

#[test]
fn something_else_where_the_library_or_its_folder_should_be_is_not_called_missing() {
    // A link to nowhere as the library, or as the third-party folder: the preview names what it
    // finds instead of offering to create a folder over it.
    let home = tmp("dangling");
    let threep = home.join(".config/Claude-3p");
    std::fs::create_dir_all(&threep).unwrap();
    std::os::unix::fs::symlink(home.join("nowhere"), threep.join("configLibrary")).unwrap();
    assert!(default_selections(Some(home.clone().into())).is_ok());
    std::fs::remove_dir_all(&threep).unwrap();
    std::os::unix::fs::symlink(home.join("nowhere"), &threep).unwrap();
    assert!(default_selections(Some(home.clone().into())).is_ok());
    std::fs::remove_dir_all(home).unwrap();
}

#[test]
fn files_claude_desktop_would_not_take_are_reported_not_refused() {
    let broken = Some(b"{ // no\n}".to_vec());
    let v = settings_of(&[None, None, broken], None);
    assert_eq!(v["state"], "refused");
    assert!(v["reason"].as_str().unwrap().contains("_meta.json"), "{v}");
    let v = settings_of(&[None, None, None], None);
    assert_eq!(v["state"], "not-configured");
}

/// A preview whose pins say sha256 of pinned, and then the bytes read.
fn read_of(pinned: &[&[u8]; 3], read: &[&[u8]; 3]) -> (Value, [Option<Vec<u8>>; 3]) {
    (
        json!({"targets": pinned.iter().map(|p| json!({"sha256": sha256_of(Some(p))})).collect::<Vec<_>>()}),
        read.map(|r| Some(r.to_vec())),
    )
}

#[test]
fn settings_read_back_are_the_pinned_files_or_the_read_is_refused() {
    let n = Cell::new(0);
    let (_, bytes) = pinned_read(|| {
        n.set(n.get() + 1);
        Ok::<_, Error>(if n.get() == 1 {
            read_of(&[b"{}", b"{}", b"{}"], &[b"{}", b"{}", b"{\"x\": 1}"])
        } else {
            read_of(
                &[b"{}", b"{}", b"{\"x\": 1}"],
                &[b"{}", b"{}", b"{\"x\": 1}"],
            )
        })
    })
    .unwrap();
    assert_eq!(n.get(), 2);
    assert_eq!(bytes[2].as_deref(), Some(&b"{\"x\": 1}"[..]));
    let e =
        pinned_read(|| Ok(read_of(&[b"{}", b"{}", b"{}"], &[b"[]", b"{}", b"{}"]))).unwrap_err();
    assert!(e.message.contains("changed while it was read"), "{e}");
}
