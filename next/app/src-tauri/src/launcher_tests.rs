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
    let (id, old) = l.keep("client", &input, None, "a-1");
    assert_eq!(old, None);
    // The same launch remembered again (after a change asked again): one entry, the old
    // approval handed back for revoking.
    let (again, old) = l.keep("client", &input, None, "a-2");
    assert_eq!((again.as_str(), old.as_deref()), (id.as_str(), Some("a-1")));
    assert_eq!(l.store.entries.len(), 1);
    assert_eq!(l.store.entries[0].approval, "a-2");
    // A one-click start that confirmed afresh replaces it too; replaying the same one does not.
    assert_eq!(l.replace(&id, "a-3").as_deref(), Some("a-2"));
    assert_eq!(l.replace(&id, "a-3"), None);
    assert_eq!(l.replace("l-none", "a-4"), None);
}

/// The execution program gets PATH's absolute folders, split as the system splits PATH.
#[cfg(unix)]
#[test]
fn search_path_keeps_paths_absolute_folders_on_unix() {
    let entries = path_entries(std::ffi::OsStr::new("/usr/local/bin::bin:./x:/usr/bin"));
    assert_eq!(entries, ["/usr/local/bin", "/usr/bin"]);
}

/// On Windows PATH is split at `;`, an entry may be quoted, and a folder starts with its drive (or
/// is a UNC path); a drive-relative or relative entry is left out.
#[cfg(windows)]
#[test]
fn search_path_keeps_paths_absolute_folders_on_windows() {
    let entries = path_entries(std::ffi::OsStr::new(
        r#"C:\Windows\system32;;"C:\Program Files\Git\cmd";bin;C:rel;\\server\share\tools;\noroot"#,
    ));
    assert_eq!(
        entries,
        [
            r"C:\Windows\system32",
            r"C:\Program Files\Git\cmd",
            r"\\server\share\tools"
        ]
    );
}

fn bob() -> Record {
    Record::from_check(
        "Ubuntu",
        "bob",
        &json!({"distro": "Ubuntu", "user": "bob", "uid": 1001, "home": "/home/bob",
            "arch": "amd64", "automount": "/mnt/", "drives": []}),
        0,
    )
    .unwrap()
}

/// What `pluxy/wsl`'s prepare gives for a launch of claude in /home/bob/p.
fn prepared(kind: &str) -> Value {
    json!({"launch": {"executable": r"C:\Windows\System32\wsl.exe",
        "argv": ["-d", "Ubuntu", "-u", "bob", "--cd", "/home/bob/p", "--exec", "/usr/local/bin/claude"],
        "cwd": r"C:\Users\bob", "program": "/usr/local/bin/claude"}, "kind": kind})
}

#[test]
fn a_launch_in_wsl_is_prepared_there_and_names_no_terminal() {
    let req = json!({"client": "Claude Code", "kind": "terminal", "program": "claude",
        "args": ["--x"], "cwd": "/home/bob/p"});
    assert_eq!(
        wsl_prepare_input(&req, &bob()).unwrap(),
        json!({"distro": "Ubuntu", "user": "bob", "kind": "terminal", "program": "claude",
            "args": ["--x"], "cwd": "/home/bob/p"})
    );
    let mut t = req.clone();
    t["terminal"] = json!("automatic");
    assert!(wsl_prepare_input(&t, &bob())
        .unwrap_err()
        .contains("Windows console"));
    let wsl = Environment::wsl("Ubuntu", "bob").unwrap();
    assert_eq!(
        client_in("Claude Code", &wsl),
        "Claude Code (WSL Ubuntu/bob)"
    );
    assert_eq!(client_in("Claude Code", &Environment::Local), "Claude Code");
}

#[test]
fn a_launch_in_wsl_gets_its_kind_applied_and_its_linux_program_only_shown() {
    let req = json!({"client": "c", "kind": "terminal", "cwd": "/home/bob/p",
        "environment": {"set": {"A": "1"}}});
    let (input, program) = wsl_launch_input(&prepared("terminal"), &req).unwrap();
    assert_eq!(program, "/usr/local/bin/claude");
    // The host cannot open a file in the distribution: no `program` for the confirmation to pin.
    assert!(input.get("program").is_none());
    assert_eq!(input["executable"], r"C:\Windows\System32\wsl.exe");
    assert_eq!(input["environment"], json!({"set": {"A": "1"}}));
    // A terminal launch: no standard input, detached only.
    let mut stdin = req.clone();
    stdin["stdin"] = json!("key");
    assert!(wsl_launch_input(&prepared("terminal"), &stdin)
        .unwrap_err()
        .contains("keyboard"));
    let mut managed = req.clone();
    managed["lifetime"] = json!("managed");
    assert!(wsl_launch_input(&prepared("terminal"), &managed)
        .unwrap_err()
        .contains("detached only"));
    // A direct one may be managed (its own wsl.exe job ends the Linux side with it).
    let (input, _) = wsl_launch_input(&prepared("direct"), &managed).unwrap();
    assert_eq!(input["lifetime"], "managed");
    assert!(wsl_launch_input(&prepared("open"), &req).is_err());
    let line = wsl_launch_line(&bob(), &program);
    assert_eq!(
        line,
        "Starts in WSL: distribution Ubuntu, user bob (/home/bob). The program inside WSL \
         (/usr/local/bin/claude) is found when it starts; pluxy does not check it."
    );
}

#[test]
fn a_remembered_launch_in_wsl_keeps_its_line_and_its_folder_there() {
    let mut l = launcher();
    let input = json!({"executable": "wsl.exe", "argv": [], "cwd": "C:\\"});
    let line = "Starts in WSL: distribution Ubuntu, user bob (/home/bob).";
    let (bob_id, _) = l.keep(
        "c (WSL Ubuntu/bob)",
        &input,
        Some((line, "/home/bob/p")),
        "a-1",
    );
    // The same input without the line is another launch: it never stands for the WSL one.
    let (plain, old) = l.keep("c", &input, None, "a-2");
    assert_ne!(plain, bob_id);
    assert_eq!(old, None);
    let e = l.store.entries.iter().find(|e| e.id == bob_id).unwrap();
    assert_eq!(e.shown.as_deref(), Some(line));
    assert_eq!(e.folder.as_deref(), Some("/home/bob/p"));
    // Kept in the file.
    l.save().unwrap();
    let again = Launcher::open(l.file.clone());
    assert!(again
        .store
        .entries
        .iter()
        .any(|e| e.id == bob_id && e.shown.as_deref() == Some(line)));
}
