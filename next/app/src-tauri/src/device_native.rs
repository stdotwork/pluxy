//! The native test's check of this device's extension catalog (native-test builds only). It
//! drives the real app as the person would: the window's own commands, the native file chooser
//! (real X input from `xdotool`) and the page webviews, against archives and a local registry
//! `native/run.ts` builds in `PLUXY_APP_TEST_DEVICE`, with their SHA-256 digests in `expect.json`
//! (computed there, not by the app). Each denial has a positive control on the same route:
//!
//! - registry: `pluxy/notes` (a reserved publisher) is refused; `example/notes` 1.0.0 installs,
//!   is listed Unverified (the Device extensions page names its registry's index) and its page
//!   answers through the channel; 2.0.0 replaces it while its
//!   page is open (the old page webview closes, the new one shows 2.0.0); 3.0.0, which needs a
//!   host contract this app lacks, is refused and 2.0.0 keeps working; the page webview's request
//!   for 1.0.0's files is refused (the asset lines the scheme logs); roll back to the last good
//!   release, disable and enable, and remove, each leaving no page webview behind;
//! - local file: the chooser cancelled changes nothing, `pluxy/local` is refused and
//!   `example/local` installs and shows its page, left open for the window's close check.
//!
//! One line on stderr: `native-test: device <JSON>`; the asset scheme adds
//! `native-test: asset <JSON>` for each request.

use super::{eval, log, wait_for};
use crate::MAIN;
use serde_json::{json, Value};
use std::collections::BTreeSet;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use tauri::{AppHandle, Manager, Webview};

static CALLS: AtomicU64 = AtomicU64::new(0);
const CHOOSER: &str = "Install an extension package on this device";

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

fn chooser() -> Result<String, String> {
    wait_for("the native file chooser", || {
        x(&["search", "--onlyvisible", "--name", &format!("^{CHOOSER}$")])
            .ok()
            .and_then(|s| s.lines().last().map(String::from))
    })
}

pub(super) fn require(ok: bool, what: impl Into<String>) -> Result<(), String> {
    if ok {
        Ok(())
    } else {
        Err(what.into())
    }
}

/// Starts a command in the window's webview; `settle` waits for its outcome.
pub(super) fn start(main: &Webview, cmd: &str, args: Value) -> Result<String, String> {
    let slot = format!("c{}", CALLS.fetch_add(1, Ordering::SeqCst));
    main.eval(format!(
        "(window.__pluxyDevice ??= {{}}, window.__TAURI_INTERNALS__.invoke({cmd}, {args}).then(\
         (v) => {{ window.__pluxyDevice[{slot}] = {{ok: true, value: v ?? null}}; }},\
         (e) => {{ window.__pluxyDevice[{slot}] = {{ok: false, error: String(e)}}; }}))",
        cmd = json!(cmd),
        slot = json!(slot),
    ))
    .map_err(|e| e.to_string())?;
    Ok(slot)
}

pub(super) fn settle(main: &Webview, slot: &str) -> Result<Value, String> {
    wait_for("a device command", || {
        eval(
            main,
            &format!("window.__pluxyDevice?.[{}] ?? null", json!(slot)),
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

/// A package as `device_list` reports it.
pub(super) fn listed(main: &Webview, id: &str) -> Result<Option<Value>, String> {
    let list = ok(main, "device_list", json!({}))?;
    Ok(list["packages"]
        .as_array()
        .ok_or_else(|| format!("device_list: {list}"))?
        .iter()
        .find(|p| p["id"] == id)
        .cloned())
}

/// The folders in the device's package store.
fn store(app: &AppHandle) -> Result<BTreeSet<String>, String> {
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

fn webviews(app: &AppHandle) -> Vec<String> {
    let mut l: Vec<String> = app.webviews().keys().cloned().collect();
    l.sort();
    l
}

fn nav_entry(main: &Webview, route: &str) -> bool {
    eval(
        main,
        &format!("document.querySelector('a[href=\"#/{route}\"]') !== null"),
    ) == Some(json!(true))
}

/// Opens route in the window and returns the page webview showing digest, once its page
/// reported marker through the channel.
pub(super) fn open_page(
    app: &AppHandle,
    main: &Webview,
    route: &str,
    digest: &str,
    marker: &str,
) -> Result<String, String> {
    wait_for("the page in the navigation", || {
        nav_entry(main, route).then_some(())
    })?;
    main.eval(format!("location.hash = {}", json!(format!("#/{route}"))))
        .map_err(|e| e.to_string())?;
    showing(app, digest, marker)
}

/// The page webview whose frame shows a file of digest, once marker came from its page.
pub(super) fn showing(app: &AppHandle, digest: &str, marker: &str) -> Result<String, String> {
    let label = wait_for(&format!("a page webview showing {digest}"), || {
        app.webviews().into_iter().find_map(|(label, w)| {
            (label != MAIN
                && eval(&w, "document.querySelector('iframe')?.src ?? null")
                    .and_then(|v| v.as_str().map(|s| s.contains(&format!("/{digest}/"))))
                    == Some(true))
            .then_some(label)
        })
    })?;
    marker_from(app, &label, marker)?;
    Ok(label)
}

/// Waits for marker from the page in label's frame: its page sends one after a channel reply.
fn marker_from(app: &AppHandle, label: &str, marker: &str) -> Result<(), String> {
    let w = app
        .get_webview(label)
        .ok_or_else(|| format!("no webview {label}"))?;
    w.eval(
        "if (!window.__deviceMarkers) { window.__deviceMarkers = []; window.addEventListener('message', (e) => {\
         const f = document.querySelector('iframe'); const m = e.data?.deviceMarker;\
         if (f && e.source === f.contentWindow && typeof m === 'string' && !window.__deviceMarkers.includes(m))\
         window.__deviceMarkers.push(m); }); }",
    )
    .map_err(|e| e.to_string())?;
    wait_for(&format!("{marker} from {label}"), || {
        eval(
            &w,
            &format!("window.__deviceMarkers.includes({})", json!(marker)),
        )
        .filter(|v| v == true)
    })?;
    Ok(())
}

pub(super) fn gone(app: &AppHandle, label: &str) -> Result<(), String> {
    wait_for(&format!("{label} to close"), || {
        app.get_webview(label).is_none().then_some(())
    })
}

fn only_main(app: &AppHandle, what: &str) -> Result<(), String> {
    wait_for(&format!("no page webview after {what}"), || {
        (webviews(app) == [MAIN.to_string()]).then_some(())
    })
    .map_err(|e| format!("{e}: {:?}", webviews(app)))
}

/// Chooses path in the native file chooser that `device_install_file` opened.
fn choose(path: &Path) -> Result<(), String> {
    let id = chooser()?;
    x(&["windowfocus", "--sync", &id])?;
    x(&["key", "--clearmodifiers", "ctrl+l"])?;
    std::thread::sleep(std::time::Duration::from_millis(300));
    x(&[
        "type",
        "--clearmodifiers",
        "--delay",
        "20",
        path.to_str().ok_or("fixture path is not UTF-8")?,
    ])?;
    std::thread::sleep(std::time::Duration::from_millis(300));
    x(&["key", "--clearmodifiers", "Return"])?;
    Ok(())
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let expect: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("expect.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let digest = |k: &str| expect[k].as_str().unwrap_or_default().to_string();
    let (notes1, notes2, notes3, spoof) = (
        digest("notes1"),
        digest("notes2"),
        digest("notes3"),
        digest("spoof"),
    );
    let (local, pluxy_local) = (digest("local"), digest("pluxy_local"));
    let route = "device/example/notes/notes";
    let mut out = json!({});

    let list = ok(main, "device_list", json!({}))?;
    require(
        list["packages"].as_array().is_some_and(|p| p.is_empty()) && list["safe_mode"] == false,
        format!("a fresh device catalog: {list}"),
    )?;

    // Registry route: a reserved publisher is refused, before anything reaches the store.
    out["registry_spoof"] = json!(refused(
        main,
        "device_install",
        json!({"registry": "fixture", "id": "pluxy/notes", "version": "1.0.0"}),
        "reserved",
    )?);
    require(
        store(app)?.is_empty(),
        "the refused package reached the store",
    )?;
    require(
        listed(main, "pluxy/notes")?.is_none(),
        "the refused package is listed",
    )?;

    // Same route, the positive control: an Unverified package installs and its page answers.
    ok(
        main,
        "device_install",
        json!({"registry": "fixture", "id": "example/notes", "version": "1.0.0"}),
    )?;
    let p = listed(main, "example/notes")?.ok_or("example/notes is not listed")?;
    require(
        p["active"]["version"] == "1.0.0"
            && p["active"]["sha256"] == notes1
            && p["trust"] == "unverified"
            && p["source"]["kind"] == "registry"
            && p["source"]["name"] == "fixture"
            && p["enabled"] == true,
        format!("example/notes 1.0.0 as listed: {p}"),
    )?;
    // The Device extensions page names the registry by its index too, not only its name.
    let index = fixtures.join("index.json").display().to_string();
    main.eval("location.hash = '#/device'")
        .map_err(|e| e.to_string())?;
    wait_for("the registry's index on the Device extensions page", || {
        eval(
            main,
            &format!(
                "document.body.innerText.includes({})",
                json!(format!("Registry fixture ({index})"))
            ),
        )
        .filter(|v| v == true)
    })?;
    let v1 = open_page(app, main, route, &notes1, "notes-1.0.0")?;
    out["v1_label"] = json!(v1);

    // Hot swap while the page is open: its webview closes and the window shows 2.0.0.
    ok(
        main,
        "device_install",
        json!({"registry": "fixture", "id": "example/notes", "version": "2.0.0"}),
    )?;
    gone(app, &v1)?;
    let v2 = showing(app, &notes2, "notes-2.0.0")?;
    out["v2_label"] = json!(v2);
    wait_for("only the new page webview after the swap", || {
        (webviews(app) == [MAIN.to_string(), v2.clone()]).then_some(())
    })
    .map_err(|e| format!("{e}: {:?}", webviews(app)))?;

    // A candidate that fails keeps the page that works.
    out["candidate"] = json!(refused(
        main,
        "device_install",
        json!({"registry": "fixture", "id": "example/notes", "version": "3.0.0"}),
        "host contract",
    )?);
    let p = listed(main, "example/notes")?.ok_or("example/notes is gone")?;
    require(
        p["active"]["sha256"] == notes2 && p["last_good"]["sha256"] == notes1,
        format!("after the failed candidate: {p}"),
    )?;
    let kept = store(app)?;
    require(
        kept == BTreeSet::from([notes1.clone(), notes2.clone()]),
        format!("the store after the failed candidate: {kept:?} (3.0.0 is {notes3})"),
    )?;
    require(app.get_webview(&v2).is_some(), "the working page closed")?;
    let w2 = app.get_webview(&v2).ok_or("no v2 webview")?;
    w2.eval(
        "document.querySelector('iframe').contentWindow.postMessage({deviceProbe: 'after'}, '*')",
    )
    .map_err(|e| e.to_string())?;
    marker_from(app, &v2, "notes-2.0.0-after")?;

    // The 2.0.0 page asks for 1.0.0's files: the asset scheme refuses that page webview (its
    // asset line says so), while it served 2.0.0's to it.
    w2.eval(format!(
        "document.querySelector('iframe').contentWindow.postMessage({{deviceLoad: {}}}, '*')",
        json!(format!(
            "pluxy-app://localhost/example/notes/{notes1}/index.html"
        ))
    ))
    .map_err(|e| e.to_string())?;
    std::thread::sleep(std::time::Duration::from_secs(2));

    // Roll back to the last good release.
    ok(main, "device_rollback", json!({"id": "example/notes"}))?;
    let p = listed(main, "example/notes")?.ok_or("example/notes is gone")?;
    require(
        p["active"]["sha256"] == notes1 && p["last_good"]["sha256"] == notes2,
        format!("after rolling back: {p}"),
    )?;
    gone(app, &v2)?;
    let back = showing(app, &notes1, "notes-1.0.0")?;

    // Disable: its page and navigation entry go; enable brings the entry back.
    ok(
        main,
        "device_enable",
        json!({"id": "example/notes", "enabled": false}),
    )?;
    gone(app, &back)?;
    only_main(app, "disabling")?;
    wait_for("the disabled page's entry to go", || {
        (!nav_entry(main, route)).then_some(())
    })?;
    ok(
        main,
        "device_enable",
        json!({"id": "example/notes", "enabled": true}),
    )?;
    let again = open_page(app, main, route, &notes1, "notes-1.0.0")?;

    // Remove: no page webview, no navigation entry, nothing left in the store.
    ok(main, "device_remove", json!({"id": "example/notes"}))?;
    gone(app, &again)?;
    only_main(app, "removing")?;
    wait_for("the removed page's entry to go", || {
        (!nav_entry(main, route)).then_some(())
    })?;
    require(
        listed(main, "example/notes")?.is_none(),
        "removed package listed",
    )?;
    require(
        store(app)?.is_empty(),
        format!("the store after removing: {:?}", store(app)?),
    )?;

    // Local file route: cancel, a reserved publisher, then the positive control.
    let slot = start(main, "device_install_file", json!({}))?;
    let id = chooser()?;
    x(&["windowfocus", "--sync", &id])?;
    x(&["key", "--clearmodifiers", "Escape"])?;
    let r = settle(main, &slot)?;
    require(
        r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains("no file")),
        format!("cancelled chooser: {r}"),
    )?;
    require(
        store(app)?.is_empty(),
        "the cancelled chooser changed the store",
    )?;

    let slot = start(main, "device_install_file", json!({}))?;
    choose(&fixtures.join("pluxy-local-1.0.0.tar.gz"))?;
    let r = settle(main, &slot)?;
    require(
        r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains("reserved")),
        format!("local reserved package: {r}"),
    )?;
    out["local_spoof"] = r["error"].clone();
    require(
        store(app)?.is_empty(),
        "the refused local package reached the store",
    )?;

    let slot = start(main, "device_install_file", json!({}))?;
    choose(&fixtures.join("local-1.0.0.tar.gz"))?;
    let r = settle(main, &slot)?;
    require(r["ok"] == true, format!("local install: {r}"))?;
    let p = listed(main, "example/local")?.ok_or("example/local is not listed")?;
    require(
        p["active"]["sha256"] == local
            && p["trust"] == "unverified"
            && p["source"]["kind"] == "local"
            && p["source"]["name"] == "local-1.0.0.tar.gz",
        format!("example/local as listed: {p}"),
    )?;
    let l = open_page(
        app,
        main,
        "device/example/local/local",
        &local,
        "local-1.0.0",
    )?;
    out["local_label"] = json!(l);
    require(
        store(app)? == BTreeSet::from([local.clone()]),
        format!("the store with example/local: {:?}", store(app)?),
    )?;
    out["digests"] = json!({"notes1": notes1, "notes2": notes2, "notes3": notes3, "spoof": spoof, "local": local, "pluxy_local": pluxy_local});
    out["passed"] = json!(true);
    log("device", out);
    Ok(())
}
