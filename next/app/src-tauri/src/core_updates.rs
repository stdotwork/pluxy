//! The window's commands for the app's own core versions (`cores.rs`): list them, install a
//! release from the core source, roll back to an installed version, adopt a core program.
//!
//! Every change is checked first (the release's signature with the app's trust, the installed
//! version's files, the program's digest), then shown in the native confirmation with what will
//! run; only its confirmed, one-use token applies it. A refusal comes back to the window before
//! any dialog. The window hears `pluxy-connection` when a change has ended, and reads how in
//! `core_versions`.

use crate::conn::{CoreChange, Versions};
use crate::{trusted, App, MAIN};
use tauri::{AppHandle, Emitter, EventTarget, Manager, Webview};

/// A change waiting for the person's confirmation.
pub(crate) struct Pending {
    #[cfg_attr(not(target_os = "linux"), allow(dead_code))]
    action: String,
    #[cfg_attr(not(target_os = "linux"), allow(dead_code))]
    change: CoreChange,
}

enum Ask {
    Install(String),
    Rollback(String),
    Adopt,
}

impl Ask {
    fn name(&self) -> &'static str {
        match self {
            Ask::Install(_) => "install",
            Ask::Rollback(_) => "rollback",
            Ask::Adopt => "adopt",
        }
    }
}

async fn blocking<T: Send + 'static>(f: impl FnOnce() -> T + Send + 'static) -> Result<T, String> {
    tauri::async_runtime::spawn_blocking(f)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn core_versions(webview: Webview) -> Result<Versions, String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    blocking(move || app.state::<App>().conn.lock().unwrap().versions()).await
}

#[tauri::command]
pub async fn core_install(webview: Webview, version: String) -> Result<(), String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    blocking(move || ask(&app, Ask::Install(version))).await?
}

#[tauri::command]
pub async fn core_rollback(webview: Webview, version: String) -> Result<(), String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    blocking(move || ask(&app, Ask::Rollback(version))).await?
}

#[tauri::command]
pub async fn core_adopt(webview: Webview) -> Result<(), String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    blocking(move || ask(&app, Ask::Adopt)).await?
}

#[cfg(not(target_os = "linux"))]
fn ask(_app: &AppHandle, _ask: Ask) -> Result<(), String> {
    Err("changing the app's own core needs a native confirmation, on Linux only for now".into())
}

/// Checks the change and opens its native confirmation; returns once the dialog shows.
#[cfg(target_os = "linux")]
fn ask(app: &AppHandle, ask: Ask) -> Result<(), String> {
    use crate::confirmation::{self, Action, Package};
    let st = app.state::<App>();
    let name = ask.name();
    {
        let mut slot = st.core_change.lock().unwrap();
        if slot.is_some() {
            return Err("another core change is waiting for confirmation".into());
        }
        // Held empty while this change is checked, so a second one waits its turn.
        *slot = Some(Pending {
            action: String::new(),
            change: CoreChange::Rollback(String::new()),
        });
    }
    let release = |e: String| {
        *st.core_change.lock().unwrap() = None;
        e
    };
    let home = app.path().home_dir().map_err(|e| release(e.to_string()))?;
    // The checks read files and hash them: without the connection's lock, except to read it.
    let checked = (|| -> Result<(String, Package, Vec<std::path::PathBuf>, CoreChange), String> {
        match ask {
            Ask::Install(version) => {
                let trust = crate::trust::TrustStore::load(&home)?;
                let conn = st.conn.lock().unwrap();
                let source = conn.core_source()?;
                let store = conn.store()?;
                drop(conn);
                let staged = store.stage(&trust, &source, &version)?;
                let what = format!(
                    "Install core {} ({}) from {} and run it as the app's core. Signed by key {}; archive SHA-256 {}.",
                    staged.version(),
                    crate::cores::platform(),
                    staged.source(),
                    staged.key_id(),
                    staged.sha256()
                );
                let executor = Package {
                    id: crate::cores::CORE_ID.into(),
                    source: staged.source().into(),
                    digest: staged.sha256().into(),
                    signer: staged.key_id().into(),
                    contract: format!("core {}", staged.version()),
                    generation: 0,
                };
                let files = vec![staged.path().to_path_buf()];
                Ok((what, executor, files, CoreChange::Install(staged)))
            }
            Ask::Rollback(version) => {
                let mut conn = st.conn.lock().unwrap();
                conn.prepare_rollback(&version)?;
                let folder = conn.store()?.folder(&version);
                let from = conn.view().core.and_then(|c| c.version);
                drop(conn);
                let what = format!(
                    "Roll the app's core back from {} to the installed core {version}, and run it.",
                    from.as_deref().unwrap_or("no core")
                );
                let executor = Package {
                    id: crate::cores::CORE_ID.into(),
                    source: "installed".into(),
                    digest: String::new(),
                    signer: String::new(),
                    contract: format!("core {version}"),
                    generation: 0,
                };
                let files = vec![folder.join(crate::cores::PROGRAM)];
                Ok((what, executor, files, CoreChange::Rollback(version)))
            }
            Ask::Adopt => {
                let (path, sha256, size) = st.conn.lock().unwrap().prepare_adopt()?;
                let what = format!(
                    "Copy the core program {} ({size} bytes, SHA-256 {sha256}) into the app's own cores, and run the copy as the app's core. It is not signed: run only a program you trust.",
                    path.display()
                );
                let executor = Package {
                    id: "custom core".into(),
                    source: path.display().to_string(),
                    digest: sha256.clone(),
                    signer: String::new(),
                    contract: "core program".into(),
                    generation: 0,
                };
                let files = vec![path.clone()];
                Ok((what, executor, files, CoreChange::Adopt { path, sha256 }))
            }
        }
    })();
    let (what, executor, files, change) = checked.map_err(release)?;
    let app_package = Package {
        id: "pluxy app".into(),
        source: "the app's window".into(),
        digest: String::new(),
        signer: String::new(),
        contract: "the app's own core".into(),
        generation: 0,
    };
    let action = Action {
        what,
        chain: vec![app_package],
        executor,
        // A core runs with everything this user may do.
        native_tool: true,
        files,
        launch: None,
    };
    let id = {
        let mut c = st.confirmations.lock().unwrap();
        c.prepare_main(action).and_then(|p| c.queue(p))
    }
    .map_err(release)?;
    *st.core_change.lock().unwrap() = Some(Pending {
        action: id.clone(),
        change,
    });
    if let Err(e) = confirmation::request(app, id.clone()) {
        return Err(release(e));
    }
    let app = app.clone();
    std::thread::spawn(move || settle(&app, &id, name));
    Ok(())
}

/// Waits for the confirmation's native result; only a confirmed, consumed token applies the
/// change. Then tells the window.
#[cfg(target_os = "linux")]
fn settle(app: &AppHandle, id: &str, name: &'static str) {
    use std::time::{Duration, Instant};
    let st = app.state::<App>();
    // The dialog cancels itself after its lifetime; window close invalidates it sooner.
    let deadline = Instant::now() + Duration::from_secs(75);
    let result = loop {
        if let Some(r) = st.confirmations.lock().unwrap().take_result(id) {
            break Some(r);
        }
        if Instant::now() > deadline {
            break None;
        }
        std::thread::sleep(Duration::from_millis(100));
    };
    let authorized = match result {
        Some(Ok(Some(token))) => st.confirmations.lock().unwrap().consume(id, &token).is_ok(),
        _ => false,
    };
    let pending = st
        .core_change
        .lock()
        .unwrap()
        .take_if(|p| p.action == id)
        .map(|p| p.change);
    let version = match &pending {
        Some(CoreChange::Install(s)) => s.version().to_string(),
        Some(CoreChange::Rollback(v)) => v.clone(),
        Some(CoreChange::Adopt { sha256, .. }) => {
            format!("custom-{}", &sha256[..12.min(sha256.len())])
        }
        None => String::new(),
    };
    let mut conn = st.conn.lock().unwrap();
    match pending {
        Some(change) if authorized => match conn.apply(change) {
            Ok(()) => {
                let running = conn.view().core.and_then(|c| c.version).unwrap_or(version);
                conn.record_outcome(name, &running, "done", None);
            }
            Err(e) => conn.record_outcome(name, &version, "failed", Some(e)),
        },
        // Dropping a staged release removes its copy.
        _ => conn.record_outcome(name, &version, "cancelled", None),
    }
    let v = conn.view();
    drop(conn);
    crate::show_status(app, &v);
    let _ = app.emit_to(EventTarget::webview(MAIN), "pluxy-connection", ());
}
