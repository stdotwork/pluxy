//! The owner module's own tests: that nothing outside `owned/` signals or ends a process (every
//! platform), and the primitives each platform's spawns and stops rest on, on real files, folders
//! and processes.

use std::collections::BTreeMap;
use std::path::Path;

/// Words by which code signals or ends a process: system calls, programs and their arguments.
/// Matched as whole words, outside lines that are only a comment.
const SIGNALS: &[&str] = &[
    "killpg",
    "libc::kill",
    "SYS_kill",
    "SYS_tkill",
    "SYS_tgkill",
    "pidfd_send_signal",
    "sigqueue",
    "signal::kill",
    "\"kill\"",
    "kill -",
    "pkill",
    "killall",
    "taskkill",
    "Stop-Process",
    "TerminateProcess",
    "NtTerminateProcess",
    "TerminateJobObject",
    "TerminateThread",
    "OpenProcess",
    "GenerateConsoleCtrlEvent",
    "DebugActiveProcess",
    "CreateRemoteThread",
    "EndTask",
    "WM_CLOSE",
    "WM_QUIT",
];

/// Where in a file a site is: in its inline `#[cfg(test)] mod tests { ... }`, or anywhere else.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
enum Part {
    Code,
    Tests,
}

/// The only places outside `src/owned/` that may use a word of `SIGNALS`: (file under `src/`,
/// part, word, exactly how many times, why). A new site, or one gone, fails the test until this
/// list says so.
const ALLOWED: &[(&str, Part, &str, usize, &str)] = &[
    (
        "dialog_windows.rs",
        Part::Code,
        "WM_CLOSE",
        1,
        "the native dialog's own window procedure answers a WM_CLOSE sent to it (its close box, Alt+F4) by cancelling; it sends none",
    ),
    (
        "instance/win.rs",
        Part::Code,
        "OpenProcess",
        2,
        "single instance (Windows): opens the pipe's server process with QUERY_LIMITED_INFORMATION only, to compare its user with this one; that right cannot end it",
    ),
    (
        "launcher_native.rs",
        Part::Code,
        "libc::kill",
        1,
        "native test (Linux, feature native-test): kills the execution program by the process ID its status gives, to make it crash",
    ),
    (
        "managed.rs",
        Part::Tests,
        "libc::kill",
        1,
        "test: kills the core the test started and holds, to see its exit before it is reaped",
    ),
    (
        "native_core.rs",
        Part::Code,
        "OpenProcess",
        4,
        "native test (Windows): opens a process with QUERY_LIMITED_INFORMATION only, to ask whether it runs and which program it runs; that right cannot end it",
    ),
    (
        "native_core.rs",
        Part::Code,
        "libc::kill",
        1,
        "native test (macOS): signal 0, which only asks whether a process exists",
    ),
    (
        "proc_native.rs",
        Part::Code,
        "OpenProcess",
        2,
        "native test (Windows, feature native-test): opens a process with QUERY_LIMITED_INFORMATION only, to read its program file and whether it has exited; that right cannot end it",
    ),
    (
        "program_tests.rs",
        Part::Code,
        "OpenProcess",
        2,
        "test (Windows): opens a process only to wait for it (SYNCHRONIZE) or to ask whether it is in a job (QUERY_LIMITED_INFORMATION); neither right can end it",
    ),
    (
        "spawn_keys_native.rs",
        Part::Code,
        "libc::kill",
        1,
        "native test (Linux): ends an agent the test started, only while /proc shows it runs as that test's",
    ),
    (
        "spawn_native.rs",
        Part::Code,
        "libc::kill",
        1,
        "native test (Linux): signal 0, which only asks whether a process exists",
    ),
    (
        "spawn_tests.rs",
        Part::Code,
        "libc::kill",
        2,
        "test: signal 0 to see whether a process runs, and the cleanup of processes /proc shows run as this test's",
    ),
];

/// How often word appears in line as a whole word.
fn count(line: &str, word: &str) -> usize {
    let ident = |c: char| c.is_alphanumeric() || c == '_';
    let starts = word.starts_with(ident);
    let ends = word.ends_with(ident);
    line.match_indices(word)
        .filter(|(i, _)| {
            let before = line[..*i].chars().next_back();
            let after = line[i + word.len()..].chars().next();
            !(starts && before.is_some_and(ident)) && !(ends && after.is_some_and(ident))
        })
        .count()
}

/// Each use of a word of `SIGNALS` in text, a Rust source: (part, word, line number).
fn sites_in(text: &str) -> Vec<(Part, &'static str, usize)> {
    let mut out = Vec::new();
    let mut tests = false;
    let mut previous = "";
    for (n, line) in text.lines().enumerate() {
        if !tests
            && line == "mod tests {"
            && previous.starts_with("#[cfg(")
            && previous.contains("test")
        {
            tests = true;
        } else if tests && line == "}" {
            tests = false;
            continue;
        }
        if !line.trim().is_empty() {
            previous = line.trim();
        }
        if line.trim_start().starts_with("//") {
            continue;
        }
        let part = if tests { Part::Tests } else { Part::Code };
        for word in SIGNALS {
            for _ in 0..count(line, word) {
                out.push((part, *word, n + 1));
            }
        }
    }
    out
}

type Sites = BTreeMap<(String, Part, &'static str), Vec<usize>>;

/// Every Rust source under dir but the owner module's own (`owned/` and this file), named by
/// its path under src with `/`; and each site in them.
fn scan(src: &Path, dir: &Path, files: &mut Vec<String>, sites: &mut Sites) {
    let mut entries: Vec<_> = std::fs::read_dir(dir)
        .unwrap_or_else(|e| panic!("{}: {e}", dir.display()))
        .map(|e| e.unwrap().path())
        .collect();
    entries.sort();
    for path in entries {
        let rel: Vec<String> = path
            .strip_prefix(src)
            .unwrap()
            .components()
            .map(|c| c.as_os_str().to_string_lossy().into_owned())
            .collect();
        let rel = rel.join("/");
        if path.is_dir() {
            if rel != "owned" {
                scan(src, &path, files, sites);
            }
            continue;
        }
        if !rel.ends_with(".rs") || rel == "owned_tests.rs" {
            continue;
        }
        let text =
            std::fs::read_to_string(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()));
        for (part, word, line) in sites_in(&text) {
            sites
                .entry((rel.clone(), part, word))
                .or_default()
                .push(line);
        }
        files.push(rel);
    }
}

/// The owner module is the only code that signals or ends a process (`owned/mod.rs`): anything
/// else that does is in `ALLOWED`, with its reason.
#[test]
fn only_the_owner_module_signals_or_ends_a_process() {
    let src = Path::new(env!("CARGO_MANIFEST_DIR")).join("src");
    let (mut files, mut sites) = (Vec::new(), Sites::new());
    scan(&src, &src, &mut files, &mut sites);
    assert!(
        files.len() > 20
            && ["spawn.rs", "program.rs", "managed.rs"]
                .iter()
                .all(|f| files.iter().any(|g| g == f)),
        "the scan read {} files under {}: {files:?}",
        files.len(),
        src.display()
    );
    let mut problems = Vec::new();
    for ((file, part, word), lines) in &sites {
        let allowed = ALLOWED
            .iter()
            .find(|a| a.0 == file && a.1 == *part && a.2 == *word)
            .map_or(0, |a| a.3);
        if lines.len() != allowed {
            problems.push(format!(
                "src/{file} ({part:?}): `{word}` {} times (lines {lines:?}), allowed {allowed}",
                lines.len()
            ));
        }
    }
    for (file, part, word, n, _) in ALLOWED {
        if !sites.contains_key(&(file.to_string(), *part, *word)) {
            problems.push(format!(
                "src/{file} ({part:?}): `{word}` allowed {n} times, found none: take it out of ALLOWED"
            ));
        }
    }
    assert!(
        problems.is_empty(),
        "only src/owned/ signals or ends a process; elsewhere only the sites in ALLOWED may:\n{}",
        problems.join("\n")
    );
}

/// The scan sees each form as a whole word, in code and in a file's inline test module, and
/// passes over comments.
#[test]
fn the_scan_finds_each_use_and_where_it_is() {
    assert_eq!(count("libc::kill(p, 0); libc::kill(q, 9)", "libc::kill"), 2);
    assert_eq!(count("libc::killpg(p, 9)", "libc::kill"), 0);
    assert_eq!(count("unsafe { libc::killpg(p, 9) }", "killpg"), 1);
    assert_eq!(count("pkill -f x", "kill -"), 0);
    assert_eq!(count("sh -c 'kill -0 $p'", "kill -"), 1);
    assert_eq!(count("NtTerminateProcess(h, 1)", "TerminateProcess"), 0);
    assert_eq!(count("TerminateProcess(h, 1)", "TerminateProcess"), 1);
    assert_eq!(count("Command::new(\"kill\")", "\"kill\""), 1);
    assert_eq!(count("child.kill()", "libc::kill"), 0);
    let text = "\
fn a() { unsafe { libc::killpg(1, 9) } }
/// libc::kill in a comment
    // TerminateProcess in a comment
#[cfg(test)]
mod tests {
    fn b() { unsafe { libc::kill(2, 0) } }
}
fn c() { TerminateJobObject(j, 1); }
";
    assert_eq!(
        sites_in(text),
        vec![
            (Part::Code, "killpg", 1),
            (Part::Tests, "libc::kill", 6),
            (Part::Code, "TerminateJobObject", 8),
        ]
    );
}

/// A fresh folder under the system's temporary folder (private to this user on unix), removed
/// when dropped.
struct Temp(std::path::PathBuf);

impl Temp {
    fn new(name: &str) -> Temp {
        static N: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
        let p = std::env::temp_dir().join(format!(
            "pluxy-owned-{name}-{}-{}",
            std::process::id(),
            N.fetch_add(1, std::sync::atomic::Ordering::SeqCst)
        ));
        let _ = std::fs::remove_dir_all(&p);
        #[cfg(unix)]
        std::os::unix::fs::DirBuilderExt::mode(&mut std::fs::DirBuilder::new(), 0o700)
            .create(&p)
            .unwrap();
        #[cfg(not(unix))]
        std::fs::create_dir(&p).unwrap();
        Temp(p)
    }
}

impl Drop for Temp {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

#[cfg(unix)]
mod unix {
    use crate::owned::{kill_group, leader, term_group, Leader};
    use std::io::{BufRead, BufReader, Read};
    use std::os::unix::process::{CommandExt, ExitStatusExt};
    use std::process::{Child, ChildStdin, Command, Stdio};
    use std::sync::mpsc;
    use std::time::{Duration, Instant};

    /// A leader and one more process in its group, each running until its input ends.
    const RUNNING: &str = "exec 3<&0; { echo member; exec cat <&3; } & echo leader; exec cat";
    /// A leader that exits at once, and one more process in its group, running until its input
    /// ends.
    const LEADER_EXITS: &str = "exec 3<&0; { echo member; exec cat <&3; } & echo leader";

    /// A child leading a process group of its own, and its input; ended reports when no process
    /// holds its output any more (each of its group has ended).
    struct Group {
        child: Child,
        stdin: Option<ChildStdin>,
        ended: mpsc::Receiver<()>,
    }

    impl Group {
        /// Starts script with `sh` in a process group of its own, and waits until both its
        /// processes said they run.
        fn start(script: &str) -> Group {
            let mut child = Command::new("/bin/sh")
                .arg("-c")
                .arg(script)
                .process_group(0)
                .stdin(Stdio::piped())
                .stdout(Stdio::piped())
                .stderr(Stdio::null())
                .spawn()
                .unwrap();
            let stdin = child.stdin.take();
            let mut out = BufReader::new(child.stdout.take().unwrap());
            for _ in 0..2 {
                let mut line = String::new();
                out.read_line(&mut line).unwrap();
                assert!(!line.is_empty(), "the group ended before it ran");
            }
            let (tx, ended) = mpsc::channel();
            std::thread::spawn(move || {
                let _ = out.read_to_end(&mut Vec::new());
                let _ = tx.send(());
            });
            Group {
                child,
                stdin,
                ended,
            }
        }

        /// Whether every process of the group has ended within.
        fn ended(&self, within: Duration) -> bool {
            self.ended.recv_timeout(within).is_ok()
        }
    }

    fn until(mut f: impl FnMut() -> bool) -> bool {
        let deadline = Instant::now() + Duration::from_secs(10);
        while !f() {
            if Instant::now() > deadline {
                return false;
            }
            std::thread::sleep(Duration::from_millis(10));
        }
        true
    }

    const LONG: Duration = Duration::from_secs(10);

    /// The leader's state is read without reaping it: running, then exited for as long as it is
    /// not reaped, then gone.
    #[test]
    fn leader_is_running_then_exited_until_reaped_then_gone() {
        let mut g = Group::start(RUNNING);
        assert_eq!(leader(&g.child), Leader::Running);
        drop(g.stdin.take());
        assert!(
            until(|| leader(&g.child) == Leader::Exited),
            "the leader's exit was not seen"
        );
        assert_eq!(
            leader(&g.child),
            Leader::Exited,
            "looking at the exit reaped the leader"
        );
        assert!(g.child.wait().unwrap().success());
        assert_eq!(leader(&g.child), Leader::Gone);
        assert!(g.ended(LONG));
    }

    /// SIGTERM reaches every process of the child's group, and no other group.
    #[test]
    fn term_group_ends_the_childs_group_and_no_other() {
        let mut g = Group::start(RUNNING);
        let mut other = Group::start(RUNNING);
        assert!(term_group(&g.child));
        assert!(
            g.ended(LONG),
            "a process of the child's group outlived its SIGTERM"
        );
        assert_eq!(g.child.wait().unwrap().signal(), Some(libc::SIGTERM));
        assert_eq!(leader(&other.child), Leader::Running);
        drop(other.stdin.take());
        let status = other.child.wait().unwrap();
        assert!(status.success(), "another group was signalled: {status}");
        assert!(other.ended(LONG));
    }

    /// SIGKILL reaches every process of the child's group, and no other group.
    #[test]
    fn kill_group_ends_the_childs_group_and_no_other() {
        let mut g = Group::start(RUNNING);
        let mut other = Group::start(RUNNING);
        kill_group(&mut g.child);
        assert!(
            g.ended(LONG),
            "a process of the child's group outlived kill_group"
        );
        assert_eq!(g.child.wait().unwrap().signal(), Some(libc::SIGKILL));
        assert_eq!(leader(&other.child), Leader::Running);
        drop(other.stdin.take());
        let status = other.child.wait().unwrap();
        assert!(status.success(), "another group was signalled: {status}");
        assert!(other.ended(LONG));
    }

    /// Once the leader has exited, the rest of its group is still ended while the leader is
    /// unreaped (the group's number is still its own); once reaped, nothing is signalled.
    #[test]
    fn kill_group_ends_the_rest_of_the_group_until_the_leader_is_reaped() {
        let mut g = Group::start(LEADER_EXITS);
        assert!(until(|| leader(&g.child) == Leader::Exited));
        kill_group(&mut g.child);
        assert!(
            g.ended(LONG),
            "the rest of the group outlived kill_group after its leader exited"
        );
        assert!(g.child.wait().unwrap().success());

        let mut r = Group::start(LEADER_EXITS);
        assert!(until(|| leader(&r.child) == Leader::Exited));
        assert!(r.child.wait().unwrap().success());
        assert_eq!(leader(&r.child), Leader::Gone);
        kill_group(&mut r.child);
        assert!(
            !r.ended(Duration::from_secs(1)),
            "kill_group signalled the group of a reaped child"
        );
        drop(r.stdin.take());
        assert!(r.ended(LONG));
    }

    /// Whether a child the test starts, which makes a descriptor of the test's inheritable just
    /// before it executes, still holds it after, with close was `close_on_exec_from_3`.
    #[cfg(any(target_os = "linux", target_os = "macos"))]
    fn keeps_descriptor(close: bool) -> bool {
        use std::os::fd::AsRawFd;
        // std's pipe is close-on-exec in the test, so no other test's child gets it.
        let (mut r, w) = std::io::pipe().unwrap();
        let fd = w.as_raw_fd();
        let mut cmd = Command::new("/bin/sh");
        cmd.arg("-c")
            .arg("read _; exit 0")
            .stdin(Stdio::piped())
            .stdout(Stdio::null())
            .stderr(Stdio::null());
        // SAFETY: fcntl is async-signal-safe and allocates nothing; so does
        // close_on_exec_from_3, which is for this place only.
        unsafe {
            cmd.pre_exec(move || {
                if libc::fcntl(fd, libc::F_SETFD, 0) != 0 {
                    return Err(std::io::Error::last_os_error());
                }
                if close {
                    crate::owned::close_on_exec_from_3()?;
                }
                Ok(())
            });
        }
        let mut child = cmd.spawn().unwrap();
        drop(w);
        let (tx, rx) = mpsc::channel();
        std::thread::spawn(move || {
            let _ = r.read_to_end(&mut Vec::new());
            let _ = tx.send(());
        });
        // The pipe's end comes once no process holds its write end: at once unless the child
        // does, and then once the child ends.
        let kept = rx
            .recv_timeout(if close { LONG } else { Duration::from_secs(1) })
            .is_err();
        drop(child.stdin.take());
        assert!(child.wait().unwrap().success());
        if kept {
            assert!(rx.recv_timeout(LONG).is_ok());
        }
        kept
    }

    /// Every descriptor of the app's but 0-2 closes as a child executes.
    #[cfg(any(target_os = "linux", target_os = "macos"))]
    #[test]
    fn close_on_exec_from_3_lets_no_descriptor_through() {
        assert!(
            keeps_descriptor(false),
            "the probe did not see a descriptor the child was given"
        );
        assert!(
            !keeps_descriptor(true),
            "a descriptor above 2 reached the executed program"
        );
    }
}

/// The program a terminal runs on Linux, checked by the path the terminal follows
/// (`path_target`).
#[cfg(target_os = "linux")]
mod linux {
    use super::Temp;
    use crate::owned::{chmod_hint, path_target, path_target_by, private, private_group, Group};
    use std::fs::{self, File};
    use std::os::unix::fs::{symlink, PermissionsExt};
    use std::path::{Path, PathBuf};

    /// t's folder by its real path.
    fn real(t: &Temp) -> PathBuf {
        fs::canonicalize(&t.0).unwrap()
    }
    fn chmod(p: &Path, mode: u32) {
        fs::set_permissions(p, fs::Permissions::from_mode(mode)).unwrap();
    }
    fn folder(p: &Path, mode: u32) {
        fs::create_dir(p).unwrap();
        chmod(p, mode);
    }
    fn prog(p: &Path, mode: u32) {
        fs::write(p, "#!/bin/sh\n").unwrap();
        chmod(p, mode);
    }
    /// path_target of the file at p, opened now, with private saying which group is private.
    fn by(p: &Path, private: &dyn Fn(u32) -> bool) -> Result<(), String> {
        path_target_by(&File::open(p).unwrap(), p, private)
    }
    /// A refusal for part, saying why.
    fn refused(r: Result<(), String>, part: &Path, why: &str) {
        let e = r.expect_err(&part.display().to_string());
        assert!(
            e.starts_with(&format!("{} {why}", part.display())),
            "refused for {e}, not as {} {why}",
            part.display()
        );
    }
    fn gid() -> u32 {
        // SAFETY: getegid has no preconditions.
        unsafe { libc::getegid() }
    }
    /// What `id` says, in words.
    fn id(arg: &str) -> String {
        let out = std::process::Command::new("id").arg(arg).output().unwrap();
        assert!(out.status.success(), "id {arg}");
        String::from_utf8(out.stdout).unwrap().trim().to_string()
    }
    /// The name and members of group g, as `getent` gives them.
    fn group(g: u32) -> Option<(String, Vec<String>)> {
        let out = std::process::Command::new("getent")
            .args(["group", &g.to_string()])
            .output()
            .unwrap();
        let line = String::from_utf8(out.stdout).ok()?;
        let fields: Vec<&str> = line.trim().split(':').collect();
        let members = fields.get(3)?.split(',').filter(|m| !m.is_empty());
        Some((fields[0].to_string(), members.map(String::from).collect()))
    }
    /// Gives p the group g, which its owner may do as a member of g.
    fn set_group(p: &Path, g: u32) {
        let c = std::ffi::CString::new(p.as_os_str().as_encoded_bytes()).unwrap();
        // SAFETY: c is NUL-terminated.
        assert_eq!(
            unsafe { libc::chown(c.as_ptr(), u32::MAX, g) },
            0,
            "{}",
            p.display()
        );
    }

    /// Group write, by the person's own private group only (Homebrew on Linux, a umask of 002):
    /// a folder and a program 0775 in that group pass; the same in any other group, or writable
    /// by others, are refused, with the command that fixes it.
    #[test]
    fn group_write_is_taken_from_the_persons_private_group_only() {
        let t = Temp::new("group");
        let d = real(&t);
        let bin = d.join("bin");
        folder(&bin, 0o775);
        let p = bin.join("agent");
        prog(&p, 0o775);
        let mine = |g: u32| g == gid();
        let other = |_: u32| false;
        assert_eq!(by(&p, &mine), Ok(()));
        refused(by(&p, &other), &bin, "may be changed by other users");
        chmod(&bin, 0o755);
        refused(by(&p, &other), &p, "may be written by other users");
        let e = by(&p, &other).unwrap_err();
        assert!(
            e.contains(&format!("`chmod g-w,o-w '{}'`", p.display())),
            "{e}"
        );
        // Others may write: refused whatever the group.
        chmod(&p, 0o757);
        refused(by(&p, &mine), &p, "may be written by other users");
        chmod(&p, 0o755);
        chmod(&bin, 0o777);
        refused(by(&p, &mine), &bin, "may be changed by other users");
        // A sticky folder anyone may write, with the person's own entry (as /tmp).
        chmod(&bin, 0o1777);
        assert_eq!(by(&p, &other), Ok(()));
    }

    /// A group is private only when it is this user's alone, by each of five conditions: each
    /// one broken alone says no.
    #[test]
    fn a_group_is_private_only_when_each_condition_holds() {
        let me = vec!["me".to_string()];
        let bob = vec!["me".to_string(), "bob".to_string()];
        let base = Group {
            user: "me",
            primary: 1000,
            name: "me",
            gid: 1000,
            members: &me,
            others_primary: &[100, 1001],
            other_lines: &[0, 100, 1001],
        };
        assert!(private(&base));
        assert!(
            private(&Group {
                members: &[],
                ..base
            }),
            "no member listed"
        );
        assert!(
            !private(&Group {
                primary: 100,
                ..base
            }),
            "not the primary group"
        );
        assert!(
            !private(&Group {
                name: "devs",
                ..base
            }),
            "named otherwise"
        );
        assert!(
            !private(&Group {
                members: &bob,
                ..base
            }),
            "another member"
        );
        assert!(
            !private(&Group {
                others_primary: &[100, 1000],
                ..base
            }),
            "another user's primary group (useradd -g)"
        );
        assert!(
            !private(&Group {
                other_lines: &[0, 1000],
                ..base
            }),
            "another group line with its number (devs:x:1000:bob)"
        );
    }

    /// The hint quotes the path for a shell, a `'` in it too, and says when root must run it.
    #[test]
    fn the_chmod_hint_quotes_the_path_for_a_shell() {
        let p = Path::new("/home/me/it's here");
        assert_eq!(
            chmod_hint(p, false),
            r"`chmod g-w,o-w '/home/me/it'\''s here'` makes it yours alone"
        );
        assert_eq!(
            chmod_hint(p, true),
            r"as root, `chmod g-w,o-w '/home/me/it'\''s here'` makes it root's alone"
        );
        // What a shell makes of the quoted path is the path.
        let quoted = chmod_hint(p, false);
        let arg = quoted
            .strip_prefix("`chmod g-w,o-w ")
            .and_then(|q| q.split_once("` "))
            .unwrap()
            .0;
        let out = std::process::Command::new("/bin/sh")
            .args(["-c", &format!("printf %s {arg}")])
            .output()
            .unwrap();
        assert_eq!(String::from_utf8(out.stdout).unwrap(), p.to_str().unwrap());
    }

    /// The person's private group as `id` and `getent` tell: their primary group, named as they
    /// are, with no other member, no other user's primary group and no other group with its
    /// number; and the real check takes group write from it alone.
    #[test]
    fn the_private_group_is_the_persons_own_and_no_other() {
        let user = id("-un");
        let primary: u32 = id("-g").parse().unwrap();
        let groups: Vec<u32> = id("-G")
            .split_whitespace()
            .map(|g| g.parse().unwrap())
            .collect();
        // Every user's (name, primary group) and every group's (name, number), from getent.
        let table = |db: &str, at: usize| -> Vec<(String, u32)> {
            let out = std::process::Command::new("getent")
                .arg(db)
                .output()
                .unwrap();
            String::from_utf8(out.stdout)
                .unwrap()
                .lines()
                .filter_map(|l| {
                    let f: Vec<&str> = l.split(':').collect();
                    Some((f.first()?.to_string(), f.get(at)?.parse().ok()?))
                })
                .collect()
        };
        let (users, lines) = (table("passwd", 3), table("group", 2));
        let expected = |g: u32| {
            g == primary
                && group(g).is_some_and(|(name, members)| {
                    name == user
                        && members.iter().all(|m| *m == user)
                        && !users.iter().any(|(u, p)| *u != user && *p == g)
                        && lines.iter().filter(|(_, n)| *n == g).count() == 1
                })
        };
        for &g in groups.iter().chain([0, 65534].iter()) {
            assert_eq!(private_group(g), expected(g), "group {g}");
        }
        let t = Temp::new("real-group");
        let d = real(&t);
        if expected(primary) {
            let bin = d.join("bin");
            folder(&bin, 0o775);
            set_group(&bin, primary);
            prog(&bin.join("agent"), 0o775);
            set_group(&bin.join("agent"), primary);
            let p = bin.join("agent");
            assert_eq!(path_target(&File::open(&p).unwrap(), &p), Ok(()));
        } else {
            println!("skipped: {user}'s primary group {primary} is not a private group");
        }
        match groups.iter().find(|g| !expected(**g)) {
            Some(&g) => {
                let shared = d.join("shared");
                folder(&shared, 0o775);
                set_group(&shared, g);
                let p = shared.join("agent");
                prog(&p, 0o755);
                refused(
                    path_target(&File::open(&p).unwrap(), &p),
                    &shared,
                    "may be changed by other users",
                );
            }
            None => println!("skipped: {user} is in no group but a private one"),
        }
    }

    /// The file the path leads to must be the one opened: another renamed over it is refused.
    #[test]
    fn a_file_renamed_over_the_path_after_it_was_opened_is_refused() {
        let t = Temp::new("same");
        let d = real(&t);
        let p = d.join("agent");
        prog(&p, 0o755);
        let f = File::open(&p).unwrap();
        prog(&d.join("new"), 0o755);
        fs::rename(d.join("new"), &p).unwrap();
        let e = path_target(&f, &p).unwrap_err();
        assert!(e.contains("no longer the file"), "{e}");
    }

    /// `..` after an absolute link resolves from the link's target, as the kernel does; a link
    /// loop ends in a refusal; relative and missing paths are refused; a name that is not UTF-8
    /// is fine.
    #[test]
    fn links_and_dot_dot_resolve_as_the_kernel_resolves_them() {
        use std::os::unix::ffi::OsStrExt;
        let t = Temp::new("links");
        let d = real(&t);
        let real_dir = d.join("real");
        fs::create_dir_all(real_dir.join("sub")).unwrap();
        prog(&real_dir.join("agent"), 0o755);
        let bin = d.join("bin");
        folder(&bin, 0o755);
        prog(&bin.join("agent"), 0o755);
        symlink(real_dir.join("sub"), bin.join("l")).unwrap();
        let named = bin.join("l").join("..").join("agent");
        assert_eq!(path_target(&File::open(&named).unwrap(), &named), Ok(()));
        let lexical = File::open(bin.join("agent")).unwrap();
        assert!(
            path_target(&lexical, &named).is_err(),
            "the lexical file passed"
        );

        symlink("a", d.join("b")).unwrap();
        symlink("b", d.join("a")).unwrap();
        let e = path_target(&lexical, &d.join("a")).unwrap_err();
        assert!(e.contains("too many links"), "{e}");

        assert!(path_target(&lexical, Path::new("agent")).is_err());
        assert!(path_target(&lexical, &d.join("missing")).is_err());
        let odd = d.join(std::ffi::OsStr::from_bytes(b"\xff"));
        folder(&odd, 0o755);
        prog(&odd.join("agent"), 0o755);
        let p = odd.join("agent");
        assert_eq!(path_target(&File::open(&p).unwrap(), &p), Ok(()));
    }

    /// A path through the process file system leads elsewhere for the terminal than for the app
    /// (`/proc/self` is each process's own): refused, and `/dev/fd` and `/dev/stdin` with it.
    #[test]
    fn a_path_through_the_process_file_system_is_refused() {
        let t = Temp::new("proc");
        let d = real(&t);
        let p = d.join("agent");
        prog(&p, 0o755);
        let f = File::open(&p).unwrap();
        let root = PathBuf::from("/proc/self/root").join(p.strip_prefix("/").unwrap());
        let fd = PathBuf::from(format!("/dev/fd/{}", std::os::fd::AsRawFd::as_raw_fd(&f)));
        for path in [root, fd, PathBuf::from("/dev/stdin")] {
            let e = path_target(&f, &path).unwrap_err();
            assert!(
                e.contains("is in the process file system"),
                "{}: {e}",
                path.display()
            );
        }
    }
}

/// The paths in `secure`'s suggested commands are one shell word each, whatever they hold.
#[cfg(unix)]
#[test]
fn a_path_is_quoted_as_one_shell_word() {
    use std::os::unix::ffi::OsStrExt;
    let p = Path::new("/Users/a b/it's here/$HOME`x`/prog");
    let q = crate::owned::shell_quoted(p);
    assert_eq!(q, r"'/Users/a b/it'\''s here/$HOME`x`/prog'");
    let out = std::process::Command::new("/bin/sh")
        .arg("-c")
        .arg(format!("printf %s {q}"))
        .output()
        .unwrap();
    assert!(out.status.success(), "{out:?}");
    assert_eq!(out.stdout, p.as_os_str().as_bytes());
}

#[cfg(target_os = "macos")]
mod macos {
    use super::Temp;
    use crate::owned::{exec_target, secure, Writers};
    use std::fs;
    use std::os::unix::fs::{MetadataExt, PermissionsExt};
    use std::path::{Path, PathBuf};

    /// t's folder by its real path (the system's temporary folder is under the link `/var`).
    fn real(t: &Temp) -> PathBuf {
        fs::canonicalize(&t.0).unwrap()
    }

    fn file(p: &Path, mode: u32) {
        fs::write(p, b"#!/bin/sh\n").unwrap();
        fs::set_permissions(p, fs::Permissions::from_mode(mode)).unwrap();
    }

    /// A refusal that names part.
    fn refused(path: &Path, part: &Path, writers: Writers) {
        let e = secure(path, writers).expect_err(&path.display().to_string());
        assert!(
            e.starts_with(&format!("{} ", part.display())),
            "{} refused for {e}, not for {}",
            path.display(),
            part.display()
        );
    }

    #[test]
    fn secure_takes_a_file_no_other_user_can_change() {
        let t = Temp::new("secure");
        let dir = real(&t);
        let p = dir.join("prog");
        file(&p, 0o700);
        assert_eq!(secure(&p, Writers::Nobody), Ok(()));

        // In a folder anyone can write to with the sticky bit set (as /tmp), only the owner of
        // an entry can rename or remove it.
        let sticky = dir.join("sticky");
        fs::create_dir(&sticky).unwrap();
        fs::set_permissions(&sticky, fs::Permissions::from_mode(0o1777)).unwrap();
        let q = sticky.join("prog");
        file(&q, 0o700);
        assert_eq!(secure(&q, Writers::Nobody), Ok(()));
    }

    #[test]
    fn secure_refuses_what_another_user_could_change() {
        let t = Temp::new("insecure");
        let dir = real(&t);

        let others = dir.join("others");
        file(&others, 0o702);
        refused(&others, &others, Writers::Nobody);

        // A group writable file, in any group: the user's own, and admin's.
        let group = dir.join("group");
        file(&group, 0o720);
        set_group(&group, unsafe { libc::getegid() });
        refused(&group, &group, Writers::Nobody);
        let admins = dir.join("admins");
        file(&admins, 0o720);
        set_group(&admins, admin_gid());
        refused(&admins, &admins, Writers::Nobody);

        let open = dir.join("open");
        fs::create_dir(&open).unwrap();
        fs::set_permissions(&open, fs::Permissions::from_mode(0o777)).unwrap();
        let inside = open.join("prog");
        file(&inside, 0o700);
        refused(&inside, &open, Writers::Nobody);

        let real_dir = dir.join("real");
        fs::create_dir(&real_dir).unwrap();
        file(&real_dir.join("prog"), 0o700);
        let link = dir.join("link");
        std::os::unix::fs::symlink(&real_dir, &link).unwrap();
        refused(&link.join("prog"), &link, Writers::Nobody);
        let file_link = dir.join("file-link");
        std::os::unix::fs::symlink(real_dir.join("prog"), &file_link).unwrap();
        refused(&file_link, &file_link, Writers::Nobody);

        refused(Path::new("prog"), Path::new("prog"), Writers::Nobody);
    }

    /// The `admin` group's number, looked up here with `getgrnam_r` (the thread-safe `getgrnam`),
    /// not with the product's own lookup (`owned::admin_gid`), so the tests do not check the product
    /// against itself. No subprocess: parsing `dscl`'s output, as this did before, now and then
    /// found a last word that was not a number on cloud macOS runners, for a cause not found.
    fn admin_gid() -> u32 {
        let mut g: libc::group = unsafe { std::mem::zeroed() };
        let mut buf = vec![0 as libc::c_char; 1 << 20];
        let mut found: *mut libc::group = std::ptr::null_mut();
        let r = unsafe {
            libc::getgrnam_r(
                c"admin".as_ptr(),
                &mut g,
                buf.as_mut_ptr(),
                buf.len(),
                &mut found,
            )
        };
        assert!(
            r == 0 && !found.is_null(),
            "looking up the group admin: getgrnam_r returned {r} ({}), found {}",
            std::io::Error::from_raw_os_error(r),
            !found.is_null()
        );
        g.gr_gid
    }

    /// Gives p the group gid, which its owner may do only as a member of that group.
    fn set_group(p: &Path, gid: u32) {
        let c = std::ffi::CString::new(p.as_os_str().as_encoded_bytes()).unwrap();
        assert_eq!(
            unsafe { libc::chown(c.as_ptr(), u32::MAX, gid) },
            0,
            "giving {} the group {gid}: {} (the test's user must be in that group)",
            p.display(),
            std::io::Error::last_os_error()
        );
    }

    fn folder(p: &Path, mode: u32) {
        fs::create_dir(p).unwrap();
        fs::set_permissions(p, fs::Permissions::from_mode(mode)).unwrap();
    }

    #[test]
    fn secure_lets_the_admin_group_write_a_folder_above_a_launch_only() {
        let t = Temp::new("admin");
        let dir = real(&t);
        let admin = admin_gid();

        // As /Applications (root:admin 0775) or a Homebrew folder (the user's, admin, 0775).
        let shared = dir.join("shared");
        folder(&shared, 0o775);
        set_group(&shared, admin);
        let p = shared.join("prog");
        file(&p, 0o700);
        // The app's own core and native tools: refused, as for any other group.
        refused(&p, &shared, Writers::Nobody);
        // A launch's program.
        assert_eq!(secure(&p, Writers::Admins), Ok(()));

        // Never the file itself.
        let own = dir.join("admin-file");
        file(&own, 0o770);
        set_group(&own, admin);
        refused(&own, &own, Writers::Admins);

        // Nor a folder that another group may write, or everyone.
        let staff = dir.join("staff");
        folder(&staff, 0o775);
        let gid = unsafe { libc::getegid() };
        assert!(gid != admin, "the test runs with group {gid}, admin's");
        set_group(&staff, gid);
        file(&staff.join("prog"), 0o700);
        refused(&staff.join("prog"), &staff, Writers::Admins);
        let open = dir.join("open");
        folder(&open, 0o777);
        set_group(&open, admin);
        file(&open.join("prog"), 0o700);
        refused(&open.join("prog"), &open, Writers::Admins);
    }

    #[test]
    fn secure_refuses_a_folder_the_wheel_group_may_write() {
        // A folder made in /private/tmp (root:wheel) gets its group: wheel (0).
        let t = Temp(PathBuf::from(format!(
            "/private/tmp/pluxy-owned-wheel-{}",
            std::process::id()
        )));
        let _ = fs::remove_dir_all(&t.0);
        folder(&t.0, 0o770);
        assert_eq!(fs::metadata(&t.0).unwrap().gid(), 0, "{}", t.0.display());
        let p = t.0.join("prog");
        file(&p, 0o700);
        refused(&p, &t.0, Writers::Nobody);
        refused(&p, &t.0, Writers::Admins);
    }

    #[test]
    fn exec_target_is_the_opened_file_until_it_changes() {
        let t = Temp::new("exec");
        let dir = real(&t);

        let p = dir.join("prog");
        file(&p, 0o700);
        let f = fs::File::open(&p).unwrap();
        assert_eq!(exec_target(&f, Writers::Nobody).unwrap(), p);
        // Written in place it is still the file that was opened, as on Linux, where the kernel
        // runs what it holds then: what it holds is checked by the callers (`recheck`).
        fs::write(&p, b"#!/bin/sh\necho changed\n").unwrap();
        assert_eq!(exec_target(&f, Writers::Nobody).unwrap(), p);

        let q = dir.join("swapped");
        file(&q, 0o700);
        let g = fs::File::open(&q).unwrap();
        assert_eq!(exec_target(&g, Writers::Nobody).unwrap(), q);
        let other = dir.join("other");
        file(&other, 0o700);
        fs::rename(&other, &q).unwrap();
        assert!(
            exec_target(&g, Writers::Nobody).is_err(),
            "a file replaced after it was opened"
        );

        // Below a folder the admin group may write, it runs only under the rule that allows it.
        let shared = dir.join("shared");
        folder(&shared, 0o775);
        set_group(&shared, admin_gid());
        let r = shared.join("prog");
        file(&r, 0o700);
        let h = fs::File::open(&r).unwrap();
        assert_eq!(exec_target(&h, Writers::Admins).unwrap(), r);
        let e = exec_target(&h, Writers::Nobody).unwrap_err();
        assert!(e.contains("may be changed by other users"), "{e}");
    }

    /// The place of the entry that holds text in path's access list, as macOS's own `ls -le`
    /// numbers it.
    fn entry_index(path: &Path, text: &str) -> usize {
        let out = std::process::Command::new("/bin/ls")
            .arg("-led")
            .arg(path)
            .output()
            .unwrap();
        let listed = String::from_utf8_lossy(&out.stdout).into_owned();
        let line = listed
            .lines()
            .skip(1)
            .find(|l| l.contains(text))
            .unwrap_or_else(|| panic!("no entry {text:?} on {}: {listed}", path.display()));
        line.trim().split(':').next().unwrap().parse().unwrap()
    }

    fn chmod(args: &[&str], path: &Path) {
        let out = std::process::Command::new("/bin/chmod")
            .args(args)
            .arg(path)
            .output()
            .unwrap();
        assert!(
            out.status.success(),
            "chmod {args:?} {}: {out:?}",
            path.display()
        );
    }

    #[test]
    fn secure_refuses_an_access_list_entry_that_lets_another_user_change_the_program() {
        use crate::private_macos::add_entry;
        let t = Temp::new("acl-file");
        let dir = real(&t);

        // Rights that change nothing pass.
        let reads = dir.join("reads");
        file(&reads, 0o700);
        add_entry(
            &reads,
            "user:daemon allow read,execute,readattr,readsecurity",
        );
        assert_eq!(secure(&reads, Writers::Nobody), Ok(()));

        // write, with a deny entry beside it, so the allow entry need not come first.
        let p = dir.join("prog");
        file(&p, 0o700);
        add_entry(&p, "everyone deny delete");
        add_entry(&p, "user:daemon allow write");
        let i = entry_index(&p, "user:daemon allow write");
        for writers in [Writers::Nobody, Writers::Admins] {
            refused(&p, &p, writers);
        }
        let e = secure(&p, Writers::Admins).unwrap_err();
        assert!(e.contains("user:daemon"), "{e}");
        assert!(e.contains("(write)"), "{e}");
        let remove = format!("`chmod -a# {i} {}`", crate::owned::shell_quoted(&p));
        assert!(e.contains(&remove), "{e} does not say {remove}");
        // Doing what it says passes.
        chmod(&["-a#", &i.to_string()], &p);
        assert_eq!(secure(&p, Writers::Nobody), Ok(()));

        // An entry for the admin group on the file itself, even for a launch.
        let q = dir.join("admin-entry");
        file(&q, 0o700);
        add_entry(&q, "group:admin allow append");
        refused(&q, &q, Writers::Admins);

        // An entry for everyone.
        let r = dir.join("everyone");
        file(&r, 0o700);
        add_entry(&r, "everyone allow write");
        refused(&r, &r, Writers::Nobody);
        let e = secure(&r, Writers::Nobody).unwrap_err();
        assert!(e.contains("group:everyone change it (write)"), "{e}");
    }

    /// Gives a new program, or a new folder above one, an entry for user:daemon holding exactly
    /// rights, and what `secure` makes of it under each rule.
    fn with_raw_entry(
        dir: &Path,
        name: &str,
        on_folder: bool,
        rights: u32,
    ) -> [Result<(), String>; 2] {
        let f = dir.join(name);
        folder(&f, 0o700);
        let p = f.join("prog");
        file(&p, 0o700);
        crate::private_macos::set_raw_entry(if on_folder { &f } else { &p }, "user:daemon", rights);
        let part = if on_folder { &f } else { &p };
        let got = [secure(&p, Writers::Nobody), secure(&p, Writers::Admins)];
        for e in got.iter().filter_map(|r| r.as_ref().err()) {
            assert!(
                e.starts_with(&format!("{} ", part.display())),
                "{e} is not for {}",
                part.display()
            );
        }
        got
    }

    #[test]
    fn secure_refuses_every_right_that_changes_the_program_or_a_folder_above() {
        use crate::private_macos::{FILE_CHANGES, FOLDER_CHANGES, READ_ONLY};
        let t = Temp::new("acl-rights");
        let dir = real(&t);
        for (on_folder, names) in [(false, FILE_CHANGES), (true, FOLDER_CHANGES)] {
            for (bit, name) in names {
                let got =
                    with_raw_entry(&dir, &format!("{on_folder}-{name}"), on_folder, *bit as u32);
                for r in got {
                    let e = r.expect_err(name);
                    assert!(
                        e.contains(&format!("user:daemon change it ({name})")),
                        "{e}"
                    );
                }
            }
            // Every read-only right at once passes: read, execute, readattr, readextattr,
            // readsecurity, synchronize, generic_execute and generic_read (`sys/acl.h`), written
            // out so that a bit added to `READ_ONLY` by mistake fails here.
            let reads = 1 << 1 | 1 << 3 | 1 << 7 | 1 << 9 | 1 << 11 | 1 << 20 | 1 << 22 | 1 << 24;
            assert_eq!(READ_ONLY, reads);
            let got = with_raw_entry(&dir, &format!("{on_folder}-reads"), on_folder, reads as u32);
            assert_eq!(got, [Ok(()), Ok(())], "on a folder: {on_folder}");
            // A right no one names is refused, named by its number.
            let got = with_raw_entry(&dir, &format!("{on_folder}-unknown"), on_folder, 1 << 14);
            for r in got {
                let e = r.expect_err("bit 14");
                assert!(e.contains("user:daemon change it (bit 14)"), "{e}");
            }
        }
    }

    #[test]
    fn secure_refuses_the_generic_write_and_all_rights() {
        let t = Temp::new("acl-generic");
        let dir = real(&t);
        for on_folder in [false, true] {
            for (bit, name) in [(23, "generic_write"), (21, "generic_all")] {
                let got = with_raw_entry(&dir, &format!("{on_folder}-{name}"), on_folder, 1 << bit);
                for r in got {
                    let e = r.expect_err(name);
                    assert!(e.contains(&format!("({name})")), "{e}");
                }
            }
        }
    }

    #[test]
    fn secure_refuses_an_access_list_entry_that_lets_another_user_change_a_folder_above() {
        use crate::private_macos::add_entry;
        let t = Temp::new("acl-folder");
        let dir = real(&t);

        for right in ["add_file", "delete_child", "add_subdirectory", "delete"] {
            let f = dir.join(right);
            folder(&f, 0o700);
            let p = f.join("prog");
            file(&p, 0o700);
            assert_eq!(secure(&p, Writers::Nobody), Ok(()), "{right}");
            add_entry(&f, &format!("user:daemon allow {right}"));
            refused(&p, &f, Writers::Nobody);
            refused(&p, &f, Writers::Admins);
            let e = secure(&p, Writers::Nobody).unwrap_err();
            assert!(
                e.contains(&format!("user:daemon change it ({right})")),
                "{e}"
            );
            let clear = format!("`chmod -N {}`", crate::owned::shell_quoted(&f));
            assert!(e.contains(&clear), "{e} does not say {clear}");
            chmod(&["-N"], &f);
            assert_eq!(secure(&p, Writers::Nobody), Ok(()), "{right} cleared");
        }

        // A folder is judged before what is below it: with an entry on both, the refusal names
        // the folder. Walked from the program up, another user could swap the program and then
        // remove their entry on the folder before it was read.
        let both = dir.join("both");
        folder(&both, 0o700);
        let p = both.join("prog");
        file(&p, 0o700);
        add_entry(&both, "user:daemon allow add_file");
        add_entry(&p, "user:daemon allow write");
        refused(&p, &both, Writers::Nobody);
        refused(&p, &both, Writers::Admins);

        // A deny entry, as a home folder carries.
        let home = dir.join("home");
        folder(&home, 0o700);
        let p = home.join("prog");
        file(&p, 0o700);
        add_entry(&home, "group:everyone deny delete");
        add_entry(&p, "group:everyone deny delete");
        assert_eq!(secure(&p, Writers::Nobody), Ok(()));
        assert_eq!(secure(&p, Writers::Admins), Ok(()));
    }

    #[test]
    fn secure_lets_an_admin_entry_on_a_folder_above_a_launch_only() {
        use crate::private_macos::add_entry;
        let t = Temp::new("acl-admin");
        let dir = real(&t);
        let shared = dir.join("shared");
        folder(&shared, 0o755);
        let p = shared.join("prog");
        file(&p, 0o700);
        add_entry(
            &shared,
            "group:admin allow add_file,add_subdirectory,delete_child",
        );
        refused(&p, &shared, Writers::Nobody);
        assert_eq!(secure(&p, Writers::Admins), Ok(()));

        // Another group's is refused for a launch too.
        let staff = dir.join("staff");
        folder(&staff, 0o755);
        file(&staff.join("prog"), 0o700);
        add_entry(&staff, "group:staff allow add_file");
        refused(&staff.join("prog"), &staff, Writers::Admins);
    }

    #[test]
    fn secure_refuses_an_inherited_access_list_entry() {
        use crate::private_macos::add_entry;
        let t = Temp::new("acl-inherit");
        let dir = real(&t);

        // Entries that reach only what is made later: the folder itself passes.
        let outer = dir.join("outer");
        folder(&outer, 0o700);
        add_entry(
            &outer,
            "user:daemon allow add_file,directory_inherit,only_inherit",
        );
        // A file made in it takes nothing: the entry reaches folders only.
        let plain = outer.join("plain");
        file(&plain, 0o700);
        assert_eq!(secure(&plain, Writers::Nobody), Ok(()));

        // A folder made in it inherits the entry, and is refused for it.
        let inner = outer.join("inner");
        folder(&inner, 0o700);
        entry_index(&inner, "inherited");
        let p = inner.join("prog");
        file(&p, 0o700);
        refused(&p, &inner, Writers::Nobody);

        // A program made in a folder with a file entry inherits it, and is refused for it.
        let files = dir.join("files");
        folder(&files, 0o700);
        add_entry(&files, "user:daemon allow write,file_inherit,only_inherit");
        let q = files.join("prog");
        file(&q, 0o700);
        entry_index(&q, "inherited");
        refused(&q, &q, Writers::Nobody);
        let e = secure(&q, Writers::Nobody).unwrap_err();
        assert!(e.contains("user:daemon change it (write)"), "{e}");
    }
}

#[cfg(windows)]
mod windows {
    use super::Temp;
    use crate::owned::windows::{
        breakaway, command_line, environment_block, final_path, image, pin_dir, pin_file,
        std_spawn, NO_BREAKAWAY,
    };
    use std::ffi::OsString;
    use std::fs::{self, File, OpenOptions};
    use std::os::windows::ffi::{OsStrExt, OsStringExt};
    use std::os::windows::fs::OpenOptionsExt;
    use std::path::{Path, PathBuf};
    use windows_sys::Win32::Foundation::LocalFree;
    use windows_sys::Win32::Storage::FileSystem::FILE_FLAG_BACKUP_SEMANTICS;
    use windows_sys::Win32::System::JobObjects::IsProcessInJob;
    use windows_sys::Win32::System::Threading::{GetCurrentProcess, CREATE_BREAKAWAY_FROM_JOB};

    /// std's full path of p (`GetFinalPathNameByHandleW` too), in plain drive or UNC form.
    fn plain(p: &Path) -> PathBuf {
        let s = fs::canonicalize(p)
            .unwrap()
            .into_os_string()
            .into_string()
            .unwrap();
        PathBuf::from(if let Some(rest) = s.strip_prefix(r"\\?\UNC\") {
            format!(r"\\{rest}")
        } else {
            s.strip_prefix(r"\\?\").unwrap_or(&s).to_string()
        })
    }

    /// p, as a path Windows' path rules leave as it is (a name may end in a dot or a space).
    fn verbatim(p: &Path) -> PathBuf {
        PathBuf::from(format!(r"\\?\{}", plain(p).display()))
    }

    fn open_dir(p: &Path) -> File {
        OpenOptions::new()
            .read(true)
            .custom_flags(FILE_FLAG_BACKUP_SEMANTICS)
            .open(p)
            .unwrap()
    }

    #[test]
    fn final_path_is_where_the_opened_file_is() {
        let t = Temp::new("final");
        let dir = t.0.join("a folder");
        fs::create_dir(&dir).unwrap();
        let p = dir.join("f.txt");
        fs::write(&p, b"x").unwrap();
        assert_eq!(final_path(&File::open(&p).unwrap()).unwrap(), plain(&p));
        assert_eq!(final_path(&open_dir(&dir)).unwrap(), plain(&dir));

        // Opened through a junction: the path of the file itself.
        let junction = t.0.join("junction");
        let made = std_spawn(
            std::process::Command::new("cmd")
                .args(["/d", "/c", "mklink", "/J"])
                .arg(&junction)
                .arg(&dir)
                .stdout(std::process::Stdio::null()),
        )
        .unwrap()
        .wait()
        .unwrap();
        assert!(made.success(), "mklink /J: {made}");
        let through = junction.join("f.txt");
        assert_eq!(
            final_path(&File::open(&through).unwrap()).unwrap(),
            plain(&p)
        );
    }

    /// Windows' path rules read a name ending in a dot or a space, and a long path, otherwise
    /// than the file system has it: refused.
    #[test]
    fn final_path_refuses_a_path_windows_would_read_otherwise() {
        let t = Temp::new("final-refused");
        for name in ["dot.", "space "] {
            let p = verbatim(&t.0).join(name);
            fs::write(&p, b"x").unwrap();
            assert!(final_path(&File::open(&p).unwrap()).is_err(), "{name:?}");
        }
        let mut long = verbatim(&t.0);
        for _ in 0..5 {
            long.push("a".repeat(60));
        }
        fs::create_dir_all(&long).unwrap();
        let p = long.join("f.txt");
        fs::write(&p, b"x").unwrap();
        assert!(final_path(&File::open(&p).unwrap()).is_err());
    }

    #[test]
    fn image_takes_exe_and_com_programs_only() {
        let t = Temp::new("image");
        let pe: &[u8] = b"MZ\x90\0fixture";
        for (name, bytes, ok) in [
            ("p.exe", pe, true),
            ("P.EXE", pe, true),
            ("p.com", pe, true),
            ("p.exe", b"#!/bin/sh\n".as_slice(), false),
            ("p.exe", b"M", false),
            ("p.exe", b"", false),
            ("p.bat", pe, false),
            ("p.cmd", pe, false),
            ("p.ps1", pe, false),
            ("p.exe.txt", pe, false),
            ("p", pe, false),
        ] {
            let p = t.0.join(name);
            fs::write(&p, bytes).unwrap();
            let r = image(&File::open(&p).unwrap(), &p);
            assert_eq!(r.is_ok(), ok, "{name} {bytes:?}: {r:?}");
            fs::remove_file(&p).unwrap();
        }
    }

    /// While pinned, no process can write, move, replace or remove the program; once let go, it
    /// can.
    #[test]
    fn a_pinned_program_stays_until_let_go() {
        let t = Temp::new("pin");
        let exe = t.0.join("prog.exe");
        fs::write(&exe, b"MZ one").unwrap();
        let other = t.0.join("other.exe");
        fs::write(&other, b"MZ two").unwrap();
        let held = File::open(&exe).unwrap();
        let pinned = pin_file(&held).unwrap();
        drop(held);
        assert_eq!(pinned.path, plain(&exe));

        assert!(
            OpenOptions::new().write(true).open(&exe).is_err(),
            "written"
        );
        assert!(fs::rename(&other, &exe).is_err(), "replaced");
        assert!(fs::rename(&exe, t.0.join("moved.exe")).is_err(), "moved");
        assert!(fs::remove_file(&exe).is_err(), "removed");
        assert_eq!(fs::read(&exe).unwrap(), b"MZ one");

        drop(pinned);
        fs::write(&exe, b"MZ three").unwrap();
        fs::rename(&other, &exe).unwrap();
        fs::rename(&exe, t.0.join("moved.exe")).unwrap();
        fs::remove_file(t.0.join("moved.exe")).unwrap();
    }

    /// The folders above a pinned program cannot be renamed while it is pinned either (NTFS), so
    /// the path it starts by stays its path.
    #[test]
    fn the_folders_above_a_pinned_program_stay_until_let_go() {
        let t = Temp::new("pin-folders");
        let (a, b) = (t.0.join("a"), t.0.join("a").join("b"));
        fs::create_dir_all(&b).unwrap();
        let exe = b.join("prog.exe");
        fs::write(&exe, b"MZ").unwrap();
        let held = File::open(&exe).unwrap();
        let pinned = pin_file(&held).unwrap();
        drop(held);

        assert!(fs::rename(&b, a.join("b2")).is_err(), "its folder renamed");
        assert!(
            fs::rename(&a, t.0.join("a2")).is_err(),
            "the folder above renamed"
        );

        drop(pinned);
        fs::rename(&b, a.join("b2")).unwrap();
        fs::rename(&a, t.0.join("a2")).unwrap();
    }

    #[test]
    fn pin_file_refuses_what_is_not_a_program() {
        let t = Temp::new("pin-refused");
        for (name, bytes) in [
            ("script.cmd", b"MZ".as_slice()),
            ("script.exe", b"@echo off"),
        ] {
            let p = t.0.join(name);
            fs::write(&p, bytes).unwrap();
            assert!(pin_file(&File::open(&p).unwrap()).is_err(), "{name}");
        }
    }

    /// While pinned, the folder cannot be moved or removed; what is in it can change.
    #[test]
    fn a_pinned_folder_stays_until_let_go() {
        let t = Temp::new("pin-dir");
        let d = t.0.join("work");
        fs::create_dir(&d).unwrap();
        let held = open_dir(&d);
        let pinned = pin_dir(&held).unwrap();
        drop(held);
        assert_eq!(pinned.path, plain(&d));

        assert!(fs::rename(&d, t.0.join("moved")).is_err(), "moved");
        assert!(fs::remove_dir(&d).is_err(), "removed");
        fs::write(d.join("inside"), b"x").unwrap();
        fs::remove_file(d.join("inside")).unwrap();

        drop(pinned);
        fs::rename(&d, t.0.join("moved")).unwrap();
    }

    /// A copy of `cmd.exe` in t, and the run of it that waits for a line on its input.
    fn waiting_cmd(t: &Temp) -> (PathBuf, crate::owned::windows::Command) {
        use crate::owned::windows::{Command, Io, Kind};
        let root = std::env::var_os("SystemRoot").expect("SystemRoot");
        let exe = t.0.join("prog.exe");
        fs::copy(Path::new(&root).join("System32").join("cmd.exe"), &exe).unwrap();
        let mut c = Command::new(pin_file(&File::open(&exe).unwrap()).unwrap(), Kind::Tool);
        c.args(["/d", "/c", "set /p x="]).stdin(Io::Piped);
        (exe, c)
    }

    /// A pin kept with a run (`keep`, `keep_program`) holds for as long as the child is held,
    /// not only until it starts: a running program can be renamed on Windows otherwise, and a
    /// file it only reads (a Linux program `wsl.exe` runs) written.
    #[test]
    fn kept_pins_last_as_long_as_the_child() {
        use crate::owned::windows::pin_other;
        let t = Temp::new("keep");

        // Without keep_program, the running copy can be renamed: what keep_program changes.
        let (exe, mut c) = waiting_cmd(&t);
        let mut child = c.spawn().unwrap();
        drop(c);
        let moved = t.0.join("moved.exe");
        fs::rename(&exe, &moved).unwrap();
        drop(child.stdin.take());
        child.wait().unwrap();
        drop(child);
        fs::remove_file(&moved).unwrap();

        let data = t.0.join("data.bin");
        fs::write(&data, b"a Linux program").unwrap();
        let (exe, mut c) = waiting_cmd(&t);
        c.keep_program()
            .keep(pin_other(&File::open(&data).unwrap()).unwrap());
        let mut child = c.spawn().unwrap();
        drop(c);
        assert!(fs::rename(&exe, &moved).is_err(), "the program renamed");
        assert!(fs::remove_file(&data).is_err(), "the kept file removed");
        assert!(
            OpenOptions::new().write(true).open(&data).is_err(),
            "the kept file written"
        );
        drop(child.stdin.take());
        assert!(child.wait().is_ok());
        drop(child);
        fs::rename(&exe, &moved).unwrap();
        fs::remove_file(&data).unwrap();
    }

    /// A detached launch is not held, so it cannot keep pins: refused before anything starts.
    #[test]
    fn a_detached_launch_keeps_no_pins() {
        use crate::owned::windows::{Command, Kind};
        let t = Temp::new("keep-detached");
        let root = std::env::var_os("SystemRoot").expect("SystemRoot");
        let exe = t.0.join("prog.exe");
        fs::copy(Path::new(&root).join("System32").join("cmd.exe"), &exe).unwrap();
        let mut c = Command::new(
            pin_file(&File::open(&exe).unwrap()).unwrap(),
            Kind::Detached,
        );
        c.args(["/d", "/c", "exit 0"]).keep_program();
        let e = c.spawn_detached().unwrap_err();
        assert!(e.to_string().contains("not held"), "{e}");
    }

    #[link(name = "shell32.dll", kind = "raw-dylib", modifiers = "+verbatim")]
    extern "system" {
        fn CommandLineToArgvW(line: *const u16, n: *mut i32) -> *mut *mut u16;
    }

    /// What Windows' own parser (`CommandLineToArgvW`) reads line, ending in a NUL, as.
    fn parsed(line: &[u16]) -> Vec<OsString> {
        let mut n = 0;
        let v = unsafe { CommandLineToArgvW(line.as_ptr(), &mut n) };
        assert!(!v.is_null());
        let out = (0..n as usize)
            .map(|i| {
                let p = unsafe { *v.add(i) };
                let len = (0..).take_while(|j| unsafe { *p.add(*j) } != 0).count();
                OsString::from_wide(unsafe { std::slice::from_raw_parts(p, len) })
            })
            .collect();
        unsafe { LocalFree(v as _) };
        out
    }

    fn line(name: &str, args: &[&str]) -> std::io::Result<Vec<u16>> {
        let args: Vec<OsString> = args.iter().map(OsString::from).collect();
        command_line(name.as_ref(), &args)
    }

    /// Quoted by the rules the C runtimes and `CommandLineToArgvW` read arguments by: each
    /// comes back as it was.
    #[test]
    fn command_line_quotes_each_argument_back_to_itself() {
        let cases: &[(&str, &[&str], &str)] = &[
            ("p", &[], r#""p""#),
            (
                r"C:\Program Files\x\p.exe",
                &["a", "b c"],
                r#""C:\Program Files\x\p.exe" a "b c""#,
            ),
            ("p", &[""], r#""p" """#),
            ("p", &[r#"a"b"#], r#""p" a\"b"#),
            ("p", &[r"a\b"], r#""p" a\b"#),
            ("p", &[r#"a\\"b"#], r#""p" a\\\\\"b"#),
            ("p", &[r"dir\"], r#""p" dir\"#),
            ("p", &[r"my dir\"], r#""p" "my dir\\""#),
            ("p", &[r"a\\ b\\"], r#""p" "a\\ b\\\\""#),
            ("p", &[r#"say "hi""#], r#""p" "say \"hi\"""#),
            ("p", &["a\tb"], "\"p\" \"a\tb\""),
        ];
        for (name, args, want) in cases {
            let got = line(name, args).unwrap();
            assert_eq!(got.last(), Some(&0));
            assert_eq!(
                String::from_utf16(&got[..got.len() - 1]).unwrap(),
                *want,
                "{name} {args:?}"
            );
        }
        let more: &[(&str, &[&str])] = &[
            (r"C:\dir\", &["x"]),
            (
                "p",
                &[
                    "\\",
                    "\\\\",
                    "\"",
                    "\"\"",
                    " ",
                    "\\\"",
                    "\\\\\"",
                    "a b\\\\\\",
                ],
            ),
            ("p", &["", "", "x y", "-flag=\"v w\"", "ünï cødé"]),
        ];
        for (name, args) in cases.iter().map(|c| (c.0, c.1)).chain(more.iter().copied()) {
            let want: Vec<OsString> = std::iter::once(name)
                .chain(args.iter().copied())
                .map(OsString::from)
                .collect();
            assert_eq!(parsed(&line(name, args).unwrap()), want, "{name} {args:?}");
        }
    }

    #[test]
    fn command_line_refuses_what_it_cannot_pass() {
        assert!(
            line(r#"a"b.exe"#, &[]).is_err(),
            "a quote in the program's name"
        );
        assert!(
            line("a\0b.exe", &[]).is_err(),
            "a NUL in the program's name"
        );
        assert!(line("p", &["a\0b"]).is_err(), "a NUL in an argument");
    }

    fn env(vars: &[(&str, &str)]) -> std::io::Result<Vec<u16>> {
        let vars: Vec<(OsString, OsString)> =
            vars.iter().map(|(k, v)| (k.into(), v.into())).collect();
        environment_block(&vars)
    }

    /// The block CreateProcessW takes: each `NAME=value` and a NUL, then a NUL.
    fn block(entries: &[&str]) -> Vec<u16> {
        let mut out = Vec::new();
        for e in entries {
            out.extend(std::ffi::OsStr::new(e).encode_wide());
            out.push(0);
        }
        if out.is_empty() {
            out.push(0);
        }
        out.push(0);
        out
    }

    /// Sorted by name ignoring case, as Windows compares them (in upper case: `A` before `_`),
    /// each name once, the last value given winning.
    #[test]
    fn environment_block_is_sorted_with_each_name_once() {
        assert_eq!(env(&[]).unwrap(), block(&[]));
        assert_eq!(
            env(&[("b", "1"), ("A", "2"), ("c", "3")]).unwrap(),
            block(&["A=2", "b=1", "c=3"])
        );
        assert_eq!(
            env(&[("_X", "1"), ("a", "2")]).unwrap(),
            block(&["a=2", "_X=1"])
        );
        let one = env(&[("Path", "1"), ("PATH", "2")]).unwrap();
        let text = String::from_utf16(&one).unwrap();
        let entries: Vec<&str> = text.split('\0').filter(|e| !e.is_empty()).collect();
        assert_eq!(entries.len(), 1, "{entries:?}");
        assert!(entries[0].eq_ignore_ascii_case("PATH=2"), "{entries:?}");
        // A drive's current folder (`=C:=C:\x`), as Windows keeps them.
        assert_eq!(env(&[("=C:", r"C:\x")]).unwrap(), block(&[r"=C:=C:\x"]));
    }

    #[test]
    fn environment_block_refuses_what_it_cannot_hold() {
        for (k, v) in [("A=B", "1"), ("", "1"), ("A\0", "1"), ("A", "x\0y")] {
            assert!(env(&[(k, v)]).is_err(), "{k:?}={v:?}");
        }
    }

    /// Whether a detached launch can leave the job the tests run in, as the app would see it.
    #[test]
    fn breakaway_says_whether_a_detached_launch_can_leave_the_apps_job() {
        let mut in_job = 0;
        assert_ne!(
            unsafe { IsProcessInJob(GetCurrentProcess(), std::ptr::null_mut(), &mut in_job) },
            0
        );
        let r = breakaway();
        eprintln!("in a job: {}; breakaway: {r:?}", in_job != 0);
        match &r {
            Ok(f) => assert!(*f == 0 || *f == CREATE_BREAKAWAY_FROM_JOB, "{f:#x}"),
            Err(e) => assert_eq!(e, NO_BREAKAWAY),
        }
        if in_job == 0 {
            assert_eq!(r, Ok(0));
        }
    }

    /// Run from a process outside every job that refuses breakaway (a hosted runner's steps run
    /// in one that does).
    #[test]
    #[ignore = "needs a process outside every job that refuses breakaway; CI starts it so"]
    fn outside_job_breakaway() {
        let r = breakaway();
        assert!(
            matches!(r, Ok(0) | Ok(CREATE_BREAKAWAY_FROM_JOB)),
            "a detached launch could not leave the app's job: {r:?}"
        );
    }
}
