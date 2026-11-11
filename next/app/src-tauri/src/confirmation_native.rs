//! Fixture actions and XTEST input exist only in native-test builds. The dialog response path
//! is the production GTK handler, and the trusted-gesture path is a real native button click.
use super::*;
use crate::native_test::{eval, wait_for};
use gtk::prelude::*;
use std::os::unix::fs::PermissionsExt;
use std::process::Command;
use std::sync::mpsc;
use tauri::{Manager, Webview};
pub(crate) static PAGE_MAPPED_DISABLED: std::sync::atomic::AtomicBool =
    std::sync::atomic::AtomicBool::new(false);
pub(crate) static PAGE_CLAIMS: std::sync::atomic::AtomicUsize =
    std::sync::atomic::AtomicUsize::new(0);

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
pub(crate) fn window(title: &str) -> Option<String> {
    x(&["search", "--onlyvisible", "--name", &format!("^{title}$")])
        .ok()
        .and_then(|s| s.lines().last().map(String::from))
}
pub(crate) fn key(title: &str, keys: &str) -> Result<(), String> {
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
        .prepare(a.clone(), Origin::Main)
}
fn ask(app: &tauri::AppHandle, main: &Webview, a: &Action) -> Result<String, String> {
    let p = prepare(app, a)?;
    let id = app
        .state::<crate::App>()
        .confirmations
        .lock()
        .unwrap()
        .queue(p)?;
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
                let p = c.prepare(action.clone(), Origin::NativeUi)?;
                match c.remembered(
                    p,
                    Gesture {
                        created: Instant::now(),
                    },
                ) {
                    Ok(token) => Ok((true, token)),
                    Err(p) => {
                        let id = c.queue(*p)?;
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
        let snapshot = || {
            let st = app.state::<crate::App>();
            let c = st.confirmations.lock().unwrap();
            (
                c.pending.len(),
                c.issued.len(),
                c.showing.len(),
                c.approvals.len(),
                c.results.len(),
            )
        };
        let before = snapshot();
        // Observe the actual channel response after main has handled the claim, not merely
        // the POST completing at ingress. A valid config.get on the same route is the control.
        main.eval("window.__claimReplies = {}; const originalPost = MessagePort.prototype.postMessage; MessagePort.prototype.postMessage = function(data, ...rest) { if (data?.id === 'spoof-confirm' || data?.id === 'confirm-control') window.__claimReplies[data.id] = data; return originalPost.call(this, data, ...rest); }").map_err(|e| e.to_string())?;
        page.eval("fetch('/frame').then(r => r.json()).then(info => fetch('/send', {method: 'POST', body: JSON.stringify({generation: info.generation, type: 'call', data: {v: 0, id: 'confirm-control', method: 'config.get', params: null}})}))").map_err(|e| e.to_string())?;
        let control = wait_for("positive page channel control", || {
            eval(main, "window.__claimReplies['confirm-control'] ?? null")
                .filter(|v| v.get("result").is_some() || v.get("error").is_some())
        })?;
        require(
            control.get("result").is_some(),
            "positive page channel control failed",
        )?;
        let claims = PAGE_CLAIMS.load(std::sync::atomic::Ordering::SeqCst);
        // A real page channel call carrying an approval claim, on the same route and document
        // as the control, is refused by native ingress (unknown method) and never reaches the
        // window, so it cannot mutate confirmation state or issue a token. Only the GTK
        // response below can do that.
        page.eval(format!(
            "fetch('/frame').then(r => r.json()).then(info => fetch('/send', {{method: 'POST', body: JSON.stringify({{generation: info.generation, type: 'call', data: {{v: 0, id: 'spoof-confirm', method: 'confirmation_request', params: {{action: {}, approved: true, remember: true}}}}}})}})).then(async r => window.__approvalMessage = {{status: r.status, body: await r.json()}})", serde_json::json!(id)
        )).map_err(|e| e.to_string())?;
        let refused = wait_for("page approval claim refused at native ingress", || {
            eval(page, "window.__approvalMessage ?? null").filter(|v| !v.is_null())
        })?;
        require(
            refused["status"] == 400
                && refused["body"]["id"] == "spoof-confirm"
                && refused["body"]["error"]["code"] == "unknown_method",
            "channel approval claim was not refused by native ingress",
        )?;
        std::thread::sleep(std::time::Duration::from_millis(200));
        require(
            PAGE_CLAIMS.load(std::sync::atomic::Ordering::SeqCst) == claims,
            "channel approval claim reached the window",
        )?;
        require(
            eval(main, "window.__claimReplies['spoof-confirm'] ?? null")
                == Some(serde_json::Value::Null),
            "channel approval claim got a window reply",
        )?;
        for command in crate::COMMANDS
            .iter()
            .filter(|c| c.starts_with("confirmation_"))
        {
            page.eval(format!("window.__confirmSpoof = null; window.__TAURI_INTERNALS__.invoke({}, {{action: {}, approved: true}}).then(() => window.__confirmSpoof = 'accepted', e => window.__confirmSpoof = String(e))", serde_json::json!(command), serde_json::json!(id))).map_err(|e| e.to_string())?;
            let refused = wait_for("real page confirmation command refusal", || {
                eval(page, "window.__confirmSpoof ?? null").filter(|v| v.is_string())
            })?;
            require(
                refused.as_str().is_some_and(|s| s.contains("not allowed")),
                "real page confirmation command was not refused",
            )?;
        }
        require(
            snapshot() == before,
            "page approval claim changed native confirmation state",
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
            let st = app.state::<crate::App>();
            let c = st.confirmations.lock().unwrap();
            (!c.issued.contains_key(&t)).then_some(())
        })?;
        require(consume(app, &t).is_err(), "revoked unconsumed token worked")?;
        key("pluxy remembered launches", "Escape")?;
        // Re-establish an exact approval, then prove page Start cannot consume it.
        let id = ask(app, main, &a)?;
        let t = confirm(app, &id, true)?;
        consume(app, &t)?;
        let p = app
            .state::<crate::App>()
            .confirmations
            .lock()
            .unwrap()
            .prepare_page(a.clone())?;
        PAGE_MAPPED_DISABLED.store(false, std::sync::atomic::Ordering::SeqCst);
        let id = super::page_start(app, p)?;
        wait_for("page Start dialog", || window("pluxy native confirmation"))?;
        let (tx, rx) = mpsc::channel();
        app.run_on_main_thread(move || {
            let dialog = gtk::Window::list_toplevels().into_iter().find_map(|w| {
                w.downcast::<gtk::Dialog>()
                    .ok()
                    .filter(|d| d.title().as_deref() == Some("pluxy native confirmation"))
            });
            let state = dialog.map(|d| {
                (
                    PAGE_MAPPED_DISABLED.load(std::sync::atomic::Ordering::SeqCst),
                    d.widget_for_response(gtk::ResponseType::Cancel)
                        .unwrap()
                        .is_focus(),
                )
            });
            let _ = tx.send(state);
        })
        .map_err(|e| e.to_string())?;
        require(
            rx.recv_timeout(Duration::from_secs(5))
                .map_err(|e| e.to_string())?
                == Some((true, true)),
            "page dialog did not delay Confirm and focus Cancel",
        )?;
        require(
            app.state::<crate::App>()
                .confirmations
                .lock()
                .unwrap()
                .issued
                .is_empty(),
            "page Start reused remembered approval",
        )?;
        for _ in 0..10 {
            let p = app
                .state::<crate::App>()
                .confirmations
                .lock()
                .unwrap()
                .prepare_page(a.clone())?;
            require(
                super::page_start(app, p).is_err(),
                "page flood was admitted",
            )?;
        }
        let (tx, rx) = mpsc::channel();
        app.run_on_main_thread(move || {
            let shown = gtk::Window::list_toplevels()
                .into_iter()
                .filter_map(|w| w.downcast::<gtk::Window>().ok())
                .find(|w| w.title().as_deref() == Some("pluxy launch notice") && w.is_visible())
                .and_then(|w| w.child())
                .and_then(|c| c.downcast::<gtk::Label>().ok())
                .map(|l| l.text().to_string());
            let _ = tx.send(shown);
        })
        .map_err(|e| e.to_string())?;
        let shown = rx
            .recv_timeout(Duration::from_secs(5))
            .map_err(|e| e.to_string())?
            .ok_or("host-owned launch notice not visible")?;
        require(
            shown.contains("fixture/confirm")
                && shown.contains("Start native fixture")
                && (shown.contains("per second") || shown.contains("already pending")),
            "notice did not show the refused page Start",
        )?;
        std::thread::sleep(Duration::from_millis(1100));
        let token = confirm(app, &id, false)?;
        consume(app, &token)?;
        crate::native_test::log(
            "confirmations",
            serde_json::json!({"passed": true, "checks": ["confirm", "cancel", "page-spoof", "exact-native-gesture", "different-chain", "metadata", "replacement", "revoke", "page-flood-notice"]}),
        );
        Ok(())
    })();
    let _ = fs::remove_dir_all(dir);
    checked
}
