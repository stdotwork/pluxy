//! The native test's check of the Claude Code setup and launcher, pluxy/claude-code, through the
//! shared acceptance flow of an integration (native-test builds with `artifact-test-trust` only).
//! It drives the real app through the window's own commands, the real native key entry and
//! confirmations, with the packages and the files `native/claude-code.ts` made in this run's HOME
//! (Claude Code's settings as a person keeps them, its login files, a stow link, a chezmoi-style
//! root, settings Claude Code would not take) and its stand-in terminal and `claude`. Each refusal
//! has a positive control on the same route:
//!
//! - install: pluxy/claude-code and pluxy/execution install from the curated registry and are
//!   enabled through their native confirmations, which say "full access to your user account";
//! - key: the key is typed into native key entry; the window gets only its ID;
//! - unknown: a preview of settings naming another schema, and of settings that are not strict
//!   JSON, is refused unasked and changes nothing; a preview of the person's settings (a read,
//!   asked nothing) pins them and reads their values back, with no key;
//! - apply: asks a native confirmation naming the file, the key's label and the same-instant
//!   sentence, never the key; then the settings hold exactly the bytes `expect.json` says, mode
//!   kept, and the preview reads them back as configured; the login files are untouched;
//! - restore: after the person's edit, Restore Backup puts the original bytes back;
//! - clear: applied again and edited twice by the person, Clear takes out only the untouched
//!   values: their API key comes back, their timeout and model stay;
//! - stow: an apply through the selected link changes only its target, the link keeps its inode
//!   and text; once the target is replaced, a confirmed apply fails as changed and writes
//!   nothing; a new preview's restore puts the original back through the link;
//! - root: an apply to a file inside the selected root creates it, the root link kept; clear
//!   removes it again;
//! - backups: the backup browser (`backups_native.rs`): manual backups listed by ID, a diff that
//!   names settings and never values, restore over the person's edits and of an absent file,
//!   cancels and foreign IDs refused, retention, delete, nothing of the files in the tool's data;
//! - launch: the package's recipe is the terminal running `claude`; through the launcher, a
//!   cancelled confirmation starts nothing; a confirmed one runs the stand-in terminal with
//!   exactly `-e <claude>` in the chosen folder, and `claude` with no arguments, no key and no
//!   Anthropic variable in its environment; an executable override runs the override instead;
//! - uninstall: disabled and removed, no pluxy/claude-code process is left and the files are as
//!   the last step left them;
//! - audit: the key and the login canary are in no window answer and no confirmation, no
//!   temporary file is left, and files no step names keep their bytes.
//!
//! One line on stderr: `native-test: claude-code <JSON>`.

use super::admission::{ok, press, require, settle, start};
use super::program::processes;
use super::routing::{call, enable, install};
use super::transactions::{leftovers, link, read, snapshot, unasked};
use super::vault::{dialog_with, type_key};
use super::{log, wait_for};
use serde_json::{json, Value};
use std::cell::RefCell;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::time::Duration;
use tauri::{AppHandle, Manager, Webview};

const PACKAGE: &str = "pluxy/claude-code";
const EXECUTION: &str = "pluxy/execution";
const LABEL: &str = "fixture Claude Code key";
const CLIENT: &str = "Claude Code";
const SAME_INSTANT: &str = "an edit made in the same instant by another program may be lost";

/// The answers and confirmations the check saw, for the audit.
#[derive(Default)]
pub(super) struct Seen(RefCell<Vec<String>>);

impl Seen {
    pub(super) fn add(&self, s: impl Into<String>) {
        self.0.borrow_mut().push(s.into());
    }
}

/// A read operation of the package: its value, failing on an error or a native confirmation.
pub(super) fn read_op(
    main: &Webview,
    seen: &Seen,
    op: &str,
    input: Value,
) -> Result<Value, String> {
    let (r, asked) = unasked(|| call(main, PACKAGE, op, input.clone()));
    let r = r?;
    seen.add(r.to_string());
    require(!asked, format!("{op} asked a native confirmation"))?;
    require(r["ok"] == true, format!("{op} {input}: {r}"))?;
    Ok(r["value"].clone())
}

/// A read operation that must be refused, unasked, saying says.
pub(super) fn read_refused(
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
pub(super) fn confirmed(
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
    let what = format!("{} {op}", PACKAGE);
    let mut all = vec![what.as_str()];
    all.extend_from_slice(needles);
    let text = dialog_with(app, &all)?;
    seen.add(text.clone());
    if hidden.iter().any(|h| text.contains(h)) {
        press("alt+c")?;
        return Err(format!("the {op} confirmation shows a value it must not"));
    }
    press("alt+o")?;
    std::thread::sleep(Duration::from_millis(500));
    let (r, asked) = unasked(|| settle(main, &slot));
    let r = r?;
    seen.add(r.to_string());
    require(!asked, format!("{op} asked a second native confirmation"))?;
    Ok(r)
}

/// A confirmed write that must succeed: its output.
#[allow(clippy::too_many_arguments)]
pub(super) fn written(
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

pub(super) fn mode(p: &Path) -> u32 {
    std::fs::metadata(p)
        .map(|m| m.permissions().mode() & 0o777)
        .unwrap_or(0)
}

/// Replaces line from with to in the file at p, as a person's editor would (in place).
pub(super) fn edit(p: &Path, from: &str, to: &str) -> Result<(), String> {
    let now = read(p).ok_or_else(|| format!("{} cannot be read", p.display()))?;
    require(now.contains(from), format!("{} has no {from}", p.display()))?;
    std::fs::write(p, now.replacen(from, to, 1)).map_err(|e| e.to_string())
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
    let settings = p(&x["settings"]);
    let (credentials, claude_json) = (p(&x["credentials"]), p(&x["claude_json"]));
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
    let work = std::fs::canonicalize(p(&x["work"])).map_err(|e| e.to_string())?;
    let (terminal, claude, override_claude) = (
        std::fs::canonicalize(p(&x["terminal"])).map_err(|e| e.to_string())?,
        std::fs::canonicalize(p(&x["claude"])).map_err(|e| e.to_string())?,
        std::fs::canonicalize(p(&x["override"])).map_err(|e| e.to_string())?,
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
            read(&credentials) == Some(bytes("credentials"))
                && read(&claude_json) == Some(bytes("claude_json")),
            "a Claude Code login file changed",
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
    let program = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("device-packages")
        .join("store");
    let tool_program = || -> Option<PathBuf> {
        std::fs::read_dir(&program).ok()?.find_map(|e| {
            let dir = e.ok()?.path();
            let manifest: Value =
                serde_json::from_slice(&std::fs::read(dir.join("pluxy-extension.json")).ok()?)
                    .ok()?;
            (manifest["id"] == w).then(|| dir.join("bin").join("tool"))
        })
    };
    let tool_program = tool_program().ok_or("pluxy/claude-code is not in the store")?;

    // ---- key: native key entry, the window gets only the ID
    step(
        "key",
        (|| {
            let slot = start(main, "vault_secret_add", json!({"label": LABEL}))?;
            type_key(&key)?;
            let r = settle(main, &slot)?;
            seen.add(r.to_string());
            require(r["ok"] == true, format!("vault_secret_add: {r}"))?;
            secret = s(&r["value"]["id"]);
            require(secret.starts_with("s-"), format!("the new secret: {r}"))
        })(),
    )?;
    passed("key");

    // ---- unknown: settings Claude Code would not take are refused, the person's are read
    step(
        "unknown",
        (|| {
            read_refused(main, &seen, "preview", file(&unknown), "schema")?;
            read_refused(main, &seen, "preview", file(&broken), "JSON")?;
            require(
                read(&unknown) == Some(bytes("unknown")) && read(&broken) == Some(bytes("broken")),
                "a refused file changed",
            )?;
            // The positive control on the same route: the person's settings, the default file.
            let v = read_op(main, &seen, "preview", json!({}))?;
            require(
                v["targets"][0]["selected"] == json!(txt(&settings)) && v["applied"] == false,
                format!("the default preview: {v}"),
            )?;
            let st = &v["settings"];
            require(
                st["state"] == "not-configured"
                    && st["key_set"] == false
                    && st["startup_model"] == "sonnet",
                format!("the settings read back: {st}"),
            )?;
            require(
                read(&settings) == Some(bytes("original")),
                "a preview changed the settings",
            )
        })(),
    )?;
    passed("unknown");

    // ---- apply: the file holds exactly the expected bytes
    step(
        "apply",
        (|| {
            let v = read_op(main, &seen, "preview", file(&settings))?;
            let o = written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
                &[&txt(&settings), LABEL, SAME_INSTANT],
                &hidden,
            )?;
            require(o["outcome"] == "applied", format!("the apply: {o}"))?;
            require(
                read(&settings) == Some(bytes("applied")),
                format!("the applied settings: {:?}", read(&settings)),
            )?;
            require(mode(&settings) == 0o644, "the settings' mode changed")?;
            let v = read_op(main, &seen, "preview", file(&settings))?;
            let st = &v["settings"];
            let want = &x["settings_input"];
            require(
                v["applied"] == true
                    && st["state"] == "configured"
                    && st["key_set"] == true
                    && st["base_url"] == want["base_url"]
                    && st["opus"] == want["opus"]
                    && st["sonnet"] == want["sonnet"]
                    && st["sonnet_1m"] == true
                    && st["haiku"] == want["haiku"]
                    && st["startup_model"] == "opus"
                    && st["subagent_model"] == "haiku"
                    && st["max_context_tokens"] == 1_000_000
                    && st["auto_compact_pct"] == 80
                    && st["disable_auto_compact"] == false,
                format!("the applied settings read back: {v}"),
            )?;
            untouched()
        })(),
    )?;
    passed("apply");

    // ---- restore: the person's edit, then Restore Backup puts the original back
    step(
        "restore",
        (|| {
            let (from, to) = pair("edited_timeout");
            edit(&settings, &from, &to)?;
            let v = read_op(main, &seen, "preview", file(&settings))?;
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
                &[&txt(&settings)],
                &hidden,
            )?;
            require(o["outcome"] == "restored", format!("the restore: {o}"))?;
            require(
                read(&settings) == Some(bytes("original")) && mode(&settings) == 0o644,
                format!("the restored settings: {:?}", read(&settings)),
            )?;
            untouched()
        })(),
    )?;
    passed("restore");

    // ---- clear: applied again, edited twice, only the untouched values go
    step(
        "clear",
        (|| {
            let v = read_op(main, &seen, "preview", file(&settings))?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
                &[&txt(&settings), LABEL],
                &hidden,
            )?;
            require(
                read(&settings) == Some(bytes("applied")),
                "the second apply",
            )?;
            for k in ["edited_timeout", "edited_model"] {
                let (from, to) = pair(k);
                edit(&settings, &from, &to)?;
            }
            let v = read_op(main, &seen, "preview", file(&settings))?;
            let o = written(
                app,
                main,
                &seen,
                "clear",
                json!({"targets": v["targets"]}),
                &[&txt(&settings)],
                &hidden,
            )?;
            require(o["outcome"] == "cleared", format!("the clear: {o}"))?;
            require(
                read(&settings) == Some(bytes("cleared")),
                format!("the cleared settings: {:?}", read(&settings)),
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
            let tmp = stow_folder.join("settings.json.new");
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
            let sel = json!({"targets": [{"root": root_link, "path": "settings.json"}]});
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

    // ---- backups: the backup browser (backups_native.rs)
    step(
        "backups",
        super::backups::run(&super::backups::Ctx {
            app,
            main,
            seen: &seen,
            hidden: &hidden,
            x: &x,
            key: &key,
            secret: &secret,
        }),
    )?;
    passed("backups");

    // ---- launch: the recipe through the launcher; cancel starts nothing
    step(
        "launch",
        (|| {
            let recipe = read_op(main, &seen, "recipe", json!({}))?;
            require(
                recipe
                    == json!({"client": CLIENT, "kind": "terminal", "program": "claude", "args": []}),
                format!("the recipe: {recipe}"),
            )?;
            let launch = |extra: Value| {
                let mut l = recipe.clone();
                l["cwd"] = json!(work);
                l["terminal"] = json!("automatic");
                if let Some(m) = extra.as_object() {
                    for (k, v) in m {
                        l[k] = v.clone();
                    }
                }
                json!({ "launch": l })
            };
            let needles = [
                EXECUTION,
                "launch",
                &txt(&terminal),
                &txt(&claude),
                &txt(&work),
            ];
            // Cancelled: nothing starts.
            let slot = start(main, "launcher_launch", launch(json!({})))?;
            let text = dialog_with(app, &needles)?;
            seen.add(text);
            press("alt+c")?;
            let r = settle(main, &slot)?;
            seen.add(r.to_string());
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
            // Confirmed: the terminal runs claude in the folder, with no key.
            let slot = start(main, "launcher_launch", launch(json!({})))?;
            let text = dialog_with(app, &needles)?;
            seen.add(text);
            press("alt+o")?;
            let r = settle(main, &slot)?;
            seen.add(r.to_string());
            require(r["ok"] == true, format!("the confirmed launch: {r}"))?;
            let (argv, cwd, _) = stand_in(&records, "terminal")?;
            require(
                argv == vec!["-e".to_string(), txt(&claude)] && cwd == txt(&work),
                format!("the terminal ran with {argv:?} in {cwd}"),
            )?;
            let (argv, cwd, env) = stand_in(&records, "claude")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("claude ran with {argv:?} in {cwd}"),
            )?;
            require(
                !env.contains(&key) && !env.contains(&login),
                "claude's environment holds the key or the login canary",
            )?;
            require(
                !env.lines().any(|l| l.starts_with("ANTHROPIC_")),
                "claude's environment holds an Anthropic variable",
            )?;
            // The executable override runs instead of claude.
            let slot = start(
                main,
                "launcher_launch",
                launch(json!({"override": override_claude})),
            )?;
            let text = dialog_with(app, &[EXECUTION, "launch", &txt(&override_claude)])?;
            seen.add(text);
            press("alt+o")?;
            let r = settle(main, &slot)?;
            seen.add(r.to_string());
            require(r["ok"] == true, format!("the override launch: {r}"))?;
            let (argv, cwd, _) = stand_in(&records, "override")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("the override ran with {argv:?} in {cwd}"),
            )
        })(),
    )?;
    passed("launch");

    // ---- uninstall: no tool process is left, the files stay as they are
    step(
        "uninstall",
        (|| {
            let before = std::fs::read(&settings).map_err(|e| e.to_string())?;
            seen.add(ok(main, "device_enable", json!({"id": w, "enabled": false}))?.to_string());
            seen.add(ok(main, "device_remove", json!({"id": w}))?.to_string());
            wait_for("no pluxy/claude-code process once removed", || {
                processes(&tool_program).is_empty().then_some(())
            })?;
            require(
                std::fs::read(&settings).map_err(|e| e.to_string())? == before,
                "removing the package changed the settings",
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
            let mut left = leftovers(&home.join(".claude"))?;
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
    log("claude-code", out);
    Ok(())
}
