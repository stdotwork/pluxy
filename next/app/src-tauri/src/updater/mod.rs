use crate::owned::Successor;
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

#[cfg(any(target_os = "macos", all(test, unix)))]
mod acl;
#[cfg(any(target_os = "macos", all(test, unix)))]
mod app;
#[cfg(target_os = "linux")]
mod appimage;
#[cfg(any(target_os = "macos", all(test, unix)))]
mod applications;
#[cfg(any(target_os = "macos", all(test, unix)))]
mod bundle;
#[cfg(windows)]
mod exe;
#[cfg(any(not(target_os = "linux"), test))]
mod leftovers;
mod set;
#[cfg(any(windows, test))]
mod uninstall_entry;
#[cfg(target_os = "macos")]
use app as format;
#[cfg(target_os = "linux")]
use appimage as format;
#[cfg(windows)]
use exe as format;

/// The updater's name for this system's artifact.
use format::TARGET;
/// How long a new version has to accept itself.
const ACCEPT_WITHIN: Duration = Duration::from_secs(90);
/// Kept copies of earlier versions, newest first; older ones are deleted.
const KEEP: usize = 3;
/// Most entries an update's archive may hold (`bundle.rs`, `set.rs`).
#[cfg_attr(target_os = "linux", allow(dead_code))]
const MAX_ENTRIES: usize = 4096;
/// Most bytes its files may hold, unpacked.
#[cfg_attr(target_os = "linux", allow(dead_code))]
const MAX_BYTES: u64 = 1 << 30;
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

/// A version of the app and the digest of its installed copy: the SHA-256 of the AppImage or the
/// program, or of an app bundle's tree (`app.rs`).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Pinned {
    pub version: String,
    pub sha256: String,
}

/// An install waiting for its new version to accept itself (`pending.json`).
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Pending {
    format: u32,
    /// update or rollback; on Windows also put-back, the kept set going back in place of a new
    /// version that did not start.
    kind: String,
    from: Pinned,
    to: Pinned,
    /// The installed copy: the AppImage, the program set's folder or the app bundle. Its name on
    /// disk is the one format 1 gave it on Linux.
    #[serde(rename = "appimage")]
    installed: PathBuf,
    nonce: String,
    /// Windows: the parts the install moves and where from (`set::Moves`), recorded before any
    /// is moved, so a start after a crash can finish or undo them.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    moves: Option<set::Moves>,
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
    /// The digest of the installed copy this process started from, taken once at startup
    /// (`record_started_from`): only that copy is kept as this version.
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

/// The updates folder: kept copies, the pending install, history. On Windows in the app's local
/// data folder, on a fixed local drive, so kept program sets stay on this machine when the
/// profile roams; elsewhere in its data folder.
fn dir(app: &AppHandle) -> Result<PathBuf, String> {
    #[cfg(windows)]
    let data = app.path().app_local_data_dir();
    #[cfg(not(windows))]
    let data = app.path().app_data_dir();
    let dir = data.map(|d| d.join("updates")).map_err(|e| e.to_string())?;
    #[cfg(windows)]
    exe::local_fixed(&dir)?;
    Ok(dir)
}

/// The updates folder, for the native test.
#[cfg_attr(not(feature = "native-test"), allow(dead_code))]
pub(crate) fn updates_dir(app: &AppHandle) -> Result<PathBuf, String> {
    dir(app)
}

/// Where the kept copy of p is: `updates/kept/<version>-<digest>` and the format's ending.
pub(crate) fn kept_path(dir: &Path, p: &Pinned) -> PathBuf {
    dir.join("kept")
        .join(format!("{}-{}{}", p.version, p.sha256, format::SUFFIX))
}

/// The installed copy this process runs from, as the updater sees it (the AppImage, the program
/// or the app bundle), if it can tell. For the native test.
#[cfg_attr(not(feature = "native-test"), allow(dead_code))]
pub(crate) fn installed_here(app: &AppHandle) -> Option<PathBuf> {
    format::here(app)
}

/// The digest an installed copy is pinned by (see `Pinned`). For the native test, and on Windows
/// for the version an update starts, to check it runs from the copy the update named
/// (`instance`).
#[cfg_attr(not(any(windows, feature = "native-test")), allow(dead_code))]
pub(crate) fn digest(path: &Path) -> Result<String, String> {
    format::digest(path)
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

#[cfg(unix)]
fn private_dir(path: &Path) -> Result<(), String> {
    use std::os::unix::fs::DirBuilderExt;
    fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(path)
        .map_err(|e| format!("{}: {e}", path.display()))
}

#[cfg(windows)]
fn private_dir(path: &Path) -> Result<(), String> {
    crate::private_windows::create_private_dir_all(path)
        .map_err(|e| format!("{}: {e}", path.display()))
}

/// A file (or folder) this user owns, not a link, that no one else can write to: one a package
/// manager or another user keeps is not this updater's to replace.
#[cfg(unix)]
fn check_owned(path: &Path, folder: bool) -> Result<(), String> {
    let m = fs::symlink_metadata(path).map_err(|e| format!("{}: {e}", path.display()))?;
    owned_as(&m, path, folder)
}

/// `check_owned` on m, what was read of path (through an open descriptor, say).
#[cfg(unix)]
fn owned_as(m: &fs::Metadata, path: &Path, folder: bool) -> Result<(), String> {
    use std::os::unix::fs::MetadataExt;
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

/// How this copy of the app was installed, as far as updating it goes: the copy this user keeps
/// (the AppImage, the program or the app bundle), or why it cannot be updated from here.
pub fn provenance(app: &AppHandle) -> Result<PathBuf, String> {
    format::provenance(app)
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
/// confirmation), and clears a cancel left over from an earlier one. The clearing happens under
/// the task's lock, which Cancel Download also holds when it sets the flag: a cancel arrives
/// either before this task exists (refused) or after the flag was cleared (kept).
fn claim(task: &Mutex<Task>, cancel: &AtomicBool, t: Task) -> Result<(), String> {
    let mut current = task.lock().unwrap();
    if current.running() {
        return Err("An update is already running.".into());
    }
    cancel.store(false, Ordering::SeqCst);
    *current = t;
    Ok(())
}

/// Takes, once, the digest of the installed copy this process started from. Call after the app
/// state exists.
pub fn record_started_from(app: &AppHandle) {
    let app = app.clone();
    std::thread::spawn(move || {
        let digest = match format::here(&app) {
            Some(p) => format::digest(&p),
            None => Err(format!("this program is not an {}", format::WHAT)),
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
            return Err(format!(
                "the {} this version started from is not hashed yet",
                format::WHAT
            ));
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
    let installed = provenance(&app)?;
    let (release, artifact) =
        offered(&app)?.ok_or("There is no newer version of the app on this channel.")?;
    let st = app.state::<App>();
    claim(
        &st.updater.task,
        &st.updater.cancel,
        Task {
            phase: "downloading",
            version: Some(release.version.clone()),
            downloaded: 0,
            total: Some(artifact.size),
            error: None,
        },
    )?;
    emit_task(&app);
    std::thread::spawn(
        move || match install(&app, &installed, &release, &artifact) {
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
    let task = st.updater.task.lock().unwrap();
    if task.phase != "downloading" {
        return Err("No download is running.".into());
    }
    st.updater.cancel.store(true, Ordering::SeqCst);
    Ok(())
}

fn install(
    app: &AppHandle,
    installed: &Path,
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
    if st.updater.cancel.load(Ordering::SeqCst) {
        return Err("cancelled".into());
    }
    let dir = dir(app)?;
    // The verified bytes, checked as this format's and made ready to put in place (on macOS,
    // unpacked into a staging folder recorded in dir).
    let staged = format::stage(installed, bytes, &release.version, &dir)?;
    if st.updater.cancel.load(Ordering::SeqCst) {
        return Err("cancelled".into());
    }
    set_task(app, |t| t.phase = "installing");
    let running = app.package_info().version.to_string();
    let from = keep(&dir, installed, &running, &started_from(app)?)?;
    let to = Pinned {
        version: release.version.clone(),
        sha256: staged.digest().to_string(),
    };
    // Checked before the pending record is written, so a refusal leaves none behind.
    provenance(app)?;
    let pending = pending(&dir, "update", from, to.clone(), installed, moves(&staged))?;
    // The app's own replace of what the plugin just verified: on Linux and macOS the installed
    // copy is the old one or the new one; on Windows the set is a mix of the two while its parts
    // move, which a failure undoes here and a crash at the next start (`set.rs`).
    if let Err(e) = format::swap_in(installed, staged, &dir) {
        failed(&dir, &pending);
        return Err(format!("The update could not be installed: {e}"));
    }
    evict(&dir);
    Ok(Restart { dir, pending })
}

/// The move list of staged, which the pending record carries (Windows; none elsewhere).
#[cfg(windows)]
fn moves(staged: &format::Staged) -> Option<set::Moves> {
    Some(staged.moves())
}

#[cfg(not(windows))]
fn moves(_: &format::Staged) -> Option<set::Moves> {
    None
}

fn pending(
    dir: &Path,
    kind: &str,
    from: Pinned,
    to: Pinned,
    installed: &Path,
    moves: Option<set::Moves>,
) -> Result<Pending, String> {
    #[cfg(windows)]
    open_record(dir, installed)?;
    let p = Pending {
        format: 1,
        kind: kind.into(),
        from,
        to,
        installed: installed.to_path_buf(),
        nonce: nonce()?,
        moves,
    };
    write_json(&dir.join("pending.json"), &p)?;
    Ok(p)
}

/// Windows: refused while `pending.json` records an install that is not finished, which writing
/// a new one would lose: another install's (two installs of one identifier share the updates
/// folder; theirs is left alone), or one of this install's with parts still aside. Checked with
/// the installation (`exe::provenance`), before any download, and again as the record is written.
#[cfg_attr(not(windows), allow(dead_code))]
fn open_record(dir: &Path, installed: &Path) -> Result<(), String> {
    let Some(p) = read_json::<Pending>(&dir.join("pending.json")) else {
        return Ok(());
    };
    if p.installed != installed {
        return Err(format!(
            "Another copy of pluxy ({}) has an update or rollback that is not finished, and it \
             shares this app's update records. Start that copy once to finish it, or, if it is \
             gone, delete {}.",
            p.installed.display(),
            dir.join("pending.json").display()
        ));
    }
    if p.moves
        .as_ref()
        .is_none_or(|m| set::unresolved(installed, m))
    {
        return Err(format!(
            "An earlier update or rollback left parts of {} aside, and they were not put back \
             (see {}); the app's README says how to put them back by hand.",
            installed.display(),
            dir.join("pending.json").display()
        ));
    }
    Ok(())
}

/// After a replace that failed: the pending record goes, unless (Windows) parts it moved are
/// still aside, which the next start puts back by it.
fn failed(dir: &Path, p: &Pending) {
    if p.moves
        .as_ref()
        .is_some_and(|m| set::unresolved(&p.installed, m))
    {
        return;
    }
    let _ = fs::remove_file(dir.join("pending.json"));
}

fn nonce() -> Result<String, String> {
    let mut nonce = [0u8; 16];
    getrandom::fill(&mut nonce).map_err(|e| e.to_string())?;
    Ok(crate::artifact::hex(&nonce))
}

/// Keeps a copy of the installed copy this version started from, pinned by its digest, as the
/// version to go back to; returns that pin. started is the digest taken when this version
/// started: a copy replaced since is not this version and is not kept under its name. The kept
/// copy is checked against the pin once made.
fn keep(dir: &Path, installed: &Path, version: &str, started: &str) -> Result<Pinned, String> {
    let pin = Pinned {
        version: version.into(),
        sha256: format::digest(installed)?,
    };
    if pin.sha256 != started {
        return Err(format!(
            "{} is not the {} this version ({version}) started from; restart the app before updating.",
            installed.display(),
            format::WHAT
        ));
    }
    let path = kept_path(dir, &pin);
    private_dir(&dir.join("kept"))?;
    if format::digest(&path).ok().as_deref() != Some(pin.sha256.as_str()) {
        format::remove(&path);
        format::copy_new(installed, &path)?;
        if format::digest(&path).ok().as_deref() != Some(pin.sha256.as_str()) {
            format::remove(&path);
            return Err(format!(
                "{} changed while it was kept; restart the app before updating.",
                installed.display()
            ));
        }
    }
    let mut h = history(dir);
    h.kept.retain(|p| p != &pin);
    h.kept.insert(0, pin.clone());
    write_json(&dir.join("history.json"), &h)?;
    Ok(pin)
}

/// Drops the oldest kept versions beyond `KEEP`, history entry and copy. Run only once the
/// install they were kept for has replaced the installed copy: until then a failed replace must
/// leave every earlier version it could roll back to.
fn evict(dir: &Path) {
    let mut h = history(dir);
    let old = h.kept.split_off(KEEP.min(h.kept.len()));
    if old.is_empty() {
        return;
    }
    // History first: a copy without an entry is harmless, an entry without its copy is not.
    if write_json(&dir.join("history.json"), &h).is_ok() {
        for p in old {
            format::remove(&kept_path(dir, &p));
        }
    }
}

/// Puts the kept copy of the version an install came from back, if it still has its digest.
/// The pending install is over either way. On Windows the put-back moves the set part by part,
/// so it is recorded first as an install of its own (`put-back`, from the version that did not
/// start to the kept one, with its move list): should it stop part way, the next start finishes
/// it (`set::recover`), and its record stays until then.
fn restore(dir: &Path, p: &Pending) -> Result<(), String> {
    let kept = kept_path(dir, &p.from);
    let mut put_back = None;
    let outcome = format::stage_kept(&p.installed, &kept, &p.from, dir).and_then(|s| {
        if let Some(m) = moves(&s) {
            let _ = fs::remove_file(dir.join("pending.json"));
            let r = pending(
                dir,
                "put-back",
                p.to.clone(),
                p.from.clone(),
                &p.installed,
                Some(m),
            )?;
            put_back = Some(r);
        }
        format::swap_in(&p.installed, s, dir)
    });
    match (&outcome, &put_back) {
        (Err(_), Some(r)) => failed(dir, r),
        _ => {
            let _ = fs::remove_file(dir.join("pending.json"));
        }
    }
    outcome
}

/// After an install: the app stops as Quit stops it (its tools, its managed runtimes and its own
/// core, in that order; detached launches run on), keeping its profile lock and its tray; a
/// thread of its own then starts the new version, hands the profile to it, and waits for it to
/// accept itself (`supervise`) before the app exits.
fn restart_into(app: &AppHandle, restart: Restart) {
    set_task(app, |t| t.phase = "restarting");
    let app = app.clone();
    std::thread::spawn(move || {
        let st = app.state::<App>();
        // Tray actions, Open and Quit refuse now (a Quit would end this process, which waits
        // for the new version and rolls a broken one back), and closing the window no longer
        // keeps the app.
        st.quitting.store(true, Ordering::SeqCst);
        crate::remember_window(&app);
        st.confirmations.lock().unwrap().invalidate();
        crate::stop_owned(&st);
        // Hidden, not closed: closing the last window would end the app before the wait does.
        if let Some(w) = app.get_window(crate::WINDOW) {
            let _ = w.hide();
        }
        // No later launch is raised by this app any more; the lock stays held.
        if let Some(i) = app.try_state::<crate::instance::Instance>() {
            i.release();
        }
        supervise(&app, &restart);
        app.exit(0);
    });
}

/// Starts the installed copy as this app's successor, with this process's arguments. The profile
/// passes to it without ever being free: on Linux and macOS this process hands it the profile
/// lock's descriptor; on Windows (where a lock cannot be handed on) this process names it in a
/// successor record by nonce and digest, and it takes the lock once this process has ended
/// (`instance`). With after, on Linux and macOS it starts once this process has ended.
fn start_successor(
    app: &AppHandle,
    installed: &Path,
    nonce: &str,
    digest: &str,
    after: bool,
) -> std::io::Result<Successor> {
    let args: Vec<OsString> = std::env::args_os().skip(1).collect();
    let program = format::program(installed);
    #[cfg(unix)]
    {
        let _ = (nonce, digest);
        let lock = app
            .try_state::<crate::instance::Instance>()
            .and_then(|i| i.lock_fd());
        Successor::start(
            &program,
            &args,
            lock.map(|fd| (fd, crate::instance::LOCK_FD_ENV)),
            after.then(format::restart_after),
        )
    }
    #[cfg(windows)]
    {
        let _ = after;
        let i = app
            .try_state::<crate::instance::Instance>()
            .ok_or_else(|| std::io::Error::other("the app holds no profile"))?;
        i.name_successor(nonce, &program, digest)?;
        let mut env: Vec<(OsString, OsString)> = std::env::vars_os()
            .filter(|(k, _)| {
                !k.to_string_lossy()
                    .eq_ignore_ascii_case(crate::instance::SUCCESSOR_ENV)
            })
            .collect();
        env.push((crate::instance::SUCCESSOR_ENV.into(), nonce.into()));
        Successor::start(&program, &args, &env)
    }
}

/// Waits until the started version accepts itself (then lets it go), ends, or the deadline
/// passes (then ends it). A receipt that arrives as it ends, or in the last wait before the
/// deadline, still counts.
fn await_acceptance(
    mut next: Successor,
    deadline: Instant,
    accepted: impl Fn() -> bool,
) -> Result<(), String> {
    loop {
        if accepted() {
            next.release();
            return Ok(());
        }
        if let Ok(Some(status)) = next.try_wait() {
            return if accepted() {
                next.release();
                Ok(())
            } else {
                Err(format!("it ended ({status}) before accepting the update"))
            };
        }
        if Instant::now() > deadline {
            // One last look before ending it: a receipt written during the last wait counts, and
            // a version ended after accepting would leave nothing running.
            if accepted() {
                next.release();
                return Ok(());
            }
            next.end();
            return Err(format!(
                "it did not accept the update within {} seconds",
                ACCEPT_WITHIN.as_secs()
            ));
        }
        std::thread::sleep(Duration::from_millis(100));
    }
}

/// Starts the installed version as this app's successor and waits for it to accept itself; if it
/// ends or does not accept in time, puts the earlier version back and starts that (or, if it
/// cannot be put back, starts nothing and says where the kept copy is).
fn supervise(app: &AppHandle, r: &Restart) {
    let receipt = r.dir.join("accepted.json");
    let _ = fs::remove_file(&receipt);
    let p = &r.pending;
    let accepted = || {
        read_json::<Accepted>(&receipt)
            .is_some_and(|a| a.nonce == p.nonce && a.version == p.to.version)
    };
    let outcome = match start_successor(app, &p.installed, &p.nonce, &p.to.sha256, false) {
        Ok(next) => await_acceptance(next, Instant::now() + ACCEPT_WITHIN, accepted),
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
        Err(e) => {
            let kept = kept_path(&r.dir, &p.from);
            // On Windows the kept copy is a folder holding the set's three parts, copied each
            // into the program's folder.
            #[cfg(windows)]
            let how = format!(
                "copy {}, {} and {} from {} into {}",
                set::BUNDLED,
                set::CORE,
                exe::program_name(),
                kept.display(),
                p.installed.display()
            );
            #[cfg(not(windows))]
            let how = format!("copy {} to {}", kept.display(), p.installed.display());
            (
                format!(
                    "{} did not start ({why}), and {} could not be put back: {e}. Nothing was \
                     started: {how} to run {} again.",
                    p.to.version, p.from.version, p.from.version
                ),
                false,
            )
        }
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
    // The version put back takes the profile once this process has ended; nothing waits for it.
    if let Ok(n) = nonce() {
        if let Ok(next) = start_successor(app, &p.installed, &n, &p.from.sha256, true) {
            next.release();
        }
    }
}

/// What `accept` found at startup that `accepted` acts on once the app's state exists.
#[must_use = "pass it to `accepted` after the app's state is managed"]
pub struct Accepting {
    /// Windows: why this version takes no start digest (`recover`), if so.
    #[cfg(windows)]
    not_started: Option<String>,
}

/// At startup, after the profile lock and before the app's state exists, the managed core starts
/// or anything reads the core that came with the app: the version an install started accepts
/// itself once the installed copy is the one it pinned, which the waiting earlier version reads.
/// On Windows, first, a replace of the program set that stopped part way is undone or finished
/// (`recover`), so the core beside the program is one set's. Nothing here reads the app's state;
/// what needs it, `accepted` does once it is managed.
pub fn accept(app: &AppHandle) -> Accepting {
    #[cfg(windows)]
    let accepting = Accepting {
        not_started: recover(app),
    };
    #[cfg(not(windows))]
    let accepting = Accepting {};
    accept_pending(app);
    accepting
}

/// At startup, once the app's state is managed and before this version's start digest is taken
/// (`record_started_from`, called after this): on Windows, a replace `recover` could not finish
/// keeps this version from taking a start digest. What an earlier install recorded that it left
/// (`leftovers.rs`: on macOS its staging folders, on Windows what it left beside the installed
/// copy) is removed once the earlier version has ended; nothing else is looked at.
pub fn accepted(app: &AppHandle, accepting: Accepting) {
    #[cfg(windows)]
    if let Some(why) = accepting.not_started {
        let _ = app.state::<App>().updater.started_from.set(Err(why));
    }
    #[cfg(not(windows))]
    let Accepting {} = accepting;
    // An AppImage's replace leaves nothing beside it.
    #[cfg(target_os = "linux")]
    let _ = app;
    #[cfg(not(target_os = "linux"))]
    if gate(app).is_ok() && format::here(app).is_some() {
        if let Ok(dir) = dir(app) {
            let app = app.clone();
            std::thread::spawn(move || {
                earlier_ended(&app);
                format::cleanup(&dir);
            });
        }
    }
}

/// The pending install's acceptance (`accept`), when this version is the one it installed.
fn accept_pending(app: &AppHandle) {
    let Ok(dir) = dir(app) else { return };
    let Some(p) = read_json::<Pending>(&dir.join("pending.json")) else {
        return;
    };
    let running = app.package_info().version.to_string();
    // A put-back is not a version accepting itself (`recover` finishes one).
    if p.kind == "put-back"
        || running != p.to.version
        || format::here(app).as_deref() != Some(p.installed.as_path())
        || format::digest(&p.installed).ok().as_deref() != Some(p.to.sha256.as_str())
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
    #[cfg(windows)]
    let product = app.package_info().name.clone();
    let finish = move || {
        // Windows: Settings > Apps and the setup program read the installed version from the
        // uninstall entry the setup wrote, if it wrote one for this folder; this version is the
        // installed one now. Written before the outcome, which the waiting version reads.
        #[cfg(windows)]
        if let Err(e) = uninstall_entry::record(&product, &p.installed, &running) {
            eprintln!("pluxy-app: the uninstall entry's version was not updated: {e}");
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
    };
    // The native test holds the moment before acceptance open (`<dir>/hold-gap`), while the
    // earlier version still waits and this one already answers later launches, to launch the
    // app a third time in it.
    #[cfg(feature = "native-test")]
    if let Some(d) = std::env::var_os("PLUXY_APP_TEST_UPDATE")
        .map(PathBuf::from)
        .filter(|d| d.join("hold-gap").exists())
    {
        std::thread::spawn(move || {
            let _ = fs::write(d.join("gap"), std::process::id().to_string());
            eprintln!(
                "native-test: update_gap {{\"pid\":{},\"run\":\"{}\"}}",
                std::process::id(),
                crate::update_native::run_id()
            );
            let until = Instant::now() + Duration::from_secs(60);
            while d.join("hold-gap").exists() && Instant::now() < until {
                std::thread::sleep(Duration::from_millis(100));
            }
            finish();
        });
        return;
    }
    finish();
}

/// Windows, at startup: what `pending.json` left part way for this install's program set, undone
/// or finished by its move list (`set::recover`, which says when). Until a record of this
/// install's that this start could not finish is resolved, this version takes no start digest,
/// so nothing is kept or installed (the mixed set is not this version), and the page says why:
/// returned, for `accepted` to record once the app's state exists.
#[cfg(windows)]
fn recover(app: &AppHandle) -> Option<String> {
    let (Ok(dir), Some(here)) = (dir(app), format::here(app)) else {
        return None;
    };
    let p = read_json::<Pending>(&dir.join("pending.json"))?;
    let exe = exe::program_name();
    let running = app.package_info().version.to_string();
    let r = set::recover(
        &exe::Moving::new(&dir),
        &dir,
        &p,
        &here,
        &exe,
        &running,
        &here.join(&exe),
    );
    if let set::Recovery::Undone | set::Recovery::Left(_) = &r {
        eprintln!(
            "pluxy-app: the {} to {} stopped part way and {}",
            p.kind,
            p.to.version,
            match &r {
                set::Recovery::Left(why) => format!("is left as it is: {why}"),
                _ => "was undone".into(),
            }
        );
    }
    let (last, not_started) = recovered(&r, &p, &running, &here);
    if let Some(detail) = last {
        let _ = write_json(
            &dir.join("last.json"),
            &Last {
                result: "failed".into(),
                detail,
            },
        );
    }
    not_started
}

/// What a start says after `set::recover` (r, for the pending install p, running version
/// running, in the folder here): what `last.json` then records, and why this version takes no
/// start digest (and so installs nothing until a restart), if so.
#[cfg_attr(not(windows), allow(dead_code))]
fn recovered(
    r: &set::Recovery,
    p: &Pending,
    running: &str,
    here: &Path,
) -> (Option<String>, Option<String>) {
    match r {
        set::Recovery::Nothing | set::Recovery::Undone => (None, None),
        set::Recovery::PutBack => (
            Some(format!(
                "{} did not start, and {} was put back.",
                p.from.version, p.to.version
            )),
            (running != p.to.version).then(|| {
                format!(
                    "version {} was put back in place of this one ({running}); restart the app \
                     to run it",
                    p.to.version
                )
            }),
        ),
        set::Recovery::Left(why) => (
            Some(format!(
                "The {} to {} stopped part way and could not be finished: {why}. Its parts are \
                 still aside in {}; the app's README says how to put them back.",
                p.kind,
                p.to.version,
                here.display()
            )),
            Some(format!(
                "an earlier {} stopped part way and is not finished ({why})",
                p.kind
            )),
        ),
    }
}

/// Returns once the version this one took over from has ended (or after two minutes): on Windows
/// once this app holds the profile lock, which that version held to its end; on macOS once this
/// process is no longer that version's child (`still_its_child`).
#[cfg(not(target_os = "linux"))]
fn earlier_ended(app: &AppHandle) {
    let deadline = Instant::now() + Duration::from_secs(120);
    #[cfg(windows)]
    while !app
        .try_state::<crate::instance::Instance>()
        .is_some_and(|i| i.holds_lock())
        && Instant::now() < deadline
    {
        std::thread::sleep(Duration::from_millis(200));
    }
    #[cfg(target_os = "macos")]
    {
        let _ = app;
        let started_by = std::env::var_os(crate::instance::LOCK_FD_ENV).is_some();
        // SAFETY: getppid has no preconditions.
        let parent = unsafe { libc::getppid() };
        while still_its_child(started_by, parent, unsafe { libc::getppid() })
            && Instant::now() < deadline
        {
            std::thread::sleep(Duration::from_millis(200));
        }
    }
}

/// macOS: whether this process, started by an update or a rollback (`started_by`), is still the
/// child of the version that started it: its parent now (`now`) is the one it read first
/// (`first`), and is not launchd (process 1). macOS has no subreapers, so a process whose parent
/// has ended is launchd's: so is one a restore's wait loop starts once the earlier version has
/// ended, or one whose parent ended before it first looked.
#[cfg(any(target_os = "macos", test))]
fn still_its_child(started_by: bool, first: i32, now: i32) -> bool {
    started_by && now != 1 && now == first
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
    let installed = provenance(&app)?;
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
    if format::digest(&kept_path(&dir, &target)).ok().as_deref() != Some(target.sha256.as_str()) {
        return Err(format!(
            "The kept copy of {} no longer has its pinned digest; it is not used.",
            target.version
        ));
    }
    let st = app.state::<App>();
    claim(
        &st.updater.task,
        &st.updater.cancel,
        Task {
            phase: "confirming",
            version: Some(target.version.clone()),
            ..Task::default()
        },
    )?;
    emit_task(&app);
    let asked = confirm_rollback(app.clone(), installed, dir, running, target);
    if asked.is_err() {
        set_task(&app, |t| *t = idle());
    }
    asked
}

fn confirm_rollback(
    app: AppHandle,
    installed: PathBuf,
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
             It replaces {} with the kept copy of {} ({} {}), then restarts the app.",
            target.version,
            installed.display(),
            target.version,
            format::DIGEST,
            target.sha256
        ),
        chain: vec![package.clone()],
        executor: package,
        native_tool: false,
        files: vec![],
        launch: None,
        environment: None,
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
        match roll_back(&app, &installed, &dir, &running, &target) {
            Ok(restart) => restart_into(&app, restart),
            Err(e) => set_task(&app, |t| {
                t.phase = "failed";
                t.error = Some(e);
            }),
        }
    });
    Ok(())
}

fn roll_back(
    app: &AppHandle,
    installed: &Path,
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
    let staged =
        format::stage_kept(installed, &kept_path(dir, target), target, dir).map_err(|_| {
            format!(
                "The kept copy of {} no longer has its pinned digest; it is not used.",
                target.version
            )
        })?;
    set_task(app, |t| {
        t.phase = "installing";
        t.error = None;
    });
    let from = keep(dir, installed, running, &started_from(app)?)?;
    let pending = pending(
        dir,
        "rollback",
        from,
        target.clone(),
        installed,
        moves(&staged),
    )?;
    if let Err(e) = format::swap_in(installed, staged, dir) {
        failed(dir, &pending);
        return Err(format!("The rollback could not be installed: {e}"));
    }
    evict(dir);
    Ok(Restart {
        dir: dir.to_path_buf(),
        pending,
    })
}

#[cfg(test)]
pub(super) mod tests {
    use super::*;

    pub(crate) fn temp(name: &str) -> PathBuf {
        let tmp = std::env::temp_dir().join(format!("pluxy-updater-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&tmp);
        fs::create_dir_all(&tmp).unwrap();
        tmp
    }

    /// Writes an installed copy of this format that is version v: different contents per v.
    pub(crate) fn installed(tmp: &Path, v: &str) -> PathBuf {
        format::tests::fixture(tmp, v)
    }

    #[test]
    fn keeping_pins_the_running_copy_and_keeps_three() {
        let tmp = temp("keep");
        let mut pins = vec![];
        let mut last = PathBuf::new();
        for v in ["0.1.0", "0.2.0", "0.3.0", "0.4.0"] {
            let image = installed(&tmp, v);
            let d = format::digest(&image).unwrap();
            pins.push(keep(&tmp.join("updates"), &image, v, &d).unwrap());
            evict(&tmp.join("updates"));
            last = image;
        }
        let h = history(&tmp.join("updates"));
        assert_eq!(h.kept, pins[1..].iter().rev().cloned().collect::<Vec<_>>());
        assert!(!kept_path(&tmp.join("updates"), &pins[0]).exists());
        let newest = &pins[3];
        assert_eq!(newest.sha256, format::digest(&last).unwrap());
        assert_eq!(
            format::digest(&kept_path(&tmp.join("updates"), newest)).unwrap(),
            newest.sha256
        );
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A copy replaced after this version started is not kept under this version's name.
    #[test]
    fn keeping_refuses_a_copy_this_version_did_not_start_from() {
        let tmp = temp("keep-other");
        let image = installed(&tmp, "replaced since");
        let e = keep(
            &tmp.join("updates"),
            &image,
            "0.1.0",
            &sha256(b"started from"),
        )
        .unwrap_err();
        assert!(
            e.contains(&format!("not the {} this version", format::WHAT)),
            "{e}"
        );
        assert!(history(&tmp.join("updates")).kept.is_empty());
        fs::remove_dir_all(&tmp).unwrap();
    }

    #[test]
    fn restore_puts_back_only_a_copy_with_its_pinned_digest() {
        let tmp = temp("restore");
        // The staging folder's home folder (`app.rs`'s private staging).
        #[cfg(target_os = "macos")]
        format::tests::home_at(&tmp);
        let dir = tmp.join("updates");
        let image = installed(&tmp, "old");
        let old = format::digest(&image).unwrap();
        let from = keep(&dir, &image, "0.1.0", &old).unwrap();
        let to = Pinned {
            version: "0.2.0".into(),
            sha256: "new".into(),
        };
        let p = pending(&dir, "update", from.clone(), to.clone(), &image, None).unwrap();
        installed(&tmp, "new");
        restore(&dir, &p).unwrap();
        assert_eq!(format::digest(&image).unwrap(), old);
        assert!(!dir.join("pending.json").exists());
        // A changed kept copy is not put back, and the message names it.
        let new = format::digest(&installed(&tmp, "new")).unwrap();
        format::tests::tamper(&kept_path(&dir, &from));
        let e = restore(&dir, &p).unwrap_err();
        assert!(
            e.contains(&kept_path(&dir, &from).display().to_string()),
            "{e}"
        );
        assert_eq!(format::digest(&image).unwrap(), new);
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A rollback waiting for its confirmation holds the task: Update Now and another rollback
    // are refused until it is settled.
    #[test]
    fn a_waiting_rollback_holds_the_task() {
        let task = Mutex::new(idle());
        let cancel = AtomicBool::new(false);
        claim(
            &task,
            &cancel,
            Task {
                phase: "confirming",
                ..Task::default()
            },
        )
        .unwrap();
        for phase in ["downloading", "confirming"] {
            let e = claim(
                &task,
                &cancel,
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
            &cancel,
            Task {
                phase: "downloading",
                ..Task::default()
            },
        )
        .unwrap();
    }

    // Update Now clears an old cancel inside the claim, and a refused claim leaves the running
    // download's cancel alone: a cancel cannot be lost between the claim and the clearing.
    #[test]
    fn a_claim_clears_the_cancel_only_when_it_takes_the_task() {
        let task = Mutex::new(idle());
        let cancel = AtomicBool::new(true);
        let download = || Task {
            phase: "downloading",
            ..Task::default()
        };
        claim(&task, &cancel, download()).unwrap();
        assert!(!cancel.load(Ordering::SeqCst), "the old cancel was cleared");
        cancel.store(true, Ordering::SeqCst);
        claim(&task, &cancel, download()).unwrap_err();
        assert!(
            cancel.load(Ordering::SeqCst),
            "a refused claim keeps the cancel"
        );
    }

    // A failed replace must not cost the oldest kept version: the history and the copies change
    // only in `evict`, which runs after the installed copy was replaced.
    #[test]
    fn keeping_deletes_nothing_until_the_replace_has_worked() {
        let tmp = temp("keep-evict");
        let dir = tmp.join("updates");
        let mut pins = vec![];
        for v in ["0.1.0", "0.2.0", "0.3.0", "0.4.0"] {
            let image = installed(&tmp, v);
            let d = format::digest(&image).unwrap();
            pins.push(keep(&dir, &image, v, &d).unwrap());
        }
        // The install of 0.5.0 failed at replace: no evict ran.
        assert_eq!(history(&dir).kept.len(), 4);
        assert!(pins.iter().all(|p| kept_path(&dir, p).exists()));
        evict(&dir);
        assert_eq!(history(&dir).kept.len(), KEEP);
        assert!(!kept_path(&dir, &pins[0]).exists());
        assert!(kept_path(&dir, &pins[1]).exists());
        fs::remove_dir_all(&tmp).unwrap();
    }

    /// A successor that runs for 30 seconds and never accepts. On Windows a copy of PING.EXE in a
    /// folder of the test's own (name): tests run in parallel, and a running copy cannot be
    /// replaced by another test's.
    fn sleeper(name: &str) -> (Successor, PathBuf) {
        #[cfg(unix)]
        {
            let _ = name;
            let s = Successor::start(Path::new("/bin/sleep"), &["30".into()], None, None).unwrap();
            (s, PathBuf::new())
        }
        #[cfg(windows)]
        {
            let tmp = temp(&format!("sleeper-{name}"));
            let exe = tmp.join("ping.exe");
            fs::copy(r"C:\Windows\System32\PING.EXE", &exe).unwrap();
            let env: Vec<(OsString, OsString)> = std::env::vars_os().collect();
            let s = Successor::start(&exe, &["-n".into(), "30".into(), "127.0.0.1".into()], &env)
                .unwrap();
            (s, tmp)
        }
    }

    // A receipt that arrives in the last wait before the deadline counts: the new version is not
    // ended and the earlier one is not put back over it.
    #[test]
    fn a_receipt_at_the_deadline_is_accepted_and_the_successor_not_ended() {
        let (next, _tmp) = sleeper("receipt");
        let calls = std::cell::Cell::new(0);
        let accepted = || {
            calls.set(calls.get() + 1);
            calls.get() >= 2
        };
        let past = Instant::now() - Duration::from_secs(1);
        let r = await_acceptance(next, past, accepted);
        assert_eq!(r, Ok(()));
        assert_eq!(calls.get(), 2, "it was let go at the deadline's last look");
    }

    // macOS: the version started by an update or a rollback waits only while the earlier version
    // is its parent. A successor started after the earlier version ended (a restore's wait loop
    // runs it once that version has ended) or one whose parent ended before it first looked has
    // launchd, process 1, as its parent from the start, and does not wait.
    #[test]
    fn a_successor_whose_parent_is_already_launchd_does_not_wait() {
        assert!(still_its_child(true, 4242, 4242), "its parent still runs");
        assert!(!still_its_child(true, 4242, 1), "its parent ended");
        assert!(
            !still_its_child(false, 4242, 4242),
            "not started by an update"
        );
        assert!(
            !still_its_child(true, 1, 1),
            "orphaned before it first looked"
        );
    }

    #[test]
    fn a_version_that_never_accepts_is_ended_at_the_deadline() {
        let (next, _tmp) = sleeper("never");
        let past = Instant::now() - Duration::from_secs(1);
        let started = Instant::now();
        let r = await_acceptance(next, past, || false);
        assert!(r.unwrap_err().contains("did not accept"));
        // `end` waits for it to have ended: well before its 30 seconds.
        assert!(started.elapsed() < Duration::from_secs(20));
    }
}
