//! The native test's report on the core that came with the app (native-test builds only), for
//! the test package the runner starts with `PLUXY_APP_TEST_BUNDLED` (`native/bundled-session.ts`
//! on Linux, `native/platform.ts` on Windows and macOS). Once the window is up it prints
//! `native-test: bundled <JSON>`: the connection and the core versions as the window reads them,
//! whether the running core's program is in the app's own cores, the files of the bundled
//! version there, the packages the running core has installed, and the store's selection record.
//! With `PLUXY_APP_TEST_BUNDLED=rollback:<version>` it then rolls the core back to that version
//! through the window's command and the real native confirmation, confirmed, and prints
//! `native-test: bundled_rollback <JSON>` the same way. Then it quits.

use super::core_updates::{act, call, in_store, CONFIRM};
use super::{log, wait_for};
use crate::{App, MAIN};
use serde_json::{json, Value};
use std::path::Path;
use tauri::{AppHandle, Manager, Webview};

/// Every file under dir, as relative paths with `/`, sorted.
fn files(dir: &Path) -> Vec<String> {
    fn walk(base: &Path, dir: &Path, out: &mut Vec<String>) {
        for e in std::fs::read_dir(dir).into_iter().flatten().flatten() {
            let p = e.path();
            if e.file_type().is_ok_and(|t| t.is_dir()) {
                walk(base, &p, out);
            } else if let Ok(rel) = p.strip_prefix(base) {
                out.push(rel.to_string_lossy().replace('\\', "/"));
            }
        }
    }
    let mut out = Vec::new();
    walk(dir, dir, &mut out);
    out.sort();
    out
}

/// The IDs of the packages the running core's bundled registry offers (`GET /registries`: core
/// reads it from `bundled/index.json` beside its program, and a first start installs none of
/// them), once it offers at least want of them; none without a running core.
fn packages(app: &AppHandle, want: usize) -> Vec<String> {
    let ids = || -> Option<Vec<String>> {
        let caller = app.state::<App>().conn.lock().unwrap().caller();
        let r = caller.call("GET", "/registries", None);
        let bundled = r.body.as_ref()?["registries"]
            .as_array()?
            .iter()
            .find(|g| g["name"] == "bundled")?
            .clone();
        let mut ids: Vec<String> = bundled["packages"]
            .as_array()?
            .iter()
            .filter_map(|p| p["id"].as_str().map(String::from))
            .collect();
        ids.sort();
        Some(ids)
    };
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(60);
    loop {
        let got = ids().unwrap_or_default();
        if got.len() >= want || std::time::Instant::now() > deadline {
            return got;
        }
        std::thread::sleep(std::time::Duration::from_millis(250));
    }
}

fn report(app: &AppHandle, main: &Webview, step: &str) -> Result<(), String> {
    // Where the app keeps them (lib.rs): on Windows in the local application data.
    #[cfg(windows)]
    let data = app.path().app_local_data_dir().map_err(|e| e.to_string())?;
    #[cfg(not(windows))]
    let data = app.path().app_data_dir().map_err(|e| e.to_string())?;
    let cores = data.join("cores");
    let view = call(main, "connection_get", json!({}))?;
    let versions = call(main, "core_versions", json!({}))?;
    let pid = view["core"]["pid"].as_u64();
    let bundled = versions["bundled"]["version"].as_str().unwrap_or_default();
    let state: Value = std::fs::read(cores.join("state.json"))
        .ok()
        .and_then(|b| serde_json::from_slice(&b).ok())
        .unwrap_or(Value::Null);
    let want = std::env::var("PLUXY_APP_TEST_BUNDLED_PACKAGES")
        .ok()
        .and_then(|n| n.parse().ok())
        .unwrap_or(0);
    let packages = if pid.is_some() {
        packages(app, want)
    } else {
        Vec::new()
    };
    log(
        step,
        json!({
            "core": view["core"],
            "versions": versions,
            "exe_in_store": pid.is_some_and(|p| in_store(p, &cores)),
            "store_files": if bundled.is_empty() { Vec::new() } else { files(&cores.join("versions").join(bundled)) },
            "packages": packages,
            "state": state,
        }),
    );
    Ok(())
}

pub fn run(app: &AppHandle, what: &str) -> Result<(), String> {
    let main = wait_for("the window", || app.get_webview(MAIN))?;
    // The window's commands answer once its page has loaded.
    wait_for("the window's page", || {
        super::eval(&main, "document.readyState === 'complete'").filter(|v| v == true)
    })?;
    report(app, &main, "bundled")?;
    if let Some(version) = what.strip_prefix("rollback:") {
        let o = act(
            &main,
            "core_rollback",
            json!({"version": version}),
            CONFIRM,
            &[version.to_string()],
        )?;
        if o["result"] != "done" {
            return Err(format!("the rollback to {version} did not finish: {o}"));
        }
        report(app, &main, "bundled_rollback")?;
    }
    crate::quit(app);
    Ok(())
}
