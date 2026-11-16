//! The runner of wsl.exe, on Windows' own programs in System32 (no stand-in for wsl.exe: these
//! check the runner's own rules, whatever program it runs).

use super::*;

fn system(name: &str) -> PathBuf {
    system32().unwrap().join(name)
}

fn never() -> bool {
    false
}

#[test]
fn a_program_runs_in_system32_with_only_the_windows_variables() {
    let args = ["/d".to_string(), "/c".to_string(), "cd & set".to_string()];
    let deadline = Instant::now() + Duration::from_secs(30);
    let out = once(&system("cmd.exe"), &args, 64 << 10, deadline, &never).unwrap();
    assert_eq!(out.code, 0);
    let text = String::from_utf8_lossy(&out.stdout).to_lowercase();
    let mut lines = text.lines();
    assert_eq!(
        lines.next().map(str::trim),
        Some(
            system32()
                .unwrap()
                .to_str()
                .unwrap()
                .to_lowercase()
                .as_str()
        )
    );
    let names: Vec<&str> = lines
        .filter_map(|l| l.split_once('=').map(|(k, _)| k))
        .collect();
    for n in ["systemroot", "windir", "wsl_utf8"] {
        assert!(names.contains(&n), "{text}");
    }
    assert!(!names.contains(&"userprofile"), "{text}");
}

/// ping, which writes a line a second for about 30 seconds.
fn ping() -> (PathBuf, Vec<String>) {
    let a = ["-n", "30", "127.0.0.1"]
        .iter()
        .map(|s| s.to_string())
        .collect();
    (system("PING.EXE"), a)
}

#[test]
fn a_program_past_its_deadline_is_ended() {
    let (exe, args) = ping();
    let start = Instant::now();
    let e = once(&exe, &args, 1 << 20, start + Duration::from_secs(2), &never)
        .err()
        .expect("ping ran past its deadline");
    assert_eq!(e.message, "WSL did not answer in time; try again");
    assert!(
        start.elapsed() < Duration::from_secs(10),
        "{:?}",
        start.elapsed()
    );
}

#[test]
fn a_program_that_writes_more_than_its_cap_is_ended() {
    let (exe, args) = ping();
    let start = Instant::now();
    let e = once(&exe, &args, 64, start + Duration::from_secs(60), &never)
        .err()
        .expect("ping wrote past its cap");
    assert_eq!(e.message, "wsl.exe wrote more than expected");
    assert!(
        start.elapsed() < Duration::from_secs(20),
        "{:?}",
        start.elapsed()
    );
}

#[test]
fn a_cancelled_call_ends_its_program() {
    let (exe, args) = ping();
    let start = Instant::now();
    let after = move || start.elapsed() > Duration::from_millis(500);
    let e = once(
        &exe,
        &args,
        1 << 20,
        start + Duration::from_secs(60),
        &after,
    )
    .err()
    .expect("ping ran on after the cancel");
    assert_eq!(e.code, "cancelled");
    assert!(
        start.elapsed() < Duration::from_secs(10),
        "{:?}",
        start.elapsed()
    );
}
