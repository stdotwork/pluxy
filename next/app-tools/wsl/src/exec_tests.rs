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
