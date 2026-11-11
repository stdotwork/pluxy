//! Real GTK entry input through XTEST; test keys never enter JavaScript.
use super::*;
use std::process::Command;

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

fn answer(key: String, keep: bool, cancel: bool) -> std::thread::JoinHandle<Result<(), String>> {
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
        Ok(())
    })
}

// Capture native callbacks (including events), DOM/input values and storage without teaching
// any webview the canaries. Comparison stays native; output is booleans only.
fn watch(w: &Webview) -> Result<(), String> {
    w.eval(r#"(() => {
      window.__keyAudit = [];
      const record = (v) => { window.__keyAudit.push(JSON.stringify(v)); };
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
        let audit =
            eval(w, "(window.__keySnapshot(), window.__keyAudit)").ok_or("missing secret audit")?;
        let text = serde_json::to_string(&audit).map_err(|_| "invalid secret audit")?;
        if canaries.iter().any(|key| text.contains(key)) {
            return Err("a key reached a webview DOM, storage or callback/event".into());
        }
        count += 1;
    }
    if count < 2 {
        return Err("secret audit did not inspect main and page webviews".into());
    }
    Ok(())
}

fn enter(main: &Webview, key: &str, keep: bool, cancel: bool) -> Result<Value, String> {
    main.eval("window.__keyResult = null; window.__TAURI_INTERNALS__.invoke('key_entry', {change: null}).then(v => window.__keyResult = v, () => window.__keyResult = {status: 'ipc_error'})")
        .map_err(|_| "could not request key entry")?;
    let input = answer(key.to_string(), keep, cancel);
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
    let before = app.state::<App>().conn.lock().unwrap().view();
    let settings = app.state::<App>().conn.lock().unwrap().test_settings_path();
    let disk = std::fs::read(&settings).map_err(|_| "no settings")?;
    let bad = enter(main, wrong, true, false)?;
    if bad["status"] != "wrong_key" || app.state::<App>().conn.lock().unwrap().view() != before {
        return Err("bad key did not preserve the old connection".into());
    }
    if std::fs::read(&settings).ok().as_ref() != Some(&disk) {
        return Err("bad key changed saved settings".into());
    }
    let cancelled = enter(main, key, true, true)?;
    if cancelled["status"] != "cancelled"
        || app.state::<App>().conn.lock().unwrap().view() != before
    {
        return Err("cancel changed the connection".into());
    }
    if std::fs::read(&settings).ok().as_ref() != Some(&disk) {
        return Err("cancel changed saved settings".into());
    }
    let good = enter(main, key, false, false)?;
    let view = app.state::<App>().conn.lock().unwrap().view();
    if good["status"] != "signed_in" || !view.signed_in || view.remembered {
        return Err("session-only key was not accepted".into());
    }
    if crate::conn::Settings::load(&settings)?.key.is_some() {
        return Err("session key was persisted".into());
    }
    let good = enter(main, key, true, false)?;
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
    main.eval("[...document.querySelectorAll('button')].find(b => b.textContent === 'Enter management key').click()")
        .map_err(|_| "could not press the entry button")?;
    answer(key.to_string(), true, false)
        .join()
        .map_err(|_| "native input panicked")??;
    wait_for("Connection to finish native entry", || {
        eval(main, "[...document.querySelectorAll('button')].some(b => b.textContent === 'Enter management key' && !b.disabled)")
            .filter(|v| v == true)
    })?;
    if !app.state::<App>().conn.lock().unwrap().view().signed_in {
        return Err("Connection button did not sign in".into());
    }

    log(
        "keyentry",
        json!({"good": true, "bad_preserved": true, "cancel_preserved": true,
        "session": true, "remember": true, "webviews_clean": true, "events_clean": true}),
    );
    Ok(())
}
