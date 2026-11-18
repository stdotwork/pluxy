//! The execution program's launch modes on macOS, run as the app runs them.
#![cfg(target_os = "macos")]

use std::fs::File;
use std::os::unix::fs::PermissionsExt;
use std::os::unix::io::AsRawFd;
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{Duration, Instant};

const TERMINAL: &str = "/System/Applications/Utilities/Terminal.app";

fn helper() -> Command {
    Command::new(env!("CARGO_BIN_EXE_execution"))
}

/// The execution program given program, opened as the app opens and checks it, as descriptor 3,
/// as the app gives it.
fn given(program: File) -> Command {
    let mut c = helper();
    // SAFETY: dup2 and fcntl are async-signal-safe; program lives as long as c.
    unsafe {
        c.pre_exec(move || {
            if libc::dup2(program.as_raw_fd(), 3) == -1 || libc::fcntl(3, libc::F_SETFD, 0) != 0 {
                return Err(std::io::Error::last_os_error());
            }
            Ok(())
        });
    }
    c
}

/// The execution program given the file at path as its program.
fn given_path(path: &str) -> Command {
    given(File::open(path).unwrap())
}

/// A new folder for one test, by its real path.
fn folder(name: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!("pluxy-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    std::fs::canonicalize(d).unwrap()
}

/// A shell script at p running body, which only its owner may change or run.
fn script(p: &Path, body: &str) {
    std::fs::write(p, format!("#!/bin/sh\n{body}\n")).unwrap();
    std::fs::set_permissions(p, std::fs::Permissions::from_mode(0o700)).unwrap();
}

/// What file p holds once it exists, within 30 seconds.
fn wait_for(p: &Path) -> Option<String> {
    let end = Instant::now() + Duration::from_secs(30);
    while !p.exists() && Instant::now() < end {
        std::thread::sleep(Duration::from_millis(100));
    }
    std::fs::read_to_string(p).ok()
}

/// Runs program with out in a Terminal window, the app having given file as the program: true
/// once the launch was handed over.
fn in_window(file: File, program: &Path, out: &Path) -> bool {
    let status = given(file)
        .args(["terminal", "--with", "terminal", "--app", TERMINAL, "--"])
        .arg(program)
        .arg(out)
        .status()
        .unwrap();
    status.success()
}

/// Whether this test may open a Terminal window: on GitHub's runners only, elsewhere it would open
/// one on the person's desktop.
fn windows_allowed() -> bool {
    if std::env::var_os("GITHUB_ACTIONS").is_none() {
        println!("skipped: opens a Terminal window; runs on GitHub's runners only");
        return false;
    }
    true
}

#[test]
fn the_terminal_mode_hands_over_to_terminal_only_and_attach_takes_only_a_live_launch() {
    let out = given_path("/bin/echo")
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

/// The window runs only the file the app checked: with none given, nothing is handed over.
#[test]
fn the_terminal_mode_takes_no_launch_without_the_apps_program() {
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
        .output()
        .unwrap();
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert_eq!(out.status.code(), Some(1), "{stderr}");
    assert!(stderr.contains("the app gave no program"), "{stderr}");
}

#[test]
fn a_launch_too_large_to_hand_over_is_refused_before_terminal_opens() {
    // 200,000 control characters: 1.2 MB in the hand-over (over its 1 MiB), within macOS'
    // argument limit.
    let out = given_path("/bin/echo")
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
    if !windows_allowed() {
        return;
    }
    let d = std::env::temp_dir().join(format!("pluxy-terminal-{}", std::process::id()));
    std::fs::create_dir_all(&d).unwrap();
    let d = std::fs::canonicalize(d).unwrap();
    let out = d.join("out");
    let value = r#"a 'b' "c" $HOME `x`"#;
    let script =
        r#"pwd -P > "$1.part"; printf '%s' "$PLUXY_TEST_VALUE" >> "$1.part"; mv "$1.part" "$1""#;
    let status = given_path("/bin/sh")
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

/// The window runs the file the app checked, not what is at its path by then: here the checked
/// program was renamed aside and another put in its place, between the app's check and the
/// window, as another user who may write its folder could. It runs from where it was renamed to.
/// Opens a Terminal window (GitHub's runners only).
#[test]
fn a_program_swapped_at_its_path_after_the_apps_check_never_runs_in_the_window() {
    if !windows_allowed() {
        return;
    }
    let d = folder("swap");
    let program = d.join("agent");
    script(&program, r#"echo checked > "$1.part"; mv "$1.part" "$1""#);
    let checked = File::open(&program).unwrap();
    std::fs::rename(&program, d.join("aside")).unwrap();
    script(&program, r#"echo swapped > "$1.part"; mv "$1.part" "$1""#);
    let out = d.join("out");
    assert!(in_window(checked, &program, &out), "not handed over");
    let ran = wait_for(&out);
    std::fs::remove_dir_all(&d).unwrap();
    assert_eq!(ran.as_deref(), Some("checked\n"));
}

/// A program below a folder the `admin` group may write (as `/Applications`, or Homebrew's
/// folders) runs in the window, as a direct launch of it does. Opens a Terminal window (GitHub's
/// runners only).
#[test]
fn a_program_below_a_folder_the_admin_group_may_write_runs_in_the_window() {
    if !windows_allowed() {
        return;
    }
    let d = folder("admin");
    let bin = d.join("bin");
    std::fs::create_dir(&bin).unwrap();
    let chgrp = Command::new("/usr/bin/chgrp")
        .arg("admin")
        .arg(&bin)
        .output()
        .unwrap();
    assert!(
        chgrp.status.success(),
        "chgrp admin (the test's user must be in admin): {}",
        String::from_utf8_lossy(&chgrp.stderr)
    );
    std::fs::set_permissions(&bin, std::fs::Permissions::from_mode(0o775)).unwrap();
    let program = bin.join("agent");
    script(&program, r#"echo ran > "$1.part"; mv "$1.part" "$1""#);
    let out = d.join("out");
    assert!(
        in_window(File::open(&program).unwrap(), &program, &out),
        "not handed over"
    );
    let ran = wait_for(&out);
    std::fs::remove_dir_all(&d).unwrap();
    assert_eq!(ran.as_deref(), Some("ran\n"));
}

/// Offers a hand-over on a new socket in d, as the terminal mode does: spec's bytes, with
/// program's descriptor attached (`SCM_RIGHTS`).
fn offer(d: &Path, spec: &serde_json::Value, program: File) -> std::thread::JoinHandle<()> {
    use std::io::Write;
    let listener = std::os::unix::net::UnixListener::bind(d.join("s")).unwrap();
    let bytes = serde_json::to_vec(spec).unwrap();
    std::thread::spawn(move || {
        let (mut s, _) = listener.accept().unwrap();
        let mut control = [0u64; 8];
        let mut iov = libc::iovec {
            iov_base: bytes.as_ptr() as *mut libc::c_void,
            iov_len: bytes.len(),
        };
        // SAFETY: an all-zero msghdr is a valid empty one; control has room for one descriptor.
        let sent = unsafe {
            let mut msg: libc::msghdr = std::mem::zeroed();
            msg.msg_iov = &mut iov;
            msg.msg_iovlen = 1;
            msg.msg_control = control.as_mut_ptr().cast();
            msg.msg_controllen = libc::CMSG_SPACE(4) as _;
            let c = libc::CMSG_FIRSTHDR(&msg);
            (*c).cmsg_level = libc::SOL_SOCKET;
            (*c).cmsg_type = libc::SCM_RIGHTS;
            (*c).cmsg_len = libc::CMSG_LEN(4) as _;
            std::ptr::write_unaligned(libc::CMSG_DATA(c).cast::<i32>(), program.as_raw_fd());
            libc::sendmsg(s.as_raw_fd(), &msg, 0)
        };
        assert!(sent > 0, "{}", std::io::Error::last_os_error());
        s.write_all(&bytes[sent as usize..]).unwrap();
        let _ = s.shutdown(std::net::Shutdown::Write);
    })
}

/// What the window runs (`attach`) for program, handed over as the terminal mode would: its exit
/// code, its error output, and what program wrote to out.
fn attach(d: &Path, program: &Path) -> (Option<i32>, String, Option<String>) {
    let out = d.join("out");
    let spec = serde_json::json!({"argv": [program, out], "cwd": d,
        "env": [["PATH", "/usr/bin:/bin"]]});
    let offered = offer(d, &spec, File::open(program).unwrap());
    let r = helper().arg("attach").arg(d.join("s")).output().unwrap();
    offered.join().unwrap();
    (
        r.status.code(),
        String::from_utf8_lossy(&r.stderr).into_owned(),
        std::fs::read_to_string(&out).ok(),
    )
}

/// The window refuses a program below a folder another user could change, saying which, and runs
/// nothing; the same program from a folder only its user can change runs. No window opens: the
/// test runs `attach` itself, as Terminal's window would.
#[test]
fn the_window_refuses_a_program_another_user_could_change() {
    let d = folder("attach");
    let own = d.join("own");
    std::fs::create_dir(&own).unwrap();
    std::fs::set_permissions(&own, std::fs::Permissions::from_mode(0o755)).unwrap();
    script(&own.join("agent"), r#"echo ran > "$1""#);
    let ran = attach(&own, &own.join("agent"));
    let open = d.join("open");
    std::fs::create_dir(&open).unwrap();
    std::fs::set_permissions(&open, std::fs::Permissions::from_mode(0o777)).unwrap();
    script(&open.join("agent"), r#"echo ran > "$1""#);
    let refused = attach(&open, &open.join("agent"));
    std::fs::remove_dir_all(&d).unwrap();
    assert_eq!(ran, (Some(0), String::new(), Some("ran\n".into())));
    assert_eq!(refused.0, Some(1), "{}", refused.1);
    assert!(
        refused
            .1
            .contains(&format!("{} may be changed by other users", open.display())),
        "{}",
        refused.1
    );
    assert_eq!(refused.2, None, "the program ran");
}
