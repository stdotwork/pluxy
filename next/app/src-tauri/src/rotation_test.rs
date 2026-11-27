//! Native management-key rotation through the real window, the native confirmation and the
//! native key window, worked through the UI driver (`ui_driver.rs`). Each new key comes
//! independently from core's config file; keys are compared natively, and each one is recorded
//! for the runner's log audit, never sent to a webview.
use super::key_entry_test::{access, clean_webview, enter, watch};
use super::ui::{self, Key};
use super::*;
use crate::client::{certificates, Client};
use crate::conn::{Mode, Settings};
use crate::key_entry::text;
use std::path::{Path, PathBuf};

const CONFIRM: &str = crate::confirmation::text::CONFIRM_TITLE;
const REVEAL: &str = text::REVEAL_TITLE;
const LAUNCH_NOTICE: &str = crate::confirmation::text::NOTICE_TITLE;

/// How each rotation's confirmation must end, by how the key is kept.
const SAYS_CORE: &str = "The app's core saves the new key in its config file. The app shows the new key once in a window of its own.";
const SAYS_DEVICE: &str = "The app keeps the new key on this device, as it kept the old one. The app shows the new key once in a window of its own.";
const SAYS_SESSION: &str = "The app uses the new key until it quits; like the old one, it is not kept on this device. The app shows the new key once in a window of its own.";

fn require(ok: bool, what: &str) -> Result<(), String> {
    if ok {
        Ok(())
    } else {
        Err(what.into())
    }
}

/// Presses key in the window titled title once exactly one is shown, so that a key never answers
/// another window with the same title.
fn press(app: &AppHandle, title: &str, key: Key) -> Result<(), String> {
    wait_for(title, || (ui::count(app, title) == 1).then_some(()))?;
    ui::press(app, title, key)
}

/// The texts of the one shown native window titled title (empty when none is).
fn labels(app: &AppHandle, title: &str) -> Result<String, String> {
    Ok(ui::texts(app, title)?.join("\n"))
}

/// The confirmation's Cancel and Replace key keys. Replace key's access key works only on a button
/// labelled Replace key, so pressing it also checks the button's words.
fn cancel_key() -> Key {
    access(crate::confirmation::text::CANCEL)
}

fn confirm_key() -> Key {
    access(crate::confirmation::text::REPLACE_KEY)
}

/// The connected core, reached directly with a given key, and its config file.
struct Core {
    client: Client,
    config: PathBuf,
}

impl Core {
    fn of(app: &AppHandle) -> Result<Core, String> {
        let view = app.state::<App>().conn.lock().unwrap().view();
        let target = view.target.ok_or("no connected core")?;
        let certs = match &view.ca_file {
            Some(f) => Some(certificates(
                &std::fs::read(f).map_err(|_| "could not read the core's authority")?,
            )?),
            None => None,
        };
        let config = if view.mode == Some(Mode::Managed) {
            // Where lib.rs puts the app's own core's folder.
            #[cfg(windows)]
            let data = app.path().app_local_data_dir();
            #[cfg(not(windows))]
            let data = app.path().app_data_dir();
            data.map_err(|e| e.to_string())?
                .join("core")
                .join("config.json")
        } else {
            PathBuf::from(
                std::env::var("PLUXY_APP_TEST_CORE_CONFIG")
                    .map_err(|_| "no core config for the rotation check")?,
            )
        };
        Ok(Core {
            client: Client::new(&target, certs),
            config,
        })
    }

    /// The key in core's config file: core saves a rotated key there before it replies.
    fn key(&self) -> Result<String, String> {
        crate::managed::management_key(&self.config)
    }

    fn status(&self, key: &str) -> u16 {
        self.client.call(key, "GET", "/status", None).status
    }
}

/// Keeps a key for the runner, which checks that no log line holds it.
fn record(key: &str) -> Result<(), String> {
    use std::io::Write;
    let path = std::env::var("PLUXY_APP_TEST_ROTATED_FILE").map_err(|_| "no rotated-key file")?;
    let mut options = std::fs::OpenOptions::new();
    options.create(true).append(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut f = options
        .open(path)
        .map_err(|_| "could not open the rotated-key file")?;
    writeln!(f, "{key}").map_err(|_| "could not record a rotated key".into())
}

fn app_status(app: &AppHandle) -> u16 {
    let caller = app.state::<App>().conn.lock().unwrap().caller();
    caller.call("GET", "/status", None).status
}

fn kept(settings: &Path) -> Result<Option<String>, String> {
    Ok(Settings::load(settings)?.key)
}

fn start_rotation(main: &Webview) -> Result<(), String> {
    main.eval("window.__rotation = null; window.__TAURI_INTERNALS__.invoke('management_key_rotate').then(v => window.__rotation = v, e => window.__rotation = {ipc_error: String(e)})")
        .map_err(|_| "could not request a rotation".into())
}

fn rotation_outcome(main: &Webview) -> Result<Value, String> {
    wait_for("the rotation's outcome", || {
        eval(main, "window.__rotation").filter(|v| !v.is_null())
    })
}

/// Answers the native confirmation with keys, after checking it names the rotation and core,
/// ends with says, and shows no launch fields.
fn answer(app: &AppHandle, target: &str, says: &str, key: Key) -> Result<(), String> {
    ui::wait_shown(app, CONFIRM)?;
    let text = labels(app, CONFIRM)?;
    require(
        text.contains(&format!("Replace the management key for {target}")),
        "the native confirmation did not name the rotation and its core",
    )?;
    require(
        text.contains(says),
        "the native confirmation did not say how the new key is kept and shown",
    )?;
    require(
        !["digest", "signer", "generation", "launch"]
            .iter()
            .any(|f| text.contains(f)),
        "the native confirmation showed launch fields",
    )?;
    press(app, CONFIRM, key)
}

/// No launch notice tells of a rotation: one is the app's own action, not a launch.
fn no_launch_notice(app: &AppHandle) -> Result<(), String> {
    require(
        !labels(app, LAUNCH_NOTICE)?.contains("management key"),
        "a launch notice appeared for a rotation",
    )
}

/// Waits for the key window of the rotation that replaced before: core's new key, from its config
/// file, in the only key window open (an earlier rotation's window must be gone), saying says.
fn new_key_window(
    app: &AppHandle,
    core: &Core,
    before: &str,
    says: &str,
) -> Result<String, String> {
    let key = wait_for("core's new key", || core.key().ok().filter(|k| k != before))?;
    record(&key)?;
    let shown = wait_for("the key window with core's new key", || {
        ui::texts(app, REVEAL)
            .ok()
            .filter(|w| w.iter().any(|t| t.lines().any(|l| l == key)))
    })?;
    require(
        shown.len() == 1,
        "the key window of an earlier rotation is still open",
    )?;
    require(
        shown[0].contains(says),
        "the key window did not say how the key is kept",
    )?;
    Ok(key)
}

/// Copies the key with the key window's Copy button. Where the platform has a marker for a
/// concealed copy (Windows, macOS), the copy carries it.
fn copy(app: &AppHandle, key: &str) -> Result<(), String> {
    press(app, REVEAL, access(text::COPY))?;
    let concealed = wait_for("the copied key", || {
        let (text, concealed) = ui::clipboard(app);
        text.filter(|t| t == key).map(|_| concealed)
    })?;
    require(
        concealed != Some(false),
        "the copied key is not marked concealed",
    )
}

/// The key the native key window shows for the rotation that replaced before, checked against
/// core's config file, copied with its Copy button and closed; says must be in its text.
fn revealed(app: &AppHandle, core: &Core, before: &str, says: &str) -> Result<String, String> {
    let key = new_key_window(app, core, before, says)?;
    copy(app, &key)?;
    press(app, REVEAL, access(text::CLOSE))?;
    wait_for("the key window to close", || {
        (!ui::shown(app, REVEAL)).then_some(())
    })?;
    Ok(key)
}

fn sign_out(main: &Webview) -> Result<(), String> {
    main.eval("window.__rotationSignedOut = false; window.__TAURI_INTERNALS__.invoke('sign_out').then(v => window.__rotationSignedOut = !v.signed_in)")
        .map_err(|_| "could not sign out")?;
    wait_for("sign-out", || {
        eval(main, "window.__rotationSignedOut").filter(|v| v == true)
    })
    .map(|_| ())
}

fn sign_in(app: &AppHandle, main: &Webview, key: &str, keep: bool) -> Result<(), String> {
    sign_out(main)?;
    let r = enter(app, main, key, keep, false)?;
    let view = app.state::<App>().conn.lock().unwrap().view();
    require(
        r["status"] == "signed_in" && view.signed_in && view.remembered == keep,
        "native sign-in with the rotated key failed",
    )?;
    // The window learns of a sign-in from its own key-entry call, which this one bypasses, and
    // may have shown the signed-out state meanwhile: tell it the connection changed, as the app
    // does, and wait for its signed-in shell. The window takes the change by making its shell
    // anew (`main.tsx`, `key={generation}`), closing the pages the old one had open, so the wait
    // is for a new shell: the old one may still be signed in (`sign_out` above is the window's
    // own call, which does not change what it shows), and a page opened in it is closed again.
    let old_shell = eval(
        main,
        "(window.__rotationNav = document.querySelector('[data-part=nav]')) !== null",
    );
    use tauri::Emitter;
    app.emit_to(tauri::EventTarget::webview(MAIN), "pluxy-connection", ())
        .map_err(|e| e.to_string())?;
    wait_for("the window's new signed-in shell", || {
        eval(
            main,
            "(() => { const n = document.querySelector('[data-part=nav]'); return n !== null && n !== window.__rotationNav; })()",
        )
        .filter(|v| v == true)
    })?;
    log("rotation_sign_in", json!({"old_shell": old_shell}));
    Ok(())
}

/// Makes the folder of connection.json read-only until dropped, so saving the settings fails.
struct ReadOnly(PathBuf);

#[cfg(unix)]
impl ReadOnly {
    fn new(dir: &Path) -> Result<ReadOnly, String> {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o500))
            .map_err(|_| "could not make the settings folder read-only")?;
        Ok(ReadOnly(dir.to_path_buf()))
    }
}

#[cfg(unix)]
impl Drop for ReadOnly {
    fn drop(&mut self) {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&self.0, std::fs::Permissions::from_mode(0o700));
    }
}

/// On Windows a folder's read-only attribute does not stop new files: an entry denying this user
/// the adding of files and the writing of data (`icacls`, Windows' own tool) does, until dropped.
#[cfg(windows)]
impl ReadOnly {
    fn icacls(dir: &Path, args: &[&str]) -> Result<(), String> {
        let user = std::env::var("USERNAME").map_err(|_| "no USERNAME")?;
        let args: Vec<String> = args.iter().map(|a| a.replace("{user}", &user)).collect();
        let out = std::process::Command::new("icacls")
            .arg(dir)
            .args(&args)
            .output()
            .map_err(|e| format!("could not run icacls: {e}"))?;
        if !out.status.success() {
            return Err(format!(
                "icacls {args:?}: {}",
                String::from_utf8_lossy(&out.stdout)
            ));
        }
        Ok(())
    }

    fn new(dir: &Path) -> Result<ReadOnly, String> {
        Self::icacls(dir, &["/deny", "{user}:(AD,WD)"])?;
        Ok(ReadOnly(dir.to_path_buf()))
    }
}

#[cfg(windows)]
impl Drop for ReadOnly {
    fn drop(&mut self) {
        let _ = Self::icacls(&self.0, &["/remove:d", "{user}"]);
    }
}

const REFUSALS: &str = r#"(async () => {
  const inv = window.__TAURI_INTERNALS__.invoke;
  const call = (method, path) => inv('core_call', {method, path, body: null}).catch((e) => ({ipc_error: String(e)}));
  const out = {variants: {}};
  out.plain = await call('POST', '/management-key/rotate');
  for (const path of ['/management-key/%72otate', '/%6Danagement-key/rotate', '/Management-Key/rotate/', '/management-key/rotate?x=1', '//management-key/rotate'])
    out.variants[path] = await call('POST', path);
  out.control = await call('GET', '/status');
  window.__rotationRefusals = out;
})()"#;

/// The window's webview, ready, and watched for keys.
fn window_ready(app: &AppHandle) -> Result<Webview, String> {
    let main = wait_for("the window", || app.get_webview(MAIN))?;
    wait_for("the window's shell", || {
        eval(&main, "document.querySelector('[data-part=nav]') !== null").filter(|v| v == true)
    })?;
    watch(&main)?;
    Ok(main)
}

/// Opens the Config page at its Keys section and presses Replace key. The page asks nothing of its
/// own: the native confirmation, which `answer` waits for, is the one question.
fn press_rotate(main: &Webview) -> Result<(), String> {
    main.eval("location.hash = '#/config/connectivity'")
        .map_err(|_| "could not open Config")?;
    wait_for("the Replace key button", || {
        eval(main, "(() => { const b = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Replace key' && !b.disabled); if (b) b.click(); return !!b; })()")
            .filter(|v| v == true)
    })
    .map(|_| ())
}

/// Returns the window's webview: a new one in local and remote mode, where the window closes to
/// the tray while a key window is open, and opens again.
pub(super) fn check(app: &AppHandle, main: &Webview) -> Result<Webview, String> {
    let core = Core::of(app)?;
    let view = app.state::<App>().conn.lock().unwrap().view();
    let managed = view.mode == Some(Mode::Managed);
    let target = view.target.clone().ok_or("no connected core")?;
    let settings = app.state::<App>().conn.lock().unwrap().test_settings_path();
    let old = core.key()?;
    record(&old)?;
    require(
        core.status(&old) == 200 && app_status(app) == 200,
        "the app is not signed in with core's key",
    )?;

    // core_call never reaches the rotation, however the path is spelled; a status call through
    // the same command is the positive control.
    main.eval(REFUSALS)
        .map_err(|_| "could not try rotating through core_call")?;
    let refusals = wait_for("core_call's rotation refusals", || {
        eval(main, "window.__rotationRefusals ?? null").filter(|v| !v.is_null())
    })?;
    require(core.key()? == old, "core_call rotated the management key")?;
    require(
        refusals["plain"]["status"] == 403 && refusals["plain"]["error"]["code"] == "native_only",
        "core_call did not refuse the rotation as native-only",
    )?;
    let variants = refusals["variants"]
        .as_object()
        .ok_or("no variants tried")?;
    require(variants.len() == 5, "not every spelling was tried")?;
    for (path, reply) in variants {
        require(
            reply["status"] == 403 && reply["error"]["code"] == "native_only",
            &format!("core_call did not refuse the rotation as {path} as native-only"),
        )?;
    }
    require(
        refusals["control"]["status"] == 200,
        "the allowed core_call on the same command failed",
    )?;
    require(
        core.status(&old) == 200,
        "the refused rotations changed core's key",
    )?;

    for w in app.webviews().values() {
        if eval(w, "typeof window.__keySnapshot === 'function'") != Some(json!(true)) {
            watch(w)?;
        }
    }

    // Cancel: nothing changes, and no launch notice follows.
    require(
        managed || view.remembered,
        "the rotation check expects a kept key",
    )?;
    let says = if managed { SAYS_CORE } else { SAYS_DEVICE };
    let disk = std::fs::read(&settings).ok();
    start_rotation(main)?;
    answer(app, &target, says, cancel_key())?;
    let cancelled = rotation_outcome(main)?;
    require(
        cancelled["status"] == 204 && cancelled.get("body").is_none(),
        "a cancelled rotation did not answer 204",
    )?;
    no_launch_notice(app)?;
    require(
        core.key()? == old && core.status(&old) == 200 && app_status(app) == 200,
        "a cancelled rotation changed the key",
    )?;
    require(
        std::fs::read(&settings).ok() == disk,
        "a cancelled rotation changed saved settings",
    )?;

    // Rotate from the Config page's Replace key button, keeping the key as the old one was kept.
    press_rotate(main)?;
    answer(app, &target, says, confirm_key())?;
    let first = revealed(
        app,
        &core,
        &old,
        if managed {
            "saved this management key in its config file"
        } else {
            "keeps it on this device"
        },
    )?;
    no_launch_notice(app)?;
    wait_for("the Config page's rotation notice", || {
        eval(
            main,
            "document.body.innerText.includes('Replaced. The app uses the new key')",
        )
        .filter(|v| v == true)
    })?;
    require(
        eval(
            main,
            "document.querySelector('[data-part=key-value]') === null",
        ) == Some(json!(true)),
        "the Config page showed a key",
    )?;
    require(first != old, "core kept the old key")?;
    require(core.status(&old) == 401, "the old key still works")?;
    require(core.status(&first) == 200, "the new key does not work")?;
    require(
        app_status(app) == 200,
        "the app did not switch to the new key",
    )?;
    require(
        kept(&settings)? == (!managed).then(|| first.clone()),
        "the kept key was not replaced as it was kept",
    )?;
    let mut keys = vec![old, first.clone()];
    let mut main = main.clone();
    let mut audited = 0;

    if !managed {
        // Not kept: it lasts for this session, and the person gets it to sign in again. An
        // extension page is open meanwhile, so its webview is audited too.
        sign_in(app, &main, &first, false)?;
        let page = std::env::var("PLUXY_APP_TEST_PAGE").map_err(|_| "no extension page")?;
        main.eval(format!("location.hash = {}", json!(format!("#/{page}"))))
            .map_err(|_| "could not open the extension page")?;
        let label = wait_for("the extension page's webview", || {
            app.state::<App>()
                .pages
                .lock()
                .unwrap()
                .labels()
                .first()
                .cloned()
        })?;
        // Which pages are bound, and which webviews there are: to tell, on a failure here, the
        // page waited for from the one the window shows.
        let pages_now = || {
            let mut bound = app.state::<App>().pages.lock().unwrap().labels();
            bound.sort();
            let mut webviews: Vec<String> = app.webviews().into_keys().collect();
            webviews.sort();
            json!({"label": label, "bound": bound, "webviews": webviews})
        };
        let page_view = wait_for("the extension page", || app.get_webview(&label))
            .map_err(|e| format!("{e}: {}", pages_now()))?;
        wait_for("the extension page's frame", || {
            eval(&page_view, "document.querySelector('iframe')?.src ?? null")
                .filter(|v| v.is_string())
        })
        .map_err(|e| format!("{e}: {}", pages_now()))?;
        log("rotation_page", pages_now());
        watch(&page_view)?;
        start_rotation(&main)?;
        answer(app, &target, SAYS_SESSION, confirm_key())?;
        let second = new_key_window(app, &core, &first, "not kept on this device")?;
        let outcome = rotation_outcome(&main)?;
        require(
            outcome["status"] == 200 && outcome["body"]["kept"] == "session",
            "a session-only rotation did not say so",
        )?;
        require(
            core.status(&first) == 401 && core.status(&second) == 200 && app_status(app) == 200,
            "the app did not switch to the session-only key",
        )?;
        require(kept(&settings)?.is_none(), "a session-only key was saved")?;
        keys.push(second.clone());
        {
            let canaries: Vec<&str> = keys.iter().map(String::as_str).collect();
            clean_webview(&main, &canaries)?;
            clean_webview(&page_view, &canaries)?;
            audited += 2;
        }

        // The window closes to the tray with the key window open: the key window, the only
        // place the session-only key is shown, stays, and its Copy still works.
        app.get_window(WINDOW)
            .ok_or("no window")?
            .close()
            .map_err(|e| e.to_string())?;
        wait_for("the window to close to the tray", || {
            app.get_window(WINDOW).is_none().then_some(())
        })?;
        let shown = ui::texts(app, REVEAL)?;
        require(
            shown.len() == 1 && shown[0].lines().any(|l| l == second),
            "the key window closed with the main window",
        )?;
        copy(app, &second)?;
        let reopen = app.clone();
        app.run_on_main_thread(move || {
            let _ = crate::open_window(&reopen);
        })
        .map_err(|_| "could not open the window again")?;
        main = window_ready(app)?;

        // A failed save, rotated from the Config page while the last key window is still open:
        // that window goes, and the app keeps working on the new key and says what to do, on
        // the Config page and on the Connection page.
        sign_in(app, &main, &second, true)?;
        let dir = settings.parent().ok_or("no settings folder")?;
        let third = {
            let _locked = ReadOnly::new(dir)?;
            press_rotate(&main)?;
            answer(app, &target, SAYS_DEVICE, confirm_key())?;
            revealed(app, &core, &second, "could not be kept on this device")?
        };
        // The reply the Config page got, from the audit's record of the window's callbacks.
        let outcome = wait_for("the Config page's rotation reply", || {
            eval(&main, "(window.__keyAudit ?? []).filter((s) => s.startsWith('{\"status\"')).map((s) => JSON.parse(s)).find((v) => v.body?.kept !== undefined) ?? null")
                .filter(|v| !v.is_null())
        })?;
        let why = outcome["error"]["message"].as_str().unwrap_or_default();
        require(
            outcome["status"] == 200
                && outcome["body"]["kept"] == "not_saved"
                && outcome["error"]["code"] == "not_saved"
                && why.contains("could not be kept on this device"),
            "a failed save did not explain the recovery",
        )?;
        wait_for("the Config page's recovery notice", || {
            eval(&main, "document.body.innerText.includes('Replaced. The app uses the new key') && document.body.innerText.includes('could not be kept on this device')")
                .filter(|v| v == true)
        })?;
        require(
            eval(
                &main,
                "document.querySelector('[data-part=key-value]') === null",
            ) == Some(json!(true)),
            "the Config page showed a key",
        )?;
        let view = app.state::<App>().conn.lock().unwrap().view();
        require(
            view.error
                .as_deref()
                .is_some_and(|e| e.contains("could not be kept on this device")),
            "the connection does not show the recovery state",
        )?;
        main.eval("location.hash = '#/connection'")
            .map_err(|_| "could not open Connection")?;
        wait_for("the Connection page's recovery state", || {
            eval(
                &main,
                "document.body.innerText.includes('could not be kept on this device')",
            )
            .filter(|v| v == true)
        })?;
        require(
            kept(&settings)? == Some(second.clone()),
            "a failed save changed saved settings",
        )?;
        require(
            core.status(&second) == 401 && core.status(&third) == 200 && app_status(app) == 200,
            "the app did not keep working on the unsaved key",
        )?;
        // The recovery the app gives: enter the shown key again, kept, without signing out.
        let recovered = enter(app, &main, &third, true, false)?;
        require(
            recovered["status"] == "signed_in" && kept(&settings)? == Some(third.clone()),
            "the recovered key was not kept",
        )?;
        require(
            app.state::<App>()
                .conn
                .lock()
                .unwrap()
                .view()
                .error
                .is_none(),
            "the recovery state outlived the recovery",
        )?;
        keys.push(third);
    }

    let canaries: Vec<&str> = keys.iter().map(String::as_str).collect();
    for w in app.webviews().values() {
        if eval(w, "typeof window.__keySnapshot === 'function'") == Some(json!(true)) {
            clean_webview(w, &canaries)?;
            audited += 1;
        }
    }
    require(audited > 0, "no webview was audited for rotated keys")?;
    log(
        "rotation",
        json!({
            "refused": true, "control": true, "cancelled": true, "rotated": true,
            "old_refused": true, "new_works": true, "kept": if managed { "core" } else { "device" },
            "session": !managed, "recovery": !managed, "webviews_clean": true, "audited": audited,
            "page_audited": !managed, "outlived_window": !managed, "replaced": !managed,
            "connection_shown": !managed, "no_launch_notice": true,
        }),
    );
    Ok(main)
}

/// After a restart on the folders of a run that rotated and kept the key: signed in on core's
/// current key, and every key rotated away refused.
pub(super) fn after_restart(app: &AppHandle) -> Result<(), String> {
    wait_for("the window", || app.get_webview(MAIN))?;
    let core = Core::of(app)?;
    let key = core.key()?;
    let view = app.state::<App>().conn.lock().unwrap().view();
    let settings = app.state::<App>().conn.lock().unwrap().test_settings_path();
    require(
        view.signed_in && view.remembered,
        "the kept rotated key did not sign in after a restart",
    )?;
    require(
        kept(&settings)? == Some(key.clone()),
        "the kept key is not core's current key",
    )?;
    require(
        app_status(app) == 200,
        "the app did not reach core after a restart",
    )?;
    let path = std::env::var("PLUXY_APP_TEST_ROTATED_FILE").map_err(|_| "no rotated-key file")?;
    let rotated = std::fs::read_to_string(path).map_err(|_| "no keys from the earlier run")?;
    let before: Vec<&str> = rotated.lines().filter(|k| *k != key).collect();
    require(!before.is_empty(), "the earlier run rotated no key")?;
    require(
        before.iter().all(|k| core.status(k) == 401),
        "a rotated-away key works after a restart",
    )?;
    log(
        "rotation_restart",
        json!({"signed_in": true, "status": 200, "refused": before.len()}),
    );
    crate::quit(app);
    Ok(())
}
