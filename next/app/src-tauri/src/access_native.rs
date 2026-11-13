//! The native test's check of Access (native-test builds only): the broker that lets a web
//! package on this device read the files and folders the person chose. It drives the real app
//! as the person would: the window's own commands, the native file chooser and the native
//! confirmation (real X input from `xdotool`), and page webviews whose pages make channel calls.
//! `native/access-suite.ts` runs the app twice in one session, through the stages `grant` and
//! `relaunch`, with GTK_USE_PORTAL=1, so the chooser is the desktop portal's. Fixtures come from
//! `native/access.ts` in `PLUXY_APP_TEST_ACCESS`, with what each file holds in `expect.json`
//! (written there, not by the app). Each denial has a positive control on the same route:
//!
//! - signer policy: an unsigned package and a signed one whose signer is not approved are
//!   refused a grant; the signer's native approval, cancelled then confirmed, lets it ask;
//! - grants: a cancelled chooser and a cancelled confirmation grant nothing; a confirmed one
//!   grants the chosen config-manager link's target, which the package's page reads;
//! - scope: a path with a file grant, an undeclared or ungranted resource, another package's
//!   grant, a page core lists, traversal, absolute paths and a link leaving a granted folder are
//!   refused, while the granted folder lists and reads, also through a link inside it;
//! - changes: a retargeted link and a replaced target are refused until granted again;
//! - revocation: a read in flight when the grant is revoked answers `revoked`, not bytes, and
//!   the revocation returns only after it finished;
//! - portal: a file the document portal offers is granted for this session only and is refused
//!   once the portal no longer offers it;
//! - releases and sources: a newer release and the same release from another source are not
//!   granted what the first was; removing a package removes its grants;
//! - relaunch: the kept grant reads again, the session-only one is gone.
//!
//! One line on stderr: `native-test: access <JSON>`.

use super::device::{call, listed, ok, refused, require, settle, start, x};
use super::{eval, log, wait_for};
use crate::MAIN;
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;
use tauri::{AppHandle, Manager, Webview};

static CALLS: AtomicU64 = AtomicU64::new(0);
const CONFIRMATION: &str = "pluxy native confirmation";

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

/// The native file chooser's title for a grant.
fn chooser_title(id: &str, resource: &str, folder: bool) -> String {
    format!(
        "Choose a {} for {id}: {resource}",
        if folder { "folder" } else { "file" }
    )
}

/// Types path into the chooser titled title and accepts it. The session has no recent files
/// (run.ts), so a folder chooser opens in a folder, where the typed location is what it
/// chooses. A folder is typed as `<folder>/`: text ending in a separator names that folder
/// itself (gtkfilechooserentry.c `gtk_file_chooser_get_directory_for_text`), and with two
/// subfolders starting with different letters (native/access.ts) the entry has no single
/// completion to add to it.
fn choose(title: &str, path: &Path, folder: bool) -> Result<(), String> {
    let id = wait_for(title, || window(title))?;
    x(&["windowfocus", "--sync", &id])?;
    x(&["key", "--clearmodifiers", "ctrl+l"])?;
    std::thread::sleep(Duration::from_millis(300));
    let path = path.to_str().ok_or("fixture path is not UTF-8")?;
    let typed = if folder {
        format!("{path}/")
    } else {
        path.to_string()
    };
    x(&["type", "--clearmodifiers", "--delay", "20", &typed])?;
    std::thread::sleep(Duration::from_millis(300));
    x(&["key", "--clearmodifiers", "Return"])?;
    Ok(())
}

#[derive(Clone, Copy, PartialEq)]
enum Answer {
    CancelChooser,
    CancelConfirmation,
    Confirm,
}

/// Asks for a grant of resource to package id, choosing path, and answers as told. Returns
/// what `access_grant` returned.
fn grant(
    main: &Webview,
    id: &str,
    resource: &str,
    path: &Path,
    folder: bool,
    answer: Answer,
) -> Result<Value, String> {
    let slot = start(
        main,
        "access_grant",
        json!({"id": id, "resource": resource}),
    )?;
    let title = chooser_title(id, resource, folder);
    if answer == Answer::CancelChooser {
        key(&title, "Escape")?;
    } else {
        choose(&title, path, folder)?;
        key(
            CONFIRMATION,
            if answer == Answer::Confirm {
                "alt+o"
            } else {
                "alt+c"
            },
        )?;
    }
    let r = settle(main, &slot)?;
    let want = if answer == Answer::Confirm {
        "granted"
    } else {
        "cancelled"
    };
    require(
        r["ok"] == true && r["value"]["outcome"] == want,
        format!("access_grant {id} {resource} ({path:?}) should be {want}: {r}"),
    )?;
    Ok(r["value"].clone())
}

/// Approves (or cancels approving) the signer of package id's active release.
fn signer(main: &Webview, id: &str, confirm: bool) -> Result<(), String> {
    let slot = start(main, "access_signer", json!({"id": id}))?;
    key(CONFIRMATION, if confirm { "alt+o" } else { "alt+c" })?;
    let r = settle(main, &slot)?;
    let want = if confirm { "approved" } else { "cancelled" };
    require(
        r["ok"] == true && r["value"]["outcome"] == want,
        format!("access_signer {id} should be {want}: {r}"),
    )
}

fn grants(main: &Webview) -> Result<Vec<Value>, String> {
    let list = ok(main, "access_list", json!({}))?;
    list["grants"]
        .as_array()
        .cloned()
        .ok_or_else(|| format!("access_list: {list}"))
}

fn grant_of(main: &Webview, id: &str, resource: &str) -> Result<Option<Value>, String> {
    Ok(grants(main)?
        .into_iter()
        .find(|g| g["package"] == id && g["resource"] == resource))
}

/// The page webview showing a release of digest, its page answering through the channel.
fn page(app: &AppHandle, main: &Webview, route: &str, digest: &str) -> Result<Webview, String> {
    wait_for(&format!("{route} in the navigation"), || {
        (eval(
            main,
            &format!("document.querySelector('a[href=\"#/{route}\"]') !== null"),
        ) == Some(json!(true)))
        .then_some(())
    })?;
    main.eval(format!("location.hash = {}", json!(format!("#/{route}"))))
        .map_err(|e| e.to_string())?;
    let label = wait_for(&format!("a page webview showing {digest}"), || {
        app.webviews().into_iter().find_map(|(label, w)| {
            (label != MAIN
                && eval(&w, "document.querySelector('iframe')?.src ?? null")
                    .and_then(|v| v.as_str().map(|s| s.contains(&format!("/{digest}/"))))
                    == Some(true))
            .then_some(label)
        })
    })?;
    let w = app
        .get_webview(&label)
        .ok_or_else(|| format!("no webview {label}"))?;
    let r = channel(&w, "theme.get", Value::Null)?;
    require(
        r.get("result").is_some(),
        format!("{route}: theme.get: {r}"),
    )?;
    Ok(w)
}

/// Collects the page's replies in its webview's top document, keyed by call ID, with the text
/// of any `data` (base64) decoded there.
const REPLIES: &str = "if (!window.__accessReplies) { window.__accessReplies = {}; window.addEventListener('message', (e) => {\
     const f = document.querySelector('iframe'); const m = e.data?.accessReply;\
     if (!f || e.source !== f.contentWindow || !m || typeof m.id !== 'string') return;\
     if (typeof m.result?.data === 'string') { try { m.text = new TextDecoder().decode(Uint8Array.from(atob(m.result.data), (c) => c.charCodeAt(0))); } catch { m.text = null; } }\
     window.__accessReplies[m.id] = m; }); }";

/// Sends a channel call from the page in w and returns its id, without waiting for the reply.
fn send(w: &Webview, method: &str, params: Value) -> Result<String, String> {
    w.eval(REPLIES).map_err(|e| e.to_string())?;
    let id = format!("a{}", CALLS.fetch_add(1, Ordering::SeqCst));
    w.eval(format!(
        "document.querySelector('iframe').contentWindow.postMessage({{accessCall: {}}}, '*')",
        json!({"id": id, "method": method, "params": params})
    ))
    .map_err(|e| e.to_string())?;
    Ok(id)
}

fn reply(w: &Webview, id: &str) -> Result<Value, String> {
    wait_for(&format!("the page's reply to {id}"), || {
        eval(
            w,
            &format!("window.__accessReplies?.[{}] ?? null", json!(id)),
        )
        .filter(|v| !v.is_null())
    })
}

/// A channel call from the page in w, and its reply.
fn channel(w: &Webview, method: &str, params: Value) -> Result<Value, String> {
    let id = send(w, method, params)?;
    reply(w, &id)
}

/// A read the page must be given: what it got is want.
fn reads(w: &Webview, params: Value, want: &str) -> Result<(), String> {
    let r = channel(w, "resources.read", params.clone())?;
    require(
        r["text"] == want && r["result"]["eof"] == true,
        format!("resources.read {params} should give {want:?}: {r}"),
    )
}

/// A call the page must be refused, with error code.
fn denied(w: &Webview, method: &str, params: Value, code: &str) -> Result<Value, String> {
    let r = channel(w, method, params.clone())?;
    require(
        r.get("result").is_none() && r["error"]["code"] == code && r.get("text").is_none(),
        format!("{method} {params} should be refused with {code}: {r}"),
    )?;
    Ok(r["error"].clone())
}

/// Posts a call straight to the native scheme from w's top document, as a hostile frame could,
/// and returns the HTTP status and body.
fn direct(w: &Webview, method: &str, params: Value) -> Result<(Value, String), String> {
    let id = format!("d{}", CALLS.fetch_add(1, Ordering::SeqCst));
    w.eval(format!(
        "fetch('/frame').then((r) => r.json()).then((info) => fetch('/send', {{method: 'POST', body: JSON.stringify({{generation: info.generation, type: 'call', data: {}}})}}))\
         .then(async (r) => {{ (window.__accessDirect ??= {{}})[{id}] = {{status: r.status, body: await r.text()}}; }})",
        json!({"v": 0, "id": id, "method": method, "params": params}),
        id = json!(id),
    ))
    .map_err(|e| e.to_string())?;
    let r = wait_for("a direct scheme call", || {
        eval(
            w,
            &format!("window.__accessDirect?.[{}] ?? null", json!(id)),
        )
        .filter(|v| !v.is_null())
    })?;
    Ok((r, id))
}

/// session_probe (examples/session_probe.rs), for the document portal.
fn probe(args: &[&str]) -> Result<String, String> {
    let bin = std::env::var("PLUXY_APP_TEST_PROBE").map_err(|_| "no PLUXY_APP_TEST_PROBE")?;
    let out = Command::new(bin)
        .args(args)
        .output()
        .map_err(|e| e.to_string())?;
    if !out.status.success() {
        return Err(format!(
            "session_probe {args:?}: {}",
            String::from_utf8_lossy(&out.stderr)
        ));
    }
    Ok(String::from_utf8_lossy(&out.stdout).into_owned())
}

fn probed<'a>(out: &'a str, key: &str) -> Result<&'a str, String> {
    out.lines()
        .find_map(|l| l.strip_prefix(&format!("{key}=")))
        .ok_or_else(|| format!("session_probe printed no {key}: {out}"))
}

/// Replaces link with one to target, as a config manager re-linking it would (rename over).
fn relink(link: &Path, target: &str) -> Result<(), String> {
    let tmp = link.with_extension("relink");
    std::os::unix::fs::symlink(target, &tmp).map_err(|e| e.to_string())?;
    std::fs::rename(&tmp, link).map_err(|e| e.to_string())
}

/// The app's saved Access file, as text.
fn saved(app: &AppHandle) -> Result<String, String> {
    let dir = app.path().app_config_dir().map_err(|e| e.to_string())?;
    std::fs::read_to_string(dir.join("access.json")).map_err(|e| format!("access.json: {e}"))
}

pub(crate) fn run(app: &AppHandle, stage: &str) -> Result<(), String> {
    let fixtures = PathBuf::from(
        std::env::var("PLUXY_APP_TEST_ACCESS").map_err(|_| "no PLUXY_APP_TEST_ACCESS")?,
    );
    let expect: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("expect.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let main = wait_for("the window", || app.get_webview(MAIN))?;
    wait_for("the window's shell", || {
        eval(&main, "document.querySelector('[data-part=nav]') !== null").filter(|v| v == true)
    })?;
    let text = |k: &str| {
        expect["contents"][k]
            .as_str()
            .unwrap_or_default()
            .to_string()
    };
    let digest = |k: &str| {
        expect["digests"][k]
            .as_str()
            .unwrap_or_default()
            .to_string()
    };
    let data = PathBuf::from(expect["data"].as_str().ok_or("expect.json has no data")?);
    let mut out = json!({"stage": stage});
    match stage {
        "grant" => grant_stage(
            app, &main, &fixtures, &expect, &data, &text, &digest, &mut out,
        )?,
        "relaunch" => {
            // The kept grant is listed and reads again; the session-only one is gone.
            let gs = grants(&main)?;
            require(
                gs.len() == 1
                    && gs[0]["package"] == "example/other"
                    && gs[0]["resource"] == "config",
                format!("grants after relaunch: {gs:?}"),
            )?;
            let other = page(app, &main, "device/example/other/other", &digest("other"))?;
            reads(&other, json!({"resource": "config"}), &text("other"))?;
            denied(
                &other,
                "resources.read",
                json!({"resource": "doc"}),
                "not_granted",
            )?;
            out["grants"] = json!(gs);
        }
        _ => return Err(format!("unknown access stage {stage}")),
    }
    out["passed"] = json!(true);
    log("access", out);
    crate::quit(app);
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn grant_stage(
    app: &AppHandle,
    main: &Webview,
    fixtures: &Path,
    expect: &Value,
    data: &Path,
    text: &dyn Fn(&str) -> String,
    digest: &dyn Fn(&str) -> String,
    out: &mut Value,
) -> Result<(), String> {
    let signed_index = fixtures.join("signed.json").display().to_string();
    let install = |registry: &str, id: &str, version: &str| {
        ok(
            main,
            "device_install",
            json!({"registry": registry, "id": id, "version": version}),
        )
    };
    require(
        grants(main)?.is_empty(),
        "a fresh app has grants".to_string(),
    )?;

    // Signer policy. An unsigned package may display, never be granted a file.
    install("signed", "example/plain", "1.0.0")?;
    let plain = listed(main, "example/plain")?.ok_or("example/plain is not listed")?;
    require(
        plain["active"]["signer"].is_null() && plain["trust"] == "unverified",
        format!("example/plain as listed: {plain}"),
    )?;
    out["unsigned"] = json!(refused(
        main,
        "access_grant",
        json!({"id": "example/plain", "resource": "config"}),
        "signed",
    )?);

    // A signed package, its signer not yet approved: refused too.
    install("signed", "example/files", "1.0.0")?;
    let files = listed(main, "example/files")?.ok_or("example/files is not listed")?;
    let s = &files["active"]["signer"];
    require(
        s["publisher"] == "example"
            && s["key_id"] == expect["key_id"]
            && s["public_key"] == expect["public_key"]
            && s["approved"] == false
            && files["trust"] == "unverified",
        format!("example/files as listed: {files}"),
    )?;
    out["unapproved"] = json!(refused(
        main,
        "access_grant",
        json!({"id": "example/files", "resource": "config"}),
        "signer",
    )?);

    // Approving the signer takes a native confirmation: cancelled, nothing changes.
    signer(main, "example/files", false)?;
    let files = listed(main, "example/files")?.ok_or("example/files is gone")?;
    require(
        files["active"]["signer"]["approved"] == false,
        format!("cancelled signer approval: {files}"),
    )?;
    refused(
        main,
        "access_grant",
        json!({"id": "example/files", "resource": "config"}),
        "signer",
    )?;
    signer(main, "example/files", true)?;
    let list = ok(main, "access_list", json!({}))?;
    require(
        list["signers"].as_array().is_some_and(|s| {
            s.len() == 1
                && s[0]["publisher"] == "example"
                && s[0]["key_id"] == expect["key_id"]
                && s[0]["public_key"] == expect["public_key"]
                && s[0]["source"] == signed_index.as_str()
        }),
        format!("approved signers: {list}"),
    )?;

    // A grant: cancelled chooser, cancelled confirmation, then the person's choice confirmed.
    let link = data.join("config");
    let target = data.join("stow").join("config");
    grant(
        main,
        "example/files",
        "config",
        &link,
        false,
        Answer::CancelChooser,
    )?;
    grant(
        main,
        "example/files",
        "config",
        &link,
        false,
        Answer::CancelConfirmation,
    )?;
    require(grants(main)?.is_empty(), "a cancelled grant was kept")?;
    let g = grant(
        main,
        "example/files",
        "config",
        &link,
        false,
        Answer::Confirm,
    )?;
    require(
        g["grant"]["selected"] == link.display().to_string()
            && g["grant"]["target"] == target.display().to_string()
            && g["grant"]["session_only"] == false,
        format!("the file grant: {g}"),
    )?;

    // The package's page reads what was granted, and nothing else.
    let files_page = page(app, main, "device/example/files/files", &digest("files1"))?;
    let status = channel(&files_page, "resources.status", Value::Null)?;
    require(
        status["result"]["resources"].as_array().is_some_and(|r| {
            r.len() == 2
                && r.iter()
                    .any(|x| x["id"] == "config" && x["granted"] == true)
                && r.iter().any(|x| x["id"] == "tree" && x["granted"] == false)
        }),
        format!("resources.status: {status}"),
    )?;
    reads(&files_page, json!({"resource": "config"}), &text("config"))?;
    denied(
        &files_page,
        "resources.read",
        json!({"resource": "config", "path": "../sibling"}),
        "bad_request",
    )?;
    denied(
        &files_page,
        "resources.read",
        json!({"resource": "tree", "path": "top.txt"}),
        "not_granted",
    )?;
    denied(
        &files_page,
        "resources.read",
        json!({"resource": "undeclared"}),
        "bad_request",
    )?;

    // A folder: it lists and reads inside, also through a link that stays inside; nothing
    // outside it is read, by `..`, an absolute path or a link that leaves it.
    let tree = data.join("tree");
    let g = grant(main, "example/files", "tree", &tree, true, Answer::Confirm)?;
    require(
        g["grant"]["selected"] == tree.display().to_string()
            && g["grant"]["target"] == tree.display().to_string(),
        format!("the folder grant is of the typed folder: {g}"),
    )?;
    let listing = channel(
        &files_page,
        "resources.list",
        json!({"resource": "tree", "path": ""}),
    )?;
    let names: Vec<(String, String)> = listing["result"]["entries"]
        .as_array()
        .ok_or_else(|| format!("resources.list: {listing}"))?
        .iter()
        .map(|e| {
            (
                e["name"].as_str().unwrap_or_default().to_string(),
                e["kind"].as_str().unwrap_or_default().to_string(),
            )
        })
        .collect();
    require(
        names
            == [
                (String::from("escape"), String::from("link")),
                (String::from("inner"), String::from("folder")),
                (String::from("inside"), String::from("link")),
                (String::from("notes"), String::from("folder")),
                (String::from("top.txt"), String::from("file")),
            ],
        format!("the granted folder's entries: {names:?}"),
    )?;
    reads(
        &files_page,
        json!({"resource": "tree", "path": "top.txt"}),
        &text("top"),
    )?;
    reads(
        &files_page,
        json!({"resource": "tree", "path": "inner/ok.txt"}),
        &text("inner"),
    )?;
    reads(
        &files_page,
        json!({"resource": "tree", "path": "inside"}),
        &text("inner"),
    )?;
    let outside = data.join("outside.txt").display().to_string();
    for path in [
        "../outside.txt",
        "inner/../../outside.txt",
        outside.as_str(),
        "escape",
    ] {
        denied(
            &files_page,
            "resources.read",
            json!({"resource": "tree", "path": path}),
            "refused",
        )?;
    }
    denied(
        &files_page,
        "resources.list",
        json!({"resource": "tree", "path": ".."}),
        "refused",
    )?;

    // Another package from the same signer and source: refused files.config's grant, then
    // given one of its own on the same page and method.
    install("signed", "example/other", "1.0.0")?;
    let other = listed(main, "example/other")?.ok_or("example/other is not listed")?;
    require(
        other["active"]["signer"]["approved"] == true,
        format!("example/other as listed (same signer and source): {other}"),
    )?;
    let other_page = page(app, main, "device/example/other/other", &digest("other"))?;
    denied(
        &other_page,
        "resources.read",
        json!({"resource": "config"}),
        "not_granted",
    )?;
    grant(
        main,
        "example/other",
        "config",
        &data.join("other.txt"),
        false,
        Answer::Confirm,
    )?;
    reads(&other_page, json!({"resource": "config"}), &text("other"))?;

    // A page core lists has no Access methods, even posting to the native scheme itself; the
    // same direct post from a device page is admitted and answered.
    let route = std::env::var("PLUXY_APP_TEST_PAGE").map_err(|_| "no PLUXY_APP_TEST_PAGE")?;
    main.eval(format!("location.hash = {}", json!(format!("#/{route}"))))
        .map_err(|e| e.to_string())?;
    let core_label = wait_for("the core page's webview", || {
        app.webviews().into_iter().find_map(|(label, w)| {
            (label != MAIN
                && eval(&w, "document.querySelector('iframe')?.src ?? ''")
                    .and_then(|v| v.as_str().map(|s| s.starts_with("http")))
                    == Some(true))
            .then_some(label)
        })
    })?;
    let core_page = app.get_webview(&core_label).ok_or("no core page webview")?;
    wait_for("the core page's channel", || {
        app.state::<crate::App>()
            .pages
            .lock()
            .unwrap()
            .get(&core_label)
            .map(|_| ())
    })?;
    std::thread::sleep(Duration::from_secs(1));
    let (core, _) = direct(&core_page, "resources.read", json!({"resource": "config"}))?;
    require(
        core["status"] == 400
            && core["body"]
                .as_str()
                .is_some_and(|b| b.contains("unknown_method")),
        format!("a core page's direct resources.read: {core}"),
    )?;
    let files_page = page(app, main, "device/example/files/files", &digest("files1"))?;
    let (dev, id) = direct(&files_page, "resources.read", json!({"resource": "config"}))?;
    require(
        dev["status"] == 204,
        format!("a device page's direct resources.read: {dev}"),
    )?;
    let r = reply(&files_page, &id)?;
    require(
        r["text"] == text("config"),
        format!("the direct read's reply: {r}"),
    )?;
    out["core_page"] = core;

    // The chosen link retargeted: refused, and still refused when put back, until granted
    // again. Then the target replaced: refused again.
    relink(&link, "stow/sibling")?;
    denied(
        &files_page,
        "resources.read",
        json!({"resource": "config"}),
        "changed",
    )?;
    relink(&link, "stow/config")?;
    denied(
        &files_page,
        "resources.read",
        json!({"resource": "config"}),
        "changed",
    )?;
    grant(
        main,
        "example/files",
        "config",
        &link,
        false,
        Answer::Confirm,
    )?;
    reads(&files_page, json!({"resource": "config"}), &text("config"))?;
    let new = data.join("stow").join("config.new");
    std::fs::write(&new, text("replaced")).map_err(|e| e.to_string())?;
    std::fs::rename(&new, &target).map_err(|e| e.to_string())?;
    denied(
        &files_page,
        "resources.read",
        json!({"resource": "config"}),
        "changed",
    )?;
    grant(
        main,
        "example/files",
        "config",
        &link,
        false,
        Answer::Confirm,
    )?;
    reads(
        &files_page,
        json!({"resource": "config"}),
        &text("replaced"),
    )?;

    // Revoked while a read is in flight: the read answers revoked, and the revocation returns
    // only once that read is done.
    let hold = PathBuf::from(
        std::env::var("PLUXY_APP_TEST_ACCESS_HOLD").map_err(|_| "no PLUXY_APP_TEST_ACCESS_HOLD")?,
    );
    let held = hold.with_extension("held");
    std::fs::write(&hold, "").map_err(|e| e.to_string())?;
    let pending = send(&files_page, "resources.read", json!({"resource": "config"}))?;
    wait_for("the broker to hold the read", || {
        held.exists().then_some(())
    })?;
    let g = grant_of(main, "example/files", "config")?.ok_or("no config grant")?;
    let revoking = start(main, "access_revoke", json!({"grant": g["id"]}))?;
    std::thread::sleep(Duration::from_secs(1));
    let early = eval(
        main,
        &format!("window.__pluxyDevice?.[{}] ?? null", json!(revoking)),
    );
    require(
        early.as_ref().is_none_or(Value::is_null),
        format!("revocation returned while a read was in flight: {early:?}"),
    )?;
    std::fs::remove_file(&hold).map_err(|e| e.to_string())?;
    let revoked = settle(main, &revoking)?;
    require(
        revoked["ok"] == true && revoked["value"]["finished"] == 1,
        format!("access_revoke: {revoked}"),
    )?;
    let r = reply(&files_page, &pending)?;
    require(
        r["error"]["code"] == "revoked" && r.get("text").is_none(),
        format!("the read in flight at revocation: {r}"),
    )?;
    denied(
        &files_page,
        "resources.read",
        json!({"resource": "config"}),
        "not_granted",
    )?;
    require(
        grant_of(main, "example/files", "config")?.is_none(),
        "the revoked grant is listed",
    )?;

    // A file the document portal offers: granted for this session only, refused once the
    // portal no longer offers it (never read from its host file instead).
    let offered = probe(&["documents", &data.join("portal.txt").display().to_string()])?;
    let doc = probed(&offered, "doc")?.to_string();
    let doc_path = Path::new(probed(&offered, "mount")?)
        .join(&doc)
        .join(probed(&offered, "name")?);
    let g = grant(
        main,
        "example/other",
        "doc",
        &doc_path,
        false,
        Answer::Confirm,
    )?;
    require(
        g["grant"]["session_only"] == true
            && g["grant"]["selected"] == doc_path.display().to_string(),
        format!("the portal grant: {g}"),
    )?;
    let other_page = page(app, main, "device/example/other/other", &digest("other"))?;
    reads(&other_page, json!({"resource": "doc"}), &text("portal"))?;
    let kept = saved(app)?;
    require(
        !kept.contains(&doc) && kept.contains("other.txt"),
        format!("access.json keeps the session-only grant or lacks the kept one: {kept}"),
    )?;
    probe(&["documents-delete", &doc])?;
    denied(
        &other_page,
        "resources.read",
        json!({"resource": "doc"}),
        "unavailable",
    )?;

    // A reply that comes back after its page reloaded is dropped: the reloaded document is not
    // the one that asked. Its positive control, the same read on the same page before the
    // reload, answers.
    let files_page = page(app, main, "device/example/files/files", &digest("files1"))?;
    let top = json!({"resource": "tree", "path": "top.txt"});
    reads(&files_page, top.clone(), &text("top"))?;
    let tree_grant = grant_of(main, "example/files", "tree")?.ok_or("no tree grant")?;
    let tree_grant = tree_grant["id"]
        .as_str()
        .ok_or("a grant without id")?
        .to_string();
    let _ = std::fs::remove_file(&held);
    std::fs::write(&hold, "").map_err(|e| e.to_string())?;
    let stale = send(&files_page, "resources.read", top.clone())?;
    wait_for("the broker to hold the read", || {
        held.exists().then_some(())
    })?;
    files_page
        .eval("window.__beforeReload = true; location.reload()")
        .map_err(|e| e.to_string())?;
    wait_for("the page to load again", || {
        (eval(
            &files_page,
            "window.__beforeReload === undefined && document.readyState === 'complete'",
        ) == Some(json!(true)))
        .then_some(())
    })?;
    files_page.eval(REPLIES).map_err(|e| e.to_string())?;
    std::fs::remove_file(&hold).map_err(|e| e.to_string())?;
    // The broker counts a read in flight until its answer went to the page (or was dropped).
    wait_for("the held read to be answered", || {
        (app.state::<crate::App>()
            .access
            .lock()
            .unwrap()
            .in_flight(&tree_grant)
            == 0)
            .then_some(())
    })?;
    // A delivered reply would arrive in the page a moment later: give it that moment.
    std::thread::sleep(Duration::from_secs(1));
    let after = eval(
        &files_page,
        &format!("window.__accessReplies?.[{}] ?? null", json!(stale)),
    );
    require(
        after.as_ref().is_some_and(Value::is_null),
        format!("the reloaded page got the earlier document's reply: {after:?}"),
    )?;

    // A newer release is not granted what 1.0.0 was; granted itself, it reads.
    install("signed", "example/files", "2.0.0")?;
    let files_page = page(app, main, "device/example/files/files", &digest("files2"))?;
    denied(
        &files_page,
        "resources.read",
        json!({"resource": "tree", "path": "top.txt"}),
        "not_granted",
    )?;
    grant(main, "example/files", "tree", &tree, true, Answer::Confirm)?;
    reads(&files_page, top.clone(), &text("top"))?;

    // Removing the package removes its grants; the same release from another source, with the
    // same ID, version, bytes and key, starts with no trust.
    ok(main, "device_remove", json!({"id": "example/files"}))?;
    require(
        grants(main)?
            .iter()
            .all(|g| g["package"] != "example/files"),
        "grants of a removed package are listed",
    )?;
    install("mirror", "example/files", "1.0.0")?;
    let files = listed(main, "example/files")?.ok_or("example/files is not listed")?;
    require(
        files["active"]["sha256"] == digest("files1").as_str()
            && files["active"]["signer"]["approved"] == false,
        format!("example/files from the mirror: {files}"),
    )?;
    out["source_change"] = json!(refused(
        main,
        "access_grant",
        json!({"id": "example/files", "resource": "config"}),
        "signer",
    )?);
    // The positive control on the same route: the mirror's signer approved for the mirror, the
    // same grant is given and reads.
    signer(main, "example/files", true)?;
    grant(
        main,
        "example/files",
        "config",
        &link,
        false,
        Answer::Confirm,
    )?;
    let files_page = page(app, main, "device/example/files/files", &digest("files1"))?;
    reads(
        &files_page,
        json!({"resource": "config"}),
        &text("replaced"),
    )?;
    let r = call(main, "access_list", json!({}))?;
    out["grants"] = r["value"]["grants"].clone();
    Ok(())
}
