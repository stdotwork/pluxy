//! The Codex parser's tests: the old desktop app's Codex cases with numbers counted by hand, the
//! corners of its rules, and where the parser finds a session's file.

use super::rollout::{self, Out};
use super::Codex;
use pluxy_program::Error;
use pluxy_usage::{Parser, Tokens};
use serde_json::{json, Value};
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

/// 2026-10-29 12:00:00 UTC and one day later, in unix seconds (counted from 1970-01-01: 20,755
/// days to the first).
const OCT29: i64 = 1_793_275_200;
const OCT30: i64 = OCT29 + 86_400;

/// Rows summed per hour and model, as the usage sink sums them.
#[derive(Default)]
struct Rows {
    rows: BTreeMap<(i64, String), Tokens>,
    cancel: bool,
}

impl Out for Rows {
    fn row(&mut self, time: i64, model: &str, tokens: &Tokens) -> Result<(), Error> {
        let key = (time.div_euclid(3600) * 3600, model.to_string());
        self.rows.entry(key).or_default().add(tokens);
        Ok(())
    }

    fn cancelled(&self) -> bool {
        self.cancel
    }
}

/// A rollout of lines, parsed.
fn parse(lines: &[Value]) -> Rows {
    let text: String = lines.iter().map(|l| format!("{l}\n")).collect();
    let mut rows = Rows::default();
    rollout::parse(text.as_bytes(), &mut rows).unwrap();
    rows
}

/// [input, output, cache read, cache write, reasoning, total, samples].
fn t(v: [u64; 7]) -> Tokens {
    Tokens::from_values(v)
}

fn at(seconds: i64) -> String {
    // Only the instants these tests use, as RFC 3339 (UTC).
    let day = seconds.div_euclid(86_400) - 20_755;
    let (h, m, s) = (
        seconds.rem_euclid(86_400) / 3600,
        seconds.rem_euclid(3600) / 60,
        seconds.rem_euclid(60),
    );
    // Day 0 is 2026-10-29.
    let date = match day {
        0 => "2026-10-29",
        1 => "2026-10-30",
        _ => panic!("no date for day {day}"),
    };
    format!("{date}T{h:02}:{m:02}:{s:02}Z")
}

fn context(model: &str) -> Value {
    json!({"type": "turn_context", "payload": {"model": model, "cwd": "/private"}})
}

fn count(time: i64, total: Value) -> Value {
    json!({"timestamp": at(time), "type": "event_msg", "payload": {"type": "token_count",
        "info": {"total_token_usage": total, "last_token_usage": {"input_tokens": 9999}}}})
}

#[test]
fn counts_cumulative_deltas_once_on_the_correct_day() {
    // The old app's case: a repeated event adds nothing (but is a sample); a day later the
    // counter's growth counts on that day, with no total given, so it is worked out.
    let rows = parse(&[
        context("gpt-test"),
        count(
            OCT29,
            json!({"input_tokens": 100, "cached_input_tokens": 20, "output_tokens": 30}),
        ),
        count(
            OCT29,
            json!({"input_tokens": 100, "cached_input_tokens": 20, "output_tokens": 30}),
        ),
        count(
            OCT30,
            json!({"input_tokens": 160, "cached_input_tokens": 30, "output_tokens": 50}),
        ),
    ]);
    assert_eq!(
        rows.rows,
        BTreeMap::from([
            // input 100 - 20, output 30, cache read 20, total 80 + 30 + 20; two samples.
            (
                (OCT29, "gpt-test".to_string()),
                t([80, 30, 20, 0, 0, 130, 2])
            ),
            // input (160 - 30) - 80 = 50, output 20, cache read 10, total 210 - 130.
            (
                (OCT30, "gpt-test".to_string()),
                t([50, 20, 10, 0, 0, 80, 1])
            ),
        ])
    );
}

#[test]
fn counts_a_token_count_without_a_cumulative_counter_as_it_is_and_keeps_nothing_private() {
    // The old app's case: only last_token_usage, with its own model and a folder.
    let line = json!({"timestamp": at(OCT29), "payload": {"type": "token_count", "info": {
        "last_token_usage": {"input_tokens": 90, "cached_input_tokens": 10, "output_tokens": 50,
            "reasoning_output_tokens": 5, "total_tokens": 155, "model": "gpt-5",
            "cwd": "C:\\private"}}}});
    let rows = parse(&[line.clone(), line]);
    // Each counts whole (nothing differenced): input 80, output 50, cache read 10, reasoning 5,
    // the event's own total 155.
    assert_eq!(
        rows.rows,
        BTreeMap::from([(
            (OCT29, "gpt-5".to_string()),
            t([160, 100, 20, 0, 10, 310, 2])
        )])
    );
    assert!(!format!("{:?}", rows.rows).contains("private"));
}

#[test]
fn a_counter_that_falls_starts_again_from_the_lower_value() {
    // Totals 1,000, then 400 (a rewound or rotated file), then 700: 1,000 + 0 + 300.
    let c = |input| json!({"input_tokens": input, "output_tokens": 0, "total_tokens": input});
    let rows = parse(&[
        context("m"),
        count(OCT29, c(1000)),
        count(OCT29 + 60, c(400)),
        count(OCT29 + 120, c(700)),
    ]);
    assert_eq!(
        rows.rows,
        BTreeMap::from([((OCT29, "m".to_string()), t([1300, 0, 0, 0, 0, 1300, 3]))])
    );
}

#[test]
fn takes_the_model_from_the_latest_turn_context_and_a_usage_model_wins() {
    let rows = parse(&[
        count(OCT29, json!({"input_tokens": 1, "total_tokens": 1})),
        context("First"),
        count(OCT29 + 60, json!({"input_tokens": 3, "total_tokens": 3})),
        context("Second\u{7}Model"),
        count(OCT29 + 120, json!({"input_tokens": 6, "total_tokens": 6})),
        // A model inside the usage object, as a string or as {"id"}, beats the context's.
        json!({"timestamp": at(OCT29 + 180), "payload": {"type": "token_count", "info": {
            "last_token_usage": {"input_tokens": 10, "total_tokens": 10, "model": {"id": "Own"}}}}}),
    ]);
    // No model before the first context (an empty name); a control character is dropped.
    assert_eq!(
        rows.rows,
        BTreeMap::from([
            ((OCT29, String::new()), t([1, 0, 0, 0, 0, 1, 1])),
            ((OCT29, "First".to_string()), t([2, 0, 0, 0, 0, 2, 1])),
            ((OCT29, "SecondModel".to_string()), t([3, 0, 0, 0, 0, 3, 1])),
            ((OCT29, "Own".to_string()), t([10, 0, 0, 0, 0, 10, 1])),
        ])
    );
}

#[test]
fn skips_what_is_not_a_usage_event() {
    let ok = |time| count(time, json!({"input_tokens": 5, "total_tokens": 5}));
    let mut text = String::new();
    for line in [
        // An event with no counter yet (Codex's first), and a counter with no known field.
        json!({"timestamp": at(OCT29), "type": "event_msg", "payload": {"type": "token_count",
            "info": null}}),
        json!({"timestamp": at(OCT29), "payload": {"type": "token_count", "info": {
            "last_token_usage": {"nothing": 1}}}}),
        // A line of another kind, and a prompt that mentions tokens.
        json!({"timestamp": at(OCT29), "type": "response_item", "payload": {"type": "message",
            "content": [{"text": "input_tokens 77"}]}}),
        // No time: it cannot be placed in an hour.
        json!({"payload": {"type": "token_count", "info": {"last_token_usage":
            {"input_tokens": 9, "total_tokens": 9}}}}),
        ok(OCT29),
    ] {
        text.push_str(&format!("{line}\n"));
        text.push_str("not json\n\n");
    }
    let mut rows = Rows::default();
    rollout::parse(text.as_bytes(), &mut rows).unwrap();
    assert_eq!(
        rows.rows,
        BTreeMap::from([((OCT29, String::new()), t([5, 0, 0, 0, 0, 5, 1]))])
    );
}

#[test]
fn reads_the_times_codex_and_other_writers_give() {
    let one = |stamp: Value| {
        let line = json!({"timestamp": stamp, "payload": {"type": "token_count", "info": {
            "last_token_usage": {"input_tokens": 1, "total_tokens": 1}}}});
        parse(&[line]).rows.keys().next().map(|k| k.0)
    };
    // The hour of 2026-10-29T12:00:00Z, however it is written.
    assert_eq!(one(json!("2026-10-29T12:34:56.789Z")), Some(OCT29));
    assert_eq!(one(json!("2026-10-29T14:34:56+02:00")), Some(OCT29));
    assert_eq!(one(json!("2026-10-29T09:04:56-03:30")), Some(OCT29));
    assert_eq!(one(json!(OCT29 + 30)), Some(OCT29));
    assert_eq!(one(json!((OCT29 + 30) * 1000)), Some(OCT29));
    assert_eq!(one(json!({"created": OCT29 + 30})), Some(OCT29));
    // A date alone is that day's start (UTC); things that are no time are none.
    assert_eq!(one(json!("2026-10-29")), Some(OCT29 - 12 * 3600));
    assert_eq!(one(json!("2026-13-29T12:00:00Z")), None);
    assert_eq!(one(json!("2026-10-29T12:00:00")), None);
    assert_eq!(one(json!("yesterday")), None);
    // The time may be inside the payload.
    let line = json!({"payload": {"type": "token_count", "timestamp": at(OCT29), "info": {
        "last_token_usage": {"input_tokens": 1, "total_tokens": 1}}}});
    assert_eq!(parse(&[line]).rows.keys().next().map(|k| k.0), Some(OCT29));
}

#[test]
fn stops_when_the_job_is_cancelled() {
    let mut rows = Rows {
        cancel: true,
        ..Rows::default()
    };
    let err = rollout::parse(b"{}\n", &mut rows).unwrap_err();
    assert_eq!(err.code, "cancelled");
}

// ---- where a session's file is

fn folder(name: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("usage-codex-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

fn write(path: &Path, text: &str) {
    std::fs::create_dir_all(path.parent().unwrap()).unwrap();
    std::fs::write(path, text).unwrap();
}

const NAME: &str = "rollout-2026-10-29T12-00-00-0198a000-0000-7000-8000-00000000000a.jsonl";

#[test]
fn reads_from_the_codex_home_and_names_a_file_by_its_name() {
    let home = Path::new("/home/someone");
    assert_eq!(
        Codex.default_roots(home),
        [
            home.join(".codex/sessions"),
            home.join(".codex/archived_sessions")
        ]
    );
    assert!(Codex.wanted(Path::new("/a/b/x.jsonl")) && Codex.wanted(Path::new("X.JSONL")));
    assert!(!Codex.wanted(Path::new("/a/b/x.json")) && !Codex.wanted(Path::new("/a/jsonl")));
    // A session moved to the archive is the same file.
    let active = home.join(".codex/sessions/2026/10/29").join(NAME);
    let archived = home.join(".codex/archived_sessions").join(NAME);
    assert_eq!(Codex.file_id(&active).as_deref(), Some(NAME));
    assert_eq!(Codex.file_id(&archived), Codex.file_id(&active));
}

#[test]
fn finds_a_file_by_name_where_codex_puts_it_and_elsewhere() {
    let dir = folder("locate");
    let roots = [dir.join("sessions"), dir.join("archived_sessions")];
    let dated = roots[0].join("2026/10/29").join(NAME);
    let other = roots[0].join("odd/place/deep.jsonl");
    let flat = roots[1].join("flat.jsonl");
    for p in [&dated, &other, &flat] {
        write(p, "{}\n");
    }
    assert_eq!(Codex.locate(&roots, NAME), Some(dated.clone()));
    assert_eq!(Codex.locate(&roots, "deep.jsonl"), Some(other));
    assert_eq!(Codex.locate(&roots, "flat.jsonl"), Some(flat));
    assert_eq!(Codex.locate(&roots, "missing.jsonl"), None);
    // Only a plain file name; never a path out of the roots, and never a link.
    for bad in [
        "../sessions/2026/10/29/x.jsonl",
        "/etc/passwd",
        "a/b.jsonl",
        "..",
        "",
    ] {
        assert_eq!(Codex.locate(&roots, bad), None, "{bad:?}");
    }
    let link = roots[1].join("link.jsonl");
    std::os::unix::fs::symlink(&dated, &link).unwrap();
    assert_eq!(Codex.locate(&roots, "link.jsonl"), None);
    let _ = std::fs::remove_dir_all(dir);
}

#[test]
fn a_session_in_both_folders_is_the_newer_file() {
    let dir = folder("copy");
    let roots = [dir.join("sessions"), dir.join("archived_sessions")];
    let dated = roots[0].join("2026/10/29").join(NAME);
    let archived = roots[1].join(NAME);
    write(&dated, "{}\n");
    std::thread::sleep(std::time::Duration::from_millis(20));
    write(&archived, "{}\n{}\n");
    // The listing puts the newest first, and the hub keeps that one's size and time.
    let (found, _) = pluxy_usage::scan(&roots, &|p| Codex.wanted(p), &|| false).unwrap();
    assert_eq!(found[0].path, archived);
    assert_eq!(Codex.locate(&roots, NAME), Some(archived));
    let _ = std::fs::remove_dir_all(dir);
}
