//! The native test's check that a launch's client key is checked as a delivery is: before the
//! confirmation, and again at the spawn and at each restart, where the host turns the launch's
//! reference into a value (native-test builds with `artifact-test-trust` only). It runs in the
//! spawn check's session, after it (`spawn_native.rs`), with its signed tools (`native/spawn.ts`)
//! and the app's own core, through the window's own commands, the real native confirmations and
//! native client-key capture. The keys core makes are read from that core's config file, the
//! independent record, and noted for the runner's audit (`client-keys`). Each refusal has a
//! positive control on the same route:
//!
//! - capture: three client keys for pluxy/launcher and one for pluxy/other;
//! - launch: the launcher's managed launch with its first key in the environment runs with it;
//! - launch stdin: its detached launch with that key on its stdin reads it there, and only there;
//! - restart deleted: once that key is deleted on core, the runtime's restart is refused and it
//!   does not start again;
//! - launch deleted: a launch with the deleted key, in its environment or on its stdin, is
//!   refused before any confirmation;
//! - spawn deleted: a detached launch confirmed with a key core still takes, held before its
//!   spawn frame while that key is deleted on core, starts nothing;
//! - other core: a managed runtime with the second key restarts with it; with the app connected
//!   to another core (a listener of this check's), its restart is refused, and a managed launch
//!   confirmed before the switch and held before its spawn frame starts nothing, and nothing
//!   reaches that core (each of these is checked, and every failure reported); connected to the
//!   app's own core again, the restart starts it with the key;
//! - recipient: pluxy/other passes its reference to a plain vault secret on to the launcher's start
//!   and, once the import is confirmed, the runtime gets it; its reference to its own client key
//!   is refused before the import's confirmation, and nothing starts;
//! - audit: no key reached the window's webview.
//!
//! One line on stderr: `native-test: spawn_keys <JSON>`.

use super::admission::{ok, press, require, settle, start};
use super::client_key::{core_keys, new_key, note, refused_unasked, Listener};
use super::key_entry_test::{clean_webview, watch};
use super::routing::call;
use super::spawn::{ended, launch, owned, record, runtime, AGENT, LABEL, LAUNCHER, OTHER};
use super::vault::{dialog_with, settle_unasked};
use super::{log, wait_for};
use crate::App;
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::time::Duration;
use tauri::{AppHandle, Manager, Webview};

/// A client key the check captured: its vault secret's ID and label, core's ID of it, its value.
struct Key {
    secret: String,
    label: String,
    id: String,
    value: String,
}

/// Captures a client key for client as label (no confirmation of its own: its use asks), and
/// reads its value from core's config.
fn capture(
    main: &Webview,
    (_base, config, audit): (&str, &Path, &Path),
    client: &str,
    label: &str,
) -> Result<Key, String> {
    let before = core_keys(config)?;
    let slot = start(
        main,
        "client_key_capture",
        json!({"label": label, "client": client}),
    )?;
    let r = settle_unasked(main, &slot)?;
    let value = new_key(&before, &core_keys(config)?)?;
    note(audit, &value)?;
    let v = &r["value"];
    require(
        r["ok"] == true
            && v["client_key"]["client"] == client
            && v["client_key"]["key_id"]
                .as_str()
                .is_some_and(|i| !i.is_empty())
            && !r.to_string().contains(&value),
        format!("the capture of a client key for {client}: {r}"),
    )?;
    Ok(Key {
        secret: v["id"].as_str().unwrap_or_default().into(),
        label: label.into(),
        id: v["client_key"]["key_id"]
            .as_str()
            .unwrap_or_default()
            .into(),
        value,
    })
}

/// Deletes key on core, through the app's management connection.
fn delete(main: &Webview, config: &Path, key: &Key) -> Result<(), String> {
    let d = ok(
        main,
        "core_call",
        json!({"method": "DELETE", "path": format!("/client-keys/{}", key.id)}),
    )?;
    require(d["status"] == 204, format!("deleting the key on core: {d}"))?;
    require(
        !core_keys(config)?.contains(&key.value),
        "core still has the deleted key",
    )
}

/// The launcher's start input: the agent, writing its record to rec, with secret in `key` given
/// to it as FIXTURE_KEY.
fn input(agent: &str, work: &Path, rec: &Path, lifetime: &str, secret: &str) -> Value {
    json!({
        "executable": agent, "argv": [AGENT, rec], "cwd": work, "lifetime": lifetime,
        "key": secret, "environment": {"secrets": {"FIXTURE_KEY": "key"}},
    })
}

/// input with its secret given on the agent's stdin, which it reads, instead of in its
/// environment.
fn on_stdin(mut input: Value) -> Value {
    if let Some(o) = input.as_object_mut() {
        o.insert(
            "environment".into(),
            json!({"set": {"FIXTURE_READ_STDIN": "1"}}),
        );
        o.insert("stdin".into(), json!("key"));
    }
    input
}

/// Ends the agent whose record (r, at rec) says it started, while it still runs as that agent.
fn end(r: &Value, rec: &Path) {
    let marker = rec.to_string_lossy().into_owned();
    for k in ["pid", "child"] {
        let pid = r[k].as_u64().unwrap_or(0);
        if owned(pid, &marker) {
            unsafe { libc::kill(pid as libc::pid_t, libc::SIGKILL) };
        }
    }
}

/// Ends what an agent that should not have started left, and says so.
fn started_anyway(rec: &Path, what: &str) -> Result<(), String> {
    let Some(r) = std::fs::read(rec)
        .ok()
        .and_then(|b| serde_json::from_slice::<Value>(&b).ok())
    else {
        return Ok(());
    };
    end(&r, rec);
    let leaked = r["env"]["FIXTURE_KEY"].as_str().is_some();
    Err(format!(
        "{what} started anyway{}",
        if leaked { ", with the key" } else { "" }
    ))
}

/// The launcher's start from the window with input, its confirmation (naming needles) accepted,
/// held before its spawn frame (`hold`): the call's slot and the file that lets the spawn go.
fn held(
    app: &AppHandle,
    main: &Webview,
    mut input: Value,
    hold: &Path,
    needles: &[&str],
) -> Result<(String, PathBuf), String> {
    input["hold"] = json!(hold);
    let slot = start(
        main,
        "program_call",
        json!({"package": LAUNCHER, "op": "start", "input": input}),
    )?;
    dialog_with(app, needles)?;
    press("alt+o")?;
    let ready = PathBuf::from(format!("{}.ready", hold.display()));
    wait_for("the launcher holding its spawn", || {
        ready.exists().then_some(())
    })?;
    Ok((slot, hold.to_path_buf()))
}

/// The host's answer to the launcher's spawn of the call in slot, once the spawn is let go.
fn released(main: &Webview, slot: &str, hold: &Path) -> Result<Value, String> {
    std::fs::write(hold, "").map_err(|e| e.to_string())?;
    let r = settle(main, slot)?;
    require(r["ok"] == true, format!("the held launch: {r}"))?;
    Ok(r["value"]["answer"].clone())
}

/// The restart of runtime id is refused, saying says; its old process (rec's, before) ended and
/// nothing started again.
fn restart_refused(
    main: &Webview,
    id: &str,
    rec: &Path,
    before: &Value,
    says: &str,
) -> Result<(), String> {
    let r = call(
        main,
        LAUNCHER,
        "runtime",
        json!({"op": "restart", "runtime": id}),
    )?;
    let answer = &r["value"]["answer"];
    let message = answer["error"]["message"].as_str().unwrap_or_default();
    std::thread::sleep(Duration::from_millis(500));
    let now = std::fs::read(rec)
        .ok()
        .and_then(|b| serde_json::from_slice::<Value>(&b).ok())
        .unwrap_or(Value::Null);
    let again = now["pid"] != before["pid"];
    require(
        r["ok"] == true && message.contains(says) && !again,
        format!(
            "the restart should be refused with {says:?}{}: {answer}",
            if again && now["env"]["FIXTURE_KEY"].is_string() {
                "; it started again, with the key"
            } else {
                ""
            }
        ),
    )?;
    let marker = rec.to_string_lossy().into_owned();
    let pid = before["pid"].as_u64().unwrap_or(0);
    require(
        ended(pid, &marker, Duration::from_secs(10)),
        "the refused restart left the old runtime running",
    )?;
    let status = runtime(main, LAUNCHER, "status", id)?;
    require(
        status["output"]["state"] == "exited",
        format!("the runtime after its refused restart: {status}"),
    )
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let work = fixtures.join("keys-work");
    std::fs::create_dir_all(&work).map_err(|e| e.to_string())?;
    let work = std::fs::canonicalize(&work).map_err(|e| e.to_string())?;
    let at = |name: &str| work.join(name);
    let audit = fixtures.join("client-keys");
    let config = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("core")
        .join("config.json");
    let agent = fixtures.join("agent").to_string_lossy().into_owned();
    let view = ok(main, "connection_get", json!({}))?;
    let base = view["target"].as_str().unwrap_or_default().to_string();
    require(
        view["mode"] == "managed" && base.starts_with("http://127.0.0.1:"),
        format!("the connection: {view}"),
    )?;
    watch(main)?;
    let core = (base.as_str(), config.as_path(), audit.as_path());
    let mut out = json!({});
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("spawn keys {name}: {e}"));
    // The refusals are independent of each other: each is checked, and every failure reported.
    let mut failed: Vec<String> = Vec::new();
    let mut each = |name: &str, r: Result<(), String>| {
        if let Err(e) = step(name, r) {
            failed.push(e);
        }
    };

    // ---- capture
    let mut keys: Vec<Key> = Vec::new();
    step(
        "capture",
        (|| {
            for (client, label) in [
                (LAUNCHER, "launcher key one"),
                (LAUNCHER, "launcher key two"),
                (LAUNCHER, "launcher key three"),
                (OTHER, "other key"),
            ] {
                keys.push(capture(main, core, client, label)?);
            }
            Ok(())
        })(),
    )?;
    let (one, two, three, other_key) = (&keys[0], &keys[1], &keys[2], &keys[3]);
    let values: Vec<&str> = keys.iter().map(|k| k.value.as_str()).collect();

    // ---- launch: the positive control of the refusals below
    let rec_one = at("one");
    let mut first = (String::new(), Value::Null);
    step(
        "launch",
        (|| {
            let (v, _) = launch(
                app,
                main,
                input(&agent, &work, &rec_one, "managed", &one.secret),
                &["pluxy/launcher start", "managed", "FIXTURE_KEY", &one.label],
                &values,
            )?;
            let id = v["answer"]["output"]["runtime"]
                .as_str()
                .unwrap_or_default();
            require(
                id.starts_with("rt-"),
                format!("the managed launch's answer: {v}"),
            )?;
            let r = record(&rec_one, 0)?;
            require(
                r["env"]["FIXTURE_KEY"] == one.value.as_str(),
                "the runtime did not get its client key",
            )?;
            first = (id.to_string(), r);
            Ok(())
        })(),
    )?;

    // ---- launch stdin: the positive control of the stdin refusal below
    each(
        "launch stdin",
        (|| {
            let rec = at("one-stdin");
            launch(
                app,
                main,
                on_stdin(input(&agent, &work, &rec, "detached", &one.secret)),
                &["pluxy/launcher start", "detached", "stdin", &one.label],
                &values,
            )?;
            let r = record(&rec, 0)?;
            end(&r, &rec);
            require(
                r["stdin"] == one.value.as_str() && !r["env"].to_string().contains(&one.value),
                "the launch did not get its client key on its stdin, and only there",
            )
        })(),
    );

    // ---- restart deleted: a restart checks the key again
    each(
        "restart deleted",
        (|| {
            delete(main, &config, one)?;
            restart_refused(main, &first.0, &rec_one, &first.1, "no longer accepts")
        })(),
    );

    // ---- launch deleted: refused before the confirmation
    each(
        "launch deleted",
        (|| {
            let rec = at("launch-deleted");
            refused_unasked(
                main,
                "program_call",
                json!({"package": LAUNCHER, "op": "start",
                    "input": input(&agent, &work, &rec, "detached", &one.secret)}),
                "no longer accepts",
            )?;
            std::thread::sleep(Duration::from_millis(500));
            started_anyway(&rec, "the launch with a deleted client key")?;
            // The same key given on its stdin instead.
            let rec = at("launch-deleted-stdin");
            refused_unasked(
                main,
                "program_call",
                json!({"package": LAUNCHER, "op": "start",
                    "input": on_stdin(input(&agent, &work, &rec, "detached", &one.secret))}),
                "no longer accepts",
            )?;
            std::thread::sleep(Duration::from_millis(500));
            started_anyway(&rec, "the launch with a deleted client key on its stdin")
        })(),
    );

    // ---- spawn deleted: the spawn checks the key again
    each(
        "spawn deleted",
        (|| {
            let rec = at("spawn-deleted");
            let (slot, hold) = held(
                app,
                main,
                input(&agent, &work, &rec, "detached", &three.secret),
                &at("hold-deleted"),
                &["pluxy/launcher start", "detached", &three.label],
            )?;
            delete(main, &config, three)?;
            let answer = released(main, &slot, &hold)?;
            std::thread::sleep(Duration::from_millis(500));
            started_anyway(&rec, "the launch whose key was deleted before its spawn")?;
            require(
                answer["error"]["code"] == "unavailable"
                    && answer["error"]["message"]
                        .as_str()
                        .is_some_and(|m| m.contains("no longer accepts")),
                format!("the spawn of a launch whose key was deleted on core: {answer}"),
            )
        })(),
    );

    // ---- other core: the restart and the spawn check the key's core again
    let rec_two = at("two");
    each(
        "other core",
        (|| {
            let (v, _) = launch(
                app,
                main,
                input(&agent, &work, &rec_two, "managed", &two.secret),
                &["pluxy/launcher start", "managed", &two.label],
                &values,
            )?;
            let id = v["answer"]["output"]["runtime"]
                .as_str()
                .unwrap_or_default()
                .to_string();
            let a = record(&rec_two, 0)?;
            // A restart with a key core takes: the positive control.
            let r = runtime(main, LAUNCHER, "restart", &id)?;
            require(r["output"]["state"] == "running", format!("restart: {r}"))?;
            let b = record(&rec_two, a["pid"].as_u64().unwrap_or(0))?;
            require(
                b["env"]["FIXTURE_KEY"] == two.value.as_str(),
                "the restarted runtime did not get its client key",
            )?;
            // A managed launch confirmed now, its spawn held across the switch.
            let rec_held = at("spawn-switched");
            let (slot, hold) = held(
                app,
                main,
                input(&agent, &work, &rec_held, "managed", &two.secret),
                &at("hold-switched"),
                &["pluxy/launcher start", "managed", &two.label],
            )?;
            let settings = app.state::<App>().conn.lock().unwrap().test_settings_path();
            let saved: Value =
                serde_json::from_slice(&std::fs::read(&settings).map_err(|e| e.to_string())?)
                    .map_err(|e| e.to_string())?;
            let other = Listener::start()?;
            let v = ok(
                main,
                "connection_set",
                json!({"change": {"mode": "local", "url": other.base}}),
            )?;
            require(
                v["target"] == other.base.as_str(),
                format!("the switch: {v}"),
            )?;
            // Each check below runs, and every failure is reported: none hides another, and the
            // app is connected to its own core again whatever failed.
            let refused = restart_refused(main, &id, &rec_two, &b, "another core");
            let answer = released(main, &slot, &hold).and_then(|answer| {
                require(
                    answer["error"]["code"] == "unavailable"
                        && answer["error"]["message"]
                            .as_str()
                            .is_some_and(|m| m.contains("another core")),
                    format!("the spawn while connected to another core: {answer}"),
                )
            });
            std::thread::sleep(Duration::from_millis(500));
            let spawned = started_anyway(&rec_held, "the launch held across a core switch");
            std::thread::sleep(Duration::from_millis(1000));
            let got = other.received();
            drop(other);
            let sent = require(
                !got.contains("/v1/models") && !values.iter().any(|k| got.contains(k)),
                "a client key was sent to the other core",
            );
            let back = ok(
                main,
                "connection_set",
                json!({"change": {"mode": "managed", "core_source": saved["core_source"]}}),
            )
            .and_then(|back| {
                require(
                    back["target"] == base.as_str() && back["signed_in"] == true,
                    format!("back to the app's own core: {back}"),
                )
            });
            let failures: Vec<String> = [refused, answer, spawned, sent, back]
                .into_iter()
                .filter_map(Result::err)
                .collect();
            if !failures.is_empty() {
                return Err(failures.join("; "));
            }
            // The app's own core again: the restart starts it with the key.
            let r = runtime(main, LAUNCHER, "restart", &id)?;
            require(
                r["output"]["state"] == "running",
                format!("the restart on the app's own core again: {r}"),
            )?;
            let c = record(&rec_two, b["pid"].as_u64().unwrap_or(0))?;
            require(
                c["env"]["FIXTURE_KEY"] == two.value.as_str(),
                "the runtime restarted on the app's own core did not get its client key",
            )
        })(),
    );

    // ---- recipient: another tool's client key, passed on to the launcher's launch
    each(
        "recipient",
        (|| {
            let list = ok(main, "vault_secrets", json!({}))?;
            let plain = list["secrets"]
                .as_array()
                .and_then(|a| a.iter().find(|s| s["label"] == LABEL))
                .and_then(|s| s["id"].as_str())
                .ok_or(format!("no secret labelled {LABEL:?}: {list}"))?
                .to_string();
            let relay = |secret: &str, rec: &Path| {
                let mut launch_input = input(&agent, &work, rec, "managed", "");
                if let Some(o) = launch_input.as_object_mut() {
                    o.remove("key");
                }
                json!({"package": OTHER, "op": "relay", "input": {
                    "package": LAUNCHER, "op": "start", "input": launch_input,
                    "key": secret, "key_as": "key"}})
            };
            // A plain secret's reference, passed on: asked, then started with it.
            let rec_plain = at("relayed-plain");
            let slot = start(main, "program_call", relay(&plain, &rec_plain))?;
            dialog_with(app, &["pluxy/other relay", LABEL])?;
            press("alt+o")?;
            dialog_with(app, &["pluxy/launcher start", "managed", LABEL])?;
            // An import's Confirm is enabled a second after its dialog shows.
            std::thread::sleep(Duration::from_millis(1100));
            press("alt+o")?;
            let r = settle(main, &slot)?;
            let spawned = &r["value"]["answer"]["output"]["answer"];
            require(
                r["ok"] == true
                    && spawned["output"]["runtime"]
                        .as_str()
                        .is_some_and(|id| id.starts_with("rt-")),
                format!("the relayed launch of a plain secret: {r}"),
            )?;
            let p = record(&rec_plain, 0)?;
            require(
                p["env"]["FIXTURE_KEY"].is_string(),
                "the relayed launch did not get its secret",
            )?;
            // Its own client key's reference, passed on: refused before the import's confirmation.
            let rec_key = at("relayed-key");
            let slot = start(main, "program_call", relay(&other_key.secret, &rec_key))?;
            dialog_with(app, &["pluxy/other relay", &other_key.label])?;
            press("alt+o")?;
            std::thread::sleep(Duration::from_millis(500));
            let r = settle_unasked(main, &slot);
            std::thread::sleep(Duration::from_millis(500));
            started_anyway(&rec_key, "the launch with another tool's client key")?;
            let r = r?;
            let answer = &r["value"]["answer"];
            require(
                r["ok"] == true
                    && answer["error"]["code"] == "permission"
                    && answer["error"]["message"]
                        .as_str()
                        .is_some_and(|m| m.contains("client key for pluxy/other")),
                format!("pluxy/other's client key passed on to the launcher's launch: {r}"),
            )
        })(),
    );

    // ---- audit
    each("audit", clean_webview(main, &values));
    if !failed.is_empty() {
        return Err(failed.join("; "));
    }

    out["passed"] = json!(true);
    log("spawn_keys", out);
    Ok(())
}
