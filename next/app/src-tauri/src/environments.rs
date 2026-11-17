//! Where a native tool runs: on this computer (`Environment::Local`, what every package has), or,
//! on Windows, inside a WSL distribution as one of its users (`Environment::Wsl`). A package whose
//! manifest says `"app": {"environments": ["wsl"]}` may run its Linux program there: the host
//! starts it through Windows' own `wsl.exe` (`wsl.exe -d <distro> -u <user> --cd <folder> --exec
//! <program>`), from the release's Linux archive installed for WSL, on the same contract pipes.
//!
//! The environment is chosen by the app's own window only and carried by the host (the router's
//! record of a call), never as an operation's input: a page or another package cannot choose it.
//! The host knows an environment only from its own record of it, made when the person added it in
//! the window and the `pluxy/wsl` tool checked it (`check`): the distribution, the user, their
//! home, the processor and the Windows drives mounted there. `environments.json` (owner-only,
//! beside `launcher.json`) keeps those records.
//!
//! What runs per instance, a package in one environment (`Instance`), is separate from every other
//! instance of the same package: its process, its data folder, its backups and journals in the
//! vault (`Instance::vault_suffix`), and its calls of unknown outcome.

use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha2::{Digest, Sha256};
use std::fs;
use std::path::{Path, PathBuf};
use std::time::Duration;

pub const FILE: &str = "environments.json";
/// The tool that lists, checks and prepares launches in WSL distributions.
pub const WSL_PACKAGE: &str = "pluxy/wsl";
/// What a package's manifest names in `app.environments` to run in WSL.
pub const WSL: &str = "wsl";
/// How long a tool in WSL may take to say hello and describe itself: a stopped WSL 2 distribution
/// starts its virtual machine first.
pub const WSL_STARTUP: Duration = Duration::from_secs(60);
const VERSION: u32 = 1;
/// Environments kept.
const MAX_RECORDS: usize = 64;
const MAX_DISTRO: usize = 64;
const MAX_USER: usize = 32;

/// Where a tool runs.
#[derive(Clone, Debug, Default, PartialEq, Eq, Hash, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Environment {
    /// This computer.
    #[default]
    Local,
    /// A WSL distribution, as one of its users.
    Wsl { distro: String, user: String },
}

impl Environment {
    /// The environment the window names: none (or null) for this computer, or exactly
    /// `{"wsl": {"distro", "user"}}` with names `pluxy/wsl` accepts.
    pub fn from_window(v: Option<&Value>) -> Result<Environment, String> {
        let bad = || "an environment is {\"wsl\": {\"distro\", \"user\"}}".to_string();
        let Some(v) = v.filter(|v| !v.is_null()) else {
            return Ok(Environment::Local);
        };
        let outer = v.as_object().filter(|m| m.len() == 1).ok_or_else(bad)?;
        let inner = outer
            .get(WSL)
            .and_then(Value::as_object)
            .filter(|m| m.len() == 2)
            .ok_or_else(bad)?;
        let (Some(distro), Some(user)) = (
            inner.get("distro").and_then(Value::as_str),
            inner.get("user").and_then(Value::as_str),
        ) else {
            return Err(bad());
        };
        Environment::wsl(distro, user)
    }

    /// The WSL environment of distro and user, when both are names pluxy uses.
    pub fn wsl(distro: &str, user: &str) -> Result<Environment, String> {
        if !distro_ok(distro) {
            return Err(format!(
                "{distro:?} is not a WSL distribution name pluxy uses"
            ));
        }
        if !user_ok(user) {
            return Err(format!("{user:?} is not a user name pluxy uses"));
        }
        Ok(Environment::Wsl {
            distro: distro.into(),
            user: user.into(),
        })
    }

    pub fn is_local(&self) -> bool {
        matches!(self, Environment::Local)
    }

    /// `PLUXY_ENVIRONMENT`'s value: `wsl:<distro>:<user>`; None for this computer.
    pub fn label(&self) -> Option<String> {
        match self {
            Environment::Local => None,
            Environment::Wsl { distro, user } => Some(format!("wsl:{distro}:{user}")),
        }
    }

    /// The window's form of it: null, or `{"wsl": {"distro", "user"}}`.
    pub fn to_window(&self) -> Value {
        match self {
            Environment::Local => Value::Null,
            Environment::Wsl { distro, user } => {
                serde_json::json!({WSL: {"distro": distro, "user": user}})
            }
        }
    }
}

/// A distribution name pluxy passes to `wsl.exe -d`, as `pluxy/wsl` checks it: not empty, `.` or
/// `..`; no `\ / : * ? " < > |`, control character or trailing dot or space; at most 64
/// characters; not starting with `-`.
pub fn distro_ok(name: &str) -> bool {
    !name.is_empty()
        && name != "."
        && name != ".."
        && !name.ends_with(['.', ' '])
        && !name.starts_with('-')
        && !name.contains(['\\', '/', ':', '*', '?', '"', '<', '>', '|'])
        && !name.chars().any(char::is_control)
        && name.chars().count() <= MAX_DISTRO
}

/// A POSIX portable user name: `[a-z_][a-z0-9_-]*[$]?`, at most 32 bytes.
pub fn user_ok(name: &str) -> bool {
    let b = name.as_bytes();
    let body = b.strip_suffix(b"$").unwrap_or(b);
    !body.is_empty()
        && b.len() <= MAX_USER
        && (body[0].is_ascii_lowercase() || body[0] == b'_')
        && body
            .iter()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || *c == b'_' || *c == b'-')
}

/// A package in an environment: what the program host runs and keys its state by.
#[derive(Clone, Debug, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub struct Instance {
    pub package: String,
    pub environment: Environment,
}

impl Instance {
    /// Package on this computer.
    pub fn local(package: &str) -> Instance {
        Instance {
            package: package.into(),
            environment: Environment::Local,
        }
    }

    /// What its vault folder's name adds to its package's: nothing on this computer; in WSL, `@`
    /// and the first 32 hex digits of SHA-256 of `wsl\0<distro>\0<user>` (names never appear in a
    /// file name, and the name's length stays bounded).
    pub fn vault_suffix(&self) -> String {
        match &self.environment {
            Environment::Local => String::new(),
            Environment::Wsl { distro, user } => {
                let d = Sha256::digest(format!("wsl\0{distro}\0{user}").as_bytes());
                let hex: String = d.iter().take(16).map(|b| format!("{b:02x}")).collect();
                format!("@{hex}")
            }
        }
    }

    /// The program host's key for it: the package ID on this computer (as before environments),
    /// the package ID and its vault suffix in WSL.
    pub fn key(&self) -> String {
        format!("{}{}", self.package, self.vault_suffix())
    }

    /// How messages name it: the package, and `in WSL <distro>/<user>` there.
    pub fn name(&self) -> String {
        match &self.environment {
            Environment::Local => self.package.clone(),
            Environment::Wsl { distro, user } => {
                format!("{} in WSL {distro}/{user}", self.package)
            }
        }
    }
}

/// A Windows drive mounted in a distribution, as `pluxy/wsl`'s `check` saw it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Drive {
    /// Its letter, lower case.
    pub letter: String,
    /// Where it is mounted, such as `/mnt/c`.
    pub path: String,
    /// Whether programs on it may run (not `noexec`).
    pub exec: bool,
}

/// The host's record of a WSL environment: what `pluxy/wsl`'s `check` gave when the person added
/// it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Record {
    pub distro: String,
    pub user: String,
    pub uid: u64,
    /// The user's home in the distribution.
    pub home: String,
    /// `amd64` or `arm64`: which Linux archive runs there.
    pub arch: String,
    /// 1 or 2, when known: shown only.
    #[serde(default)]
    pub wsl: Option<u8>,
    /// The folder WSL mounts Windows drives in, with a trailing `/`; None when none is mounted.
    #[serde(default)]
    pub automount: Option<String>,
    #[serde(default)]
    pub drives: Vec<Drive>,
    /// When it was checked (Unix seconds).
    pub checked_at: u64,
}

impl Record {
    /// The record `check`'s output gives for distro and user, checked at `now`: it must name that
    /// distribution and user, an absolute home, `amd64` or `arm64`, and drives with one-letter
    /// names mounted at absolute paths.
    pub fn from_check(distro: &str, user: &str, v: &Value, now: u64) -> Result<Record, String> {
        let bad = |what: &str| format!("pluxy/wsl's check of {distro}/{user} gave {what}");
        let env = Environment::wsl(distro, user)?;
        let Environment::Wsl { distro, user } = env else {
            unreachable!()
        };
        if v["distro"].as_str() != Some(distro.as_str())
            || v["user"].as_str() != Some(user.as_str())
        {
            return Err(bad("another distribution or user"));
        }
        let home = v["home"]
            .as_str()
            .filter(|h| h.starts_with('/') && !h.contains('\0'))
            .ok_or_else(|| bad("no absolute home"))?;
        let arch = v["arch"]
            .as_str()
            .filter(|a| matches!(*a, "amd64" | "arm64"))
            .ok_or_else(|| bad("a processor pluxy has no Linux build for"))?;
        let uid = v["uid"].as_u64().ok_or_else(|| bad("no user number"))?;
        let wsl = v["wsl"]
            .as_u64()
            .filter(|n| matches!(n, 1 | 2))
            .map(|n| n as u8);
        let automount = match &v["automount"] {
            Value::Null => None,
            Value::String(s) if s.starts_with('/') => Some(s.clone()),
            _ => return Err(bad("a mount folder that is not absolute")),
        };
        let mut drives = Vec::new();
        for d in v["drives"].as_array().ok_or_else(|| bad("no drive list"))? {
            let letter = d["letter"]
                .as_str()
                .filter(|l| l.len() == 1 && l.as_bytes()[0].is_ascii_lowercase())
                .ok_or_else(|| bad("a drive with no letter"))?;
            let path = d["path"]
                .as_str()
                .filter(|p| p.starts_with('/') && !p.contains('\0'))
                .ok_or_else(|| bad("a drive mounted at no absolute folder"))?;
            let exec = d["exec"]
                .as_bool()
                .ok_or_else(|| bad("a drive with no exec"))?;
            drives.push(Drive {
                letter: letter.into(),
                path: path.into(),
                exec,
            });
        }
        Ok(Record {
            distro,
            user,
            uid,
            home: home.into(),
            arch: arch.into(),
            wsl,
            automount,
            drives,
            checked_at: now,
        })
    }

    pub fn environment(&self) -> Environment {
        Environment::Wsl {
            distro: self.distro.clone(),
            user: self.user.clone(),
        }
    }

    /// The platform of the Linux archive that runs here: `linux/<arch>`.
    pub fn platform(&self) -> String {
        format!("linux/{}", self.arch)
    }

    /// The line native confirmations show above the review of an operation in it. The names are
    /// quoted (neither holds a `"`), so a distribution named like the rest of a line reads as one
    /// name.
    pub fn confirmation_line(&self) -> String {
        format!(
            "Runs in WSL: distribution \"{}\", user \"{}\" ({}).",
            self.distro, self.user, self.home
        )
    }

    /// Package's data folder in the distribution, `<home>/.local/state/pluxy/tools/<publisher>/
    /// <name>`. The tool makes it at its first write (owner-only); the host never writes in a
    /// distribution.
    pub fn data_dir(&self, package: &str) -> Result<String, String> {
        let (publisher, name) = package
            .split_once('/')
            .filter(|(p, n)| !p.is_empty() && !n.is_empty() && !n.contains('/'))
            .ok_or_else(|| format!("{package} is not a package ID"))?;
        Ok(format!(
            "{}/.local/state/pluxy/tools/{publisher}/{name}",
            self.home.trim_end_matches('/')
        ))
    }

    /// The path in the distribution of a Windows file or folder on a drive it mounts: `C:\a\b`
    /// (or `\\?\C:\a\b`) is `<mount of c>/a/b`. Refused for any other kind of path, a drive not
    /// mounted there, or one mounted `noexec` (the file is a program to run).
    pub fn linux_path(&self, windows: &Path) -> Result<String, String> {
        let text = windows
            .to_str()
            .ok_or_else(|| format!("{} is not Unicode", windows.display()))?;
        let text = text.strip_prefix(r"\\?\").unwrap_or(text);
        let b = text.as_bytes();
        if b.len() < 3 || !b[0].is_ascii_alphabetic() || b[1] != b':' || b[2] != b'\\' {
            return Err(format!("{text} is not on a Windows drive WSL can mount"));
        }
        let letter = (b[0] as char).to_ascii_lowercase().to_string();
        let Some(drive) = self.drives.iter().find(|d| d.letter == letter) else {
            return Err(format!(
                "This distribution does not mount Windows drive {}:, which pluxy needs to run its \
                 tools there (automount in /etc/wsl.conf).",
                letter.to_ascii_uppercase()
            ));
        };
        if !drive.exec {
            return Err(format!(
                "This distribution mounts Windows drive {}: without permission to run programs \
                 (noexec), which pluxy needs to run its tools there.",
                letter.to_ascii_uppercase()
            ));
        }
        let mut out = drive.path.trim_end_matches('/').to_string();
        for part in text[3..].split('\\').filter(|p| !p.is_empty()) {
            if part == "." || part == ".." || part.contains(['/', '\0']) {
                return Err(format!("{text} is not a plain path"));
            }
            out.push('/');
            out.push_str(part);
        }
        Ok(out)
    }

    /// `wsl.exe`'s arguments (after its own name) for a tool: its Linux program, run in its
    /// folder, in this distribution as this user, with no shell.
    pub fn tool_args(&self, program: &str, dir: &str) -> Vec<String> {
        [
            "-d",
            &self.distro,
            "-u",
            &self.user,
            "--cd",
            dir,
            "--exec",
            program,
        ]
        .iter()
        .map(|s| s.to_string())
        .collect()
    }

    /// The Windows environment of `wsl.exe` for package's tool, at its Linux archive's digest:
    /// `SystemRoot` (WSL 2 refuses a block without it), the contract's values, `PLUXY_ENVIRONMENT`,
    /// `PLUXY_ENVIRONMENT_ACCOUNT` (the record's user ID and home, `<uid>:<home>`, which the tool
    /// checks too) and the data folder, each passed on into the distribution unchanged by
    /// `WSLENV` (`/u`, Windows to Linux only). Nothing else: no `PATH`, so WSL adds no Windows
    /// folders to the tool's Linux `PATH`; WSL itself sets `HOME`, `USER`, `PATH` and its own
    /// variables there.
    pub fn tool_env(
        &self,
        package: &str,
        sha256: &str,
        contract: &str,
        system_root: Option<&Path>,
    ) -> Result<Vec<(String, String)>, String> {
        let crossing = [
            ("PLUXY_APP_CONTRACT_VERSIONS", contract.to_string()),
            ("PLUXY_EXTENSION_ID", package.to_string()),
            ("PLUXY_APP_ARTIFACT_SHA256", sha256.to_string()),
            ("PLUXY_TOOL_DATA_DIR", self.data_dir(package)?),
            (
                "PLUXY_ENVIRONMENT",
                self.environment().label().unwrap_or_default(),
            ),
            (
                "PLUXY_ENVIRONMENT_ACCOUNT",
                format!("{}:{}", self.uid, self.home),
            ),
        ];
        let mut out: Vec<(String, String)> = Vec::new();
        if let Some(root) = system_root {
            let root = root
                .to_str()
                .ok_or("SystemRoot is not Unicode")?
                .to_string();
            out.push(("SystemRoot".into(), root));
        }
        let wslenv: Vec<String> = crossing.iter().map(|(k, _)| format!("{k}/u")).collect();
        out.extend(crossing.into_iter().map(|(k, v)| (k.to_string(), v)));
        out.push(("WSLENV".into(), wslenv.join(":")));
        Ok(out)
    }
}

#[derive(Default, Serialize, Deserialize)]
struct Store {
    version: u32,
    #[serde(default)]
    records: Vec<Record>,
}

/// The host's WSL environments, kept in `environments.json`.
pub struct Environments {
    file: PathBuf,
    store: Store,
}

impl Environments {
    /// The records in file; none when it is missing. A file that cannot be read is moved aside
    /// and the records start empty: the person adds them again.
    pub fn open(file: PathBuf) -> Environments {
        let store = match fs::read(&file) {
            Ok(b) => match serde_json::from_slice::<Store>(&b) {
                Ok(s) if s.version == VERSION => s,
                r => {
                    let why = match r {
                        Ok(s) => format!("version {}", s.version),
                        Err(e) => e.to_string(),
                    };
                    let moved = file.with_extension(format!("json.unread-{}", now()));
                    let moved = fs::rename(&file, &moved).map(|_| moved);
                    eprintln!(
                        "pluxy-app: the environments file was not read ({why}); moved aside: \
                         {moved:?}"
                    );
                    Store::default()
                }
            },
            Err(_) => Store::default(),
        };
        Environments { file, store }
    }

    /// Every record, in the order they were added.
    pub fn all(&self) -> &[Record] {
        &self.store.records
    }

    /// The record of environment; None for this computer or one the host does not know.
    pub fn get(&self, environment: &Environment) -> Option<&Record> {
        let Environment::Wsl { distro, user } = environment else {
            return None;
        };
        self.store
            .records
            .iter()
            .find(|r| &r.distro == distro && &r.user == user)
    }

    /// Keeps r, replacing the record of its distribution and user, and saves.
    pub fn put(&mut self, r: Record) -> Result<(), String> {
        let mut records = self.store.records.clone();
        let full = records.len() >= MAX_RECORDS;
        match records
            .iter_mut()
            .find(|x| x.distro == r.distro && x.user == r.user)
        {
            Some(x) => *x = r,
            None if full => {
                return Err(format!(
                    "pluxy keeps at most {MAX_RECORDS} WSL environments"
                ))
            }
            None => records.push(r),
        }
        self.save(records)
    }

    /// Forgets environment's record, and saves; whether there was one.
    pub fn remove(&mut self, environment: &Environment) -> Result<bool, String> {
        let Environment::Wsl { distro, user } = environment else {
            return Ok(false);
        };
        let mut records = self.store.records.clone();
        let before = records.len();
        records.retain(|r| &r.distro != distro || &r.user != user);
        if records.len() == before {
            return Ok(false);
        }
        self.save(records).map(|_| true)
    }

    fn save(&mut self, records: Vec<Record>) -> Result<(), String> {
        let store = Store {
            version: VERSION,
            records,
        };
        let dir = self
            .file
            .parent()
            .ok_or("the environments file has no folder")?;
        #[cfg(unix)]
        let made = {
            use std::os::unix::fs::DirBuilderExt;
            fs::DirBuilder::new()
                .recursive(true)
                .mode(0o700)
                .create(dir)
        };
        #[cfg(windows)]
        let made = crate::private_windows::create_private_dir_all(dir);
        made.map_err(|e| e.to_string())?;
        let bytes = serde_json::to_vec_pretty(&store).map_err(|e| e.to_string())?;
        crate::managed::write_private(&self.file, &bytes).map_err(|e| e.to_string())?;
        self.store = store;
        Ok(())
    }
}

/// Starts of a tool in WSL tried again after WSL itself refused them, and the pause before each.
pub const WSL_RETRIES: u32 = 2;
pub const WSL_RETRY_PAUSE: Duration = Duration::from_secs(2);

/// Whether a start's error is WSL 1's own refusal to start the program (`0xd0000225`: nothing
/// ran), which a second try usually gets past, as `pluxy/wsl` retries it.
pub fn wsl_refused_start(error: &str) -> bool {
    error.contains("0xd0000225")
}

/// What `wsl.exe` printed, on the tool's stdout instead of a frame or on its stderr before hello,
/// as text: UTF-16 (its usual form) when the bytes hold a NUL, else UTF-8; white space trimmed,
/// at most 512 characters.
pub fn wsl_said(bytes: &[u8]) -> String {
    let text = if bytes.contains(&0) {
        let units: Vec<u16> = bytes
            .as_chunks::<2>()
            .0
            .iter()
            .map(|c| u16::from_le_bytes(*c))
            .collect();
        String::from_utf16_lossy(&units)
    } else {
        String::from_utf8_lossy(bytes).into_owned()
    };
    let text: String = text
        .trim_matches(|c: char| c.is_whitespace() || c == '\u{feff}' || c == '\0')
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ");
    text.chars().take(512).collect()
}

pub fn now() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

#[cfg(test)]
#[path = "environments_tests.rs"]
mod tests;
