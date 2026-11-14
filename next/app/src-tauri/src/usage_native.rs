//! The native test's check of the usage hub, pluxy/agent-usage, with the test-only fixture parser
//! pluxy/usage-fixture (native-test builds with `artifact-test-trust` only). It drives the real app
//! through the window's own commands, the real native confirmations and real page webviews, with
//! both tools and their relay pages packed by `native/usage.ts`, whose `expect.json` holds every
//! expected value, written by hand:
//!
//! - install and enable: both, and pluxy/usage-peer (a tool the parser's test archive imports),
//!   install from the curated registry and are enabled through their native confirmations;
//! - ingest is the host's alone: the window, the hub's own page and the parser (a tool) calling
//!   the hub's deliver operation are refused; the window's and the page's calls of `sources`, and
//!   the parser's relay of the peer's `summary`, on the same routes are the positive controls;
//! - roots: the hub's page sets the parser's root, which asks a native confirmation naming it;
//! - sync: the page drives a sync with `sync` calls until it is done, the host running the parser's
//!   jobs; `report` then gives exactly the expected values (the hub's buckets in the page's zone);
//!   a repeated sync reads no file and changes nothing; after a file grows and another is added,
//!   the next sync reads just those two and the report counts each record once;
//! - report rights: the parser's page calling the hub's `report` (or `ingest`) is refused, the
//!   hub's own page on the same route being the positive control;
//! - cancelled mid-file and disabled: while the parser waits between two parts of a file, it is
//!   disabled; its job is cancelled, the part delivered leaves no row, the report is unchanged and
//!   shows the parser archived with its rows kept; the next sync schedules nothing and starts no
//!   parser;
//! - the Codex parser (pluxy/usage-codex, a real package): installed and enabled after the cancel
//!   step, it reads a Codex home written by hand in the run's HOME with its default roots; the
//!   report for its source equals the expected two-date totals (one session copied to the archive
//!   counts once), a repeated sync reads nothing, and after a session grows and another is rewound
//!   (truncated) each is read and counted once; no prompt canary of the Codex home reaches a
//!   report, the hub's store or (by run.ts) the app's output;
//! - the Claude Code parser (pluxy/usage-claude, a real package): installed and enabled after the
//!   Codex step, it reads a Claude Code home written by hand in the run's HOME with its default
//!   root, in the same syncs as the Codex parser; the reports equal the expected totals for Claude
//!   Code alone, for Claude Code and Codex together, and in a second time zone (one response's
//!   content-block lines count once, a branch session's copy of the original's history counts
//!   once, also after the branch grows); no prompt, reply, folder or raw message ID reaches a
//!   report, the hub's store or (by run.ts) the app's output;
//! - the OpenCode parser (pluxy/usage-opencode, a real package): installed and enabled after the
//!   Claude Code step, it reads an OpenCode home written by hand in the run's HOME (a database with
//!   OpenCode's old and current message tables, and JSON message files) with its default roots;
//!   the reports equal the expected totals (a message in the JSON store and in both tables counts
//!   once, as does a fork's copy of its parent's history), a repeated sync reads nothing, and after
//!   a message is added to the database only its session is read; the database stays byte for byte
//!   as written with no file added beside it; no prompt, title, folder or raw ID reaches a report,
//!   the hub's store or (by run.ts) the app's output; disabled, the parser is not scheduled;
//! - the hub's own page: opened, it starts the hub;
//! - idle: once nothing uses them, no hub, parser or peer process is left.
//!
//! One line on stderr: `native-test: usage <JSON>`.

use super::admission::{dialog_text, ok, press, require};
use super::program::processes;
use super::routing::{call, enable, install, showing, Page};
use super::{eval, log, wait_for};
use serde_json::{json, Value};
use std::io::Write;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager, Webview};

/// The page's call's result, failing on an error, with no native confirmation showing meanwhile.
fn unasked(page: &mut Page, id: &str, op: &str, input: Value) -> Result<Value, String> {
    let call = page.send(id, op, input.clone())?;
    let deadline = Instant::now() + Duration::from_secs(60);
    loop {
        if let Some(r) = eval(
            page.webview(),
            &format!("window.__route.replies[{}] ?? null", json!(call)),
        )
        .filter(|v| !v.is_null())
        {
            require(
                r.get("result").is_some(),
                format!("the page's {op} {input}: {r}"),
            )?;
            return Ok(r["result"].clone());
        }
        if showing() {
            press("alt+c")?;
            return Err(format!(
                "the page's {op} {input} asked a native confirmation"
            ));
        }
        if Instant::now() > deadline {
            return Err(format!("the page's {op} {input}: no answer"));
        }
        std::thread::sleep(Duration::from_millis(200));
    }
}

fn forbidden(r: &Value, what: &str) -> Result<(), String> {
    require(
        r["error"]["code"] == "forbidden" && r.get("result").is_none(),
        format!("{what} should be refused as forbidden: {r}"),
    )
}

/// Drives a sync from the page (started with `now` when asked) until the hub says it is done:
/// its last status, and the most parts it saw staged for the parser while it ran.
fn synced(page: &mut Page, hub: &str, now: bool) -> Result<Value, String> {
    let mut r = unasked(page, hub, "sync", json!({"now": now}))?;
    let deadline = Instant::now() + Duration::from_secs(120);
    while r["running"] == true {
        if Instant::now() > deadline {
            return Err(format!("the sync did not end: {r}"));
        }
        std::thread::sleep(Duration::from_millis(300));
        r = unasked(page, hub, "sync", json!({}))?;
    }
    Ok(r)
}

/// The sync's counts for source: listed, changed, read and failed.
fn counts(status: &Value, source: &str) -> Value {
    status["sources"]
        .as_array()
        .and_then(|a| a.iter().find(|s| s["id"] == source))
        .map(|s| {
            json!({"listed": s["listed"], "changed": s["changed"], "read": s["read"],
                "failed": s["failed"]})
        })
        .unwrap_or(Value::Null)
}

/// Whether report got matches want: every field equal but the calendar and hours, which want
/// gives as their nonzero cells (and the calendar's start and length); the sync status is not
/// compared.
fn matches(got: &Value, want: &Value, what: &str) -> Result<(), String> {
    let mut rest = got.clone();
    let mut wanted = want.clone();
    for o in [&mut rest, &mut wanted] {
        if let Some(m) = o.as_object_mut() {
            for k in ["calendar", "hours", "sync"] {
                m.remove(k);
            }
        }
    }
    require(
        rest == wanted,
        format!("{what}: the report\n  {rest}\nis not the expected\n  {wanted}"),
    )?;
    let cal = &got["calendar"];
    let want_cal = &want["calendar"];
    let totals = cal["totals"].as_array().cloned().unwrap_or_default();
    require(
        cal["start"] == want_cal["start"] && Some(totals.len() as u64) == want_cal["days"].as_u64(),
        format!(
            "{what}: the calendar should start {} and hold {} days: {cal}",
            want_cal["start"], want_cal["days"]
        ),
    )?;
    // The calendar's days, by date, from its start.
    let start = want_cal["start"].as_str().unwrap_or_default();
    let nonzero = want_cal["nonzero"].as_object().cloned().unwrap_or_default();
    let mut found = 0;
    for (i, v) in totals.iter().enumerate() {
        let date = day_after(start, i as i64);
        let expected = nonzero.get(&date).cloned().unwrap_or(json!(0));
        require(
            *v == expected,
            format!("{what}: the calendar's {date} is {v}, not {expected}"),
        )?;
        if expected != 0 {
            found += 1;
        }
    }
    require(
        found == nonzero.len(),
        format!("{what}: the calendar misses some of {nonzero:?}"),
    )?;
    let hours = got["hours"].as_array().cloned().unwrap_or_default();
    require(
        hours.len() == 7
            && hours
                .iter()
                .all(|r| r.as_array().is_some_and(|r| r.len() == 24)),
        format!("{what}: the hours should be 7 rows of 24: {}", got["hours"]),
    )?;
    for (d, row) in hours.iter().enumerate() {
        for (h, v) in row.as_array().into_iter().flatten().enumerate() {
            let expected = want["hours"][format!("{d}:{h}")].clone();
            let expected = if expected.is_null() {
                json!(0)
            } else {
                expected
            };
            require(
                *v == expected,
                format!("{what}: hours {d}:{h} is {v}, not {expected}"),
            )?;
        }
    }
    Ok(())
}

/// The date n days after date (both `YYYY-MM-DD`), by the civil calendar.
fn day_after(date: &str, n: i64) -> String {
    let p: Vec<i64> = date.split('-').filter_map(|x| x.parse().ok()).collect();
    let (mut y, mut m, mut d) = (p[0], p[1], p[2]);
    for _ in 0..n {
        let leap = (y % 4 == 0 && y % 100 != 0) || y % 400 == 0;
        let len = match m {
            4 | 6 | 9 | 11 => 30,
            2 if leap => 29,
            2 => 28,
            _ => 31,
        };
        d += 1;
        if d > len {
            d = 1;
            m += 1;
            if m > 12 {
                m = 1;
                y += 1;
            }
        }
    }
    format!("{y:04}-{m:02}-{d:02}")
}

/// Each expected report of a phase, asked from the page and compared.
fn reports(page: &mut Page, hub: &str, cases: &Value, what: &str) -> Result<usize, String> {
    let mut n = 0;
    for case in cases.as_array().into_iter().flatten() {
        let got = unasked(page, hub, "report", case["input"].clone())?;
        matches(&got, &case["output"], &format!("{what} {}", case["input"]))?;
        n += 1;
    }
    Ok(n)
}

/// Appends lines to path, making its folder.
fn write_lines(spec: &Value) -> Result<(), String> {
    let path = Path::new(spec["path"].as_str().unwrap_or_default());
    std::fs::create_dir_all(path.parent().ok_or("no folder")?).map_err(|e| e.to_string())?;
    let mut f = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(path)
        .map_err(|e| format!("{}: {e}", path.display()))?;
    for line in spec["lines"].as_array().into_iter().flatten() {
        writeln!(f, "{}", line.as_str().unwrap_or_default()).map_err(|e| e.to_string())?;
    }
    Ok(())
}

/// Rewrites the file of spec to its first `keep` lines.
fn keep_lines(spec: &Value) -> Result<(), String> {
    let path = Path::new(spec["path"].as_str().unwrap_or_default());
    let text = std::fs::read_to_string(path).map_err(|e| format!("{}: {e}", path.display()))?;
    let keep = spec["keep"].as_u64().unwrap_or(0) as usize;
    let kept: String = text.split_inclusive('\n').take(keep).collect();
    std::fs::write(path, kept).map_err(|e| format!("{}: {e}", path.display()))
}

/// Fails when any canary is in the text.
fn no_canary(canaries: &[String], text: &str, what: &str) -> Result<(), String> {
    match canaries.iter().find(|c| text.contains(c.as_str())) {
        Some(c) => Err(format!("{what} holds the canary {c}")),
        None => Ok(()),
    }
}

/// Every file under dir, as lossy text, for a canary search.
fn folder_text(dir: &Path) -> Result<String, String> {
    let mut text = String::new();
    let mut files = 0;
    for entry in std::fs::read_dir(dir).map_err(|e| format!("{}: {e}", dir.display()))? {
        let path = entry.map_err(|e| e.to_string())?.path();
        if path.is_file() {
            text.push_str(&String::from_utf8_lossy(
                &std::fs::read(&path).map_err(|e| format!("{}: {e}", path.display()))?,
            ));
            files += 1;
        }
    }
    require(files > 0, format!("{} holds no file", dir.display()))?;
    Ok(text)
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let expect: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("expect.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let hub = expect["hub"].as_str().unwrap_or_default().to_string();
    let parser = expect["parser"].as_str().unwrap_or_default().to_string();
    let peer = expect["peer"].as_str().unwrap_or_default().to_string();
    let store = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("device-packages")
        .join("store");
    let program = |id: &str| {
        store
            .join(expect["sha256"][id].as_str().unwrap_or_default())
            .join(expect["bin"][id].as_str().unwrap_or_default())
    };
    let mut out = json!({});
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("usage {name}: {e}"));

    // ---- install and enable: the peer and the parser first, so a missing hub fails at its own
    // step; the peer is enabled before the parser, which requires it.
    step(
        "install",
        (|| {
            for id in [&peer, &parser, &hub] {
                install(
                    main,
                    "curated",
                    id,
                    expect["versions"][id].as_str().unwrap_or_default(),
                )?;
                require(
                    program(id).is_file(),
                    format!("{} is not installed", program(id).display()),
                )?;
            }
            let text = enable(app, main, &hub)?;
            require(
                text.contains(expect["kind"].as_str().unwrap_or_default()),
                format!("the hub's confirmation should name the kind it consumes: {text}"),
            )?;
            enable(app, main, &peer)?;
            enable(app, main, &parser)?;
            Ok(())
        })(),
    )?;

    // ---- ingest is the host's alone; sources on the same routes is allowed.
    let mut page = Page::open(app, main, &hub)?;
    step(
        "ingest",
        (|| {
            let r = call(main, &hub, "ingest", json!({}))?;
            require(
                r["ok"] == false && r["error"].as_str().unwrap_or_default().contains("host"),
                format!("the window calling the hub's ingest should be refused: {r}"),
            )?;
            let r = call(main, &hub, "sources", json!({}))?;
            require(r["ok"] == true, format!("the window's sources: {r}"))?;
            forbidden(
                &page.call(&hub, "ingest", json!({}))?,
                "the hub's page calling ingest",
            )?;
            let s = unasked(&mut page, &hub, "sources", json!({}))?;
            require(
                s["sources"]
                    .as_array()
                    .is_some_and(|a| a.iter().any(|x| x["id"] == parser.as_str())),
                format!("the hub's sources should list the parser: {s}"),
            )?;
            let want = &expect["peer_call"];
            let r = call(
                main,
                &parser,
                "relay",
                json!({"package": peer, "op": "summary", "input": {"items": want["items"]}}),
            )?;
            require(
                r["ok"] == true
                    && r["value"]["output"]["count"] == want["count"]
                    && r["value"]["output"]["longest"] == want["longest"],
                format!("the parser's relay of the peer's summary should succeed: {r}"),
            )?;
            let r = call(
                main,
                &parser,
                "relay",
                json!({"package": hub, "op": "ingest", "input": {}}),
            )?;
            require(
                r["ok"] == true && r["value"]["error"]["code"] == "permission",
                format!("the parser calling the hub's ingest should be refused: {r}"),
            )
        })(),
    )?;

    // ---- roots, confirmed; the interval, kept.
    step(
        "roots",
        (|| {
            let root = expect["root"].as_str().unwrap_or_default();
            let call = page.send(&hub, "roots", json!({"source": parser, "roots": [root]}))?;
            let text = dialog_text(app)?;
            std::thread::sleep(Duration::from_millis(1100));
            press("alt+o")?;
            let r = page.reply(&call)?;
            std::thread::sleep(Duration::from_millis(1100));
            require(
                text.contains(&format!("{hub} roots")) && text.contains(root),
                format!("the roots confirmation should name the hub's roots and {root}: {text}"),
            )?;
            require(
                r["result"]["roots"] == json!([root]),
                format!("the confirmed roots: {r}"),
            )?;
            let r = unasked(&mut page, &hub, "settings", json!({"interval_minutes": 15}))?;
            require(
                r["interval_minutes"] == 15,
                format!("the interval set: {r}"),
            )
        })(),
    )?;

    // ---- sync, repeat, append.
    step(
        "sync",
        (|| {
            let s = synced(&mut page, &hub, true)?;
            require(
                counts(&s, &parser) == expect["syncs"]["first"] && s["interval_minutes"] == 15,
                format!("the first sync: {s}"),
            )?;
            reports(&mut page, &hub, &expect["reports"]["first"], "first")?;
            let s = synced(&mut page, &hub, true)?;
            require(
                counts(&s, &parser) == expect["syncs"]["repeat"],
                format!("a repeated sync should read nothing: {s}"),
            )?;
            reports(&mut page, &hub, &expect["reports"]["first"], "repeated")?;
            write_lines(&expect["writes"]["append"])?;
            write_lines(&expect["writes"]["add"])?;
            let s = synced(&mut page, &hub, true)?;
            require(
                counts(&s, &parser) == expect["syncs"]["append"],
                format!("the sync after the append: {s}"),
            )?;
            let n = reports(&mut page, &hub, &expect["reports"]["second"], "appended")?;
            out["reports"] = json!(n + 2);
            Ok(())
        })(),
    )?;
    page.close(main)?;

    // ---- report rights: the parser's page is refused what the hub's page was given.
    step(
        "rights",
        (|| {
            let mut other = Page::open(app, main, &parser)?;
            forbidden(
                &other.call(
                    &hub,
                    "report",
                    expect["reports"]["first"][0]["input"].clone(),
                )?,
                "the parser's page calling the hub's report",
            )?;
            forbidden(
                &other.call(&hub, "ingest", json!({}))?,
                "the parser's page calling the hub's ingest",
            )?;
            other.close(main)
        })(),
    )?;

    // ---- cancelled mid-file by a disable; the parser archived with its rows, not scheduled.
    let mut page = Page::open(app, main, &hub)?;
    step(
        "cancel",
        (|| {
            write_lines(&expect["writes"]["pause"])?;
            let mut s = unasked(&mut page, &hub, "sync", json!({"now": true}))?;
            let staged = |s: &Value| {
                s["sources"]
                    .as_array()
                    .and_then(|a| a.iter().find(|x| x["id"] == parser.as_str()))
                    .and_then(|x| x["staged"].as_u64())
                    .unwrap_or(0)
            };
            let deadline = Instant::now() + Duration::from_secs(60);
            while staged(&s) == 0 {
                require(
                    s["running"] == true && Instant::now() < deadline,
                    format!("the sync should stage a part of the paused file: {s}"),
                )?;
                std::thread::sleep(Duration::from_millis(300));
                s = unasked(&mut page, &hub, "sync", json!({}))?;
            }
            out["staged"] = json!(staged(&s));
            ok(
                main,
                "device_enable",
                json!({"id": parser, "enabled": false}),
            )?;
            let s = synced(&mut page, &hub, false)?;
            require(
                counts(&s, &parser)["read"] == 0,
                format!("the cancelled sync read no file: {s}"),
            )?;
            reports(
                &mut page,
                &hub,
                &expect["reports"]["cancelled"],
                "cancelled",
            )?;
            let s = synced(&mut page, &hub, true)?;
            require(
                s["sources"] == json!([]),
                format!("a sync with the parser disabled schedules nothing: {s}"),
            )?;
            require(
                processes(&program(&parser)).is_empty(),
                "a disabled parser was started",
            )
        })(),
    )?;
    page.close(main)?;

    // ---- the Codex parser: a real package, its default roots in the run's HOME.
    let codex = expect["codex"]["id"]
        .as_str()
        .unwrap_or_default()
        .to_string();
    let canaries: Vec<String> = expect["codex"]["canaries"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|c| c.as_str().map(String::from))
        .collect();
    let mut page = Page::open(app, main, &hub)?;
    step(
        "codex",
        (|| {
            install(
                main,
                "curated",
                &codex,
                expect["versions"][&codex].as_str().unwrap_or_default(),
            )?;
            require(
                program(&codex).is_file(),
                format!("{} is not installed", program(&codex).display()),
            )?;
            enable(app, main, &codex)?;
            let mut seen = String::new();
            let mut check =
                |page: &mut Page, sync: &str, cases: &Value, what: &str| -> Result<(), String> {
                    let s = synced(page, &hub, true)?;
                    require(
                        counts(&s, &codex) == expect["codex"]["syncs"][sync],
                        format!("the {what} sync of the Codex source: {s}"),
                    )?;
                    for case in cases.as_array().into_iter().flatten() {
                        let got = unasked(page, &hub, "report", case["input"].clone())?;
                        seen.push_str(&got.to_string());
                        matches(&got, &case["output"], &format!("codex {what}"))?;
                    }
                    Ok(())
                };
            check(
                &mut page,
                "first",
                &expect["codex"]["reports"]["first"],
                "first",
            )?;
            check(
                &mut page,
                "repeat",
                &expect["codex"]["reports"]["first"],
                "repeated",
            )?;
            write_lines(&expect["codex"]["writes"]["append"])?;
            keep_lines(&expect["codex"]["writes"]["rewind"])?;
            check(
                &mut page,
                "after",
                &expect["codex"]["reports"]["after"],
                "appended and rewound",
            )?;
            // A third sync changes nothing.
            check(
                &mut page,
                "repeat",
                &expect["codex"]["reports"]["after"],
                "last",
            )?;
            // No prompt, reply, folder or key text in any report or in the hub's store.
            no_canary(&canaries, &seen, "a report")?;
            let data = app
                .path()
                .app_data_dir()
                .map_err(|e| e.to_string())?
                .join("tools")
                .join(&hub);
            // The same search finds what the store does hold (a positive control).
            let stored = folder_text(&data)?;
            require(
                stored.contains("gpt-5.5-mini") && seen.contains("gpt-5.5-mini"),
                "the store and the reports should hold the Codex models: the canary search reads nothing",
            )?;
            no_canary(&canaries, &stored, "the hub's store")?;
            wait_for("the Codex parser to stop when idle", || {
                processes(&program(&codex)).is_empty().then_some(())
            })?;
            Ok(())
        })(),
    )?;
    page.close(main)?;

    // ---- the Claude Code parser: a real package, its default root in the run's HOME, the Codex
    // parser still enabled, so one sync runs both and the reports compare them.
    let claude = expect["claude"]["id"]
        .as_str()
        .unwrap_or_default()
        .to_string();
    let canaries: Vec<String> = expect["claude"]["canaries"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|c| c.as_str().map(String::from))
        .collect();
    let mut page = Page::open(app, main, &hub)?;
    step(
        "claude",
        (|| {
            install(
                main,
                "curated",
                &claude,
                expect["versions"][&claude].as_str().unwrap_or_default(),
            )?;
            require(
                program(&claude).is_file(),
                format!("{} is not installed", program(&claude).display()),
            )?;
            enable(app, main, &claude)?;
            let mut seen = String::new();
            let mut check = |page: &mut Page, sync: &str, cases: &str| -> Result<(), String> {
                let s = synced(page, &hub, true)?;
                require(
                    counts(&s, &claude) == expect["claude"]["syncs"][sync],
                    format!("the {sync} sync of the Claude Code source: {s}"),
                )?;
                // The Codex source, unchanged since its step, is listed and reads nothing.
                require(
                    counts(&s, &codex) == expect["claude"]["syncs"]["codex"],
                    format!("the {sync} sync of the Codex source beside Claude Code: {s}"),
                )?;
                for case in expect["claude"]["reports"][cases]
                    .as_array()
                    .into_iter()
                    .flatten()
                {
                    let got = unasked(page, &hub, "report", case["input"].clone())?;
                    seen.push_str(&got.to_string());
                    let what = format!("claude {sync} ({})", case["name"]);
                    matches(&got, &case["output"], &what)?;
                }
                Ok(())
            };
            check(&mut page, "first", "first")?;
            check(&mut page, "repeat", "first")?;
            // The branch session grows; its copy of the original's history is read again and
            // still counted once.
            write_lines(&expect["claude"]["writes"]["append"])?;
            check(&mut page, "after", "after")?;
            // No prompt, reply, folder, summary or raw message or request ID in any report or in
            // the hub's store.
            no_canary(&canaries, &seen, "a report")?;
            let data = app
                .path()
                .app_data_dir()
                .map_err(|e| e.to_string())?
                .join("tools")
                .join(&hub);
            // The same search finds what the store does hold (a positive control).
            let stored = folder_text(&data)?;
            require(
                stored.contains("claude-sonnet-4-5") && seen.contains("claude-sonnet-4-5"),
                "the store and the reports should hold the Claude models: the canary search reads nothing",
            )?;
            no_canary(&canaries, &stored, "the hub's store")?;
            wait_for("the Claude Code parser to stop when idle", || {
                processes(&program(&claude)).is_empty().then_some(())
            })?;
            Ok(())
        })(),
    )?;
    page.close(main)?;

    // ---- the OpenCode parser: a real package, its default roots in the run's HOME, the Codex and
    // Claude Code parsers still enabled.
    let opencode = expect["opencode"]["id"]
        .as_str()
        .unwrap_or_default()
        .to_string();
    let canaries: Vec<String> = expect["opencode"]["canaries"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|c| c.as_str().map(String::from))
        .collect();
    let files = &expect["opencode"]["files"];
    let path = |k: &str| PathBuf::from(files[k].as_str().unwrap_or_default());
    // The database is byte for byte the copy written as want, and its folder holds nothing new.
    let untouched = |want: &str, what: &str| -> Result<(), String> {
        let db = std::fs::read(path("db")).map_err(|e| e.to_string())?;
        let copy = std::fs::read(path(want)).map_err(|e| e.to_string())?;
        require(db == copy, format!("{what}: the OpenCode database changed"))?;
        let mut names: Vec<String> = std::fs::read_dir(path("folder"))
            .map_err(|e| e.to_string())?
            .filter_map(|e| e.ok().and_then(|e| e.file_name().into_string().ok()))
            .collect();
        names.sort();
        require(
            json!(names) == files["entries"],
            format!("{what}: the OpenCode database's folder holds {names:?}"),
        )
    };
    let mut page = Page::open(app, main, &hub)?;
    step(
        "opencode",
        (|| {
            install(
                main,
                "curated",
                &opencode,
                expect["versions"][&opencode].as_str().unwrap_or_default(),
            )?;
            require(
                program(&opencode).is_file(),
                format!("{} is not installed", program(&opencode).display()),
            )?;
            enable(app, main, &opencode)?;
            let mut seen = String::new();
            let mut check = |page: &mut Page, sync: &str, cases: &str| -> Result<(), String> {
                let s = synced(page, &hub, true)?;
                require(
                    counts(&s, &opencode) == expect["opencode"]["syncs"][sync],
                    format!("the {sync} sync of the OpenCode source: {s}"),
                )?;
                for other in [&codex, &claude] {
                    let name = if *other == codex { "codex" } else { "claude" };
                    require(
                        counts(&s, other) == expect["opencode"]["syncs"][name],
                        format!("the {sync} sync of the {name} source beside OpenCode: {s}"),
                    )?;
                }
                for case in expect["opencode"]["reports"][cases]
                    .as_array()
                    .into_iter()
                    .flatten()
                {
                    let got = unasked(page, &hub, "report", case["input"].clone())?;
                    seen.push_str(&got.to_string());
                    let what = format!("opencode {sync} ({})", case["name"]);
                    matches(&got, &case["output"], &what)?;
                }
                Ok(())
            };
            check(&mut page, "first", "first")?;
            untouched("before", "after the first sync")?;
            check(&mut page, "repeat", "first")?;
            untouched("before", "after the repeated sync")?;
            // A new message in the database: only its session is read again.
            std::fs::copy(path("after"), path("db")).map_err(|e| e.to_string())?;
            check(&mut page, "after", "after")?;
            untouched("after", "after the third sync")?;
            // No prompt, title, summary, reply, folder or raw session or message ID in any report or in
            // the hub's store.
            no_canary(&canaries, &seen, "a report")?;
            let data = app
                .path()
                .app_data_dir()
                .map_err(|e| e.to_string())?
                .join("tools")
                .join(&hub);
            // The same search finds what the store does hold (a positive control).
            let stored = folder_text(&data)?;
            require(
                stored.contains("gpt-5.5-mini") && seen.contains("gpt-5.5-mini"),
                "the store and the reports should hold the OpenCode models: the canary search reads nothing",
            )?;
            no_canary(&canaries, &stored, "the hub's store")?;
            wait_for("the OpenCode parser to stop when idle", || {
                processes(&program(&opencode)).is_empty().then_some(())
            })?;
            // Disabled, the parser is not scheduled and scans nothing.
            ok(
                main,
                "device_enable",
                json!({"id": opencode, "enabled": false}),
            )?;
            let s = synced(&mut page, &hub, true)?;
            require(
                counts(&s, &opencode).is_null(),
                format!("a sync with the OpenCode parser disabled schedules it: {s}"),
            )?;
            require(
                processes(&program(&opencode)).is_empty(),
                "a disabled OpenCode parser was started",
            )?;
            untouched("after", "after the sync with the parser disabled")
        })(),
    )?;
    page.close(main)?;

    // ---- the hub's own page starts it.
    step(
        "page",
        (|| {
            let t = Instant::now();
            wait_for("the hub to stop when idle", || {
                processes(&program(&hub)).is_empty().then_some(())
            })?;
            out["idle_stop_secs"] = json!(t.elapsed().as_secs_f64());
            let route = format!("device/{hub}/usage");
            wait_for(&format!("{route} in the navigation"), || {
                eval(
                    main,
                    &format!("document.querySelector('a[href=\"#/{route}\"]') !== null"),
                )
                .filter(|v| v == true)
            })?;
            main.eval(format!("location.hash = {}", json!(format!("#/{route}"))))
                .map_err(|e| e.to_string())?;
            let label = wait_for(&format!("a page webview showing {hub}"), || {
                app.webviews().into_iter().find_map(|(label, w)| {
                    (label != crate::MAIN
                        && eval(&w, "document.querySelector('iframe')?.src ?? null")
                            .and_then(|v| v.as_str().map(|s| s.contains(&format!("/{hub}/"))))
                            == Some(true))
                    .then_some(label)
                })
            })?;
            wait_for("the hub's own page to start it", || {
                (!processes(&program(&hub)).is_empty()).then_some(())
            })?;
            std::thread::sleep(Duration::from_secs(2));
            main.eval("location.hash = '#/device'")
                .map_err(|e| e.to_string())?;
            wait_for(&format!("{label} to close"), || {
                app.get_webview(&label).is_none().then_some(())
            })?;
            Ok(())
        })(),
    )?;

    // ---- idle: no tool is left running.
    step(
        "idle",
        (|| {
            wait_for("the hub to stop when idle", || {
                processes(&program(&hub)).is_empty().then_some(())
            })?;
            wait_for("the peer to stop when idle", || {
                processes(&program(&peer)).is_empty().then_some(())
            })?;
            require(
                processes(&program(&parser)).is_empty(),
                "a parser process is left",
            )
        })(),
    )?;

    out["passed"] = json!(true);
    log("usage", out);
    Ok(())
}
