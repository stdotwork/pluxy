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
    /// idle, downloading, verifying, installing, restarting, failed or cancelled.
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
            "downloading" | "verifying" | "installing" | "restarting"
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

/// The restart a finished install asked for, done once the app has quit (`lib.rs`).
pub struct Restart {
    dir: PathBuf,
    pending: Pending,
}

pub struct State {
    /// Why the updater plugin was not registered at startup; Ok when it was.
    plugin: Result<(), String>,
    task: Mutex<Task>,
    cancel: AtomicBool,
    pub restart: Mutex<Option<Restart>>,
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
            restart: Mutex::new(None),
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

fn emit(app: &AppHandle) {
    let _ = app.emit_to(EventTarget::webview(MAIN), EVENT, view(app));
}

fn set_task(app: &AppHandle, f: impl FnOnce(&mut Task)) {
    f(&mut app.state::<App>().updater.task.lock().unwrap());
    emit(app);
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
    {
        let st = app.state::<App>();
        let mut t = st.updater.task.lock().unwrap();
        if t.running() {
            return Err("An update is already running.".into());
        }
        *t = Task {
            phase: "downloading",
            version: Some(release.version.clone()),
            downloaded: 0,
            total: Some(artifact.size),
            error: None,
        };
        st.updater.cancel.store(false, Ordering::SeqCst);
    }
    emit(&app);
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

/// "Cancel Download": stops a download; once the update is checked and installing, it finishes.
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
    let from = keep(&dir, appimage, &app.package_info().version.to_string())?;
    let to = Pinned {
        version: release.version.clone(),
        sha256: sha256(&bytes),
    };
    let pending = pending(&dir, "update", from, to.clone(), appimage)?;
    provenance(app)?;
    if let Err(e) = update.install(&bytes) {
        let _ = fs::remove_file(dir.join("pending.json"));
        return Err(format!("The update could not be installed: {e}"));
    }
    let now = fs::read(appimage).map(|b| sha256(&b)).unwrap_or_default();
    if now != to.sha256 {
        restore(&dir, &pending);
        return Err(
            "The installed AppImage is not the update; the earlier one was put back.".into(),
        );
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

/// Keeps a copy of the AppImage this version runs from, pinned by its digest, as the version
/// to go back to; returns that pin.
fn keep(dir: &Path, appimage: &Path, version: &str) -> Result<Pinned, String> {
    let bytes = fs::read(appimage).map_err(|e| format!("{}: {e}", appimage.display()))?;
    let pin = Pinned {
        version: version.into(),
        sha256: sha256(&bytes),
    };
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

/// Replaces the AppImage with bytes in one step, executable by this user only.
fn replace(appimage: &Path, bytes: &[u8]) -> Result<(), String> {
    use std::io::Write;
    use std::os::unix::fs::OpenOptionsExt;
    let parent = appimage.parent().ok_or("The AppImage has no folder.")?;
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
            f.sync_all()
        })
        .and_then(|_| fs::rename(&tmp, appimage));
    if let Err(e) = written {
        let _ = fs::remove_file(&tmp);
        return Err(format!("{}: {e}", appimage.display()));
    }
    Ok(())
}

/// Puts the kept copy of the version an install came from back, if it still has its digest.
fn restore(dir: &Path, p: &Pending) -> Option<String> {
    let bytes = fs::read(kept_path(dir, &p.from)).ok();
    let outcome = match bytes {
        Some(b) if sha256(&b) == p.from.sha256 => replace(&p.appimage, &b).err(),
        _ => Some(format!(
            "the kept copy of {} is missing or changed",
            p.from.version
        )),
    };
    let _ = fs::remove_file(dir.join("pending.json"));
    outcome
}

/// After an install: shows it, then quits the app as Quit does; once it has quit, `supervise`
/// starts the new version.
fn restart_into(app: &AppHandle, restart: Restart) {
    set_task(app, |t| t.phase = "restarting");
    *app.state::<App>().updater.restart.lock().unwrap() = Some(restart);
    crate::quit(app);
}

/// Runs in the quitting app, its tools and core already stopped and its profile lock dropped:
/// starts the installed AppImage and waits for it to accept itself; if it ends or does not
/// accept in time, puts the earlier version back and starts that.
pub fn supervise(r: Restart) {
    use std::os::unix::process::CommandExt;
    let receipt = r.dir.join("accepted.json");
    let _ = fs::remove_file(&receipt);
    let args: Vec<OsString> = std::env::args_os().skip(1).collect();
    let start = |path: &Path| {
        std::process::Command::new(path)
            .args(&args)
            .process_group(0)
            .spawn()
    };
    let accepted = match start(&r.pending.appimage) {
        Ok(mut child) => {
            let deadline = Instant::now() + ACCEPT_WITHIN;
            loop {
                if read_json::<Accepted>(&receipt).is_some_and(|a| {
                    a.nonce == r.pending.nonce && a.version == r.pending.to.version
                }) {
                    break Ok(());
                }
                if let Ok(Some(status)) = child.try_wait() {
                    break Err(format!("it ended ({status}) before accepting the update"));
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
    let Err(why) = accepted else {
        return;
    };
    eprintln!("pluxy-app: {} did not start: {why}", r.pending.to.version);
    let p = &r.pending;
    let detail = match restore(&r.dir, p) {
        None => format!(
            "{} did not start ({why}); {} was put back.",
            p.to.version, p.from.version
        ),
        Some(e) => format!(
            "{} did not start ({why}), and {} could not be put back: {e}. Install it again by hand.",
            p.to.version, p.from.version
        ),
    };
    let _ = write_json(
        &r.dir.join("last.json"),
        &Last {
            result: "failed".into(),
            detail,
        },
    );
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
    let wait_for = if extracted {
        // SAFETY: getppid has no preconditions.
        unsafe { libc::getppid() }
    } else {
        std::process::id() as i32
    };
    let _ = std::process::Command::new("/bin/sh")
        .arg("-c")
        .arg(r#"p=$1; a=$2; shift 2; while kill -0 "$p" 2>/dev/null; do sleep 0.2; done; exec "$a" "$@""#)
        .arg("sh")
        .arg(wait_for.to_string())
        .arg(&p.appimage)
        .args(&args)
        .process_group(0)
        .spawn();
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
/// confirmation. Returns once the confirmation shows.
#[tauri::command]
pub fn app_update_rollback(
    webview: Webview,
    version: String,
    sha256: String,
) -> Result<(), String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
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
    if app.state::<App>().updater.task.lock().unwrap().running() {
        return Err("An update is already running.".into());
    }
    confirm_rollback(app, appimage, dir, running, target)
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
            emit(&app);
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
    // Checked again now that it was confirmed: the copy and the installed file.
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
        t.version = Some(target.version.clone());
        t.error = None;
    });
    let from = keep(dir, appimage, running)?;
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
        let tmp = std::env::temp_dir().join(format!("pluxy-updater-test-{}", std::process::id()));
        let _ = fs::remove_dir_all(&tmp);
        fs::create_dir_all(&tmp).unwrap();
        let image = tmp.join("app.AppImage");
        let mut pins = vec![];
        for v in ["0.1.0", "0.2.0", "0.3.0", "0.4.0"] {
            fs::write(&image, format!("AppImage {v}")).unwrap();
            pins.push(keep(&tmp.join("updates"), &image, v).unwrap());
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

    #[test]
    fn restore_puts_back_only_a_copy_with_its_pinned_digest() {
        let tmp =
            std::env::temp_dir().join(format!("pluxy-updater-restore-{}", std::process::id()));
        let _ = fs::remove_dir_all(&tmp);
        fs::create_dir_all(&tmp).unwrap();
        let dir = tmp.join("updates");
        let image = tmp.join("app.AppImage");
        fs::write(&image, b"old").unwrap();
        let from = keep(&dir, &image, "0.1.0").unwrap();
        let to = Pinned {
            version: "0.2.0".into(),
            sha256: sha256(b"new"),
        };
        let p = pending(&dir, "update", from.clone(), to.clone(), &image).unwrap();
        fs::write(&image, b"new").unwrap();
        assert_eq!(restore(&dir, &p), None);
        assert_eq!(fs::read(&image).unwrap(), b"old");
        assert!(!dir.join("pending.json").exists());
        // A changed kept copy is not put back.
        fs::write(&image, b"new").unwrap();
        fs::write(kept_path(&dir, &from), b"changed").unwrap();
        assert!(restore(&dir, &p).is_some());
        assert_eq!(fs::read(&image).unwrap(), b"new");
        fs::remove_dir_all(&tmp).unwrap();
    }
}
