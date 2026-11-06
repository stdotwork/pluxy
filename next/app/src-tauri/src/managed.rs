//! The app's own core, in managed mode: a core program the app starts with a config it keeps in
//! its data folder, supervises (only that process) and stops when it quits or the person picks
//! another mode.

use serde_json::{json, Value};
use std::collections::VecDeque;
use std::fs;
use std::io::{self, BufRead, BufReader, Write};
use std::net::TcpListener;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::mpsc;
use std::sync::{Arc, Mutex};
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
    let tmp = path.with_extension("tmp");
    let mut opts = fs::OpenOptions::new();
    opts.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    let mut f = opts.open(&tmp)?;
    f.write_all(data)?;
    f.sync_all()?;
    drop(f);
    fs::rename(&tmp, path)
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

/// A running core of the app's own.
pub struct ManagedCore {
    child: Child,
    base: String,
    key: String,
    tail: Arc<Mutex<VecDeque<String>>>,
}

impl ManagedCore {
    /// Starts program with the config in dir (made if missing) and waits until core says where it
    /// listens. Core's log goes to the app's stderr, each line prefixed `core: `.
    pub fn start(program: &Path, dir: &Path) -> Result<ManagedCore, String> {
        let cfg = ensure_config(dir)?;
        let key = management_key(&cfg)?;
        let mut cmd = Command::new(program);
        cmd.arg("-config")
            .arg(&cfg)
            .current_dir(dir)
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
        let mut child = cmd
            .spawn()
            .map_err(|e| format!("starting {}: {e}", program.display()))?;
        let stderr = child.stderr.take().expect("piped stderr");
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
        let deadline = Instant::now() + START_TIMEOUT;
        let base = loop {
            match rx.recv_timeout(Duration::from_millis(100)) {
                Ok(addr) => break addr,
                Err(mpsc::RecvTimeoutError::Timeout) if Instant::now() < deadline => {
                    if let Ok(Some(status)) = child.try_wait() {
                        return Err(Self::exit_message(status, &tail));
                    }
                }
                Err(_) => {
                    let _ = child.kill();
                    let status = child.wait();
                    return Err(match status {
                        Ok(s) if Instant::now() >= deadline => format!(
                            "core did not say where it listens within {}s ({s})",
                            START_TIMEOUT.as_secs()
                        ),
                        Ok(s) => Self::exit_message(s, &tail),
                        Err(e) => e.to_string(),
                    });
                }
            }
        };
        Ok(ManagedCore {
            child,
            base,
            key,
            tail,
        })
    }

    fn exit_message(status: std::process::ExitStatus, tail: &Mutex<VecDeque<String>>) -> String {
        let last = tail.lock().unwrap().back().cloned().unwrap_or_default();
        format!("core exited ({status}): {last}")
    }

    /// Where core listens, such as `http://127.0.0.1:8400`.
    pub fn base(&self) -> &str {
        &self.base
    }

    pub fn key(&self) -> &str {
        &self.key
    }

    pub fn pid(&self) -> u32 {
        self.child.id()
    }

    /// None while core runs; why it stopped once it has.
    pub fn exited(&mut self) -> Option<String> {
        match self.child.try_wait() {
            Ok(Some(status)) => Some(Self::exit_message(status, &self.tail)),
            Ok(None) => None,
            Err(e) => Some(e.to_string()),
        }
    }

    /// Asks core to stop (SIGTERM, so it drains), and kills it if it has not within 10 seconds.
    pub fn stop(&mut self) {
        if self.exited().is_some() {
            return;
        }
        #[cfg(unix)]
        unsafe {
            libc::kill(self.child.id() as libc::pid_t, libc::SIGTERM);
        }
        #[cfg(not(unix))]
        let _ = self.child.kill();
        let deadline = Instant::now() + STOP_TIMEOUT;
        while Instant::now() < deadline {
            if let Ok(Some(_)) = self.child.try_wait() {
                return;
            }
            std::thread::sleep(Duration::from_millis(50));
        }
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

impl Drop for ManagedCore {
    fn drop(&mut self) {
        self.stop();
    }
}

#[cfg(test)]
mod tests {
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
        let err = ManagedCore::start(&script, &dir).err().unwrap();
        assert!(err.contains("listen is required"), "{err}");
        fs::remove_dir_all(dir).unwrap();
    }
}
