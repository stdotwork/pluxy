//! The native test's check of declared jobs and source registration (native-test builds with
//! `artifact-test-trust` only), run after the routing check in its session, with the signed tools
//! `native/routing.ts` built (their digests are in `expect.json`, worked out there, not by the
//! app). It drives the real app through the window's own commands and the real GTK native
//! confirmations: the window calls the consumer pluxy/jobs-hub, whose `jobs` operation asks the
//! host's job service, and reads back what the host delivered to its `take` operation (`report`).
//! Each refusal has a positive control on the same route:
//!
//! - register: the consumer lists the registrations of its kind: exactly pluxy/jobs-source, at the
//!   package, version, digest, generation, schema and source the catalog admitted it with;
//!   pluxy/jobs-other, at a schema the consumer does not accept, is not listed; a tool that does
//!   not consume the kind, or a kind the consumer does not consume, is refused; a package naming a
//!   kind twice, or consuming another package's kind, is refused at install;
//! - one job: a job of three batches is delivered in order, each with the host's source and the
//!   chain the host set, then its completion, once the producer saw every batch acknowledged;
//! - forged: a batch naming a source fails its job before anything is delivered; the window
//!   calling the deliver operation itself is refused; a start naming another digest, a producer
//!   at a schema the consumer does not accept, or made by a tool that does not consume the kind,
//!   is refused;
//! - backpressure: twelve batches behind a slow consumer arrive in order, the producer never more
//!   than the window ahead; a producer sending past the window, or a batch past the size limit,
//!   fails its job with at most the window delivered; a fifth job at once is refused, and the four
//!   end cancelled;
//! - cancel/restart: another tool cannot cancel the consumer's job, the consumer can, and a new
//!   job then completes under a new ID;
//! - stale: once the producer is updated (a swap, confirmed), its running job is cancelled with
//!   nothing more delivered, a start naming the old release is refused, and the new one's
//!   registration works;
//! - disable: disabling the producer cancels its running job, its registration is gone and a
//!   start is refused; enabled again, it is listed again.
//!
//! One line on stderr: `native-test: jobs <JSON>`.

use super::admission::{dialog_text, ok, press, refused, require, settle, start};
use super::routing::{call, enable, install};
use super::{log, wait_for};
use serde_json::{json, Value};
use std::path::PathBuf;
use tauri::{AppHandle, Manager, Webview};

const HUB: &str = "pluxy/jobs-hub";
const SOURCE: &str = "pluxy/jobs-source";
const OTHER: &str = "pluxy/jobs-other";
const THIEF: &str = "pluxy/jobs-thief";
const KIND: &str = "pluxy/jobs-hub/items";
/// Unacknowledged batches a producer may have, as the host sets it.
const WINDOW: u64 = 4;

/// The answer to package's `jobs` call asking the host's job service for op with input:
/// `{"output": ...}` or `{"error": ...}`.
fn ask(main: &Webview, package: &str, op: &str, input: Value) -> Result<Value, String> {
    let r = call(main, package, "jobs", json!({"op": op, "input": input}))?;
    require(
        r["ok"] == true,
        format!("{package}'s jobs call for {op}: {r}"),
    )?;
    Ok(r["value"]["answer"].clone())
}

fn output(a: &Value, what: &str) -> Result<Value, String> {
    require(
        a.get("output").is_some() && a.get("error").is_none(),
        format!("{what}: {a}"),
    )?;
    Ok(a["output"].clone())
}

fn denied(a: &Value, code: &str, what: &str) -> Result<(), String> {
    require(
        a["error"]["code"] == code && a.get("output").is_none(),
        format!("{what} should be refused as {code}: {a}"),
    )
}

/// What the host delivered to the hub for job, in order.
fn delivered(main: &Webview, job: &str) -> Result<Vec<Value>, String> {
    let r = call(main, HUB, "report", json!({}))?;
    require(r["ok"] == true, format!("the hub's report: {r}"))?;
    Ok(r["value"]["deliveries"]
        .as_array()
        .cloned()
        .unwrap_or_default()
        .into_iter()
        .filter(|d| d["input"]["job"] == job)
        .collect())
}

/// Job's batches and its one terminal delivery, once it came; nothing after it.
fn finished(main: &Webview, job: &str) -> Result<(Vec<Value>, Value), String> {
    let all = wait_for(&format!("job {job}'s end"), || {
        let d = delivered(main, job).ok()?;
        d.iter()
            .any(|d| d["input"]["event"] != "batch")
            .then_some(d)
    })?;
    let ends: Vec<&Value> = all
        .iter()
        .filter(|d| d["input"]["event"] != "batch")
        .collect();
    require(
        ends.len() == 1 && all.last() == ends.first().copied(),
        format!("job {job} should end once, after its batches: {all:?}"),
    )?;
    let end = ends[0]["input"].clone();
    let batches = all[..all.len() - 1].to_vec();
    require(
        end["batches"] == batches.len(),
        format!(
            "job {job}'s end counts {} batches; {} came",
            end["batches"],
            batches.len()
        ),
    )?;
    Ok((batches, end))
}

/// Waits until job has had n batches delivered.
fn batches_seen(main: &Webview, job: &str, n: usize) -> Result<(), String> {
    wait_for(&format!("{n} batches of job {job}"), || {
        (delivered(main, job).ok()?.len() >= n).then_some(())
    })
}

/// Batches are items 1..=n, in order, numbered 1..=n, each from source.
fn items(batches: &[Value], n: u64, source: &Value) -> Result<(), String> {
    require(
        batches.len() as u64 == n,
        format!("{n} batches should come: {batches:?}"),
    )?;
    for (i, b) in batches.iter().enumerate() {
        let k = i as u64 + 1;
        let input = &b["input"];
        require(
            input["event"] == "batch"
                && input["kind"] == KIND
                && input["seq"] == k
                && input["value"]["n"] == k
                && input["value"]["item"] == format!("item-{k}")
                && input["source"] == *source,
            format!("batch {k} should be item-{k} from {source}: {b}"),
        )?;
    }
    Ok(())
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let expect: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("expect.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let sha = |id: &str, version: &str| expect["sha256"][format!("{id} {version}")].clone();
    let curated = fixtures.join("curated.json").display().to_string();
    let generation = |id: &str| -> Result<u64, String> {
        app.state::<crate::App>()
            .device
            .lock()
            .unwrap()
            .admitted(id, &crate::trust::TrustStore::current())
            .map(|a| a.installed_at())
    };
    // The registration the catalog admitted the source as, at version.
    let registration = |version: &str| -> Result<Value, String> {
        Ok(json!({
            "package": SOURCE,
            "version": version,
            "sha256": sha(SOURCE, version),
            "generation": generation(SOURCE)?,
            "schema": 1,
            "source": curated,
        }))
    };
    let listed = || -> Result<Vec<Value>, String> {
        let out = output(
            &ask(main, HUB, "registrations", json!({"kind": KIND}))?,
            "the hub's registrations",
        )?;
        // The signer is the curated key's: only its publisher is known here.
        let mut regs = out["registrations"].as_array().cloned().unwrap_or_default();
        for x in &mut regs {
            require(
                x["signer"]["publisher"] == "pluxy",
                format!("a registration's signer: {x}"),
            )?;
            if let Some(m) = x.as_object_mut() {
                m.remove("signer");
            }
        }
        Ok(regs)
    };
    let start_job = |reg: &Value, input: Value| -> Result<String, String> {
        let a = ask(
            main,
            HUB,
            "start",
            json!({"kind": KIND, "package": reg["package"], "sha256": reg["sha256"],
                "generation": reg["generation"], "input": input}),
        )?;
        let job = output(&a, "the hub's start")?["job"]
            .as_str()
            .unwrap_or_default()
            .to_string();
        require(job.starts_with("j-"), format!("a host-issued job ID: {a}"))?;
        Ok(job)
    };
    let cancel = |package: &str, job: &str| ask(main, package, "cancel", json!({"job": job}));
    let mut out = json!({});
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("jobs {name}: {e}"));

    // ---- register
    let mut reg = Value::Null;
    step(
        "register",
        (|| {
            for id in [HUB, SOURCE, OTHER, THIEF] {
                install(main, "curated", id, "1.0.0")?;
            }
            let text = enable(app, main, HUB)?;
            require(
                text.contains(KIND),
                format!("the hub's confirmation does not name the kind it consumes: {text}"),
            )?;
            let text = enable(app, main, SOURCE)?;
            require(
                text.contains(KIND),
                format!("the source's confirmation does not name the kind it produces: {text}"),
            )?;
            enable(app, main, OTHER)?;
            enable(app, main, THIEF)?;
            reg = registration("1.0.0")?;
            let regs = listed()?;
            require(
                regs == vec![reg.clone()],
                format!("the hub's registrations should be only {reg}: {regs:?}"),
            )?;
            denied(
                &ask(main, THIEF, "registrations", json!({"kind": KIND}))?,
                "permission",
                "a tool that does not consume the kind listing it",
            )?;
            denied(
                &ask(
                    main,
                    HUB,
                    "registrations",
                    json!({"kind": "pluxy/jobs-thief/items"}),
                )?,
                "permission",
                "the hub listing a kind it does not consume",
            )?;
            refused(
                main,
                "device_install",
                json!({"registry": "curated", "id": "pluxy/jobs-dup", "version": "1.0.0"}),
                "more than once",
            )?;
            refused(
                main,
                "device_install",
                json!({"registry": "curated", "id": "pluxy/jobs-outside", "version": "1.0.0"}),
                "its own",
            )?;
            Ok(())
        })(),
    )?;
    out["registration"] = reg.clone();

    // ---- one job
    step(
        "one job",
        (|| {
            let job = start_job(&reg, json!({"count": 3}))?;
            let (batches, end) = finished(main, &job)?;
            items(&batches, 3, &reg)?;
            let hop = |id: &str, version: &str| -> Result<Value, String> {
                Ok(
                    json!({"kind": "package", "id": id, "sha256": sha(id, version),
                    "generation": generation(id)?}),
                )
            };
            let chain = json!([{"kind": "main"}, hop(HUB, "1.0.0")?, hop(SOURCE, "1.0.0")?]);
            require(
                batches.iter().all(|b| b["chain"] == chain),
                format!("each delivery's chain should be {chain}: {batches:?}"),
            )?;
            require(
                end["event"] == "done"
                    && end["source"] == reg
                    && end["output"]["sent"] == 3
                    && end["output"]["acked"] == 3
                    && end["output"]["window"] == WINDOW,
                format!("the job's completion: {end}"),
            )?;
            out["job"] = json!(job);
            Ok(())
        })(),
    )?;

    // ---- forged
    step(
        "forged",
        (|| {
            let job = start_job(&reg, json!({"mode": "forge"}))?;
            let (batches, end) = finished(main, &job)?;
            require(
                batches.is_empty()
                    && end["event"] == "failed"
                    && end["error"]["code"] == "permission",
                format!(
                    "a batch naming its source should fail its job undelivered: {batches:?} {end}"
                ),
            )?;
            let r = call(main, HUB, "take", json!({}))?;
            require(
                r["ok"] == false && r["error"].as_str().unwrap_or_default().contains("host"),
                format!("the window calling the hub's deliver operation should be refused: {r}"),
            )?;
            let mut wrong = reg.clone();
            wrong["sha256"] = sha(OTHER, "1.0.0");
            let a = ask(
                main,
                HUB,
                "start",
                json!({"kind": KIND, "package": SOURCE, "sha256": wrong["sha256"],
                    "generation": reg["generation"], "input": {"count": 1}}),
            )?;
            denied(&a, "permission", "a start naming another digest")?;
            let a = ask(
                main,
                HUB,
                "start",
                json!({"kind": KIND, "package": OTHER, "sha256": sha(OTHER, "1.0.0"),
                    "generation": generation(OTHER)?, "input": {"count": 1}}),
            )?;
            denied(
                &a,
                "permission",
                "a start of a producer at a schema the hub does not accept",
            )?;
            let a = ask(
                main,
                THIEF,
                "start",
                json!({"kind": KIND, "package": SOURCE, "sha256": reg["sha256"],
                    "generation": reg["generation"], "input": {"count": 1}}),
            )?;
            denied(
                &a,
                "permission",
                "a start by a tool that does not consume the kind",
            )
        })(),
    )?;

    // ---- backpressure
    step(
        "backpressure",
        (|| {
            let job = start_job(&reg, json!({"count": 12, "hold": 100}))?;
            let (batches, end) = finished(main, &job)?;
            items(&batches, 12, &reg)?;
            require(
                end["event"] == "done"
                    && end["output"]["acked"] == 12
                    && end["output"]["max_unacked"]
                        .as_u64()
                        .is_some_and(|m| m <= WINDOW),
                format!("twelve batches behind a slow consumer: {end}"),
            )?;
            let job = start_job(&reg, json!({"mode": "flood"}))?;
            let (batches, end) = finished(main, &job)?;
            require(
                batches.len() as u64 <= WINDOW
                    && end["event"] == "failed"
                    && end["error"]["message"]
                        .as_str()
                        .unwrap_or_default()
                        .contains("acknowledged"),
                format!("a producer past the window should fail its job: {batches:?} {end}"),
            )?;
            let job = start_job(&reg, json!({"mode": "big"}))?;
            let (batches, end) = finished(main, &job)?;
            require(
                batches.is_empty()
                    && end["event"] == "failed"
                    && end["error"]["message"]
                        .as_str()
                        .unwrap_or_default()
                        .contains("larger"),
                format!("a batch past the size limit should fail its job: {batches:?} {end}"),
            )?;
            let mut jobs = Vec::new();
            for _ in 0..4 {
                jobs.push(start_job(&reg, json!({"mode": "wait"}))?);
            }
            let a = ask(
                main,
                HUB,
                "start",
                json!({"kind": KIND, "package": SOURCE, "sha256": reg["sha256"],
                    "generation": reg["generation"], "input": {"mode": "wait"}}),
            )?;
            denied(&a, "unavailable", "a fifth job at once")?;
            for j in &jobs {
                output(&cancel(HUB, j)?, "the hub cancelling its job")?;
            }
            for j in &jobs {
                let (_, end) = finished(main, j)?;
                require(
                    end["event"] == "cancelled",
                    format!("cancelled job {j}'s end: {end}"),
                )?;
            }
            Ok(())
        })(),
    )?;

    // ---- cancel/restart
    step(
        "cancel/restart",
        (|| {
            let job = start_job(&reg, json!({"mode": "wait"}))?;
            batches_seen(main, &job, 1)?;
            denied(
                &cancel(THIEF, &job)?,
                "permission",
                "another tool cancelling the hub's job",
            )?;
            output(&cancel(HUB, &job)?, "the hub cancelling its job")?;
            let (batches, end) = finished(main, &job)?;
            require(
                batches.len() == 1 && end["event"] == "cancelled",
                format!("the cancelled job: {batches:?} {end}"),
            )?;
            let again = start_job(&reg, json!({"count": 2}))?;
            require(again != job, format!("a restarted job's ID {again} is new"))?;
            let (batches, end) = finished(main, &again)?;
            items(&batches, 2, &reg)?;
            require(end["event"] == "done", format!("the restarted job: {end}"))
        })(),
    )?;

    // ---- stale
    step(
        "stale",
        (|| {
            let job = start_job(&reg, json!({"mode": "wait"}))?;
            batches_seen(main, &job, 1)?;
            // The source is enabled, so its update is a swap with its own native confirmation.
            let slot = start(
                main,
                "device_install",
                json!({"registry": "curated", "id": SOURCE, "version": "1.1.0"}),
            )?;
            let text = dialog_text(app)?;
            require(
                text.contains(SOURCE) && text.contains("1.1.0"),
                format!("the update's confirmation: {text}"),
            )?;
            press("alt+o")?;
            let r = settle(main, &slot)?;
            require(r["ok"] == true, format!("the confirmed update: {r}"))?;
            let (batches, end) = finished(main, &job)?;
            require(
                batches.len() == 1 && end["event"] == "cancelled",
                format!("the job of the replaced release: {batches:?} {end}"),
            )?;
            let a = ask(
                main,
                HUB,
                "start",
                json!({"kind": KIND, "package": SOURCE, "sha256": reg["sha256"],
                    "generation": reg["generation"], "input": {"count": 1}}),
            )?;
            denied(&a, "permission", "a start naming the replaced release")?;
            let new = registration("1.1.0")?;
            let regs = listed()?;
            require(
                regs == vec![new.clone()],
                format!("after the update the registrations should be {new}: {regs:?}"),
            )?;
            let job = start_job(&new, json!({"count": 1}))?;
            let (batches, end) = finished(main, &job)?;
            items(&batches, 1, &new)?;
            require(
                end["event"] == "done",
                format!("the new release's job: {end}"),
            )?;
            reg = new;
            Ok(())
        })(),
    )?;

    // ---- disable
    step(
        "disable",
        (|| {
            let job = start_job(&reg, json!({"mode": "wait"}))?;
            batches_seen(main, &job, 1)?;
            ok(
                main,
                "device_enable",
                json!({"id": SOURCE, "enabled": false}),
            )?;
            let (batches, end) = finished(main, &job)?;
            require(
                batches.len() == 1 && end["event"] == "cancelled",
                format!("the disabled producer's job: {batches:?} {end}"),
            )?;
            let regs = listed()?;
            require(
                regs.is_empty(),
                format!("a disabled producer is not listed: {regs:?}"),
            )?;
            let a = ask(
                main,
                HUB,
                "start",
                json!({"kind": KIND, "package": SOURCE, "sha256": reg["sha256"],
                    "generation": reg["generation"], "input": {"count": 1}}),
            )?;
            require(
                a["error"].is_object() && a.get("output").is_none(),
                format!("a start of a disabled producer should be refused: {a}"),
            )?;
            enable(app, main, SOURCE)?;
            let again = registration("1.1.0")?;
            let regs = listed()?;
            require(
                regs == vec![again.clone()],
                format!("enabled again, the source is listed again: {regs:?}"),
            )
        })(),
    )?;

    out["passed"] = json!(true);
    log("jobs", out);
    Ok(())
}
