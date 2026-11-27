//! Fixture actions and native input exist only in native-test builds. The dialog response path
//! is the production native handler (GTK, Win32 or AppKit), worked through the UI driver
//! (`ui_driver.rs`), and the trusted-gesture path is a real native button press.
use super::*;
use crate::native_test::ui::{self, Key};
use crate::native_test::{eval, wait_for};
use std::sync::mpsc;
use tauri::{Manager, Webview};
pub(crate) static PAGE_MAPPED_DISABLED: std::sync::atomic::AtomicBool =
    std::sync::atomic::AtomicBool::new(false);
pub(crate) static PAGE_CLAIMS: std::sync::atomic::AtomicUsize =
    std::sync::atomic::AtomicUsize::new(0);

/// For the Linux-only native checks that press GTK dialogs with xdotool themselves
/// (`native_core.rs`, `release_native.rs`).
#[cfg(target_os = "linux")]
fn x(args: &[&str]) -> Result<String, String> {
    let out = std::process::Command::new("xdotool")
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
#[cfg(target_os = "linux")]
pub(crate) fn window(title: &str) -> Option<String> {
    x(&["search", "--onlyvisible", "--name", &format!("^{title}$")])
        .ok()
        .and_then(|s| s.lines().last().map(String::from))
}
#[cfg(target_os = "linux")]
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
    ui::wait_shown(app, text::CONFIRM_TITLE)?;
    Ok(id)
}
/// The key of the confirmation's button or check box labelled label.
fn access(label: &str) -> Key {
    Key::Access(text::access_key(label).expect("a label with an access key"))
}
fn confirm(app: &tauri::AppHandle, id: &str, remember: bool) -> Result<String, String> {
    if remember {
        // The Remember check box (`Prepared::remember_text`).
        ui::press(app, text::CONFIRM_TITLE, Key::Access('r'))?;
    }
    ui::press(app, text::CONFIRM_TITLE, access(text::CONFIRM))?;
    result(app, id)?.ok_or("confirmed action returned no token".into())
}
/// Returns a token if remembered, otherwise the ID of the new dialog. Only native input can
/// press the fixture's button.
fn trusted_start(app: &tauri::AppHandle, a: &Action) -> Result<(bool, String), String> {
    let (tx, rx) = mpsc::channel();
    let action = a.clone();
    let app2 = app.clone();
    ui::fixture_button(app, "pluxy trusted Start", move || {
        let app = app2;
        let r = (|| {
            let st = app.state::<crate::App>();
            let mut c = st.confirmations.lock().unwrap();
            let p = c.prepare(action.clone(), Origin::NativeUi)?;
            match c.remembered(
                p,
                Gesture {
                    created: Instant::now(),
                    origin: Origin::NativeUi,
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
    })?;
    rx.recv_timeout(Duration::from_secs(10))
        .map_err(|e| e.to_string())?
}

/// Shows a native window titled title with one button and presses it with native input: its
/// press handler calls f with a gesture made there, as a trusted native launcher's would. For
/// other native checks that start a trusted call (`routing_native.rs`).
pub(crate) fn click(
    app: &tauri::AppHandle,
    title: &str,
    f: impl FnOnce(Gesture) + Send + 'static,
) -> Result<(), String> {
    ui::fixture_button(app, title, move || {
        f(Gesture {
            created: Instant::now(),
            origin: Origin::NativeUi,
        })
    })
}

/// A program the fixture launch names (reviewed, never run): a shell script that ends at once with
/// code, or on Windows a file named `.exe` that starts `MZ`, the only kind a launch starts there.
fn fixture_program(path: &Path, code: u8) -> Result<(), String> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::write(path, format!("#!/bin/sh\nexit {code}\n")).map_err(|e| e.to_string())?;
        fs::set_permissions(path, fs::Permissions::from_mode(0o700)).map_err(|e| e.to_string())
    }
    #[cfg(windows)]
    {
        fs::write(path, format!("MZ fixture {code}\r\n")).map_err(|e| e.to_string())
    }
}

/// Changes the program's metadata, not its bytes: its mode, or on Windows its read-only
/// attribute.
fn change_metadata(path: &Path) -> Result<(), String> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(0o500)).map_err(|e| e.to_string())
    }
    #[cfg(windows)]
    {
        let mut p = fs::metadata(path).map_err(|e| e.to_string())?.permissions();
        p.set_readonly(true);
        fs::set_permissions(path, p).map_err(|e| e.to_string())
    }
}

pub(crate) fn run(app: &tauri::AppHandle, main: &Webview, page: &Webview) -> Result<(), String> {
    let dir = std::env::temp_dir().join(format!(
        "pluxy-native-confirm-{}",
        crate::managed::random_key()
    ));
    fs::create_dir(&dir).map_err(|e| e.to_string())?;
    let checked = (|| {
        let name = if cfg!(windows) {
            "fixture-program.exe"
        } else {
            "fixture-script"
        };
        let executable = dir.join(name);
        fixture_program(&executable, 0)?;
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
                argv: vec![name.into(), "--exact".into()],
                cwd: dir.clone(),
                program: None,
                opens: None,
                terminal_adapter: pkg,
                adapter_program: None,
                environment_revision: "1".into(),
                environment_review: vec![],
                secret_uses: vec![],
            }),
            environment: None,
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
        // The window passes a call on to its channel through a MessagePort too: each id seen,
        // call or reply, is kept apart, to tell on a timeout which side never answered.
        main.eval("window.__claimReplies = {}; window.__claimSeen = []; const originalPost = MessagePort.prototype.postMessage; MessagePort.prototype.postMessage = function(data, ...rest) { if (data?.id === 'spoof-confirm' || data?.id === 'confirm-control') { window.__claimReplies[data.id] = data; window.__claimSeen.push({id: data.id, reply: 'result' in data || 'error' in data}); } return originalPost.call(this, data, ...rest); }").map_err(|e| e.to_string())?;
        page.eval("window.__controlSent = null; fetch('/frame').then(r => r.json()).then(info => fetch('/send', {method: 'POST', body: JSON.stringify({generation: info.generation, type: 'call', data: {v: 0, id: 'confirm-control', method: 'config.get', params: null}})})).then(async r => window.__controlSent = {status: r.status, body: await r.text()}, e => window.__controlSent = {error: String(e)})").map_err(|e| e.to_string())?;
        let control = wait_for("positive page channel control", || {
            eval(main, "window.__claimReplies['confirm-control'] ?? null")
                .filter(|v| v.get("result").is_some() || v.get("error").is_some())
        })
        .map_err(|e| {
            let st = app.state::<crate::App>();
            let channel = st.pages.lock().unwrap().channel_state(page.label());
            format!(
                "{e}: {}",
                serde_json::json!({
                    "sent": eval(page, "window.__controlSent"),
                    "window_seen": eval(main, "window.__claimSeen"),
                    "native": channel,
                })
            )
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
        ui::press(app, text::CONFIRM_TITLE, access(text::CANCEL))?;
        require(result(app, &id)?.is_none(), "cancel issued token")?;
        // The close box gives the cancel answer at once, not the end of the lifetime.
        #[cfg(not(target_os = "linux"))]
        {
            let id = ask(app, main, &a)?;
            let asked = std::time::Instant::now();
            ui::close_box(app, text::CONFIRM_TITLE)?;
            require(
                result(app, &id)?.is_none() && asked.elapsed() < LIFETIME / 2,
                "the close box gave no cancel answer",
            )?;
        }
        let id = ask(app, main, &a)?;
        let t = confirm(app, &id, true)?;
        consume(app, &t)?;
        let (remembered, t) = trusted_start(app, &a)?;
        require(remembered, "exact native gesture did not remember")?;
        require(
            !ui::shown(app, text::CONFIRM_TITLE),
            "remembered launch showed dialog",
        )?;
        consume(app, &t)?;
        let mut other = a.clone();
        other.chain[0].source = "different-source".into();
        let (remembered, id) = trusted_start(app, &other)?;
        require(!remembered, "different chain reused approval")?;
        let t = confirm(app, &id, false)?;
        consume(app, &t)?;
        change_metadata(&executable)?;
        let (remembered, id) = trusted_start(app, &a)?;
        require(!remembered, "metadata change reused approval")?;
        let t = confirm(app, &id, true)?;
        consume(app, &t)?;
        fs::rename(&executable, dir.join("old")).map_err(|e| e.to_string())?;
        fixture_program(&executable, 1)?;
        let (remembered, id) = trusted_start(app, &a)?;
        require(!remembered, "same-path replacement reused approval")?;
        let t = confirm(app, &id, true)?;
        // Real native settings revocation, before this token is consumed.
        settings(app)?;
        ui::press(app, text::SETTINGS_TITLE, Key::Tab)?;
        // Revoke the newest approval using a real button at a stable mnemonic: only the first
        // (newest) row's button has one.
        ui::press(app, text::SETTINGS_TITLE, access(text::REVOKE))?;
        wait_for("native revocation", || {
            let st = app.state::<crate::App>();
            let c = st.confirmations.lock().unwrap();
            (!c.issued.contains_key(&t)).then_some(())
        })?;
        require(consume(app, &t).is_err(), "revoked unconsumed token worked")?;
        ui::press(app, text::SETTINGS_TITLE, Key::Escape)?;
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
        ui::wait_shown(app, text::CONFIRM_TITLE)?;
        let state = (
            PAGE_MAPPED_DISABLED.load(std::sync::atomic::Ordering::SeqCst),
            ui::cancel_focused(app, text::CONFIRM_TITLE)?,
        );
        require(
            state == (true, true),
            "page dialog did not delay Confirm and focus Cancel",
        )?;
        // Windows: a click message posted to the dialog during the delay, not made on the
        // disabled button, answers nothing either.
        #[cfg(windows)]
        {
            let enabled = ui::post_click(app, text::CONFIRM_TITLE, text::CONFIRM)?;
            std::thread::sleep(Duration::from_millis(300));
            let answered = app
                .state::<crate::App>()
                .confirmations
                .lock()
                .unwrap()
                .results
                .contains_key(&id);
            require(
                !enabled && !answered && ui::shown(app, text::CONFIRM_TITLE),
                "a posted click answered the delayed Confirm",
            )?;
        }
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
        let shown = ui::texts(app, text::NOTICE_TITLE)?
            .into_iter()
            .next()
            .ok_or("host-owned launch notice not visible")?;
        require(
            shown.contains("fixture/confirm")
                && shown.contains("Start native fixture")
                && (shown.contains("per second") || shown.contains("already pending")),
            "notice did not show the refused page Start",
        )?;
        // Requester and Action on lines of their own, not joined by an escaped line break.
        require(
            shown.contains("Asked by: fixture/confirm\nWhat: ") && !shown.contains("\\u000a"),
            &format!("notice does not hold real lines: {shown:?}"),
        )?;
        std::thread::sleep(Duration::from_millis(1100));
        let token = confirm(app, &id, false)?;
        consume(app, &token)?;
        // Confirm pressed while it is still disabled: the press waits for it, then confirms. A
        // press that went at once would be dropped, and the dialog would wait out its lifetime.
        std::thread::sleep(Duration::from_millis(1100));
        let p = app
            .state::<crate::App>()
            .confirmations
            .lock()
            .unwrap()
            .prepare_page(a.clone())?;
        PAGE_MAPPED_DISABLED.store(false, std::sync::atomic::Ordering::SeqCst);
        let id = super::page_start(app, p)?;
        ui::wait_shown(app, text::CONFIRM_TITLE)?;
        let waited = ui::waited();
        let token = confirm(app, &id, false)?;
        require(
            PAGE_MAPPED_DISABLED.load(std::sync::atomic::Ordering::SeqCst)
                && ui::waited() == waited + 1,
            "a press on the delayed Confirm did not wait for it",
        )?;
        consume(app, &token)?;
        crate::native_test::log(
            "confirmations",
            serde_json::json!({"passed": true, "close_box": cfg!(not(target_os = "linux")), "checks": ["confirm", "cancel", "page-spoof", "exact-native-gesture", "different-chain", "metadata", "replacement", "revoke", "page-flood-notice", "press-waits-for-confirm"]}),
        );
        Ok(())
    })();
    // The replaced program keeps its read-only attribute on Windows, which removal refuses.
    #[cfg(windows)]
    if let Ok(m) = fs::metadata(dir.join("old")) {
        let mut p = m.permissions();
        #[allow(clippy::permissions_set_readonly_false)]
        p.set_readonly(false);
        let _ = fs::set_permissions(dir.join("old"), p);
    }
    let _ = fs::remove_dir_all(dir);
    checked
}
