//! The execution program's launch modes on Windows, run as the app runs them.
#![cfg(windows)]

use std::io::Write;
use std::path::{Path, PathBuf};
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

/// A new folder for one test, by its plain resolved path, in the user's temporary folder (which
/// no other account may change).
fn folder(name: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!("pluxy-launch-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    let r = std::fs::canonicalize(&d).unwrap();
    let r = r.to_str().unwrap();
    PathBuf::from(r.strip_prefix(r"\\?\").unwrap_or(r))
}

/// Runs icacls on p with args: whether it succeeded.
fn icacls(p: &Path, args: &[&str]) -> bool {
    let icacls =
        PathBuf::from(std::env::var_os("SystemRoot").unwrap()).join(r"System32\icacls.exe");
    let out = Command::new(icacls).arg(p).args(args).output().unwrap();
    out.status.success()
}

/// `<root>\apps\bin\agent.exe`, the execution program again, run in a console launch: as a program
/// it says "no terminal nope" and ends with 2.
struct Agent {
    root: PathBuf,
    above: PathBuf,
    bin: PathBuf,
    exe: PathBuf,
}

fn agent(name: &str) -> Agent {
    let root = folder(name);
    let above = root.join("apps");
    let bin = above.join("bin");
    std::fs::create_dir_all(&bin).unwrap();
    let exe = bin.join("agent.exe");
    std::fs::copy(env!("CARGO_BIN_EXE_execution"), &exe).unwrap();
    Agent {
        root,
        above,
        bin,
        exe,
    }
}

/// The console launch of program: its output, and whether program ran.
fn launch(program: &Path) -> (Output, bool) {
    let out = run(&[
        "terminal",
        "--with",
        "console",
        "--",
        program.to_str().unwrap(),
        "terminal",
        "--with",
        "nope",
        "--",
        "x",
    ]);
    let ran = String::from_utf8_lossy(&out.stderr).contains("pluxy: no terminal nope");
    (out, ran)
}

/// The terminal runs a program only when no account but this user, SYSTEM, Administrators and
/// TrustedInstaller may change it or its folder, or remove or rename what a folder above it holds:
/// otherwise it says which path another account may change and how, and the program never runs.
/// Adding to a folder above is allowed, as every account may in `C:\`.
#[test]
fn a_program_another_account_may_change_never_runs() {
    enum At {
        Program,
        Folder,
        Above,
    }
    use At::*;
    let cases: &[(&str, At, &[&str], Option<&str>)] = &[
        ("own", Program, &[], None),
        (
            "everyone-writes",
            Program,
            &["/grant", "*S-1-1-0:(W)"],
            Some("S-1-1-0"),
        ),
        (
            "users-delete",
            Program,
            &["/grant", "*S-1-5-32-545:(D)"],
            Some("S-1-5-32-545"),
        ),
        (
            "everyone-adds",
            Folder,
            &["/grant", "*S-1-1-0:(WD)"],
            Some("S-1-1-0"),
        ),
        (
            "everyone-removes",
            Above,
            &["/grant", "*S-1-1-0:(DC)"],
            Some("S-1-1-0"),
        ),
        (
            "everyone-renames",
            Above,
            &["/grant", "*S-1-1-0:(D)"],
            Some("S-1-1-0"),
        ),
        (
            "everyone-adds-above",
            Above,
            &["/grant", "*S-1-1-0:(WD,AD)"],
            None,
        ),
    ];
    for (name, at, grant, who) in cases {
        let a = agent(name);
        let changed = match at {
            Program => &a.exe,
            Folder => &a.bin,
            Above => &a.above,
        };
        if !grant.is_empty() {
            assert!(icacls(changed, grant), "{name}: icacls {grant:?}");
        }
        let (out, ran) = launch(&a.exe);
        let stdout = String::from_utf8_lossy(&out.stdout);
        let stderr = String::from_utf8_lossy(&out.stderr);
        match who {
            None => {
                assert!(ran, "{name}: {stdout}{stderr}");
                assert_eq!(out.status.code(), Some(2), "{name}: {stdout}{stderr}");
            }
            Some(sid) => {
                assert!(!ran, "{name}: the program ran: {stdout}{stderr}");
                assert_eq!(out.status.code(), Some(1), "{name}: {stdout}{stderr}");
                let refusal = format!(
                    "pluxy: will not run \"{}\": \"{}\" may be changed by another account: it has an access list entry that lets ",
                    a.exe.display(),
                    changed.display()
                );
                assert!(stdout.contains(&refusal), "{name}: {stdout}");
                assert!(
                    stdout.contains(&format!("({sid}) change it")),
                    "{name}: {stdout}"
                );
            }
        }
        std::fs::remove_dir_all(&a.root).unwrap();
    }
    // A program another account owns, when this account may hand it to the Users group.
    let a = agent("users-own");
    if icacls(&a.exe, &["/setowner", "*S-1-5-32-545"]) {
        let (out, ran) = launch(&a.exe);
        let stdout = String::from_utf8_lossy(&out.stdout);
        assert!(!ran, "{stdout}");
        assert!(
            stdout.contains(&format!(
                "pluxy: will not run \"{0}\": \"{0}\" may be changed by another account: it is owned by ",
                a.exe.display()
            )),
            "{stdout}"
        );
        assert!(stdout.contains("(S-1-5-32-545)"), "{stdout}");
    } else {
        println!("skipped: this account cannot hand a file to the Users group");
    }
    std::fs::remove_dir_all(&a.root).unwrap();
}

/// A program named by a path through a junction is refused, naming that path: what runs is named
/// by its own path, the one the app checked.
#[test]
fn a_program_reached_through_a_junction_never_runs() {
    let a = agent("junction");
    let link = a.root.join("link");
    let made = Command::new(
        PathBuf::from(std::env::var_os("SystemRoot").unwrap()).join(r"System32\cmd.exe"),
    )
    .args(["/D", "/C", "mklink", "/J"])
    .arg(&link)
    .arg(&a.above)
    .output()
    .unwrap();
    assert!(made.status.success(), "{made:?}");
    let through = link.join(r"bin\agent.exe");
    let (out, ran) = launch(&through);
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(!ran, "{stdout}");
    assert_eq!(out.status.code(), Some(1), "{stdout}");
    assert!(
        stdout.contains(&format!(
            "pluxy: will not run \"{0}\": \"{0}\" is not the program's own path",
            through.display()
        )),
        "{stdout}"
    );
    // By its own path it runs.
    assert!(launch(&a.exe).1);
    std::fs::remove_dir_all(&a.root).unwrap();
}
