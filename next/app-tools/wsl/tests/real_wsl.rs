//! pluxy/wsl and the Linux side of a WSL instance against Windows' own `wsl.exe` and real
//! distributions, on `windows-latest` (the WSL job of `.github/workflows/next-app-tools-platforms.yml`,
//! which sets the distributions up and runs these with `--ignored`). Nothing here stands in for
//! WSL. The job's distributions: `PLUXY_WSL_TEST_DISTRO` (default user `alice`, users `bob` and
//! root), `PLUXY_WSL_TEST_OTHER` (a copy of it), both of WSL version `PLUXY_WSL_TEST_VERSION`.
//! `/usr/local/bin/pluxy-standin` records how it was started; `pluxy-nonlogin` is on bob's PATH
//! only through `.bashrc`. `PLUXY_WSL_TEST_WRITER` and `PLUXY_WSL_TEST_WRITER_DIR` are the Linux
//! paths of the fixture writer (`pluxy-transactions`' example, built for Linux) and of its package
//! folder; `PLUXY_WSL_TEST_DRIVE_DIR` a Linux path to a folder on a Windows drive holding
//! `settings.json`.
#![cfg(windows)]

use pluxy_program::{read_frame, write_frame, MAX_INBOUND};
use serde_json::{json, Value};
use std::io::Read;
use std::process::{Child, ChildStdin, ChildStdout, Command, Stdio};
use std::time::{Duration, Instant};

fn var(name: &str) -> String {
    std::env::var(name).unwrap_or_else(|_| panic!("{name} is not set: run from the WSL job"))
}

/// A Linux path the job gives (`/` first): never a Windows path a shell made of it.
fn linux_path(name: &str) -> String {
    let v = var(name);
    assert!(
        v.starts_with('/'),
        "{name} is {v:?}, not a Linux path: was it converted by Git Bash (MSYS2_ENV_CONV_EXCL)?"
    );
    v
}

/// wsl.exe's own text, which is UTF-16LE unless `WSL_UTF8` is set (then UTF-8).
fn text(bytes: &[u8]) -> String {
    if bytes.starts_with(&[0xFF, 0xFE]) || bytes.contains(&0) {
        let words: Vec<u16> = bytes
            .as_chunks::<2>()
            .0
            .iter()
            .map(|p| u16::from_le_bytes(*p))
            .collect();
        String::from_utf16_lossy(&words)
    } else {
        String::from_utf8_lossy(bytes).into_owned()
    }
}

/// A reader that keeps what it has read, so bytes taken for a frame that is not one can be shown.
struct Kept<'a, R: Read> {
    inner: &'a mut R,
    seen: Vec<u8>,
}

impl<R: Read> Read for Kept<'_, R> {
    fn read(&mut self, buf: &mut [u8]) -> std::io::Result<usize> {
        let n = self.inner.read(buf)?;
        if self.seen.len() < 4096 {
            self.seen.extend_from_slice(&buf[..n]);
        }
        Ok(n)
    }
}

fn distro() -> String {
    var("PLUXY_WSL_TEST_DISTRO")
}

fn other() -> String {
    var("PLUXY_WSL_TEST_OTHER")
}

/// A program spoken to over the contract's pipes.
struct Program {
    child: Child,
    stdin: Option<ChildStdin>,
    stdout: ChildStdout,
    next: u64,
}

impl Program {
    fn call(&mut self, op: &str, input: Value) -> Value {
        self.next += 1;
        let id = format!("c-{}", self.next);
        let frame = json!({"type": "call", "id": id, "op": op, "input": input,
            "chain": [{"kind": "main"}]});
        write_frame(self.stdin.as_mut().unwrap(), &frame, MAX_INBOUND).unwrap();
        loop {
            let f = read_frame(&mut self.stdout)
                .unwrap()
                .expect("the program ended");
            if f["id"] == id.as_str() && (f["type"] == "result" || f["type"] == "error") {
                return f;
            }
        }
    }

    fn ok(&mut self, op: &str, input: Value) -> Value {
        let r = self.call(op, input.clone());
        assert_eq!(r["type"], "result", "{op} {input}: {r}");
        r["output"].clone()
    }

    fn err(&mut self, op: &str, input: Value) -> Value {
        let r = self.call(op, input.clone());
        assert_eq!(r["type"], "error", "{op} {input}: {r}");
        r["error"].clone()
    }

    /// Closes its input, as the host does at idle: it must end.
    fn end(mut self) {
        drop(self.stdin.take());
        let until = Instant::now() + Duration::from_secs(20);
        while self.child.try_wait().unwrap().is_none() {
            assert!(
                Instant::now() < until,
                "the program did not end at the end of its input"
            );
            std::thread::sleep(Duration::from_millis(50));
        }
    }
}

/// pluxy/wsl started as the app starts a tool: in its package folder, with the contract's
/// variables and the person's home.
fn pluxy_wsl() -> Program {
    let mut c = Command::new(env!("CARGO_BIN_EXE_pluxy-wsl"));
    c.current_dir(env!("CARGO_MANIFEST_DIR"))
        .env_clear()
        .env("SystemRoot", std::env::var("SystemRoot").unwrap())
        .env("HOME", std::env::var("USERPROFILE").unwrap())
        .env("PLUXY_APP_CONTRACT_VERSIONS", "0")
        .env("PLUXY_EXTENSION_ID", "pluxy/wsl")
        .env("PLUXY_APP_ARTIFACT_SHA256", "ab".repeat(32))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::inherit());
    let mut child = c.spawn().unwrap();
    let (stdin, mut stdout) = (child.stdin.take(), child.stdout.take().unwrap());
    assert_eq!(read_frame(&mut stdout).unwrap().unwrap()["type"], "hello");
    Program {
        child,
        stdin,
        stdout,
        next: 0,
    }
}

/// Runs wsl.exe with args (and extra environment) to its end: exit code and output. A launch
/// WSL 1 refuses with 0xd0000225 (nothing ran) is tried again, as the program does.
fn wsl(args: &[&str], env: &[(&str, &str)]) -> (i32, String, String) {
    for attempt in 1..=5 {
        let mut c = Command::new(r"C:\Windows\System32\wsl.exe");
        c.args(args).env("WSL_UTF8", "1").stdin(Stdio::null());
        for (k, v) in env {
            c.env(k, v);
        }
        let out = c.output().unwrap();
        let (o, e) = (
            String::from_utf8_lossy(&out.stdout).into_owned(),
            String::from_utf8_lossy(&out.stderr).into_owned(),
        );
        let code = out.status.code().unwrap_or(-1);
        if code == -1 && (o.contains("0xd0000225") || e.contains("0xd0000225")) && attempt < 5 {
            eprintln!("retry: WSL refused a launch with 0xd0000225 ({attempt})");
            std::thread::sleep(Duration::from_secs(2));
            continue;
        }
        return (code, o, e);
    }
    unreachable!()
}

/// Runs a shell command in the test distribution as root (for checks; never on a PID).
fn as_root(script: &str) -> String {
    let d = distro();
    let (code, out, err) = wsl(
        &["-d", &d, "-u", "root", "--exec", "/bin/sh", "-c", script],
        &[],
    );
    assert_eq!(code, 0, "{script}: {out} {err}");
    out
}

/// Run by the WSL job before it installs any distribution (and skipped by name after).
#[test]
#[ignore = "needs WSL with no distribution installed (the WSL job, before its setup)"]
fn distros_before_any_distribution_is_installed_says_not_installed() {
    let mut p = pluxy_wsl();
    assert_eq!(
        p.ok("distros", json!({})),
        json!({"distros": [], "installed": false})
    );
    p.end();
}

#[test]
#[ignore = "needs real WSL distributions (the WSL job)"]
fn distros_lists_both_distributions_with_the_default_and_their_version() {
    let mut p = pluxy_wsl();
    let v = p.ok("distros", json!({}));
    assert_eq!(v["installed"], true);
    let list = v["distros"].as_array().unwrap();
    let find = |n: &str| list.iter().find(|d| d["name"] == n).cloned();
    let version: u64 = var("PLUXY_WSL_TEST_VERSION").parse().unwrap();
    let main = find(&distro()).unwrap_or_else(|| panic!("{v}"));
    assert_eq!(main["default"], true, "{v}");
    assert_eq!(main["version"], version, "{v}");
    let copy = find(&other()).unwrap_or_else(|| panic!("{v}"));
    assert_eq!(copy["default"], false, "{v}");
    assert_eq!(list.iter().filter(|d| d["default"] == true).count(), 1);
    p.end();
}

#[test]
#[ignore = "needs real WSL distributions (the WSL job)"]
fn users_are_people_and_root_with_the_default_user_marked() {
    let mut p = pluxy_wsl();
    let v = p.ok("users", json!({"distro": distro()}));
    let names: Vec<&str> = v["users"]
        .as_array()
        .unwrap()
        .iter()
        .map(|u| u["name"].as_str().unwrap())
        .collect();
    assert_eq!(names.last(), Some(&"root"), "{v}");
    for u in ["alice", "bob"] {
        assert!(names.contains(&u), "{v}");
    }
    for system in ["daemon", "nobody", "bin", "sys"] {
        assert!(!names.contains(&system), "{v}");
    }
    assert_eq!(v["default"], "alice");
    assert_eq!(v["arch"], "amd64");
    let version: u64 = var("PLUXY_WSL_TEST_VERSION").parse().unwrap();
    assert_eq!(v["wsl"], version);
    let alice = v["users"]
        .as_array()
        .unwrap()
        .iter()
        .find(|u| u["name"] == "alice")
        .unwrap();
    assert_eq!(alice["default"], true);
    // A distribution that does not exist.
    let e = p.err("users", json!({"distro": "PluxyNoSuchDistro"}));
    assert_eq!(e["code"], "not_found", "{e}");
    p.end();
}

#[test]
#[ignore = "needs real WSL distributions (the WSL job)"]
fn check_reads_the_users_home_processor_and_drives_and_refuses_a_wrong_pair() {
    let mut p = pluxy_wsl();
    let v = p.ok("check", json!({"distro": distro(), "user": "bob"}));
    assert_eq!(v["home"], "/home/bob");
    assert_eq!(v["user"], "bob");
    assert_eq!(v["distro"], distro().as_str());
    assert_eq!(v["arch"], "amd64");
    assert_eq!(v["automount"], "/mnt/", "{v}");
    let c = v["drives"]
        .as_array()
        .unwrap()
        .iter()
        .find(|d| d["letter"] == "c")
        .cloned();
    assert_eq!(
        c,
        Some(json!({"letter": "c", "path": "/mnt/c", "exec": true})),
        "{v}"
    );
    // The same user in the other distribution is that distribution's.
    let v = p.ok("check", json!({"distro": other(), "user": "bob"}));
    assert_eq!(v["distro"], other().as_str());
    // A missing user, a missing distribution.
    let e = p.err(
        "check",
        json!({"distro": distro(), "user": "pluxynosuchuser"}),
    );
    assert_eq!(e["code"], "not_found", "{e}");
    assert!(
        e["message"].as_str().unwrap().contains("pluxynosuchuser"),
        "{e}"
    );
    let e = p.err(
        "check",
        json!({"distro": "PluxyNoSuchDistro", "user": "bob"}),
    );
    assert_eq!(e["code"], "not_found", "{e}");
    assert!(
        e["message"]
            .as_str()
            .unwrap()
            .contains("no WSL distribution named"),
        "{e}"
    );
    p.end();
}

#[test]
#[ignore = "needs real WSL distributions (the WSL job)"]
fn prepare_answers_the_exact_launch_and_that_launch_runs_as_the_user() {
    let mut p = pluxy_wsl();
    let d = distro();
    as_root("rm -f /tmp/pluxy-standin.out; mkdir -p '/home/bob/work dir'; chown bob '/home/bob/work dir'");
    let v = p.ok(
        "prepare",
        json!({"distro": d, "user": "bob", "kind": "direct", "program": "pluxy-standin",
            "cwd": "/home/bob/work dir", "args": ["a b", "\"q\"", "", "ü"]}),
    );
    let exe = v["launch"]["executable"].as_str().unwrap().to_string();
    assert!(exe.to_lowercase().ends_with(r"\system32\wsl.exe"), "{v}");
    assert_eq!(v["launch"]["program"], "/usr/local/bin/pluxy-standin");
    assert_eq!(v["kind"], "direct");
    assert_eq!(
        v["launch"]["argv"],
        json!([
            "-d",
            d,
            "-u",
            "bob",
            "--cd",
            "/home/bob/work dir",
            "--exec",
            "/usr/local/bin/pluxy-standin",
            "a b",
            "\"q\"",
            "",
            "ü"
        ])
    );
    assert_eq!(v["launch"]["cwd"], std::env::var("USERPROFILE").unwrap());
    // The prepared launch, run as it stands: the stand-in records who, where and what.
    let argv: Vec<String> = v["launch"]["argv"]
        .as_array()
        .unwrap()
        .iter()
        .map(|a| a.as_str().unwrap().to_string())
        .collect();
    let refs: Vec<&str> = argv.iter().map(String::as_str).collect();
    let (code, out, err) = wsl(&refs, &[]);
    assert_eq!(code, 0, "{out} {err}");
    assert_eq!(
        as_root("cat /tmp/pluxy-standin.out"),
        "bob\n/home/bob/work dir\na b\n\"q\"\n\nü\n"
    );
    // The home is the default folder.
    let v = p.ok(
        "prepare",
        json!({"distro": d, "user": "bob", "kind": "terminal",
        "program": "pluxy-standin"}),
    );
    assert_eq!(v["launch"]["argv"][5], "/home/bob");
    assert_eq!(v["kind"], "terminal");
    // An absolute override; a program only a login shell's rc file puts on the PATH is not
    // found by name (no login shell), but is by its full path.
    let e = p.err(
        "prepare",
        json!({"distro": d, "user": "bob", "kind": "terminal",
        "program": "pluxy-nonlogin"}),
    );
    assert_eq!(e["code"], "not_found", "{e}");
    let v = p.ok(
        "prepare",
        json!({"distro": d, "user": "bob", "kind": "terminal",
        "override": "/home/bob/.local/bin/pluxy-nonlogin"}),
    );
    assert_eq!(
        v["launch"]["program"],
        "/home/bob/.local/bin/pluxy-nonlogin"
    );
    // Missing program, folder or override, a missing user: refused, nothing started.
    for (input, word) in [
        (json!({"program": "pluxy-no-such-program"}), "PATH"),
        (
            json!({"program": "pluxy-standin", "cwd": "/home/bob/nope"}),
            "working folder",
        ),
        (
            json!({"override": "/home/bob/nope"}),
            "not an executable file",
        ),
    ] {
        let mut i = input.clone();
        i["distro"] = json!(d);
        i["user"] = json!("bob");
        i["kind"] = json!("direct");
        let e = p.err("prepare", i);
        assert_eq!(e["code"], "not_found", "{input}: {e}");
        assert!(
            e["message"].as_str().unwrap().contains(word),
            "{input}: {e}"
        );
    }
    let e = p.err(
        "prepare",
        json!({"distro": d, "user": "pluxynosuchuser", "kind": "direct",
        "program": "pluxy-standin"}),
    );
    assert_eq!(e["code"], "not_found", "{e}");
    p.end();
}

/// The fixture writer's Linux program started in distro as user, as the host starts a WSL
/// instance: `wsl.exe -d -u --cd <package> --exec <program>`, the contract's variables crossing by
/// `WSLENV`, with `PLUXY_ENVIRONMENT` naming environment and `PLUXY_ENVIRONMENT_ACCOUNT` account
/// (`<user ID>:<home>`). Its first frame (None: it ended without one) and, when it ended, what it
/// wrote (wsl.exe's own errors go to standard output) and the command line.
fn writer(distro: &str, user: &str, environment: &str, account: &str) -> (Option<Program>, String) {
    let (prog, dir) = (
        linux_path("PLUXY_WSL_TEST_WRITER"),
        linux_path("PLUXY_WSL_TEST_WRITER_DIR"),
    );
    let args = ["-d", distro, "-u", user, "--cd", &dir, "--exec", &prog];
    for attempt in 1..=5 {
        let mut c = Command::new(r"C:\Windows\System32\wsl.exe");
        c.args(args)
            .env_clear()
            .env("SystemRoot", std::env::var("SystemRoot").unwrap())
            .env("PLUXY_APP_CONTRACT_VERSIONS", "0")
            .env("PLUXY_EXTENSION_ID", "pluxy/fixture-writer")
            .env("PLUXY_APP_ARTIFACT_SHA256", "cd".repeat(32))
            .env(
                "PLUXY_TOOL_DATA_DIR",
                "/home/bob/.local/state/pluxy-wsl-test/w",
            )
            .env("PLUXY_ENVIRONMENT", environment)
            .env("PLUXY_ENVIRONMENT_ACCOUNT", account)
            .env(
                "WSLENV",
                "PLUXY_APP_CONTRACT_VERSIONS/u:PLUXY_EXTENSION_ID/u:PLUXY_APP_ARTIFACT_SHA256/u:\
                 PLUXY_TOOL_DATA_DIR/u:PLUXY_ENVIRONMENT/u:PLUXY_ENVIRONMENT_ACCOUNT/u",
            )
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        let mut child = c.spawn().unwrap();
        let (stdin, mut stdout) = (child.stdin.take(), child.stdout.take().unwrap());
        let mut stderr = child.stderr.take().unwrap();
        let err = std::thread::spawn(move || {
            let mut s = Vec::new();
            let _ = stderr.read_to_end(&mut s);
            s
        });
        let mut kept = Kept {
            inner: &mut stdout,
            seen: Vec::new(),
        };
        let first = read_frame(&mut kept);
        let seen = kept.seen;
        match first {
            Ok(Some(f)) => {
                assert_eq!(f["type"], "hello", "{f}");
                assert_eq!(f["id"], "pluxy/fixture-writer");
                return (
                    Some(Program {
                        child,
                        stdin,
                        stdout,
                        next: 0,
                    }),
                    String::new(),
                );
            }
            _ => {
                drop(stdin);
                let mut out = seen;
                let _ = stdout.read_to_end(&mut out);
                child.wait().unwrap();
                let said = format!(
                    "{} {} (wsl.exe {args:?})",
                    text(&err.join().unwrap()).trim(),
                    text(&out).trim()
                );
                if said.contains("0xd0000225") && attempt < 5 {
                    eprintln!("retry: WSL refused a launch with 0xd0000225 ({attempt})");
                    std::thread::sleep(Duration::from_secs(2));
                    continue;
                }
                return (None, said);
            }
        }
    }
    unreachable!()
}

/// user's account in the test distribution as a host's record has it: `<user ID>:<home>`.
fn account(user: &str) -> String {
    as_root(&format!(
        "printf '%s:%s' \"$(id -u {user})\" \"$(getent passwd {user} | cut -d: -f6)\""
    ))
}

#[test]
#[ignore = "needs real WSL distributions (the WSL job)"]
fn a_linux_program_says_hello_only_in_its_distribution_as_its_user() {
    let d = distro();
    as_root("rm -rf /home/bob/.local/state/pluxy-wsl-test");
    let bob = account("bob");
    // The right pair: hello, and a preview of bob's file.
    let (p, err) = writer(&d, "bob", &format!("wsl:{d}:bob"), &bob);
    let mut p = p.unwrap_or_else(|| panic!("no hello: {err}"));
    let v = p.ok(
        "preview",
        json!({"set": "s", "targets": [{"file": "/home/bob/.claude/settings.json"}]}),
    );
    assert_eq!(v["targets"][0]["target"], "/home/bob/.claude/settings.json");
    // A file on a Windows drive is refused in WSL.
    let drive = format!("{}/settings.json", linux_path("PLUXY_WSL_TEST_DRIVE_DIR"));
    let e = p.err("preview", json!({"set": "s", "targets": [{"file": drive}]}));
    assert_eq!(
        e["message"], "This file is on a Windows drive; set it up from Windows local.",
        "{e}"
    );
    p.end();
    // A read leaves no data folder behind.
    assert_eq!(
        as_root("test -e /home/bob/.local/state/pluxy-wsl-test && echo made || echo none"),
        "none\n"
    );
    // Another user or distribution than the one it runs as: it stops before hello.
    for (environment, now) in [
        (format!("wsl:{d}:alice"), format!("{d}/bob")),
        (format!("wsl:{}:bob", other()), format!("{d}/bob")),
    ] {
        let (p, err) = writer(&d, "bob", &environment, &bob);
        assert!(p.is_none(), "{environment} said hello");
        let expected = environment.trim_start_matches("wsl:").replacen(':', "/", 1);
        assert!(
            err.contains(&format!("pluxy expected WSL {expected}, this is {now}")),
            "{environment}: {err}"
        );
    }
    // bob's names, but another account than bob's (a record of a bob made again, or of a home
    // since moved): it stops before hello too.
    let (uid, home) = bob.split_once(':').unwrap();
    let other_uid = uid.parse::<u32>().unwrap() + 4242;
    for (account, expected) in [
        (
            format!("{other_uid}:{home}"),
            format!("user ID {other_uid} and home {home}"),
        ),
        (
            format!("{uid}:/nonexistent-home"),
            format!("user ID {uid} and home /nonexistent-home"),
        ),
    ] {
        let (p, err) = writer(&d, "bob", &format!("wsl:{d}:bob"), &account);
        assert!(p.is_none(), "{account} said hello");
        assert!(
            err.contains(&format!(
                "pluxy expected bob with {expected}, this is user ID {uid} with home {home}"
            )),
            "{account}: {err}"
        );
    }
}
