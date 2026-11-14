//! The native test's check of the Codex setup and launcher, pluxy/codex-setup, through the shared
//! acceptance flow of an integration (native-test builds with `artifact-test-trust` only). It
//! drives the real app through the window's own commands, the real native key entry and
//! confirmations, with the packages and the files `native/codex.ts` made in this run's HOME
//! (Codex's config.toml as a person keeps it, its login file, a stow link, a chezmoi-style root,
//! files Codex would not take) and its stand-in terminal, `codex` and Codex App program. Each
//! refusal has a positive control on the same route:
//!
//! - install: pluxy/codex-setup and pluxy/execution install from the curated registry and are
//!   enabled through their native confirmations, which say "full access to your user account";
//! - key: the key is typed into native key entry; the window gets only its ID;
//! - unknown: a preview of a config that is not TOML, of one asking pluxy's provider for the
//!   removed "chat" wire API and of a catalog with no models list is refused unasked and changes
//!   nothing; a preview of the person's files (a read, asked nothing) pins both and reads their
//!   values back, with no key;
//! - import: the core's served models become catalog entries from pluxy's Codex templates; a
//!   model without a template is named, not listed; nothing is written;
//! - apply: asks a native confirmation naming both files, the key's label and the same-instant
//!   sentence, never the key; then config.toml and the catalog hold exactly the bytes
//!   `expect.json` says (comments and unknown tables kept), the preview reads them back as
//!   configured, with the provider the person used before; the login file is untouched;
//! - restore: after the person's edit, Restore Backup puts the original config back and removes
//!   the catalog Apply made;
//! - clear: applied again and edited twice by the person, Clear takes out only the untouched
//!   values: their provider comes back, their model and their own setting stay, the catalog goes;
//! - stow: an apply through the selected link changes only its target, the link keeps its inode
//!   and text; once the target is replaced, a confirmed write fails as changed and writes
//!   nothing; a new preview's restore puts the original back through the link;
//! - root: an apply to files inside the selected root creates both, the root link kept; clear
//!   removes them again;
//! - backups: a cancelled Manual Backup keeps nothing; a confirmed one keeps both files (the
//!   catalog as absent); the diff names the person's changed setting, never its value; restore
//!   puts the backed-up bytes back; delete removes it;
//! - cli: the package's CLI recipe is the terminal running `codex`; through the launcher, a
//!   cancelled confirmation starts nothing; a confirmed one runs the stand-in terminal with
//!   exactly `-e <codex>` in the chosen folder, and `codex` with no arguments and no key in its
//!   environment; an executable override runs the override instead;
//! - app: Start App runs the program the person names, directly, with no arguments;
//! - service: with an unrelated task running, a cancelled Restart CLI Service starts nothing; a
//!   confirmed one runs exactly `codex app-server daemon restart` in HOME, and the unrelated task
//!   still runs;
//! - uninstall: disabled and removed, no pluxy/codex-setup process is left and the files are as
//!   the last step left them;
//! - audit: the key and the login canary are in no window answer and no confirmation, no
//!   temporary file is left, and files no step names keep their bytes.
//!
//! One line on stderr: `native-test: codex <JSON>`.

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

const PACKAGE: &str = "pluxy/codex-setup";
const EXECUTION: &str = "pluxy/execution";
const LABEL: &str = "fixture Codex key";
const SAME_INSTANT: &str = "an edit made in the same instant by another program may be lost";
/// Confirm stays disabled for a second in a dialog another package asked for.
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
    let (config, catalog, auth) = (p(&x["config"]), p(&x["catalog"]), p(&x["auth"]));
    let (stow_link, stow_text, stow_target, stow_folder, stow_catalog) = (
        p(&x["stow"]["link"]),
        s(&x["stow"]["text"]),
        p(&x["stow"]["target"]),
        p(&x["stow"]["folder"]),
        p(&x["stow"]["catalog"]),
    );
    let (root_link, root_text, root_config, root_catalog) = (
        p(&x["root"]["link"]),
        s(&x["root"]["text"]),
        p(&x["root"]["config"]),
        p(&x["root"]["catalog"]),
    );
    let (broken, unknown, badcat) = (p(&x["broken"]), p(&x["unknown"]), p(&x["badcat"]));
    let records = p(&x["records"]);
    let canon = |q: PathBuf| std::fs::canonicalize(q).map_err(|e| e.to_string());
    let work = canon(p(&x["work"]))?;
    let (terminal, codex, override_codex, codex_app) = (
        canon(p(&x["terminal"]))?,
        canon(p(&x["codex"]))?,
        canon(p(&x["override"]))?,
        canon(p(&x["app"]))?,
    );
    let bytes = |k: &str| s(&x["bytes"][k]).replace("@KEY@", &key);
    let pair = |k: &str| (s(&x["bytes"][k][0]), s(&x["bytes"][k][1]));
    let txt = |q: &Path| q.display().to_string();
    let hidden = [key.as_str(), login.as_str()];
    let seen = Seen::default();
    let mut secret = String::new();
    let mut models = Value::Null;
    let mut out = json!({"steps": []});
    let mut passed = |name: &str| out["steps"].as_array_mut().unwrap().push(json!(name));
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("{name}: {e}"));
    let files = |a: &Path, b: &Path| json!({"targets": [{"file": a}, {"file": b}]});
    let pins = |v: &Value| json!({"targets": v["targets"]});
    let untouched = || -> Result<(), String> {
        require(
            read(&auth) == Some(bytes("auth")),
            "Codex's login file changed",
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
        .ok_or("pluxy/codex-setup is not in the store")?;

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

    // ---- unknown: files Codex would not take are refused, the person's are read
    step(
        "unknown",
        (|| {
            let cat_of = |dir: &Path| dir.parent().unwrap().join("pluxy-model-catalog.json");
            read_refused(
                main,
                &seen,
                "preview",
                files(&broken, &cat_of(&broken)),
                "not TOML",
            )?;
            read_refused(
                main,
                &seen,
                "preview",
                files(&unknown, &cat_of(&unknown)),
                "wire_api",
            )?;
            read_refused(
                main,
                &seen,
                "preview",
                files(
                    &badcat.join("config.toml"),
                    &badcat.join("pluxy-model-catalog.json"),
                ),
                "no models list",
            )?;
            require(
                read(&broken) == Some(bytes("broken"))
                    && read(&unknown) == Some(bytes("unknown"))
                    && read(&badcat.join("pluxy-model-catalog.json"))
                        == Some(bytes("badcat_catalog")),
                "a refused file changed",
            )?;
            // The positive control on the same route: the person's files, the default ones.
            let v = read_op(main, &seen, "preview", json!({}))?;
            require(
                v["targets"][0]["selected"] == json!(txt(&config))
                    && v["targets"][1]["selected"] == json!(txt(&catalog))
                    && v["applied"] == false,
                format!("the default preview: {v}"),
            )?;
            let st = &v["settings"];
            require(
                st["state"] == "not-configured"
                    && st["key_set"] == false
                    && st["model"] == "gpt-5.5"
                    && st["provider"] == "openai"
                    && st["models"] == json!([]),
                format!("the settings read back: {st}"),
            )?;
            require(
                read(&config) == Some(bytes("original")) && !catalog.exists(),
                "a preview changed the files",
            )
        })(),
    )?;
    passed("unknown");

    // ---- import: the core's served models become catalog entries; nothing is written
    step(
        "import",
        (|| {
            let before = snapshot(&home.join(".codex"))?;
            let v = read_op(main, &seen, "import", json!({"models": x["served"]}))?;
            require(
                v["missing"] == x["missing"],
                format!("the import's missing models: {v}"),
            )?;
            let list = v["models"].as_array().cloned().unwrap_or_default();
            require(
                list.len() == 1
                    && list[0]["slug"] == "gpt-6-luna"
                    && list[0]["upstream"] == "gpt-6-luna",
                format!("the imported models: {v}"),
            )?;
            let mut m = list[0].clone();
            for (k, e) in x["edits"].as_object().into_iter().flatten() {
                m[k] = e.clone();
            }
            m.as_object_mut().unwrap().remove("system_prompt_chars");
            models = json!([m]);
            require(
                snapshot(&home.join(".codex"))? == before,
                "the import wrote a file",
            )
        })(),
    )?;
    passed("import");
    // The input of an apply: the window's values, the pins of a preview, the models and the key's ID.
    let apply_input = |preview: &Value| {
        let mut i = x["settings_input"].clone();
        i["targets"] = preview["targets"].clone();
        i["key"] = json!(secret);
        i["models"] = models.clone();
        i
    };

    // ---- apply: both files hold exactly the expected bytes
    step(
        "apply",
        (|| {
            let v = read_op(main, &seen, "preview", files(&config, &catalog))?;
            let o = written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v),
                &[&txt(&config), &txt(&catalog), LABEL, SAME_INSTANT],
                &hidden,
            )?;
            require(o["outcome"] == "applied", format!("the apply: {o}"))?;
            require(
                read(&config) == Some(bytes("applied")),
                format!("the applied config: {:?}", read(&config)),
            )?;
            require(
                read(&catalog) == Some(bytes("catalog")),
                "the applied catalog differs",
            )?;
            require(
                mode(&config) == 0o644 && mode(&catalog) == 0o600,
                "a file's mode is not as expected",
            )?;
            let v = read_op(main, &seen, "preview", files(&config, &catalog))?;
            let st = &v["settings"];
            require(
                v["applied"] == true
                    && st["state"] == "configured"
                    && st["key_set"] == true
                    && st["provider"] == "pluxy"
                    && st["model"] == "gpt-6-luna"
                    && st["base_url"] == "http://127.0.0.1:41999/v1"
                    && st["approval_model"] == "gpt-6-luna"
                    && st["agents"]["max_threads"] == 3
                    && st["agents"]["effort"] == "low"
                    && st["models"][0]["display_name"] == "Luna via pluxy"
                    && st["models"][0]["context_window"] == 200000,
                format!("the applied settings read back: {v}"),
            )?;
            untouched()
        })(),
    )?;
    passed("apply");

    // ---- restore: the person's edit, then Restore Backup puts the originals back
    step(
        "restore",
        (|| {
            let (from, to) = pair("edited_policy");
            edit(&config, &from, &to)?;
            let v = read_op(main, &seen, "preview", files(&config, &catalog))?;
            require(
                v["edited"] == json!([true, false]),
                format!("the edit is not seen: {v}"),
            )?;
            let o = written(
                app,
                main,
                &seen,
                "restore",
                pins(&v),
                &[&txt(&config), &txt(&catalog)],
                &hidden,
            )?;
            require(o["outcome"] == "restored", format!("the restore: {o}"))?;
            require(
                read(&config) == Some(bytes("original")) && mode(&config) == 0o644,
                format!("the restored config: {:?}", read(&config)),
            )?;
            require(!catalog.exists(), "restore left the catalog Apply made")?;
            untouched()
        })(),
    )?;
    passed("restore");

    // ---- clear: applied again, edited twice, only the untouched values go
    step(
        "clear",
        (|| {
            let v = read_op(main, &seen, "preview", files(&config, &catalog))?;
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
            for k in ["edited_model", "edited_retries"] {
                let (from, to) = pair(k);
                edit(&config, &from, &to)?;
            }
            let v = read_op(main, &seen, "preview", files(&config, &catalog))?;
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
            require(!catalog.exists(), "clear left the untouched catalog")?;
            untouched()
        })(),
    )?;
    passed("clear");

    // ---- stow: only the link's target changes; a replaced target refuses writes
    step(
        "stow",
        (|| {
            let (inode, _) = link(&stow_link)?;
            let sel = files(&stow_link, &stow_catalog);
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
                apply_input(&v),
                &[&txt(&stow_link), &txt(&stow_target)],
                &hidden,
            )?;
            require(
                read(&stow_target) == Some(bytes("stow_applied")) && mode(&stow_target) == 0o640,
                format!("the stow target after apply: {:?}", read(&stow_target)),
            )?;
            require(
                read(&stow_catalog) == Some(bytes("catalog")),
                "the stow catalog differs",
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
            let v = read_op(main, &seen, "preview", sel.clone())?;
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
                read(&stow_target) == Some(bytes("stow")) && !stow_catalog.exists(),
                format!("the stow files after restore: {:?}", read(&stow_target)),
            )?;
            require(
                link(&stow_link)? == (inode, PathBuf::from(&stow_text)),
                "the stow link changed",
            )
        })(),
    )?;
    passed("stow");

    // ---- root: files inside the selected root are created, then cleared away
    step(
        "root",
        (|| {
            let (inode, _) = link(&root_link)?;
            let sel = json!({"targets": [{"root": root_link, "path": "config.toml"},
                {"root": root_link, "path": "pluxy-model-catalog.json"}]});
            let v = read_op(main, &seen, "preview", sel.clone())?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v),
                &[&txt(&root_link), &txt(&root_config)],
                &hidden,
            )?;
            require(
                read(&root_config) == Some(bytes("root_applied")) && mode(&root_config) == 0o600,
                format!("the root's config after apply: {:?}", read(&root_config)),
            )?;
            require(
                read(&root_catalog) == Some(bytes("catalog")),
                "the root's catalog differs",
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
            require(
                !root_config.exists() && !root_catalog.exists(),
                "clear left the files it created",
            )?;
            require(
                link(&root_link)? == (inode, PathBuf::from(&root_text)),
                "the root link changed",
            )
        })(),
    )?;
    passed("root");

    // ---- backups: both files in one manual backup, a names-only diff, restore, delete
    step(
        "backups",
        (|| {
            let sel = files(&config, &catalog);
            let list = || -> Result<Vec<Value>, String> {
                Ok(read_op(main, &seen, "backup-list", json!({}))?["backups"]
                    .as_array()
                    .cloned()
                    .unwrap_or_default())
            };
            require(list()?.is_empty(), "a manual backup exists already")?;
            let v = read_op(main, &seen, "preview", sel.clone())?;
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
                        == json!([
                            {"selected": txt(&config), "path": null, "target": txt(&config),
                                "exists": true, "size": bytes("cleared").len()},
                            {"selected": txt(&catalog), "path": null, "target": txt(&catalog),
                                "exists": false, "size": 0}
                        ]),
                format!("the list after Manual Backup: {l:?}"),
            )?;
            let b = &x["backups"];
            edit(&config, &s(&b["edit"][0]), &s(&b["edit"][1]))?;
            let d = read_op(main, &seen, "backup-diff", json!({"backup": id}))?;
            require(
                d["same"] == false
                    && d["files"][0]["changes"] == b["diff"]
                    && d["files"][1]["same"] == true,
                format!("the diff after the edit: {d}"),
            )?;
            for value in b["values"].as_array().into_iter().flatten() {
                require(!d.to_string().contains(&s(value)), "the diff shows a value")?;
            }
            let v = read_op(main, &seen, "preview", sel.clone())?;
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

    // A launch through the launcher of recipe with extra fields, its confirmation naming needles,
    // cancelled or confirmed: the window's answer.
    let launch =
        |recipe: &Value, extra: Value, needles: &[&str], cancel: bool| -> Result<Value, String> {
            let mut l = recipe.clone();
            if l.get("cwd").is_none() {
                l["cwd"] = json!(work);
            }
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

    // ---- cli: the recipe through the launcher; cancel starts nothing
    step(
        "cli",
        (|| {
            let recipe = read_op(main, &seen, "recipe", json!({"target": "cli"}))?;
            require(
                recipe
                    == json!({"client": "Codex", "kind": "terminal", "program": "codex", "args": []}),
                format!("the CLI recipe: {recipe}"),
            )?;
            let auto = json!({"terminal": "automatic"});
            let needles = [
                EXECUTION,
                "launch",
                &txt(&terminal),
                &txt(&codex),
                &txt(&work),
            ];
            let r = launch(&recipe, auto.clone(), &needles, true)?;
            require(
                r["ok"] == false,
                format!("a cancelled launch went ahead: {r}"),
            )?;
            std::thread::sleep(Duration::from_secs(1));
            require(empty(&records)?, "a cancelled launch started something")?;
            let r = launch(&recipe, auto.clone(), &needles, false)?;
            require(r["ok"] == true, format!("the confirmed launch: {r}"))?;
            let (argv, cwd, _) = stand_in(&records, "terminal")?;
            require(
                argv == vec!["-e".to_string(), txt(&codex)] && cwd == txt(&work),
                format!("the terminal ran with {argv:?} in {cwd}"),
            )?;
            let (argv, cwd, env) = stand_in(&records, "codex")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("codex ran with {argv:?} in {cwd}"),
            )?;
            require(
                !env.contains(&key) && !env.contains(&login),
                "codex's environment holds the key or the login canary",
            )?;
            let mut o = auto;
            o["override"] = json!(override_codex);
            let r = launch(
                &recipe,
                o,
                &[EXECUTION, "launch", &txt(&override_codex)],
                false,
            )?;
            require(r["ok"] == true, format!("the override launch: {r}"))?;
            let _ = stand_in(&records, "terminal")?;
            let (argv, cwd, _) = stand_in(&records, "override")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("the override ran with {argv:?} in {cwd}"),
            )
        })(),
    )?;
    passed("cli");

    // ---- app: Start App runs the program the person names, directly
    step(
        "app",
        (|| {
            let recipe = read_op(main, &seen, "recipe", json!({"target": "app"}))?;
            require(
                recipe["kind"] == "direct" && recipe["args"] == json!([]),
                format!("the App recipe: {recipe}"),
            )?;
            let r = launch(
                &recipe,
                json!({"override": codex_app}),
                &[EXECUTION, "launch", &txt(&codex_app), &txt(&work)],
                false,
            )?;
            require(r["ok"] == true, format!("the App launch: {r}"))?;
            let (argv, cwd, env) = stand_in(&records, "app")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("the App ran with {argv:?} in {cwd}"),
            )?;
            require(!env.contains(&key), "the App's environment holds the key")
        })(),
    )?;
    passed("app");

    // ---- service: Restart CLI Service runs Codex's own restart; an unrelated task keeps running
    step(
        "service",
        (|| {
            let mut task = std::process::Command::new("sleep")
                .arg("600")
                .spawn()
                .map_err(|e| format!("the unrelated task: {e}"))?;
            let result = (|| {
                let recipe = read_op(main, &seen, "recipe", json!({"target": "service"}))?;
                let home_now = canon(home.clone())?;
                require(
                    recipe["kind"] == "direct"
                        && recipe["program"] == "codex"
                        && recipe["args"] == json!(["app-server", "daemon", "restart"]),
                    format!("the service recipe: {recipe}"),
                )?;
                let needles = [EXECUTION, "launch", &txt(&codex), "app-server"];
                let r = launch(&recipe, json!({}), &needles, true)?;
                require(
                    r["ok"] == false,
                    format!("a cancelled restart went ahead: {r}"),
                )?;
                std::thread::sleep(Duration::from_secs(1));
                require(empty(&records)?, "a cancelled restart started something")?;
                let r = launch(&recipe, json!({}), &needles, false)?;
                require(r["ok"] == true, format!("the confirmed restart: {r}"))?;
                let (argv, cwd, env) = stand_in(&records, "codex")?;
                require(
                    argv == ["app-server", "daemon", "restart"] && cwd == txt(&home_now),
                    format!("codex ran with {argv:?} in {cwd}"),
                )?;
                require(
                    !env.contains(&key),
                    "the restart's environment holds the key",
                )?;
                require(
                    task.try_wait().map_err(|e| e.to_string())?.is_none(),
                    "the unrelated task stopped",
                )
            })();
            let _ = task.kill();
            let _ = task.wait();
            result
        })(),
    )?;
    passed("service");

    // ---- uninstall: no tool process is left, the files stay as they are
    step(
        "uninstall",
        (|| {
            let before = std::fs::read(&config).map_err(|e| e.to_string())?;
            seen.add(ok(main, "device_enable", json!({"id": w, "enabled": false}))?.to_string());
            seen.add(ok(main, "device_remove", json!({"id": w}))?.to_string());
            wait_for("no pluxy/codex-setup process once removed", || {
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
            let mut left = leftovers(&home.join(".codex"))?;
            left.extend(leftovers(&home.join("dotfiles"))?);
            left.extend(leftovers(&home.join(".config/codex-stow"))?);
            left.extend(leftovers(&home.join(".local/share/chezmoi"))?);
            require(left.is_empty(), format!("temporary files left: {left:?}"))?;
            untouched()?;
            require(
                read(&broken) == Some(bytes("broken"))
                    && read(&unknown) == Some(bytes("unknown"))
                    && read(&badcat.join("config.toml")) == Some(bytes("badcat_config")),
                "a refused file changed",
            )
        })(),
    )?;
    passed("audit");

    out["passed"] = json!(true);
    log("codex", out);
    Ok(())
}
