//! The app's own core, in managed mode: a core program the app starts with a config it keeps in
//! its data folder, supervises (only that process) and stops when it quits or the person picks
//! another mode.

use crate::owned::Child;
use serde_json::{json, Value};
use std::collections::VecDeque;
use std::fs::{self, File};
use std::io::{self, BufRead, BufReader, Write};
use std::net::{SocketAddr, TcpListener};
use std::path::{Path, PathBuf};
use std::process::ExitStatus;
#[cfg(unix)]
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc;
use std::sync::{Arc, Mutex, Weak};
use std::time::{Duration, Instant};

const START_TIMEOUT: Duration = Duration::from_secs(30);
const STOP_TIMEOUT: Duration = Duration::from_secs(10);
/// How many of core's last log lines the app keeps, to say why it stopped.
const TAIL: usize = 20;

/// How often a start is tried while the program's file is busy, and the pause after the nth
/// try (n times this): about 0.9 s in all.
const BUSY_TRIES: u32 = 10;
pub(crate) const BUSY_PAUSE: Duration = Duration::from_millis(20);

/// Whether e says the program's file is open for writing (ETXTBSY).
fn is_busy(e: &io::Error) -> bool {
    #[cfg(unix)]
    return e.raw_os_error() == Some(libc::ETXTBSY);
    #[cfg(not(unix))]
    {
        let _ = e;
        false
    }
}

/// Runs `start`, again after a pause (growing by `pause` each time) up to `BUSY_TRIES` tries
/// while it fails because the program's file is busy, and returns the last result. A program the
/// app has just written can be busy for a moment although the app closed it: a process forked
/// by another thread meanwhile holds a copy of the write handle until it execs. Any other
/// error is returned at once.
pub(crate) fn start_when_free<T>(
    pause: Duration,
    mut start: impl FnMut() -> io::Result<T>,
) -> io::Result<T> {
    let mut tried = 1;
    loop {
        match start() {
            Err(e) if is_busy(&e) && tried < BUSY_TRIES => {
                std::thread::sleep(pause * tried);
                tried += 1;
            }
            r => return r,
        }
    }
}

/// 32 random bytes, hex-encoded.
pub fn random_key() -> String {
    let mut b = [0u8; 32];
    getrandom::fill(&mut b).expect("the system's random source");
    b.iter().map(|x| format!("{x:02x}")).collect()
}

/// Writes `path` readable only by this user, replacing it in one step: on Windows with a protected
/// access list allowing only this user and SYSTEM, and the replace written through to disk before
/// this returns (see `replace`).
pub fn write_private(path: &Path, data: &[u8]) -> io::Result<()> {
    write_private_with_nonce(path, data, random_key)
}

fn write_private_with_nonce(
    path: &Path,
    data: &[u8],
    mut nonce: impl FnMut() -> String,
) -> io::Result<()> {
    let parent = path
        .parent()
        .filter(|p| !p.as_os_str().is_empty())
        .unwrap_or(Path::new("."));
    let name = path
        .file_name()
        .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidInput, "no file name"))?;
    #[cfg(unix)]
    let folder = fs::File::open(parent)?;
    let (tmp, mut f) = loop {
        let mut name = name.to_os_string();
        name.push(format!(".{}.tmp", nonce()));
        let tmp = parent.join(name);
        #[cfg(unix)]
        let made = create_temp(&folder, &tmp);
        #[cfg(windows)]
        let made = create_temp(&tmp);
        match made {
            Ok(f) => break (tmp, f),
            Err(e) if e.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(e) => return Err(e),
        }
    };
    let result = (|| {
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            f.set_permissions(fs::Permissions::from_mode(0o600))?;
        }
        f.write_all(data)?;
        f.sync_all()?;
        drop(f);
        #[cfg(unix)]
        fs::rename(&tmp, path)?;
        #[cfg(windows)]
        replace(&tmp, path)?;
        // The file is in place once renamed; syncing the folder only makes the rename durable
        // sooner, and some filesystems refuse it, so a failure here does not fail the write.
        #[cfg(unix)]
        let _ = folder.sync_all();
        Ok(())
    })();
    if result.is_err() {
        let _ = fs::remove_file(&tmp);
    }
    result
}

/// Windows: tmp replaces path, the rename written through to disk before this returns
/// (`MoveFileExW` with `MOVEFILE_WRITE_THROUGH`). A record written this way is on disk before
/// what follows it: the app's update writes `pending.json` and its record of leftovers so before
/// it moves any part of the program set (`updater/set.rs`), whatever the volumes. When another
/// program has path open, that rename is refused (access denied); then the standard library's
/// rename replaces it (its POSIX-semantics replace), as before, which is not written through.
#[cfg(windows)]
fn replace(tmp: &Path, path: &Path) -> io::Result<()> {
    use windows_sys::Win32::Foundation::ERROR_ACCESS_DENIED;
    use windows_sys::Win32::Storage::FileSystem::{
        MoveFileExW, MOVEFILE_REPLACE_EXISTING, MOVEFILE_WRITE_THROUGH,
    };
    // In their `\\?\` form, so a path longer than MAX_PATH works as with std's rename.
    let from = crate::private_windows::verbatim(tmp)?;
    let to = crate::private_windows::verbatim(path)?;
    // SAFETY: both are NUL-terminated wide strings that outlive the call.
    let moved = unsafe {
        MoveFileExW(
            from.as_ptr(),
            to.as_ptr(),
            MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH,
        )
    };
    if moved != 0 {
        return Ok(());
    }
    let e = io::Error::last_os_error();
    if e.raw_os_error() == Some(ERROR_ACCESS_DENIED as i32) {
        return fs::rename(tmp, path);
    }
    Err(e)
}

/// Makes tmp, in the folder open as folder, new and for writing, readable only by this user
/// (`write_private`'s temporary file), never through a link. macOS: where the volume keeps access
/// lists, with an empty one that takes nothing from the folder's, so no entry is on it at any
/// moment (`private_macos.rs`).
#[cfg(target_os = "macos")]
fn create_temp(folder: &fs::File, tmp: &Path) -> io::Result<fs::File> {
    crate::private_macos::create_new_in(folder, tmp, 0o600)
}
#[cfg(all(unix, not(target_os = "macos")))]
fn create_temp(_folder: &fs::File, tmp: &Path) -> io::Result<fs::File> {
    use std::os::unix::fs::OpenOptionsExt;
    fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .custom_flags(libc::O_NOFOLLOW)
        .open(tmp)
}
/// Windows: with a protected access list allowing only this user and SYSTEM, given as it is made
/// (`private_windows.rs`).
#[cfg(windows)]
fn create_temp(tmp: &Path) -> io::Result<fs::File> {
    crate::private_windows::create_new_private(tmp)
}

/// The core config the app keeps, `<dir>/config.json`. The app writes it once, the first time,
/// with a free loopback port that then stays the core's, fresh keys and folders under dir; after
/// that it is the person's to edit (core reloads it), and the app only reads the management key.
/// On macOS dir is first emptied of extended access list entries (`clear_core_folder`).
pub fn ensure_config(dir: &Path) -> Result<PathBuf, String> {
    let path = dir.join("config.json");
    #[cfg(target_os = "macos")]
    fs::create_dir_all(dir)
        .and_then(|_| clear_core_folder(dir))
        .map_err(|e| format!("{}: {e}", dir.display()))?;
    if path.exists() {
        return Ok(path);
    }
    fs::create_dir_all(dir).map_err(|e| format!("creating {}: {e}", dir.display()))?;
    let port = TcpListener::bind("127.0.0.1:0")
        .and_then(|l| l.local_addr())
        .map_err(|e| format!("finding a free port: {e}"))?
        .port();
    let cfg = json!({
        "listen": format!("127.0.0.1:{port}"),
        "client_keys": [random_key()],
        "management_key": random_key(),
        "credentials_dir": dir.join("credentials"),
        "packages": {"dir": dir.join("packages")},
    });
    let text = serde_json::to_vec_pretty(&cfg).expect("a JSON value");
    write_private(&path, &text).map_err(|e| format!("writing {}: {e}", path.display()))?;
    Ok(path)
}

/// macOS: the app's core folder holds the core's keys (config.json) and its credentials, so it
/// keeps no extended access list entry, the rule of the app's other private folders
/// (`private_macos.rs`): the folder, its config and the credentials and packages folders the
/// config names by default are given an empty list, and so is each credential file with an
/// entry (cleared, not refused: the core owns them, and refusing would stop it). The folder is
/// opened by its path and `credentials` is listed by its path; what is inside the folder is
/// opened beneath it, never through a link, so a linked credential file is skipped. The core
/// writes into folders that pass nothing on.
#[cfg(target_os = "macos")]
fn clear_core_folder(dir: &Path) -> io::Result<()> {
    use crate::private_macos::{clear_entries, open_beneath};
    let root = fs::File::open(dir)?;
    clear_entries(&root)?;
    if let Some(f) = open_beneath(&root, "config.json", false)? {
        clear_entries(&f)?;
    }
    if let Some(d) = open_beneath(&root, "packages", true)? {
        clear_entries(&d)?;
    }
    if let Some(d) = open_beneath(&root, "credentials", true)? {
        clear_entries(&d)?;
        for e in fs::read_dir(dir.join("credentials"))? {
            if let Some(f) = open_beneath(&d, e?.file_name(), false)? {
                clear_entries(&f)?;
            }
        }
    }
    Ok(())
}

/// The address core is to listen on, from `listen` in the core config at path: an IP address and
/// port, or `localhost` and a port (`127.0.0.1`, which is where core listens for it).
pub fn listen_addr(path: &Path) -> Result<SocketAddr, String> {
    let text = fs::read(path).map_err(|e| format!("reading {}: {e}", path.display()))?;
    let cfg: Value = serde_json::from_slice(&text)
        .map_err(|e| format!("{} is not JSON: {e}", path.display()))?;
    let listen = cfg.get("listen").and_then(Value::as_str).unwrap_or("");
    if let Ok(a) = listen.parse() {
        return Ok(a);
    }
    match listen.strip_prefix("localhost:").map(str::parse::<u16>) {
        Some(Ok(port)) => Ok(SocketAddr::from(([127, 0, 0, 1], port))),
        _ => Err(format!(
            "{}: listen {listen:?} is not an IP address and port (such as 127.0.0.1:8400), which the app needs to hold its core's port",
            path.display()
        )),
    }
}

/// Binds addr for the app's core, waiting up to two seconds while it is in use: the previous
/// core's socket stays open until the last call that could reach it has ended.
#[cfg(unix)]
fn bind(addr: SocketAddr) -> Result<TcpListener, String> {
    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        match TcpListener::bind(addr) {
            Ok(l) => return Ok(l),
            Err(e) if e.kind() == io::ErrorKind::AddrInUse && Instant::now() < deadline => {
                std::thread::sleep(Duration::from_millis(50))
            }
            Err(e) => return Err(format!("listening on {addr} for the app's core: {e}")),
        }
    }
}

/// The management key in the core config at path.
pub fn management_key(path: &Path) -> Result<String, String> {
    let text = fs::read(path).map_err(|e| format!("reading {}: {e}", path.display()))?;
    let cfg: Value = serde_json::from_slice(&text)
        .map_err(|e| format!("{} is not JSON: {e}", path.display()))?;
    match cfg.get("management_key").and_then(Value::as_str) {
        Some(k) if !k.is_empty() => Ok(k.to_string()),
        _ => Err(format!(
            "{} sets no management_key, so the app cannot manage this core",
            path.display()
        )),
    }
}

/// The core program to run: the one the person chose, or `pluxy` beside the app's own program.
/// The app never looks it up on PATH, which could hold some other core.
pub fn core_program(chosen: Option<&str>) -> Result<PathBuf, String> {
    if let Some(p) = chosen.filter(|p| !p.is_empty()) {
        let p = PathBuf::from(p);
        if !p.is_absolute() {
            return Err("the core program must be given as a full path".into());
        }
        return Ok(p);
    }
    let exe = std::env::current_exe().map_err(|e| e.to_string())?;
    let name = if cfg!(windows) { "pluxy.exe" } else { "pluxy" };
    let beside = exe.with_file_name(name);
    if beside.is_file() {
        Ok(beside)
    } else {
        Err(format!(
            "no core program at {}: choose one on the Connection page",
            beside.display()
        ))
    }
}

/// Called when the app's core exits, whatever the reason.
pub type OnExit = Arc<dyn Fn() + Send + Sync>;

/// Set once the app's core is being stopped or has exited: from then on nothing is sent to its
/// address.
pub type Gone = Arc<AtomicBool>;

/// The app's copy of its core's listening socket. While any copy is held no other program can
/// listen on that address, so a call with the management key reaches the core or waits in this
/// socket, never another program, also while the core drains after a stop or once it has crashed.
pub type Hold = Arc<TcpListener>;

/// A running core of the app's own. A supervisor thread starts it and stays until it exits:
/// on Linux the parent-death signal follows the thread that started the process, and the
/// threads the app's commands run on end when idle.
pub struct ManagedCore {
    child: Arc<Mutex<Child>>,
    pid: u32,
    base: String,
    key: String,
    tail: Arc<Mutex<VecDeque<String>>>,
    status: Arc<Mutex<Option<ExitStatus>>>,
    gone: Gone,
    hold: Option<Hold>,
}

impl ManagedCore {
    /// Starts the open program (named name in messages) with the config in dir (made if missing)
    /// and waits until core says where it listens. The program runs from that open file, the one
    /// its caller checked (on macOS and Windows by the path it has now, checked again: see
    /// `owned`). On Unix the app binds core's `listen` address itself and hands core the socket
    /// (`-listen-fd`), keeping its own copy; core must say it listens there. On Windows it runs
    /// in a job of its own, which ends it if the app ends without stopping it. Core's log goes to
    /// the app's stderr, each line prefixed `core: `. on_exit runs once core has exited.
    pub fn start(
        program: File,
        name: &Path,
        dir: &Path,
        on_exit: Option<OnExit>,
    ) -> Result<ManagedCore, String> {
        let cfg = ensure_config(dir)?;
        let key = management_key(&cfg)?;
        #[cfg(unix)]
        let hold: Option<Hold> = Some(Arc::new(bind(listen_addr(&cfg)?)?));
        #[cfg(not(unix))]
        let hold: Option<Hold> = None;
        #[cfg(unix)]
        let mut cmd = Command::new(crate::owned::exec_target(
            &program,
            crate::owned::Writers::Nobody,
        )?);
        #[cfg(unix)]
        {
            use std::os::unix::process::CommandExt;
            cmd.arg0(name);
            // On Linux the child keeps the open program across exec, which a #! script's
            // interpreter then reads (as /dev/fd/N); the app's own copy closes on exec as usual.
            #[cfg(target_os = "linux")]
            unsafe {
                use std::os::fd::AsRawFd;
                let fd = program.as_raw_fd();
                cmd.pre_exec(move || {
                    if libc::fcntl(fd, libc::F_SETFD, 0) != 0 {
                        return Err(io::Error::last_os_error());
                    }
                    Ok(())
                });
            }
        }
        // Its environment is the app's, as on Unix, where it inherits it.
        #[cfg(windows)]
        let mut cmd = {
            use crate::owned::windows::{self as w, Io, Kind};
            let mut c = w::Command::new(w::pin_file(&program)?, Kind::Managed);
            c.arg0(name)
                .envs(std::env::vars_os())
                .stdin(Io::Null)
                .stdout(Io::Null)
                .stderr(Io::Piped);
            c
        };
        cmd.args([std::ffi::OsStr::new("-config"), cfg.as_os_str()]);
        #[cfg(unix)]
        if let Some(l) = &hold {
            use std::os::fd::AsRawFd;
            use std::os::unix::process::CommandExt;
            let lfd = l.as_raw_fd();
            cmd.arg("-listen-fd").arg(lfd.to_string());
            // Only the core inherits the socket: the app's copy closes on exec as usual.
            unsafe {
                cmd.pre_exec(move || {
                    if libc::fcntl(lfd, libc::F_SETFD, 0) != 0 {
                        return Err(io::Error::last_os_error());
                    }
                    Ok(())
                });
            }
        }
        cmd.current_dir(dir);
        #[cfg(unix)]
        cmd.stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::piped());
        #[cfg(target_os = "linux")]
        {
            use std::os::unix::process::CommandExt;
            // If the app dies without stopping it, core gets SIGTERM rather than living on.
            unsafe {
                cmd.pre_exec(|| {
                    if libc::prctl(libc::PR_SET_PDEATHSIG, libc::SIGTERM) != 0 {
                        return Err(io::Error::last_os_error());
                    }
                    Ok(())
                });
            }
        }
        let status: Arc<Mutex<Option<ExitStatus>>> = Arc::new(Mutex::new(None));
        let gone: Gone = Arc::new(AtomicBool::new(false));
        let (started_tx, started_rx) = mpsc::channel();
        let st = Arc::clone(&status);
        let g = Arc::clone(&gone);
        let waiting = hold.as_ref().map(Arc::downgrade);
        let name = name.display().to_string();
        std::thread::spawn(move || {
            let spawned = start_when_free(BUSY_PAUSE, || cmd.spawn());
            drop(program);
            let mut child = match spawned {
                Ok(c) => c,
                Err(e) => {
                    let _ = started_tx.send(Err(format!("starting {name}: {e}")));
                    return;
                }
            };
            // Opened before anything can reap the child, so it is this process's.
            #[cfg(target_os = "linux")]
            let mut exit_fd = pidfd(child.id());
            let stderr = child.stderr.take().expect("piped stderr");
            let child = Arc::new(Mutex::new(child));
            let _ = started_tx.send(Ok((Arc::clone(&child), stderr)));
            // Reaped only here, under the lock stop() kills under, so a signal never reaches a
            // process that has already gone. The exit is seen as it happens (a pidfd), and calls
            // stop before the process is reaped; without a pidfd, within 100 ms.
            loop {
                #[cfg(target_os = "linux")]
                if let Some(fd) = &exit_fd {
                    if exited_fd(fd) {
                        g.store(true, Ordering::SeqCst);
                    } else {
                        exit_fd = None;
                    }
                }
                let done = child.lock().unwrap().try_wait();
                match done {
                    Ok(None) => std::thread::sleep(Duration::from_millis(100)),
                    Ok(Some(s)) => {
                        g.store(true, Ordering::SeqCst);
                        *st.lock().unwrap() = Some(s);
                        break;
                    }
                    Err(_) => {
                        g.store(true, Ordering::SeqCst);
                        break;
                    }
                }
            }
            if let Some(f) = on_exit {
                f();
            }
            if let Some(w) = waiting {
                refuse_waiting(w);
            }
        });
        let (child, stderr) = started_rx.recv().map_err(|e| e.to_string())??;
        let pid = child.lock().unwrap().id();
        let tail = Arc::new(Mutex::new(VecDeque::with_capacity(TAIL)));
        let (tx, rx) = mpsc::channel::<String>();
        let lines = Arc::clone(&tail);
        std::thread::spawn(move || {
            let mut tx = Some(tx);
            for line in BufReader::new(stderr).lines() {
                let Ok(line) = line else { break };
                eprintln!("core: {line}");
                if let Some(addr) = line.strip_prefix("pluxy: listening on ") {
                    if let Some(tx) = tx.take() {
                        let _ = tx.send(addr.to_string());
                    }
                }
                let mut t = lines.lock().unwrap();
                if t.len() == TAIL {
                    t.pop_front();
                }
                t.push_back(line);
            }
        });
        let mut core = ManagedCore {
            child,
            pid,
            base: String::new(),
            key,
            tail,
            status,
            gone,
            hold,
        };
        let deadline = Instant::now() + START_TIMEOUT;
        let mut gone: Option<Instant> = None;
        loop {
            match rx.recv_timeout(Duration::from_millis(100)) {
                Ok(addr) => {
                    if let Some(l) = &core.hold {
                        let given = l.local_addr().map_err(|e| e.to_string())?;
                        let said = addr
                            .split_once("://")
                            .and_then(|(_, a)| a.parse::<SocketAddr>().ok());
                        if said != Some(given) {
                            core.kill();
                            return Err(format!(
                                "the started core is not listening on the socket the app gave it ({given}): it said {addr}"
                            ));
                        }
                    }
                    core.base = addr;
                    return Ok(core);
                }
                Err(mpsc::RecvTimeoutError::Timeout) if Instant::now() < deadline => {
                    // Exited: give its last log lines a moment to arrive, to say why.
                    if core.exited().is_some() {
                        let since = *gone.get_or_insert_with(Instant::now);
                        if since.elapsed() > Duration::from_secs(1) {
                            return Err(core.exited().unwrap_or_default());
                        }
                    }
                }
                Err(_) => {
                    let timed_out = Instant::now() >= deadline;
                    // Its log ended: it is exiting, or has.
                    if !timed_out {
                        core.wait(Duration::from_secs(2));
                    }
                    core.kill();
                    let why = core.exited().unwrap_or_default();
                    return Err(if timed_out {
                        format!(
                            "core did not say where it listens within {}s ({why})",
                            START_TIMEOUT.as_secs()
                        )
                    } else {
                        why
                    });
                }
            }
        }
    }

    /// Where core listens, such as `http://127.0.0.1:8400`.
    pub fn base(&self) -> &str {
        &self.base
    }

    pub fn key(&self) -> &str {
        &self.key
    }

    pub fn pid(&self) -> u32 {
        self.pid
    }

    /// On Windows, a handle on the core's process that keeps its process ID from passing to
    /// another process for as long as it is held, past this `ManagedCore` too; it cannot end the
    /// core (`owned::Child::pin`).
    #[cfg(windows)]
    pub(crate) fn pin(&self) -> Result<crate::owned::Pin, String> {
        self.child
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .pin()
            .map_err(|e| format!("holding the core's process: {e}"))
    }

    /// Set once this core is being stopped or has exited.
    pub fn gone(&self) -> Gone {
        Arc::clone(&self.gone)
    }

    /// The app's copy of this core's listening socket, for a call to hold while it may send.
    pub fn hold(&self) -> Option<Hold> {
        self.hold.clone()
    }

    /// None while core runs; why it stopped once it has.
    pub fn exited(&self) -> Option<String> {
        let status = (*self.status.lock().unwrap())?;
        let last = self
            .tail
            .lock()
            .unwrap()
            .back()
            .cloned()
            .unwrap_or_default();
        Some(format!("core exited ({status}): {last}"))
    }

    /// Waits up to t for core to have exited.
    fn wait(&self, t: Duration) -> bool {
        let deadline = Instant::now() + t;
        while Instant::now() < deadline {
            if self.status.lock().unwrap().is_some() {
                return true;
            }
            std::thread::sleep(Duration::from_millis(50));
        }
        self.status.lock().unwrap().is_some()
    }

    fn kill(&self) {
        self.gone.store(true, Ordering::SeqCst);
        {
            let mut c = self.child.lock().unwrap();
            if !matches!(c.try_wait(), Ok(None)) {
                return;
            }
            let _ = c.kill();
        }
        self.wait(STOP_TIMEOUT);
    }

    /// Asks core to stop (SIGTERM, so it drains), and kills it if it has not within 10 seconds.
    /// Calls stop first: core closes its listener as it starts draining.
    pub fn stop(&mut self) {
        self.gone.store(true, Ordering::SeqCst);
        {
            let mut c = self.child.lock().unwrap();
            if !matches!(c.try_wait(), Ok(None)) {
                drop(c);
                self.wait(Duration::from_secs(1));
                return;
            }
            // SIGTERM to it (on Windows CTRL_BREAK to its group), while it is unreaped; one that
            // cannot be asked (on Windows: no console it can get one on) is ended at once.
            if !crate::owned::term_process(&c) {
                let _ = c.kill();
            }
        }
        if !self.wait(STOP_TIMEOUT) {
            self.kill();
        }
    }
}

/// Once the app's core has exited, fails the calls that still reach the app's copy of its
/// listening socket (accepted and closed at once) instead of leaving them waiting, until nothing
/// else holds that socket and it closes.
fn refuse_waiting(socket: Weak<TcpListener>) {
    while let Some(l) = socket.upgrade() {
        let _ = l.set_nonblocking(true);
        while let Ok((c, _)) = l.accept() {
            drop(c);
        }
        #[cfg(unix)]
        {
            use std::os::fd::AsRawFd;
            let mut p = libc::pollfd {
                fd: l.as_raw_fd(),
                events: libc::POLLIN,
                revents: 0,
            };
            unsafe { libc::poll(&mut p, 1, 100) };
        }
        #[cfg(not(unix))]
        std::thread::sleep(Duration::from_millis(100));
    }
}

/// A pidfd for process pid, readable once it has exited; none on kernels without them.
#[cfg(target_os = "linux")]
fn pidfd(pid: u32) -> Option<std::os::fd::OwnedFd> {
    use std::os::fd::FromRawFd;
    let fd = unsafe { libc::syscall(libc::SYS_pidfd_open, pid as libc::pid_t, 0) };
    (fd >= 0).then(|| unsafe { std::os::fd::OwnedFd::from_raw_fd(fd as libc::c_int) })
}

/// Waits until the process behind pidfd fd has exited: true then, false if it cannot wait.
#[cfg(target_os = "linux")]
fn exited_fd(fd: &std::os::fd::OwnedFd) -> bool {
    use std::os::fd::AsRawFd;
    let mut p = libc::pollfd {
        fd: fd.as_raw_fd(),
        events: libc::POLLIN,
        revents: 0,
    };
    loop {
        match unsafe { libc::poll(&mut p, 1, -1) } {
            1.. => return p.revents & libc::POLLIN != 0,
            _ if io::Error::last_os_error().kind() == io::ErrorKind::Interrupted => continue,
            _ => return false,
        }
    }
}

/// Checks that every socket listening on base's port is the started core's own, before anything
/// is sent there: another program on this computer (another user's too) could listen on that
/// port, and the app sends the management key there.
#[cfg(target_os = "linux")]
pub fn owns_listener(core: &ManagedCore, base: &str) -> Result<(), String> {
    let not = || format!("the started core is not listening on {base} itself");
    let port = url::Url::parse(base)
        .ok()
        .and_then(|u| u.port_or_known_default())
        .ok_or_else(not)?;
    let listening =
        listening_inodes(port).map_err(|e| format!("reading this computer's sockets: {e}"))?;
    // The core stays unreaped until it exits, so its process ID is not reused meanwhile.
    let own =
        socket_inodes(core.pid()).map_err(|e| format!("reading the core's open files: {e}"))?;
    if core.exited().is_some() || listening.is_empty() || !listening.iter().all(|i| own.contains(i))
    {
        return Err(not());
    }
    Ok(())
}

/// On Windows: every listener on base's port, on any address, is the core's process (the
/// system's table of listeners and the process that owns each, which lists every user's). The
/// app holds the core's process handle, so its process ID is not another process's meanwhile.
#[cfg(windows)]
pub fn owns_listener(core: &ManagedCore, base: &str) -> Result<(), String> {
    let not = || format!("the started core is not listening on {base} itself");
    let port = url::Url::parse(base)
        .ok()
        .and_then(|u| u.port_or_known_default())
        .ok_or_else(not)?;
    let owners =
        listener_owners(port).map_err(|e| format!("reading this computer's listeners: {e}"))?;
    if core.exited().is_some() || owners.is_empty() || owners.iter().any(|p| *p != core.pid()) {
        return Err(not());
    }
    Ok(())
}

/// The process IDs owning each TCP socket listening on port, IPv4 and IPv6, on any address.
#[cfg(windows)]
fn listener_owners(port: u16) -> io::Result<Vec<u32>> {
    use windows_sys::Win32::NetworkManagement::IpHelper::TCP_TABLE_OWNER_PID_LISTENER;
    Ok(tcp_table(TCP_TABLE_OWNER_PID_LISTENER)?
        .into_iter()
        .filter(|r| r.local.port() == port)
        .map(|r| r.pid)
        .collect())
}

/// One row of the system's TCP table: its two ends, its state and the process that owns it.
#[cfg(windows)]
struct TcpRow {
    local: std::net::SocketAddr,
    remote: std::net::SocketAddr,
    state: u32,
    pid: u32,
}

/// The system's TCP table of class (`TCP_TABLE_OWNER_PID_*`), IPv4 and IPv6, every user's.
#[cfg(windows)]
fn tcp_table(class: i32) -> io::Result<Vec<TcpRow>> {
    use std::net::{Ipv4Addr, Ipv6Addr, SocketAddr, SocketAddrV6};
    use windows_sys::Win32::Foundation::{ERROR_INSUFFICIENT_BUFFER, NO_ERROR};
    use windows_sys::Win32::NetworkManagement::IpHelper::{
        GetExtendedTcpTable, MIB_TCP6TABLE_OWNER_PID, MIB_TCPTABLE_OWNER_PID,
    };
    use windows_sys::Win32::Networking::WinSock::{AF_INET, AF_INET6};
    // Ports are in network byte order, in the low 16 bits; IPv4 addresses in network order too.
    let port = |p: u32| u16::from_be(p as u16);
    let v4 = |a: u32, p: u32| SocketAddr::from((Ipv4Addr::from(a.to_ne_bytes()), port(p)));
    let v6 =
        |a: [u8; 16], p: u32| SocketAddr::V6(SocketAddrV6::new(Ipv6Addr::from(a), port(p), 0, 0));
    let mut rows = Vec::new();
    for family in [AF_INET, AF_INET6] {
        // u64s, so the table is aligned as its rows need.
        let mut buf: Vec<u64> = Vec::new();
        let mut size = 0u32;
        loop {
            let r = unsafe {
                GetExtendedTcpTable(
                    buf.as_mut_ptr().cast(),
                    &mut size,
                    0,
                    u32::from(family),
                    class,
                    0,
                )
            };
            match r {
                NO_ERROR => break,
                // The table grew since its size was asked: ask again.
                ERROR_INSUFFICIENT_BUFFER => buf = vec![0; (size as usize).div_ceil(8) + 1],
                e => return Err(io::Error::from_raw_os_error(e as i32)),
            }
        }
        if buf.is_empty() {
            continue;
        }
        // SAFETY: GetExtendedTcpTable wrote a table of this family's rows into buf.
        unsafe {
            if family == AF_INET {
                let t = &*(buf.as_ptr() as *const MIB_TCPTABLE_OWNER_PID);
                let t = std::slice::from_raw_parts(t.table.as_ptr(), t.dwNumEntries as usize);
                rows.extend(t.iter().map(|r| TcpRow {
                    local: v4(r.dwLocalAddr, r.dwLocalPort),
                    remote: v4(r.dwRemoteAddr, r.dwRemotePort),
                    state: r.dwState,
                    pid: r.dwOwningPid,
                }));
            } else {
                let t = &*(buf.as_ptr() as *const MIB_TCP6TABLE_OWNER_PID);
                let t = std::slice::from_raw_parts(t.table.as_ptr(), t.dwNumEntries as usize);
                rows.extend(t.iter().map(|r| TcpRow {
                    local: v6(r.ucLocalAddr, r.dwLocalPort),
                    remote: v6(r.ucRemoteAddr, r.dwRemotePort),
                    state: r.dwState,
                    pid: r.dwOwningPid,
                }));
            }
        }
    }
    Ok(rows)
}

/// On Windows, where the app holds no copy of its core's listening socket: checks that the
/// connection this process just made from local to peer was accepted by process pid, its core,
/// before anything is sent on it (`client::Client::for_core`). The system's table of
/// connections lists both ends of each, with the process that owns each end, every user's; the
/// connection's other end is the row from peer to local. Another program on the core's port
/// gets nothing, not even the request line: one listening on the core's address since the core
/// ended, or one that was listening on a wildcard address over it all along (Windows lets
/// another account bind `0.0.0.0` beside a loopback listener), which gets the core's
/// connections as soon as the core's socket closes. The caller holds a handle on the core's
/// process (`owned::Pin`), so pid is not another process's meanwhile.
#[cfg(windows)]
pub(crate) fn accepted_by(
    pid: u32,
    local: std::net::SocketAddr,
    peer: std::net::SocketAddr,
) -> Result<(), String> {
    use windows_sys::Win32::NetworkManagement::IpHelper::TCP_TABLE_OWNER_PID_CONNECTIONS;
    // MIB_TCP_STATE_SYN_RCVD and MIB_TCP_STATE_ESTAB: the other end is still a live connection
    // (its closing states can be listed as no process's, and nothing sent there is read).
    const SYN_RCVD: u32 = 4;
    const ESTABLISHED: u32 = 5;
    let same = |a: &std::net::SocketAddr, b: &std::net::SocketAddr| {
        a.ip().to_canonical() == b.ip().to_canonical() && a.port() == b.port()
    };
    // A connection the system has just made can be missing its other end in the table for a
    // moment, or show it before the core's process is set as its owner: look again, for at
    // least a second, before refusing.
    let mut why = format!("the other end of {local} -> {peer} is not in this computer's table");
    for _ in 0..100 {
        let rows = tcp_table(TCP_TABLE_OWNER_PID_CONNECTIONS)
            .map_err(|e| format!("reading this computer's connections: {e}"))?;
        let other = rows.iter().find(|r| {
            same(&r.local, &peer)
                && same(&r.remote, &local)
                && matches!(r.state, SYN_RCVD | ESTABLISHED)
        });
        match other {
            Some(r) if r.pid == pid => return Ok(()),
            Some(r) => {
                let by = r.pid;
                why =
                    format!("{local} -> {peer} was accepted by process {by}, not the core ({pid})");
            }
            None => {}
        }
        std::thread::sleep(std::time::Duration::from_millis(10));
    }
    Err(why)
}

/// On macOS: core still runs and base is the address of the socket the app bound and gave it,
/// which the app still holds; base is a loopback address (`conn::local_origin`). No other user
/// can listen where that address's connections would go meanwhile: the system refuses another
/// user's bind of that address, or of a wildcard over it, on a port a socket of this user's
/// holds, unless both set `SO_REUSEPORT`, which the app's socket does not (checked on the CI
/// runner with a second user, `next-app-platforms.yml`). A wildcard the app held would not be
/// kept so (another user's dual-stack `[::]` binds over `0.0.0.0` there), but the app never
/// sends its key to one. Other processes of this user are not looked at: they can read the
/// core's config, management key and all, anyway.
#[cfg(target_os = "macos")]
pub fn owns_listener(core: &ManagedCore, base: &str) -> Result<(), String> {
    let not = || format!("the started core is not listening on {base} itself");
    let u = url::Url::parse(base).map_err(|_| not())?;
    let port = u.port_or_known_default().ok_or_else(not)?;
    let ip: std::net::IpAddr = match u.host() {
        Some(url::Host::Ipv4(a)) => a.into(),
        Some(url::Host::Ipv6(a)) => a.into(),
        _ => return Err(not()),
    };
    let held = core.hold.as_ref().and_then(|l| l.local_addr().ok());
    if core.exited().is_some() || held != Some(SocketAddr::new(ip, port)) {
        return Err(not());
    }
    Ok(())
}

/// The inodes of the sockets listening on port, on any address.
#[cfg(target_os = "linux")]
fn listening_inodes(port: u16) -> io::Result<Vec<u64>> {
    let mut found = Vec::new();
    for table in ["/proc/net/tcp", "/proc/net/tcp6"] {
        let text = match fs::read_to_string(table) {
            Ok(t) => t,
            Err(e) if e.kind() == io::ErrorKind::NotFound => continue,
            Err(e) => return Err(e),
        };
        for line in text.lines().skip(1) {
            let f: Vec<&str> = line.split_whitespace().collect();
            // sl local_address rem_address st ... inode (the tenth field); 0A is LISTEN.
            let (Some(local), Some(st), Some(inode)) = (f.get(1), f.get(3), f.get(9)) else {
                continue;
            };
            let p = local
                .rsplit_once(':')
                .and_then(|(_, p)| u16::from_str_radix(p, 16).ok());
            if *st == "0A" && p == Some(port) {
                if let Ok(i) = inode.parse() {
                    found.push(i);
                }
            }
        }
    }
    Ok(found)
}

/// The inodes of the sockets process pid has open.
#[cfg(target_os = "linux")]
fn socket_inodes(pid: u32) -> io::Result<std::collections::HashSet<u64>> {
    let mut own = std::collections::HashSet::new();
    for e in fs::read_dir(format!("/proc/{pid}/fd"))? {
        let Ok(link) = fs::read_link(e?.path()) else {
            continue;
        };
        let link = link.to_string_lossy();
        if let Some(i) = link
            .strip_prefix("socket:[")
            .and_then(|r| r.strip_suffix(']'))
            .and_then(|i| i.parse().ok())
        {
            own.insert(i);
        }
    }
    Ok(own)
}

impl Drop for ManagedCore {
    fn drop(&mut self) {
        self.stop();
    }
}

/// The real core, built for the tests (`tests::real_core`).
#[cfg(test)]
pub(crate) use tests::real_core;

#[cfg(test)]
mod tests {
    #[cfg(unix)]
    #[test]
    fn private_write_refuses_planted_temp_symlinks() {
        use std::os::unix::{
            ffi::OsStrExt,
            fs::{symlink, PermissionsExt},
        };
        let dir = std::env::temp_dir().join(format!("pluxy-private-{}", super::random_key()));
        std::fs::create_dir(&dir).unwrap();
        let path = dir.join("connection.json");
        let victim = dir.join("victim");
        std::fs::write(&victim, b"untouched").unwrap();
        let planted = dir.join("connection.json.planted.tmp");
        symlink(&victim, &planted).unwrap();
        let old_temp = path.with_extension("tmp");
        symlink(&victim, &old_temp).unwrap();
        let mut nonces = ["planted".to_string(), super::random_key()].into_iter();
        let key = super::random_key();
        super::write_private_with_nonce(&path, key.as_bytes(), || nonces.next().unwrap()).unwrap();
        assert_eq!(std::fs::read(&victim).unwrap(), b"untouched");
        assert_eq!(std::fs::read(&path).unwrap(), key.as_bytes());
        assert_eq!(
            std::fs::metadata(&path).unwrap().permissions().mode() & 0o777,
            0o600
        );
        assert_eq!(
            std::fs::read_link(&planted).unwrap().as_os_str().as_bytes(),
            victim.as_os_str().as_bytes()
        );
        assert_eq!(std::fs::read_dir(&dir).unwrap().count(), 4);
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[cfg(windows)]
    #[test]
    fn private_write_is_private_on_windows() {
        use crate::private_windows::tests::private;
        let dir = std::env::temp_dir().join(format!("pluxy-private-{}", super::random_key()));
        std::fs::create_dir(&dir).unwrap();
        let path = dir.join("connection.json");
        // The control: what the temporary folder passes down is not private.
        std::fs::write(&path, b"{}").unwrap();
        assert!(!private(&path));
        let key = super::random_key();
        super::write_private(&path, key.as_bytes()).unwrap();
        assert_eq!(std::fs::read(&path).unwrap(), key.as_bytes());
        assert!(private(&path));
        assert_eq!(std::fs::read_dir(&dir).unwrap().count(), 1);
        // A path longer than MAX_PATH (260), as the vault's records can be, and as std takes it.
        let deep = dir.join("d".repeat(200));
        std::fs::create_dir(&deep).unwrap();
        let long = deep.join("index.json");
        assert!(long.as_os_str().len() > 260, "{}", long.display());
        super::write_private(&long, key.as_bytes()).unwrap();
        assert_eq!(std::fs::read(&long).unwrap(), key.as_bytes());
        assert!(private(&long));
        // A file another handle has open (letting it be deleted, as std's opens do): the
        // write-through replace is refused, and std's rename replaces it as before.
        let held = std::fs::File::open(&path).unwrap();
        let again = super::random_key();
        super::write_private(&path, again.as_bytes()).unwrap();
        drop(held);
        assert_eq!(std::fs::read(&path).unwrap(), again.as_bytes());
        assert!(private(&path));
        std::fs::remove_dir_all(dir).unwrap();
    }

    use super::*;

    /// macOS: a file written privately takes nothing from its folder's inheritable access list
    /// entries, also while it is being written, so it is as private as its 0600 mode says.
    #[cfg(target_os = "macos")]
    #[test]
    fn private_write_has_no_access_list_on_macos() {
        let dir = temp("acl");
        crate::private_macos::add_entry(&dir, "everyone allow list,file_inherit");
        let has =
            |p: &Path| crate::private_macos::has_entries(&fs::File::open(p).unwrap()).unwrap();
        // The control: a file made the plain way inherits the entry.
        let plain = dir.join("plain.json");
        fs::write(&plain, b"{}").unwrap();
        assert!(has(&plain));
        // The temporary file write_private makes, checked on the descriptor it is made with,
        // before anything else is done to it.
        let folder = fs::File::open(&dir).unwrap();
        let made = create_temp(&folder, &dir.join("connection.json.tmp")).unwrap();
        assert!(!crate::private_macos::has_entries(&made).unwrap());
        let path = dir.join("connection.json");
        write_private(&path, b"{}").unwrap();
        assert!(!has(&path));
        fs::remove_dir_all(dir).unwrap();
    }

    /// macOS: the core folder from an install whose folders others may use is emptied of
    /// extended access list entries before the core starts: the folder, its config, the
    /// credentials and packages folders and each credential file. The folder is opened by its
    /// path; a link inside it is not followed.
    #[cfg(target_os = "macos")]
    #[test]
    fn the_core_folder_keeps_no_access_list() {
        use crate::private_macos::add_entry;
        let base = temp("core-acl");
        let dir = base.join("core");
        ensure_config(&dir).unwrap();
        let creds = dir.join("credentials");
        let packages = dir.join("packages");
        fs::create_dir_all(&creds).unwrap();
        fs::create_dir_all(&packages).unwrap();
        let cred = creds.join("account.json");
        fs::write(&cred, b"{}").unwrap();
        // A link to a file outside, whose entry is not the app's to change.
        let outside = base.join("outside.json");
        fs::write(&outside, b"{}").unwrap();
        add_entry(&outside, "everyone allow read");
        std::os::unix::fs::symlink(&outside, creds.join("link.json")).unwrap();
        let inherit = "everyone allow list,file_inherit,directory_inherit";
        add_entry(&dir, inherit);
        add_entry(&dir.join("config.json"), "everyone allow write");
        add_entry(&creds, inherit);
        add_entry(&packages, inherit);
        add_entry(&cred, "everyone allow read");
        let has =
            |p: &Path| crate::private_macos::has_entries(&fs::File::open(p).unwrap()).unwrap();
        let all = [&dir, &dir.join("config.json"), &creds, &packages, &cred];
        for p in all {
            assert!(has(p), "{}", p.display());
        }
        ensure_config(&dir).unwrap();
        for p in all {
            assert!(!has(p), "{}", p.display());
        }
        assert!(has(&outside));
        fs::remove_dir_all(base).unwrap();
    }

    fn temp(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("pluxy-app-{name}-{}", random_key()));
        fs::create_dir_all(&d).unwrap();
        d
    }

    #[test]
    fn the_config_is_written_once_and_kept_private() {
        let dir = temp("config");
        let path = ensure_config(&dir).unwrap();
        let key = management_key(&path).unwrap();
        assert_eq!(key.len(), 64);
        let cfg: Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
        let listen = cfg["listen"].as_str().unwrap();
        assert!(
            listen.starts_with("127.0.0.1:") && !listen.ends_with(":0"),
            "{listen}"
        );
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                fs::metadata(&path).unwrap().permissions().mode() & 0o777,
                0o600
            );
        }
        // A second start keeps the person's config, edits and all.
        fs::write(
            &path,
            br#"{"listen":"127.0.0.1:1","management_key":"mine"}"#,
        )
        .unwrap();
        ensure_config(&dir).unwrap();
        assert_eq!(management_key(&path).unwrap(), "mine");
        fs::write(&path, br#"{"listen":"127.0.0.1:1"}"#).unwrap();
        assert!(management_key(&path)
            .unwrap_err()
            .contains("no management_key"));
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn the_core_program_is_never_looked_up_on_path() {
        assert!(core_program(Some("pluxy"))
            .unwrap_err()
            .contains("full path"));
        // A full path starts at the root, or at a drive on Windows.
        #[cfg(windows)]
        let full = r"C:\opt\pluxy\pluxy";
        #[cfg(not(windows))]
        let full = "/opt/pluxy/pluxy";
        assert_eq!(core_program(Some(full)).unwrap(), PathBuf::from(full));
        // Beside the test program there is no pluxy.
        assert!(core_program(None).unwrap_err().contains("no core program"));
    }

    /// A script line saying the core listens on the socket the app will give it.
    #[cfg(unix)]
    fn listening(dir: &Path) -> String {
        let addr = listen_addr(&ensure_config(dir).unwrap()).unwrap();
        format!("echo 'pluxy: listening on http://{addr}' >&2")
    }

    #[cfg(unix)]
    fn script(dir: &Path, body: &str) -> PathBuf {
        let script = dir.join("core.sh");
        fs::write(&script, format!("#!/bin/sh\n{body}\n")).unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(&script, fs::Permissions::from_mode(0o755)).unwrap();
        }
        script
    }

    /// Commands start the core from worker threads that end when idle; the core must outlive
    /// the thread that started it (on Linux the parent-death signal follows that thread).
    #[test]
    #[cfg(unix)] // a #! script stands in for the core; Windows runs programs only
    fn the_core_outlives_the_thread_that_started_it() {
        let dir = temp("thread");
        let program = script(&dir, &format!("{}\nexec sleep 30", listening(&dir)));
        let d = dir.clone();
        let mut core = std::thread::spawn(move || {
            ManagedCore::start(File::open(&program).unwrap(), &program, &d, None).unwrap()
        })
        .join()
        .unwrap();
        std::thread::sleep(Duration::from_millis(500));
        assert_eq!(
            core.exited(),
            None,
            "the core stopped when its thread ended"
        );
        core.stop();
        assert!(core.exited().is_some());
        fs::remove_dir_all(dir).unwrap();
    }

    /// The exit is seen as it happens, from the pidfd, not by the 100 ms reaping poll: with the
    /// child's lock held, nothing can reap it, so only the pidfd can set gone.
    #[cfg(target_os = "linux")]
    #[test]
    fn a_core_exit_is_seen_before_it_is_reaped() {
        let dir = temp("pidfd");
        let program = script(&dir, &format!("{}\nexec sleep 30", listening(&dir)));
        let core = ManagedCore::start(File::open(&program).unwrap(), &program, &dir, None).unwrap();
        let gone = core.gone();
        // Checked after the lock is let go, so a failure does not poison it for stop().
        let (killed, seen, reaped) = {
            let _held = core.child.lock().unwrap();
            let killed = unsafe { libc::kill(core.pid() as libc::pid_t, libc::SIGKILL) } == 0;
            let deadline = Instant::now() + Duration::from_secs(2);
            while !gone.load(Ordering::SeqCst) && Instant::now() < deadline {
                std::thread::sleep(Duration::from_millis(10));
            }
            let reaped = core.status.lock().unwrap().is_some();
            (killed, gone.load(Ordering::SeqCst), reaped)
        };
        assert!(killed, "the core could not be killed");
        assert!(seen, "the core's exit was not seen while it was unreaped");
        assert!(!reaped, "the core was reaped while its lock was held");
        assert!(
            core.wait(Duration::from_secs(2)),
            "the core was never reaped"
        );
        fs::remove_dir_all(dir).unwrap();
    }

    /// The real core (`next/cmd/pluxy`), built once by `go build` into a folder of its own.
    pub(crate) fn real_core() -> PathBuf {
        static BUILT: std::sync::OnceLock<PathBuf> = std::sync::OnceLock::new();
        BUILT
            .get_or_init(|| {
                let dir = temp("real-core");
                let program = dir.join(if cfg!(windows) { "pluxy.exe" } else { "pluxy" });
                let mut go = std::process::Command::new("go");
                go.args(["build", "-o"])
                    .arg(&program)
                    .arg("./cmd/pluxy")
                    .current_dir(Path::new(env!("CARGO_MANIFEST_DIR")).join("../.."));
                #[cfg(windows)]
                let built = crate::owned::windows::std_spawn(&mut go);
                #[cfg(not(windows))]
                let built = go.spawn();
                let status = built
                    .and_then(|mut c| c.wait())
                    .expect("go, to build the core");
                assert!(status.success(), "go build ./cmd/pluxy: {status}");
                program
            })
            .clone()
    }

    /// The real core starts from the file the app opened, listens where its config says, is the
    /// only thing listening on that port before the key goes there, and stops when asked: on
    /// Unix it drains and exits cleanly. (On Windows it would get CTRL_BREAK only from an app
    /// with no console of its own; this test program has one, so it is ended at once there.)
    #[test]
    fn the_real_core_starts_owns_its_port_and_stops() {
        let program = real_core();
        let dir = temp("real");
        let mut core =
            ManagedCore::start(File::open(&program).unwrap(), &program, &dir, None).unwrap();
        let base = core.base().to_string();
        owns_listener(&core, &base).unwrap();
        let port = url::Url::parse(&base).unwrap().port().unwrap();
        // Another program listening on the same port, on another loopback address: the key must
        // not be sent there.
        #[cfg(not(target_os = "macos"))]
        {
            let other = TcpListener::bind(("127.0.0.2", port)).unwrap();
            let err = owns_listener(&core, &base).unwrap_err();
            assert!(err.contains("not listening"), "{err}");
            drop(other);
            owns_listener(&core, &base).unwrap();
        }
        // macOS checks that base is the socket the app gave the core.
        #[cfg(target_os = "macos")]
        {
            let elsewhere = format!("http://127.0.0.1:{}", if port == 1 { 2 } else { port - 1 });
            assert!(owns_listener(&core, &elsewhere).is_err());
            assert!(owns_listener(&core, &format!("http://[::1]:{port}")).is_err());
        }
        // On Windows, where the app holds no copy of the core's socket, a client for the core
        // checks the core's own process accepted each connection: it reaches the core, and is
        // refused when another process (this test) is named as the core.
        #[cfg(windows)]
        {
            use crate::client::Client;
            let r =
                Client::for_core(&base, &core)
                    .unwrap()
                    .call(core.key(), "GET", "/status", None);
            assert_eq!(r.status, 200, "{r:?}");
            let this = crate::owned::Pin::this_process_as(std::process::id());
            let r = Client::with_pin(&base, this).call(core.key(), "GET", "/status", None);
            assert_eq!(r.status, 0, "{r:?}");
        }
        core.stop();
        let why = core.exited().expect("the core stopped");
        assert!(cfg!(windows) || why.contains("exit status: 0"), "{why}");
        assert!(
            owns_listener(&core, &base).is_err(),
            "a stopped core still owns {base}"
        );
        fs::remove_dir_all(dir).unwrap();
    }

    /// On Windows, where the app holds no copy of the core's socket: another program (this
    /// test) listening all along on the wildcard address over the core's port, as Windows lets
    /// another account do, gets the core's connections once the core has ended. A client for
    /// the core, made while it ran and kept past its `ManagedCore`, sends that program nothing.
    #[cfg(windows)]
    #[test]
    fn a_core_client_sends_nothing_to_a_listener_beside_the_core_once_it_ends() {
        use std::io::Read;
        let program = real_core();
        let dir = temp("beside");
        let mut core =
            ManagedCore::start(File::open(&program).unwrap(), &program, &dir, None).unwrap();
        let base = core.base().to_string();
        let port = url::Url::parse(&base).unwrap().port().unwrap();
        let key = core.key().to_string();
        let client = crate::client::Client::for_core(&base, &core).unwrap();
        let r = client.call(&key, "GET", "/status", None);
        assert_eq!(r.status, 200, "{r:?}");
        let other = TcpListener::bind(("0.0.0.0", port))
            .expect("another listener on the wildcard address beside the core's");
        let r = client.call(&key, "GET", "/status", None);
        assert_eq!(
            r.status, 200,
            "with the wildcard listening beside the core: {r:?}"
        );
        // A new client, so a new connection: the running core still takes it.
        let fresh = crate::client::Client::for_core(&base, &core).unwrap();
        let r = fresh.call(&key, "GET", "/status", None);
        assert_eq!(
            r.status, 200,
            "a new connection with the wildcard listening beside the core: {r:?}"
        );
        drop(fresh);
        core.stop();
        assert!(core.exited().is_some(), "the core stopped");
        // The client's own handle on the core's process keeps its process ID from now on.
        drop(core);
        other.set_nonblocking(true).unwrap();
        let server = std::thread::spawn(move || {
            let deadline = Instant::now() + Duration::from_secs(15);
            let mut s = loop {
                match other.accept() {
                    Ok((s, _)) => break s,
                    Err(e)
                        if e.kind() == io::ErrorKind::WouldBlock && Instant::now() < deadline =>
                    {
                        std::thread::sleep(Duration::from_millis(20));
                    }
                    Err(e) => return Err(format!("no connection reached the wildcard: {e}")),
                }
            };
            s.set_nonblocking(false).unwrap();
            s.set_read_timeout(Some(Duration::from_secs(5))).unwrap();
            let mut got = Vec::new();
            let _ = s.read_to_end(&mut got);
            Ok(got.len())
        });
        let r = client.call(&key, "GET", "/status", None);
        let got = server.join().unwrap();
        assert_eq!(
            got,
            Ok(0),
            "what the other program read, the call answered {r:?}"
        );
        assert_eq!(r.status, 0, "{r:?}");
        let why = r.error.unwrap().message;
        assert!(
            why.starts_with("nothing sent to the app's core: ")
                && why.contains(&format!("was accepted by process {}", std::process::id())),
            "{why}"
        );
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    #[cfg(unix)] // a #! script stands in for the core; Windows runs programs only
    fn a_core_that_exits_at_start_says_why() {
        let dir = temp("exit");
        let script = dir.join("core.sh");
        fs::write(
            &script,
            "#!/bin/sh\necho 'pluxy: config: listen is required' >&2\nexit 3\n",
        )
        .unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(&script, fs::Permissions::from_mode(0o755)).unwrap();
        }
        let err = ManagedCore::start(File::open(&script).unwrap(), &script, &dir, None)
            .err()
            .unwrap();
        assert!(err.contains("listen is required"), "{err}");
        fs::remove_dir_all(dir).unwrap();
    }

    /// On macOS the app's own core never runs below a folder the `admin` group may write
    /// (`owned::Writers::Nobody`): refused before it starts. The core here would otherwise run
    /// and exit saying why.
    #[test]
    #[cfg(target_os = "macos")]
    fn on_macos_a_core_below_a_folder_the_admin_group_may_write_is_refused() {
        use std::os::unix::fs::PermissionsExt;
        let dir = temp("admin");
        let bin = dir.join("bin");
        fs::create_dir(&bin).unwrap();
        let out = std::process::Command::new("/usr/bin/chgrp")
            .arg("admin")
            .arg(&bin)
            .output()
            .unwrap();
        assert!(
            out.status.success(),
            "chgrp admin (the test's user must be in admin): {}",
            String::from_utf8_lossy(&out.stderr)
        );
        fs::set_permissions(&bin, fs::Permissions::from_mode(0o775)).unwrap();
        let script = bin.join("core.sh");
        fs::write(
            &script,
            "#!/bin/sh\necho 'pluxy: config: listen is required' >&2\nexit 3\n",
        )
        .unwrap();
        fs::set_permissions(&script, fs::Permissions::from_mode(0o755)).unwrap();
        let err = ManagedCore::start(File::open(&script).unwrap(), &script, &dir, None)
            .err()
            .unwrap();
        fs::remove_dir_all(dir).unwrap();
        assert!(err.contains("may be changed by other users"), "{err}");
    }

    #[cfg(unix)]
    fn busy() -> io::Error {
        io::Error::from_raw_os_error(libc::ETXTBSY)
    }

    #[test]
    #[cfg(unix)]
    fn a_start_that_finds_the_file_busy_is_tried_again() {
        let mut calls = 0;
        let r = start_when_free(Duration::ZERO, || {
            calls += 1;
            if calls < 3 {
                Err(busy())
            } else {
                Ok(calls)
            }
        });
        assert_eq!(r.unwrap(), 3);
        assert_eq!(calls, 3);
    }

    #[test]
    #[cfg(unix)]
    fn a_start_that_fails_any_other_way_is_not_tried_again() {
        let mut calls = 0;
        let r: io::Result<()> = start_when_free(Duration::ZERO, || {
            calls += 1;
            Err(io::Error::from_raw_os_error(libc::ENOENT))
        });
        assert_eq!(r.unwrap_err().raw_os_error(), Some(libc::ENOENT));
        assert_eq!(calls, 1);
    }

    #[test]
    #[cfg(unix)]
    fn a_file_that_stays_busy_fails_with_its_last_error_after_the_tries() {
        let mut calls = 0;
        let r: io::Result<()> = start_when_free(Duration::ZERO, || {
            calls += 1;
            Err(busy())
        });
        assert_eq!(r.unwrap_err().raw_os_error(), Some(libc::ETXTBSY));
        assert_eq!(calls, BUSY_TRIES);
    }

    /// The real start: a program some other handle still has open for writing cannot be run
    /// yet, and runs once that handle is closed.
    #[test]
    #[cfg(target_os = "linux")] // the core starts from /proc/self/fd, which only Linux has
    fn a_core_still_open_for_writing_starts_once_it_is_closed() {
        let dir = temp("busy");
        let program = script(&dir, &format!("{}\nexec sleep 30", listening(&dir)));
        let writer = fs::OpenOptions::new().append(true).open(&program).unwrap();
        let closer = std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(200));
            drop(writer);
        });
        let mut core =
            ManagedCore::start(File::open(&program).unwrap(), &program, &dir, None).unwrap();
        closer.join().unwrap();
        core.stop();
        assert!(core.exited().is_some());
        fs::remove_dir_all(dir).unwrap();
    }
}
