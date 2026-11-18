//! The hand-over of a launch to a terminal's window on macOS: the execution program, started by
//! the app with the confirmed program, arguments, working folder and environment, and the program
//! itself as the app opened and checked it (a descriptor), offers them once on a Unix socket in a
//! private folder, the descriptor attached (`SCM_RIGHTS`); the execution program started inside
//! the terminal's window takes them and runs that file. Nothing of the launch is written to a
//! file or read by a shell. Unix-wide, so it is tested on Linux too.

use serde_json::{json, Value};
use std::fs::File;
use std::io::{Read, Write};
use std::os::unix::io::{AsRawFd, FromRawFd, RawFd};
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

/// Room for the control message of one descriptor, aligned for its header.
type Control = [u64; 8];

/// Writes bytes on s with the descriptor fd attached to their first part (`SCM_RIGHTS`).
fn send_with(s: &mut UnixStream, bytes: &[u8], fd: RawFd) -> std::io::Result<()> {
    let mut control: Control = [0; 8];
    // SAFETY: CMSG_SPACE and CMSG_LEN only compute sizes.
    let (space, len) = unsafe {
        (
            libc::CMSG_SPACE(std::mem::size_of::<RawFd>() as u32),
            libc::CMSG_LEN(std::mem::size_of::<RawFd>() as u32),
        )
    };
    assert!(space as usize <= std::mem::size_of::<Control>());
    let mut iov = libc::iovec {
        iov_base: bytes.as_ptr() as *mut libc::c_void,
        iov_len: bytes.len(),
    };
    // SAFETY: an all-zero msghdr is a valid empty one.
    let mut msg: libc::msghdr = unsafe { std::mem::zeroed() };
    msg.msg_iov = &mut iov;
    msg.msg_iovlen = 1;
    msg.msg_control = control.as_mut_ptr().cast();
    msg.msg_controllen = space as _;
    // SAFETY: msg's control buffer has room for one header and one descriptor (asserted above).
    unsafe {
        let c = libc::CMSG_FIRSTHDR(&msg);
        (*c).cmsg_level = libc::SOL_SOCKET;
        (*c).cmsg_type = libc::SCM_RIGHTS;
        (*c).cmsg_len = len as _;
        std::ptr::write_unaligned(libc::CMSG_DATA(c).cast::<RawFd>(), fd);
    }
    let sent = loop {
        // SAFETY: msg points at bytes and control, which outlive the call.
        let n = unsafe { libc::sendmsg(s.as_raw_fd(), &msg, 0) };
        if n >= 0 {
            break n as usize;
        }
        let e = std::io::Error::last_os_error();
        if e.kind() != std::io::ErrorKind::Interrupted {
            return Err(e);
        }
    };
    s.write_all(&bytes[sent..])
}

/// Reads the first part of what s carries into buf, and the descriptors attached to it, each
/// close-on-exec: how many bytes came, and the descriptors as files.
fn receive_with(s: &UnixStream, buf: &mut [u8]) -> std::io::Result<(usize, Vec<File>)> {
    let mut control: Control = [0; 8];
    let mut iov = libc::iovec {
        iov_base: buf.as_mut_ptr().cast(),
        iov_len: buf.len(),
    };
    // SAFETY: an all-zero msghdr is a valid empty one.
    let mut msg: libc::msghdr = unsafe { std::mem::zeroed() };
    msg.msg_iov = &mut iov;
    msg.msg_iovlen = 1;
    msg.msg_control = control.as_mut_ptr().cast();
    msg.msg_controllen = std::mem::size_of::<Control>() as _;
    #[cfg(target_os = "linux")]
    let flags = libc::MSG_CMSG_CLOEXEC;
    #[cfg(not(target_os = "linux"))]
    let flags = 0;
    let n = loop {
        // SAFETY: msg points at buf and control, which outlive the call.
        let n = unsafe { libc::recvmsg(s.as_raw_fd(), &mut msg, flags) };
        if n >= 0 {
            break n as usize;
        }
        let e = std::io::Error::last_os_error();
        if e.kind() != std::io::ErrorKind::Interrupted {
            return Err(e);
        }
    };
    let mut files = Vec::new();
    // SAFETY: the kernel filled control with well-formed messages, up to msg_controllen; each
    // descriptor in them is new to this process and owned here.
    unsafe {
        let mut c = libc::CMSG_FIRSTHDR(&msg);
        while !c.is_null() {
            if (*c).cmsg_level == libc::SOL_SOCKET && (*c).cmsg_type == libc::SCM_RIGHTS {
                let data = libc::CMSG_DATA(c).cast::<RawFd>();
                let count = ((*c).cmsg_len as usize - libc::CMSG_LEN(0) as usize)
                    / std::mem::size_of::<RawFd>();
                for i in 0..count {
                    let fd = std::ptr::read_unaligned(data.add(i));
                    // macOS has no MSG_CMSG_CLOEXEC; this program starts nothing in between.
                    #[cfg(not(target_os = "linux"))]
                    libc::fcntl(fd, libc::F_SETFD, libc::FD_CLOEXEC);
                    files.push(File::from_raw_fd(fd));
                }
            }
            c = libc::CMSG_NXTHDR(&msg, c);
        }
    }
    if msg.msg_flags & libc::MSG_CTRUNC != 0 {
        return Err(std::io::Error::other(
            "more descriptors came than a launch has",
        ));
    }
    Ok((n, files))
}

/// Offers a hand-over's bytes (`Spec::encode`) and the program to run, the file the app checked,
/// once on listener, within wait: to the first connection from this user (another user's is
/// dropped and the wait goes on). Fails with "launch expired" when no one takes it.
pub fn serve(
    listener: &UnixListener,
    bytes: &[u8],
    program: &File,
    wait: Duration,
) -> Result<(), String> {
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
                send_with(&mut s, bytes, program.as_raw_fd()).map_err(|e| e.to_string())?;
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

/// Takes the launch offered on the socket at path, and the program to run: the file the app
/// checked, close-on-exec.
pub fn receive(path: &Path) -> Result<(Spec, File), String> {
    let s = UnixStream::connect(path)
        .map_err(|e| format!("launch expired or already taken ({e}); start it again from pluxy"))?;
    let mut bytes = vec![0; 64 * 1024];
    let (n, mut files) = receive_with(&s, &mut bytes).map_err(|e| e.to_string())?;
    bytes.truncate(n);
    if n > 0 {
        (&s).take(MAX + 1 - n as u64)
            .read_to_end(&mut bytes)
            .map_err(|e| e.to_string())?;
    }
    if bytes.len() as u64 > MAX {
        return Err("the launch is too large".into());
    }
    let v: Value = serde_json::from_slice(&bytes).map_err(|e| e.to_string())?;
    let spec = Spec::from_json(&v).ok_or("the launch is not one pluxy makes")?;
    if files.len() != 1 {
        return Err(format!(
            "the launch came with {} programs, not the one the app checked",
            files.len()
        ));
    }
    Ok((spec, files.remove(0)))
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
