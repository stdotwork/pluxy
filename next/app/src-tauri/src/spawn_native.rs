//! The native test's check of the host's spawn service, managed runtime registry and launch
//! environment (native-test builds with `artifact-test-trust` only). It drives the real app through
//! the window's own commands and the real native key entry and confirmations, with the signed
//! fixture tools `native/spawn.ts` built: pluxy/launcher (a launch tool, which holds only
//! references) and pluxy/other. The agent every launch starts is the fixture program (`agent`),
//! which writes what it sees of itself to its record. The key and the environment canaries come
//! from `keys.json`, made there; the app never chose them.
//!
//! - detached: the window's launch of an agent with a key, an environment variable set, one
//!   inherited by name and one removed asks a native confirmation naming the lifetime, the
//!   variables and the key's label, never the key; the launcher asks the host to spawn it with a
//!   frame naming nothing but the call. The agent runs with exactly the confirmed argv and cwd, in
//!   a session and process group of its own that is not the app's child, with only /dev/null open
//!   on 0-2, core limits of 1 (no core file, no core dump handler), and an environment of the selected session values, the variables asked
//!   for and the key, and none of the app's other variables (the canaries). The launcher's record of
//!   what it received holds a reference and never the key; its second spawn for the same call is
//!   refused;
//! - crash: the launcher's crash leaves the agent running;
//! - managed: two managed runtimes, each in a process group of its own (not the launcher's), the
//!   app's child, with the app's core hard limit and no key or inherited variable;
//! - restart: after the launcher crashes and starts again, it lists both runtimes and finds the
//!   first running as it was; a spawn for a call that confirmed no launch is refused; pluxy/other
//!   cannot stop it (`not_found`); the launcher's restart replaces only that
//!   runtime's group (leader and member), and its stop ends only that group: the other runtime, the
//!   agent and the launcher itself run on;
//! - idle: once the launcher stopped when idle, the agent and the other runtime run on.
//!
//! Then it writes `known.json` (the agent's and the runtime's processes, for the runner, which
//! checks after the app quit that the agent runs on and the runtime does not, then ends them) and
//! the app quits.
//!
//! One line on stderr: `native-test: spawn <JSON>`.

use super::admission::{ok, press, require, settle, start};
use super::routing::{call, enable, install};
use super::vault::{dialog_with, type_key};
use super::{log, wait_for};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Webview};

const LAUNCHER: &str = "pluxy/launcher";
const OTHER: &str = "pluxy/other";
const LABEL: &str = "fixture agent key";
const AGENT: &str = "pluxy-fixture-agent";

/// The session values a spawned process may get from the app's environment, with `LC_*`.
const BASE: &[&str] = &[
    "HOME",
    "USER",
    "LOGNAME",
    "SHELL",
    "PATH",
    "LANG",
    "LANGUAGE",
    "TZ",
    "TERM",
    "COLORTERM",
    "DISPLAY",
    "WAYLAND_DISPLAY",
    "XAUTHORITY",
    "DBUS_SESSION_BUS_ADDRESS",
    "XDG_RUNTIME_DIR",
    "XDG_CONFIG_HOME",
    "XDG_DATA_HOME",
    "XDG_CACHE_HOME",
    "XDG_STATE_HOME",
    "XDG_CONFIG_DIRS",
    "XDG_DATA_DIRS",
    "XDG_SESSION_TYPE",
    "XDG_CURRENT_DESKTOP",
    "XDG_SESSION_DESKTOP",
    "http_proxy",
    "https_proxy",
    "no_proxy",
    "all_proxy",
    "HTTP_PROXY",
    "HTTPS_PROXY",
    "NO_PROXY",
    "ALL_PROXY",
];

fn alive(pid: u64) -> bool {
    pid > 0 && unsafe { libc::kill(pid as libc::pid_t, 0) } == 0
}

/// Whether pid runs as the process whose command line holds marker.
fn owned(pid: u64, marker: &str) -> bool {
    alive(pid)
        && std::fs::read(format!("/proc/{pid}/cmdline"))
            .is_ok_and(|c| String::from_utf8_lossy(&c).contains(marker))
}

/// Waits until pid no longer runs as marker's process, at most within.
fn ended(pid: u64, marker: &str, within: Duration) -> bool {
    let deadline = Instant::now() + within;
    while Instant::now() < deadline {
        if !owned(pid, marker) {
            return true;
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    false
}

/// The record the agent writes at path, once it is there and names a process other than not.
fn record(path: &Path, not: u64) -> Result<Value, String> {
    wait_for(&format!("the agent's record {}", path.display()), || {
        std::fs::read(path)
            .ok()
            .and_then(|b| serde_json::from_slice::<Value>(&b).ok())
            .filter(|r| r["pid"].as_u64().is_some_and(|p| p != not))
    })
}

/// The app's own core file size limits, as the agent writes its own.
fn own_core() -> Value {
    let mut l = libc::rlimit {
        rlim_cur: 0,
        rlim_max: 0,
    };
    unsafe { libc::getrlimit(libc::RLIMIT_CORE, &mut l) };
    let v = |x: libc::rlim_t| {
        if x == libc::RLIM_INFINITY {
            json!("unlimited")
        } else {
            json!(x)
        }
    };
    json!([v(l.rlim_cur), v(l.rlim_max)])
}

/// The launcher's start call from the window, with input, its native confirmation (which must name
/// every one of needles and none of hidden) accepted; the call's answer.
fn launch(
    app: &AppHandle,
    main: &Webview,
    input: Value,
    needles: &[&str],
    hidden: &[&str],
) -> Result<(Value, String), String> {
    let slot = start(
        main,
        "program_call",
        json!({"package": LAUNCHER, "op": "start", "input": input}),
    )?;
    let text = dialog_with(app, needles)?;
    if let Some(h) = hidden.iter().find(|h| text.contains(**h)) {
        press("alt+c")?;
        return Err(format!(
            "the launch's confirmation shows a value it must not ({} characters)",
            h.len()
        ));
    }
    press("alt+o")?;
    let r = settle(main, &slot)?;
    require(r["ok"] == true, format!("the confirmed launch: {r}"))?;
    let answer = r["value"]["answer"].clone();
    require(
        answer.get("output").is_some(),
        format!("the host's answer to the launcher's spawn: {answer}"),
    )?;
    Ok((r["value"].clone(), text))
}

/// Waits out the launcher's crash backoff (doubling with each crash in a row), once its status
/// says it crashed.
fn after_backoff(main: &Webview) -> Result<(), String> {
    let wait = wait_for("the launcher's crash in its status", || {
        let status = ok(main, "program_status", json!({"package": LAUNCHER})).ok()?;
        (status["state"] == "crashed").then(|| status["retry_in_ms"].as_u64().unwrap_or(0))
    })?;
    std::thread::sleep(Duration::from_millis(wait + 300));
    Ok(())
}

/// The launcher's runtime call: the host's answer to its runtime frame.
fn runtime(main: &Webview, package: &str, op: &str, id: &str) -> Result<Value, String> {
    let r = call(main, package, "runtime", json!({"op": op, "runtime": id}))?;
    require(r["ok"] == true, format!("{package}'s runtime {op}: {r}"))?;
    Ok(r["value"]["answer"].clone())
}

fn names(env: &Value) -> Vec<String> {
    env.as_object()
        .map(|m| m.keys().cloned().collect())
        .unwrap_or_default()
}

/// The agent's environment holds exactly the selected session values the app has (but removed),
/// with the app's values, and asked: no other variable of the app's.
fn check_environment(
    env: &Value,
    asked: &[(&str, &str)],
    removed: &[&str],
    canaries: &[&str],
) -> Result<(), String> {
    for name in names(env) {
        let base = BASE.contains(&name.as_str()) || name.starts_with("LC_");
        require(
            base || asked.iter().any(|(n, _)| *n == name),
            format!("the agent got {name}, which nothing selected or asked for"),
        )?;
        require(
            !removed.contains(&name.as_str()),
            format!("{name} was not removed"),
        )?;
    }
    for name in BASE {
        if removed.contains(name) {
            continue;
        }
        if let Ok(v) = std::env::var(name) {
            require(
                env[*name] == v.as_str(),
                format!("the agent's {name} is not the app's"),
            )?;
        }
    }
    for want in [
        "HOME",
        "XDG_CONFIG_HOME",
        "XDG_RUNTIME_DIR",
        "DISPLAY",
        "PATH",
    ] {
        require(env.get(want).is_some(), format!("the agent has no {want}"))?;
    }
    for (n, v) in asked {
        require(
            env[*n] == *v,
            format!("the agent's {n} is not the one asked for"),
        )?;
    }
    let all = env.to_string();
    require(
        !canaries.iter().any(|c| all.contains(c)),
        "the agent inherited one of the app's canary variables",
    )
}

/// The agent's process: in a session and group of its own, not the app's child, with only
/// /dev/null on 0-2 and core limits of 1 (no core file, no core dump handler).
fn check_detached(r: &Value) -> Result<(), String> {
    let pid = r["pid"].as_u64().unwrap_or(0);
    require(
        r["sid"].as_u64() == Some(pid) && r["pgid"].as_u64() == Some(pid),
        format!("the agent does not lead a session and group of its own: {r}"),
    )?;
    require(
        r["ppid"].as_u64() != Some(u64::from(std::process::id())),
        "the agent is the app's child, not detached",
    )?;
    let fds = r["fds"].as_array().cloned().unwrap_or_default();
    require(
        fds.len() == 3
            && fds
                .iter()
                .enumerate()
                .all(|(i, f)| f["fd"] == i && f["target"] == "/dev/null"),
        format!(
            "the agent's descriptors are not just /dev/null on 0-2: {}",
            json!(fds)
        ),
    )?;
    require(
        r["core"] == json!([1, 1]),
        format!(
            "the agent holding a key may write core files or run the core dump handler: {}",
            r["core"]
        ),
    )
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let keys: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("keys.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let s = |k: &str| keys[k].as_str().map(String::from).ok_or(format!("no {k}"));
    let (key, provider, test, inherit) = (s("key")?, s("provider")?, s("test")?, s("inherit")?);
    let canaries = [provider.as_str(), test.as_str()];
    let agent = fixtures.join("agent");
    let agent_path = agent.to_string_lossy().into_owned();
    let work = fixtures.join("work");
    std::fs::create_dir_all(&work).map_err(|e| e.to_string())?;
    let work = std::fs::canonicalize(&work).map_err(|e| e.to_string())?;
    let at = |name: &str| work.join(name);
    let mut known = json!({"detached": [], "managed": []});
    let save = |known: &Value| {
        std::fs::write(fixtures.join("known.json"), known.to_string()).map_err(|e| e.to_string())
    };
    let note = |known: &mut Value, kind: &str, r: &Value, rec: &Path| {
        let marker = rec.to_string_lossy().into_owned();
        for pid in [&r["pid"], &r["child"]] {
            if let Some(list) = known[kind].as_array_mut() {
                list.push(json!({"pid": pid, "marker": marker}));
            }
        }
    };
    let mut out = json!({});
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("{name}: {e}"));

    // ---- secret: the key the detached launch hands to its agent
    let mut secret = String::new();
    step(
        "secret",
        (|| {
            let slot = start(main, "vault_secret_add", json!({"label": LABEL}))?;
            type_key("pluxy secret", &key)?;
            let r = settle(main, &slot)?;
            require(r["ok"] == true, format!("vault_secret_add: {r}"))?;
            secret = r["value"]["id"].as_str().unwrap_or_default().to_string();
            require(secret.starts_with("s-"), format!("the new secret: {r}"))
        })(),
    )?;

    step(
        "enable",
        (|| {
            for id in [LAUNCHER, OTHER] {
                install(main, "curated", id, "1.0.0")?;
                enable(app, main, id)?;
            }
            Ok(())
        })(),
    )?;

    // ---- detached
    let rec_d = at("detached");
    let mut detached = Value::Null;
    step(
        "detached",
        (|| {
            let argv = json!([AGENT, rec_d]);
            let (v, text) = launch(
                app,
                main,
                json!({
                    "executable": agent_path, "argv": argv, "cwd": work,
                    "lifetime": "detached", "key": secret, "record": at("launcher-record"),
                    "again": true,
                    "environment": {
                        "set": {"FIXTURE_SET": "set-value"},
                        "inherit": ["FIXTURE_INHERIT"],
                        "unset": ["LANG"],
                        "secrets": {"FIXTURE_KEY": "key"},
                    },
                }),
                &[
                    "pluxy/launcher start",
                    "detached",
                    "FIXTURE_SET",
                    "set-value",
                    "FIXTURE_INHERIT",
                    "FIXTURE_KEY",
                    LABEL,
                ],
                &[&key, &inherit],
            )?;
            detached = record(&rec_d, 0)?;
            note(&mut known, "detached", &detached, &rec_d);
            save(&known)?;
            let mut want = vec![agent_path.clone()];
            want.extend(
                argv.as_array()
                    .into_iter()
                    .flatten()
                    .filter_map(|a| a.as_str().map(String::from)),
            );
            require(
                detached["argv"] == json!(want),
                format!("the agent's argv: {}", detached["argv"]),
            )?;
            require(
                detached["cwd"] == json!(work),
                format!("the agent's cwd: {}", detached["cwd"]),
            )?;
            check_detached(&detached)?;
            check_environment(
                &detached["env"],
                &[
                    ("FIXTURE_SET", "set-value"),
                    ("FIXTURE_INHERIT", &inherit),
                    ("FIXTURE_KEY", &key),
                ],
                &["LANG"],
                &canaries,
            )?;
            // The launcher got a reference, never the key, and named nothing of the launch.
            let got = std::fs::read_to_string(at("launcher-record")).unwrap_or_default();
            let frame: Value = got
                .lines()
                .next()
                .and_then(|l| serde_json::from_str(l).ok())
                .unwrap_or(Value::Null);
            require(
                frame["input"]["key"]
                    .as_str()
                    .is_some_and(|r| r.starts_with("r-"))
                    && frame.get("secrets").is_none()
                    && !got.contains(&key),
                "the launcher received more than a reference to the key",
            )?;
            require(
                !v.to_string().contains(&key),
                "the launch's answer holds the key",
            )?;
            // A second spawn for the same call is refused: a confirmation spawns once.
            require(
                v["again"]["error"]["code"] == "permission",
                format!("the second spawn for the same call: {}", v["again"]),
            )?;
            out["detached_confirmation"] = json!(text.contains("detached"));
            Ok(())
        })(),
    )?;
    let d_pid = detached["pid"].as_u64().unwrap_or(0);
    let d_marker = rec_d.to_string_lossy().into_owned();

    // ---- crash: the launcher's crash leaves the agent
    step(
        "crash",
        (|| {
            let r = call(main, LAUNCHER, "crash", json!({}))?;
            require(
                r["ok"] == false,
                format!("the launcher's crash answered: {r}"),
            )?;
            std::thread::sleep(Duration::from_millis(500));
            require(
                owned(d_pid, &d_marker),
                "the agent ended with the launcher's crash",
            )
        })(),
    )?;
    // The launcher's next call waits out the crash backoff.
    after_backoff(main)?;

    // ---- managed: two runtimes
    let (rec_a, rec_b) = (at("managed-a"), at("managed-b"));
    let mut ids = Vec::new();
    let mut runs = Vec::new();
    step(
        "managed",
        (|| {
            for rec in [&rec_a, &rec_b] {
                let (v, _) = launch(
                    app,
                    main,
                    json!({
                        "executable": agent_path, "argv": [AGENT, rec], "cwd": work,
                        "lifetime": "managed",
                        "environment": {"set": {"FIXTURE_SET": "managed"}},
                    }),
                    &["pluxy/launcher start", "managed", "FIXTURE_SET"],
                    &[],
                )?;
                let id = v["answer"]["output"]["runtime"]
                    .as_str()
                    .unwrap_or_default()
                    .to_string();
                require(
                    id.starts_with("rt-"),
                    format!("the managed launch's answer: {v}"),
                )?;
                let r = record(rec, 0)?;
                let pid = r["pid"].as_u64().unwrap_or(0);
                require(
                    r["pgid"].as_u64() == Some(pid) && r["sid"].as_u64() != Some(pid),
                    format!(
                        "the runtime does not lead a group of its own in the app's session: {r}"
                    ),
                )?;
                require(
                    r["ppid"].as_u64() == Some(u64::from(std::process::id())),
                    "the runtime is not the app's child",
                )?;
                require(
                    v["pid"].as_u64() != Some(pid)
                        && r["pgid"].as_u64() != Some(v["pid"].as_u64().unwrap_or(0)),
                    "the runtime is in the launcher's group",
                )?;
                require(
                    r["core"][1] == own_core()[1],
                    format!(
                        "the runtime's core hard limit {} is not the app's {}",
                        r["core"],
                        own_core()
                    ),
                )?;
                check_environment(&r["env"], &[("FIXTURE_SET", "managed")], &[], &canaries)?;
                require(
                    r["env"].get("FIXTURE_KEY").is_none()
                        && r["env"].get("FIXTURE_INHERIT").is_none(),
                    "the runtime got a variable another launch asked for",
                )?;
                ids.push(id);
                runs.push(r);
            }
            Ok(())
        })(),
    )?;
    let marker = |p: &Path| p.to_string_lossy().into_owned();
    let pid_of = |r: &Value, k: &str| r[k].as_u64().unwrap_or(0);
    let (a, b) = (runs[0].clone(), runs[1].clone());
    // The second runtime is the one the app's quit must stop.
    note(&mut known, "managed", &b, &rec_b);
    save(&known)?;

    // ---- restart: control across the launcher's restart, of only the registered group
    step(
        "restart",
        (|| {
            let r = call(main, LAUNCHER, "crash", json!({}))?;
            require(
                r["ok"] == false,
                format!("the launcher's crash answered: {r}"),
            )?;
            // Its second crash in a row: the backoff has doubled.
            after_backoff(main)?;
            // A spawn for a call that confirmed no launch (a read) is refused.
            let r = call(main, LAUNCHER, "runtime", json!({"op": "spawn"}))?;
            require(
                r["ok"] == true && r["value"]["answer"]["error"]["code"] == "permission",
                format!("a spawn for a call that confirmed no launch: {r}"),
            )?;
            let listed = runtime(main, LAUNCHER, "list", "")?;
            let list = listed["output"]["runtimes"].to_string();
            require(
                ids.iter().all(|id| list.contains(id.as_str())),
                format!("the restarted launcher's list: {listed}"),
            )?;
            let st = runtime(main, LAUNCHER, "status", &ids[0])?;
            require(
                st["output"]["state"] == "running" && st["output"]["pid"] == a["pid"],
                format!("the first runtime's status after the launcher restarted: {st}"),
            )?;
            // Another package cannot stop it.
            let r = call(
                main,
                OTHER,
                "runtime",
                json!({"op": "stop", "runtime": ids[0]}),
            )?;
            require(
                r["ok"] == true && r["value"]["answer"]["error"]["code"] == "not_found",
                format!("pluxy/other's stop of the launcher's runtime: {r}"),
            )?;
            std::thread::sleep(Duration::from_millis(500));
            require(
                owned(pid_of(&a, "pid"), &marker(&rec_a)),
                "pluxy/other stopped the launcher's runtime",
            )?;
            let tool = ok(main, "program_status", json!({"package": LAUNCHER}))?["pid"].clone();
            // Restart replaces the group's leader and member.
            let r = runtime(main, LAUNCHER, "restart", &ids[0])?;
            require(r["output"]["state"] == "running", format!("restart: {r}"))?;
            let fresh = record(&rec_a, pid_of(&a, "pid"))?;
            require(
                r["output"]["pid"] == fresh["pid"],
                format!("restart answered {r}, the new leader is {}", fresh["pid"]),
            )?;
            for k in ["pid", "child"] {
                require(
                    ended(pid_of(&a, k), &marker(&rec_a), Duration::from_secs(10)),
                    format!("the first runtime's old {k} runs on after its restart"),
                )?;
            }
            // Stop ends the new group, and nothing else.
            let r = runtime(main, LAUNCHER, "stop", &ids[0])?;
            require(r["output"]["state"] != "running", format!("stop: {r}"))?;
            for k in ["pid", "child"] {
                require(
                    ended(pid_of(&fresh, k), &marker(&rec_a), Duration::from_secs(10)),
                    format!("the first runtime's {k} runs on after its stop"),
                )?;
            }
            for k in ["pid", "child"] {
                require(
                    owned(pid_of(&b, k), &marker(&rec_b)),
                    format!("the second runtime's {k} ended with the first one's restart or stop"),
                )?;
            }
            require(
                owned(d_pid, &d_marker),
                "the agent ended with a runtime's stop",
            )?;
            let now = ok(main, "program_status", json!({"package": LAUNCHER}))?["pid"].clone();
            require(
                !tool.is_null() && now == tool,
                format!("the launcher was {tool} before the restart and stop, {now} after"),
            )
        })(),
    )?;

    // ---- idle: the launcher stops when idle; the agent and the second runtime run on
    step(
        "idle",
        (|| {
            let deadline = Instant::now() + Duration::from_secs(90);
            loop {
                let st = ok(main, "program_status", json!({"package": LAUNCHER}))?;
                if st["state"] == "idle" {
                    break;
                }
                require(
                    Instant::now() < deadline,
                    format!("the launcher never stopped when idle: {st}"),
                )?;
                std::thread::sleep(Duration::from_millis(500));
            }
            require(
                owned(d_pid, &d_marker),
                "the agent ended with the launcher's idle stop",
            )?;
            require(
                owned(pid_of(&b, "pid"), &marker(&rec_b)),
                "the second runtime ended with the launcher's idle stop",
            )
        })(),
    )?;

    out["passed"] = json!(true);
    log("spawn", out);
    Ok(())
}
