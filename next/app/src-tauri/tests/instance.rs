//! The single-instance guard across real processes (`src/instance.rs`): one test process holds a
//! profile, this one launches against it, kills the holder with SIGKILL and recovers the stale
//! lock. The window itself is the native test's (`native/instances-session.ts`).

#![cfg(unix)]

use pluxy_app::instance::{start, Start};
use std::io::{BufRead, BufReader};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::time::Duration;

struct Temp(PathBuf);

impl Drop for Temp {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn temp() -> Temp {
    let d = std::env::temp_dir().join(format!(
        "pluxy-app-inst-{}",
        &pluxy_app::managed::random_key()[..12]
    ));
    std::fs::create_dir_all(&d).unwrap();
    Temp(d)
}

/// The holder: this same test binary, run on the test below with `PLUXY_TEST_HOLD` set. It takes
/// the profile, starts a child that outlives it (as a managed core would), says so, and stays
/// until it is killed. Without the variable this is no test at all.
#[test]
fn holder() {
    let Ok(dir) = std::env::var("PLUXY_TEST_HOLD") else {
        return;
    };
    let Start::First(held) = start(Path::new(&dir), Duration::from_secs(1)).unwrap() else {
        panic!("the holder is not the first");
    };
    held.listen(&[Path::new(&dir).parent().unwrap().join("run")], || {
        println!("activated");
        true
    })
    .unwrap();
    // A child, as the app's core is: it must not keep the lock when the holder dies. The test
    // that started this holder kills it.
    #[allow(clippy::zombie_processes)]
    let child = Command::new("sleep").arg("120").spawn().unwrap();
    println!("held {}", child.id());
    std::thread::sleep(Duration::from_secs(120));
}

/// A holder process, killed and waited for when this is dropped, also when the test fails.
struct Holder(Child);

impl Drop for Holder {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

fn spawn_holder(dir: &Path) -> (Holder, BufReader<std::process::ChildStdout>, u32) {
    let mut holder = Holder(
        Command::new(std::env::current_exe().unwrap())
            .args(["holder", "--exact", "--nocapture", "--test-threads=1"])
            .env("PLUXY_TEST_HOLD", dir)
            .stdout(Stdio::piped())
            .spawn()
            .unwrap(),
    );
    let mut out = BufReader::new(holder.0.stdout.take().unwrap());
    loop {
        let mut line = String::new();
        assert!(out.read_line(&mut line).unwrap() > 0, "the holder ended");
        // The harness starts the line ("test holder ... ") that the holder's own output ends.
        if let Some((_, pid)) = line.trim().rsplit_once("held ") {
            let pid = pid.parse().unwrap();
            return (holder, out, pid);
        }
    }
}

fn alive(pid: u32) -> bool {
    unsafe { libc::kill(pid as libc::pid_t, 0) == 0 }
}

#[test]
fn a_launch_raises_the_holder_and_a_killed_holders_lock_is_recovered() {
    let t = temp();
    let dir = t.0.join("instance");
    let (mut holder, mut out, sleeper) = spawn_holder(&dir);

    // Two actual processes: this launch is refused the lock and the holder is told to raise.
    assert!(matches!(
        start(&dir, Duration::from_secs(5)).unwrap(),
        Start::Raised
    ));
    let mut line = String::new();
    out.read_line(&mut line).unwrap();
    assert_eq!(line.trim(), "activated");

    // The holder dies without cleaning up (SIGKILL); its child lives on.
    holder.0.kill().unwrap();
    holder.0.wait().unwrap();
    assert!(alive(sleeper), "the holder's child should outlive it");
    let socket = std::fs::read(dir.join("address")).unwrap();
    let socket = Path::new(std::str::from_utf8(&socket).unwrap());
    assert!(socket.exists(), "a stale socket stays");

    // The next launch takes over at once, though the child still runs: it never held the lock.
    let Start::First(taken) = start(&dir, Duration::from_secs(1)).unwrap() else {
        panic!("a dead holder's lock was not recovered");
    };
    taken.listen(&[t.0.join("run")], || true).unwrap();
    assert!(matches!(
        start(&dir, Duration::from_secs(5)).unwrap(),
        Start::Raised
    ));
    unsafe { libc::kill(sleeper as libc::pid_t, libc::SIGKILL) };
}
