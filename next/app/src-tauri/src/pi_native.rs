//! The native test's check of the Pi setup and launcher, pluxy/pi-setup, through the shared
//! acceptance flow of an integration (native-test builds with `artifact-test-trust` only). It
//! drives the real app through the window's own commands, the real native key entry and
//! confirmations, with the packages and the files `native/pi.ts` made in this run's HOME (Pi's
//! agent folder as a person keeps it, a stow link, a chezmoi-style root, and folders Pi would not
//! take) and its stand-in terminal, `pi` and installed copies. Each refusal has a positive control
//! on the same route:
//!
//! - install: pluxy/pi-setup and pluxy/execution install from the curated registry and are enabled
//!   through their native confirmations, which say "full access to your user account";
//! - key: the key is typed into native key entry; the window gets only its ID;
//! - unknown: a preview of a folder whose packages is an object, or whose settings are not JSON,
//!   pins its files unasked, reports them refused with the reason and changes nothing; a preview of a folder whose adapter is not pluxy's
//!   succeeds, and a confirmed apply to it is refused and changes nothing; a preview of the
//!   person's files (a read, asked nothing) reads the state back, with no key;
//! - apply: asks a native confirmation naming the three files, the key's label and the same-instant
//!   sentence, never the key; then the files hold exactly the bytes `expect.json` says, owner only
//!   (0600: settings.json was 0644),
//!   the adapter folder holds exactly the bundle, and the preview reads the config back as
//!   configured;
//! - plugin_update: an installed adapter of an older version, with a file the bundle does not have,
//!   is updated by a confirmed update of the agent folder; the folder is exact again and no
//!   temporary entry is left beside it;
//! - plugin_uninstall: a confirmed uninstall removes pluxy's entry and the adapter folder, and
//!   leaves the cliproxyapi file as it was;
//! - restore: after the person's edit, Restore Backup puts the three originals back, the cache too;
//! - clear: applied again and edited by the person, Clear takes out only pluxy's values; their own
//!   entries and settings stay, as the expected bytes say;
//! - corrupt: applied again, then settings.json made into a file that is not JSON, then one of a
//!   shape Pi would not take, then cliproxyapi.json of a shape the plugin would not take: each
//!   preview reports it refused, a confirmed Clear and a confirmed Uninstall are refused and change
//!   nothing, and Restore Backup puts back the files as they were before that apply and removes the
//!   adapter folder;
//! - backups: a Manual Backup, a diff naming the setting the person changed and never its value,
//!   restore over that edit, delete;
//! - stow: an apply through the selected links changes only their targets (0640 becomes 0600), the
//!   links keep their inodes and text, and the adapter folder goes beside the links; once a target is replaced, a confirmed write fails as changed and writes
//!   nothing; a new preview's restore puts the originals back through the links;
//! - root: an apply to the selected root creates the files and the adapter folder, the root link
//!   kept; clear removes them;
//! - launch: the CLI recipe is the terminal running `pi`, and once `~/.local/bin/pi` and then
//!   `~/.pi/agent/bin/pi` (which comes first) hold a program, that one. Through the launcher, a
//!   cancelled confirmation starts nothing; confirmed ones run the stand-in terminal with exactly
//!   `-e <pi>` in the chosen folder, then `pi` with no arguments, then the installed one in each
//!   place; none has
//!   the key or the login canary in its environment;
//! - uninstall: disabled and removed, no pluxy/pi-setup process is left, and the files are as the
//!   last step left them;
//! - audit: the key and the login canary are in no window answer and no confirmation, no temporary
//!   file is left, and files no step names keep their bytes.
//!
//! The auth file is checked after every step. One line on stderr: `native-test: pi <JSON>`.

use super::admission::{ok, press, require, settle, start};
use super::claude_code::{edit, mode};
use super::program::processes;
use super::routing::{call, enable, install};
use super::transactions::{leftovers, link, read, snapshot, unasked};
use super::vault::{dialog_with, type_key};
use super::{log, wait_for};
use serde_json::{json, Value};
use std::cell::RefCell;
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::time::Duration;
use tauri::{AppHandle, Manager, Webview};

const PACKAGE: &str = "pluxy/pi-setup";
const EXECUTION: &str = "pluxy/execution";
const LABEL: &str = "fixture Pi key";
const CLIENT: &str = "Pi";
const SAME_INSTANT: &str = "an edit made in the same instant by another program may be lost";
/// Pi's adapter folder in the agent folder, and the marker pluxy writes in that folder.
const ADAPTER: &str = "pluxy-cliproxyapi-provider";
const MARKER: &str = ".pluxy-adapter.json";
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

/// The files under dir, by their path relative to it, with their text (none if dir is not there).
fn tree(dir: &Path) -> Result<BTreeMap<String, String>, String> {
    fn walk(base: &Path, dir: &Path, out: &mut BTreeMap<String, String>) -> Result<(), String> {
        for e in std::fs::read_dir(dir).map_err(|e| format!("{}: {e}", dir.display()))? {
            let path = e.map_err(|e| e.to_string())?.path();
            let m = std::fs::symlink_metadata(&path).map_err(|e| e.to_string())?;
            if m.is_dir() {
                walk(base, &path, out)?;
            } else {
                require(m.is_file(), format!("{} is not a file", path.display()))?;
                let text = read(&path).ok_or_else(|| format!("{} is not text", path.display()))?;
                let rel = path.strip_prefix(base).map_err(|e| e.to_string())?;
                out.insert(rel.display().to_string(), text);
            }
        }
        Ok(())
    }
    let mut out = BTreeMap::new();
    if dir.exists() {
        walk(dir, dir, &mut out)?;
    }
    Ok(out)
}

/// The files the bundle installs in the adapter folder: the bundle's files, then pluxy's marker
/// at the given text.
fn bundled(x: &Value, marker: &str) -> Result<BTreeMap<String, String>, String> {
    let mut files: BTreeMap<String, String> =
        serde_json::from_value(x["adapter_files"].clone()).map_err(|e| e.to_string())?;
    files.insert(MARKER.to_string(), marker.to_string());
    Ok(files)
}

/// The adapter folder holds exactly the files expect names, each with mode 0644.
fn adapter_is(dir: &Path, expect: &BTreeMap<String, String>) -> Result<(), String> {
    let got = tree(dir)?;
    require(
        got == *expect,
        format!(
            "the adapter folder {} holds {:?}",
            dir.display(),
            got.keys().collect::<Vec<_>>()
        ),
    )?;
    for name in expect.keys() {
        require(
            mode(&dir.join(name)) == 0o644,
            format!("{name} in the adapter folder is not mode 0644"),
        )?;
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
    let (agent, settings, cliproxy, cache, auth) = (
        p(&x["agent"]),
        p(&x["settings"]),
        p(&x["cliproxy"]),
        p(&x["cache"]),
        p(&x["auth"]),
    );
    let adapter = p(&x["adapter"]);
    let marker = s(&x["bytes"]["marker"]);
    let records = p(&x["records"]);
    let canon = |v: &Value| std::fs::canonicalize(p(v)).map_err(|e| e.to_string());
    let work = canon(&x["work"])?;
    let (terminal, pi_bin, installed) = (
        canon(&x["terminal"])?,
        canon(&x["pi"])?,
        canon(&x["installed"])?,
    );
    let (local_bin, agent_bin) = (p(&x["local_bin"]), p(&x["agent_bin"]));
    let (unknown, broken, foreign) = (
        p(&x["unknown"]),
        p(&x["broken"]),
        p(&x["foreign"]["folder"]),
    );
    let (foreign_settings, foreign_cliproxy, foreign_package) = (
        p(&x["foreign"]["settings"]),
        p(&x["foreign"]["cliproxy"]),
        p(&x["foreign"]["package"]),
    );
    let (stow, stow_settings, stow_cliproxy) = (
        p(&x["stow"]["folder"]),
        p(&x["stow"]["settings"]),
        p(&x["stow"]["cliproxy"]),
    );
    let (stow_settings_text, stow_cliproxy_text) = (
        s(&x["stow"]["settings_text"]),
        s(&x["stow"]["cliproxy_text"]),
    );
    let (stow_target_settings, stow_target_cliproxy, dotfiles) = (
        p(&x["stow"]["target_settings"]),
        p(&x["stow"]["target_cliproxy"]),
        p(&x["stow"]["target_folder"]),
    );
    let (root_link, root_folder) = (p(&x["root"]["link"]), p(&x["root"]["folder"]));
    let fill = |v: &Value| s(v).replace("@KEY@", &key);
    let bytes = |k: &str| fill(&x["bytes"][k]);
    let edit_pair = |k: &str| (s(&x["edits"][k][0]), s(&x["edits"][k][1]));
    let txt = |q: &Path| q.display().to_string();
    let hidden = [key.as_str(), login.as_str()];
    let seen = Seen::default();
    let mut secret = String::new();
    let mut out = json!({"steps": []});
    let mut passed = |name: &str| out["steps"].as_array_mut().unwrap().push(json!(name));
    // The auth file is never changed, after every step.
    let untouched = || -> Result<(), String> {
        require(read(&auth) == Some(bytes("auth")), "auth.json changed")
    };
    let step = |name: &str, r: Result<(), String>| {
        r.and_then(|()| untouched())
            .map_err(|e| format!("{name}: {e}"))
    };
    // The input of an apply: the window's values, the pins of a preview and the key's ID.
    let apply_input = |preview: &Value, secret: &str| {
        let mut i = x["settings_input"].clone();
        i["targets"] = preview["targets"].clone();
        i["key"] = json!(secret);
        i
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
    let tool_program = tool_program().ok_or("pluxy/pi-setup is not in the store")?;

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

    // ---- unknown: folders Pi would not take are refused, a foreign adapter is refused on apply
    step(
        "unknown",
        (|| {
            // Files Pi would not take are pinned all the same and reported refused, with why.
            for (q, says) in [(&unknown, "packages"), (&broken, "JSON")] {
                let v = read_op(main, &seen, "preview", json!({"folder": txt(q)}))?;
                let c = &v["pi"];
                require(
                    v["targets"][1]["target"] == json!(txt(&q.join("settings.json")))
                        && c["state"] == "refused"
                        && c["reason"].as_str().is_some_and(|r| r.contains(says)),
                    format!("the preview of {} should report it refused: {v}", txt(q)),
                )?;
            }
            // A folder whose adapter is not pluxy's previews, and a confirmed apply to it is refused.
            let fv = read_op(main, &seen, "preview", json!({"folder": txt(&foreign)}))?;
            require(
                fv["pi"]["adapter"]["foreign"] == true,
                format!("the foreign preview: {fv}"),
            )?;
            let before = snapshot(&foreign)?;
            let r = confirmed(
                app,
                main,
                &seen,
                "apply",
                apply_input(&fv, &secret),
                &[&txt(&foreign_cliproxy), &txt(&foreign_settings), LABEL],
                &hidden,
            )?;
            require(
                r["ok"] == false
                    && r["error"]
                        .as_str()
                        .is_some_and(|e| e.contains("not pluxy's")),
                format!("a confirmed apply to a foreign folder should be refused: {r}"),
            )?;
            require(
                snapshot(&foreign)? == before,
                "the refused apply to a foreign folder changed a file",
            )?;
            require(
                read(&foreign_settings) == Some(bytes("foreign_settings"))
                    && read(&foreign_package) == Some(bytes("foreign_package")),
                "a foreign folder's file changed",
            )?;
            // The positive control on the same route: the person's files, the default state.
            let v = read_op(main, &seen, "preview", json!({}))?;
            let c = &v["pi"];
            require(
                v["applied"] == false
                    && v["targets"][0]["selected"] == json!(txt(&cliproxy))
                    && v["targets"][1]["selected"] == json!(txt(&settings))
                    && v["targets"][2]["selected"] == json!(txt(&cache))
                    && c["state"] == "not-configured"
                    && c["key_set"] == false
                    && c["base_url"] == "http://127.0.0.1:8317"
                    && c["model"] == "claude-sonnet-5-5"
                    && c["default_provider"] == "anthropic"
                    && c["provider"] == "cliproxyapi"
                    && c["registered"] == false
                    && c["legacy"] == true
                    && c["adapter"]["installed"] == false
                    && c["adapter"]["version"].is_null()
                    && c["adapter"]["bundled"] == "1.4.25-pluxy.1",
                format!("the default preview: {v}"),
            )?;
            require(
                read(&settings) == Some(bytes("settings_original"))
                    && read(&cliproxy) == Some(bytes("cliproxy_original"))
                    && read(&cache) == Some(bytes("cache_original")),
                "a preview changed the files",
            )?;
            require(
                read(&unknown.join("settings.json")) == Some(bytes("unknown"))
                    && read(&broken.join("settings.json")) == Some(bytes("broken")),
                "a refused folder changed",
            )?;
            Ok(())
        })(),
    )?;
    passed("unknown");

    // ---- apply: the three files hold exactly the expected bytes, and the adapter folder
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
                    &txt(&cliproxy),
                    &txt(&settings),
                    &txt(&cache),
                    LABEL,
                    SAME_INSTANT,
                ],
                &hidden,
            )?;
            require(o["outcome"] == "applied", format!("the apply: {o}"))?;
            require(
                read(&cliproxy) == Some(bytes("cliproxy_applied"))
                    && mode(&cliproxy) == 0o600
                    && read(&settings) == Some(bytes("settings_applied"))
                    && mode(&settings) == 0o600
                    && !cache.exists(),
                format!(
                    "the applied files: {:?} {:?} cache present: {}",
                    read(&cliproxy),
                    read(&settings),
                    cache.exists()
                ),
            )?;
            adapter_is(&adapter, &bundled(&x, &marker)?)?;
            let v = read_op(
                main,
                &seen,
                "preview",
                json!({"base_url": x["settings_input"]["base_url"]}),
            )?;
            let c = &v["pi"];
            require(
                v["applied"] == true
                    && c["state"] == "configured"
                    && c["key_set"] == true
                    && c["base_url"] == "http://127.0.0.1:41999"
                    && c["model"] == "gpt-6.1-sol"
                    && c["default_provider"] == "cliproxyapi"
                    && c["registered"] == true
                    && c["legacy"] == false
                    && c["adapter"]["installed"] == true
                    && c["adapter"]["version"] == "1.4.25-pluxy.1"
                    && c["adapter"]["update"] == false,
                format!("the applied config read back: {v}"),
            )?;
            Ok(())
        })(),
    )?;
    passed("apply");

    // ---- plugin_update: an older adapter, with a file the bundle does not have, is updated
    step(
        "plugin_update",
        (|| {
            std::fs::write(adapter.join(MARKER), bytes("marker_stale"))
                .map_err(|e| e.to_string())?;
            std::fs::write(adapter.join("extensions").join("old.ts"), bytes("stale"))
                .map_err(|e| e.to_string())?;
            let v = read_op(main, &seen, "preview", json!({}))?;
            require(
                v["pi"]["adapter"]["version"] == "1.4.24-pluxy.1"
                    && v["pi"]["adapter"]["update"] == true,
                format!("the stale adapter's preview: {v}"),
            )?;
            written(
                app,
                main,
                &seen,
                "update",
                json!({"folder": txt(&agent)}),
                &[&txt(&agent)],
                &hidden,
            )?;
            adapter_is(&adapter, &bundled(&x, &marker)?)?;
            let stray = std::fs::read_dir(&agent)
                .map_err(|e| e.to_string())?
                .filter_map(Result::ok)
                .any(|e| {
                    e.file_name()
                        .to_string_lossy()
                        .starts_with(".pluxy-cliproxyapi-provider")
                });
            require(
                !stray,
                "the update left a .pluxy-cliproxyapi-provider entry in the agent folder",
            )?;
            require(
                read(&settings) == Some(bytes("settings_applied"))
                    && read(&cliproxy) == Some(bytes("cliproxy_applied"))
                    && !cache.exists(),
                "the update changed a config file",
            )?;
            Ok(())
        })(),
    )?;
    passed("plugin_update");

    // ---- plugin_uninstall: pluxy's entry and the adapter folder go, the cliproxyapi file stays
    step(
        "plugin_uninstall",
        (|| {
            let v = read_op(main, &seen, "preview", json!({}))?;
            written(
                app,
                main,
                &seen,
                "uninstall",
                json!({"targets": v["targets"]}),
                &[],
                &hidden,
            )?;
            require(
                read(&settings) == Some(bytes("settings_uninstalled"))
                    && read(&cliproxy) == Some(bytes("cliproxy_applied"))
                    && !adapter.exists(),
                format!(
                    "the uninstalled files: {:?} adapter present: {}",
                    read(&settings),
                    adapter.exists()
                ),
            )?;
            let v = read_op(main, &seen, "preview", json!({}))?;
            require(
                v["pi"]["registered"] == false
                    && v["pi"]["adapter"]["installed"] == false
                    && v["pi"]["state"] == "not-configured",
                format!("the preview after uninstall: {v}"),
            )?;
            Ok(())
        })(),
    )?;
    passed("plugin_uninstall");

    // ---- restore: the person's edit, then Restore Backup puts the three originals back
    step(
        "restore",
        (|| {
            let v = read_op(main, &seen, "preview", json!({}))?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
                &[&txt(&cliproxy), &txt(&settings), &txt(&cache), LABEL],
                &hidden,
            )?;
            require(
                read(&settings) == Some(bytes("settings_applied"))
                    && read(&cliproxy) == Some(bytes("cliproxy_applied"))
                    && !cache.exists(),
                "the apply again",
            )?;
            adapter_is(&adapter, &bundled(&x, &marker)?)?;
            let (from, to) = edit_pair("theme");
            edit(&settings, &from, &to)?;
            let v = read_op(main, &seen, "preview", json!({}))?;
            require(
                v["edited"] == json!([false, true, false]),
                format!("the edit is not seen: {v}"),
            )?;
            let o = written(
                app,
                main,
                &seen,
                "restore",
                json!({"targets": v["targets"]}),
                &[&txt(&cliproxy), &txt(&settings), &txt(&cache)],
                &hidden,
            )?;
            require(o["outcome"] == "restored", format!("the restore: {o}"))?;
            require(
                read(&settings) == Some(bytes("settings_original"))
                    && read(&cliproxy) == Some(bytes("cliproxy_original"))
                    && read(&cache) == Some(bytes("cache_original"))
                    && mode(&cliproxy) == 0o600
                    && mode(&settings) == 0o600
                    && !adapter.exists(),
                format!(
                    "the restored files: {:?} {:?} {:?}",
                    read(&settings),
                    read(&cliproxy),
                    read(&cache)
                ),
            )?;
            Ok(())
        })(),
    )?;
    passed("restore");

    // ---- clear: applied, edited, cleared; only pluxy's values go
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
                &[&txt(&cliproxy), &txt(&settings), &txt(&cache), LABEL],
                &hidden,
            )?;
            require(
                read(&settings) == Some(bytes("settings_applied"))
                    && read(&cliproxy) == Some(bytes("cliproxy_applied"))
                    && !cache.exists(),
                "the second apply",
            )?;
            adapter_is(&adapter, &bundled(&x, &marker)?)?;
            let (from, to) = edit_pair("quiet");
            edit(&settings, &from, &to)?;
            let (from, to) = edit_pair("mcp");
            edit(&settings, &from, &to)?;
            let v = read_op(main, &seen, "preview", json!({}))?;
            let o = written(
                app,
                main,
                &seen,
                "clear",
                json!({"targets": v["targets"]}),
                &[&txt(&cliproxy), &txt(&settings), &txt(&cache)],
                &hidden,
            )?;
            require(o["outcome"] == "cleared", format!("the clear: {o}"))?;
            require(
                read(&settings) == Some(bytes("settings_cleared"))
                    && read(&cliproxy) == Some(bytes("cliproxy_original"))
                    && read(&cache) == Some(bytes("cache_original"))
                    && !adapter.exists(),
                format!(
                    "the cleared files: {:?} {:?} {:?}",
                    read(&settings),
                    read(&cliproxy),
                    read(&cache)
                ),
            )?;
            Ok(())
        })(),
    )?;
    passed("clear");

    // ---- corrupt: applied files Pi or the plugin would not take (settings.json not JSON, of the
    // wrong shape, or cliproxyapi.json of the wrong shape); Clear and Uninstall are refused and
    // change nothing, Restore Backup still puts the files back
    step(
        "corrupt",
        (|| {
            for (file, pair, says) in [
                (&settings, "corrupt", "JSON"),
                (&settings, "model_shape", "defaultModel"),
                (&cliproxy, "provider_shape", "providerId"),
            ] {
                let v = read_op(main, &seen, "preview", json!({}))?;
                written(
                    app,
                    main,
                    &seen,
                    "apply",
                    apply_input(&v, &secret),
                    &[&txt(&cliproxy), &txt(&settings), &txt(&cache), LABEL],
                    &hidden,
                )?;
                adapter_is(&adapter, &bundled(&x, &marker)?)?;
                let (from, to) = edit_pair(pair);
                edit(file, &from, &to)?;
                let before = (read(&settings), read(&cliproxy));
                let v = read_op(main, &seen, "preview", json!({}))?;
                let c = &v["pi"];
                require(
                    v["applied"] == true
                        && c["state"] == "refused"
                        && c["reason"].as_str().is_some_and(|r| r.contains(says))
                        && c["adapter"]["installed"] == true,
                    format!("the {pair} preview: {v}"),
                )?;
                for op in ["clear", "uninstall"] {
                    let r = confirmed(
                        app,
                        main,
                        &seen,
                        op,
                        json!({"targets": v["targets"]}),
                        &[&txt(file)],
                        &hidden,
                    )?;
                    require(
                        r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains(says)),
                        format!("a {op} of the {pair} files should be refused: {r}"),
                    )?;
                    require(
                        (read(&settings), read(&cliproxy)) == before && adapter.exists(),
                        format!("the refused {op} of the {pair} files changed them"),
                    )?;
                }
                let o = written(
                    app,
                    main,
                    &seen,
                    "restore",
                    json!({"targets": v["targets"]}),
                    &[&txt(file)],
                    &hidden,
                )?;
                require(o["outcome"] == "restored", format!("the restore: {o}"))?;
                require(
                    read(&settings) == Some(bytes("settings_cleared"))
                        && read(&cliproxy) == Some(bytes("cliproxy_original"))
                        && read(&cache) == Some(bytes("cache_original"))
                        && !adapter.exists(),
                    format!(
                        "the {pair} files restored: {:?} {:?} {:?}",
                        read(&settings),
                        read(&cliproxy),
                        read(&cache)
                    ),
                )?;
            }
            Ok(())
        })(),
    )?;
    passed("corrupt");

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
                &[&txt(&cliproxy), &txt(&settings), &txt(&cache)],
                &hidden,
            )?;
            let id = s(&b["backup"]);
            let list = read_op(main, &seen, "backup-list", json!({}))?;
            require(
                list["backups"].as_array().map(Vec::len) == Some(1)
                    && list["backups"][0]["id"] == json!(id),
                format!("the backup list: {list}"),
            )?;
            let (from, to) = edit_pair("backup");
            edit(&settings, &from, &to)?;
            let d = read_op(main, &seen, "backup-diff", json!({"backup": id}))?;
            require(
                d["files"][1]["changes"] == x["backups"]["diff"]
                    && d["files"][0]["changes"] == json!([])
                    && d["files"][2]["changes"] == json!([]),
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
                &[&txt(&cliproxy), &txt(&settings), &txt(&cache)],
                &hidden,
            )?;
            require(
                read(&settings) == Some(bytes("settings_cleared"))
                    && read(&cliproxy) == Some(bytes("cliproxy_original"))
                    && read(&cache) == Some(bytes("cache_original")),
                format!(
                    "the restored backup: {:?} {:?} {:?}",
                    read(&settings),
                    read(&cliproxy),
                    read(&cache)
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
            Ok(())
        })(),
    )?;
    passed("backups");

    // ---- stow: only the links' targets change; a replaced target refuses writes
    step(
        "stow",
        (|| {
            // The links themselves are selected (a link inside a selected folder is refused).
            let stow_sel = json!({"targets": [
                {"file": txt(&stow_cliproxy)},
                {"file": txt(&stow_settings)},
                {"file": txt(&stow.join("cliproxyapi-models.json"))},
            ]});
            let (ino_settings, _) = link(&stow_settings)?;
            let (ino_cliproxy, _) = link(&stow_cliproxy)?;
            let v = read_op(main, &seen, "preview", stow_sel.clone())?;
            require(
                v["targets"][0]["target"] == json!(txt(&stow_target_cliproxy))
                    && v["targets"][0]["link"] == json!(stow_cliproxy_text)
                    && v["targets"][1]["target"] == json!(txt(&stow_target_settings))
                    && v["targets"][1]["link"] == json!(stow_settings_text),
                format!("the stow preview: {v}"),
            )?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
                &[&txt(&stow_settings), &txt(&stow_target_settings)],
                &hidden,
            )?;
            require(
                read(&stow_target_settings) == Some(bytes("stow_settings_applied"))
                    && mode(&stow_target_settings) == 0o600
                    && read(&stow_target_cliproxy) == Some(bytes("stow_cliproxy_applied")),
                format!(
                    "the stow targets after apply: {:?} {:?}",
                    read(&stow_target_settings),
                    read(&stow_target_cliproxy)
                ),
            )?;
            require(
                link(&stow_settings)? == (ino_settings, PathBuf::from(&stow_settings_text))
                    && link(&stow_cliproxy)? == (ino_cliproxy, PathBuf::from(&stow_cliproxy_text)),
                "a stow link is not the same link with the same text",
            )?;
            require(
                read(&dotfiles.join("keep.json")) == Some(bytes("keep")),
                "a file beside the stow targets changed",
            )?;
            // The adapter folder is in the stow folder, not among the targets.
            adapter_is(&stow.join(ADAPTER), &bundled(&x, &marker)?)?;
            require(
                !dotfiles.join(ADAPTER).exists(),
                "the stow apply put an adapter folder among the targets",
            )?;
            // The target replaced (as an editor saves, by rename) after a preview.
            let v = read_op(main, &seen, "preview", stow_sel.clone())?;
            let replaced = read(&stow_target_settings).unwrap_or_default();
            let tmp = dotfiles.join("settings.json.new");
            std::fs::write(&tmp, &replaced).map_err(|e| e.to_string())?;
            std::fs::rename(&tmp, &stow_target_settings).map_err(|e| e.to_string())?;
            let before = snapshot(&home.join("dotfiles"))?;
            let r = confirmed(
                app,
                main,
                &seen,
                "restore",
                json!({"targets": v["targets"]}),
                &[&txt(&stow_settings)],
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
            let v = read_op(main, &seen, "preview", stow_sel.clone())?;
            written(
                app,
                main,
                &seen,
                "restore",
                json!({"targets": v["targets"]}),
                &[&txt(&stow_settings)],
                &hidden,
            )?;
            require(
                read(&stow_target_settings) == Some(bytes("stow_settings"))
                    && read(&stow_target_cliproxy) == Some(bytes("stow_cliproxy"))
                    && !stow.join(ADAPTER).exists(),
                format!(
                    "the stow targets after restore: {:?} {:?}",
                    read(&stow_target_settings),
                    read(&stow_target_cliproxy)
                ),
            )?;
            require(
                link(&stow_settings)? == (ino_settings, PathBuf::from(&stow_settings_text))
                    && link(&stow_cliproxy)? == (ino_cliproxy, PathBuf::from(&stow_cliproxy_text)),
                "a stow link changed",
            )?;
            Ok(())
        })(),
    )?;
    passed("stow");

    // ---- root: files inside the selected root are created, then cleared away
    step(
        "root",
        (|| {
            let (inode, _) = link(&root_link)?;
            let sel = json!({"folder": txt(&root_link)});
            let v = read_op(main, &seen, "preview", sel.clone())?;
            written(
                app,
                main,
                &seen,
                "apply",
                apply_input(&v, &secret),
                &[&txt(&root_link), &txt(&root_folder.join("settings.json"))],
                &hidden,
            )?;
            require(
                read(&root_folder.join("settings.json")) == Some(bytes("root_settings"))
                    && read(&root_folder.join("cliproxyapi.json")) == Some(bytes("root_cliproxy"))
                    && !root_folder.join("cliproxyapi-models.json").exists(),
                format!(
                    "the root's files after apply: {:?} {:?}",
                    read(&root_folder.join("settings.json")),
                    read(&root_folder.join("cliproxyapi.json"))
                ),
            )?;
            adapter_is(&root_folder.join(ADAPTER), &bundled(&x, &marker)?)?;
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
                std::fs::read_dir(&root_folder)
                    .map_err(|e| e.to_string())?
                    .next()
                    .is_none(),
                "clear left a file in the root folder",
            )?;
            require(
                link(&root_link)? == (inode, root_folder.clone()),
                "the root link changed",
            )?;
            Ok(())
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
            // The CLI: the terminal running pi found on the PATH.
            let recipe = read_op(main, &seen, "recipe", json!({}))?;
            require(
                recipe
                    == json!({"client": CLIENT, "kind": "terminal", "program": "pi",
                        "args": []}),
                format!("the recipe: {recipe}"),
            )?;
            let needles = [
                EXECUTION,
                "launch",
                &txt(&terminal),
                &txt(&pi_bin),
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
            // Confirmed: the terminal runs pi in the folder, with no key.
            let r = launch(&recipe, in_work.clone(), &needles, false)?;
            require(r["ok"] == true, format!("the confirmed launch: {r}"))?;
            let (argv, cwd, _) = stand_in(&records, "terminal")?;
            require(
                argv == vec!["-e".to_string(), txt(&pi_bin)] && cwd == txt(&work),
                format!("the terminal ran with {argv:?} in {cwd}"),
            )?;
            let (argv, cwd, env) = stand_in(&records, "pi")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("pi ran with {argv:?} in {cwd}"),
            )?;
            clean(&env, "pi")?;
            // The place of the install script holds a program: the recipe names it instead.
            clear_records(&records)?;
            place(&installed, &local_bin)?;
            let recipe = read_op(main, &seen, "recipe", json!({}))?;
            require(
                recipe["program"] == json!(txt(&local_bin)),
                format!("the recipe with pi in ~/.local/bin: {recipe}"),
            )?;
            let r = launch(
                &recipe,
                in_work.clone(),
                &[EXECUTION, "launch", &txt(&local_bin)],
                false,
            )?;
            require(r["ok"] == true, format!("the installed pi's launch: {r}"))?;
            let (argv, cwd, env) = stand_in(&records, "installed")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("the installed pi ran with {argv:?} in {cwd}"),
            )?;
            clean(&env, "the installed pi")?;
            // Pi's own bin folder comes first once it holds a program.
            place(&installed, &agent_bin)?;
            let recipe = read_op(main, &seen, "recipe", json!({}))?;
            require(
                recipe["program"] == json!(txt(&agent_bin)),
                format!("the recipe with pi in the agent folder's bin: {recipe}"),
            )?;
            clear_records(&records)?;
            let r = launch(
                &recipe,
                in_work.clone(),
                &[EXECUTION, "launch", &txt(&agent_bin)],
                false,
            )?;
            require(
                r["ok"] == true,
                format!("the agent folder's pi's launch: {r}"),
            )?;
            let (argv, cwd, env) = stand_in(&records, "installed")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("the agent folder's pi ran with {argv:?} in {cwd}"),
            )?;
            clean(&env, "the agent folder's pi")?;
            Ok(())
        })(),
    )?;
    passed("launch");

    // ---- uninstall: no tool process is left, the files stay as they are
    step(
        "uninstall",
        (|| {
            let before = (read(&settings), read(&cliproxy), read(&cache));
            seen.add(ok(main, "device_enable", json!({"id": w, "enabled": false}))?.to_string());
            seen.add(ok(main, "device_remove", json!({"id": w}))?.to_string());
            wait_for("no pluxy/pi-setup process once removed", || {
                processes(&tool_program).is_empty().then_some(())
            })?;
            require(
                (read(&settings), read(&cliproxy), read(&cache)) == before,
                "removing the package changed a file",
            )?;
            Ok(())
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
            // The adapter's marker is pluxy's own file, not a temporary one.
            let mut left = leftovers(&agent)?;
            left.extend(leftovers(&home.join("dotfiles"))?);
            left.extend(leftovers(&home.join(".local/share/chezmoi"))?);
            left.retain(|p| !p.ends_with(&format!("/{ADAPTER}/{MARKER}")));
            require(left.is_empty(), format!("temporary files left: {left:?}"))?;
            require(
                read(&unknown.join("settings.json")) == Some(bytes("unknown"))
                    && read(&broken.join("settings.json")) == Some(bytes("broken"))
                    && read(&foreign_settings) == Some(bytes("foreign_settings"))
                    && read(&foreign_package) == Some(bytes("foreign_package")),
                "a refused folder changed",
            )?;
            Ok(())
        })(),
    )?;
    passed("audit");

    out["passed"] = json!(true);
    log("pi", out);
    Ok(())
}
