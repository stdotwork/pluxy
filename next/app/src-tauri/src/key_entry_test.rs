//! Real GTK entry input through XTEST; test keys never enter JavaScript.
use super::*;
use std::process::Command;
use std::sync::Mutex;

/// When the test last saw a key dialog on screen, and last pressed Escape in one. Escape is
/// sent before the app handles it, so the gap between them is never longer than the app's wait.
static DIALOG_SHOWN: Mutex<Option<Instant>> = Mutex::new(None);
static CANCEL_SENT: Mutex<Option<Instant>> = Mutex::new(None);

fn xdotool(args: &[&str]) -> Result<String, String> {
    let out = Command::new("xdotool")
        .args(args)
        .output()
        .map_err(|_| "could not run xdotool")?;
    if !out.status.success() {
        return Err("xdotool failed".into());
    }
    Ok(String::from_utf8_lossy(&out.stdout).trim().to_string())
}

fn answer(
    app: AppHandle,
    key: String,
    keep: bool,
    cancel: bool,
) -> std::thread::JoinHandle<Result<(), String>> {
    std::thread::spawn(move || {
        let id = wait_for("the native key dialog", || {
            xdotool(&[
                "search",
                "--onlyvisible",
                "--name",
                "^pluxy management key$",
            ])
            .ok()
            .filter(|s| !s.is_empty())
        })?;
        *DIALOG_SHOWN.lock().unwrap() = Some(Instant::now());
        // Read real GTK labels and entry properties on the GTK thread; expected origin comes
        // from the running connection, independently of the dialog's preparation code.
        let view = app.state::<App>().conn.lock().unwrap().view();
        let expected = format!("Key for {}", view.target.ok_or("no expected core")?);
        let ca = view.ca_file;
        let (tx, rx) = mpsc::channel();
        app.run_on_main_thread(move || {
            use gtk::prelude::*;
            fn inspect(widget: &gtk::Widget, labels: &mut Vec<String>, hidden: &mut bool) {
                if let Some(label) = widget.downcast_ref::<gtk::Label>() {
                    labels.push(label.text().to_string());
                }
                if let Some(entry) = widget.downcast_ref::<gtk::Entry>() {
                    *hidden = !gtk::prelude::EntryExt::is_visible(entry);
                }
                if let Some(container) = widget.downcast_ref::<gtk::Container>() {
                    for child in container.children() {
                        inspect(&child, labels, hidden);
                    }
                }
            }
            let mut labels = Vec::new();
            let mut hidden = false;
            for widget in gtk::Window::list_toplevels() {
                let Ok(window) = widget.downcast::<gtk::Window>() else {
                    continue;
                };
                if window.title().as_deref() == Some("pluxy management key") {
                    inspect(window.upcast_ref(), &mut labels, &mut hidden);
                }
            }
            let text = labels.join("\n");
            let _ = tx.send(
                hidden
                    && text.contains(&expected)
                    && ca.is_none_or(|ca| text.contains(&format!("Certificate authority: {ca}"))),
            );
        })
        .map_err(|_| "could not inspect native destination")?;
        if !rx
            .recv_timeout(Duration::from_secs(5))
            .map_err(|_| "missing GTK label audit")?
        {
            return Err(
                "native dialog did not identify the expected core/CA with hidden entry".into(),
            );
        }
        let id = id.lines().next().ok_or("no dialog window")?;
        xdotool(&["windowfocus", "--sync", id])?;
        // Entry gets initial focus. Send bytes via stdin, never argv or a shell.
        use std::io::Write;
        let mut child = Command::new("xdotool")
            .args(["type", "--clearmodifiers", "--file", "-"])
            .stdin(std::process::Stdio::piped())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .spawn()
            .map_err(|_| "could not start native input")?;
        child
            .stdin
            .take()
            .ok_or("no input pipe")?
            .write_all(key.as_bytes())
            .map_err(|_| "could not type native input")?;
        if !child.wait().map_err(|_| "native input failed")?.success() {
            return Err("native input failed".into());
        }
        if keep {
            xdotool(&["key", "--clearmodifiers", "alt+k"])?;
        }
        xdotool(&[
            "key",
            "--clearmodifiers",
            if cancel { "Escape" } else { "alt+o" },
        ])?;
        if cancel {
            *CANCEL_SENT.lock().unwrap() = Some(Instant::now());
        }
        Ok(())
    })
}

// Capture native callbacks (including events), DOM/input values and storage without teaching
// any webview the canaries. Comparison stays native; output is booleans only.
fn watch(w: &Webview) -> Result<(), String> {
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

fn clean_webview(w: &Webview, canaries: &[&str]) -> Result<(), String> {
    let audit =
        eval(w, "(window.__keySnapshot(), window.__keyAudit)").ok_or("missing secret audit")?;
    let text = serde_json::to_string(&audit).map_err(|_| "invalid secret audit")?;
    if canaries.iter().any(|key| text.contains(key)) {
        return Err("a key reached a webview DOM, storage or callback/event".into());
    }
    Ok(())
}

fn enter(
    app: &AppHandle,
    main: &Webview,
    key: &str,
    keep: bool,
    cancel: bool,
) -> Result<Value, String> {
    main.eval("window.__keyResult = null; window.__TAURI_INTERNALS__.invoke('key_entry', {change: null}).then(v => window.__keyResult = v, () => window.__keyResult = {status: 'ipc_error'})")
        .map_err(|_| "could not request key entry")?;
    let input = answer(app.clone(), key.to_string(), keep, cancel);
    let result = wait_for("key entry result", || {
        eval(main, "window.__keyResult").filter(|v| !v.is_null())
    });
    input.join().map_err(|_| "native input panicked")??;
    result
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
    if xdotool(&[
        "search",
        "--onlyvisible",
        "--name",
        "^pluxy management key$",
    ])
    .is_ok()
    {
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
        .map_err(|_| "could not open Connection")?;
    wait_for("the Connection entry button", || {
        eval(main, "[...document.querySelectorAll('button')].some(b => b.textContent === 'Enter management key')")
            .filter(|v| v == true)
    })?;
    if eval(
        main,
        "document.querySelector('input[type=password]') === null",
    ) != Some(json!(true))
    {
        return Err("Connection still has a webview password field".into());
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
            "document.querySelector('form[aria-label=Connection]') !== null",
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

    clean_webview(main, &[key, wrong])?;

    log(
        "keyentry",
        json!({"good": true, "bad_preserved": true, "cancel_preserved": true,
        "session": true, "remember": true, "webviews_clean": true, "events_clean": true}),
    );
    Ok(())
}
