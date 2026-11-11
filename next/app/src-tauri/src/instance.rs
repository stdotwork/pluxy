//! One app per profile. The profile is the app's data folder (`app_data_dir`, which holds the
//! managed core's folder too), not the program's place on disk: two copies or versions of the app
//! on one profile would run two cores on one core folder, and the same program on two profiles
//! (other `HOME` or `XDG_*` folders) is two separate apps.
//!
//! The first app takes an exclusive lock on `<data folder>/instance/lock` (`flock`, which the
//! kernel drops when the holder ends, however it ends, so a killed app leaves nothing to clean
//! up) and listens on `<data folder>/instance/activate.sock`. A later launch that cannot take the
//! lock sends the socket the one message `pluxy-activate 1`; the first app shows its window (a new
//! one if it was closed to the tray) and answers `ok`, and the later launch exits. The folder is
//! made readable by its user only, the socket accepts only a connection from the same user, reads
//! at most [`MAX_MESSAGE`] bytes for at most [`READ_BOUND`], and ignores anything but that
//! message. A socket left by a killed app is removed only by the app that holds the lock.
//!
//! This runs first in the app's setup, before the managed core, the tray or any webview.
//!
//! shortcut: the socket is Unix-only; where there is none (Windows) a second launch fails with
//! an error and does not raise the first's window. Upgrade with a named pipe there.

use std::fs::{self, File, OpenOptions, TryLockError};
use std::io;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

const LOCK: &str = "lock";
const SOCKET: &str = "activate.sock";
/// What a launch sends to raise the first app, and nothing else is acted on.
const MESSAGE: &[u8] = b"pluxy-activate 1\n";
const OK: &[u8] = b"ok\n";
/// Most bytes the first app reads from one connection.
const MAX_MESSAGE: usize = 64;
/// Longest the first app reads from one connection, in all.
const READ_BOUND: Duration = Duration::from_secs(2);
/// A socket path longer than this does not fit `sockaddr_un` everywhere.
const MAX_SOCKET_PATH: usize = 100;
/// How long a later launch waits for the first app to take the activation: the first may still be
/// starting its core, and answers once its window is shown.
pub const WAIT: Duration = Duration::from_secs(20);

pub enum Start {
    /// This app is the first for the profile and holds its lock.
    First(Instance),
    /// Another app holds the profile; it showed its window and this launch is done.
    Raised,
}

/// The first app's hold on its profile, until it is dropped or the process ends.
pub struct Instance {
    _lock: File,
    socket: PathBuf,
    stop: Arc<AtomicBool>,
}

/// Takes the profile's lock, or has the app that holds it raise its window. `dir` is a folder of
/// this profile's own (made, readable by its user only). When the lock is held but the holder
/// does not answer within `wait` (it may be starting, or closing), this is an error.
pub fn start(dir: &Path, wait: Duration) -> Result<Start, String> {
    prepare(dir).map_err(|e| format!("cannot use the instance folder {}: {e}", dir.display()))?;
    let lock = open_lock(&dir.join(LOCK))
        .map_err(|e| format!("cannot open the instance lock in {}: {e}", dir.display()))?;
    let socket = dir.join(SOCKET);
    let deadline = Instant::now() + wait;
    loop {
        match lock.try_lock() {
            Ok(()) => {
                // Holding the lock, so a socket still here is a dead app's.
                remove_stale(&socket)
                    .map_err(|e| format!("cannot remove the stale {}: {e}", socket.display()))?;
                return Ok(Start::First(Instance {
                    _lock: lock,
                    socket,
                    stop: Arc::new(AtomicBool::new(false)),
                }));
            }
            Err(TryLockError::WouldBlock) => {
                let left = deadline.saturating_duration_since(Instant::now());
                match ipc::activate(&socket, left) {
                    Ok(()) => return Ok(Start::Raised),
                    Err(e) if e.kind() == io::ErrorKind::Unsupported => {
                        return Err("pluxy is already running for this profile, and this platform cannot raise its window yet".into());
                    }
                    // It answered, and could not show its window: asking again would not help.
                    Err(e) if e.kind() == io::ErrorKind::InvalidData => {
                        return Err(format!("pluxy is already running for this profile: {e}"));
                    }
                    Err(e) if left.is_zero() => {
                        return Err(format!(
                            "pluxy is already running for this profile but did not answer: {e}"
                        ));
                    }
                    // The holder may not be listening yet, or may be closing (its lock goes after
                    // its socket): try the lock again too.
                    Err(_) => std::thread::sleep(Duration::from_millis(100)),
                }
            }
            Err(TryLockError::Error(e)) => {
                return Err(format!("cannot take the instance lock: {e}"));
            }
        }
    }
}

impl Instance {
    /// Listens for later launches; `on_activate` shows the window and says whether it did.
    /// Connections are served one at a time, each bounded.
    pub fn listen(&self, on_activate: impl Fn() -> bool + Send + 'static) -> io::Result<()> {
        ipc::listen(&self.socket, self.stop.clone(), on_activate)
    }

    /// Stops listening and removes the socket (the lock goes when the app ends). Safe to repeat.
    pub fn release(&self) {
        if !self.stop.swap(true, Ordering::SeqCst) {
            ipc::wake(&self.socket);
            let _ = fs::remove_file(&self.socket);
        }
    }
}

impl Drop for Instance {
    fn drop(&mut self) {
        self.release();
    }
}

#[cfg(unix)]
fn prepare(dir: &Path) -> io::Result<()> {
    use std::os::unix::fs::{DirBuilderExt, MetadataExt, PermissionsExt};
    fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(dir)?;
    // Not through a link, and ours: no other user chose what is in it.
    let meta = fs::symlink_metadata(dir)?;
    if !meta.is_dir() {
        return Err(io::Error::other("not a folder"));
    }
    if meta.uid() != unsafe { libc::geteuid() } {
        return Err(io::Error::other("it belongs to another user"));
    }
    if meta.mode() & 0o077 != 0 {
        fs::set_permissions(dir, fs::Permissions::from_mode(0o700))?;
    }
    Ok(())
}

#[cfg(not(unix))]
fn prepare(dir: &Path) -> io::Result<()> {
    fs::create_dir_all(dir)
}

fn open_lock(path: &Path) -> io::Result<File> {
    let mut o = OpenOptions::new();
    o.read(true).write(true).create(true).truncate(false);
    #[cfg(unix)]
    std::os::unix::fs::OpenOptionsExt::mode(&mut o, 0o600);
    o.open(path)
}

#[cfg(unix)]
fn remove_stale(socket: &Path) -> io::Result<()> {
    use std::os::unix::fs::FileTypeExt;
    match fs::symlink_metadata(socket) {
        Ok(m) if m.file_type().is_socket() => fs::remove_file(socket),
        // Anything else is not ours to remove; binding then says so.
        Ok(_) | Err(_) => Ok(()),
    }
}

#[cfg(not(unix))]
fn remove_stale(_: &Path) -> io::Result<()> {
    Ok(())
}

#[cfg(unix)]
mod ipc {
    use super::{MAX_MESSAGE, MAX_SOCKET_PATH, MESSAGE, OK, READ_BOUND};
    use std::io::{self, Read, Write};
    use std::os::fd::AsRawFd;
    use std::os::unix::fs::PermissionsExt;
    use std::os::unix::net::{UnixListener, UnixStream};
    use std::path::Path;
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::Arc;
    use std::time::{Duration, Instant};

    /// The user a connection comes from, as the kernel says.
    #[cfg(any(target_os = "linux", target_os = "android"))]
    fn peer_uid(s: &UnixStream) -> io::Result<u32> {
        let mut cred: libc::ucred = unsafe { std::mem::zeroed() };
        let mut len = std::mem::size_of::<libc::ucred>() as libc::socklen_t;
        let r = unsafe {
            libc::getsockopt(
                s.as_raw_fd(),
                libc::SOL_SOCKET,
                libc::SO_PEERCRED,
                &mut cred as *mut libc::ucred as *mut libc::c_void,
                &mut len,
            )
        };
        if r != 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(cred.uid)
    }

    #[cfg(not(any(target_os = "linux", target_os = "android")))]
    fn peer_uid(s: &UnixStream) -> io::Result<u32> {
        let (mut uid, mut gid) = (0, 0);
        if unsafe { libc::getpeereid(s.as_raw_fd(), &mut uid, &mut gid) } != 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(uid)
    }

    fn check_length(socket: &Path) -> io::Result<()> {
        if socket.as_os_str().len() > MAX_SOCKET_PATH {
            return Err(io::Error::other(format!(
                "the socket path {} is too long",
                socket.display()
            )));
        }
        Ok(())
    }

    pub fn listen(
        socket: &Path,
        stop: Arc<AtomicBool>,
        on_activate: impl Fn() -> bool + Send + 'static,
    ) -> io::Result<()> {
        check_length(socket)?;
        let listener = UnixListener::bind(socket)?;
        std::fs::set_permissions(socket, std::fs::Permissions::from_mode(0o600))?;
        std::thread::Builder::new()
            .name("instance-socket".into())
            .spawn(move || {
                for conn in listener.incoming() {
                    if stop.load(Ordering::SeqCst) {
                        break;
                    }
                    match conn {
                        Ok(mut c) => {
                            let _ = serve(&mut c, &on_activate);
                        }
                        Err(_) => std::thread::sleep(Duration::from_millis(50)),
                    }
                }
            })?;
        Ok(())
    }

    /// One connection: from this user, and the exact message within the bounds, or nothing.
    fn serve(c: &mut UnixStream, on_activate: &impl Fn() -> bool) -> io::Result<()> {
        if peer_uid(c)? != unsafe { libc::geteuid() } {
            return Ok(());
        }
        let deadline = Instant::now() + READ_BOUND;
        let mut buf = [0u8; MAX_MESSAGE];
        let mut n = 0;
        loop {
            let left = deadline.saturating_duration_since(Instant::now());
            if left.is_zero() {
                return Ok(());
            }
            c.set_read_timeout(Some(left))?;
            match c.read(&mut buf[n..]) {
                Ok(0) => return Ok(()),
                Ok(k) => n += k,
                Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
                Err(_) => return Ok(()),
            }
            if buf[..n].contains(&b'\n') || n == MAX_MESSAGE {
                break;
            }
        }
        if &buf[..n] != MESSAGE {
            return Ok(());
        }
        let ok = on_activate();
        c.set_write_timeout(Some(READ_BOUND))?;
        c.write_all(if ok { OK } else { b"no\n" })
    }

    /// Asks the app on the socket to raise its window; Ok once it has answered `ok`, within
    /// `bound`.
    pub fn activate(socket: &Path, bound: Duration) -> io::Result<()> {
        check_length(socket)?;
        let deadline = Instant::now() + bound;
        let mut s = UnixStream::connect(socket)?;
        s.set_write_timeout(Some(Duration::from_secs(2)))?;
        s.write_all(MESSAGE)?;
        let mut reply = [0u8; 3];
        let mut got = 0;
        while got < reply.len() {
            let left = deadline.saturating_duration_since(Instant::now());
            if left.is_zero() {
                return Err(io::ErrorKind::TimedOut.into());
            }
            s.set_read_timeout(Some(left))?;
            match s.read(&mut reply[got..]) {
                Ok(0) => return Err(io::ErrorKind::UnexpectedEof.into()),
                Ok(k) => got += k,
                Err(e) if e.kind() == io::ErrorKind::Interrupted => {}
                Err(e) => return Err(e),
            }
        }
        if reply == OK {
            Ok(())
        } else {
            Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "its window could not be shown",
            ))
        }
    }

    /// Makes the listener's thread look at its stop flag.
    pub fn wake(socket: &Path) {
        let _ = UnixStream::connect(socket);
    }
}

#[cfg(not(unix))]
mod ipc {
    use std::io;
    use std::path::Path;
    use std::sync::atomic::AtomicBool;
    use std::sync::Arc;
    use std::time::Duration;

    pub fn listen(
        _: &Path,
        _: Arc<AtomicBool>,
        _: impl Fn() -> bool + Send + 'static,
    ) -> io::Result<()> {
        Ok(())
    }

    pub fn activate(_: &Path, _: Duration) -> io::Result<()> {
        Err(io::ErrorKind::Unsupported.into())
    }

    pub fn wake(_: &Path) {}
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;
    use std::io::{Read, Write};
    use std::os::unix::fs::PermissionsExt;
    use std::os::unix::net::UnixStream;
    use std::sync::atomic::AtomicUsize;

    struct Temp(PathBuf);
    impl Drop for Temp {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }
    fn temp() -> Temp {
        let d = std::env::temp_dir().join(format!(
            "pluxy-app-inst-{}",
            &crate::managed::random_key()[..12]
        ));
        fs::create_dir_all(&d).unwrap();
        Temp(d)
    }

    fn first(dir: &Path) -> Instance {
        match start(dir, Duration::from_secs(1)).unwrap() {
            Start::First(i) => i,
            Start::Raised => panic!("expected to be the first"),
        }
    }

    /// An instance that counts the activations it gets.
    fn counting(dir: &Path) -> (Instance, Arc<AtomicUsize>) {
        let i = first(dir);
        let n = Arc::new(AtomicUsize::new(0));
        let c = n.clone();
        i.listen(move || {
            c.fetch_add(1, Ordering::SeqCst);
            true
        })
        .unwrap();
        (i, n)
    }

    #[test]
    fn the_second_launch_is_refused_the_lock_and_raises_the_first() {
        let t = temp();
        let dir = t.0.join("instance");
        let (_first, raised) = counting(&dir);
        assert!(matches!(
            start(&dir, Duration::from_secs(5)).unwrap(),
            Start::Raised
        ));
        assert_eq!(raised.load(Ordering::SeqCst), 1);
        assert!(matches!(
            start(&dir, Duration::from_secs(5)).unwrap(),
            Start::Raised
        ));
        assert_eq!(raised.load(Ordering::SeqCst), 2);
    }

    #[test]
    fn another_profile_is_its_own_instance() {
        let t = temp();
        let (_a, na) = counting(&t.0.join("a"));
        let (_b, nb) = counting(&t.0.join("b"));
        assert!(matches!(
            start(&t.0.join("a"), Duration::from_secs(5)).unwrap(),
            Start::Raised
        ));
        assert_eq!(
            (na.load(Ordering::SeqCst), nb.load(Ordering::SeqCst)),
            (1, 0)
        );
    }

    #[test]
    fn a_holder_that_does_not_answer_is_an_error_not_a_second_instance() {
        let t = temp();
        let dir = t.0.join("instance");
        let _held = first(&dir); // holds the lock, never listens
        let e = start(&dir, Duration::from_millis(400)).err().unwrap();
        assert!(e.contains("did not answer"), "{e}");
    }

    #[test]
    fn the_lock_is_free_again_when_the_holder_is_dropped() {
        let t = temp();
        let dir = t.0.join("instance");
        let (i, _) = counting(&dir);
        drop(i);
        assert!(!dir.join(SOCKET).exists(), "the socket goes with its owner");
        let _again = first(&dir);
    }

    #[test]
    fn a_stale_socket_is_removed_under_the_lock_and_anything_else_is_left() {
        let t = temp();
        let dir = t.0.join("instance");
        prepare(&dir).unwrap();
        // A socket nobody listens on, as a killed app leaves.
        drop(std::os::unix::net::UnixListener::bind(dir.join(SOCKET)).unwrap());
        assert!(dir.join(SOCKET).exists());
        let (_i, n) = counting(&dir);
        assert!(matches!(
            start(&dir, Duration::from_secs(5)).unwrap(),
            Start::Raised
        ));
        assert_eq!(n.load(Ordering::SeqCst), 1);
        // A regular file in its place is not ours to remove.
        let other = t.0.join("other");
        prepare(&other).unwrap();
        fs::write(other.join(SOCKET), b"mine").unwrap();
        let i = first(&other);
        assert!(i.listen(|| true).is_err());
        assert_eq!(fs::read(other.join(SOCKET)).unwrap(), b"mine");
    }

    #[test]
    fn the_folder_lock_and_socket_are_for_the_user_only() {
        let t = temp();
        let dir = t.0.join("instance");
        // A folder that exists and is open to others is closed.
        fs::create_dir_all(&dir).unwrap();
        fs::set_permissions(&dir, fs::Permissions::from_mode(0o755)).unwrap();
        let (_i, _) = counting(&dir);
        let mode = |p: &Path| fs::metadata(p).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode(&dir), 0o700);
        assert_eq!(mode(&dir.join(LOCK)), 0o600);
        assert_eq!(mode(&dir.join(SOCKET)), 0o600);
    }

    #[test]
    fn a_link_in_place_of_the_folder_is_refused() {
        let t = temp();
        let real = t.0.join("real");
        fs::create_dir_all(&real).unwrap();
        let link = t.0.join("link");
        std::os::unix::fs::symlink(&real, &link).unwrap();
        let e = start(&link, Duration::from_millis(100)).err().unwrap();
        assert!(e.contains("not a folder"), "{e}");
    }

    /// What the first app does with bytes that are not the message: nothing, and it goes on
    /// serving.
    #[test]
    fn anything_but_the_message_is_ignored_and_bounded() {
        let t = temp();
        let dir = t.0.join("instance");
        let (_i, n) = counting(&dir);
        let sock = dir.join(SOCKET);
        let ignored = |bytes: &[u8]| {
            let mut s = UnixStream::connect(&sock).unwrap();
            s.set_read_timeout(Some(Duration::from_secs(5))).unwrap();
            let _ = s.write_all(bytes);
            let mut out = Vec::new();
            let _ = s.read_to_end(&mut out);
            assert!(out.is_empty(), "answered {bytes:?} with {out:?}");
        };
        ignored(b"pluxy-activate 2\n");
        ignored(b"pluxy-activate 1\nextra\n");
        ignored(b"\n");
        ignored(&[b'x'; 500]); // more than it reads, with no newline
        let mut long = MESSAGE.to_vec();
        long.extend_from_slice(&[b'y'; 200]);
        ignored(&long);
        assert_eq!(n.load(Ordering::SeqCst), 0);
        // Half a message, never finished: dropped after the read bound, not waited on for ever.
        let t0 = Instant::now();
        let mut s = UnixStream::connect(&sock).unwrap();
        s.write_all(&MESSAGE[..8]).unwrap();
        let mut out = Vec::new();
        let _ = s.read_to_end(&mut out);
        assert!(out.is_empty());
        assert!(t0.elapsed() < READ_BOUND + Duration::from_secs(2));
        assert_eq!(n.load(Ordering::SeqCst), 0);
        // And the real message still works.
        assert!(matches!(
            start(&dir, Duration::from_secs(5)).unwrap(),
            Start::Raised
        ));
        assert_eq!(n.load(Ordering::SeqCst), 1);
    }

    #[test]
    fn a_launch_is_told_when_the_window_could_not_be_shown() {
        let t = temp();
        let dir = t.0.join("instance");
        let i = first(&dir);
        i.listen(|| false).unwrap();
        let e = start(&dir, Duration::from_secs(2)).err().unwrap();
        assert!(e.contains("window could not be shown"), "{e}");
    }

    #[test]
    fn a_long_socket_path_is_an_error() {
        let t = temp();
        let dir = t.0.join("x".repeat(120));
        let i = first(&dir);
        assert!(i.listen(|| true).is_err());
    }
}
