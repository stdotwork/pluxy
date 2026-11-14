//! The app's own update, through the stock Tauri updater plugin: only for an AppImage this user
//! keeps, only with updater trust, and only to a newer version the release feed (`release.rs`)
//! offers with a signed artifact for this system. README.md ("Application update") has the rules.
//!
//! Before any updater plugin call the updater gate (`trust::updater_gate`) passes; the plugin is
//! registered at all only when it passed at startup (`lib.rs`). Before any download the
//! installation's provenance is checked: a .deb, an .rpm or an unknown format is refused, with
//! how to update instead, even when the feed has a signed update. The artifact is downloaded
//! here, read as the feed is, then the plugin checks its signature (bound to the announced
//! version) and installs it over the AppImage. Before that, the running version's AppImage is
//! kept, pinned by its digest: rolling back restores only such a copy, after a native
//! confirmation. Installing ends with the app quitting as it always does (its tools and its own
//! core stop), starting the new version and waiting for it to accept itself; without that, the
//! kept copy is put back and started again.
use crate::release::{self, Product, Release, UpdateArtifact};
use crate::{trusted, App, MAIN};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::ffi::OsString;
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::time::{Duration, Instant};
use tauri::{AppHandle, Emitter, EventTarget, Manager, Webview};

/// The updater's name for this system's artifact: Linux, x86_64, AppImage.
pub const TARGET: &str = "linux-x86_64-appimage";
/// How long a new version has to accept itself.
const ACCEPT_WITHIN: Duration = Duration::from_secs(90);
/// Kept copies of earlier versions, newest first; older ones are deleted.
const KEEP: usize = 3;
const EVENT: &str = "pluxy-app-update";

/// What the update is doing now.
#[derive(Clone, Debug, Default, Serialize)]
pub struct Task {
    /// idle, confirming (a rollback waits for its native confirmation), downloading, verifying,
    /// installing, restarting, failed or cancelled.
    pub phase: &'static str,
    pub version: Option<String>,
    pub downloaded: u64,
    pub total: Option<u64>,
    pub error: Option<String>,
}

impl Task {
    fn running(&self) -> bool {
        matches!(
            self.phase,
            "confirming" | "downloading" | "verifying" | "installing" | "restarting"
        )
    }
}

/// A version of the app and the SHA-256 of its AppImage.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Pinned {
    pub version: String,
    pub sha256: String,
}

/// An install waiting for its new version to accept itself (`pending.json`).
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Pending {
    format: u32,
    /// update or rollback.
    kind: String,
    from: Pinned,
    to: Pinned,
    appimage: PathBuf,
    nonce: String,
}

/// What the new version writes once it accepts itself (`accepted.json`).
#[derive(Serialize, Deserialize)]
struct Accepted {
    nonce: String,
    version: String,
}

/// The last install's outcome (`last.json`), as the page shows it.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Last {
    /// installed, rolled-back or failed.
    pub result: String,
    pub detail: String,
}

#[derive(Default, Serialize, Deserialize)]
struct History {
    /// Versions that ran here, newest first, each with a kept copy.
    kept: Vec<Pinned>,
}

/// The restart a finished install asked for.
pub struct Restart {
    dir: PathBuf,
    pending: Pending,
}

pub struct State {
    /// Why the updater plugin was not registered at startup; Ok when it was.
    plugin: Result<(), String>,
    task: Mutex<Task>,
    cancel: AtomicBool,
    /// The SHA-256 of the AppImage this process started from, hashed once at startup
    /// (`record_started_from`): only those bytes are kept as this version.
    started_from: std::sync::OnceLock<Result<String, String>>,
}

impl State {
    pub fn new(plugin: Result<(), String>) -> State {
        State {
            plugin,
            task: Mutex::new(Task {
                phase: "idle",
                ..Task::default()
            }),
            cancel: AtomicBool::new(false),
            started_from: std::sync::OnceLock::new(),
        }
    }
}

/// What the window shows: whether an update can be installed now and, if not, why; the task;
/// the last outcome; the versions it can go back to.
#[derive(Clone, Serialize)]
pub struct View {
    pub available: bool,
    pub reason: Option<String>,
    pub version: Option<String>,
    pub task: Task,
    pub last: Option<Last>,
    pub rollback: Vec<Pinned>,
}

fn sha256(bytes: &[u8]) -> String {
    crate::artifact::hex(&Sha256::digest(bytes))
}

fn dir(app: &AppHandle) -> Result<PathBuf, String> {
    app.path()
        .app_data_dir()
        .map(|d| d.join("updates"))
        .map_err(|e| e.to_string())
}

fn kept_path(dir: &Path, p: &Pinned) -> PathBuf {
    dir.join("kept")
        .join(format!("{}-{}.AppImage", p.version, p.sha256))
}

fn read_json<T: for<'a> Deserialize<'a>>(path: &Path) -> Option<T> {
    let bytes = crate::trust::read_bounded(path).ok()?;
    serde_json::from_slice(&bytes).ok()
}

fn write_json<T: Serialize>(path: &Path, v: &T) -> Result<(), String> {
    if let Some(parent) = path.parent() {
        private_dir(parent)?;
    }
    crate::managed::write_private(path, &serde_json::to_vec(v).map_err(|e| e.to_string())?)
        .map_err(|e| format!("{}: {e}", path.display()))
}

fn private_dir(path: &Path) -> Result<(), String> {
    use std::os::unix::fs::DirBuilderExt;
    fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(path)
        .map_err(|e| format!("{}: {e}", path.display()))
}

/// Whether bytes start as a type-2 AppImage: an ELF file with `AI\x02` at offset 8.
pub fn is_appimage(bytes: &[u8]) -> bool {
    bytes.len() >= 11 && bytes[..4] == *b"\x7fELF" && bytes[8..11] == *b"AI\x02"
}

/// Why this build has no updater, if it has none: the plugin was not registered at startup, or
/// the updater gate refuses now. Run before every use of the plugin.
fn gate(app: &AppHandle) -> Result<(), String> {
    let not = |e: String| format!("The updater is not configured: {e}");
    app.state::<App>().updater.plugin.clone().map_err(not)?;
    let home = std::env::var_os("HOME").unwrap_or_default();
    crate::trust::updater_gate(app.config(), Path::new(&home))
        .map(|_| ())
        .map_err(not)
}

/// How this copy of the app was installed, as far as updating it goes: the AppImage this user
/// keeps, or why it cannot be updated from here.
pub fn provenance(app: &AppHandle) -> Result<PathBuf, String> {
    use tauri::utils::config::BundleType;
    match tauri::utils::platform::bundle_type() {
        Some(BundleType::AppImage) => {}
        Some(BundleType::Deb) => {
            return Err("This copy of pluxy was installed from a .deb package. Update it with your \
                 package manager (for example `sudo apt install ./<new package>.deb`), not from here."
                .into())
        }
        Some(BundleType::Rpm) => {
            return Err("This copy of pluxy was installed from an .rpm package. Update it with your \
                 package manager (for example `sudo dnf upgrade` or `sudo rpm -U <new package>.rpm`), \
                 not from here."
                .into())
        }
        _ => {
            return Err("This copy of pluxy is not an AppImage, the only kind this updater can \
                 replace (its install format is unknown). Install the new version the way you \
                 installed this one."
                .into())
        }
    }
    let env = app.env();
    let appimage = env
        .appimage
        .map(PathBuf::from)
        .filter(|p| p.is_absolute())
        .ok_or("This AppImage does not say where it is (APPIMAGE is not set). Install the new version by hand.")?;
    check_owned(&appimage, false)?;
    let parent = appimage.parent().ok_or("The AppImage has no folder.")?;
    check_owned(parent, true)?;
    let mut head = [0u8; 16];
    {
        use std::io::Read;
        fs::File::open(&appimage)
            .and_then(|mut f| f.read_exact(&mut head))
            .map_err(|e| format!("{}: {e}", appimage.display()))?;
    }
    if !is_appimage(&head) {
        return Err(format!(
            "{} is not a type-2 AppImage; install the new version by hand.",
            appimage.display()
        ));
    }
    // The program that runs is the one inside that AppImage.
    let inside = env
        .appdir
        .map(PathBuf::from)
        .and_then(|d| d.canonicalize().ok())
        .zip(
            std::env::current_exe()
                .ok()
                .and_then(|e| e.canonicalize().ok()),
        )
        .is_some_and(|(d, exe)| exe.starts_with(d));
    if !inside {
        return Err(
            "This program is not running from its AppImage; install the new version by hand."
                .into(),
        );
    }
    Ok(appimage)
}

/// A file (or folder) this user owns, not a link, that no one else can write to: one a package
/// manager or another user keeps is not this updater's to replace.
fn check_owned(path: &Path, folder: bool) -> Result<(), String> {
    use std::os::unix::fs::MetadataExt;
    let m = fs::symlink_metadata(path).map_err(|e| format!("{}: {e}", path.display()))?;
    let kind_ok = if folder { m.is_dir() } else { m.is_file() };
    // SAFETY: geteuid has no preconditions.
    let me = unsafe { libc::geteuid() };
    if !kind_ok || m.uid() != me || m.mode() & 0o022 != 0 {
        return Err(format!(
            "{} is not a {} of yours that only you can change (a package manager or another \
             user may keep it); update it the way it was installed.",
            path.display(),
            if folder { "folder" } else { "file" }
        ));
    }
    Ok(())
}

/// The newer app release the feed offers on the current channel, with this system's artifact.
fn offered(app: &AppHandle) -> Result<Option<(Release, UpdateArtifact)>, String> {
    let latest = app
        .state::<App>()
        .releases
        .lock()
        .unwrap()
        .latest(Product::App)
        .cloned();
    let Some(latest) = latest else {
        return Ok(None);
    };
    let version = semver::Version::parse(&latest.version).map_err(|e| e.to_string())?;
    if version.cmp_precedence(&app.package_info().version) != std::cmp::Ordering::Greater {
        return Ok(None);
    }
    let artifact = latest.update.get(TARGET).cloned().ok_or_else(|| {
        format!(
            "Version {} has no update for this system ({TARGET}) in the feed.",
            latest.version
        )
    })?;
    Ok(Some((latest, artifact)))
}

fn history(dir: &Path) -> History {
    read_json(&dir.join("history.json")).unwrap_or_default()
}

pub fn view(app: &AppHandle) -> View {
    let task = app.state::<App>().updater.task.lock().unwrap().clone();
    let (mut available, mut reason, mut version) = (false, None, None);
    match gate(app)
        .and_then(|_| provenance(app))
        .and_then(|_| offered(app))
    {
        Ok(Some((r, _))) => {
            available = !task.running();
            version = Some(r.version);
        }
        Ok(None) => {}
        Err(e) => reason = Some(e),
    }
    let running = app.package_info().version.to_string();
    let (last, rollback) = match dir(app) {
        Ok(d) => (
            read_json(&d.join("last.json")),
            history(&d)
                .kept
                .into_iter()
                .filter(|p| p.version != running)
                .collect(),
        ),
        Err(_) => (None, vec![]),
    };
    View {
        available,
        reason,
        version,
        task,
        last,
        rollback,
    }
}

fn idle() -> Task {
    Task {
        phase: "idle",
        ..Task::default()
    }
}

/// Sends the task alone: progress and phase changes cost no gate, installation check or history
/// read. The window asks for the whole view (`app_update_get`) once a task ends.
fn emit_task(app: &AppHandle) {
    let task = app.state::<App>().updater.task.lock().unwrap().clone();
    let _ = app.emit_to(
        EventTarget::webview(MAIN),
        EVENT,
        serde_json::json!({ "task": task }),
    );
}

fn set_task(app: &AppHandle, f: impl FnOnce(&mut Task)) {
    f(&mut app.state::<App>().updater.task.lock().unwrap());
    emit_task(app);
}

/// Takes the task for t, unless an update or a rollback already runs (or waits for its
/// confirmation).
fn claim(task: &Mutex<Task>, t: Task) -> Result<(), String> {
    let mut current = task.lock().unwrap();
    if current.running() {
        return Err("An update is already running.".into());
    }
    *current = t;
    Ok(())
}

/// Hashes, once, the AppImage this process started from. Call after the app state exists.
pub fn record_started_from(app: &AppHandle) {
    let app = app.clone();
    std::thread::spawn(move || {
        let digest = match app.env().appimage {
            Some(p) => fs::read(&p)
                .map(|b| sha256(&b))
                .map_err(|e| format!("{}: {e}", Path::new(&p).display())),
            None => Err("this program is not an AppImage".into()),
        };
        let _ = app.state::<App>().updater.started_from.set(digest);
    });
}

fn started_from(app: &AppHandle) -> Result<String, String> {
    let st = app.state::<App>();
    let deadline = Instant::now() + Duration::from_secs(120);
    loop {
        if let Some(d) = st.updater.started_from.get() {
            return d.clone();
        }
        if Instant::now() > deadline {
            return Err("the AppImage this version started from is not hashed yet".into());
        }
        std::thread::sleep(Duration::from_millis(100));
    }
}

#[tauri::command]
pub fn app_update_get(webview: Webview) -> Result<View, String> {
    trusted(&webview)?;
    Ok(view(webview.app_handle()))
}

/// "Update Now": downloads, checks and installs the newer release, then restarts the app.
/// Returns once it has started; the window hears `pluxy-app-update` as it goes.
#[tauri::command]
pub fn app_update_start(webview: Webview) -> Result<(), String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    gate(&app)?;
    let appimage = provenance(&app)?;
    let (release, artifact) =
        offered(&app)?.ok_or("There is no newer version of the app on this channel.")?;
    let st = app.state::<App>();
    claim(
        &st.updater.task,
        Task {
            phase: "downloading",
            version: Some(release.version.clone()),
            downloaded: 0,
            total: Some(artifact.size),
            error: None,
        },
    )?;
    st.updater.cancel.store(false, Ordering::SeqCst);
    emit_task(&app);
    std::thread::spawn(
        move || match install(&app, &appimage, &release, &artifact) {
            Ok(restart) => restart_into(&app, restart),
            Err(e) if e == "cancelled" => set_task(&app, |t| {
                t.phase = "cancelled";
                t.error = None;
            }),
            Err(e) => set_task(&app, |t| {
                t.phase = "failed";
                t.error = Some(e);
            }),
        },
    );
    Ok(())
}

/// "Cancel Download": stops a download, within the download's read timeout at most; once the
/// update is checked and installing, it finishes.
#[tauri::command]
pub fn app_update_cancel(webview: Webview) -> Result<(), String> {
    trusted(&webview)?;
    let app = webview.app_handle();
    let st = app.state::<App>();
    if st.updater.task.lock().unwrap().phase != "downloading" {
        return Err("No download is running.".into());
    }
    st.updater.cancel.store(true, Ordering::SeqCst);
    Ok(())
}

fn install(
    app: &AppHandle,
    appimage: &Path,
    release: &Release,
    artifact: &UpdateArtifact,
) -> Result<Restart, String> {
    use tauri_plugin_updater::UpdaterExt;
    let st = app.state::<App>();
    let feed = st
        .releases
        .lock()
        .unwrap()
        .settings()
        .source
        .clone()
        .ok_or("No release feed is set.")?;
    gate(app)?;
    let updater = app
        .updater_builder()
        .endpoints(vec![tauri::Url::parse(&feed).map_err(|e| e.to_string())?])
        .and_then(|b| b.build())
        .map_err(|e| format!("The updater could not start: {e}"))?;
    // The plugin's own checks of the announced release: the default comparator (newer than
    // this version only), and the signature must carry the announced version.
    let raw = serde_json::json!({
        "version": release.version,
        "pub_date": release.published,
        "url": artifact.url,
        "signature": artifact.signature,
    });
    let update = updater
        .restore_update(raw)
        .map_err(|e| format!("The feed's update is not usable: {e}"))?
        .ok_or_else(|| format!("Version {} is not newer than this one.", release.version))?;
    let mut shown = Instant::now();
    let bytes = release::download(
        &artifact.url,
        &crate::updates::roots(),
        artifact.size,
        &st.updater.cancel,
        |n| {
            if shown.elapsed() >= Duration::from_millis(100) || n == artifact.size {
                shown = Instant::now();
                set_task(app, |t| t.downloaded = n);
            }
        },
    )?;
    set_task(app, |t| {
        t.downloaded = bytes.len() as u64;
        t.phase = "verifying";
    });
    update
        .verify(&bytes)
        .map_err(|e| format!("The update failed its signature check: {e}"))?;
    if !is_appimage(&bytes) {
        return Err("The update is not a type-2 AppImage.".into());
    }
    if st.updater.cancel.load(Ordering::SeqCst) {
        return Err("cancelled".into());
    }
    set_task(app, |t| t.phase = "installing");
    let dir = dir(app)?;
    let running = app.package_info().version.to_string();
    let from = keep(&dir, appimage, &running, &started_from(app)?)?;
    let to = Pinned {
        version: release.version.clone(),
        sha256: sha256(&bytes),
    };
    let pending = pending(&dir, "update", from, to.clone(), appimage)?;
    provenance(app)?;
    // The app's own replace, in one step, of the bytes the plugin just verified: the AppImage
    // is the old one or the new one, never missing or cut short.
    if let Err(e) = replace(appimage, &bytes) {
        let _ = fs::remove_file(dir.join("pending.json"));
        return Err(format!("The update could not be installed: {e}"));
    }
    Ok(Restart { dir, pending })
}

fn pending(
    dir: &Path,
    kind: &str,
    from: Pinned,
    to: Pinned,
    appimage: &Path,
) -> Result<Pending, String> {
    let mut nonce = [0u8; 16];
    getrandom::fill(&mut nonce).map_err(|e| e.to_string())?;
    let p = Pending {
        format: 1,
        kind: kind.into(),
        from,
        to,
        appimage: appimage.to_path_buf(),
        nonce: crate::artifact::hex(&nonce),
    };
    write_json(&dir.join("pending.json"), &p)?;
    Ok(p)
}

/// Keeps a copy of the AppImage this version started from, pinned by its digest, as the
/// version to go back to; returns that pin. started is the digest hashed when this version
/// started: an AppImage replaced since is not this version and is not kept under its name.
fn keep(dir: &Path, appimage: &Path, version: &str, started: &str) -> Result<Pinned, String> {
    let bytes = fs::read(appimage).map_err(|e| format!("{}: {e}", appimage.display()))?;
    let pin = Pinned {
        version: version.into(),
        sha256: sha256(&bytes),
    };
    if pin.sha256 != started {
        return Err(format!(
            "{} is not the AppImage this version ({version}) started from; restart the app before updating.",
            appimage.display()
        ));
    }
    let path = kept_path(dir, &pin);
    private_dir(&dir.join("kept"))?;
    if fs::read(&path).map(|b| sha256(&b)).ok().as_deref() != Some(pin.sha256.as_str()) {
        crate::managed::write_private(&path, &bytes)
            .map_err(|e| format!("{}: {e}", path.display()))?;
    }
    let mut h = history(dir);
    h.kept.retain(|p| p != &pin);
    h.kept.insert(0, pin.clone());
    for old in h.kept.split_off(KEEP.min(h.kept.len())) {
        let _ = fs::remove_file(kept_path(dir, &old));
    }
    write_json(&dir.join("history.json"), &h)?;
    Ok(pin)
}

/// Replaces the AppImage with bytes in one step: a new file in the same folder, written and
/// synced, with the AppImage's permissions, renamed over it, then the folder synced.
fn replace(appimage: &Path, bytes: &[u8]) -> Result<(), String> {
    use std::io::Write;
    use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
    let parent = appimage.parent().ok_or("The AppImage has no folder.")?;
    let mode = fs::metadata(appimage).map_or(0o755, |m| m.permissions().mode() & 0o755);
    let mut nonce = [0u8; 8];
    getrandom::fill(&mut nonce).map_err(|e| e.to_string())?;
    let tmp = parent.join(format!(".pluxy-update-{}", crate::artifact::hex(&nonce)));
    let written = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o700)
        .open(&tmp)
        .and_then(|mut f| {
            f.write_all(bytes)?;
            f.set_permissions(fs::Permissions::from_mode(mode | 0o700))?;
            f.sync_all()
        })
        .and_then(|_| fs::rename(&tmp, appimage))
        .and_then(|_| fs::File::open(parent)?.sync_all());
    if let Err(e) = written {
        let _ = fs::remove_file(&tmp);
        return Err(format!("{}: {e}", appimage.display()));
    }
    Ok(())
}

/// Puts the kept copy of the version an install came from back, if it still has its digest.
/// The pending install is over either way.
fn restore(dir: &Path, p: &Pending) -> Result<(), String> {
    let kept = kept_path(dir, &p.from);
    let outcome = match fs::read(&kept) {
        Ok(b) if sha256(&b) == p.from.sha256 => replace(&p.appimage, &b),
        Ok(_) => Err(format!("the kept copy {} changed", kept.display())),
        Err(e) => Err(format!("the kept copy {}: {e}", kept.display())),
    };
    let _ = fs::remove_file(dir.join("pending.json"));
    outcome
}

/// After an install: the app stops as Quit stops it (its tools and its own core), keeping its
/// profile lock and its tray; a thread of its own then starts the new version, hands it the
/// lock, and waits for it to accept itself (`supervise`) before the app exits.
fn restart_into(app: &AppHandle, restart: Restart) {
    set_task(app, |t| t.phase = "restarting");
    let app = app.clone();
    std::thread::spawn(move || {
        let st = app.state::<App>();
        // Tray actions refuse to start now, and closing the window no longer keeps the app.
        st.quitting.store(true, Ordering::SeqCst);
        crate::remember_window(&app);
        #[cfg(target_os = "linux")]
        st.confirmations.lock().unwrap().invalidate();
        #[cfg(target_os = "linux")]
        st.programs.stop_all();
        st.conn.lock().unwrap().stop_core();
        // Hidden, not closed: closing the last window would end the app before the wait does.
        if let Some(w) = app.get_window(crate::WINDOW) {
            let _ = w.hide();
        }
        let lock = app.try_state::<crate::instance::Instance>().and_then(|i| {
            // No later launch is raised by this app any more; the lock stays held.
            i.release();
            i.lock_fd()
        });
        supervise(&restart, lock);
        app.exit(0);
    });
}

/// Starts program with args, in a process group of its own, handing it the profile lock (the
/// file descriptor stays open across exec, and `instance` adopts it). With after set, it starts
/// once that process has ended.
fn spawn_holding(
    lock: Option<std::os::fd::RawFd>,
    program: &Path,
    args: &[OsString],
    after: Option<i32>,
) -> std::io::Result<std::process::Child> {
    use std::os::unix::process::CommandExt;
    let mut c = match after {
        Some(pid) => {
            let mut c = std::process::Command::new("/bin/sh");
            c.arg("-c")
                .arg(r#"p=$1; a=$2; shift 2; while kill -0 "$p" 2>/dev/null; do sleep 0.2; done; exec "$a" "$@""#)
                .arg("sh")
                .arg(pid.to_string())
                .arg(program)
                .args(args);
            c
        }
        None => {
            let mut c = std::process::Command::new(program);
            c.args(args);
            c
        }
    };
    c.process_group(0);
    match lock {
        Some(fd) => {
            c.env(crate::instance::LOCK_FD_ENV, fd.to_string());
            // SAFETY: fcntl is async-signal-safe; it only clears close-on-exec on the lock.
            unsafe {
                c.pre_exec(move || {
                    let flags = libc::fcntl(fd, libc::F_GETFD);
                    if flags < 0 || libc::fcntl(fd, libc::F_SETFD, flags & !libc::FD_CLOEXEC) < 0 {
                        return Err(std::io::Error::last_os_error());
                    }
                    Ok(())
                });
            }
        }
        None => {
            c.env_remove(crate::instance::LOCK_FD_ENV);
        }
    }
    c.spawn()
}

/// Starts the installed AppImage, holding the profile lock for it, and waits for it to accept
/// itself; if it ends or does not accept in time, puts the earlier version back and starts that
/// (or, if it cannot be put back, starts nothing and says where the kept copy is).
fn supervise(r: &Restart, lock: Option<std::os::fd::RawFd>) {
    let receipt = r.dir.join("accepted.json");
    let _ = fs::remove_file(&receipt);
    let args: Vec<OsString> = std::env::args_os().skip(1).collect();
    let p = &r.pending;
    let accepted = || {
        read_json::<Accepted>(&receipt)
            .is_some_and(|a| a.nonce == p.nonce && a.version == p.to.version)
    };
    let outcome = match spawn_holding(lock, &p.appimage, &args, None) {
        Ok(mut child) => {
            let deadline = Instant::now() + ACCEPT_WITHIN;
            loop {
                if accepted() {
                    break Ok(());
                }
                if let Ok(Some(status)) = child.try_wait() {
                    break if accepted() {
                        Ok(())
                    } else {
                        Err(format!("it ended ({status}) before accepting the update"))
                    };
                }
                if Instant::now() > deadline {
                    // SAFETY: kill has no memory preconditions; the group is the child's own.
                    unsafe { libc::kill(-(child.id() as i32), libc::SIGKILL) };
                    let _ = child.wait();
                    break Err(format!(
                        "it did not accept the update within {} seconds",
                        ACCEPT_WITHIN.as_secs()
                    ));
                }
                std::thread::sleep(Duration::from_millis(100));
            }
        }
        Err(e) => Err(format!("it could not be started: {e}")),
    };
    let _ = fs::remove_file(&receipt);
    let Err(why) = outcome else {
        return;
    };
    eprintln!("pluxy-app: {} did not start: {why}", p.to.version);
    let (detail, restored) = match restore(&r.dir, p) {
        Ok(()) => (
            format!(
                "{} did not start ({why}); {} was put back.",
                p.to.version, p.from.version
            ),
            true,
        ),
        Err(e) => (
            format!(
                "{} did not start ({why}), and {} could not be put back: {e}. Nothing was started: \
                 copy {} to {} to run {} again.",
                p.to.version,
                p.from.version,
                kept_path(&r.dir, &p.from).display(),
                p.appimage.display(),
                p.from.version
            ),
            false,
        ),
    };
    let _ = write_json(
        &r.dir.join("last.json"),
        &Last {
            result: "failed".into(),
            detail,
        },
    );
    if !restored {
        return;
    }
    // The earlier version's AppImage has the same contents as this one's: run without FUSE
    // (APPIMAGE_EXTRACT_AND_RUN) it would unpack into the folder this one's runtime, its parent,
    // deletes once this process ends. So it starts once that runtime has ended.
    let extracted = std::env::var_os("APPDIR")
        .map(PathBuf::from)
        .and_then(|d| {
            d.file_name()
                .map(|n| n.to_string_lossy().starts_with("appimage_extracted_"))
        })
        .unwrap_or(false);
    let after = if extracted {
        // SAFETY: getppid has no preconditions.
        unsafe { libc::getppid() }
    } else {
        std::process::id() as i32
    };
    let _ = spawn_holding(lock, &p.appimage, &args, Some(after));
}

/// At startup, after the profile lock: the version an install started accepts itself once the
/// installed AppImage is the one it pinned, which the waiting earlier version reads.
pub fn accept(app: &AppHandle) {
    let Ok(dir) = dir(app) else { return };
    let Some(p) = read_json::<Pending>(&dir.join("pending.json")) else {
        return;
    };
    let running = app.package_info().version.to_string();
    let here = app.env().appimage.map(PathBuf::from);
    if running != p.to.version
        || here.as_deref() != Some(p.appimage.as_path())
        || fs::read(&p.appimage).map(|b| sha256(&b)).ok().as_deref() != Some(p.to.sha256.as_str())
    {
        return;
    }
    // The native test's stand-in for a new version that cannot start.
    #[cfg(feature = "native-test")]
    if let Some(hook) = std::env::var_os("PLUXY_APP_TEST_UPDATE")
        .map(|d| PathBuf::from(d).join("refuse-accept"))
        .filter(|h| h.exists())
    {
        let _ = fs::remove_file(hook);
        eprintln!("native-test: update_not_accepting {{}}");
        std::process::exit(3);
    }
    let (result, detail) = if p.kind == "rollback" {
        (
            "rolled-back",
            format!("Rolled back from {} to {}.", p.from.version, p.to.version),
        )
    } else {
        (
            "installed",
            format!("Updated from {} to {}.", p.from.version, p.to.version),
        )
    };
    let _ = write_json(
        &dir.join("last.json"),
        &Last {
            result: result.into(),
            detail,
        },
    );
    let _ = fs::remove_file(dir.join("pending.json"));
    let _ = write_json(
        &dir.join("accepted.json"),
        &Accepted {
            nonce: p.nonce,
            version: running,
        },
    );
}

/// Rolls back to a version that ran here and was kept, by its digest, after a native
/// confirmation. Returns once the confirmation shows; until it is settled no update or other
/// rollback can start.
#[tauri::command]
pub fn app_update_rollback(
    webview: Webview,
    version: String,
    sha256: String,
) -> Result<(), String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    gate(&app)?;
    let appimage = provenance(&app)?;
    let dir = dir(&app)?;
    let target = Pinned { version, sha256 };
    if !history(&dir).kept.contains(&target) {
        return Err(format!(
            "Version {} with that digest was never kept here; the app rolls back only to a version that ran here.",
            target.version
        ));
    }
    let running = app.package_info().version.to_string();
    if target.version == running {
        return Err("That is the version running now.".into());
    }
    let bytes = fs::read(kept_path(&dir, &target)).unwrap_or_default();
    if self::sha256(&bytes) != target.sha256 {
        return Err(format!(
            "The kept copy of {} no longer has its pinned digest; it is not used.",
            target.version
        ));
    }
    claim(
        &app.state::<App>().updater.task,
        Task {
            phase: "confirming",
            version: Some(target.version.clone()),
            ..Task::default()
        },
    )?;
    emit_task(&app);
    let asked = confirm_rollback(app.clone(), appimage, dir, running, target);
    if asked.is_err() {
        set_task(&app, |t| *t = idle());
    }
    asked
}

#[cfg(target_os = "linux")]
fn confirm_rollback(
    app: AppHandle,
    appimage: PathBuf,
    dir: PathBuf,
    running: String,
    target: Pinned,
) -> Result<(), String> {
    use crate::confirmation::{self, Action, Package};
    let st = app.state::<App>();
    let package = Package {
        id: "pluxy app".into(),
        source: "the app's window".into(),
        digest: target.sha256.clone(),
        signer: String::new(),
        contract: "app rollback".into(),
        generation: 0,
    };
    let action = Action {
        what: format!(
            "Roll the app back from {running} to {}, the version that ran here before.\n\
             It replaces {} with the kept copy of {} (SHA-256 {}), then restarts the app.",
            target.version,
            appimage.display(),
            target.version,
            target.sha256
        ),
        chain: vec![package.clone()],
        executor: package,
        native_tool: false,
        files: vec![],
        launch: None,
    };
    let id = {
        let mut c = st.confirmations.lock().unwrap();
        let p = c.prepare_main(action)?;
        c.queue(p)?
    };
    confirmation::request(&app, id.clone())?;
    std::thread::spawn(move || {
        let st = app.state::<App>();
        let deadline = Instant::now() + Duration::from_secs(75);
        let result = loop {
            if let Some(r) = st.confirmations.lock().unwrap().take_result(&id) {
                break Some(r);
            }
            if Instant::now() > deadline {
                break None;
            }
            std::thread::sleep(Duration::from_millis(100));
        };
        let authorized = match result {
            Some(Ok(Some(token))) => st
                .confirmations
                .lock()
                .unwrap()
                .consume(&id, &token)
                .is_ok(),
            _ => false,
        };
        if !authorized {
            set_task(&app, |t| *t = idle());
            return;
        }
        match roll_back(&app, &appimage, &dir, &running, &target) {
            Ok(restart) => restart_into(&app, restart),
            Err(e) => set_task(&app, |t| {
                t.phase = "failed";
                t.error = Some(e);
            }),
        }
    });
    Ok(())
}

#[cfg(not(target_os = "linux"))]
fn confirm_rollback(
    _: AppHandle,
    _: PathBuf,
    _: PathBuf,
    _: String,
    _: Pinned,
) -> Result<(), String> {
    Err("Rolling back needs a native confirmation, on Linux only for now.".into())
}

#[cfg(target_os = "linux")]
fn roll_back(
    app: &AppHandle,
    appimage: &Path,
    dir: &Path,
    running: &str,
    target: &Pinned,
) -> Result<Restart, String> {
    // Still this rollback's task, and checked again now that it was confirmed: the trust, the
    // installation, the kept copy.
    if app.state::<App>().updater.task.lock().unwrap().phase != "confirming" {
        return Err("The rollback is no longer waiting for its confirmation.".into());
    }
    gate(app)?;
    provenance(app)?;
    let bytes = fs::read(kept_path(dir, target)).map_err(|e| e.to_string())?;
    if sha256(&bytes) != target.sha256 || !is_appimage(&bytes) {
        return Err(format!(
            "The kept copy of {} no longer has its pinned digest; it is not used.",
            target.version
        ));
    }
    set_task(app, |t| {
        t.phase = "installing";
        t.error = None;
    });
    let from = keep(dir, appimage, running, &started_from(app)?)?;
    let pending = pending(dir, "rollback", from, target.clone(), appimage)?;
    if let Err(e) = replace(appimage, &bytes) {
        let _ = fs::remove_file(dir.join("pending.json"));
        return Err(format!("The rollback could not be installed: {e}"));
    }
    Ok(Restart {
        dir: dir.to_path_buf(),
        pending,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp(name: &str) -> PathBuf {
        let tmp = std::env::temp_dir().join(format!("pluxy-updater-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&tmp);
        fs::create_dir_all(&tmp).unwrap();
        tmp
    }

    #[test]
    fn only_a_type_2_appimage_header_counts() {
        assert!(is_appimage(
            b"\x7fELF\x02\x01\x01\x00AI\x02\x00\x00\x00\x00\x00"
        ));
        assert!(!is_appimage(
            b"\x7fELF\x02\x01\x01\x00AI\x01\x00\x00\x00\x00\x00"
        ));
        assert!(!is_appimage(
            b"\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00"
        ));
        assert!(!is_appimage(b"\x1f\x8b\x08\x00"));
    }

    #[test]
    fn keeping_pins_the_running_appimage_and_keeps_three() {
        let tmp = temp("keep");
        let image = tmp.join("app.AppImage");
        let mut pins = vec![];
        for v in ["0.1.0", "0.2.0", "0.3.0", "0.4.0"] {
            let b = format!("AppImage {v}");
            fs::write(&image, &b).unwrap();
            pins.push(keep(&tmp.join("updates"), &image, v, &sha256(b.as_bytes())).unwrap());
        }
        let h = history(&tmp.join("updates"));
        assert_eq!(h.kept, pins[1..].iter().rev().cloned().collect::<Vec<_>>());
        assert!(!kept_path(&tmp.join("updates"), &pins[0]).exists());
        let last = &pins[3];
        assert_eq!(last.sha256, sha256(b"AppImage 0.4.0"));
        assert_eq!(
            fs::read(kept_path(&tmp.join("updates"), last)).unwrap(),
            b"AppImage 0.4.0"
        );
        fs::remove_dir_all(&tmp).unwrap();
    }

    // An AppImage replaced after this version started is not kept under this version's name.
    #[test]
    fn keeping_refuses_an_appimage_this_version_did_not_start_from() {
        let tmp = temp("keep-other");
        let image = tmp.join("app.AppImage");
        fs::write(&image, b"replaced since").unwrap();
        let e = keep(
            &tmp.join("updates"),
            &image,
            "0.1.0",
            &sha256(b"started from"),
        )
        .unwrap_err();
        assert!(e.contains("not the AppImage this version"), "{e}");
        assert!(history(&tmp.join("updates")).kept.is_empty());
        fs::remove_dir_all(&tmp).unwrap();
    }

    #[test]
    fn restore_puts_back_only_a_copy_with_its_pinned_digest() {
        let tmp = temp("restore");
        let dir = tmp.join("updates");
        let image = tmp.join("app.AppImage");
        fs::write(&image, b"old").unwrap();
        let from = keep(&dir, &image, "0.1.0", &sha256(b"old")).unwrap();
        let to = Pinned {
            version: "0.2.0".into(),
            sha256: sha256(b"new"),
        };
        let p = pending(&dir, "update", from.clone(), to.clone(), &image).unwrap();
        fs::write(&image, b"new").unwrap();
        restore(&dir, &p).unwrap();
        assert_eq!(fs::read(&image).unwrap(), b"old");
        assert!(!dir.join("pending.json").exists());
        // A changed kept copy is not put back, and the message names it.
        fs::write(&image, b"new").unwrap();
        fs::write(kept_path(&dir, &from), b"changed").unwrap();
        let e = restore(&dir, &p).unwrap_err();
        assert!(
            e.contains(&kept_path(&dir, &from).display().to_string()),
            "{e}"
        );
        assert_eq!(fs::read(&image).unwrap(), b"new");
        fs::remove_dir_all(&tmp).unwrap();
    }

    // The replace keeps the AppImage's permissions and leaves no temporary file.
    #[test]
    fn replace_is_one_rename_with_the_old_permissions() {
        use std::os::unix::fs::PermissionsExt;
        let tmp = temp("replace");
        let image = tmp.join("app.AppImage");
        fs::write(&image, b"old").unwrap();
        fs::set_permissions(&image, fs::Permissions::from_mode(0o750)).unwrap();
        replace(&image, b"new").unwrap();
        assert_eq!(fs::read(&image).unwrap(), b"new");
        assert_eq!(
            fs::metadata(&image).unwrap().permissions().mode() & 0o777,
            0o750
        );
        assert_eq!(fs::read_dir(&tmp).unwrap().count(), 1);
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A rollback waiting for its confirmation holds the task: Update Now and another rollback
    // are refused until it is settled.
    #[test]
    fn a_waiting_rollback_holds_the_task() {
        let task = Mutex::new(idle());
        claim(
            &task,
            Task {
                phase: "confirming",
                ..Task::default()
            },
        )
        .unwrap();
        for phase in ["downloading", "confirming"] {
            let e = claim(
                &task,
                Task {
                    phase,
                    ..Task::default()
                },
            )
            .unwrap_err();
            assert!(e.contains("already running"), "{e}");
        }
        *task.lock().unwrap() = idle();
        claim(
            &task,
            Task {
                phase: "downloading",
                ..Task::default()
            },
        )
        .unwrap();
    }
}
