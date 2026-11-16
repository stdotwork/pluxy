//! The execution program's launch modes on Windows, run as the app runs them.
#![cfg(windows)]

use std::io::Write;
use std::process::{Command, Output, Stdio};

/// The execution program run with args, its input Enter.
fn run(args: &[&str]) -> Output {
    let mut c = Command::new(env!("CARGO_BIN_EXE_execution"))
        .args(args)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    c.stdin.take().unwrap().write_all(b"\n").unwrap();
    c.wait_with_output().unwrap()
}

#[test]
fn a_console_launch_runs_the_program_with_its_arguments_then_says_how_it_ended() {
    // The program is the execution program again, asked for a terminal kind there is none of:
    // it says so with its arguments as it got them, and ends with code 2.
    let me = env!("CARGO_BIN_EXE_execution");
    let hostile = r#"nope "x" & calc ^| %PATH% \"#;
    let out = run(&[
        "terminal", "--with", "console", "--", me, "terminal", "--with", hostile, "--", "x",
    ]);
    let stdout = String::from_utf8_lossy(&out.stdout);
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert_eq!(out.status.code(), Some(2), "{stdout}{stderr}");
    assert!(
        stderr.contains(&format!("pluxy: no terminal {hostile}\n")),
        "{stderr}"
    );
    assert!(
        stdout.contains("[execution.exe exited with code 2]"),
        "{stdout}"
    );
    assert!(
        stdout.contains("Press Enter to close this window."),
        "{stdout}"
    );
}

#[test]
fn the_shell_a_window_stays_at_is_system32s_whatever_the_launchs_system_root() {
    // A launch environment whose SystemRoot names a folder with a System32\cmd.exe of its own
    // (the execution program again, which would say "not a launch this program makes").
    let me = env!("CARGO_BIN_EXE_execution");
    let root = std::env::temp_dir().join(format!("pluxy-system-root-{}", std::process::id()));
    std::fs::create_dir_all(root.join("System32")).unwrap();
    std::fs::copy(me, root.join(r"System32\cmd.exe")).unwrap();
    let mut c = Command::new(me)
        .args([
            "terminal", "--with", "cmd", "--", me, "terminal", "--with", "nope", "--", "x",
        ])
        .env("SystemRoot", &root)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    c.stdin
        .take()
        .unwrap()
        .write_all(b"echo pluxy-shell-ran\r\nexit\r\n")
        .unwrap();
    let out = c.wait_with_output().unwrap();
    let stdout = String::from_utf8_lossy(&out.stdout);
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        !stderr.contains("not a launch this program makes"),
        "{stderr}"
    );
    assert!(
        stdout.contains("[execution.exe exited with code 2]"),
        "{stdout}"
    );
    assert!(stdout.contains("pluxy-shell-ran"), "{stdout}{stderr}");
    assert_eq!(out.status.code(), Some(2));
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn the_open_and_app_modes_refuse_what_they_do_not_open() {
    for (args, why) in [
        (
            vec!["open", "--", r"C:\Windows\notepad.exe"],
            "is not a file pluxy opens",
        ),
        (
            vec!["open", "--", "relative.txt"],
            "is not a file pluxy opens",
        ),
        (vec!["app", "--", "notepad"], "is not an app's ID"),
        (
            vec!["open", "--", "a.txt", "b.txt"],
            "not a launch this program makes",
        ),
    ] {
        let out = run(&args);
        let stderr = String::from_utf8_lossy(&out.stderr);
        assert_eq!(out.status.code(), Some(1), "{args:?}: {stderr}");
        assert!(stderr.contains(why), "{args:?}: {stderr}");
    }
}

/// Opens a text file in the app Windows opens text with (Notepad on a new system), as an open
/// launch does. On GitHub's runners only: elsewhere it would open a window on the person's desktop.
#[test]
fn an_open_launch_opens_a_text_file() {
    if std::env::var_os("GITHUB_ACTIONS").is_none() {
        println!("skipped: opens a window; runs on GitHub's runners only");
        return;
    }
    let d = std::env::temp_dir().join(format!("pluxy-open-{}", std::process::id()));
    std::fs::create_dir_all(&d).unwrap();
    let file = d.join("notes.txt");
    std::fs::write(&file, b"pluxy\r\n").unwrap();
    let out = run(&["open", "--", file.to_str().unwrap()]);
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
}
