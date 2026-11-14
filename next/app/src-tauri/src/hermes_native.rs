//! The native test's check of the Hermes Agent setup and launcher, pluxy/hermes-setup, through the
//! shared acceptance flow of an integration (native-test builds with `artifact-test-trust` only).
//! It drives the real app through the window's own commands, the real native key entry and
//! confirmations, with the packages and the files `native/hermes.ts` made in this run's HOME (Hermes
//! Agent's folder as a person keeps it, with `config.yaml`, its `.env` and `auth.json` holding a
//! login canary, a stow link, a chezmoi-style root, and files Hermes Agent would not take) and its
//! stand-in terminal, `hermes` and installed copy. Each refusal has a positive control on the same
//! route:
//!
//! - install: pluxy/hermes-setup and pluxy/execution install from the curated registry and are
//!   enabled through their native confirmations, which say "full access to your user account";
//! - key: the key is typed into native key entry; the window gets only its ID;
//! - unknown: a preview of a config.yaml whose `providers` is a list, and of one with a duplicate
//!   key that is not YAML, are refused unasked and change nothing; a preview of the person's file
//!   (a read, asked nothing) picks `config.yaml`, pins it and reads its values back, with no key;
//! - apply: asks a native confirmation naming the file, the key's label and the same-instant
//!   sentence, never the key; then the file holds exactly the bytes `expect.json` says (comments,
//!   the person's providers and settings in place), mode kept, and the preview reads the config
//!   back as configured; `.env` and `auth.json` are untouched;
//! - restore: after the person's edit, Restore Backup puts the original back;
//! - clear: applied, edited by the person, Clear takes out only pluxy's values and keeps their
//!   edits; an apply again keeps the edits, and Clear takes pluxy's values out again;
//! - backups: a Manual Backup, a diff naming the setting the person changed and never its value,
//!   restore over that edit, delete;
//! - stow: an apply through the selected link changes only its target, the link keeps its inode
//!   and text; once the target is replaced, a confirmed write fails as changed and writes nothing;
//!   a new preview's restore puts the original back through the link;
//! - root: an apply to the selected root creates `config.yaml`, the root link kept; clear removes it;
//! - launch: the CLI recipe is the terminal running `hermes`, and once `~/.local/bin/hermes` holds
//!   a program, that one. Through the launcher, a cancelled confirmation starts nothing; confirmed
//!   ones run the stand-in terminal with exactly `-e <hermes>` in the chosen folder, then `hermes`
//!   with no arguments, then the installed one; none has the key or the login canary in its
//!   environment;
//! - uninstall: disabled and removed, no pluxy/hermes-setup process is left and the files are as
//!   the last step left them;
//! - audit: the key and the login canary are in no window answer and no confirmation, no temporary
//!   file is left, and files no step names keep their bytes.
//!
//! One line on stderr: `native-test: hermes <JSON>`.

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

const PACKAGE: &str = "pluxy/hermes-setup";
const EXECUTION: &str = "pluxy/execution";
const LABEL: &str = "fixture Hermes Agent key";
const SAME_INSTANT: &str = "an edit made in the same instant by another program may be lost";
/// A confirmation keeps its Confirm button disabled for a second after it shows.
const ARMED: Duration = Duration::from_millis(1300);

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

/// Empties records, so the next launch's stand-ins write fresh ones.
fn clear_records(records: &Path) -> Result<(), String> {
    for e in std::fs::read_dir(records).map_err(|e| e.to_string())? {
        std::fs::remove_file(e.map_err(|e| e.to_string())?.path()).map_err(|e| e.to_string())?;
    }
    Ok(())
}

/// Copies the stand-in to a place, its folders made first (std::fs::copy keeps the exec bit).
fn place(from: &Path, to: &Path) -> Result<(), String> {
    std::fs::create_dir_all(to.parent().ok_or("no folder")?).map_err(|e| e.to_string())?;
    std::fs::copy(from, to)
        .map(|_| ())
        .map_err(|e| e.to_string())
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
    let (config, env, auth) = (p(&x["config"]), p(&x["env"]), p(&x["auth"]));
    let stow_config = p(&x["stow"]["config"]);
    let stow_config_text = s(&x["stow"]["config_text"]);
    let (stow_target_config, target_folder) = (
        p(&x["stow"]["target_config"]),
        p(&x["stow"]["target_folder"]),
    );
    let (root_link, root_text, root_config) = (
        p(&x["root"]["link"]),
        s(&x["root"]["text"]),
        p(&x["root"]["config"]),
    );
    let (unknown, broken) = (p(&x["unknown"]), p(&x["broken"]));
    let records = p(&x["records"]);
    let canon = |v: &Value| std::fs::canonicalize(p(v)).map_err(|e| e.to_string());
    let work = canon(&x["work"])?;
    let (terminal, hermes, installed) = (
        canon(&x["terminal"])?,
        canon(&x["hermes"])?,
        canon(&x["installed"])?,
    );
    let local_bin = p(&x["local_bin"]);
    let fill = |v: &Value| s(v).replace("@KEY@", &key);
    let bytes = |k: &str| fill(&x["bytes"][k]);
    let pair = |k: &str| (fill(&x["edits"][k][0]), fill(&x["edits"][k][1]));
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
    // The one file, pinned.
    let pin = |f: &Path| json!({"targets": [{"file": f}]});
    let untouched = || -> Result<(), String> {
        require(
            read(&env) == Some(bytes("env")) && read(&auth) == Some(bytes("auth")),
            "the .env or auth.json changed",
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
    let tool_program = || -> Option<PathBuf> {
        std::fs::read_dir(&store).ok()?.find_map(|e| {
            let dir = e.ok()?.path();
            let manifest: Value =
                serde_json::from_slice(&std::fs::read(dir.join("pluxy-extension.json")).ok()?)
                    .ok()?;
            (manifest["id"] == w).then(|| dir.join("bin").join("tool"))
        })
    };
    let tool_program = tool_program().ok_or("pluxy/hermes-setup is not in the store")?;

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

    // ---- unknown: files Hermes Agent would not take are refused, the person's is read
    step(
        "unknown",
        (|| {
            read_refused(
                main,
                &seen,
                "preview",
                pin(&unknown.join("config.yaml")),
                "providers",
            )?;
            read_refused(
                main,
                &seen,
                "preview",
                pin(&broken.join("config.yaml")),
                "YAML",
            )?;
            require(
                read(&unknown.join("config.yaml")) == Some(bytes("unknown"))
                    && read(&broken.join("config.yaml")) == Some(bytes("broken")),
                "a refused file changed",
            )?;
            // The positive control on the same route: the person's file, the default one.
            let v = read_op(main, &seen, "preview", json!({}))?;
            require(
                v["targets"][0]["selected"] == json!(txt(&config)) && v["applied"] == false,
                format!("the default preview: {v}"),
            )?;
            let c = &v["config"];
            require(
                c["state"] == "not-configured"
                    && c["key_set"] == false
                    && c["model"].is_null()
                    && c["legacy"] == true,
                format!("the config read back: {c}"),
            )?;
            require(
                read(&config) == Some(bytes("original")),
                "a preview changed the file",
            )?;
            untouched()
        })(),
    )?;
    passed("unknown");

    // ---- apply: the file holds exactly the expected bytes
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
                &[&txt(&config), LABEL, SAME_INSTANT],
                &hidden,
            )?;
            require(o["outcome"] == "applied", format!("the apply: {o}"))?;
            require(
                read(&config) == Some(bytes("applied")),
                format!("the applied config: {:?}", read(&config)),
            )?;
            require(mode(&config) == 0o600, "the file's mode changed")?;
            let v = read_op(
                main,
                &seen,
                "preview",
                json!({"base_url": x["settings_input"]["base_url"]}),
            )?;
            let c = &v["config"];
            require(
                v["applied"] == true
                    && c["state"] == "configured"
                    && c["key_set"] == true
                    && c["base_url"] == "http://127.0.0.1:41999/v1"
                    && c["provider"] == "custom:pluxy"
                    && c["model"] == "gpt-6.1-sol"
                    && c["legacy"] == true,
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
            let (from, to) = pair("backend");
            edit(&config, &from, &to)?;
            let v = read_op(main, &seen, "preview", json!({}))?;
            require(
                v["edited"] == json!([true]),
                format!("the edit is not seen: {v}"),
            )?;
            let o = written(
                app,
                main,
                &seen,
                "restore",
                json!({"targets": v["targets"]}),
                &[&txt(&config)],
                &hidden,
            )?;
            require(o["outcome"] == "restored", format!("the restore: {o}"))?;
            require(
                read(&config) == Some(bytes("original")) && mode(&config) == 0o600,
                format!("the restored file: {:?}", read(&config)),
            )?;
            untouched()
        })(),
    )?;
    passed("restore");

    // ---- clear: applied, edited, cleared; only pluxy's values go; an apply keeps the edits
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
                &[&txt(&config), LABEL],
                &hidden,
            )?;
            require(read(&config) == Some(bytes("applied")), "the second apply")?;
            for k in ["backend", "models"] {
                let (from, to) = pair(k);
                edit(&config, &from, &to)?;
            }
            let v = read_op(main, &seen, "preview", json!({}))?;
            let o = written(
                app,
                main,
                &seen,
                "clear",
                json!({"targets": v["targets"]}),
                &[&txt(&config)],
                &hidden,
            )?;
            require(o["outcome"] == "cleared", format!("the clear: {o}"))?;
            require(
                read(&config) == Some(bytes("cleared")),
                format!("the cleared file: {:?}", read(&config)),
            )?;
            // An apply again keeps the person's edits; the clear after it takes pluxy's out.
            let v = read_op(main, &seen, "preview", json!({}))?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
                &[&txt(&config), LABEL],
                &hidden,
            )?;
            require(
                read(&config) == Some(bytes("applied_with_edits")),
                format!("the apply over the person's edits: {:?}", read(&config)),
            )?;
            let v = read_op(main, &seen, "preview", json!({}))?;
            written(
                app,
                main,
                &seen,
                "clear",
                json!({"targets": v["targets"]}),
                &[&txt(&config)],
                &hidden,
            )?;
            require(
                read(&config) == Some(bytes("cleared")),
                format!("the cleared file again: {:?}", read(&config)),
            )?;
            untouched()
        })(),
    )?;
    passed("clear");

    // ---- backups: a manual backup, diff by name, restore, delete
    step(
        "backups",
        (|| {
            let v = read_op(main, &seen, "preview", json!({}))?;
            let b = written(
                app,
                main,
                &seen,
                "backup-create",
                json!({"targets": v["targets"]}),
                &[&txt(&config)],
                &hidden,
            )?;
            let id = s(&b["backup"]);
            let list = read_op(main, &seen, "backup-list", json!({}))?;
            require(
                list["backups"].as_array().map(Vec::len) == Some(1)
                    && list["backups"][0]["id"] == json!(id),
                format!("the backup list: {list}"),
            )?;
            let (from, to) = pair("backup");
            edit(&config, &from, &to)?;
            let d = read_op(main, &seen, "backup-diff", json!({"backup": id}))?;
            require(
                d["files"][0]["changes"] == x["backups"]["diff"],
                format!("the backup diff: {d}"),
            )?;
            for value in x["backups"]["values"].as_array().into_iter().flatten() {
                require(
                    !d.to_string().contains(&s(value)) && !list.to_string().contains(&s(value)),
                    "a backup answer holds a file's value",
                )?;
            }
            let v = read_op(main, &seen, "preview", json!({}))?;
            written(
                app,
                main,
                &seen,
                "backup-restore",
                json!({"backup": id, "targets": v["targets"]}),
                &[&txt(&config)],
                &hidden,
            )?;
            require(
                read(&config) == Some(bytes("cleared")),
                format!("the restored backup: {:?}", read(&config)),
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
            )?;
            untouched()
        })(),
    )?;
    passed("backups");

    // ---- stow: only the link's target changes; a replaced target refuses writes
    step(
        "stow",
        (|| {
            let (ino_config, _) = link(&stow_config)?;
            let v = read_op(main, &seen, "preview", pin(&stow_config))?;
            require(
                v["targets"][0]["target"] == json!(txt(&stow_target_config))
                    && v["targets"][0]["link"] == json!(stow_config_text),
                format!("the stow preview: {v}"),
            )?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
                &[&txt(&stow_config), &txt(&stow_target_config)],
                &hidden,
            )?;
            require(
                read(&stow_target_config) == Some(bytes("stow_applied"))
                    && mode(&stow_target_config) == 0o640,
                format!(
                    "the stow target after apply: {:?}",
                    read(&stow_target_config)
                ),
            )?;
            require(
                link(&stow_config)? == (ino_config, PathBuf::from(&stow_config_text)),
                "a stow link is not the same link with the same text",
            )?;
            require(
                read(&target_folder.join("keep.yml")) == Some(bytes("keep")),
                "a file beside the stow target changed",
            )?;
            // The target replaced (as an editor saves, by rename) after a preview.
            let v = read_op(main, &seen, "preview", pin(&stow_config))?;
            let replaced = read(&stow_target_config).unwrap_or_default();
            let tmp = target_folder.join("config.yaml.new");
            std::fs::write(&tmp, &replaced).map_err(|e| e.to_string())?;
            std::fs::rename(&tmp, &stow_target_config).map_err(|e| e.to_string())?;
            let before = snapshot(&home.join("dotfiles"))?;
            let r = confirmed(
                app,
                main,
                &seen,
                "restore",
                json!({"targets": v["targets"]}),
                &[&txt(&stow_config)],
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
            // A new preview's restore puts the original back through the link.
            let v = read_op(main, &seen, "preview", pin(&stow_config))?;
            written(
                app,
                main,
                &seen,
                "restore",
                json!({"targets": v["targets"]}),
                &[&txt(&stow_config)],
                &hidden,
            )?;
            require(
                read(&stow_target_config) == Some(bytes("stow")),
                format!(
                    "the stow target after restore: {:?}",
                    read(&stow_target_config)
                ),
            )?;
            require(
                link(&stow_config)? == (ino_config, PathBuf::from(&stow_config_text)),
                "a stow link changed",
            )
        })(),
    )?;
    passed("stow");

    // ---- root: the file inside the selected root is created, then cleared away
    step(
        "root",
        (|| {
            let (inode, _) = link(&root_link)?;
            let sel = json!({"folder": root_link});
            let v = read_op(main, &seen, "preview", sel.clone())?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
                &[&txt(&root_link), &txt(&root_config)],
                &hidden,
            )?;
            require(
                read(&root_config) == Some(bytes("root")) && mode(&root_config) == 0o600,
                format!("the root's file after apply: {:?}", read(&root_config)),
            )?;
            let v = read_op(main, &seen, "preview", sel)?;
            written(
                app,
                main,
                &seen,
                "clear",
                json!({"targets": v["targets"]}),
                &[&txt(&root_link)],
                &hidden,
            )?;
            require(!root_config.exists(), "clear left the file it created")?;
            require(
                link(&root_link)? == (inode, PathBuf::from(&root_text)),
                "the root link changed",
            )
        })(),
    )?;
    passed("root");

    // ---- launch: the recipes through the launcher; cancel starts nothing
    step(
        "launch",
        (|| {
            let launch = |recipe: &Value, extra: Value, needles: &[&str], cancel: bool| {
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
                Ok::<Value, String>(r)
            };
            let clean = |env: &str, who: &str| {
                require(
                    !env.contains(&key) && !env.contains(&login),
                    format!("{who}'s environment holds the key or the login canary"),
                )
            };
            let in_work = json!({"cwd": work, "terminal": "automatic"});
            // The CLI: the terminal running hermes, found on the PATH.
            let recipe = read_op(main, &seen, "recipe", json!({}))?;
            require(
                recipe
                    == json!({"client": "Hermes Agent", "kind": "terminal", "program": "hermes",
                        "args": []}),
                format!("the recipe: {recipe}"),
            )?;
            let needles = [
                EXECUTION,
                "launch",
                &txt(&terminal),
                &txt(&hermes),
                &txt(&work),
            ];
            // Cancelled: nothing starts.
            let r = launch(&recipe, in_work.clone(), &needles, true)?;
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
            // Confirmed: the terminal runs hermes in the folder, with no key.
            let r = launch(&recipe, in_work.clone(), &needles, false)?;
            require(r["ok"] == true, format!("the confirmed launch: {r}"))?;
            let (argv, cwd, _) = stand_in(&records, "terminal")?;
            require(
                argv == vec!["-e".to_string(), txt(&hermes)] && cwd == txt(&work),
                format!("the terminal ran with {argv:?} in {cwd}"),
            )?;
            let (argv, cwd, env) = stand_in(&records, "hermes")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("hermes ran with {argv:?} in {cwd}"),
            )?;
            clean(&env, "hermes")?;
            // The installer's place holds a program: the recipe names it instead.
            clear_records(&records)?;
            place(&installed, &local_bin)?;
            let recipe = read_op(main, &seen, "recipe", json!({}))?;
            require(
                recipe["program"] == json!(txt(&local_bin)),
                format!("the recipe with hermes in ~/.local/bin: {recipe}"),
            )?;
            let r = launch(
                &recipe,
                in_work,
                &[EXECUTION, "launch", &txt(&local_bin)],
                false,
            )?;
            require(
                r["ok"] == true,
                format!("the installed hermes' launch: {r}"),
            )?;
            let (argv, cwd, env) = stand_in(&records, "installed")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("the installed hermes ran with {argv:?} in {cwd}"),
            )?;
            clean(&env, "the installed hermes")
        })(),
    )?;
    passed("launch");

    // ---- uninstall: no tool process is left, the files stay as they are
    step(
        "uninstall",
        (|| {
            let before = (read(&config), read(&env), read(&auth));
            seen.add(ok(main, "device_enable", json!({"id": w, "enabled": false}))?.to_string());
            seen.add(ok(main, "device_remove", json!({"id": w}))?.to_string());
            wait_for("no pluxy/hermes-setup process once removed", || {
                processes(&tool_program).is_empty().then_some(())
            })?;
            require(
                (read(&config), read(&env), read(&auth)) == before,
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
            let mut left = leftovers(&home.join(".hermes"))?;
            left.extend(leftovers(&home.join("dotfiles"))?);
            left.extend(leftovers(&home.join(".local/share/chezmoi"))?);
            require(left.is_empty(), format!("temporary files left: {left:?}"))?;
            untouched()?;
            require(
                read(&unknown.join("config.yaml")) == Some(bytes("unknown"))
                    && read(&broken.join("config.yaml")) == Some(bytes("broken")),
                "a refused file changed",
            )
        })(),
    )?;
    passed("audit");

    out["passed"] = json!(true);
    log("hermes", out);
    Ok(())
}
