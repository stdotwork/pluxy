//! The hand-over of a launch to a terminal's window on macOS: the execution program, started by
//! the app with the confirmed program, arguments, working folder and environment, offers them once
//! on a Unix socket in a private folder; the execution program started inside the terminal's
//! window takes them and runs the program. Nothing of the launch is written to a file or read by
//! a shell. Unix-wide, so it is tested on Linux too.

use serde_json::{json, Value};
use std::io::{Read, Write};
use std::os::unix::io::AsRawFd;
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::Path;
use std::time::{Duration, Instant};

/// The most a hand-over carries (the program's arguments and environment).
const MAX: u64 = 1024 * 1024;

/// A launch as handed over: argv (the program first), the working folder and the environment.
#[derive(Debug, PartialEq, Eq)]
pub struct Spec {
    pub argv: Vec<String>,
    pub cwd: String,
    pub env: Vec<(String, String)>,
}

impl Spec {
    fn to_json(&self) -> Value {
        json!({"argv": self.argv, "cwd": self.cwd, "env": self.env})
    }

    /// The hand-over's bytes, refused over `MAX` (what `receive` takes): checked before Terminal
    /// is asked to open anything, so a launch too large fails here and not in its window.
    pub fn encode(&self) -> Result<Vec<u8>, String> {
        let bytes = serde_json::to_vec(&self.to_json()).map_err(|e| e.to_string())?;
        if bytes.len() as u64 > MAX {
            return Err(format!(
                "the launch is too large to hand over to Terminal: its program, arguments, \
                 folder and environment come to {} bytes, over {MAX}",
                bytes.len()
            ));
        }
        Ok(bytes)
    }

    fn from_json(v: &Value) -> Option<Spec> {
        let strings = |v: &Value| -> Option<Vec<String>> {
            v.as_array()?
                .iter()
                .map(|s| s.as_str().map(String::from))
                .collect()
        };
        let argv = strings(&v["argv"]).filter(|a| !a.is_empty())?;
        let env = v["env"]
            .as_array()?
            .iter()
            .map(|p| Some((p[0].as_str()?.to_string(), p[1].as_str()?.to_string())))
            .collect::<Option<_>>()?;
        Some(Spec {
            argv,
            cwd: v["cwd"].as_str()?.to_string(),
            env,
        })
    }
}

/// The user ID of the process at the other end of s.
fn peer_uid(s: &UnixStream) -> std::io::Result<u32> {
    #[cfg(target_os = "macos")]
    {
        let (mut uid, mut gid) = (0, 0);
        // SAFETY: the descriptor is s's, open while it lives.
        if unsafe { libc::getpeereid(s.as_raw_fd(), &mut uid, &mut gid) } != 0 {
            return Err(std::io::Error::last_os_error());
        }
        Ok(uid)
    }
    #[cfg(target_os = "linux")]
    {
        let mut cred = libc::ucred {
            pid: 0,
            uid: 0,
            gid: 0,
        };
        let mut len = std::mem::size_of::<libc::ucred>() as libc::socklen_t;
        // SAFETY: cred is a ucred of len bytes.
        let r = unsafe {
            libc::getsockopt(
                s.as_raw_fd(),
                libc::SOL_SOCKET,
                libc::SO_PEERCRED,
                (&mut cred as *mut libc::ucred).cast(),
                &mut len,
            )
        };
        if r != 0 {
            return Err(std::io::Error::last_os_error());
        }
        Ok(cred.uid)
    }
}

/// Offers a hand-over's bytes (`Spec::encode`) once on listener, within wait: to the first
/// connection from this user (another user's is dropped and the wait goes on). Fails with "launch
/// expired" when no one takes it.
pub fn serve(listener: &UnixListener, bytes: &[u8], wait: Duration) -> Result<(), String> {
    listener.set_nonblocking(true).map_err(|e| e.to_string())?;
    let me = unsafe { libc::geteuid() };
    let end = Instant::now() + wait;
    loop {
        match listener.accept() {
            Ok((mut s, _)) => {
                if peer_uid(&s).ok() != Some(me) {
                    continue;
                }
                s.set_nonblocking(false).map_err(|e| e.to_string())?;
                s.write_all(bytes).map_err(|e| e.to_string())?;
                return s
                    .shutdown(std::net::Shutdown::Write)
                    .map_err(|e| e.to_string());
            }
            Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                if Instant::now() >= end {
                    return Err("launch expired: the terminal did not take it in time".into());
                }
                std::thread::sleep(Duration::from_millis(50));
            }
            Err(e) => return Err(e.to_string()),
        }
    }
}

/// Takes the launch offered on the socket at path.
pub fn receive(path: &Path) -> Result<Spec, String> {
    let s = UnixStream::connect(path)
        .map_err(|e| format!("launch expired or already taken ({e}); start it again from pluxy"))?;
    let mut bytes = Vec::new();
    s.take(MAX + 1)
        .read_to_end(&mut bytes)
        .map_err(|e| e.to_string())?;
    if bytes.len() as u64 > MAX {
        return Err("the launch is too large".into());
    }
    let v: Value = serde_json::from_slice(&bytes).map_err(|e| e.to_string())?;
    Spec::from_json(&v).ok_or_else(|| "the launch is not one pluxy makes".into())
}

/// The `.command` file Terminal runs: it removes itself, then runs the execution program at
/// helper in its `attach` mode on socket. Both paths are single-quoted for `/bin/sh`.
pub fn script(script: &Path, helper: &str, socket: &Path) -> String {
    let q = |s: &str| format!("'{}'", s.replace('\'', r"'\''"));
    format!(
        "#!/bin/sh\nrm -f -- {}\nexec {} attach {}\n",
        q(&script.to_string_lossy()),
        q(helper),
        q(&socket.to_string_lossy())
    )
}

/// The terminal's own environment names a handed-over launch keeps from the window it runs in:
/// what the terminal tells programs about itself, and PATH (an app on macOS is given launchd's
/// short PATH; the window's shell has the person's).
pub const TERMINAL_NAMES: &[&str] = &[
    "TERM",
    "COLORTERM",
    "TERM_PROGRAM",
    "TERM_PROGRAM_VERSION",
    "TERM_SESSION_ID",
    "TERMINFO",
    "LC_TERMINAL",
    "LC_TERMINAL_VERSION",
    "PATH",
];

/// The environment a handed-over launch runs with: spec's, with own's values for the
/// `TERMINAL_NAMES` own has.
pub fn environment(spec: &Spec, own: &[(String, String)]) -> Vec<(String, String)> {
    let terminal = |k: &str| TERMINAL_NAMES.contains(&k) && own.iter().any(|(o, _)| o == k);
    let mut env: Vec<(String, String)> = spec
        .env
        .iter()
        .filter(|(k, _)| !terminal(k))
        .cloned()
        .collect();
    env.extend(
        own.iter()
            .filter(|(k, _)| TERMINAL_NAMES.contains(&k.as_str()))
            .cloned(),
    );
    env
}

#[cfg(test)]
#[path = "handover_tests.rs"]
mod tests;
