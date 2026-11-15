use super::*;

fn launcher() -> Launcher {
    let dir = std::env::temp_dir().join(format!(
        "pluxy-launcher-{}-{}",
        std::process::id(),
        now_ms()
    ));
    fs::create_dir_all(&dir).unwrap();
    Launcher::open(dir.join(FILE))
}

#[test]
fn remembering_a_launch_again_hands_back_the_approval_it_replaces() {
    let mut l = launcher();
    let input = json!({"executable": "/bin/true", "argv": [], "cwd": "/"});
    let (id, old) = l.keep("client", &input, "a-1");
    assert_eq!(old, None);
    // The same launch remembered again (after a change asked again): one entry, the old
    // approval handed back for revoking.
    let (again, old) = l.keep("client", &input, "a-2");
    assert_eq!((again.as_str(), old.as_deref()), (id.as_str(), Some("a-1")));
    assert_eq!(l.store.entries.len(), 1);
    assert_eq!(l.store.entries[0].approval, "a-2");
    // A one-click start that confirmed afresh replaces it too; replaying the same one does not.
    assert_eq!(l.replace(&id, "a-3").as_deref(), Some("a-2"));
    assert_eq!(l.replace(&id, "a-3"), None);
    assert_eq!(l.replace("l-none", "a-4"), None);
}
