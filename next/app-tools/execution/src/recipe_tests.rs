use super::*;
use std::os::unix::fs::PermissionsExt;

/// A folder with executables named names (and a non-executable `plain`).
fn bin(names: &[&str]) -> PathBuf {
    let d = std::env::temp_dir().join(crate::test_folder_name("pluxy-execution"));
    std::fs::create_dir_all(&d).unwrap();
    for n in names.iter().chain(&["plain"]) {
        let p = d.join(n);
        std::fs::write(&p, b"#!/bin/sh\n").unwrap();
        let mode = if *n == "plain" { 0o644 } else { 0o755 };
        std::fs::set_permissions(&p, std::fs::Permissions::from_mode(mode)).unwrap();
    }
    d
}

#[test]
fn terminals_lists_automatic_then_those_found_in_order() {
    let d = bin(&["xterm", "konsole", "agent"]);
    let t = terminals(&json!({"search_path": ["relative", d]}));
    let ids: Vec<&str> = t["terminals"]
        .as_array()
        .unwrap()
        .iter()
        .map(|t| t["id"].as_str().unwrap())
        .collect();
    assert_eq!(ids, ["automatic", "konsole", "xterm"]);
    assert_eq!(t["terminals"][0]["label"], "Automatic");
    assert_eq!(t["terminals"][1]["path"], json!(d.join("konsole")));
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_terminal_launch_runs_the_program_found_by_name_after_the_terminals_flags() {
    let d = bin(&["xterm", "gnome-terminal", "agent"]);
    let cwd = d.to_string_lossy().into_owned();
    let base = json!({"kind": "terminal", "program": "agent", "args": ["--x", "a b"], "cwd": cwd, "search_path": [d]});
    // Automatic: gnome-terminal comes before xterm.
    let r = prepare(&base).unwrap();
    let agent = d.join("agent").to_string_lossy().into_owned();
    assert_eq!(r["executable"], json!(d.join("gnome-terminal")));
    assert_eq!(r["argv"], json!(["--", agent, "--x", "a b"]));
    assert_eq!(r["program"], json!(agent));
    assert_eq!(r["cwd"], json!(cwd));
    let mut x = base.clone();
    x["terminal"] = json!("xterm");
    assert_eq!(
        prepare(&x).unwrap()["argv"],
        json!(["-e", agent, "--x", "a b"])
    );
    x["terminal"] = json!("kitty");
    assert_eq!(prepare(&x).unwrap_err().code, "not_found");
    x["terminal"] = json!("my-terminal");
    assert_eq!(prepare(&x).unwrap_err().code, "invalid_request");
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_direct_launch_runs_the_program_or_its_override() {
    let d = bin(&["agent", "other"]);
    let cwd = d.to_string_lossy().into_owned();
    let mut i = json!({"kind": "direct", "program": "agent", "args": ["a"], "cwd": cwd, "search_path": [d]});
    let r = prepare(&i).unwrap();
    assert_eq!(r["executable"], json!(d.join("agent")));
    assert_eq!(r["argv"], json!(["a"]));
    assert!(r.get("program").is_none());
    i["override"] = json!(d.join("other"));
    assert_eq!(prepare(&i).unwrap()["executable"], json!(d.join("other")));
    for bad in [
        json!("other"),
        json!(d.join("plain")),
        json!(d.join("missing")),
    ] {
        i["override"] = bad;
        assert!(prepare(&i).is_err());
    }
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn programs_and_folders_that_do_not_fit_are_refused() {
    let d = bin(&["agent", "xterm"]);
    let cwd = d.to_string_lossy().into_owned();
    let ok = json!({"kind": "terminal", "program": "agent", "cwd": cwd, "search_path": [d]});
    assert!(prepare(&ok).is_ok());
    for (k, v) in [
        ("program", json!("missing")),
        ("program", json!("plain")),
        ("program", json!("sub/agent")),
        ("cwd", json!("relative")),
        ("cwd", json!(d.join("agent"))),
        ("kind", json!("shell")),
        ("args", json!(["a\0b"])),
    ] {
        let mut i = ok.clone();
        i[k] = v;
        assert!(prepare(&i).is_err(), "{k} accepted");
    }
    // No search path: a name is found nowhere.
    let mut i = ok.clone();
    i["search_path"] = json!([]);
    assert_eq!(prepare(&i).unwrap_err().code, "not_found");
    std::fs::remove_dir_all(d).unwrap();
}

/// A folder to open files in, with a session file, an executable file and a link to a file
/// outside it; and that file outside.
fn files() -> (PathBuf, PathBuf, PathBuf) {
    let root = std::fs::canonicalize(bin(&[])).unwrap();
    let within = root.join("codex");
    let sessions = within.join("sessions");
    std::fs::create_dir_all(&sessions).unwrap();
    // Whatever the umask: an open launch wants its folders the user's alone.
    for d in [&within, &sessions] {
        std::fs::set_permissions(d, std::fs::Permissions::from_mode(0o700)).unwrap();
    }
    let file = sessions.join("rollout-a.jsonl");
    std::fs::write(&file, b"{}\n").unwrap();
    std::fs::set_permissions(&file, std::fs::Permissions::from_mode(0o600)).unwrap();
    std::fs::write(sessions.join("run.sh"), b"#!/bin/sh\n").unwrap();
    std::fs::set_permissions(
        sessions.join("run.sh"),
        std::fs::Permissions::from_mode(0o755),
    )
    .unwrap();
    let outside = root.join("private.txt");
    std::fs::write(&outside, b"x\n").unwrap();
    std::os::unix::fs::symlink(&outside, sessions.join("link.jsonl")).unwrap();
    (root, within, file)
}

/// The fields an open launch takes in a release build.
const OPEN: &[&str] = &["kind", "file", "within"];

#[test]
fn an_open_launch_runs_the_opener_with_the_resolved_file_in_its_folder() {
    let (root, within, file) = files();
    let openers = bin(&["xdg-open", "gio"]);
    let s = |p: &Path| p.to_string_lossy().into_owned();
    // Named through `..`: the resolved file is the argument.
    let named = within.join("sessions/../sessions/rollout-a.jsonl");
    let i = json!({"kind": "open", "file": s(&named), "within": s(&within)});
    assert_eq!(
        open_from(&i, OPEN, std::slice::from_ref(&openers)).unwrap(),
        json!({
            "executable": s(&openers.join("xdg-open")), "argv": [s(&file)],
            "cwd": s(&within.join("sessions")), "file": s(&file), "within": s(&within),
        })
    );
    // With no xdg-open, gio's `open`.
    let gio = bin(&["gio"]);
    let r = open_from(&i, OPEN, std::slice::from_ref(&gio)).unwrap();
    assert_eq!(r["executable"], json!(s(&gio.join("gio"))));
    assert_eq!(r["argv"], json!(["open", s(&file)]));
    // Neither: not found.
    assert_eq!(
        open_from(&i, OPEN, std::slice::from_ref(&root))
            .unwrap_err()
            .code,
        "not_found"
    );
    for d in [root, openers, gio] {
        std::fs::remove_dir_all(d).unwrap();
    }
}

#[test]
fn an_open_launch_takes_no_search_path_in_a_release_build() {
    let (root, within, file) = files();
    let s = |p: &Path| p.to_string_lossy().into_owned();
    let i =
        json!({"kind": "open", "file": s(&file), "within": s(&within), "search_path": [s(&root)]});
    let e = prepare(&i).unwrap_err();
    if cfg!(feature = "native-test") {
        // The stand-in's way: the search path names no opener here.
        assert_eq!(e.code, "not_found", "{}", e.message);
    } else {
        assert!(e.message.contains("not search_path"), "{}", e.message);
    }
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn an_open_launch_names_only_a_plain_file_inside_its_folder() {
    let (root, within, file) = files();
    let openers = bin(&["xdg-open"]);
    let s = |p: &Path| p.to_string_lossy().into_owned();
    let ok = json!({"kind": "open", "file": s(&file), "within": s(&within)});
    let from = std::slice::from_ref(&openers);
    assert!(open_from(&ok, OPEN, from).is_ok());
    let sessions = within.join("sessions");
    let mut cases = vec![
        ("file", json!(s(&root.join("private.txt"))), "inside"),
        ("file", json!(s(&sessions.join("link.jsonl"))), "inside"),
        ("file", json!(s(&within.join("../private.txt"))), "inside"),
        ("file", json!(s(&sessions.join("run.sh"))), "executable"),
        ("file", json!(s(&sessions)), "regular file"),
        ("file", json!("rollout-a.jsonl"), "absolute"),
        ("within", json!("/"), "cannot be /"),
        ("within", json!(s(&file)), "not a folder"),
        ("program", json!("agent"), "not program"),
    ];
    // The home folder and every folder above it.
    if let Some(home) = std::env::var_os("HOME").and_then(|h| std::fs::canonicalize(h).ok()) {
        for d in home.ancestors() {
            cases.push(("within", json!(s(d)), "a folder above it"));
        }
    }
    for (k, v, says) in cases {
        let mut i = ok.clone();
        i[k] = v.clone();
        let e = open_from(&i, OPEN, from).unwrap_err();
        assert!(e.message.contains(says), "{k} {v}: {}", e.message);
    }
    std::fs::remove_dir_all(root).unwrap();
    std::fs::remove_dir_all(openers).unwrap();
}

#[test]
fn an_open_launch_wants_every_folder_up_to_its_folder_to_be_the_users_own() {
    let (root, within, _) = files();
    let openers = bin(&["xdg-open"]);
    let from = std::slice::from_ref(&openers);
    let s = |p: &Path| p.to_string_lossy().into_owned();
    let sessions = within.join("sessions");
    let deep = sessions.join("2026/11");
    std::fs::create_dir_all(&deep).unwrap();
    for d in [sessions.join("2026"), deep.clone()] {
        std::fs::set_permissions(&d, std::fs::Permissions::from_mode(0o700)).unwrap();
    }
    let nested = deep.join("rollout-b.jsonl");
    std::fs::write(&nested, b"{}\n").unwrap();
    std::fs::set_permissions(&nested, std::fs::Permissions::from_mode(0o600)).unwrap();
    let open = || {
        let i = json!({"kind": "open", "file": s(&nested), "within": s(&within)});
        open_from(&i, OPEN, from)
    };
    // A clean nested path passes.
    assert!(open().is_ok());
    // A folder between the file and its folder, or the folder itself, that group or others may
    // write is refused.
    for (folder, mode) in [
        (sessions.clone(), 0o770),
        (sessions.join("2026"), 0o707),
        (deep.clone(), 0o777),
        (within.clone(), 0o770),
    ] {
        std::fs::set_permissions(&folder, std::fs::Permissions::from_mode(mode)).unwrap();
        let e = open()
            .err()
            .unwrap_or_else(|| panic!("{} at {mode:o} was accepted", folder.display()));
        assert_eq!(e.code, "invalid_request");
        assert!(
            e.message.contains("writable by no one else")
                && e.message.contains(&folder.display().to_string()),
            "{}: {}",
            folder.display(),
            e.message
        );
        std::fs::set_permissions(&folder, std::fs::Permissions::from_mode(0o700)).unwrap();
    }
    assert!(open().is_ok());
    std::fs::remove_dir_all(root).unwrap();
    std::fs::remove_dir_all(openers).unwrap();
}

#[test]
fn a_terminal_or_direct_launch_still_names_its_program_cwd_and_search_path() {
    let d = bin(&["agent"]);
    let cwd = d.to_string_lossy().into_owned();
    let ok = json!({"kind": "direct", "program": "agent", "cwd": cwd, "search_path": [d]});
    assert!(prepare(&ok).is_ok());
    for k in ["program", "cwd", "search_path"] {
        let mut i = ok.clone();
        i.as_object_mut().unwrap().remove(k);
        assert_eq!(prepare(&i).unwrap_err().code, "invalid_request", "{k}");
    }
    std::fs::remove_dir_all(d).unwrap();
}
