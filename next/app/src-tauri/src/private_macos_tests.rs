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
    // After: a new file inherits none; clearing again changes nothing.
    let after = inner.join("after");
    fs::write(&after, b"x").unwrap();
    assert!(!has_entries(&fs::File::open(&after).unwrap()).unwrap());
    clear_entries(&folder).unwrap();
    // A file is cleared through its own descriptor too.
    clear_entries(&fs::File::open(&before).unwrap()).unwrap();
    assert!(!has_entries(&fs::File::open(&before).unwrap()).unwrap());
    fs::remove_dir_all(d).unwrap();
}
