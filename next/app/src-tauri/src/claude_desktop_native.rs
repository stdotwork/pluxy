//! The native test's check of the Claude Desktop setup and launcher, pluxy/claude-desktop, through
//! the shared acceptance flow of an integration (native-test builds with `artifact-test-trust`
//! only). It drives the real app through the window's own commands, the real native key entry and
//! confirmations, with the packages and the files `native/claude-desktop.ts` made in this run's
//! HOME (Claude Desktop's third-party folder as a person keeps it, the standard app's files and a
//! login canary, a stow link, a chezmoi-style root, files Claude Desktop would not take) and its
//! stand-in `claude-desktop`. Each refusal has a positive control on the same route:
//!
//! - install: pluxy/claude-desktop and pluxy/execution install from the curated registry and are
//!   enabled through their native confirmations, which say "full access to your user account";
//! - key: the key is typed into native key entry; the window gets only its ID;
//! - missing: with no `configLibrary` folder yet, a preview is refused unasked and writes nothing;
//!   the confirmed `folders` operation creates it (mode 0700) and then the preview pins all three
//!   files and reads them back, with no key;
//! - unknown: a library whose `_meta.json` entries are not a list, and a `claude_desktop_config.json`
//!   that is not strict JSON, are refused unasked and change nothing;
//! - apply: asks a native confirmation naming the three files, the key's label and the
//!   same-instant sentence, never the key; then each of the three files holds exactly the bytes
//!   `expect.json` says (the deployment choice, the new gateway profile, the library index keeping
//!   the person's own configuration), modes kept, and the preview reads them back as configured;
//!   the standard app's files and the person's own configuration are untouched;
//! - restore: after the person's edit, Restore Backup puts each original back (the profile, which
//!   did not exist, is removed);
//! - clear: applied again, Claude Desktop seals the key in place, the person edits a preference
//!   and adds a configuration of their own; Clear takes out only pluxy's values: the sign-in
//!   choice, appliedId and the hybrid pointer come back, the profile and pluxy's entry go, the
//!   person's preference and configuration stay;
//! - backups: a Manual Backup of the three files (one absent), a diff naming the setting the person
//!   changed and never its value, restore over that edit, delete;
//! - stow: an apply through the selected link changes only its target, the link keeps its inode
//!   and text; once the target is replaced, a confirmed write fails as changed and writes nothing;
//!   a new preview's restore puts the original back through the link;
//! - root: a selected root with no `configLibrary` is refused; with it, its pins taken and the
//!   folder then gone, a confirmed apply writes none of the three files (a missing path aborts all
//!   writes); with it back, an apply creates the three files, the root link kept, and clear
//!   removes them again;
//! - launch: the package's recipe is a direct launch of `claude-desktop` in HOME; through the
//!   launcher, a cancelled confirmation starts nothing; a confirmed one runs the stand-in with no
//!   arguments in HOME, with no key and no login canary in its environment; an executable override
//!   runs the override instead;
//! - uninstall: disabled and removed, no pluxy/claude-desktop process is left and the files are as
//!   the last step left them;
//! - audit: the key and the login canary are in no window answer and no confirmation, no
//!   temporary file is left, and files no step names keep their bytes.
//!
//! One line on stderr: `native-test: claude-desktop <JSON>`.

use super::admission::{ok, press, require, settle, start};
use super::claude_code::{edit, mode};
use super::program::processes;
use super::routing::{call, enable, install};
use super::transactions::{leftovers, link, read, snapshot, unasked};
use super::vault::{dialog_with, type_key};
use super::{log, wait_for};
use serde_json::{json, Value};
use std::cell::RefCell;
use std::path::{Path, PathBuf};
use std::time::Duration;
use tauri::{AppHandle, Manager, Webview};

/// The answers and confirmations the check saw, for the audit.
#[derive(Default)]
struct Seen(RefCell<Vec<String>>);

impl Seen {
    fn add(&self, s: impl Into<String>) {
        self.0.borrow_mut().push(s.into());
    }
}

const PACKAGE: &str = "pluxy/claude-desktop";
const EXECUTION: &str = "pluxy/execution";
const LABEL: &str = "fixture Claude Desktop key";
const CLIENT: &str = "Claude Desktop";
const SAME_INSTANT: &str = "an edit made in the same instant by another program may be lost";
/// A confirmation keeps its Confirm button disabled for a second after it shows.
const ARMED: Duration = Duration::from_millis(1300);

/// A read operation of the package: its value, failing on an error or a native confirmation.
fn read_op(main: &Webview, seen: &Seen, op: &str, input: Value) -> Result<Value, String> {
    let (r, asked) = unasked(|| call(main, PACKAGE, op, input.clone()));
    let r = r?;
    seen.add(r.to_string());
    require(!asked, format!("{op} asked a native confirmation"))?;
    require(r["ok"] == true, format!("{op} {input}: {r}"))?;
    Ok(r["value"].clone())
}

/// A read operation that must be refused, unasked, saying says.
fn read_refused(
    main: &Webview,
    seen: &Seen,
    op: &str,
    input: Value,
    says: &str,
) -> Result<(), String> {
    let (r, asked) = unasked(|| call(main, PACKAGE, op, input.clone()));
    let r = r?;
    seen.add(r.to_string());
    require(!asked, format!("{op} {input} asked a native confirmation"))?;
    require(
        r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains(says)),
        format!("{op} {input} should be refused with {says:?}: {r}"),
    )
}

/// A write operation of the package from the window, its confirmation (naming every one of
/// needles and none of hidden) accepted: the window's answer.
fn confirmed(
    app: &AppHandle,
    main: &Webview,
    seen: &Seen,
    op: &str,
    input: Value,
    needles: &[&str],
    hidden: &[&str],
) -> Result<Value, String> {
    let slot = start(
        main,
        "program_call",
        json!({"package": PACKAGE, "op": op, "input": input}),
    )?;
    let what = format!("{PACKAGE} {op}");
    let mut all = vec![what.as_str()];
    all.extend_from_slice(needles);
    let text = dialog_with(app, &all)?;
    seen.add(text.clone());
    if hidden.iter().any(|h| text.contains(h)) {
        press("alt+c")?;
        return Err(format!("the {op} confirmation shows a value it must not"));
    }
    std::thread::sleep(ARMED);
    press("alt+o")?;
    std::thread::sleep(Duration::from_millis(500));
    let (r, asked) = unasked(|| settle(main, &slot));
    let r = r?;
    seen.add(r.to_string());
    require(!asked, format!("{op} asked a second native confirmation"))?;
    Ok(r)
}

/// A confirmed write that must succeed: its output.
fn written(
    app: &AppHandle,
    main: &Webview,
    seen: &Seen,
    op: &str,
    input: Value,
    needles: &[&str],
    hidden: &[&str],
) -> Result<Value, String> {
    let r = confirmed(app, main, seen, op, input, needles, hidden)?;
    require(r["ok"] == true, format!("{op}: {r}"))?;
    Ok(r["value"].clone())
}

/// A stand-in's record: its arguments, its folder and its environment, once it is done.
fn stand_in(records: &Path, name: &str) -> Result<(Vec<String>, String, String), String> {
    wait_for(&format!("the {name} stand-in's record"), || {
        records.join(format!("{name}.done")).exists().then_some(())
    })?;
    let r = |x: &str| read(&records.join(format!("{name}.{x}"))).unwrap_or_default();
    let argv = r("argv").lines().map(String::from).collect();
    Ok((argv, r("cwd").trim_end().to_string(), r("env")))
}

/// Writes bytes to p with mode 0644, whatever the umask.
fn write(p: &Path, bytes: &str) -> Result<(), String> {
    use std::os::unix::fs::PermissionsExt;
    std::fs::write(p, bytes)
        .and_then(|()| std::fs::set_permissions(p, std::fs::Permissions::from_mode(0o644)))
        .map_err(|e| format!("{}: {e}", p.display()))
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let x: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("expect.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let keys: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("keys.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let key = keys["key"].as_str().ok_or("no key")?.to_string();
    let s = |v: &Value| v.as_str().unwrap_or_default().to_string();
    let p = |v: &Value| PathBuf::from(s(v));
    let login = s(&x["login"]);
    let w = PACKAGE;
    let home = p(&x["home"]);
    let (library, deployment, profile, meta, other) = (
        p(&x["library"]),
        p(&x["deployment"]),
        p(&x["profile"]),
        p(&x["meta"]),
        p(&x["other"]),
    );
    let (standard, login_file) = (p(&x["standard"]), p(&x["login_file"]));
    let (stow_link, stow_text, stow_target, stow_folder, stow_profile, stow_meta) = (
        p(&x["stow"]["link"]),
        s(&x["stow"]["text"]),
        p(&x["stow"]["target"]),
        p(&x["stow"]["folder"]),
        p(&x["stow"]["profile"]),
        p(&x["stow"]["meta"]),
    );
    let (root_link, root_text, root_library) = (
        p(&x["root"]["link"]),
        s(&x["root"]["text"]),
        p(&x["root"]["library"]),
    );
    let root_dir = PathBuf::from(&root_text);
    let (unknown, broken) = (p(&x["unknown"]), p(&x["broken"]));
    let profile_id = s(&x["profile_id"]);
    let records = p(&x["records"]);
    let canon = |q: PathBuf| std::fs::canonicalize(q).map_err(|e| e.to_string());
    let (app_bin, override_bin) = (canon(p(&x["app"]))?, canon(p(&x["override"]))?);
    let bytes = |k: &str| s(&x["bytes"][k]).replace("@KEY@", &key);
    let pair = |k: &str| {
        (
            s(&x["bytes"][k][0]).replace("@KEY@", &key),
            s(&x["bytes"][k][1]).replace("@KEY@", &key),
        )
    };
    let txt = |q: &Path| q.display().to_string();
    let hidden = [key.as_str(), login.as_str()];
    let seen = Seen::default();
    let mut secret = String::new();
    let mut out = json!({"steps": []});
    let mut passed = |name: &str| out["steps"].as_array_mut().unwrap().push(json!(name));
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("{name}: {e}"));
    // The input of an apply: the window's values, the pins of a preview and the key's ID.
    let apply_input = |preview: &Value, secret: &str| {
        let mut i = x["settings_input"].clone();
        i["targets"] = preview["targets"].clone();
        i["key"] = json!(secret);
        i
    };
    let pins = |v: &Value| json!({"targets": v["targets"]});
    let in_root = |r: &Path| {
        json!({"targets": [
            {"root": r, "path": "claude_desktop_config.json"},
            {"root": r, "path": format!("configLibrary/{profile_id}.json")},
            {"root": r, "path": "configLibrary/_meta.json"},
        ]})
    };
    let untouched = || -> Result<(), String> {
        require(
            read(&standard) == Some(bytes("standard")) && read(&login_file) == Some(bytes("login")),
            "a file of the standard Claude Desktop changed",
        )
    };
    let other_untouched = || -> Result<(), String> {
        require(
            read(&other) == Some(bytes("other")),
            "the person's own gateway configuration changed",
        )
    };
    // The three default files as the check expects them: deployment, profile (None: absent), meta.
    let files_are = |d: &str, pr: Option<&str>, m: &str, what: &str| -> Result<(), String> {
        require(
            read(&deployment) == Some(bytes(d))
                && read(&profile) == pr.map(&bytes)
                && read(&meta) == Some(bytes(m)),
            format!(
                "{what}: deployment {:?}, profile {:?}, meta {:?}",
                read(&deployment),
                read(&profile),
                read(&meta)
            ),
        )
    };

    // ---- install and enable, each through its full-access confirmation
    step(
        "install",
        (|| {
            for id in [w, EXECUTION] {
                install(main, "curated", id, &s(&x["versions"][id]))?;
                let text = enable(app, main, id)?;
                require(
                    text.contains("full access to your user account"),
                    format!(
                        "the enable confirmation of {id} does not say it has full access: {text}"
                    ),
                )?;
            }
            Ok(())
        })(),
    )?;
    passed("install");
    let store = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("device-packages")
        .join("store");
    let tool_program = std::fs::read_dir(&store)
        .ok()
        .and_then(|mut d| {
            d.find_map(|e| {
                let dir = e.ok()?.path();
                let manifest: Value =
                    serde_json::from_slice(&std::fs::read(dir.join("pluxy-extension.json")).ok()?)
                        .ok()?;
                (manifest["id"] == w).then(|| dir.join("bin").join("tool"))
            })
        })
        .ok_or("pluxy/claude-desktop is not in the store")?;

    // ---- key: native key entry, the window gets only the ID
    step(
        "key",
        (|| {
            let slot = start(main, "vault_secret_add", json!({"label": LABEL}))?;
            type_key("pluxy secret", &key)?;
            let r = settle(main, &slot)?;
            seen.add(r.to_string());
            require(r["ok"] == true, format!("vault_secret_add: {r}"))?;
            secret = s(&r["value"]["id"]);
            require(secret.starts_with("s-"), format!("the new secret: {r}"))
        })(),
    )?;
    passed("key");

    // ---- missing: no configLibrary yet refuses the preview; `folders` creates it
    step(
        "missing",
        (|| {
            read_refused(main, &seen, "preview", json!({}), "configLibrary")?;
            require(!library.exists(), "a refused preview created the library")?;
            require(
                read(&deployment) == Some(bytes("deployment")),
                "a refused preview changed the deployment file",
            )?;
            written(
                app,
                main,
                &seen,
                "folders",
                json!({}),
                &["~/.config/Claude-3p/configLibrary"],
                &hidden,
            )?;
            require(
                library.is_dir() && mode(&library) == 0o700,
                format!("the library folder: mode {:o}", mode(&library)),
            )?;
            // What Claude Desktop saves there once the person sets up their own gateway.
            write(&meta, &bytes("meta"))?;
            write(&other, &bytes("other"))?;
            // The positive control on the same route: the default preview pins all three.
            let v = read_op(main, &seen, "preview", json!({}))?;
            require(
                v["targets"][0]["selected"] == json!(txt(&deployment))
                    && v["targets"][1]["selected"] == json!(txt(&profile))
                    && v["targets"][2]["selected"] == json!(txt(&meta))
                    && v["applied"] == false,
                format!("the default preview: {v}"),
            )?;
            let st = &v["settings"];
            require(
                st["state"] == "not-configured" && st["key_set"] == false,
                format!("the files read back: {st}"),
            )?;
            files_are("deployment", None, "meta", "after a preview")
        })(),
    )?;
    passed("missing");

    // ---- unknown: files Claude Desktop would not take are refused
    step(
        "unknown",
        (|| {
            read_refused(main, &seen, "preview", in_root(&unknown), "entries")?;
            read_refused(main, &seen, "preview", in_root(&broken), "JSON")?;
            require(
                read(&unknown.join("configLibrary/_meta.json")) == Some(bytes("unknown_meta"))
                    && read(&broken.join("claude_desktop_config.json")) == Some(bytes("broken")),
                "a refused file changed",
            )
        })(),
    )?;
    passed("unknown");

    // ---- apply: each of the three files holds exactly the expected bytes
    step(
        "apply",
        (|| {
            let v = read_op(main, &seen, "preview", json!({}))?;
            let o = written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
                &[
                    &txt(&deployment),
                    &txt(&profile),
                    &txt(&meta),
                    LABEL,
                    SAME_INSTANT,
                ],
                &hidden,
            )?;
            require(o["outcome"] == "applied", format!("the apply: {o}"))?;
            files_are(
                "deployment_applied",
                Some("profile_applied"),
                "meta_applied",
                "the applied files",
            )?;
            require(
                mode(&deployment) == 0o644 && mode(&profile) == 0o600 && mode(&meta) == 0o644,
                "a file's mode is not as expected",
            )?;
            let v = read_op(main, &seen, "preview", json!({}))?;
            let st = &v["settings"];
            require(
                v["applied"] == true
                    && st["state"] == "configured"
                    && st["key_set"] == true
                    && st["base_url"] == x["settings_input"]["base_url"]
                    && st["models"] == x["settings_input"]["models"],
                format!("the applied files read back: {v}"),
            )?;
            untouched()?;
            other_untouched()
        })(),
    )?;
    passed("apply");

    // ---- restore: the person's edit, then Restore Backup puts each original back
    step(
        "restore",
        (|| {
            let (from, to) = pair("edited_preference");
            edit(&deployment, &from, &to)?;
            let v = read_op(main, &seen, "preview", json!({}))?;
            require(
                v["edited"] == json!([true, false, false]),
                format!("the edit is not seen: {v}"),
            )?;
            let o = written(
                app,
                main,
                &seen,
                "restore",
                pins(&v),
                &[&txt(&deployment), &txt(&profile), &txt(&meta)],
                &hidden,
            )?;
            require(o["outcome"] == "restored", format!("the restore: {o}"))?;
            files_are("deployment", None, "meta", "the restored files")?;
            untouched()?;
            other_untouched()
        })(),
    )?;
    passed("restore");

    // ---- clear: applied again, sealed by Claude Desktop, edited by the person; only pluxy's goes
    step(
        "clear",
        (|| {
            let v = read_op(main, &seen, "preview", json!({}))?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
                &[&txt(&profile), LABEL],
                &hidden,
            )?;
            files_are(
                "deployment_applied",
                Some("profile_applied"),
                "meta_applied",
                "the second apply",
            )?;
            for (k, f) in [
                ("edited_preference", &deployment),
                ("sealed", &profile),
                ("added_entry", &meta),
            ] {
                let (from, to) = pair(k);
                edit(f, &from, &to)?;
            }
            let v = read_op(main, &seen, "preview", json!({}))?;
            let o = written(
                app,
                main,
                &seen,
                "clear",
                pins(&v),
                &[&txt(&deployment), &txt(&profile), &txt(&meta)],
                &hidden,
            )?;
            require(o["outcome"] == "cleared", format!("the clear: {o}"))?;
            files_are(
                "deployment_cleared",
                None,
                "meta_cleared",
                "the cleared files",
            )?;
            untouched()?;
            other_untouched()
        })(),
    )?;
    passed("clear");

    // ---- backups: a manual backup of the three files, diff by name, restore, delete
    step(
        "backups",
        (|| {
            let v = read_op(main, &seen, "preview", json!({}))?;
            let b = written(
                app,
                main,
                &seen,
                "backup-create",
                pins(&v),
                &[&txt(&deployment)],
                &hidden,
            )?;
            let id = s(&b["backup"]);
            let list = read_op(main, &seen, "backup-list", json!({}))?;
            let files = &list["backups"][0]["files"];
            require(
                list["backups"].as_array().map(Vec::len) == Some(1)
                    && list["backups"][0]["id"] == json!(id)
                    && files.as_array().map(Vec::len) == Some(3)
                    && files[1]["exists"] == false,
                format!("the backup list: {list}"),
            )?;
            let edit_pair = (s(&x["backups"]["edit"][0]), s(&x["backups"]["edit"][1]));
            edit(&deployment, &edit_pair.0, &edit_pair.1)?;
            let d = read_op(main, &seen, "backup-diff", json!({"backup": id}))?;
            require(
                d["files"][0]["changes"] == x["backups"]["diff"] && d["files"][2]["same"] == true,
                format!("the backup diff: {d}"),
            )?;
            for value in x["backups"]["values"].as_array().into_iter().flatten() {
                require(
                    !d.to_string().contains(&s(value)) && !list.to_string().contains(&s(value)),
                    "a backup answer holds a file's value",
                )?;
            }
            let v = read_op(main, &seen, "preview", json!({}))?;
            let mut input = pins(&v);
            input["backup"] = json!(id);
            written(
                app,
                main,
                &seen,
                "backup-restore",
                input,
                &[&txt(&deployment)],
                &hidden,
            )?;
            files_are(
                "deployment_cleared",
                None,
                "meta_cleared",
                "the restored backup",
            )?;
            written(
                app,
                main,
                &seen,
                "backup-delete",
                json!({"backup": id}),
                &[],
                &hidden,
            )?;
            let list = read_op(main, &seen, "backup-list", json!({}))?;
            require(
                list["backups"] == json!([]),
                format!("the deleted backup is still listed: {list}"),
            )
        })(),
    )?;
    passed("backups");

    // ---- stow: only the link's target changes; a replaced target refuses writes
    step(
        "stow",
        (|| {
            let sel = json!({"targets": [{"file": stow_link}, {"file": stow_profile}, {"file": stow_meta}]});
            let (inode, _) = link(&stow_link)?;
            let v = read_op(main, &seen, "preview", sel.clone())?;
            require(
                v["targets"][0]["target"] == json!(txt(&stow_target))
                    && v["targets"][0]["link"] == json!(stow_text),
                format!("the stow preview: {v}"),
            )?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
                &[&txt(&stow_link), &txt(&stow_target)],
                &hidden,
            )?;
            require(
                read(&stow_target) == Some(bytes("stow_applied")) && mode(&stow_target) == 0o640,
                format!("the stow target after apply: {:?}", read(&stow_target)),
            )?;
            require(
                read(&stow_profile) == Some(bytes("profile_applied"))
                    && read(&stow_meta) == Some(bytes("meta_new")),
                "the stow library's files after apply",
            )?;
            require(
                link(&stow_link)? == (inode, PathBuf::from(&stow_text)),
                "the stow link is not the same link with the same text",
            )?;
            require(
                read(&stow_folder.join("keep.json")) == Some(bytes("keep")),
                "a file beside the stow target changed",
            )?;
            // The target replaced (as an editor saves, by rename) after a preview.
            let v = read_op(main, &seen, "preview", sel.clone())?;
            let replaced = read(&stow_target).unwrap_or_default();
            let tmp = stow_folder.join("claude_desktop_config.json.new");
            write(&tmp, &replaced)?;
            std::fs::rename(&tmp, &stow_target).map_err(|e| e.to_string())?;
            let before = snapshot(&home.join("dotfiles"))?;
            let r = confirmed(
                app,
                main,
                &seen,
                "restore",
                pins(&v),
                &[&txt(&stow_link)],
                &hidden,
            )?;
            require(
                r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains("changed")),
                format!("a write to a replaced target should fail as changed: {r}"),
            )?;
            require(
                snapshot(&home.join("dotfiles"))? == before,
                "the refused write changed a file",
            )?;
            // A new preview's restore puts the originals back through the link.
            let v = read_op(main, &seen, "preview", sel)?;
            written(
                app,
                main,
                &seen,
                "restore",
                pins(&v),
                &[&txt(&stow_link)],
                &hidden,
            )?;
            require(
                read(&stow_target) == Some(bytes("stow"))
                    && !stow_profile.exists()
                    && !stow_meta.exists(),
                format!("the stow files after restore: {:?}", read(&stow_target)),
            )?;
            require(
                link(&stow_link)? == (inode, PathBuf::from(&stow_text)),
                "the stow link changed",
            )
        })(),
    )?;
    passed("stow");

    // ---- root: a missing library refuses; a library gone after the preview aborts every write
    step(
        "root",
        (|| {
            let sel = in_root(&root_link);
            read_refused(main, &seen, "preview", sel.clone(), "configLibrary")?;
            std::fs::create_dir(&root_library).map_err(|e| e.to_string())?;
            let (inode, _) = link(&root_link)?;
            let v = read_op(main, &seen, "preview", sel.clone())?;
            // The library gone between the preview and the apply: none of the three is written,
            // not even the deployment file, which comes first.
            let away = root_dir.join("configLibrary.away");
            std::fs::rename(&root_library, &away).map_err(|e| e.to_string())?;
            let before = snapshot(&root_dir)?;
            let r = confirmed(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
                &[&txt(&root_link)],
                &hidden,
            )?;
            require(
                r["ok"] == false,
                format!("an apply with a missing path went ahead: {r}"),
            )?;
            require(
                snapshot(&root_dir)? == before
                    && !root_dir.join("claude_desktop_config.json").exists(),
                "an apply with a missing path wrote a file",
            )?;
            std::fs::rename(&away, &root_library).map_err(|e| e.to_string())?;
            // The positive control: with the library back, the apply creates the three files.
            let v = read_op(main, &seen, "preview", sel.clone())?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
                &[&txt(&root_link)],
                &hidden,
            )?;
            let created = [
                (
                    root_dir.join("claude_desktop_config.json"),
                    "deployment_new",
                ),
                (
                    root_library.join(format!("{profile_id}.json")),
                    "profile_applied",
                ),
                (root_library.join("_meta.json"), "meta_new"),
            ];
            for (f, k) in &created {
                require(
                    read(f) == Some(bytes(k)) && mode(f) == 0o600,
                    format!("the root's {} after apply: {:?}", f.display(), read(f)),
                )?;
            }
            let v = read_op(main, &seen, "preview", sel)?;
            written(
                app,
                main,
                &seen,
                "clear",
                pins(&v),
                &[&txt(&root_link)],
                &hidden,
            )?;
            require(
                created.iter().all(|(f, _)| !f.exists()),
                "clear left a file it created",
            )?;
            require(
                link(&root_link)? == (inode, PathBuf::from(&root_text)),
                "the root link changed",
            )
        })(),
    )?;
    passed("root");

    // ---- launch: the recipe through the launcher; cancel starts nothing
    step(
        "launch",
        (|| {
            let recipe = read_op(main, &seen, "recipe", json!({}))?;
            require(
                recipe
                    == json!({"client": CLIENT, "kind": "direct", "program": "claude-desktop",
                        "args": [], "cwd": home}),
                format!("the recipe: {recipe}"),
            )?;
            let home_now = std::fs::canonicalize(&home).map_err(|e| e.to_string())?;
            let launch = |extra: Value, needles: &[&str], cancel: bool| -> Result<Value, String> {
                let mut l = recipe.clone();
                for (k, v) in extra.as_object().into_iter().flatten() {
                    l[k] = v.clone();
                }
                let slot = start(main, "launcher_launch", json!({ "launch": l }))?;
                let text = dialog_with(app, needles)?;
                seen.add(text);
                if cancel {
                    press("alt+c")?;
                } else {
                    std::thread::sleep(ARMED);
                    press("alt+o")?;
                }
                let r = settle(main, &slot)?;
                seen.add(r.to_string());
                Ok(r)
            };
            let needles = [EXECUTION, "launch", &txt(&app_bin)];
            // Cancelled: nothing starts.
            let r = launch(json!({}), &needles, true)?;
            require(
                r["ok"] == false,
                format!("a cancelled launch went ahead: {r}"),
            )?;
            std::thread::sleep(Duration::from_secs(1));
            require(
                std::fs::read_dir(&records)
                    .map_err(|e| e.to_string())?
                    .next()
                    .is_none(),
                "a cancelled launch started something",
            )?;
            // Confirmed: Claude Desktop runs in HOME, with no arguments and no key.
            let r = launch(json!({}), &needles, false)?;
            require(r["ok"] == true, format!("the confirmed launch: {r}"))?;
            let (argv, cwd, env) = stand_in(&records, "app")?;
            require(
                argv.is_empty() && cwd == txt(&home_now),
                format!("claude-desktop ran with {argv:?} in {cwd}"),
            )?;
            require(
                !env.contains(&key) && !env.contains(&login),
                "claude-desktop's environment holds the key or the login canary",
            )?;
            // The executable override runs instead.
            let r = launch(
                json!({"override": override_bin}),
                &[EXECUTION, "launch", &txt(&override_bin)],
                false,
            )?;
            require(r["ok"] == true, format!("the override launch: {r}"))?;
            let (argv, cwd, _) = stand_in(&records, "override")?;
            require(
                argv.is_empty() && cwd == txt(&home_now),
                format!("the override ran with {argv:?} in {cwd}"),
            )
        })(),
    )?;
    passed("launch");

    // ---- uninstall: no tool process is left, the files stay as they are
    step(
        "uninstall",
        (|| {
            let before = snapshot(&home.join(".config"))?;
            seen.add(ok(main, "device_enable", json!({"id": w, "enabled": false}))?.to_string());
            seen.add(ok(main, "device_remove", json!({"id": w}))?.to_string());
            wait_for("no pluxy/claude-desktop process once removed", || {
                processes(&tool_program).is_empty().then_some(())
            })?;
            require(
                snapshot(&home.join(".config"))? == before,
                "removing the package changed a file",
            )
        })(),
    )?;
    passed("uninstall");

    // ---- audit
    step(
        "audit",
        (|| {
            let seen = seen.0.borrow();
            require(!seen.is_empty(), "the check saw no answer")?;
            require(
                !seen.iter().any(|t| hidden.iter().any(|h| t.contains(h))),
                "the key or the login canary reached the window or a confirmation",
            )?;
            let mut left = leftovers(&home.join(".config"))?;
            left.extend(leftovers(&home.join("dotfiles"))?);
            left.extend(leftovers(&home.join(".local/share/chezmoi"))?);
            require(left.is_empty(), format!("temporary files left: {left:?}"))?;
            untouched()?;
            other_untouched()?;
            require(
                read(&unknown.join("configLibrary/_meta.json")) == Some(bytes("unknown_meta"))
                    && read(&broken.join("claude_desktop_config.json")) == Some(bytes("broken")),
                "a refused file changed",
            )
        })(),
    )?;
    passed("audit");

    out["passed"] = json!(true);
    log("claude_desktop", out);
    Ok(())
}
