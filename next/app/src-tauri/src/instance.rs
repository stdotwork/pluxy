//! One app per profile. The profile is the app's data folder (`app_data_dir`, which holds the
//! managed core's folder too), not the program's place on disk: two copies or versions of the app
//! on one profile would run two cores on one core folder, and the same program on two profiles
//! (other `HOME` or `XDG_*` folders) is two separate apps.
//!
//! The first app takes an exclusive lock on `<data folder>/instance/lock` (`flock`, which the
//! kernel drops when the holder ends, however it ends, so a killed app leaves nothing to clean
//! up). The folder must be its user's own and not a link, and the lock is opened inside it,
//! not through a link, and used only if it is a plain file of this user's that only they can read
//! and write: a lock reached through a link planted while the folder was open to others could be
//! swapped for another file, and two apps would both hold "the" lock.
//!
//! The first app then listens on a socket in a short folder of its user's: `pluxy-app` in
//! `XDG_RUNTIME_DIR`, else `pluxy-app-<uid>` in the temporary folder. A data folder can be too
//! long for a socket path (about 100 bytes); the socket's name is a digest of the data folder, so
//! each profile has its own. No folder above the socket's may be a link or one another user could
//! rename things in (of a user other than this one or root, or open to others' writes and not
//! sticky, as `/tmp` is): one could swap the socket's folder for theirs after it was checked. The
//! socket's folder is used through a handle on it, except to bind, which takes a path.
//!
//! The first app writes the socket's path to `instance/address`, and a later launch that cannot
//! take the lock reads it there (so both need not agree on where the runtime folder is), opening
//! it without waiting (a FIFO there would otherwise wait for a writer), connects, checks the
//! socket is its own user's, and sends the one message `pluxy-activate 1`; the first app shows
//! its window (a new one if it was closed to the tray) and answers `ok`, and the later launch
//! exits, all within one deadline. If the first app could not listen, it leaves the address
//! empty, and a later launch says so at once.
//!
//! The socket accepts only connections from the same user, reads at most [`MAX_MESSAGE`] bytes
//! from each for at most [`READ_BOUND`], serves up to [`MAX_CONNECTIONS`] at once (one that says
//! nothing does not hold up the others), and ignores anything but that message. A later launch
//! connects without blocking and keeps to its deadline, also when the socket's queue is full; the
//! first app stops listening through a pipe of its own, not a connection. A socket left by a
//! killed app is removed only by the app that holds the lock.
//!
//! This runs first in the app's setup, before the managed core, the tray or any webview.
//!
//! shortcut: the socket is Unix-only; where there is none (Windows) a second launch fails with
//! an error and does not raise the first's window. Upgrade with a named pipe there.

use std::fs::File;
use std::io;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::OnceLock;
use std::time::{Duration, Instant};

const LOCK: &str = "lock";
/// Where the first app says its socket is.
const ADDRESS: &str = "address";
/// What a launch sends to raise the first app, and nothing else is acted on.
const MESSAGE: &[u8] = b"pluxy-activate 1\n";
const OK: &[u8] = b"ok\n";
/// Most bytes the first app reads from one connection.
const MAX_MESSAGE: usize = 64;
/// Longest the first app reads from one connection, in all.
const READ_BOUND: Duration = Duration::from_secs(2);
/// Connections the first app serves at once; more wait in the socket's queue.
const MAX_CONNECTIONS: usize = 32;
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
    #[cfg(unix)]
    dir: std::os::fd::OwnedFd,
    /// Names this profile's socket: a digest of its instance folder.
    key: String,
    listening: OnceLock<ipc::Listening>,
    released: AtomicBool,
}

/// The folders, in order, where the first app may put its socket: `pluxy-app` in
/// `XDG_RUNTIME_DIR` (when set), then `pluxy-app-<uid>` in the temporary folder. Links in those
/// are resolved here: [`Instance::listen`] refuses a folder with a link above it.
pub fn socket_dirs() -> Vec<PathBuf> {
    let resolved = |p: PathBuf| std::fs::canonicalize(&p).unwrap_or(p);
    let mut dirs = Vec::new();
    if let Some(r) = std::env::var_os("XDG_RUNTIME_DIR").map(PathBuf::from) {
        if r.is_absolute() {
            dirs.push(resolved(r).join("pluxy-app"));
        }
    }
    #[cfg(unix)]
    dirs.push(
        resolved(std::env::temp_dir()).join(format!("pluxy-app-{}", unsafe { libc::geteuid() })),
    );
    dirs
}

/// Takes the profile's lock, or has the app that holds it raise its window. `dir` is a folder of
/// this profile's own (made, readable by its user only). When the lock is held but the holder
/// does not answer within `wait` (it may be starting, or closing), this is an error.
pub fn start(dir: &Path, wait: Duration) -> Result<Start, String> {
    start_by(dir, Instant::now() + wait)
}

/// [`start`], where the holder of a held lock must have answered by `deadline`. After it, the
/// lock is still tried, but the holder is not asked again.
fn start_by(dir: &Path, deadline: Instant) -> Result<Start, String> {
    let d = fsx::open_dir(dir)
        .map_err(|e| format!("cannot use the instance folder {}: {e}", dir.display()))?;
    let lock = fsx::open_lock(&d)
        .map_err(|e| format!("cannot use the instance lock in {}: {e}", dir.display()))?;
    let key = {
        use sha2::{Digest, Sha256};
        let canonical = std::fs::canonicalize(dir).unwrap_or_else(|_| dir.to_path_buf());
        let digest = Sha256::digest(canonical.as_os_str().as_encoded_bytes());
        digest[..16].iter().map(|b| format!("{b:02x}")).collect()
    };
    loop {
        match lock.try_lock() {
            Ok(()) => {
                // Holding the lock: an address still here is a dead app's.
                fsx::remove(&d, ADDRESS)
                    .map_err(|e| format!("cannot remove the stale address: {e}"))?;
                return Ok(Start::First(Instance {
                    _lock: lock,
                    #[cfg(unix)]
                    dir: d,
                    key,
                    listening: OnceLock::new(),
                    released: AtomicBool::new(false),
                }));
            }
            Err(std::fs::TryLockError::WouldBlock) => {
                // One deadline for the whole launch: `activate` checks it after the address is
                // read and before each step, and it is checked again before another try.
                let tried = fsx::read_address(&d).and_then(|socket| match socket {
                    Some(socket) => ipc::activate(&socket, deadline),
                    None => Err(io::ErrorKind::Unsupported.into()),
                });
                match tried {
                    Ok(()) => return Ok(Start::Raised),
                    Err(e) if e.kind() == io::ErrorKind::Unsupported => {
                        return Err("pluxy is already running for this profile, and its window cannot be raised from here".into());
                    }
                    // It answered, and could not show its window: asking again would not help.
                    Err(e) if e.kind() == io::ErrorKind::InvalidData => {
                        return Err(format!("pluxy is already running for this profile: {e}"));
                    }
                    Err(e) => {
                        let left = deadline.saturating_duration_since(Instant::now());
                        if left.is_zero() {
                            return Err(format!(
                                "pluxy is already running for this profile but did not answer: {e}"
                            ));
                        }
                        // The holder may not be listening yet, its queue may be full, or it may
                        // be closing (its lock goes after its socket): try the lock again too.
                        std::thread::sleep(Duration::from_millis(100).min(left));
                    }
                }
            }
            Err(std::fs::TryLockError::Error(e)) => {
                return Err(format!("cannot take the instance lock: {e}"));
            }
        }
    }
}

impl Instance {
    /// Listens for later launches on a socket in the first of `dirs` that works (see
    /// [`socket_dirs`]); `on_activate` shows the window and says whether it did. If none works,
    /// later launches are told at once that the window cannot be raised.
    pub fn listen(
        &self,
        dirs: &[PathBuf],
        on_activate: impl Fn() -> bool + Send + 'static,
    ) -> io::Result<()> {
        if self.listening.get().is_some() {
            return Err(io::Error::other("already listening"));
        }
        let mut last = io::Error::other("no folder for the socket");
        for dir in dirs {
            let socket = dir.join(format!("{}.sock", self.key));
            if socket.as_os_str().len() > MAX_SOCKET_PATH {
                last =
                    io::Error::other(format!("the socket path {} is too long", socket.display()));
                continue;
            }
            match ipc::listen(dir, &socket) {
                Ok(listener) => {
                    #[cfg(unix)]
                    fsx::write_address(&self.dir, socket.as_os_str().as_encoded_bytes())?;
                    let listening = listener.serve(socket, on_activate)?;
                    let _ = self.listening.set(listening);
                    return Ok(());
                }
                Err(e) => last = io::Error::new(e.kind(), format!("{}: {e}", socket.display())),
            }
        }
        #[cfg(unix)]
        let _ = fsx::write_address(&self.dir, b"");
        Err(last)
    }

    /// The socket's path, once listening.
    pub fn socket(&self) -> Option<&Path> {
        self.listening.get().map(|l| l.socket.as_path())
    }

    /// Stops listening and removes the address and socket (the lock goes when the app ends).
    /// Safe to repeat.
    pub fn release(&self) {
        if self.released.swap(true, Ordering::SeqCst) {
            return;
        }
        #[cfg(unix)]
        let _ = fsx::remove(&self.dir, ADDRESS);
        if let Some(l) = self.listening.get() {
            l.stop();
        }
    }
}

impl Drop for Instance {
    fn drop(&mut self) {
        self.release();
    }
}

#[cfg(unix)]
mod fsx {
    //! The instance folder, through a handle on it: nothing in it is reached through a link.
    use std::ffi::CString;
    use std::fs::File;
    use std::io::{self, Read, Write};
    use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
    use std::os::unix::ffi::OsStrExt;
    use std::path::{Path, PathBuf};

    fn cstr(s: &[u8]) -> io::Result<CString> {
        CString::new(s).map_err(|_| io::ErrorKind::InvalidInput.into())
    }

    fn fstat(fd: i32) -> io::Result<libc::stat> {
        let mut st: libc::stat = unsafe { std::mem::zeroed() };
        if unsafe { libc::fstat(fd, &mut st) } != 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(st)
    }

    fn euid() -> libc::uid_t {
        unsafe { libc::geteuid() }
    }

    /// Makes `dir` (and any missing parent) for this user only, and opens it: not through a link,
    /// and only if it is this user's. Closes it to others if it was open.
    pub fn open_dir(dir: &Path) -> io::Result<OwnedFd> {
        use std::os::unix::fs::DirBuilderExt;
        std::fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(dir)?;
        let p = cstr(dir.as_os_str().as_bytes())?;
        let fd = unsafe {
            libc::open(
                p.as_ptr(),
                libc::O_RDONLY | libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC,
            )
        };
        if fd < 0 {
            let e = io::Error::last_os_error();
            return Err(match e.raw_os_error() {
                Some(libc::ELOOP) | Some(libc::ENOTDIR) => io::Error::other("not a folder"),
                _ => e,
            });
        }
        let fd = unsafe { OwnedFd::from_raw_fd(fd) };
        let st = fstat(fd.as_raw_fd())?;
        if st.st_mode & libc::S_IFMT != libc::S_IFDIR {
            return Err(io::Error::other("not a folder"));
        }
        if st.st_uid != euid() {
            return Err(io::Error::other("it belongs to another user"));
        }
        if st.st_mode & 0o077 != 0 && unsafe { libc::fchmod(fd.as_raw_fd(), 0o700) } != 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(fd)
    }

    /// Opens `name` in `dir`, not through a link, and checks it is a plain file of this user's
    /// that only they can read and write.
    fn open_in(dir: &OwnedFd, name: &str, flags: i32) -> io::Result<File> {
        let n = cstr(name.as_bytes())?;
        // Without O_NONBLOCK, opening a FIFO waits for a writer, before its type can be checked;
        // it is cleared again once the file is known to be a plain one.
        let fd = unsafe {
            libc::openat(
                dir.as_raw_fd(),
                n.as_ptr(),
                flags | libc::O_NOFOLLOW | libc::O_CLOEXEC | libc::O_NOCTTY | libc::O_NONBLOCK,
                0o600 as libc::c_uint,
            )
        };
        if fd < 0 {
            let e = io::Error::last_os_error();
            return Err(match e.raw_os_error() {
                Some(libc::ELOOP) => io::Error::other(format!("{name} is a link")),
                _ => e,
            });
        }
        let file = unsafe { File::from_raw_fd(fd) };
        let st = fstat(file.as_raw_fd())?;
        if st.st_mode & libc::S_IFMT != libc::S_IFREG {
            return Err(io::Error::other(format!("{name} is not a plain file")));
        }
        if st.st_uid != euid() {
            return Err(io::Error::other(format!("{name} belongs to another user")));
        }
        if st.st_mode & 0o777 != 0o600 {
            return Err(io::Error::other(format!(
                "{name} is mode {:o}, not 600: remove it and start again",
                st.st_mode & 0o777
            )));
        }
        let fl = unsafe { libc::fcntl(file.as_raw_fd(), libc::F_GETFL) };
        if fl < 0
            || unsafe { libc::fcntl(file.as_raw_fd(), libc::F_SETFL, fl & !libc::O_NONBLOCK) } < 0
        {
            return Err(io::Error::last_os_error());
        }
        Ok(file)
    }

    /// What `name` in `dir` is, not following a link.
    pub fn stat_in(dir: &OwnedFd, name: &str) -> io::Result<libc::stat> {
        let n = cstr(name.as_bytes())?;
        let mut st: libc::stat = unsafe { std::mem::zeroed() };
        let r = unsafe {
            libc::fstatat(
                dir.as_raw_fd(),
                n.as_ptr(),
                &mut st,
                libc::AT_SYMLINK_NOFOLLOW,
            )
        };
        if r != 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(st)
    }

    /// Sets the mode of `name` in `dir`.
    pub fn chmod_in(dir: &OwnedFd, name: &str, mode: libc::mode_t) -> io::Result<()> {
        let n = cstr(name.as_bytes())?;
        if unsafe { libc::fchmodat(dir.as_raw_fd(), n.as_ptr(), mode, 0) } != 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(())
    }

    pub fn open_lock(dir: &OwnedFd) -> io::Result<File> {
        open_in(dir, super::LOCK, libc::O_RDWR | libc::O_CREAT)
    }

    /// The socket the first app wrote down: None if it said it has none, an error if it has not
    /// written one (yet).
    pub fn read_address(dir: &OwnedFd) -> io::Result<Option<PathBuf>> {
        let mut f = open_in(dir, super::ADDRESS, libc::O_RDONLY)?;
        let mut b = Vec::new();
        (&mut f).take(4096).read_to_end(&mut b)?;
        if b.is_empty() {
            return Ok(None);
        }
        Ok(Some(PathBuf::from(std::ffi::OsStr::from_bytes(&b))))
    }

    /// Writes the address in one step: a later launch reads all of it or none.
    pub fn write_address(dir: &OwnedFd, socket: &[u8]) -> io::Result<()> {
        const NEW: &str = "address.new";
        remove(dir, NEW)?;
        let mut f = open_in(dir, NEW, libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL)?;
        f.write_all(socket)?;
        drop(f);
        let (from, to) = (cstr(NEW.as_bytes())?, cstr(super::ADDRESS.as_bytes())?);
        let d = dir.as_raw_fd();
        if unsafe { libc::renameat(d, from.as_ptr(), d, to.as_ptr()) } != 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(())
    }

    /// Removes `name` from `dir` (a link itself, not what it points to). Gone already is fine.
    pub fn remove(dir: &OwnedFd, name: &str) -> io::Result<()> {
        let n = cstr(name.as_bytes())?;
        if unsafe { libc::unlinkat(dir.as_raw_fd(), n.as_ptr(), 0) } != 0 {
            let e = io::Error::last_os_error();
            if e.kind() != io::ErrorKind::NotFound {
                return Err(e);
            }
        }
        Ok(())
    }
}

#[cfg(not(unix))]
mod fsx {
    use std::fs::{File, OpenOptions};
    use std::io;
    use std::path::{Path, PathBuf};

    pub struct Dir(PathBuf);

    pub fn open_dir(dir: &Path) -> io::Result<Dir> {
        std::fs::create_dir_all(dir)?;
        Ok(Dir(dir.to_path_buf()))
    }

    pub fn open_lock(dir: &Dir) -> io::Result<File> {
        OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .truncate(false)
            .open(dir.0.join(super::LOCK))
    }

    pub fn read_address(_: &Dir) -> io::Result<Option<PathBuf>> {
        Ok(None)
    }

    pub fn remove(_: &Dir, _: &str) -> io::Result<()> {
        Ok(())
    }
}

#[cfg(unix)]
mod ipc {
    use super::{MAX_CONNECTIONS, MAX_MESSAGE, MESSAGE, OK, READ_BOUND};
    use std::io::{self, Read, Write};
    use std::os::fd::{AsRawFd, FromRawFd, OwnedFd, RawFd};
    use std::os::unix::ffi::OsStrExt;
    use std::os::unix::net::{UnixListener, UnixStream};
    use std::path::{Path, PathBuf};
    use std::time::{Duration, Instant};

    /// Connections waiting to be accepted, beyond those being served.
    const BACKLOG: i32 = 16;

    fn euid() -> libc::uid_t {
        unsafe { libc::geteuid() }
    }

    fn cvt(r: i32) -> io::Result<i32> {
        if r < 0 {
            Err(io::Error::last_os_error())
        } else {
            Ok(r)
        }
    }

    /// The user at the other end of a connection, as the kernel says.
    #[cfg(any(target_os = "linux", target_os = "android"))]
    fn peer_uid(fd: RawFd) -> io::Result<u32> {
        let mut cred: libc::ucred = unsafe { std::mem::zeroed() };
        let mut len = std::mem::size_of::<libc::ucred>() as libc::socklen_t;
        cvt(unsafe {
            libc::getsockopt(
                fd,
                libc::SOL_SOCKET,
                libc::SO_PEERCRED,
                &mut cred as *mut libc::ucred as *mut libc::c_void,
                &mut len,
            )
        })?;
        Ok(cred.uid)
    }

    #[cfg(not(any(target_os = "linux", target_os = "android")))]
    fn peer_uid(fd: RawFd) -> io::Result<u32> {
        let (mut uid, mut gid) = (0, 0);
        cvt(unsafe { libc::getpeereid(fd, &mut uid, &mut gid) })?;
        Ok(uid)
    }

    fn address(path: &Path) -> io::Result<(libc::sockaddr_un, libc::socklen_t)> {
        let mut a: libc::sockaddr_un = unsafe { std::mem::zeroed() };
        a.sun_family = libc::AF_UNIX as libc::sa_family_t;
        let b = path.as_os_str().as_bytes();
        if b.len() >= a.sun_path.len() || b.contains(&0) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "the socket path is too long",
            ));
        }
        for (d, s) in a.sun_path.iter_mut().zip(b) {
            *d = *s as libc::c_char;
        }
        let len = std::mem::offset_of!(libc::sockaddr_un, sun_path) + b.len() + 1;
        Ok((a, len as libc::socklen_t))
    }

    /// A Unix stream socket, closed on exec and nonblocking.
    fn new_socket() -> io::Result<OwnedFd> {
        let fd = unsafe {
            OwnedFd::from_raw_fd(cvt(libc::socket(libc::AF_UNIX, libc::SOCK_STREAM, 0))?)
        };
        set_flags(fd.as_raw_fd())?;
        Ok(fd)
    }

    fn set_flags(fd: RawFd) -> io::Result<()> {
        cvt(unsafe { libc::fcntl(fd, libc::F_SETFD, libc::FD_CLOEXEC) })?;
        let fl = cvt(unsafe { libc::fcntl(fd, libc::F_GETFL) })?;
        cvt(unsafe { libc::fcntl(fd, libc::F_SETFL, fl | libc::O_NONBLOCK) })?;
        Ok(())
    }

    /// Waits until `fd` has `events`, or `deadline` passes.
    fn wait(fd: RawFd, events: i16, deadline: Instant) -> io::Result<()> {
        loop {
            let left = deadline.saturating_duration_since(Instant::now());
            if left.is_zero() {
                return Err(io::ErrorKind::TimedOut.into());
            }
            let mut p = libc::pollfd {
                fd,
                events,
                revents: 0,
            };
            match cvt(unsafe { libc::poll(&mut p, 1, millis(left)) }) {
                Ok(0) => {}
                Ok(_) => return Ok(()),
                Err(e) if e.kind() == io::ErrorKind::Interrupted => {}
                Err(e) => return Err(e),
            }
        }
    }

    fn millis(d: Duration) -> i32 {
        d.as_millis().saturating_add(1).min(i32::MAX as u128) as i32
    }

    /// Refuses `dir` if another user could replace it or any folder above it after it is
    /// checked: each must be a folder (not a link) of this user's or root's and, if others may
    /// write in it, sticky.
    fn check_parents(dir: &Path) -> io::Result<()> {
        use std::os::unix::fs::MetadataExt;
        for p in dir
            .ancestors()
            .skip(1)
            .filter(|p| !p.as_os_str().is_empty())
        {
            let m = std::fs::symlink_metadata(p)?;
            if !m.file_type().is_dir() {
                return Err(io::Error::other(format!("{} is not a folder", p.display())));
            }
            let open = m.mode() & 0o022 != 0 && m.mode() & 0o1000 == 0;
            if (m.uid() != euid() && m.uid() != 0) || open {
                return Err(io::Error::other(format!(
                    "{} can be changed by other users",
                    p.display()
                )));
            }
        }
        Ok(())
    }

    /// Makes `dir` for this user only (not through a link, and only if it is this user's) and
    /// binds `socket`, a name in it, removing a socket a dead app left there.
    pub fn listen(dir: &Path, socket: &Path) -> io::Result<Listener> {
        check_parents(dir)?;
        let d = super::fsx::open_dir(dir)?;
        let name = socket
            .file_name()
            .and_then(|n| n.to_str())
            .ok_or(io::ErrorKind::InvalidInput)?
            .to_owned();
        // The name is this profile's, and the caller holds its lock: a socket of this user's
        // there is a dead app's. Anything else is not ours to remove; binding then says so.
        if let Ok(st) = super::fsx::stat_in(&d, &name) {
            if st.st_mode & libc::S_IFMT == libc::S_IFSOCK && st.st_uid == euid() {
                super::fsx::remove(&d, &name)?;
            }
        }
        let (addr, len) = address(socket)?;
        let fd = new_socket()?;
        cvt(unsafe {
            libc::bind(
                fd.as_raw_fd(),
                &addr as *const libc::sockaddr_un as *const libc::sockaddr,
                len,
            )
        })?;
        super::fsx::chmod_in(&d, &name, 0o600)?;
        cvt(unsafe { libc::listen(fd.as_raw_fd(), BACKLOG) })?;
        Ok(Listener {
            l: UnixListener::from(fd),
            dir: d,
            name,
        })
    }

    pub struct Listener {
        l: UnixListener,
        dir: OwnedFd,
        name: String,
    }

    /// A socket being served, and the way to stop serving it.
    pub struct Listening {
        pub socket: PathBuf,
        dir: OwnedFd,
        name: String,
        wake: OwnedFd,
    }

    impl Listening {
        /// Stops the serving thread (through its pipe: nothing to wait on) and removes the
        /// socket from its folder.
        pub fn stop(&self) {
            let _ = super::fsx::remove(&self.dir, &self.name);
            unsafe {
                libc::write(
                    self.wake.as_raw_fd(),
                    b"x".as_ptr() as *const libc::c_void,
                    1,
                )
            };
        }
    }

    struct Conn {
        s: UnixStream,
        buf: [u8; MAX_MESSAGE],
        n: usize,
        deadline: Instant,
    }

    enum Got {
        More,
        Done,
        Message,
    }

    impl Conn {
        /// Reads what has come; whether to wait for more, drop it, or act on the message.
        fn read(&mut self) -> Got {
            loop {
                match self.s.read(&mut self.buf[self.n..]) {
                    Ok(0) => return Got::Done,
                    Ok(k) => self.n += k,
                    Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
                    Err(e) if e.kind() == io::ErrorKind::WouldBlock => return Got::More,
                    Err(_) => return Got::Done,
                }
                let got = &self.buf[..self.n];
                if got.contains(&b'\n') || self.n == MAX_MESSAGE {
                    return if got == MESSAGE {
                        Got::Message
                    } else {
                        Got::Done
                    };
                }
            }
        }
    }

    impl Listener {
        /// Serves the socket on a thread of its own until [`Listening::stop`].
        pub fn serve(
            self,
            socket: PathBuf,
            on_activate: impl Fn() -> bool + Send + 'static,
        ) -> io::Result<Listening> {
            let mut p = [0; 2];
            cvt(unsafe { libc::pipe(p.as_mut_ptr()) })?;
            let (r, w) = unsafe { (OwnedFd::from_raw_fd(p[0]), OwnedFd::from_raw_fd(p[1])) };
            set_flags(r.as_raw_fd())?;
            set_flags(w.as_raw_fd())?;
            let Listener { l, dir, name } = self;
            std::thread::Builder::new()
                .name("instance-socket".into())
                .spawn(move || Self::run(l, r, on_activate))?;
            Ok(Listening {
                socket,
                dir,
                name,
                wake: w,
            })
        }

        fn run(l: UnixListener, wake: OwnedFd, on_activate: impl Fn() -> bool) {
            let mut conns: Vec<Conn> = Vec::new();
            loop {
                let now = Instant::now();
                conns.retain(|c| c.deadline > now);
                let accepting = conns.len() < MAX_CONNECTIONS;
                let mut fds = vec![libc::pollfd {
                    fd: wake.as_raw_fd(),
                    events: libc::POLLIN,
                    revents: 0,
                }];
                if accepting {
                    fds.push(libc::pollfd {
                        fd: l.as_raw_fd(),
                        events: libc::POLLIN,
                        revents: 0,
                    });
                }
                let first = fds.len();
                fds.extend(conns.iter().map(|c| libc::pollfd {
                    fd: c.s.as_raw_fd(),
                    events: libc::POLLIN,
                    revents: 0,
                }));
                let timeout = conns
                    .iter()
                    .map(|c| millis(c.deadline.saturating_duration_since(now)))
                    .min()
                    .unwrap_or(-1);
                let r = unsafe { libc::poll(fds.as_mut_ptr(), fds.len() as libc::nfds_t, timeout) };
                if r < 0 {
                    if io::Error::last_os_error().kind() != io::ErrorKind::Interrupted {
                        std::thread::sleep(Duration::from_millis(50));
                    }
                    continue;
                }
                if fds[0].revents != 0 {
                    return;
                }
                let mut i = conns.len();
                while i > 0 {
                    i -= 1;
                    if fds[first + i].revents == 0 {
                        continue;
                    }
                    match conns[i].read() {
                        Got::More => {}
                        Got::Done => {
                            conns.swap_remove(i);
                        }
                        Got::Message => {
                            let c = conns.swap_remove(i);
                            let ok = on_activate();
                            let _ = reply(c.s, if ok { OK } else { b"no\n" });
                        }
                    }
                }
                if accepting && fds[1].revents != 0 {
                    while conns.len() < MAX_CONNECTIONS {
                        match l.accept() {
                            Ok((s, _)) => {
                                // Only this user's connections are read at all.
                                if peer_uid(s.as_raw_fd()).ok() == Some(euid())
                                    && s.set_nonblocking(true).is_ok()
                                {
                                    conns.push(Conn {
                                        s,
                                        buf: [0; MAX_MESSAGE],
                                        n: 0,
                                        deadline: Instant::now() + READ_BOUND,
                                    });
                                }
                            }
                            Err(e) if e.kind() == io::ErrorKind::Interrupted => {}
                            Err(_) => break,
                        }
                    }
                }
            }
        }
    }

    fn reply(mut s: UnixStream, what: &[u8]) -> io::Result<()> {
        s.set_nonblocking(false)?;
        s.set_write_timeout(Some(READ_BOUND))?;
        s.write_all(what)
    }

    /// Connects without blocking, within `deadline`. A full queue is `WouldBlock`, at once.
    fn connect(socket: &Path, deadline: Instant) -> io::Result<UnixStream> {
        let (addr, len) = address(socket)?;
        let fd = new_socket()?;
        let r = unsafe {
            libc::connect(
                fd.as_raw_fd(),
                &addr as *const libc::sockaddr_un as *const libc::sockaddr,
                len,
            )
        };
        if r != 0 {
            let e = io::Error::last_os_error();
            if e.raw_os_error() != Some(libc::EINPROGRESS) {
                return Err(e);
            }
            wait(fd.as_raw_fd(), libc::POLLOUT, deadline)?;
            let mut err: libc::c_int = 0;
            let mut l = std::mem::size_of::<libc::c_int>() as libc::socklen_t;
            cvt(unsafe {
                libc::getsockopt(
                    fd.as_raw_fd(),
                    libc::SOL_SOCKET,
                    libc::SO_ERROR,
                    &mut err as *mut libc::c_int as *mut libc::c_void,
                    &mut l,
                )
            })?;
            if err != 0 {
                return Err(io::Error::from_raw_os_error(err));
            }
        }
        Ok(UnixStream::from(fd))
    }

    /// TimedOut once `deadline` has passed.
    fn before(deadline: Instant) -> io::Result<()> {
        if Instant::now() >= deadline {
            return Err(io::ErrorKind::TimedOut.into());
        }
        Ok(())
    }

    /// Asks the app on the socket to raise its window; Ok once it has answered `ok`, all by
    /// `deadline`: nothing is started after it (connecting, each write and read), and an answer
    /// read after it is not taken.
    pub fn activate(socket: &Path, deadline: Instant) -> io::Result<()> {
        before(deadline)?;
        let mut s = connect(socket, deadline)?;
        // Only an app of this user's is told anything.
        if peer_uid(s.as_raw_fd())? != euid() {
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                "the socket is another user's",
            ));
        }
        let mut sent = 0;
        while sent < MESSAGE.len() {
            before(deadline)?;
            match s.write(&MESSAGE[sent..]) {
                Ok(k) => sent += k,
                Err(e) if e.kind() == io::ErrorKind::Interrupted => {}
                Err(e) if e.kind() == io::ErrorKind::WouldBlock => {
                    wait(s.as_raw_fd(), libc::POLLOUT, deadline)?
                }
                Err(e) => return Err(e),
            }
        }
        let mut reply = [0u8; 3];
        let mut got = 0;
        while got < reply.len() {
            before(deadline)?;
            match s.read(&mut reply[got..]) {
                Ok(0) => return Err(io::ErrorKind::UnexpectedEof.into()),
                Ok(k) => got += k,
                Err(e) if e.kind() == io::ErrorKind::Interrupted => {}
                Err(e) if e.kind() == io::ErrorKind::WouldBlock => {
                    wait(s.as_raw_fd(), libc::POLLIN, deadline)?
                }
                Err(e) => return Err(e),
            }
        }
        if reply == OK {
            before(deadline)
        } else {
            Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "its window could not be shown",
            ))
        }
    }
}

#[cfg(not(unix))]
mod ipc {
    use std::io;
    use std::path::{Path, PathBuf};
    use std::time::Instant;

    pub struct Listener;
    pub struct Listening {
        pub socket: PathBuf,
    }

    impl Listening {
        pub fn stop(&self) {}
    }

    impl Listener {
        pub fn serve(
            self,
            _: PathBuf,
            _: impl Fn() -> bool + Send + 'static,
        ) -> io::Result<Listening> {
            Err(io::ErrorKind::Unsupported.into())
        }
    }

    pub fn listen(_: &Path, _: &Path) -> io::Result<Listener> {
        Err(io::ErrorKind::Unsupported.into())
    }

    pub fn activate(_: &Path, _: Instant) -> io::Result<()> {
        Err(io::ErrorKind::Unsupported.into())
    }
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;
    use std::fs;
    use std::io::{Read, Write};
    use std::os::unix::fs::PermissionsExt;
    use std::os::unix::net::UnixStream;
    use std::sync::atomic::AtomicUsize;
    use std::sync::Arc;

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
        // The tests' socket folders are made in here: it is no one else's to change.
        fs::set_permissions(&d, fs::Permissions::from_mode(0o700)).unwrap();
        Temp(d)
    }

    impl Temp {
        /// The folder the tests' sockets go in, in place of the runtime folder.
        fn run(&self) -> Vec<PathBuf> {
            vec![self.0.join("run")]
        }
    }

    fn first(dir: &Path) -> Instance {
        match start(dir, Duration::from_secs(1)).unwrap() {
            Start::First(i) => i,
            Start::Raised => panic!("expected to be the first"),
        }
    }

    /// An instance that counts the activations it gets.
    fn counting(t: &Temp, dir: &Path) -> (Instance, Arc<AtomicUsize>) {
        let i = first(dir);
        let n = Arc::new(AtomicUsize::new(0));
        let c = n.clone();
        i.listen(&t.run(), move || {
            c.fetch_add(1, Ordering::SeqCst);
            true
        })
        .unwrap();
        (i, n)
    }

    fn socket_of(i: &Instance) -> PathBuf {
        i.socket().unwrap().to_path_buf()
    }

    #[test]
    fn the_second_launch_is_refused_the_lock_and_raises_the_first() {
        let t = temp();
        let dir = t.0.join("instance");
        let (_first, raised) = counting(&t, &dir);
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
        // Both put their sockets in one runtime folder, as two profiles of one user do.
        let (a, na) = counting(&t, &t.0.join("a"));
        let (b, nb) = counting(&t, &t.0.join("b"));
        assert_ne!(socket_of(&a), socket_of(&b));
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
        let (i, _) = counting(&t, &dir);
        let sock = socket_of(&i);
        assert!(dir.join(ADDRESS).exists());
        drop(i);
        assert!(!sock.exists(), "the socket goes with its owner");
        assert!(
            !dir.join(ADDRESS).exists(),
            "the address goes with its owner"
        );
        let _again = first(&dir);
    }

    #[test]
    fn a_stale_socket_is_removed_under_the_lock_and_anything_else_is_left() {
        let t = temp();
        let dir = t.0.join("instance");
        let (i, _) = counting(&t, &dir);
        let sock = socket_of(&i);
        drop(i);
        // A socket nobody listens on and its address, as a killed app leaves them.
        drop(std::os::unix::net::UnixListener::bind(&sock).unwrap());
        fs::write(dir.join(ADDRESS), sock.as_os_str().as_encoded_bytes()).unwrap();
        fs::set_permissions(dir.join(ADDRESS), fs::Permissions::from_mode(0o600)).unwrap();
        let (_i, n) = counting(&t, &dir);
        assert!(matches!(
            start(&dir, Duration::from_secs(5)).unwrap(),
            Start::Raised
        ));
        assert_eq!(n.load(Ordering::SeqCst), 1);
        // A regular file where another profile's socket goes is not ours to remove: that
        // profile's socket goes in the next folder.
        let p = t.0.join("probe");
        let name = {
            let i = first(&p);
            i.listen(&t.run(), || true).unwrap();
            socket_of(&i).file_name().unwrap().to_owned()
        };
        let run = t.0.join("run");
        fs::write(run.join(&name), b"mine").unwrap();
        let next = t.0.join("run2");
        let i = first(&p);
        i.listen(&[run.clone(), next.clone()], || true).unwrap();
        assert_eq!(fs::read(run.join(&name)).unwrap(), b"mine");
        assert_eq!(socket_of(&i), next.join(&name));
        assert!(matches!(
            start(&p, Duration::from_secs(5)).unwrap(),
            Start::Raised
        ));
    }

    #[test]
    fn the_folders_lock_address_and_socket_are_for_the_user_only() {
        let t = temp();
        let dir = t.0.join("instance");
        // A folder that exists and is open to others is closed.
        fs::create_dir_all(&dir).unwrap();
        fs::set_permissions(&dir, fs::Permissions::from_mode(0o755)).unwrap();
        let (i, _) = counting(&t, &dir);
        let mode = |p: &Path| fs::metadata(p).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode(&dir), 0o700);
        assert_eq!(mode(&dir.join(LOCK)), 0o600);
        assert_eq!(mode(&dir.join(ADDRESS)), 0o600);
        assert_eq!(mode(&t.0.join("run")), 0o700);
        assert_eq!(mode(&socket_of(&i)), 0o600);
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
        let (i, n) = counting(&t, &dir);
        let sock = socket_of(&i);
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
        i.listen(&t.run(), || false).unwrap();
        let e = start(&dir, Duration::from_secs(2)).err().unwrap();
        assert!(e.contains("window could not be shown"), "{e}");
    }

    #[test]
    fn a_long_profile_path_still_raises_the_first() {
        let t = temp();
        let dir = t.0.join("x".repeat(120)).join("instance");
        assert!(dir.as_os_str().len() > 120);
        let (_i, n) = counting(&t, &dir);
        assert!(matches!(
            start(&dir, Duration::from_secs(5)).unwrap(),
            Start::Raised
        ));
        assert_eq!(n.load(Ordering::SeqCst), 1);
    }

    /// When the first app has nowhere to listen, a later launch says so at once instead of
    /// waiting out its bound.
    #[test]
    fn a_first_app_that_cannot_listen_is_reported_at_once() {
        let t = temp();
        let dir = t.0.join("instance");
        let i = first(&dir);
        let not_a_folder = t.0.join("file");
        fs::write(&not_a_folder, b"").unwrap();
        assert!(i
            .listen(&[not_a_folder, t.0.join("y".repeat(120))], || true)
            .is_err());
        let t0 = Instant::now();
        let e = start(&dir, Duration::from_secs(10)).err().unwrap();
        assert!(e.contains("cannot be raised"), "{e}");
        assert!(t0.elapsed() < Duration::from_secs(2), "{:?}", t0.elapsed());
    }

    /// A `lock` that is a link (planted while the folder was open to others) is not followed:
    /// through it, two launches could lock two different files and both run.
    #[test]
    fn a_lock_that_is_a_link_or_open_to_others_is_refused() {
        let t = temp();
        let dir = t.0.join("instance");
        fs::create_dir_all(&dir).unwrap();
        fs::set_permissions(&dir, fs::Permissions::from_mode(0o700)).unwrap();
        let target = t.0.join("elsewhere");
        fs::write(&target, b"").unwrap();
        std::os::unix::fs::symlink(&target, dir.join(LOCK)).unwrap();
        let e = start(&dir, Duration::from_millis(100)).err();
        assert!(e.is_some(), "a launch ran through a planted lock link");
        // Swapped for another file, the link would have let a second launch run too.
        fs::remove_file(&target).unwrap();
        fs::write(&target, b"").unwrap();
        assert!(start(&dir, Duration::from_millis(100)).is_err());

        let other = t.0.join("other");
        fs::create_dir_all(&other).unwrap();
        fs::set_permissions(&other, fs::Permissions::from_mode(0o700)).unwrap();
        fs::write(other.join(LOCK), b"").unwrap();
        fs::set_permissions(other.join(LOCK), fs::Permissions::from_mode(0o644)).unwrap();
        assert!(start(&other, Duration::from_millis(100)).is_err());
        // The positive control on the same route: a lock of this user's, 0600, is used.
        fs::set_permissions(other.join(LOCK), fs::Permissions::from_mode(0o600)).unwrap();
        let _i = first(&other);
    }

    /// Connections that never send anything, as many as the listen queue holds: each is held
    /// open and silent.
    fn fill_queue(sock: &Path) -> Vec<std::os::fd::OwnedFd> {
        use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
        use std::os::unix::ffi::OsStrExt;
        let mut held = Vec::new();
        let mut addr: libc::sockaddr_un = unsafe { std::mem::zeroed() };
        addr.sun_family = libc::AF_UNIX as libc::sa_family_t;
        for (d, s) in addr.sun_path.iter_mut().zip(sock.as_os_str().as_bytes()) {
            *d = *s as libc::c_char;
        }
        for _ in 0..10_000 {
            let fd = unsafe { libc::socket(libc::AF_UNIX, libc::SOCK_STREAM, 0) };
            assert!(fd >= 0);
            let fd = unsafe { OwnedFd::from_raw_fd(fd) };
            unsafe { libc::fcntl(fd.as_raw_fd(), libc::F_SETFL, libc::O_NONBLOCK) };
            let r = unsafe {
                libc::connect(
                    fd.as_raw_fd(),
                    &addr as *const libc::sockaddr_un as *const libc::sockaddr,
                    std::mem::size_of::<libc::sockaddr_un>() as libc::socklen_t,
                )
            };
            if r != 0 {
                let e = io::Error::last_os_error();
                assert_eq!(e.kind(), io::ErrorKind::WouldBlock, "{e}");
                return held;
            }
            held.push(fd);
        }
        panic!("the queue never filled");
    }

    /// A listener that accepts nothing, with the smallest queue.
    fn deaf_listener(sock: &Path) -> std::os::unix::net::UnixListener {
        use std::os::fd::AsRawFd;
        let l = std::os::unix::net::UnixListener::bind(sock).unwrap();
        assert_eq!(unsafe { libc::listen(l.as_raw_fd(), 0) }, 0);
        l
    }

    /// Runs `f` on a thread of its own; its result, or None if it did not end within `bound`.
    fn within<T: Send + 'static>(
        bound: Duration,
        f: impl FnOnce() -> T + Send + 'static,
    ) -> Option<T> {
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let _ = tx.send(f());
        });
        rx.recv_timeout(bound).ok()
    }

    #[test]
    fn a_launch_keeps_its_deadline_when_the_queue_is_full() {
        let t = temp();
        let sock = t.0.join("s.sock");
        let _l = deaf_listener(&sock);
        let _held = fill_queue(&sock);
        let s = sock.clone();
        let r = within(Duration::from_secs(5), move || {
            ipc::activate(&s, Instant::now() + Duration::from_millis(500)).is_err()
        });
        assert_eq!(
            r,
            Some(true),
            "the launch blocked past its deadline on a full queue"
        );
    }

    #[test]
    fn quitting_does_not_wait_on_a_full_queue() {
        let t = temp();
        let dir = t.0.join("instance");
        let (i, _) = counting(&t, &dir);
        let _held = fill_queue(&socket_of(&i));
        let r = within(Duration::from_secs(1), move || i.release());
        assert!(r.is_some(), "release blocked on a full queue");
    }

    /// Clients that connect and say nothing do not keep a real launch waiting on them.
    #[test]
    fn silent_clients_do_not_hold_up_a_launch() {
        let t = temp();
        let dir = t.0.join("instance");
        let (i, n) = counting(&t, &dir);
        let sock = socket_of(&i);
        let _silent: Vec<_> = (0..11)
            .map(|_| UnixStream::connect(&sock).unwrap())
            .collect();
        let r = start(&dir, Duration::from_millis(1500));
        assert!(matches!(r, Ok(Start::Raised)), "{:?}", r.err());
        assert_eq!(n.load(Ordering::SeqCst), 1);
    }

    /// A FIFO where the address goes (left there, or planted while the folder was open to
    /// others) does not hold up a later launch: opening it does not wait for a writer.
    #[test]
    fn a_fifo_in_place_of_the_address_does_not_hang_a_launch() {
        use std::os::unix::ffi::OsStrExt;
        let t = temp();
        let dir = t.0.join("instance");
        let held = first(&dir); // holds the lock, not listening yet
        let fifo = std::ffi::CString::new(dir.join(ADDRESS).as_os_str().as_bytes()).unwrap();
        assert_eq!(unsafe { libc::mkfifo(fifo.as_ptr(), 0o600) }, 0);
        let d = dir.clone();
        let e = within(Duration::from_secs(5), move || {
            start(&d, Duration::from_millis(300)).err()
        })
        .expect("a launch hung opening the FIFO at the address")
        .expect("a launch ran as a second instance");
        assert!(
            e.contains("did not answer") && e.contains("not a plain file"),
            "{e}"
        );
        // The positive control on the same route: once the holder listens, its address replaces
        // the FIFO and a launch raises it.
        held.listen(&t.run(), || true).unwrap();
        assert!(matches!(
            start(&dir, Duration::from_secs(5)).unwrap(),
            Start::Raised
        ));
    }

    /// A profile held by an app whose socket takes connections and never answers (its address
    /// names a listener that never accepts), and that listener.
    fn mute_holder(t: &Temp, dir: &Path) -> (Instance, std::os::unix::net::UnixListener) {
        let held = first(dir);
        let sock = t.0.join("mute.sock");
        let mute = std::os::unix::net::UnixListener::bind(&sock).unwrap();
        fs::write(dir.join(ADDRESS), sock.as_os_str().as_encoded_bytes()).unwrap();
        fs::set_permissions(dir.join(ADDRESS), fs::Permissions::from_mode(0o600)).unwrap();
        (held, mute)
    }

    /// The connections made to `l` so far: they wait in its queue, also once closed.
    fn connections(l: &std::os::unix::net::UnixListener) -> usize {
        l.set_nonblocking(true).unwrap();
        let mut n = 0;
        while l.accept().is_ok() {
            n += 1;
        }
        n
    }

    /// Once its deadline has passed, a launch does not ask the holder again: it reads the
    /// address, but connects to nothing.
    #[test]
    fn a_launch_past_its_deadline_asks_nothing() {
        let t = temp();
        let dir = t.0.join("instance");
        let (_held, mute) = mute_holder(&t, &dir);
        let e = start_by(&dir, Instant::now()).err().unwrap();
        assert!(e.contains("did not answer"), "{e}");
        assert_eq!(
            connections(&mute),
            0,
            "a launch connected after its deadline"
        );
        // The positive control on the same route: with time left, the launch does connect.
        let e = start(&dir, Duration::from_millis(500)).err().unwrap();
        assert!(e.contains("did not answer"), "{e}");
        assert_eq!(connections(&mute), 1);
    }

    /// A launch whose holder never answers ends, and not before its deadline. Only a watchdog
    /// bounds it from above; the deadline itself is the test above's.
    #[test]
    fn a_launch_that_gets_no_answer_ends() {
        let t = temp();
        let dir = t.0.join("instance");
        let (_held, mute) = mute_holder(&t, &dir);
        let wait = Duration::from_millis(300);
        let d = dir.clone();
        let (e, took) = within(Duration::from_secs(20), move || {
            let t0 = Instant::now();
            (start(&d, wait).err(), t0.elapsed())
        })
        .expect("a launch did not end");
        let e = e.expect("a launch ran as a second instance");
        assert!(e.contains("did not answer"), "{e}");
        assert!(took >= wait, "it gave up after {took:?}, before {wait:?}");
        // Its one try waits for the answer until the deadline; there is no try after it.
        assert!(connections(&mute) <= 1);
    }

    /// A socket folder another user could swap for theirs is not used: one in a folder open to
    /// others' writes and not sticky, one below such a folder, one below a link. The sticky
    /// folder after them, as `/tmp` is, is used.
    #[test]
    fn a_socket_folder_others_could_replace_is_not_used() {
        let t = temp();
        let dir = t.0.join("instance");
        let i = first(&dir);
        let mode = |p: &Path, m: u32| {
            fs::create_dir_all(p).unwrap();
            fs::set_permissions(p, fs::Permissions::from_mode(m)).unwrap();
        };
        let open = t.0.join("open");
        mode(&open, 0o777);
        mode(&open.join("inner"), 0o700);
        let real = t.0.join("real");
        mode(&real, 0o700);
        let link = t.0.join("link");
        std::os::unix::fs::symlink(&real, &link).unwrap();
        let sticky = t.0.join("sticky");
        mode(&sticky, 0o1777);
        let refused = [
            open.join("app"),
            open.join("inner").join("app"),
            link.join("app"),
        ];
        let mut dirs = refused.to_vec();
        dirs.push(sticky.join("app"));
        i.listen(&dirs, || true).unwrap();
        assert_eq!(socket_of(&i).parent().unwrap(), sticky.join("app"));
        for d in refused {
            assert!(!d.exists(), "{} was made", d.display());
        }
        assert!(matches!(
            start(&dir, Duration::from_secs(5)).unwrap(),
            Start::Raised
        ));
    }

    /// Quitting removes the socket from the folder it was made in, also when that folder's path
    /// now leads somewhere else: what is there is not this app's.
    #[test]
    fn quitting_removes_the_socket_from_its_own_folder() {
        let t = temp();
        let dir = t.0.join("instance");
        let (i, _) = counting(&t, &dir);
        let name = socket_of(&i).file_name().unwrap().to_owned();
        let run = t.0.join("run");
        let moved = t.0.join("moved");
        fs::rename(&run, &moved).unwrap();
        let theirs = t.0.join("theirs");
        fs::create_dir(&theirs).unwrap();
        fs::write(theirs.join(&name), b"theirs").unwrap();
        std::os::unix::fs::symlink(&theirs, &run).unwrap();
        i.release();
        assert_eq!(
            fs::read(theirs.join(&name)).unwrap(),
            b"theirs",
            "quitting removed a file through the path"
        );
        assert!(
            !moved.join(&name).exists(),
            "the socket stayed in its folder"
        );
    }
}
