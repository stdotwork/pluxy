//! The app's own core, in managed mode: a core program the app starts with a config it keeps in
//! its data folder, supervises (only that process) and stops when it quits or the person picks
//! another mode.

use serde_json::{json, Value};
use std::collections::VecDeque;
use std::fs::{self, File};
use std::io::{self, BufRead, BufReader, Write};
use std::net::{SocketAddr, TcpListener};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, ExitStatus, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc;
use std::sync::{Arc, Mutex, Weak};
use std::time::{Duration, Instant};

const START_TIMEOUT: Duration = Duration::from_secs(30);
const STOP_TIMEOUT: Duration = Duration::from_secs(10);
/// How many of core's last log lines the app keeps, to say why it stopped.
const TAIL: usize = 20;

/// 32 random bytes, hex-encoded.
pub fn random_key() -> String {
    let mut b = [0u8; 32];
    getrandom::fill(&mut b).expect("the system's random source");
    b.iter().map(|x| format!("{x:02x}")).collect()
}

/// Writes `path` readable only by this user, replacing it in one step.
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
        let mut opts = fs::OpenOptions::new();
        opts.write(true).create_new(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            opts.mode(0o600).custom_flags(libc::O_NOFOLLOW);
        }
        match opts.open(&tmp) {
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
        fs::rename(&tmp, path)?;
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

/// The core config the app keeps, `<dir>/config.json`. The app writes it once, the first time,
/// with a free loopback port that then stays the core's, fresh keys and folders under dir; after
/// that it is the person's to edit (core reloads it), and the app only reads the management key.
pub fn ensure_config(dir: &Path) -> Result<PathBuf, String> {
    let path = dir.join("config.json");
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
    /// its caller checked, not from its path again. On Unix the app binds core's `listen` address
    /// itself and hands core the socket (`-listen-fd`), keeping its own copy; core must say it
    /// listens there. Core's log goes to the app's stderr, each line prefixed `core: `. on_exit
    /// runs once core has exited.
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
        let fd = {
            use std::os::fd::AsRawFd;
            program.as_raw_fd()
        };
        let mut cmd = Command::new(format!("/proc/self/fd/{fd}"));
        #[cfg(unix)]
        {
            use std::os::unix::process::CommandExt;
            cmd.arg0(name);
            // The child keeps the open program across exec, which a #! script's interpreter
            // then reads (as /dev/fd/N); the app's own copy closes on exec as usual.
            unsafe {
                cmd.pre_exec(move || {
                    if libc::fcntl(fd, libc::F_SETFD, 0) != 0 {
                        return Err(io::Error::last_os_error());
                    }
                    Ok(())
                });
            }
        }
        cmd.arg("-config").arg(&cfg);
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
        cmd.current_dir(dir)
            .stdin(Stdio::null())
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
            let spawned = cmd.spawn();
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
            #[cfg(unix)]
            unsafe {
                libc::kill(c.id() as libc::pid_t, libc::SIGTERM);
            }
            #[cfg(not(unix))]
            let _ = c.kill();
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

#[cfg(not(target_os = "linux"))]
pub fn owns_listener(_core: &ManagedCore, _base: &str) -> Result<(), String> {
    Err("checking that the app's own core is what listens works on Linux only for now".into())
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

    use super::*;

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
        assert_eq!(
            core_program(Some("/opt/pluxy/pluxy")).unwrap(),
            PathBuf::from("/opt/pluxy/pluxy")
        );
        // Beside the test program there is no pluxy.
        assert!(core_program(None).unwrap_err().contains("no core program"));
    }

    /// A script line saying the core listens on the socket the app will give it.
    fn listening(dir: &Path) -> String {
        let addr = listen_addr(&ensure_config(dir).unwrap()).unwrap();
        format!("echo 'pluxy: listening on http://{addr}' >&2")
    }

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

    #[test]
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
}
