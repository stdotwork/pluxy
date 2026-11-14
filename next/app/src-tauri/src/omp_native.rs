//! The native test's check of the Oh My Pi setup and launcher, pluxy/omp-setup, through the shared
//! acceptance flow of an integration (native-test builds with `artifact-test-trust` only). It
//! drives the real app through the window's own commands, the real native key entry and
//! confirmations, with the packages and the files `native/omp.ts` made in this run's HOME (Oh My
//! Pi's agent folder as a person keeps it, with `models.yml`, `config.yml` and its credential
//! store, a stow link, a chezmoi-style root, and files Oh My Pi would not take) and its stand-in
//! terminal, `omp` and installed copies. Each refusal has a positive control on the same route:
//!
//! - install: pluxy/omp-setup and pluxy/execution install from the curated registry and are enabled
//!   through their native confirmations, which say "full access to your user account";
//! - key: the key is typed into native key entry; the window gets only its ID;
//! - unknown: a preview of a models file whose `providers` is a list, one that is not YAML, and of
//!   an older `models.json`, are refused unasked and change nothing; a preview of the person's files
//!   (a read, asked nothing) picks `models.yml` and `config.yml`, pins both and reads the values
//!   back, with no key;
//! - apply: asks a native confirmation naming both files, the key's label and the same-instant
//!   sentence, never the key; then both files hold exactly the bytes `expect.json` says (comments,
//!   the person's providers and settings in place), modes kept, and the preview reads the config
//!   back as configured; `agent.db` is untouched;
//! - restore: after the person's edit, Restore Backup puts both original files back;
//! - clear: applied again and edited by the person, Clear takes out only pluxy's values; their own
//!   model entry, theme and smol role stay, and an apply again keeps them too;
//! - backups: a Manual Backup, a diff naming the setting the person changed and never its value,
//!   restore over that edit, delete;
//! - stow: an apply through the selected links changes only their targets, the links keep their
//!   inodes and text; once a target is replaced, a confirmed write fails as changed and writes
//!   nothing; a new preview's restore puts the originals back through the links;
//! - root: an apply to the selected root creates both files, the root link kept; clear removes them;
//! - launch: the CLI recipe is the terminal running `omp`, and once `~/.local/bin/omp` and then
//!   `~/.bun/bin/omp` (bun's place, which comes first) hold a program, that one. Through the
//!   launcher, a cancelled confirmation starts nothing; confirmed ones run the stand-in terminal
//!   with exactly `-e <omp>` in the chosen folder, then `omp` with no arguments, then the installed
//!   one; none has the key or the login canary in its environment;
//! - uninstall: disabled and removed, no pluxy/omp-setup process is left and both files are as the
//!   last step left them;
//! - audit: the key and the login canary are in no window answer and no confirmation, no temporary
//!   file is left, and files no step names keep their bytes.
//!
//! One line on stderr: `native-test: omp <JSON>`.

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

const PACKAGE: &str = "pluxy/omp-setup";
const EXECUTION: &str = "pluxy/execution";
const LABEL: &str = "fixture Oh My Pi key";
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
    let (models, config, db) = (p(&x["models"]), p(&x["config"]), p(&x["db"]));
    let (stow_models, stow_config) = (p(&x["stow"]["models"]), p(&x["stow"]["config"]));
    let (stow_models_text, stow_config_text) =
        (s(&x["stow"]["models_text"]), s(&x["stow"]["config_text"]));
    let (stow_target_models, stow_target_config, target_folder) = (
        p(&x["stow"]["target_models"]),
        p(&x["stow"]["target_config"]),
        p(&x["stow"]["target_folder"]),
    );
    let (root_link, root_text, root_models, root_config) = (
        p(&x["root"]["link"]),
        s(&x["root"]["text"]),
        p(&x["root"]["models"]),
        p(&x["root"]["config"]),
    );
    let (unknown, broken, legacy) = (p(&x["unknown"]), p(&x["broken"]), p(&x["legacy"]));
    let records = p(&x["records"]);
    let canon = |v: &Value| std::fs::canonicalize(p(v)).map_err(|e| e.to_string());
    let work = canon(&x["work"])?;
    let (terminal, omp, installed) = (
        canon(&x["terminal"])?,
        canon(&x["omp"])?,
        canon(&x["installed"])?,
    );
    let (local_bin, bun_bin) = (p(&x["local_bin"]), p(&x["bun_bin"]));
    let fill = |v: &Value| s(v).replace("@KEY@", &key);
    let bytes = |k: &str| fill(&x["bytes"][k]);
    let pair = |k: &str| (fill(&x["bytes"][k][0]), fill(&x["bytes"][k][1]));
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
    // The two files, pinned: models first, then config.
    let pins = |m: &Path, c: &Path| json!({"targets": [{"file": m}, {"file": c}]});
    let untouched =
        || -> Result<(), String> { require(read(&db) == Some(bytes("db")), "agent.db changed") };

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
    let tool_program = tool_program().ok_or("pluxy/omp-setup is not in the store")?;

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

    // ---- unknown: files Oh My Pi would not take are refused, the person's are read
    step(
        "unknown",
        (|| {
            read_refused(
                main,
                &seen,
                "preview",
                pins(&unknown.join("models.yml"), &unknown.join("config.yml")),
                "providers",
            )?;
            read_refused(
                main,
                &seen,
                "preview",
                pins(&broken.join("models.yml"), &broken.join("config.yml")),
                "YAML",
            )?;
            read_refused(
                main,
                &seen,
                "preview",
                pins(&legacy.join("models.yml"), &legacy.join("config.yml")),
                "models.json",
            )?;
            require(
                read(&unknown.join("models.yml")) == Some(bytes("unknown"))
                    && read(&broken.join("models.yml")) == Some(bytes("broken"))
                    && read(&legacy.join("models.json")) == Some(bytes("legacy"))
                    && !legacy.join("models.yml").exists(),
                "a refused file changed, or legacy/models.yml exists",
            )?;
            // The positive control on the same route: the person's files, the default pair.
            let v = read_op(main, &seen, "preview", json!({}))?;
            require(
                v["targets"][0]["selected"] == json!(txt(&models))
                    && v["targets"][1]["selected"] == json!(txt(&config))
                    && v["applied"] == false,
                format!("the default preview: {v}"),
            )?;
            let c = &v["config"];
            require(
                c["state"] == "not-configured"
                    && c["key_set"] == false
                    && c["model"].is_null()
                    && c["default_role"] == "anthropic/claude-sonnet-5-5:high"
                    && c["legacy"] == true,
                format!("the config read back: {c}"),
            )?;
            require(
                read(&models) == Some(bytes("original"))
                    && read(&config) == Some(bytes("config_original")),
                "a preview changed the files",
            )?;
            untouched()
        })(),
    )?;
    passed("unknown");

    // ---- apply: both files hold exactly the expected bytes
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
                &[&txt(&models), &txt(&config), LABEL, SAME_INSTANT],
                &hidden,
            )?;
            require(o["outcome"] == "applied", format!("the apply: {o}"))?;
            require(
                read(&models) == Some(bytes("applied")),
                format!("the applied models: {:?}", read(&models)),
            )?;
            require(
                read(&config) == Some(bytes("config_applied")),
                format!("the applied config: {:?}", read(&config)),
            )?;
            require(
                mode(&models) == 0o644 && mode(&config) == 0o644,
                "a file's mode changed",
            )?;
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
                    && c["model"] == "gpt-6.1-sol"
                    && c["default_role"] == "pluxy/gpt-6.1-sol",
                format!("the applied config read back: {v}"),
            )?;
            untouched()
        })(),
    )?;
    passed("apply");

    // ---- restore: the person's edit, then Restore Backup puts both originals back
    step(
        "restore",
        (|| {
            let (from, to) = pair("edit_theme");
            edit(&config, &from, &to)?;
            let v = read_op(main, &seen, "preview", json!({}))?;
            require(
                v["edited"] == json!([false, true]),
                format!("the edit is not seen: {v}"),
            )?;
            let o = written(
                app,
                main,
                &seen,
                "restore",
                json!({"targets": v["targets"]}),
                &[&txt(&models), &txt(&config)],
                &hidden,
            )?;
            require(o["outcome"] == "restored", format!("the restore: {o}"))?;
            require(
                read(&models) == Some(bytes("original"))
                    && read(&config) == Some(bytes("config_original"))
                    && mode(&models) == 0o644
                    && mode(&config) == 0o644,
                format!(
                    "the restored files: {:?} {:?}",
                    read(&models),
                    read(&config)
                ),
            )?;
            untouched()
        })(),
    )?;
    passed("restore");

    // ---- clear: applied, edited, applied again, cleared; only pluxy's values go
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
                &[&txt(&models), &txt(&config), LABEL],
                &hidden,
            )?;
            require(
                read(&models) == Some(bytes("applied"))
                    && read(&config) == Some(bytes("config_applied")),
                "the second apply",
            )?;
            let (from, to) = pair("edit_entry");
            edit(&models, &from, &to)?;
            for k in ["edit_theme", "edit_smol"] {
                let (from, to) = pair(k);
                edit(&config, &from, &to)?;
            }
            let v = read_op(main, &seen, "preview", json!({}))?;
            let mut again = apply_input(&v, &secret);
            again["model"] = x["reapply_model"].clone();
            written(
                app,
                main,
                &seen,
                "apply",
                again,
                &[&txt(&models), &txt(&config), LABEL],
                &hidden,
            )?;
            require(
                read(&models) == Some(bytes("applied_with_entry"))
                    && read(&config) == Some(bytes("config_reapplied")),
                format!(
                    "the apply over the person's edits: {:?} {:?}",
                    read(&models),
                    read(&config)
                ),
            )?;
            let v = read_op(main, &seen, "preview", json!({}))?;
            let o = written(
                app,
                main,
                &seen,
                "clear",
                json!({"targets": v["targets"]}),
                &[&txt(&models), &txt(&config)],
                &hidden,
            )?;
            require(o["outcome"] == "cleared", format!("the clear: {o}"))?;
            require(
                read(&models) == Some(bytes("cleared"))
                    && read(&config) == Some(bytes("config_cleared")),
                format!("the cleared files: {:?} {:?}", read(&models), read(&config)),
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
                &[&txt(&models), &txt(&config)],
                &hidden,
            )?;
            let id = s(&b["backup"]);
            let list = read_op(main, &seen, "backup-list", json!({}))?;
            require(
                list["backups"].as_array().map(Vec::len) == Some(1)
                    && list["backups"][0]["id"] == json!(id),
                format!("the backup list: {list}"),
            )?;
            let (from, to) = (s(&x["backups"]["edit"][0]), s(&x["backups"]["edit"][1]));
            edit(&config, &from, &to)?;
            let d = read_op(main, &seen, "backup-diff", json!({"backup": id}))?;
            require(
                d["files"][1]["changes"] == x["backups"]["diff"]
                    && d["files"][0]["changes"] == json!([]),
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
                &[&txt(&models), &txt(&config)],
                &hidden,
            )?;
            require(
                read(&models) == Some(bytes("cleared"))
                    && read(&config) == Some(bytes("config_cleared")),
                format!(
                    "the restored backup: {:?} {:?}",
                    read(&models),
                    read(&config)
                ),
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

    // ---- stow: only the links' targets change; a replaced target refuses writes
    step(
        "stow",
        (|| {
            let (ino_models, _) = link(&stow_models)?;
            let (ino_config, _) = link(&stow_config)?;
            let v = read_op(main, &seen, "preview", pins(&stow_models, &stow_config))?;
            require(
                v["targets"][0]["target"] == json!(txt(&stow_target_models))
                    && v["targets"][0]["link"] == json!(stow_models_text)
                    && v["targets"][1]["target"] == json!(txt(&stow_target_config))
                    && v["targets"][1]["link"] == json!(stow_config_text),
                format!("the stow preview: {v}"),
            )?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
                &[&txt(&stow_models), &txt(&stow_target_models)],
                &hidden,
            )?;
            require(
                read(&stow_target_models) == Some(bytes("stow_models_applied"))
                    && mode(&stow_target_models) == 0o640
                    && read(&stow_target_config) == Some(bytes("stow_config_applied")),
                format!(
                    "the stow targets after apply: {:?} {:?}",
                    read(&stow_target_models),
                    read(&stow_target_config)
                ),
            )?;
            require(
                link(&stow_models)? == (ino_models, PathBuf::from(&stow_models_text))
                    && link(&stow_config)? == (ino_config, PathBuf::from(&stow_config_text)),
                "a stow link is not the same link with the same text",
            )?;
            require(
                read(&target_folder.join("keep.yml")) == Some(bytes("keep")),
                "a file beside the stow targets changed",
            )?;
            // The target replaced (as an editor saves, by rename) after a preview.
            let v = read_op(main, &seen, "preview", pins(&stow_models, &stow_config))?;
            let replaced = read(&stow_target_models).unwrap_or_default();
            let tmp = target_folder.join("models.yml.new");
            std::fs::write(&tmp, &replaced).map_err(|e| e.to_string())?;
            std::fs::rename(&tmp, &stow_target_models).map_err(|e| e.to_string())?;
            let before = snapshot(&home.join("dotfiles"))?;
            let r = confirmed(
                app,
                main,
                &seen,
                "restore",
                json!({"targets": v["targets"]}),
                &[&txt(&stow_models)],
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
            // A new preview's restore puts the originals back through the links.
            let v = read_op(main, &seen, "preview", pins(&stow_models, &stow_config))?;
            written(
                app,
                main,
                &seen,
                "restore",
                json!({"targets": v["targets"]}),
                &[&txt(&stow_models)],
                &hidden,
            )?;
            require(
                read(&stow_target_models) == Some(bytes("stow_models"))
                    && read(&stow_target_config) == Some(bytes("stow_config")),
                format!(
                    "the stow targets after restore: {:?} {:?}",
                    read(&stow_target_models),
                    read(&stow_target_config)
                ),
            )?;
            require(
                link(&stow_models)? == (ino_models, PathBuf::from(&stow_models_text))
                    && link(&stow_config)? == (ino_config, PathBuf::from(&stow_config_text)),
                "a stow link changed",
            )
        })(),
    )?;
    passed("stow");

    // ---- root: files inside the selected root are created, then cleared away
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
                &[&txt(&root_link), &txt(&root_models)],
                &hidden,
            )?;
            require(
                read(&root_models) == Some(bytes("root_models"))
                    && read(&root_config) == Some(bytes("root_config"))
                    && mode(&root_models) == 0o600
                    && mode(&root_config) == 0o600,
                format!(
                    "the root's files after apply: {:?} {:?}",
                    read(&root_models),
                    read(&root_config)
                ),
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
            require(
                !root_models.exists() && !root_config.exists(),
                "clear left a file it created",
            )?;
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
            // The CLI: the terminal running omp found on the PATH.
            let recipe = read_op(main, &seen, "recipe", json!({}))?;
            require(
                recipe
                    == json!({"client": "Oh My Pi", "kind": "terminal", "program": "omp",
                        "args": []}),
                format!("the recipe: {recipe}"),
            )?;
            let needles = [
                EXECUTION,
                "launch",
                &txt(&terminal),
                &txt(&omp),
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
            // Confirmed: the terminal runs omp in the folder, with no key.
            let r = launch(&recipe, in_work.clone(), &needles, false)?;
            require(r["ok"] == true, format!("the confirmed launch: {r}"))?;
            let (argv, cwd, _) = stand_in(&records, "terminal")?;
            require(
                argv == vec!["-e".to_string(), txt(&omp)] && cwd == txt(&work),
                format!("the terminal ran with {argv:?} in {cwd}"),
            )?;
            let (argv, cwd, env) = stand_in(&records, "omp")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("omp ran with {argv:?} in {cwd}"),
            )?;
            clean(&env, "omp")?;
            // The install script's place holds a program: the recipe names it instead.
            clear_records(&records)?;
            place(&installed, &local_bin)?;
            let recipe = read_op(main, &seen, "recipe", json!({}))?;
            require(
                recipe["program"] == json!(txt(&local_bin)),
                format!("the recipe with omp in ~/.local/bin: {recipe}"),
            )?;
            let r = launch(
                &recipe,
                in_work.clone(),
                &[EXECUTION, "launch", &txt(&local_bin)],
                false,
            )?;
            require(r["ok"] == true, format!("the installed omp's launch: {r}"))?;
            let (argv, cwd, env) = stand_in(&records, "installed")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("the installed omp ran with {argv:?} in {cwd}"),
            )?;
            clean(&env, "the installed omp")?;
            // bun's place comes first once it holds a program.
            place(&installed, &bun_bin)?;
            let recipe = read_op(main, &seen, "recipe", json!({}))?;
            require(
                recipe["program"] == json!(txt(&bun_bin)),
                format!("the recipe with omp in bun's place: {recipe}"),
            )
        })(),
    )?;
    passed("launch");

    // ---- uninstall: no tool process is left, the files stay as they are
    step(
        "uninstall",
        (|| {
            let before = (read(&models), read(&config));
            seen.add(ok(main, "device_enable", json!({"id": w, "enabled": false}))?.to_string());
            seen.add(ok(main, "device_remove", json!({"id": w}))?.to_string());
            wait_for("no pluxy/omp-setup process once removed", || {
                processes(&tool_program).is_empty().then_some(())
            })?;
            require(
                (read(&models), read(&config)) == before,
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
            let mut left = leftovers(&home.join(".omp/agent"))?;
            left.extend(leftovers(&home.join("dotfiles"))?);
            left.extend(leftovers(&home.join(".local/share/chezmoi"))?);
            require(left.is_empty(), format!("temporary files left: {left:?}"))?;
            untouched()?;
            require(
                read(&unknown.join("models.yml")) == Some(bytes("unknown"))
                    && read(&broken.join("models.yml")) == Some(bytes("broken"))
                    && read(&legacy.join("models.json")) == Some(bytes("legacy"))
                    && !legacy.join("models.yml").exists(),
                "a refused file changed",
            )
        })(),
    )?;
    passed("audit");

    out["passed"] = json!(true);
    log("omp", out);
    Ok(())
}
