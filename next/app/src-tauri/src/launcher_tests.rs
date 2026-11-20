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

/// The Windows folder whose System32 holds Windows' own `wsl.exe` in these tests.
fn windows() -> Option<&'static Path> {
    Some(Path::new(r"C:\Windows"))
}

/// The home folder the app gives its tools in these tests, where `wsl.exe` starts.
fn home() -> Option<&'static Path> {
    Some(Path::new(r"C:\Users\bob"))
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
    let (input, program) =
        wsl_launch_input(&prepared("terminal"), &req, &bob(), windows(), home()).unwrap();
    assert_eq!(program, "/usr/local/bin/claude");
    // The host cannot open a file in the distribution: no `program` for the confirmation to pin.
    assert!(input.get("program").is_none());
    assert_eq!(input["executable"], r"C:\Windows\System32\wsl.exe");
    assert_eq!(input["environment"], json!({"set": {"A": "1"}}));
    // A terminal launch: no standard input, detached only.
    let mut stdin = req.clone();
    stdin["stdin"] = json!("key");
    assert!(
        wsl_launch_input(&prepared("terminal"), &stdin, &bob(), windows(), home())
            .unwrap_err()
            .contains("keyboard")
    );
    let mut managed = req.clone();
    managed["lifetime"] = json!("managed");
    assert!(
        wsl_launch_input(&prepared("terminal"), &managed, &bob(), windows(), home())
            .unwrap_err()
            .contains("detached only")
    );
    // A direct one may be managed (its own wsl.exe job ends the Linux side with it).
    managed["kind"] = json!("direct");
    let (input, _) =
        wsl_launch_input(&prepared("direct"), &managed, &bob(), windows(), home()).unwrap();
    assert_eq!(input["lifetime"], "managed");
    assert!(wsl_launch_input(&prepared("open"), &req, &bob(), windows(), home()).is_err());
    let line = wsl_launch_line(&bob(), &program);
    assert_eq!(
        line,
        "Starts in WSL: distribution \"Ubuntu\", user \"bob\" (/home/bob). The program inside WSL \
         (/usr/local/bin/claude) is found when it starts; pluxy does not check it."
    );
}

/// The launch `pluxy/wsl` prepared runs only as the request and the record say: Windows' own
/// `wsl.exe`, `-d <distro> -u <user> --cd <folder> --exec <program>` then the request's arguments,
/// in the requested folder and of the requested kind. Any other reply is refused, so the
/// confirmation's line (from the record) never names another distribution, user or program.
#[test]
fn a_launch_in_wsl_is_refused_unless_it_is_the_one_asked_for() {
    let req = json!({"client": "c", "kind": "terminal", "program": "claude",
        "args": ["--x", "a b"], "cwd": "/home/bob/p"});
    let good = || {
        let mut p = prepared("terminal");
        p["launch"]["argv"] = json!([
            "-d",
            "Ubuntu",
            "-u",
            "bob",
            "--cd",
            "/home/bob/p",
            "--exec",
            "/usr/local/bin/claude",
            "--x",
            "a b"
        ]);
        p
    };
    let (input, program) = wsl_launch_input(&good(), &req, &bob(), windows(), home()).unwrap();
    assert_eq!(program, "/usr/local/bin/claude");
    assert_eq!(input["argv"], good()["launch"]["argv"]);
    // System32's wsl.exe as Windows compares paths: another case or `\\?\` is the same file.
    let mut p = good();
    p["launch"]["executable"] = json!(r"\\?\c:\windows\system32\WSL.EXE");
    assert!(wsl_launch_input(&p, &req, &bob(), windows(), home()).is_ok());
    let refused = |p: &Value, req: &Value, why: &str| {
        let e = wsl_launch_input(p, req, &bob(), windows(), home()).unwrap_err();
        assert!(e.contains(why), "{e} (expected {why})");
    };
    // Another executable, or no system root to compare with.
    for exe in [
        r"C:\Users\bob\wsl.exe",
        r"C:\Windows\System32\cmd.exe",
        "wsl.exe",
    ] {
        let mut p = good();
        p["launch"]["executable"] = json!(exe);
        refused(&p, &req, "not Windows' own wsl.exe");
    }
    assert!(wsl_launch_input(&good(), &req, &bob(), None, home())
        .unwrap_err()
        .contains("not Windows' own wsl.exe"));
    // Another kind than asked for.
    let mut p = good();
    p["kind"] = json!("direct");
    refused(&p, &req, "another kind");
    // argv: another distribution, user, folder or program, an option added, an argument changed.
    let argv = |a: Value| {
        let mut p = good();
        p["launch"]["argv"] = a;
        p
    };
    for a in [
        json!([
            "-d",
            "Debian",
            "-u",
            "bob",
            "--cd",
            "/home/bob/p",
            "--exec",
            "/usr/local/bin/claude",
            "--x",
            "a b"
        ]),
        json!([
            "-d",
            "Ubuntu",
            "-u",
            "root",
            "--cd",
            "/home/bob/p",
            "--exec",
            "/usr/local/bin/claude",
            "--x",
            "a b"
        ]),
        json!([
            "-d",
            "Ubuntu",
            "-u",
            "bob",
            "--cd",
            "/root",
            "--exec",
            "/usr/local/bin/claude",
            "--x",
            "a b"
        ]),
        json!([
            "-d",
            "Ubuntu",
            "-u",
            "bob",
            "--cd",
            "/home/bob/p",
            "--exec",
            "/bin/sh",
            "--x",
            "a b"
        ]),
        json!([
            "-d",
            "Ubuntu",
            "-u",
            "bob",
            "-e",
            "/bin/sh",
            "--cd",
            "/home/bob/p",
            "--exec",
            "/usr/local/bin/claude",
            "--x",
            "a b"
        ]),
        json!([
            "-d",
            "Ubuntu",
            "-u",
            "bob",
            "--cd",
            "/home/bob/p",
            "--exec",
            "/usr/local/bin/claude",
            "--x",
            "a b",
            "--y"
        ]),
        json!([
            "-d",
            "Ubuntu",
            "-u",
            "bob",
            "--cd",
            "/home/bob/p",
            "--exec",
            "/usr/local/bin/claude",
            "--x"
        ]),
        json!(["-d", "Ubuntu", "-u", "bob", "--cd", "/home/bob/p", "--exec"]),
    ] {
        refused(&argv(a), &req, "not the launch asked for");
    }
    // A launch with anything more than the four fields pluxy/wsl prepares (a file to open, a
    // folder it must be within, a lifetime, environment, input or key the request did not ask
    // for), or that starts wsl.exe in another folder than the app's home.
    for (k, v) in [
        ("file", json!(r"C:\x.txt")),
        ("within", json!(r"C:\")),
        ("lifetime", json!("managed")),
        ("environment", json!({"set": {"A": "1"}})),
        ("stdin", json!("key")),
        ("key", json!("s-1")),
    ] {
        let mut p = good();
        p["launch"][k] = v;
        refused(&p, &req, &format!("prepared a launch with {k}"));
    }
    let mut p = good();
    p["launch"]["cwd"] = json!(r"C:\Windows\Temp");
    refused(&p, &req, "starts wsl.exe in");
    let mut p = good();
    p["launch"].as_object_mut().unwrap().remove("cwd");
    refused(&p, &req, "starts wsl.exe in");
    assert!(wsl_launch_input(&good(), &req, &bob(), windows(), None)
        .unwrap_err()
        .contains("starts wsl.exe in"));
    // No folder in the reply and none known for the app: refused too, not taken as a match.
    assert!(wsl_launch_input(&p, &req, &bob(), windows(), None)
        .unwrap_err()
        .contains("starts wsl.exe in no folder"));
    // A program that is not an absolute path inside WSL.
    let mut p = good();
    p["launch"]["argv"][7] = json!("claude");
    p["launch"]["program"] = json!("claude");
    refused(&p, &req, "not the launch asked for");
    // The program named an override for runs that file, and with no folder asked for, any
    // absolute folder (the user's home) is taken.
    let over = json!({"client": "c", "kind": "terminal", "override": "/opt/c/claude"});
    let mut p = good();
    p["launch"]["argv"] = json!([
        "-d",
        "Ubuntu",
        "-u",
        "bob",
        "--cd",
        "/home/bob",
        "--exec",
        "/opt/c/claude"
    ]);
    p["launch"]["program"] = json!("/opt/c/claude");
    assert!(wsl_launch_input(&p, &over, &bob(), windows(), home()).is_ok());
    p["launch"]["argv"][7] = json!("/usr/local/bin/claude");
    p["launch"]["program"] = json!("/usr/local/bin/claude");
    refused(&p, &over, "not the launch asked for");
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

/// Removing a WSL environment forgets its remembered launches (handing back their approvals for
/// revoking) and its recent folders: none of them starts again in one click, even in a
/// distribution or user made again under the same names. Other environments' and this computer's
/// stay.
#[test]
fn removing_an_environment_forgets_its_launches_and_folders() {
    let mut l = launcher();
    let bob = Environment::wsl("Ubuntu", "bob").unwrap();
    let alice = Environment::wsl("Ubuntu", "alice").unwrap();
    let (cb, ca) = (client_in("c", &bob), client_in("c", &alice));
    let input = |u: &str| json!({"executable": "wsl.exe", "argv": ["-d", "Ubuntu", "-u", u], "cwd": "C:\\"});
    let (b, _) = l.keep(
        &cb,
        &input("bob"),
        Some(("line bob", "/home/bob/p")),
        "a-bob",
    );
    let (b2, _) = l.keep(
        &client_in("d", &bob),
        &input("bob2"),
        Some(("line bob", "/home/bob")),
        "a-bob2",
    );
    let (a, _) = l.keep(
        &ca,
        &input("alice"),
        Some(("line alice", "/home/alice")),
        "a-alice",
    );
    let local = json!({"executable": "/bin/true", "argv": [], "cwd": "/"});
    let (here, _) = l.keep("c", &local, None, "a-here");
    for (c, f) in [
        (cb.as_str(), "/home/bob/p"),
        (ca.as_str(), "/home/alice"),
        ("c", "/"),
    ] {
        l.remember_folder(c, f);
    }
    let mut gone = l.forget_environment(&bob);
    gone.sort();
    assert_eq!(gone, ["a-bob", "a-bob2"]);
    let ids: Vec<&str> = l.store.entries.iter().map(|e| e.id.as_str()).collect();
    assert_eq!(ids, [a.as_str(), here.as_str()]);
    assert!(!ids.contains(&b.as_str()) && !ids.contains(&b2.as_str()));
    let kept: Vec<&str> = l.store.history.keys().map(String::as_str).collect();
    assert_eq!(kept, ["c", ca.as_str()]);
    // This computer is no environment to remove.
    assert!(l.forget_environment(&Environment::Local).is_empty());
    assert_eq!(l.store.entries.len(), 2);
}

/// A launch in WSL runs only what `pluxy/wsl` prepared for it, after the check, and its
/// confirmation gets the line naming the record's distribution, user and home and the program;
/// the launcher remembers it with that line and the folder in the distribution.
#[test]
fn a_launch_in_wsl_is_confirmed_with_the_environments_line() {
    use std::cell::RefCell;
    let req = json!({"client": "c", "kind": "terminal", "program": "claude", "cwd": "/home/bob/p"});
    let asked = RefCell::new(None);
    let shown = RefCell::new(None);
    let w = run_in_wsl(
        &req,
        &bob(),
        windows(),
        home(),
        |input| {
            *asked.borrow_mut() = Some(input);
            Ok(prepared("terminal"))
        },
        |input, line| {
            assert_eq!(input["executable"], r"C:\Windows\System32\wsl.exe");
            *shown.borrow_mut() = Some(line);
            Ok((json!({"lifetime": "detached"}), Some("a-1".into())))
        },
    )
    .unwrap();
    assert_eq!(
        asked.into_inner(),
        Some(wsl_prepare_input(&req, &bob()).unwrap())
    );
    let line =
        "Starts in WSL: distribution \"Ubuntu\", user \"bob\" (/home/bob). The program inside \
                WSL (/usr/local/bin/claude) is found when it starts; pluxy does not check it.";
    assert_eq!(shown.into_inner().as_deref(), Some(line));
    assert_eq!(w.line, line);
    assert_eq!(w.folder, "/home/bob/p");
    assert_eq!(w.approval.as_deref(), Some("a-1"));
    assert_eq!(w.output, json!({"lifetime": "detached"}));
    assert_eq!(w.input["argv"], prepared("terminal")["launch"]["argv"]);
    // A prepared launch that is not the one asked for never reaches execution.
    let mut other = prepared("terminal");
    other["launch"]["argv"][3] = json!("root");
    let e = run_in_wsl(
        &req,
        &bob(),
        windows(),
        home(),
        |_| Ok(other),
        |_, _| panic!("a launch that is not the one asked for ran"),
    )
    .err()
    .unwrap();
    assert!(e.contains("not the launch asked for"), "{e}");
}
