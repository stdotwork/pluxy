//! macOS' launch recipes, on real files in a temporary folder.

use super::*;
use std::ffi::CString;
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};

const TERMINAL: &str = "/System/Applications/Utilities/Terminal.app";

/// A new folder for one test, resolved (`/var` is `/private/var`), the user's alone.
fn folder() -> PathBuf {
    let d = std::env::temp_dir().join(crate::test_folder_name("pluxy-execution"));
    std::fs::create_dir_all(&d).unwrap();
    std::fs::set_permissions(&d, std::fs::Permissions::from_mode(0o700)).unwrap();
    std::fs::canonicalize(d).unwrap()
}

fn s(p: &Path) -> String {
    p.to_str().unwrap().to_string()
}

#[test]
fn the_terminal_is_terminal() {
    assert_eq!(
        terminals(&json!({"search_path": []}))["terminals"],
        json!([
            {"id": "automatic", "label": "Automatic"},
            {"id": "terminal", "label": "Terminal", "path": TERMINAL},
        ])
    );
}

#[test]
fn a_terminal_launch_hands_the_program_to_terminal_through_the_execution_program() {
    let d = folder();
    let mut i = json!({"kind": "terminal", "program": "/bin/echo", "args": ["a b", "it's $HOME"],
        "cwd": d, "search_path": []});
    let want = json!({"executable": os::this_program().unwrap(),
        "argv": ["terminal", "--with", "terminal", "--app", TERMINAL, "--", "/bin/echo", "a b",
            "it's $HOME"],
        "cwd": s(&d), "program": "/bin/echo"});
    assert_eq!(prepare(&i).unwrap(), want);
    i["terminal"] = json!("terminal");
    assert_eq!(prepare(&i).unwrap(), want);
    // iTerm2 and Ghostty are not offered yet; Linux's terminals are not macOS'.
    for t in ["iterm2", "ghostty", "x-terminal-emulator"] {
        i["terminal"] = json!(t);
        let e = prepare(&i).unwrap_err();
        assert_eq!(e.code, "invalid_request", "{t}: {}", e.message);
    }
    std::fs::remove_dir_all(d).unwrap();
}

/// A folder to open files in, with a text file; and the folder above it.
fn files() -> (PathBuf, PathBuf, PathBuf) {
    let root = folder();
    let within = root.join("codex");
    let sessions = within.join("sessions");
    std::fs::create_dir_all(&sessions).unwrap();
    for d in [&within, &sessions] {
        std::fs::set_permissions(d, std::fs::Permissions::from_mode(0o700)).unwrap();
    }
    let file = sessions.join("notes.txt");
    std::fs::write(&file, b"pluxy\n").unwrap();
    std::fs::set_permissions(&file, std::fs::Permissions::from_mode(0o600)).unwrap();
    (root, within, file)
}

fn set_attribute(file: &Path, name: &str, value: &[u8]) {
    let f = CString::new(file.as_os_str().as_bytes()).unwrap();
    let n = CString::new(name).unwrap();
    // SAFETY: both strings are NUL-terminated and value holds value.len() bytes.
    let r = unsafe {
        libc::setxattr(
            f.as_ptr(),
            n.as_ptr(),
            value.as_ptr().cast(),
            value.len(),
            0,
            libc::XATTR_NOFOLLOW,
        )
    };
    assert_eq!(r, 0, "{name}: {}", std::io::Error::last_os_error());
}

#[test]
fn an_open_launch_runs_open_on_the_resolved_file() {
    let (root, within, file) = files();
    let named = within.join("sessions/../sessions/notes.txt");
    assert_eq!(
        prepare(&json!({"kind": "open", "file": named, "within": within})).unwrap(),
        json!({"executable": "/usr/bin/open", "argv": [s(&file)],
            "cwd": s(&within.join("sessions")), "file": s(&file), "within": s(&within)})
    );
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn an_open_launch_refuses_a_file_that_can_choose_its_own_app() {
    // Type and creator codes (TextEdit's text), then the rest of the 32 bytes.
    let finder_info = [&b"TEXTttxt"[..], &[0u8; 24]].concat();
    for (name, value) in [
        ("com.apple.FinderInfo", finder_info),
        ("com.apple.ResourceFork", b"\0\0\x01\0".to_vec()),
        (
            "com.apple.LaunchServices.OpenWith",
            b"bplist00\xd1\x01\x02".to_vec(),
        ),
    ] {
        let (root, within, file) = files();
        set_attribute(&file, name, &value);
        let e = prepare(&json!({"kind": "open", "file": file, "within": within})).unwrap_err();
        assert_eq!(e.code, "invalid_request", "{name}: {}", e.message);
        assert!(e.message.contains(name), "{}", e.message);
        std::fs::remove_dir_all(root).unwrap();
    }
    // Other attributes, such as a quarantine, choose no app.
    let (root, within, file) = files();
    set_attribute(&file, "com.apple.quarantine", b"0081;00000000;pluxy;");
    assert!(prepare(&json!({"kind": "open", "file": file, "within": within})).is_ok());
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn an_app_in_an_applications_folder_starts_through_open() {
    let d = folder();
    let calculator = "/System/Applications/Calculator.app";
    let mut i =
        json!({"kind": "direct", "program": calculator, "args": [], "cwd": d, "search_path": []});
    assert_eq!(
        prepare(&i).unwrap(),
        json!({"executable": "/usr/bin/open", "argv": ["-a", calculator], "cwd": s(&d)})
    );
    assert!(os::is_app_launch(
        "/usr/bin/open",
        &[json!("-a"), json!(calculator)]
    ));
    i["args"] = json!(["--x"]);
    assert_eq!(prepare(&i).unwrap_err().code, "invalid_request");
    // A bundle elsewhere, and a folder named .app that is no bundle.
    let elsewhere = d.join("Tool.app");
    std::fs::create_dir_all(elsewhere.join("Contents")).unwrap();
    std::fs::write(elsewhere.join("Contents/Info.plist"), b"<plist/>").unwrap();
    std::fs::create_dir_all(d.join("Empty.app")).unwrap();
    for (p, why) in [
        (&elsewhere, "must be in"),
        (&d.join("Empty.app"), "not an application"),
    ] {
        let i = json!({"kind": "direct", "program": p, "args": [], "cwd": d, "search_path": []});
        let e = prepare(&i).unwrap_err();
        assert_eq!(e.code, "invalid_request", "{}", e.message);
        assert!(e.message.contains(why), "{}", e.message);
    }
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_terminal_launch_too_large_to_hand_over_is_refused() {
    let d = folder();
    // Each control character is six bytes in the hand-over: 1.44 MB, over its 1 MiB.
    let args: Vec<String> = (0..8).map(|_| "\u{1}".repeat(30_000)).collect();
    let e = prepare(
        &json!({"kind": "terminal", "program": "/bin/echo", "args": args, "cwd": d,
        "search_path": []}),
    )
    .unwrap_err();
    assert_eq!(e.code, "invalid_request", "{}", e.message);
    assert!(e.message.contains("too large"), "{}", e.message);
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn an_open_launch_is_refused_for_a_type_no_app_opens() {
    let (root, within, _) = files();
    let file = within.join("sessions/rollout-a.jsonl");
    std::fs::write(&file, b"{}\n").unwrap();
    std::fs::set_permissions(&file, std::fs::Permissions::from_mode(0o600)).unwrap();
    let r = prepare(&json!({"kind": "open", "file": file, "within": within}));
    // No app claims .jsonl on a new macOS (`open` fails with kLSApplicationNotFoundErr);
    // elsewhere this Mac's own file types decide.
    if std::env::var_os("GITHUB_ACTIONS").is_some() || !os::opens(&file) {
        let e = r.unwrap_err();
        assert_eq!(e.code, "not_found", "{}", e.message);
        assert!(
            e.message
                .starts_with("no app opens .jsonl files on this Mac"),
            "{}",
            e.message
        );
    } else {
        assert!(r.is_ok(), "{r:?}");
    }
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn an_open_launch_is_refused_for_an_executable_file_or_a_folder_others_may_write() {
    let (root, within, file) = files();
    let open = json!({"kind": "open", "file": file, "within": within});
    assert!(prepare(&open).is_ok());
    std::fs::set_permissions(&file, std::fs::Permissions::from_mode(0o700)).unwrap();
    let e = prepare(&open).unwrap_err();
    assert!(
        e.message.contains("must not be executable"),
        "{}",
        e.message
    );
    std::fs::set_permissions(&file, std::fs::Permissions::from_mode(0o600)).unwrap();
    for (folder, mode) in [(within.join("sessions"), 0o777), (within.clone(), 0o770)] {
        std::fs::set_permissions(&folder, std::fs::Permissions::from_mode(mode)).unwrap();
        let e = prepare(&open).unwrap_err();
        assert_eq!(e.code, "invalid_request", "{}", e.message);
        assert!(
            e.message.contains(&format!(
                "the folder {} of the file to open",
                folder.display()
            )),
            "{}",
            e.message
        );
        std::fs::set_permissions(&folder, std::fs::Permissions::from_mode(0o700)).unwrap();
    }
    std::fs::remove_dir_all(root).unwrap();
}
