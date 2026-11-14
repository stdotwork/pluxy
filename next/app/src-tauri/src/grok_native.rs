//! The native test's check of the Grok Build setup and launcher, pluxy/grok-setup, through the
//! shared acceptance flow of an integration (native-test builds with `artifact-test-trust` only).
//! It drives the real app through the window's own commands, the real native key entry and
//! confirmations, with the packages and the files `native/grok.ts` made in this run's HOME (Grok
//! Build's config.toml as a person keeps it, a login canary file, a stow link, a chezmoi-style
//! root, files Grok Build would not take) and its stand-in terminal and `grok`. Each refusal has a
//! positive control on the same route:
//!
//! - install: pluxy/grok-setup and pluxy/execution install from the curated registry and are
//!   enabled through their native confirmations, which say "full access to your user account";
//! - key: the key is typed into native key entry; the window gets only its ID;
//! - unknown: a preview of each file Grok Build would not take (`expect.json`'s `refused`) is
//!   refused unasked and changes nothing; a preview of the person's file (a read, asked nothing)
//!   pins it and reads its values back, with no key;
//! - apply: asks a native confirmation naming the file, the key's label and the same-instant
//!   sentence, never the key; then config.toml holds exactly the bytes `expect.json` says
//!   (comments, the person's own models and unknown settings kept), and the preview reads it back
//!   as configured; the login canary file is untouched;
//! - restore: after the person's edit, Restore Backup puts the original bytes back;
//! - clear: applied again and edited twice by the person, Clear takes out only the untouched
//!   values: their default model comes back, their edit of a written model and their own setting
//!   stay;
//! - stow: an apply through the selected link changes only its target, the link keeps its inode
//!   and text; once the target is replaced, a confirmed write fails as changed and writes
//!   nothing; a new preview's restore puts the original back through the link;
//! - root: an apply to a file inside the selected root creates it, the root link kept; clear
//!   removes it again;
//! - backups: a cancelled Manual Backup keeps nothing; a confirmed one keeps the file; the diff
//!   names the person's changed settings, never a value; restore puts the backed-up bytes back;
//!   delete removes it;
//! - launch: the package's recipe is the terminal running `grok`; through the launcher, a
//!   cancelled confirmation starts nothing; a confirmed one runs the stand-in terminal with
//!   exactly `-e <grok>` in the chosen folder, and `grok` with no arguments and no key in its
//!   environment; an executable override runs the override instead; with Grok Build's installed
//!   copy in `~/.grok/bin`, the recipe names it and the terminal runs it;
//! - uninstall: disabled and removed, no pluxy/grok-setup process is left and the file is as
//!   the last step left it;
//! - audit: the key and the login canary are in no window answer and no confirmation, no
//!   temporary file is left, and files no step names keep their bytes.
//!
//! One line on stderr: `native-test: grok <JSON>`.

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

const PACKAGE: &str = "pluxy/grok-setup";
const EXECUTION: &str = "pluxy/execution";
const LABEL: &str = "fixture Grok Build key";
const CLIENT: &str = "Grok Build";
const SAME_INSTANT: &str = "an edit made in the same instant by another program may be lost";
/// Confirm stays disabled for a second in a dialog another package asked for.
const ARMED: Duration = Duration::from_millis(1300);
/// The host asks at most one confirmation a second for the same origin.
const SPACED: Duration = Duration::from_millis(1100);

/// The answers and confirmations the check saw, for the audit.
#[derive(Default)]
struct Seen(RefCell<Vec<String>>);

impl Seen {
    fn add(&self, s: impl Into<String>) {
        self.0.borrow_mut().push(s.into());
    }
}

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
/// needles and none of hidden) accepted, or cancelled when cancel: the window's answer.
#[allow(clippy::too_many_arguments)]
fn answered(
    app: &AppHandle,
    main: &Webview,
    seen: &Seen,
    op: &str,
    input: Value,
    needles: &[&str],
    hidden: &[&str],
    cancel: bool,
) -> Result<Value, String> {
    std::thread::sleep(SPACED);
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
    if cancel || hidden.iter().any(|h| text.contains(h)) {
        press("alt+c")?;
        if !cancel {
            return Err(format!("the {op} confirmation shows a value it must not"));
        }
    } else {
        std::thread::sleep(ARMED);
        press("alt+o")?;
    }
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
    let r = answered(app, main, seen, op, input, needles, hidden, false)?;
    require(r["ok"] == true, format!("{op}: {r}"))?;
    Ok(r["value"].clone())
}

/// A stand-in's record: its arguments, its folder and its environment, once it is done; the
/// record is removed, so the next run of the same stand-in writes a new one.
fn stand_in(records: &Path, name: &str) -> Result<(Vec<String>, String, String), String> {
    wait_for(&format!("the {name} stand-in's record"), || {
        records.join(format!("{name}.done")).exists().then_some(())
    })?;
    let r = |x: &str| read(&records.join(format!("{name}.{x}"))).unwrap_or_default();
    let argv = r("argv").lines().map(String::from).collect();
    let out = (argv, r("cwd").trim_end().to_string(), r("env"));
    for x in ["argv", "cwd", "env", "done"] {
        let _ = std::fs::remove_file(records.join(format!("{name}.{x}")));
    }
    Ok(out)
}

fn empty(records: &Path) -> Result<bool, String> {
    Ok(std::fs::read_dir(records)
        .map_err(|e| e.to_string())?
        .next()
        .is_none())
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
    let (config, auth) = (p(&x["config"]), p(&x["auth"]));
    let (stow_link, stow_text, stow_target, stow_folder) = (
        p(&x["stow"]["link"]),
        s(&x["stow"]["text"]),
        p(&x["stow"]["target"]),
        p(&x["stow"]["folder"]),
    );
    let (root_link, root_text, root_file) = (
        p(&x["root"]["link"]),
        s(&x["root"]["text"]),
        p(&x["root"]["file"]),
    );
    // Files Grok Build would not take: each one's path, bytes key and what its refusal says.
    let refused: Vec<(PathBuf, String, String)> = x["refused"]
        .as_array()
        .into_iter()
        .flatten()
        .map(|r| (p(&r["file"]), s(&r["bytes"]), s(&r["says"])))
        .collect();
    let records = p(&x["records"]);
    let canon = |q: PathBuf| std::fs::canonicalize(q).map_err(|e| e.to_string());
    let work = canon(p(&x["work"]))?;
    let (terminal, grok, override_grok) = (
        canon(p(&x["terminal"]))?,
        canon(p(&x["grok"]))?,
        canon(p(&x["override"]))?,
    );
    let bytes = |k: &str| s(&x["bytes"][k]).replace("@KEY@", &key);
    let pair = |k: &str| (s(&x["bytes"][k][0]), s(&x["bytes"][k][1]));
    let txt = |q: &Path| q.display().to_string();
    let hidden = [key.as_str(), login.as_str()];
    let seen = Seen::default();
    let mut secret = String::new();
    let mut out = json!({"steps": []});
    let mut passed = |name: &str| out["steps"].as_array_mut().unwrap().push(json!(name));
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("{name}: {e}"));
    let file = |q: &Path| json!({"targets": [{"file": q}]});
    let pins = |v: &Value| json!({"targets": v["targets"]});
    let untouched = || -> Result<(), String> {
        require(
            read(&auth) == Some(bytes("auth")),
            "the login canary file changed",
        )
    };
    let refused_unchanged = || -> Result<(), String> {
        for (f, b, _) in &refused {
            require(
                read(f) == Some(bytes(b)),
                format!("the refused file {} changed", f.display()),
            )?;
        }
        Ok(())
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
        .into_iter()
        .flatten()
        .flatten()
        .find_map(|e| {
            let dir = e.path();
            let manifest: Value =
                serde_json::from_slice(&std::fs::read(dir.join("pluxy-extension.json")).ok()?)
                    .ok()?;
            (manifest["id"] == w).then(|| dir.join("bin").join("tool"))
        })
        .ok_or("pluxy/grok-setup is not in the store")?;

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

    // ---- unknown: files Grok Build would not take are refused, the person's is read
    step(
        "unknown",
        (|| {
            require(!refused.is_empty(), "expect.json lists no refused file")?;
            for (f, _, says) in &refused {
                read_refused(main, &seen, "preview", file(f), says)?;
            }
            refused_unchanged()?;
            // The positive control on the same route: the person's file, the default one.
            let v = read_op(main, &seen, "preview", json!({}))?;
            require(
                v["targets"][0]["selected"] == json!(txt(&config)) && v["applied"] == false,
                format!("the default preview: {v}"),
            )?;
            let st = &v["settings"];
            require(
                st == &x["read_original"],
                format!("the settings read back: {st}"),
            )?;
            require(
                read(&config) == Some(bytes("original")),
                "a preview changed the file",
            )
        })(),
    )?;
    passed("unknown");

    // The input of an apply: the window's values, the pins of a preview and the key's ID.
    let apply_input = |preview: &Value| {
        let mut i = x["settings_input"].clone();
        i["targets"] = preview["targets"].clone();
        i["key"] = json!(secret);
        i
    };

    // ---- apply: the file holds exactly the expected bytes
    step(
        "apply",
        (|| {
            let v = read_op(main, &seen, "preview", file(&config))?;
            let o = written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v),
                &[&txt(&config), LABEL, SAME_INSTANT],
                &hidden,
            )?;
            require(o["outcome"] == "applied", format!("the apply: {o}"))?;
            require(
                read(&config) == Some(bytes("applied")),
                format!("the applied config: {:?}", read(&config)),
            )?;
            require(mode(&config) == 0o600, "the config's mode changed")?;
            let v = read_op(main, &seen, "preview", file(&config))?;
            require(
                v["applied"] == true && v["settings"] == x["read_applied"],
                format!("the applied config read back: {v}"),
            )?;
            untouched()
        })(),
    )?;
    passed("apply");

    // ---- restore: the person's edit, then Restore Backup puts the original back
    step(
        "restore",
        (|| {
            let (from, to) = pair("edited_own");
            edit(&config, &from, &to)?;
            let v = read_op(main, &seen, "preview", file(&config))?;
            require(
                v["edited"] == json!([true]),
                format!("the edit is not seen: {v}"),
            )?;
            let o = written(
                app,
                main,
                &seen,
                "restore",
                pins(&v),
                &[&txt(&config)],
                &hidden,
            )?;
            require(o["outcome"] == "restored", format!("the restore: {o}"))?;
            require(
                read(&config) == Some(bytes("original")) && mode(&config) == 0o600,
                format!("the restored config: {:?}", read(&config)),
            )?;
            untouched()
        })(),
    )?;
    passed("restore");

    // ---- clear: applied again, edited twice, only the untouched values go
    step(
        "clear",
        (|| {
            let v = read_op(main, &seen, "preview", file(&config))?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v),
                &[&txt(&config), LABEL],
                &hidden,
            )?;
            require(read(&config) == Some(bytes("applied")), "the second apply")?;
            for k in ["edited_own", "edited_written"] {
                let (from, to) = pair(k);
                edit(&config, &from, &to)?;
            }
            let v = read_op(main, &seen, "preview", file(&config))?;
            let o = written(
                app,
                main,
                &seen,
                "clear",
                pins(&v),
                &[&txt(&config)],
                &hidden,
            )?;
            require(o["outcome"] == "cleared", format!("the clear: {o}"))?;
            require(
                read(&config) == Some(bytes("cleared")),
                format!("the cleared config: {:?}", read(&config)),
            )?;
            untouched()
        })(),
    )?;
    passed("clear");

    // ---- stow: only the link's target changes; a replaced target refuses writes
    step(
        "stow",
        (|| {
            let (inode, _) = link(&stow_link)?;
            let v = read_op(main, &seen, "preview", file(&stow_link))?;
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
                apply_input(&v),
                &[&txt(&stow_link), &txt(&stow_target)],
                &hidden,
            )?;
            require(
                read(&stow_target) == Some(bytes("stow_applied")) && mode(&stow_target) == 0o640,
                format!("the stow target after apply: {:?}", read(&stow_target)),
            )?;
            require(
                link(&stow_link)? == (inode, PathBuf::from(&stow_text)),
                "the stow link is not the same link with the same text",
            )?;
            require(
                read(&stow_folder.join("keep.toml")) == Some(bytes("keep")),
                "a file beside the stow target changed",
            )?;
            // The target replaced (as an editor saves, by rename) after a preview.
            let v = read_op(main, &seen, "preview", file(&stow_link))?;
            let replaced = read(&stow_target).unwrap_or_default();
            let tmp = stow_folder.join("config.toml.new");
            std::fs::write(&tmp, &replaced).map_err(|e| e.to_string())?;
            std::fs::rename(&tmp, &stow_target).map_err(|e| e.to_string())?;
            let before = snapshot(&home.join("dotfiles"))?;
            let r = answered(
                app,
                main,
                &seen,
                "restore",
                pins(&v),
                &[&txt(&stow_link)],
                &hidden,
                false,
            )?;
            require(
                r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains("changed")),
                format!("a write to a replaced target should fail as changed: {r}"),
            )?;
            require(
                snapshot(&home.join("dotfiles"))? == before,
                "the refused write changed a file",
            )?;
            // A new preview's restore puts the original back through the link.
            let v = read_op(main, &seen, "preview", file(&stow_link))?;
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
                read(&stow_target) == Some(bytes("stow")),
                format!("the stow target after restore: {:?}", read(&stow_target)),
            )?;
            require(
                link(&stow_link)? == (inode, PathBuf::from(&stow_text)),
                "the stow link changed",
            )
        })(),
    )?;
    passed("stow");

    // ---- root: a file inside the selected root is created, then cleared away
    step(
        "root",
        (|| {
            let (inode, _) = link(&root_link)?;
            let sel = json!({"targets": [{"root": root_link, "path": "config.toml"}]});
            let v = read_op(main, &seen, "preview", sel.clone())?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v),
                &[&txt(&root_link), &txt(&root_file)],
                &hidden,
            )?;
            require(
                read(&root_file) == Some(bytes("root_applied")) && mode(&root_file) == 0o600,
                format!("the root's file after apply: {:?}", read(&root_file)),
            )?;
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
            require(!root_file.exists(), "clear left the file it created")?;
            require(
                link(&root_link)? == (inode, PathBuf::from(&root_text)),
                "the root link changed",
            )
        })(),
    )?;
    passed("root");

    // ---- backups: a manual backup of the file, a names-only diff, restore, delete
    step(
        "backups",
        (|| {
            let list = || -> Result<Vec<Value>, String> {
                Ok(read_op(main, &seen, "backup-list", json!({}))?["backups"]
                    .as_array()
                    .cloned()
                    .unwrap_or_default())
            };
            require(list()?.is_empty(), "a manual backup exists already")?;
            let v = read_op(main, &seen, "preview", file(&config))?;
            let r = answered(
                app,
                main,
                &seen,
                "backup-create",
                pins(&v),
                &[&txt(&config)],
                &hidden,
                true,
            )?;
            require(
                r["ok"] == false,
                format!("a cancelled Manual Backup went ahead: {r}"),
            )?;
            require(list()?.is_empty(), "a cancelled Manual Backup kept one")?;
            let o = written(
                app,
                main,
                &seen,
                "backup-create",
                pins(&v),
                &[&txt(&config), "newest 10"],
                &hidden,
            )?;
            let id = s(&o["backup"]);
            let l = list()?;
            require(
                l.len() == 1
                    && l[0]["id"] == json!(id)
                    && l[0]["files"]
                        == json!([{"selected": txt(&config), "path": null, "target": txt(&config),
                            "exists": true, "size": bytes("cleared").len()}]),
                format!("the list after Manual Backup: {l:?}"),
            )?;
            let b = &x["backups"];
            for e in b["edits"].as_array().into_iter().flatten() {
                edit(&config, &s(&e[0]), &s(&e[1]))?;
            }
            let d = read_op(main, &seen, "backup-diff", json!({"backup": id}))?;
            require(
                d["same"] == false && d["files"][0]["changes"] == b["diff"],
                format!("the diff after the edits: {d}"),
            )?;
            for value in b["values"].as_array().into_iter().flatten() {
                require(!d.to_string().contains(&s(value)), "the diff shows a value")?;
            }
            let v = read_op(main, &seen, "preview", file(&config))?;
            let mut input = pins(&v);
            input["backup"] = json!(id);
            written(
                app,
                main,
                &seen,
                "backup-restore",
                input,
                &[&txt(&config), "No automatic backup is created"],
                &hidden,
            )?;
            require(
                read(&config) == Some(bytes("cleared")),
                "the restored backup's config",
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
            require(list()?.is_empty(), "the deleted backup is still listed")
        })(),
    )?;
    passed("backups");

    // ---- launch: the recipe through the launcher; cancel starts nothing
    step(
        "launch",
        (|| {
            let recipe = read_op(main, &seen, "recipe", json!({}))?;
            require(
                recipe
                    == json!({"client": CLIENT, "kind": "terminal", "program": "grok", "args": []}),
                format!("the recipe: {recipe}"),
            )?;
            let launch = |extra: Value, needles: &[&str], cancel: bool| -> Result<Value, String> {
                let mut l = recipe.clone();
                l["cwd"] = json!(work);
                l["terminal"] = json!("automatic");
                for (k, v) in extra.as_object().into_iter().flatten() {
                    l[k] = v.clone();
                }
                std::thread::sleep(SPACED);
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
            let needles = [
                EXECUTION,
                "launch",
                &txt(&terminal),
                &txt(&grok),
                &txt(&work),
            ];
            // Cancelled: nothing starts.
            let r = launch(json!({}), &needles, true)?;
            require(
                r["ok"] == false,
                format!("a cancelled launch went ahead: {r}"),
            )?;
            std::thread::sleep(Duration::from_secs(1));
            require(empty(&records)?, "a cancelled launch started something")?;
            // Confirmed: the terminal runs grok in the folder, with no key.
            let r = launch(json!({}), &needles, false)?;
            require(r["ok"] == true, format!("the confirmed launch: {r}"))?;
            let (argv, cwd, _) = stand_in(&records, "terminal")?;
            require(
                argv == vec!["-e".to_string(), txt(&grok)] && cwd == txt(&work),
                format!("the terminal ran with {argv:?} in {cwd}"),
            )?;
            let (argv, cwd, env) = stand_in(&records, "grok")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("grok ran with {argv:?} in {cwd}"),
            )?;
            require(
                !env.contains(&key) && !env.contains(&login),
                "grok's environment holds the key or the login canary",
            )?;
            // The executable override runs instead of grok.
            let r = launch(
                json!({"override": override_grok}),
                &[EXECUTION, "launch", &txt(&override_grok)],
                false,
            )?;
            require(r["ok"] == true, format!("the override launch: {r}"))?;
            let _ = stand_in(&records, "terminal")?;
            let (argv, cwd, _) = stand_in(&records, "override")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("the override ran with {argv:?} in {cwd}"),
            )?;
            // Grok Build's installed copy, where its install script puts it, comes first.
            let installed = p(&x["installed"]["to"]);
            std::fs::create_dir_all(installed.parent().unwrap()).map_err(|e| e.to_string())?;
            std::fs::copy(p(&x["installed"]["from"]), &installed).map_err(|e| e.to_string())?;
            let recipe = read_op(main, &seen, "recipe", json!({}))?;
            require(
                recipe["program"] == json!(txt(&installed)),
                format!("the recipe with an installed copy: {recipe}"),
            )?;
            let mut l = recipe.clone();
            l["cwd"] = json!(work);
            l["terminal"] = json!("automatic");
            std::thread::sleep(SPACED);
            let slot = start(main, "launcher_launch", json!({ "launch": l }))?;
            let text = dialog_with(app, &[EXECUTION, "launch", &txt(&installed)])?;
            seen.add(text);
            std::thread::sleep(ARMED);
            press("alt+o")?;
            let r = settle(main, &slot)?;
            seen.add(r.to_string());
            require(r["ok"] == true, format!("the installed copy's launch: {r}"))?;
            let (argv, _, _) = stand_in(&records, "terminal")?;
            require(
                argv == vec!["-e".to_string(), txt(&installed)],
                format!("the terminal ran with {argv:?}"),
            )?;
            let (argv, cwd, env) = stand_in(&records, "installed")?;
            std::fs::remove_dir_all(installed.parent().unwrap()).map_err(|e| e.to_string())?;
            require(
                argv.is_empty() && cwd == txt(&work) && !env.contains(&key),
                format!("the installed copy ran with {argv:?} in {cwd}"),
            )
        })(),
    )?;
    passed("launch");

    // ---- uninstall: no tool process is left, the file stays as it is
    step(
        "uninstall",
        (|| {
            let before = std::fs::read(&config).map_err(|e| e.to_string())?;
            seen.add(ok(main, "device_enable", json!({"id": w, "enabled": false}))?.to_string());
            seen.add(ok(main, "device_remove", json!({"id": w}))?.to_string());
            wait_for("no pluxy/grok-setup process once removed", || {
                processes(&tool_program).is_empty().then_some(())
            })?;
            require(
                std::fs::read(&config).map_err(|e| e.to_string())? == before,
                "removing the package changed the config",
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
            let mut left = leftovers(&home.join(".grok"))?;
            left.extend(leftovers(&home.join("dotfiles"))?);
            left.extend(leftovers(&home.join(".local/share/chezmoi"))?);
            require(left.is_empty(), format!("temporary files left: {left:?}"))?;
            untouched()?;
            refused_unchanged()
        })(),
    )?;
    passed("audit");

    out["passed"] = json!(true);
    log("grok", out);
    Ok(())
}
