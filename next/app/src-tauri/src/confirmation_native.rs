//! Fixture actions and XTEST input exist only in native-test builds. The dialog response path
//! is the production GTK handler, and the trusted-gesture path is a real native button click.
use super::*;
use crate::native_test::{eval, wait_for};
use gtk::prelude::*;
use std::os::unix::fs::PermissionsExt;
use std::process::Command;
use std::sync::mpsc;
use tauri::{Manager, Webview};

fn x(args: &[&str]) -> Result<String, String> {
    let out = Command::new("xdotool")
        .args(args)
        .output()
        .map_err(|e| e.to_string())?;
    if !out.status.success() {
        return Err(format!(
            "xdotool {args:?}: {}",
            String::from_utf8_lossy(&out.stderr)
        ));
    }
    Ok(String::from_utf8_lossy(&out.stdout).trim().into())
}
fn window(title: &str) -> Option<String> {
    x(&["search", "--onlyvisible", "--name", &format!("^{title}$")])
        .ok()
        .and_then(|s| s.lines().last().map(String::from))
}
fn key(title: &str, keys: &str) -> Result<(), String> {
    let id = wait_for(title, || window(title))?;
    x(&["windowfocus", "--sync", &id])?;
    x(&["key", "--clearmodifiers", keys])?;
    Ok(())
}
fn require(ok: bool, what: &str) -> Result<(), String> {
    if ok {
        Ok(())
    } else {
        Err(what.into())
    }
}
fn result(app: &tauri::AppHandle, id: &str) -> Result<Option<String>, String> {
    wait_for("native confirmation result", || {
        app.state::<crate::App>()
            .confirmations
            .lock()
            .unwrap()
            .results
            .remove(id)
    })?
}
fn consume(app: &tauri::AppHandle, token: &str) -> Result<(), String> {
    let st = app.state::<crate::App>();
    let mut c = st.confirmations.lock().unwrap();
    let id = c.token_action(token).ok_or("no token action")?.to_string();
    let authorized = c.consume(&id, token)?;
    require(
        authorized.executable().is_some() && authorized.cwd().is_some(),
        "authorized handles missing",
    )?;
    require(c.consume(&id, token).is_err(), "token reused")
}
fn prepare(app: &tauri::AppHandle, a: &Action) -> Result<Prepared, String> {
    app.state::<crate::App>()
        .confirmations
        .lock()
        .unwrap()
        .prepare(a.clone())
}
fn ask(app: &tauri::AppHandle, main: &Webview, a: &Action) -> Result<String, String> {
    let p = prepare(app, a)?;
    let id = app
        .state::<crate::App>()
        .confirmations
        .lock()
        .unwrap()
        .queue(p, false)?;
    main.eval(format!("window.__TAURI_INTERNALS__.invoke('confirmation_request', {{action: {}}}).catch(e => console.error(e))", serde_json::json!(id))).map_err(|e| e.to_string())?;
    wait_for("real GTK confirmation", || {
        window("pluxy native confirmation")
    })?;
    Ok(id)
}
fn confirm(app: &tauri::AppHandle, id: &str, remember: bool) -> Result<String, String> {
    if remember {
        key("pluxy native confirmation", "alt+r")?;
    }
    key("pluxy native confirmation", "alt+o")?;
    result(app, id)?.ok_or("confirmed action returned no token".into())
}
/// Returns a token if remembered, otherwise the ID of the new dialog. Only X input can click.
fn trusted_start(app: &tauri::AppHandle, a: &Action) -> Result<(bool, String), String> {
    let (tx, rx) = mpsc::channel();
    let action = a.clone();
    let handle = app.clone();
    app.run_on_main_thread(move || {
        let w = gtk::Window::new(gtk::WindowType::Toplevel);
        w.set_title("pluxy trusted Start");
        let button = gtk::Button::with_mnemonic("_Start fixture");
        w.add(&button);
        let app = handle.clone();
        let weak = w.downgrade();
        button.connect_clicked(move |_| {
            let r = (|| {
                let st = app.state::<crate::App>();
                let mut c = st.confirmations.lock().unwrap();
                let p = c.prepare(action.clone())?;
                match c.remembered(p, Gesture::native_event()) {
                    Ok(token) => Ok((true, token)),
                    Err(p) => {
                        let id = c.queue(*p, false)?;
                        drop(c);
                        request(&app, id.clone())?;
                        Ok((false, id))
                    }
                }
            })();
            let _ = tx.send(r);
            if let Some(w) = weak.upgrade() {
                w.close();
            }
        });
        w.show_all();
    })
    .map_err(|e| e.to_string())?;
    key("pluxy trusted Start", "alt+s")?;
    rx.recv_timeout(Duration::from_secs(10))
        .map_err(|e| e.to_string())?
}

pub(crate) fn run(app: &tauri::AppHandle, main: &Webview, page: &Webview) -> Result<(), String> {
    let dir = std::env::temp_dir().join(format!(
        "pluxy-native-confirm-{}",
        crate::managed::random_key()
    ));
    fs::create_dir(&dir).map_err(|e| e.to_string())?;
    let checked = (|| {
        let executable = dir.join("fixture-script");
        fs::write(&executable, b"#!/bin/sh\nexit 0\n").map_err(|e| e.to_string())?;
        fs::set_permissions(&executable, fs::Permissions::from_mode(0o700))
            .map_err(|e| e.to_string())?;
        let pkg = Package {
            id: "fixture/confirm".into(),
            source: "native-fixture".into(),
            digest: "a".repeat(64),
            signer: "fixture".into(),
            contract: "1".into(),
            generation: 1,
        };
        let a = Action {
            what: "Start native fixture".into(),
            chain: vec![pkg.clone()],
            executor: pkg.clone(),
            native_tool: true,
            files: vec![],
            launch: Some(Launch {
                executable: executable.clone(),
                argv: vec!["fixture-script".into(), "--exact".into()],
                cwd: dir.clone(),
                terminal_adapter: pkg,
                environment_revision: "1".into(),
                environment_review: vec![],
                secret_uses: vec![],
            }),
        };
        let id = ask(app, main, &a)?;
        // A page-supplied approval message cannot complete an actual pending dialog.
        page.eval(format!(
            "fetch('/send', {{method: 'POST', body: JSON.stringify({{type: 'call', data: {{id: 'spoof-confirm', method: 'confirmation.approve', action: {}, approved: true, remember: true}}}})}}).then(() => window.__approvalMessageSent = true)",
            serde_json::json!(id)
        ))
        .map_err(|e| e.to_string())?;
        page.eval(format!("window.__TAURI_INTERNALS__.invoke('confirmation_request', {{action: {}}}).then(() => window.__confirmSpoof = 'accepted', e => window.__confirmSpoof = String(e))", serde_json::json!(id))).map_err(|e| e.to_string())?;
        wait_for("page approval message delivery", || eval(page, "window.__approvalMessageSent ?? null").filter(|v| v == true))?;
        let refused = wait_for("page refusal", || {
            eval(page, "window.__confirmSpoof ?? null").filter(|v| v.is_string())
        })?;
        require(
            refused.as_str().is_some_and(|s| s.contains("not allowed")),
            "page confirmation command was not refused",
        )?;
        page.eval("window.__TAURI_INTERNALS__.invoke('confirmation_consume', {action: 'forged', token: 'forged', approved: true}).then(() => window.__consumeSpoof = 'accepted', e => window.__consumeSpoof = String(e))").map_err(|e| e.to_string())?;
        let refused = wait_for("page consume refusal", || eval(page, "window.__consumeSpoof ?? null").filter(|v| v.is_string()))?;
        require(refused.as_str().is_some_and(|s| s != "accepted"), "page reached a consumption endpoint")?;
        require(
            app.state::<crate::App>()
                .confirmations
                .lock()
                .unwrap()
                .issued
                .is_empty(),
            "spoof issued a token",
        )?;
        key("pluxy native confirmation", "alt+c")?;
        require(result(app, &id)?.is_none(), "cancel issued token")?;
        let id = ask(app, main, &a)?;
        let t = confirm(app, &id, true)?;
        consume(app, &t)?;
        let (remembered, t) = trusted_start(app, &a)?;
        require(remembered, "exact native gesture did not remember")?;
        require(
            window("pluxy native confirmation").is_none(),
            "remembered launch showed dialog",
        )?;
        consume(app, &t)?;
        let mut other = a.clone();
        other.chain[0].source = "different-source".into();
        let (remembered, id) = trusted_start(app, &other)?;
        require(!remembered, "different chain reused approval")?;
        let t = confirm(app, &id, false)?;
        consume(app, &t)?;
        fs::set_permissions(&executable, fs::Permissions::from_mode(0o500))
            .map_err(|e| e.to_string())?;
        let (remembered, id) = trusted_start(app, &a)?;
        require(!remembered, "metadata change reused approval")?;
        let t = confirm(app, &id, true)?;
        consume(app, &t)?;
        fs::rename(&executable, dir.join("old")).map_err(|e| e.to_string())?;
        fs::write(&executable, b"#!/bin/sh\nexit 1\n").map_err(|e| e.to_string())?;
        fs::set_permissions(&executable, fs::Permissions::from_mode(0o700))
            .map_err(|e| e.to_string())?;
        let (remembered, id) = trusted_start(app, &a)?;
        require(!remembered, "same-path replacement reused approval")?;
        let t = confirm(app, &id, true)?;
        // Real native settings revocation, before this token is consumed.
        settings(app)?;
        key("pluxy remembered launches", "Tab")?;
        // Revoke the newest approval using a real button at a stable mnemonic, assigned below.
        key("pluxy remembered launches", "alt+r")?;
        wait_for("native revocation", || {
            let st = app.state::<crate::App>(); let c = st.confirmations.lock().unwrap();
            (!c.issued.contains_key(&t)).then_some(())
        })?;
        require(consume(app, &t).is_err(), "revoked unconsumed token worked")?;
        key("pluxy remembered launches", "Escape")?;
        // Re-establish an exact approval, then prove page Start cannot consume it.
        let id = ask(app, main, &a)?; let t = confirm(app, &id, true)?; consume(app, &t)?;
        let p = prepare(app, &a)?;
        let id = super::page_start(app, p)?;
        wait_for("page Start dialog", || window("pluxy native confirmation"))?;
        require(app.state::<crate::App>().confirmations.lock().unwrap().issued.is_empty(), "page Start reused remembered approval")?;
        for _ in 0..10 {
            let p = prepare(app, &a)?;
            require(
                super::page_start(app, p).is_err(),
                "page flood was admitted",
            )?;
        }
        require(
            window("pluxy launch notice").is_some(),
            "host-owned launch notice not visible",
        )?;
        key("pluxy native confirmation", "alt+c")?;
        require(
            result(app, &id)?.is_none(),
            "cancelled page Start got token",
        )?;
        crate::native_test::log(
            "confirmations",
            serde_json::json!({"passed": true, "checks": ["confirm", "cancel", "page-spoof", "exact-native-gesture", "different-chain", "metadata", "replacement", "revoke", "page-flood-notice"]}),
        );
        Ok(())
    })();
    let _ = fs::remove_dir_all(dir);
    checked
}
