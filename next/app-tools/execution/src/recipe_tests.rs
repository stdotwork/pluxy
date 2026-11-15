use super::*;
use std::os::unix::fs::PermissionsExt;

/// A folder with executables named names (and a non-executable `plain`).
fn bin(names: &[&str]) -> PathBuf {
    let d = std::env::temp_dir().join(format!(
        "pluxy-execution-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
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
