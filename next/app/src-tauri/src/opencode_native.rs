//! The native test's check of the OpenCode setup and launcher, pluxy/opencode, through the shared
//! acceptance flow of an integration (native-test builds with `artifact-test-trust` only). It
//! drives the real app through the window's own commands, the real native key entry and
//! confirmations, with the packages and the files `native/opencode.ts` made in this run's HOME
//! (OpenCode's global config folder as a person keeps it, with an `opencode.jsonc` and an older
//! `opencode.json`, its login file, a stow link, a chezmoi-style root, configs OpenCode would not
//! take) and its stand-in terminal, `opencode` and desktop app. Each refusal has a positive control
//! on the same route:
//!
//! - install: pluxy/opencode and pluxy/execution install from the curated registry and are enabled
//!   through their native confirmations, which say "full access to your user account";
//! - key: the key is typed into native key entry; the window gets only its ID;
//! - unknown: a preview of a config naming another schema, and of one using single quotes, is
//!   refused unasked and changes nothing; a preview of the person's config (a read, asked nothing)
//!   picks `opencode.jsonc` over `opencode.json`, as OpenCode does, pins it and reads its values
//!   back, with no key;
//! - apply: asks a native confirmation naming the file, the key's label and the same-instant
//!   sentence, never the key; then the config holds exactly the bytes `expect.json` says (comments,
//!   trailing commas and the person's settings in place), mode kept, and the preview reads it back
//!   as configured; `opencode.json` and the login file are untouched;
//! - restore: after the person's edit, Restore Backup puts the original bytes back;
//! - clear: applied again and edited twice by the person, Clear takes out only pluxy's provider and
//!   default model: their theme and their own model stay, comments too;
//! - backups: a Manual Backup, a diff naming the setting the person changed and never its value,
//!   restore over that edit, delete;
//! - stow: an apply through the selected link changes only its target (strict JSON stays without
//!   trailing commas), the link keeps its inode and text; once the target is replaced, a confirmed
//!   write fails as changed and writes nothing; a new preview's restore puts the original back
//!   through the link;
//! - root: an apply to a file inside the selected root creates it, the root link kept; clear
//!   removes it again;
//! - launch: the CLI recipe is the terminal running `opencode`, and once OpenCode's install script
//!   place `~/.opencode/bin/opencode` holds a program, that one; the desktop recipe is a direct
//!   launch of `/opt/OpenCode/ai.opencode` in HOME. Through the launcher, a cancelled confirmation
//!   starts nothing; confirmed ones run the stand-in terminal with exactly `-e <opencode>` in the
//!   chosen folder and `opencode` with no arguments, then the installed one, then the desktop app
//!   (its path given as the executable override) with no arguments in HOME; none has the key or the
//!   login canary in its environment;
//! - uninstall: disabled and removed, no pluxy/opencode process is left and the files are as the
//!   last step left them;
//! - audit: the key and the login canary are in no window answer and no confirmation, no temporary
//!   file is left, and files no step names keep their bytes.
//!
//! One line on stderr: `native-test: opencode <JSON>`.

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

const PACKAGE: &str = "pluxy/opencode";
const EXECUTION: &str = "pluxy/execution";
const LABEL: &str = "fixture OpenCode key";
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
    let (jsonc, json_file, auth) = (p(&x["jsonc"]), p(&x["json"]), p(&x["auth"]));
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
    let (unknown, broken) = (p(&x["unknown"]), p(&x["broken"]));
    let records = p(&x["records"]);
    let canon = |v: &Value| std::fs::canonicalize(p(v)).map_err(|e| e.to_string());
    let work = canon(&x["work"])?;
    let (terminal, opencode, installed, override_desktop) = (
        canon(&x["terminal"])?,
        canon(&x["opencode"])?,
        canon(&x["installed"])?,
        canon(&x["override"])?,
    );
    let installed_at = p(&x["installed_at"]);
    let bytes = |k: &str| s(&x["bytes"][k]).replace("@KEY@", &key);
    let pair = |k: &str| (s(&x["bytes"][k][0]), s(&x["bytes"][k][1]));
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
    let file = |q: &Path| json!({"targets": [{"file": q}]});
    let untouched = || -> Result<(), String> {
        require(
            read(&json_file) == Some(bytes("json")) && read(&auth) == Some(bytes("auth")),
            "opencode.json or the OpenCode login file changed",
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
    let tool_program = tool_program().ok_or("pluxy/opencode is not in the store")?;

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

    // ---- unknown: configs OpenCode would not take are refused, the person's is read
    step(
        "unknown",
        (|| {
            read_refused(main, &seen, "preview", file(&unknown), "schema")?;
            read_refused(main, &seen, "preview", file(&broken), "JSONC")?;
            require(
                read(&unknown) == Some(bytes("unknown")) && read(&broken) == Some(bytes("broken")),
                "a refused file changed",
            )?;
            // The positive control on the same route: the person's config, the default file,
            // which is opencode.jsonc (OpenCode reads it after opencode.json, so it wins).
            let v = read_op(main, &seen, "preview", json!({}))?;
            require(
                v["targets"][0]["selected"] == json!(txt(&jsonc)) && v["applied"] == false,
                format!("the default preview: {v}"),
            )?;
            let c = &v["config"];
            require(
                c["state"] == "not-configured"
                    && c["key_set"] == false
                    && c["default_model"] == "anthropic/claude-sonnet-5-5",
                format!("the config read back: {c}"),
            )?;
            require(
                read(&jsonc) == Some(bytes("original")),
                "a preview changed the config",
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
                &[&txt(&jsonc), LABEL, SAME_INSTANT],
                &hidden,
            )?;
            require(o["outcome"] == "applied", format!("the apply: {o}"))?;
            require(
                read(&jsonc) == Some(bytes("applied")),
                format!("the applied config: {:?}", read(&jsonc)),
            )?;
            require(mode(&jsonc) == 0o644, "the config's mode changed")?;
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
                    && c["models"]
                        == json!([
                            {"id": "gpt-6.1-sol", "name": "GPT-6.1 Sol", "attachment": true},
                            {"id": "deepseek-v4", "name": "deepseek-v4", "attachment": false}
                        ]),
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
            let (from, to) = pair("edited_theme");
            edit(&jsonc, &from, &to)?;
            let v = read_op(main, &seen, "preview", file(&jsonc))?;
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
                &[&txt(&jsonc)],
                &hidden,
            )?;
            require(o["outcome"] == "restored", format!("the restore: {o}"))?;
            require(
                read(&jsonc) == Some(bytes("original")) && mode(&jsonc) == 0o644,
                format!("the restored config: {:?}", read(&jsonc)),
            )?;
            untouched()
        })(),
    )?;
    passed("restore");

    // ---- clear: applied again, edited twice, only pluxy's untouched values go
    step(
        "clear",
        (|| {
            let v = read_op(main, &seen, "preview", file(&jsonc))?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
                &[&txt(&jsonc), LABEL],
                &hidden,
            )?;
            require(read(&jsonc) == Some(bytes("applied")), "the second apply")?;
            for k in ["edited_theme", "edited_model"] {
                let (from, to) = pair(k);
                edit(&jsonc, &from, &to)?;
            }
            let v = read_op(main, &seen, "preview", file(&jsonc))?;
            let o = written(
                app,
                main,
                &seen,
                "clear",
                json!({"targets": v["targets"]}),
                &[&txt(&jsonc)],
                &hidden,
            )?;
            require(o["outcome"] == "cleared", format!("the clear: {o}"))?;
            require(
                read(&jsonc) == Some(bytes("cleared")),
                format!("the cleared config: {:?}", read(&jsonc)),
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
                &[&txt(&jsonc)],
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
            edit(&jsonc, &from, &to)?;
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
                &[&txt(&jsonc)],
                &hidden,
            )?;
            require(
                read(&jsonc) == Some(bytes("cleared")),
                format!("the restored backup: {:?}", read(&jsonc)),
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
                apply_input(&v, &secret),
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
                read(&stow_folder.join("keep.json")) == Some(bytes("keep")),
                "a file beside the stow target changed",
            )?;
            // The target replaced (as an editor saves, by rename) after a preview.
            let v = read_op(main, &seen, "preview", file(&stow_link))?;
            let replaced = read(&stow_target).unwrap_or_default();
            let tmp = stow_folder.join("opencode.json.new");
            std::fs::write(&tmp, &replaced).map_err(|e| e.to_string())?;
            std::fs::rename(&tmp, &stow_target).map_err(|e| e.to_string())?;
            let before = snapshot(&home.join("dotfiles"))?;
            let r = confirmed(
                app,
                main,
                &seen,
                "restore",
                json!({"targets": v["targets"]}),
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
            // A new preview's restore puts the original back through the link.
            let v = read_op(main, &seen, "preview", file(&stow_link))?;
            written(
                app,
                main,
                &seen,
                "restore",
                json!({"targets": v["targets"]}),
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
            let sel = json!({"targets": [{"root": root_link, "path": "opencode.jsonc"}]});
            let v = read_op(main, &seen, "preview", sel.clone())?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
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
                json!({"targets": v["targets"]}),
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

    // ---- launch: both recipes through the launcher; cancel starts nothing
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
            // The CLI: the terminal running opencode found on the PATH.
            let cli = read_op(main, &seen, "recipe", json!({"target": "cli"}))?;
            require(
                cli == json!({"client": "OpenCode", "kind": "terminal", "program": "opencode",
                    "args": []}),
                format!("the CLI recipe: {cli}"),
            )?;
            let needles = [
                EXECUTION,
                "launch",
                &txt(&terminal),
                &txt(&opencode),
                &txt(&work),
            ];
            // Cancelled: nothing starts.
            let r = launch(&cli, in_work.clone(), &needles, true)?;
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
            // Confirmed: the terminal runs opencode in the folder, with no key.
            let r = launch(&cli, in_work.clone(), &needles, false)?;
            require(r["ok"] == true, format!("the confirmed launch: {r}"))?;
            let (argv, cwd, _) = stand_in(&records, "terminal")?;
            require(
                argv == vec!["-e".to_string(), txt(&opencode)] && cwd == txt(&work),
                format!("the terminal ran with {argv:?} in {cwd}"),
            )?;
            let (argv, cwd, env) = stand_in(&records, "opencode")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("opencode ran with {argv:?} in {cwd}"),
            )?;
            clean(&env, "opencode")?;
            // OpenCode's install script place holds a program: the recipe names it instead.
            clear_records(&records)?;
            std::fs::create_dir_all(installed_at.parent().ok_or("no folder")?)
                .map_err(|e| e.to_string())?;
            std::fs::copy(&installed, &installed_at).map_err(|e| e.to_string())?;
            let cli = read_op(main, &seen, "recipe", json!({"target": "cli"}))?;
            require(
                cli["program"] == json!(txt(&installed_at)),
                format!("the CLI recipe with OpenCode installed by its script: {cli}"),
            )?;
            let r = launch(
                &cli,
                in_work.clone(),
                &[EXECUTION, "launch", &txt(&installed_at)],
                false,
            )?;
            require(r["ok"] == true, format!("the installed CLI's launch: {r}"))?;
            let (argv, cwd, env) = stand_in(&records, "installed")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("the installed opencode ran with {argv:?} in {cwd}"),
            )?;
            clean(&env, "the installed opencode")?;
            // The desktop app: a direct launch in HOME; its path given as the override.
            clear_records(&records)?;
            let desktop = read_op(main, &seen, "recipe", json!({"target": "app"}))?;
            require(
                desktop
                    == json!({"client": "OpenCode Desktop", "kind": "direct",
                        "program": x["desktop"], "args": [], "cwd": home}),
                format!("the desktop recipe: {desktop}"),
            )?;
            let home_now = std::fs::canonicalize(&home).map_err(|e| e.to_string())?;
            let over = json!({"override": override_desktop});
            let needles = [EXECUTION, "launch", &txt(&override_desktop)];
            let r = launch(&desktop, over.clone(), &needles, true)?;
            require(
                r["ok"] == false,
                format!("a cancelled desktop launch went ahead: {r}"),
            )?;
            std::thread::sleep(Duration::from_secs(1));
            require(
                std::fs::read_dir(&records)
                    .map_err(|e| e.to_string())?
                    .next()
                    .is_none(),
                "a cancelled desktop launch started something",
            )?;
            let r = launch(&desktop, over, &needles, false)?;
            require(r["ok"] == true, format!("the desktop launch: {r}"))?;
            let (argv, cwd, env) = stand_in(&records, "desktop")?;
            require(
                argv.is_empty() && cwd == txt(&home_now),
                format!("the desktop app ran with {argv:?} in {cwd}"),
            )?;
            clean(&env, "the desktop app")
        })(),
    )?;
    passed("launch");

    // ---- uninstall: no tool process is left, the files stay as they are
    step(
        "uninstall",
        (|| {
            let before = std::fs::read(&jsonc).map_err(|e| e.to_string())?;
            seen.add(ok(main, "device_enable", json!({"id": w, "enabled": false}))?.to_string());
            seen.add(ok(main, "device_remove", json!({"id": w}))?.to_string());
            wait_for("no pluxy/opencode process once removed", || {
                processes(&tool_program).is_empty().then_some(())
            })?;
            require(
                std::fs::read(&jsonc).map_err(|e| e.to_string())? == before,
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
            let mut left = leftovers(&home.join(".config/opencode"))?;
            left.extend(leftovers(&home.join("dotfiles"))?);
            left.extend(leftovers(&home.join(".local/share/chezmoi"))?);
            require(left.is_empty(), format!("temporary files left: {left:?}"))?;
            untouched()?;
            require(
                read(&unknown) == Some(bytes("unknown")) && read(&broken) == Some(bytes("broken")),
                "a refused file changed",
            )
        })(),
    )?;
    passed("audit");

    out["passed"] = json!(true);
    log("opencode", out);
    Ok(())
}
