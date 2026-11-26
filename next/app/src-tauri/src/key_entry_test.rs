//! Real native entry input through the UI driver (`ui_driver.rs`); test keys never enter
//! JavaScript.
use super::ui::{self, Key};
use super::*;
use crate::key_entry::text;
use std::sync::Mutex;

/// When the test last saw a key dialog on screen, and last pressed Escape in one. Escape is
/// sent before the app handles it, so the gap between them is never longer than the app's wait.
static DIALOG_SHOWN: Mutex<Option<Instant>> = Mutex::new(None);
static CANCEL_SENT: Mutex<Option<Instant>> = Mutex::new(None);

/// The key that presses the button or check box labelled label (GTK mnemonic form).
pub(super) fn access(label: &str) -> Key {
    Key::Access(crate::confirmation::text::access_key(label).expect("a label with an access key"))
}

/// How long the native input of one key entry may take, the UI Automation check included.
const INPUT_WAIT: Duration = Duration::from_secs(300);

fn answer(
    app: AppHandle,
    key: String,
    keep: bool,
    cancel: bool,
) -> std::thread::JoinHandle<Result<(), String>> {
    std::thread::spawn(move || {
        ui::wait_shown(&app, text::MANAGEMENT_TITLE)?;
        *DIALOG_SHOWN.lock().unwrap() = Some(Instant::now());
        // Read the real native texts and entry; expected origin comes from the running
        // connection, independently of the dialog's preparation code.
        let view = app.state::<App>().conn.lock().unwrap().view();
        let expected = format!("Key for {}", view.target.ok_or("no expected core")?);
        let ca = view.ca_file;
        let shown = ui::texts(&app, text::MANAGEMENT_TITLE)?.join("\n");
        let hidden = ui::secret_hidden(&app, text::MANAGEMENT_TITLE)?;
        if !(hidden
            && shown.contains(&expected)
            && ca.is_none_or(|ca| shown.contains(&format!("Certificate authority: {ca}"))))
        {
            return Err(
                "native dialog did not identify the expected core/CA with hidden entry".into(),
            );
        }
        // The entry gets the initial focus.
        ui::type_text(&app, text::MANAGEMENT_TITLE, &key)?;
        // Once, what another process sees of the entry through UI Automation.
        #[cfg(windows)]
        {
            static ONCE: std::sync::Once = std::sync::Once::new();
            let mut result = Ok(());
            ONCE.call_once(|| {
                result = ui::uia_canary(&app, text::MANAGEMENT_TITLE, &key).map(|v| log("uia", v));
            });
            result?;
        }
        if keep {
            ui::press(&app, text::MANAGEMENT_TITLE, access(text::KEEP))?;
        }
        if cancel {
            *CANCEL_SENT.lock().unwrap() = Some(Instant::now());
        }
        ui::press(
            &app,
            text::MANAGEMENT_TITLE,
            if cancel {
                Key::Escape
            } else {
                access(text::OK)
            },
        )?;
        Ok(())
    })
}

// Capture native callbacks (including events), DOM/input values and storage without teaching
// any webview the canaries. Comparison stays native; output is booleans only.
pub(super) fn watch(w: &Webview) -> Result<(), String> {
    w.eval(r#"(() => {
      window.__keyAudit = [];
      const record = (v) => {
        window.__keyAudit.push(JSON.stringify(v));
        if (v?.status && v?.connection) window.__keyOutcome = v;
      };
      const callbacks = window.__TAURI_INTERNALS__.callbacks;
      const set = callbacks.set.bind(callbacks);
      callbacks.set = (id, cb) => set(id, (v) => { record(v); return cb(v); });
      for (const [id, cb] of callbacks) callbacks.set(id, cb);
      const post = MessagePort.prototype.postMessage;
      MessagePort.prototype.postMessage = function(...args) { record(args[0]); return post.apply(this, args); };
      const message = window.postMessage;
      window.postMessage = function(...args) { record(args[0]); return message.apply(this, args); };
      window.addEventListener('message', e => record(e.data), true);
      const snapshot = () => {
        const storage = (s) => { try { return Object.entries(s); } catch { return []; } };
        record([document.documentElement.outerHTML,
          [...document.querySelectorAll('input,textarea')].map(e => e.value),
          storage(localStorage), storage(sessionStorage)]);
      };
      window.__keySnapshot = snapshot;
      snapshot();
      setInterval(snapshot, 100);
    })()"#).map_err(|_| "could not install secret audit".into())
}

fn clean(app: &AppHandle, canaries: &[&str]) -> Result<(), String> {
    let mut count = 0;
    for w in app.webviews().values() {
        clean_webview(w, canaries)?;
        count += 1;
    }
    if count < 2 {
        return Err("secret audit did not inspect main and page webviews".into());
    }
    Ok(())
}

pub(super) fn clean_webview(w: &Webview, canaries: &[&str]) -> Result<(), String> {
    let audit =
        eval(w, "(window.__keySnapshot(), window.__keyAudit)").ok_or("missing secret audit")?;
    let text = serde_json::to_string(&audit).map_err(|_| "invalid secret audit")?;
    if canaries.iter().any(|key| text.contains(key)) {
        return Err("a key reached a webview DOM, storage or callback/event".into());
    }
    Ok(())
}

pub(super) fn enter(
    app: &AppHandle,
    main: &Webview,
    key: &str,
    keep: bool,
    cancel: bool,
) -> Result<Value, String> {
    main.eval("window.__keyResult = null; window.__TAURI_INTERNALS__.invoke('key_entry', {change: null}).then(v => window.__keyResult = v, () => window.__keyResult = {status: 'ipc_error'})")
        .map_err(|_| "could not request key entry")?;
    let input = answer(app.clone(), key.to_string(), keep, cancel);
    // The input first, with a limit of its own: on Windows it includes the UI Automation check, a
    // PowerShell that compiles C#, which a loaded runner can take most of `wait_for`'s limit on.
    // The result's wait then counts only the app's answer.
    let until = Instant::now() + INPUT_WAIT;
    while !input.is_finished() {
        if Instant::now() > until {
            return Err(format!(
                "the native key entry input did not finish within {INPUT_WAIT:?}"
            ));
        }
        beat("the native key entry input");
        std::thread::sleep(Duration::from_millis(200));
    }
    input.join().map_err(|_| "native input panicked")??;
    wait_for("key entry result", || {
        eval(main, "window.__keyResult").filter(|v| !v.is_null())
    })
}

pub(super) fn check(app: &AppHandle, main: &Webview) -> Result<(), String> {
    if app.state::<App>().conn.lock().unwrap().mode() == Some(crate::conn::Mode::Managed) {
        return Ok(());
    }
    let path = std::env::var("PLUXY_APP_TEST_KEY_FILE").map_err(|_| "no native test key file")?;
    let bytes = std::fs::read(path).map_err(|_| "could not read native test key")?;
    let keys: Value = serde_json::from_slice(&bytes).map_err(|_| "invalid native test keys")?;
    let key = keys["key"].as_str().ok_or("missing key")?;
    let wrong = keys["wrong"].as_str().ok_or("missing bad key")?;
    for w in app.webviews().values() {
        watch(w)?;
    }
    // Invalid destinations must return their specific error without asking for a secret.
    main.eval("window.__invalidTarget = null; window.__TAURI_INTERNALS__.invoke('key_entry', {change: {mode: 'remote', url: 'http://example.com'}}).then(v => window.__invalidTarget = v)")
        .map_err(|_| "could not request invalid target")?;
    let invalid = wait_for("pre-dialog target validation", || {
        eval(main, "window.__invalidTarget").filter(|v| !v.is_null())
    })?;
    if invalid["status"] != "error"
        || !invalid["connection"]["error"]
            .as_str()
            .is_some_and(|e| e.contains("https"))
    {
        return Err("invalid target did not return the specific validation reason".into());
    }
    if ui::shown(app, text::MANAGEMENT_TITLE) {
        return Err("invalid destination opened a secret dialog".into());
    }
    let before = app.state::<App>().conn.lock().unwrap().view();
    let settings = app.state::<App>().conn.lock().unwrap().test_settings_path();
    let disk = std::fs::read(&settings).map_err(|_| "no settings")?;
    let bad = enter(app, main, wrong, true, false)?;
    if bad["status"] != "wrong_key" || app.state::<App>().conn.lock().unwrap().view() != before {
        return Err("bad key did not preserve the old connection".into());
    }
    if std::fs::read(&settings).ok().as_ref() != Some(&disk) {
        return Err("bad key changed saved settings".into());
    }
    let cancelled = enter(app, main, key, true, true)?;
    if cancelled["status"] != "cancelled"
        || app.state::<App>().conn.lock().unwrap().view() != before
    {
        return Err("cancel changed the connection".into());
    }
    if std::fs::read(&settings).ok().as_ref() != Some(&disk) {
        return Err("cancel changed saved settings".into());
    }
    let cancelled_at = CANCEL_SENT.lock().unwrap().ok_or("no cancel sent")?;
    // Remove the preloaded key through the real IPC command before the positive control.
    // Success must establish authentication, not merely retain the initial key.
    main.eval("window.__keySignedOut = false; window.__TAURI_INTERNALS__.invoke('sign_out').then(v => window.__keySignedOut = !v.signed_in)")
        .map_err(|_| "could not sign out before native entry")?;
    wait_for("native sign-out", || {
        eval(main, "window.__keySignedOut").filter(|v| v == true)
    })?;
    if app.state::<App>().conn.lock().unwrap().view().signed_in {
        return Err("positive control did not start signed out".into());
    }
    let good = enter(app, main, key, false, false)?;
    // Measured to when the next dialog appeared, not to the end of typing into it.
    let shown = DIALOG_SHOWN.lock().unwrap().ok_or("no key dialog seen")?;
    if shown.saturating_duration_since(cancelled_at) < Duration::from_millis(900) {
        return Err("native key entry reopened without cancellation backoff".into());
    }
    let view = app.state::<App>().conn.lock().unwrap().view();
    if good["status"] != "signed_in" || !view.signed_in || view.remembered {
        return Err("session-only key was not accepted".into());
    }
    if crate::conn::Settings::load(&settings)?.key.is_some() {
        return Err("session key was persisted".into());
    }
    let good = enter(app, main, key, true, false)?;
    let view = app.state::<App>().conn.lock().unwrap().view();
    if good["status"] != "signed_in" || !view.signed_in || !view.remembered {
        return Err("kept key was not accepted".into());
    }
    if crate::conn::Settings::load(&settings)?.key.as_deref() != Some(key) {
        return Err("kept key was not saved".into());
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        if std::fs::metadata(&settings)
            .map_err(|_| "missing settings")?
            .permissions()
            .mode()
            & 0o777
            != 0o600
        {
            return Err("kept key file is not private".into());
        }
    }
    // On Windows a kept key lives in the local, not the roaming, application data.
    #[cfg(windows)]
    {
        let local = app.path().app_local_data_dir().map_err(|e| e.to_string())?;
        if !settings.starts_with(&local) {
            return Err("kept key file is not in the local application data".into());
        }
    }
    if app
        .state::<App>()
        .conn
        .lock()
        .unwrap()
        .caller()
        .call("GET", "/status", None)
        .status
        != 200
    {
        return Err("native key did not authenticate with core".into());
    }
    clean(app, &[key, wrong])?;
    main.eval("location.hash = '#/connection'")
        .map_err(|_| "could not open Where pluxy runs")?;
    // Signed in, the page offers the key again under "Versions and advanced"; it opens the same
    // native window as the button the signed-out page shows.
    wait_for("the key entry button of Where pluxy runs", || {
        eval(main, "[...document.querySelectorAll('button')].some(b => b.textContent === 'Enter it again')")
            .filter(|v| v == true)
    })?;
    if eval(
        main,
        "document.querySelector('input[type=password]') === null",
    ) != Some(json!(true))
    {
        return Err("Where pluxy runs still has a webview password field".into());
    }
    fn button(app: &AppHandle, main: &Webview, key: &str, cancel: bool) -> Result<Value, String> {
        main.eval("window.__keyOutcome = null; [...document.querySelectorAll('button')].find(b => b.textContent === 'Enter management key').click()")
            .map_err(|_| "could not press the entry button")?;
        answer(app.clone(), key.to_string(), true, cancel)
            .join()
            .map_err(|_| "native input panicked")??;
        wait_for("Connection button result", || {
            eval(main, "window.__keyOutcome").filter(|v| !v.is_null())
        })
    }
    let before_button = app.state::<App>().conn.lock().unwrap().view();
    let refused = button(app, main, wrong, false)?;
    if refused["status"] != "wrong_key"
        || app.state::<App>().conn.lock().unwrap().view() != before_button
    {
        return Err("Connection button accepted a bad key or changed the connection".into());
    }
    main.eval("window.__keySignedOut = false; window.__TAURI_INTERNALS__.invoke('sign_out').then(v => window.__keySignedOut = !v.signed_in)")
        .map_err(|_| "could not sign out before Connection button")?;
    wait_for("signed-out button positive control", || {
        eval(main, "window.__keySignedOut").filter(|v| v == true)
    })?;
    let accepted = button(app, main, key, false)?;
    if accepted["status"] != "signed_in"
        || accepted["connection"]["signed_in"] != true
        || !app.state::<App>().conn.lock().unwrap().view().signed_in
    {
        return Err("Connection button did not establish sign-in".into());
    }

    // Sign-out must immediately return to native entry, never the kit's browser key form.
    main.eval(
        r#"window.__passwordSeen = false;
      new MutationObserver(() => {
        if (document.querySelector('input[type=password]')) window.__passwordSeen = true;
      }).observe(document.body, {subtree: true, childList: true});
      document.querySelector('button.signout').click();"#,
    )
    .map_err(|_| "could not sign out through the window")?;
    wait_for("signed-out native entry", || {
        eval(
            main,
            "[...document.querySelectorAll('button')].some(b => b.textContent === 'Enter management key')",
        )
        .filter(|v| v == true)
    })?;
    if eval(
        main,
        "window.__passwordSeen || document.querySelector('input[type=password]') !== null",
    ) != Some(json!(false))
    {
        return Err("sign-out exposed a webview key field".into());
    }
    // Later checks (page ingress) need the signed-in window: sign in again natively.
    let again = button(app, main, key, false)?;
    if again["status"] != "signed_in" || !app.state::<App>().conn.lock().unwrap().view().signed_in {
        return Err("native sign-in after window sign-out failed".into());
    }

    clean_webview(main, &[key, wrong])?;
    // Nothing typed into the entry reached the clipboard.
    if ui::clipboard(app)
        .0
        .is_some_and(|c| c.contains(key) || c.contains(wrong))
    {
        return Err("a typed key reached the clipboard".into());
    }

    log(
        "keyentry",
        json!({"good": true, "bad_preserved": true, "cancel_preserved": true,
        "session": true, "remember": true, "webviews_clean": true, "events_clean": true}),
    );
    Ok(())
}

/// Off Linux: the window that key entry is shown on goes while the entry is open (closed to the
/// tray). The entry gives the cancel answer and closes with it, key entry can be asked for again,
/// and the connection is as it was. Returns the window's webview once it is open again.
#[cfg(not(target_os = "linux"))]
pub(super) fn owner_gone(app: &AppHandle, main: &Webview) -> Result<Webview, String> {
    if app.state::<App>().conn.lock().unwrap().mode() == Some(crate::conn::Mode::Managed) {
        return Ok(main.clone());
    }
    let before = app.state::<App>().conn.lock().unwrap().view();
    main.eval("window.__TAURI_INTERNALS__.invoke('key_entry', {change: null}).catch(() => {})")
        .map_err(|_| "could not request key entry")?;
    ui::wait_shown(app, text::MANAGEMENT_TITLE)?;
    app.get_window(WINDOW)
        .ok_or("no window")?
        .close()
        .map_err(|e| e.to_string())?;
    wait_for("the window to close to the tray", || {
        app.get_window(WINDOW).is_none().then_some(())
    })?;
    // The cancel answer ends the request: key entry is free again, and its window has gone.
    wait_for("key entry to end with its window", || {
        let open = app.state::<App>().key_entry_open.load(Ordering::SeqCst);
        (!open && ui::count(app, text::MANAGEMENT_TITLE) == 0).then_some(())
    })?;
    let after = app.state::<App>().conn.lock().unwrap().view();
    let unchanged = after.signed_in == before.signed_in && after.remembered == before.remembered;
    let reopen = app.clone();
    app.run_on_main_thread(move || {
        let _ = crate::open_window(&reopen);
    })
    .map_err(|_| "could not open the window again")?;
    let main = wait_for("the window", || app.get_webview(MAIN))?;
    wait_for("the window's shell", || {
        eval(&main, "document.querySelector('[data-part=nav]') !== null").filter(|v| v == true)
    })?;
    watch(&main)?;
    log(
        "owner_gone",
        json!({"entry_closed": true, "free_again": true, "connection_unchanged": unchanged}),
    );
    Ok(main)
}
