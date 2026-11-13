//! The native test's check of signed native-tier admission (native-test builds with
//! `artifact-test-trust` only). It drives the real app as the person would: the window's own
//! commands, the Device extensions page and the real GTK native confirmation (X input from
//! `xdotool`), against archives, three local registries and the run's test trust profile that
//! `native/admission.ts` built (the archives' SHA-256 digests are in `expect.json`, computed there,
//! not by the app). Every refusal goes through `device_install` with a positive control on that
//! route:
//!
//! - `untrusted` (no root names it) lists the curated signed `pluxy/tool` entry, digest and all:
//!   refused as not curated; its unsigned native `example/native` is refused; its web
//!   `example/page` installs Unverified;
//! - `curated`: `pluxy/forged` (signed by a key no root names) and `pluxy/unsigned` are refused;
//! - with the profile's roots emptied ("no roots"), then with its marker removed (test trust in a
//!   normal profile), `curated`'s `pluxy/tool` is refused and `example/page` installs from it;
//! - with the profile restored, `pluxy/tool` installs disabled and labelled curated, the Device
//!   extensions page shows its full-access warning, enabling opens the native confirmation, which
//!   names full access, the ID and the digest: Cancel leaves it disabled and unauthorized (no
//!   admitted record), Confirm enables it and the admitted record names its store folder,
//!   program, curated source and key;
//! - from `mirror` (another curated source) the installed package is refused; removed and
//!   installed from `mirror` it starts disabled and unauthorized again.
//!
//! One line on stderr: `native-test: admission <JSON>`.

use super::{eval, log, wait_for};
use serde_json::{json, Value};
use std::collections::BTreeSet;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use tauri::{AppHandle, Manager, Webview};

static CALLS: AtomicU64 = AtomicU64::new(0);
const DIALOG: &str = "pluxy native confirmation";
const FULL_ACCESS: &str = "full access to your user account";

pub(super) fn x(args: &[&str]) -> Result<String, String> {
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

pub(super) fn dialog() -> Result<String, String> {
    wait_for("the native confirmation", || {
        x(&["search", "--onlyvisible", "--name", &format!("^{DIALOG}$")])
            .ok()
            .and_then(|s| s.lines().last().map(String::from))
    })
}

pub(super) fn press(keys: &str) -> Result<(), String> {
    let id = dialog()?;
    x(&["windowfocus", "--sync", &id])?;
    x(&["key", "--clearmodifiers", keys])?;
    Ok(())
}

/// The text the visible native confirmation shows, read from its GTK labels.
pub(super) fn dialog_text(app: &AppHandle) -> Result<String, String> {
    use gtk::prelude::*;
    fn labels(w: &gtk::Widget, out: &mut String) {
        if let Some(l) = w.downcast_ref::<gtk::Label>() {
            out.push_str(&l.text());
            out.push('\n');
        }
        if let Some(c) = w.downcast_ref::<gtk::Container>() {
            for child in c.children() {
                labels(&child, out);
            }
        }
    }
    dialog()?;
    wait_for("the native confirmation's text", || {
        let (tx, rx) = std::sync::mpsc::channel();
        app.run_on_main_thread(move || {
            let mut text = String::new();
            for w in gtk::Window::list_toplevels() {
                if let Some(w) = w.downcast_ref::<gtk::Window>() {
                    if w.is_visible() && w.title().as_deref() == Some(DIALOG) {
                        labels(w.upcast_ref(), &mut text);
                    }
                }
            }
            let _ = tx.send(text);
        })
        .ok()?;
        rx.recv_timeout(std::time::Duration::from_secs(5))
            .ok()
            .filter(|t| !t.is_empty())
    })
}

pub(super) fn require(ok: bool, what: impl Into<String>) -> Result<(), String> {
    if ok {
        Ok(())
    } else {
        Err(what.into())
    }
}

pub(super) fn start(main: &Webview, cmd: &str, args: Value) -> Result<String, String> {
    let slot = format!("a{}", CALLS.fetch_add(1, Ordering::SeqCst));
    main.eval(format!(
        "(window.__pluxyAdmission ??= {{}}, window.__TAURI_INTERNALS__.invoke({cmd}, {args}).then(\
         (v) => {{ window.__pluxyAdmission[{slot}] = {{ok: true, value: v ?? null}}; }},\
         (e) => {{ window.__pluxyAdmission[{slot}] = {{ok: false, error: String(e)}}; }}))",
        cmd = json!(cmd),
        slot = json!(slot),
    ))
    .map_err(|e| e.to_string())?;
    Ok(slot)
}

pub(super) fn settle(main: &Webview, slot: &str) -> Result<Value, String> {
    wait_for("an admission command", || {
        eval(
            main,
            &format!("window.__pluxyAdmission?.[{}] ?? null", json!(slot)),
        )
        .filter(|v| !v.is_null())
    })
}

pub(super) fn call(main: &Webview, cmd: &str, args: Value) -> Result<Value, String> {
    let slot = start(main, cmd, args)?;
    settle(main, &slot)
}

pub(super) fn ok(main: &Webview, cmd: &str, args: Value) -> Result<Value, String> {
    let r = call(main, cmd, args.clone())?;
    require(r["ok"] == true, format!("{cmd} {args}: {r}"))?;
    Ok(r["value"].clone())
}

pub(super) fn refused(
    main: &Webview,
    cmd: &str,
    args: Value,
    says: &str,
) -> Result<String, String> {
    let r = call(main, cmd, args.clone())?;
    let error = r["error"].as_str().unwrap_or_default().to_string();
    require(
        r["ok"] == false && error.contains(says),
        format!("{cmd} {args} should be refused with {says:?}: {r}"),
    )?;
    Ok(error)
}

/// The record the program host would take for id, checked now (`Catalog::admitted`).
fn admitted(app: &AppHandle, id: &str) -> Result<crate::device::Admitted, String> {
    app.state::<crate::App>()
        .device
        .lock()
        .unwrap()
        .admitted(id, &crate::trust::TrustStore::current())
}

fn install(registry: &str, id: &str) -> Value {
    json!({"registry": registry, "id": id, "version": "1.0.0"})
}

pub(super) fn listed(main: &Webview, id: &str) -> Result<Option<Value>, String> {
    let list = ok(main, "device_list", json!({}))?;
    Ok(list["packages"]
        .as_array()
        .ok_or_else(|| format!("device_list: {list}"))?
        .iter()
        .find(|p| p["id"] == id)
        .cloned())
}

pub(super) fn store(app: &AppHandle) -> Result<BTreeSet<String>, String> {
    let dir = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("device-packages")
        .join("store");
    Ok(match std::fs::read_dir(&dir) {
        Ok(entries) => entries
            .filter_map(|e| e.ok())
            .map(|e| e.file_name().to_string_lossy().into_owned())
            .collect(),
        Err(_) => BTreeSet::new(),
    })
}

/// Replaces a trust profile file as the runner wrote it: private, owned, not a link.
fn write_private(path: &Path, bytes: &[u8]) -> Result<(), String> {
    use std::io::Write;
    use std::os::unix::fs::OpenOptionsExt;
    let _ = std::fs::remove_file(path);
    let mut f = std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(path)
        .map_err(|e| format!("{}: {e}", path.display()))?;
    f.write_all(bytes).map_err(|e| e.to_string())
}

/// The web package installs Unverified from registry, then goes: the route works.
fn web_control(app: &AppHandle, main: &Webview, registry: &str, page: &str) -> Result<(), String> {
    ok(main, "device_install", install(registry, "example/page"))?;
    let p = listed(main, "example/page")?.ok_or("example/page is not listed")?;
    require(
        p["active"]["sha256"] == page && p["trust"] == "unverified",
        format!("example/page from {registry}: {p}"),
    )?;
    ok(main, "device_remove", json!({"id": "example/page"}))?;
    require(
        store(app)?.is_empty(),
        format!("the store after removing example/page: {:?}", store(app)?),
    )
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let expect: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("expect.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let digest = |k: &str| expect[k].as_str().unwrap_or_default().to_string();
    let (tool, page) = (digest("tool"), digest("page"));
    let home = PathBuf::from(std::env::var_os("HOME").ok_or("no HOME")?);
    let roots_file = home.join(crate::trust::PROFILE_FILE);
    let marker_file = home.join(crate::trust::MARKER_FILE);
    let roots = std::fs::read(&roots_file).map_err(|e| e.to_string())?;
    let marker = std::fs::read(&marker_file).map_err(|e| e.to_string())?;
    let mut out = json!({});

    let list = ok(main, "device_list", json!({}))?;
    require(
        list["packages"].as_array().is_some_and(|p| p.is_empty()),
        format!("a fresh device catalog: {list}"),
    )?;

    // A source no root names: its listing of the curated signed entry is no provenance.
    out["untrusted_index"] = json!(refused(
        main,
        "device_install",
        install("untrusted", "pluxy/tool"),
        "not curated",
    )?);
    out["unsigned_lookalike"] = json!(refused(
        main,
        "device_install",
        install("untrusted", "example/native"),
        "native",
    )?);
    require(
        store(app)?.is_empty(),
        "a refused package reached the store",
    )?;
    web_control(app, main, "untrusted", &page)?;

    // A curated source: a signature by a key no root names, and none at all.
    out["forged"] = json!(refused(
        main,
        "device_install",
        install("curated", "pluxy/forged"),
        "signature",
    )?);
    out["unsigned"] = json!(refused(
        main,
        "device_install",
        install("curated", "pluxy/unsigned"),
        "signature",
    )?);
    require(
        store(app)?.is_empty(),
        "a refused package reached the store",
    )?;

    // No roots: the profile names none.
    write_private(&roots_file, br#"{"roots":[]}"#)?;
    out["no_roots"] = json!(refused(
        main,
        "device_install",
        install("curated", "pluxy/tool"),
        "not configured",
    )?);
    web_control(app, main, "curated", &page)?;
    write_private(&roots_file, &roots)?;

    // Test trust in a normal profile: the roots are there, the runner's marker is not.
    std::fs::remove_file(&marker_file).map_err(|e| e.to_string())?;
    out["normal_profile"] = json!(refused(
        main,
        "device_install",
        install("curated", "pluxy/tool"),
        "not configured",
    )?);
    web_control(app, main, "curated", &page)?;
    write_private(&marker_file, &marker)?;
    require(
        store(app)?.is_empty(),
        "a refused package reached the store",
    )?;

    // The positive control: the curated signed package installs, disabled until confirmed.
    ok(main, "device_install", install("curated", "pluxy/tool"))?;
    let p = listed(main, "pluxy/tool")?.ok_or("pluxy/tool is not listed")?;
    require(
        p["active"]["sha256"] == tool
            && p["trust"] == "curated"
            && p["tier"] == "native"
            && p["enabled"] == false
            && p["authorized"] == false
            && p["source"]["name"] == "curated",
        format!("pluxy/tool as installed: {p}"),
    )?;
    require(
        store(app)? == BTreeSet::from([tool.clone()]),
        format!("the store with pluxy/tool: {:?}", store(app)?),
    )?;

    // The main window says full access before enabling.
    main.eval("location.hash = '#/device'")
        .map_err(|e| e.to_string())?;
    wait_for("pluxy/tool on the Device extensions page", || {
        eval(
            main,
            "(() => { const b = [...document.querySelectorAll('button.item-name')].find((b) => b.textContent === 'pluxy/tool'); if (b) b.click(); return !!b; })()",
        )
        .filter(|v| v == true)
    })?;
    wait_for("the full-access warning in the window", || {
        eval(
            main,
            &format!("document.body.innerText.includes({})", json!(FULL_ACCESS)),
        )
        .filter(|v| v == true)
    })?;

    // Native cancel: still disabled, not authorized.
    let slot = start(
        main,
        "device_enable",
        json!({"id": "pluxy/tool", "enabled": true}),
    )?;
    let text = dialog_text(app)?;
    require(
        text.contains(FULL_ACCESS) && text.contains("pluxy/tool") && text.contains(&tool),
        format!("the native confirmation shows: {text}"),
    )?;
    out["dialog"] = json!(text.lines().next().unwrap_or_default());
    press("alt+c")?;
    let r = settle(main, &slot)?;
    require(
        r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains("cancelled")),
        format!("cancelled native enable: {r}"),
    )?;
    out["cancel"] = r["error"].clone();
    let p = listed(main, "pluxy/tool")?.ok_or("pluxy/tool is gone")?;
    require(
        p["enabled"] == false && p["authorized"] == false,
        format!("pluxy/tool after cancel: {p}"),
    )?;
    out["cancel_record"] = json!(admitted(app, "pluxy/tool")
        .err()
        .ok_or("a cancelled native tool is admitted")?);

    // Native confirm: enabled and authorized.
    let slot = start(
        main,
        "device_enable",
        json!({"id": "pluxy/tool", "enabled": true}),
    )?;
    dialog_text(app)?;
    press("alt+o")?;
    let r = settle(main, &slot)?;
    require(r["ok"] == true, format!("confirmed native enable: {r}"))?;
    let p = listed(main, "pluxy/tool")?.ok_or("pluxy/tool is gone")?;
    require(
        p["enabled"] == true && p["authorized"] == true && p["active"]["sha256"] == tool,
        format!("pluxy/tool after confirm: {p}"),
    )?;
    // The record the program host takes: the confirmed release, its signer and its program in
    // the store, checked again.
    let a = admitted(app, "pluxy/tool")?;
    let store_dir = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("device-packages")
        .join("store")
        .join(&tool);
    let curated = fixtures.join("curated.json").display().to_string();
    require(
        a.sha256 == tool
            && a.dir == store_dir
            && a.program == store_dir.join("bin").join("tool")
            && a.program.is_file()
            && a.signed.source == curated
            && a.signed.publisher == "pluxy"
            && a.signed.key_id == digest("key_id")
            && a.public_key == digest("public_key")
            && a.operations.first().map(|o| o.id.as_str()) == Some("inspect"),
        format!("the admitted record: {a:?}"),
    )?;
    out["admitted"] = json!({"sha256": a.sha256, "program": a.program, "source": a.signed.source, "key_id": a.signed.key_id});

    // Another curated source for the installed package is refused; after removal it installs
    // from there with no trust carried over.
    out["source_change"] = json!(refused(
        main,
        "device_install",
        install("mirror", "pluxy/tool"),
        "installed from",
    )?);
    ok(main, "device_remove", json!({"id": "pluxy/tool"}))?;
    ok(main, "device_install", install("mirror", "pluxy/tool"))?;
    let p = listed(main, "pluxy/tool")?.ok_or("pluxy/tool from mirror is not listed")?;
    require(
        p["source"]["name"] == "mirror"
            && p["trust"] == "curated"
            && p["enabled"] == false
            && p["authorized"] == false,
        format!("pluxy/tool from mirror: {p}"),
    )?;
    out["mirror_record"] = json!(admitted(app, "pluxy/tool")
        .err()
        .ok_or("a reinstalled native tool is admitted without confirmation")?);
    out["passed"] = json!(true);
    log("admission", out);
    Ok(())
}
