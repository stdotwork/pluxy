//! The execution program's launch modes on macOS, run as the app runs them.
#![cfg(target_os = "macos")]

use std::process::Command;
use std::time::{Duration, Instant};

const TERMINAL: &str = "/System/Applications/Utilities/Terminal.app";

fn helper() -> Command {
    Command::new(env!("CARGO_BIN_EXE_execution"))
}

#[test]
fn the_terminal_mode_hands_over_to_terminal_only_and_attach_takes_only_a_live_launch() {
    let out = helper()
        .args([
            "terminal",
            "--with",
            "terminal",
            "--app",
            "/System/Applications/Calculator.app",
        ])
        .args(["--", "/bin/echo"])
        .output()
        .unwrap();
    assert_eq!(out.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&out.stderr).contains("is not Terminal"));
    let out = helper()
        .args(["attach", "/nonexistent/pluxy-launch/s"])
        .output()
        .unwrap();
    assert_eq!(out.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&out.stderr).contains("launch expired or already taken"));
}

#[test]
fn a_launch_too_large_to_hand_over_is_refused_before_terminal_opens() {
    // 200,000 control characters: 1.2 MB in the hand-over (over its 1 MiB), within macOS'
    // argument limit.
    let out = helper()
        .args([
            "terminal",
            "--with",
            "terminal",
            "--app",
            TERMINAL,
            "--",
            "/bin/echo",
        ])
        .arg("\u{1}".repeat(200_000))
        .output()
        .unwrap();
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert_eq!(out.status.code(), Some(1), "{stderr}");
    assert!(stderr.contains("the launch is too large"), "{stderr}");
}

/// Opens a Terminal window: on GitHub's runners only, elsewhere it would open one on the
/// person's desktop.
#[test]
fn a_terminal_launch_runs_the_program_in_a_terminal_window_in_its_folder_with_its_environment() {
    if std::env::var_os("GITHUB_ACTIONS").is_none() {
        println!("skipped: opens a Terminal window; runs on GitHub's runners only");
        return;
    }
    let d = std::env::temp_dir().join(format!("pluxy-terminal-{}", std::process::id()));
    std::fs::create_dir_all(&d).unwrap();
    let d = std::fs::canonicalize(d).unwrap();
    let out = d.join("out");
    let value = r#"a 'b' "c" $HOME `x`"#;
    let script =
        r#"pwd -P > "$1.part"; printf '%s' "$PLUXY_TEST_VALUE" >> "$1.part"; mv "$1.part" "$1""#;
    let status = helper()
        .args([
            "terminal", "--with", "terminal", "--app", TERMINAL, "--", "/bin/sh", "-c",
        ])
        .args([script, "sh"])
        .arg(&out)
        .current_dir(&d)
        .env("PLUXY_TEST_VALUE", value)
        .status()
        .unwrap();
    assert!(status.success(), "{status}");
    let end = Instant::now() + Duration::from_secs(30);
    while !out.exists() && Instant::now() < end {
        std::thread::sleep(Duration::from_millis(100));
    }
    assert_eq!(
        std::fs::read_to_string(&out).unwrap(),
        format!("{}\n{value}", d.display())
    );
    std::fs::remove_dir_all(d).unwrap();
}
