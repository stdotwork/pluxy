//! The Claude Code parser's tests: the old desktop app's Claude cases with numbers counted by hand,
//! the keys that make a message one event across files, the corners of its rules, and the file
//! IDs that name no folder.

use super::transcript::{self, Out};
use super::{hash, Claude};
use pluxy_program::Error;
use pluxy_usage::{LineReader, Parser, Tokens};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};

/// 2026-10-29 12:00:00 UTC in unix seconds (20,755 days after 1970-01-01).
const OCT29: i64 = 1_793_275_200;
const NOON: &str = "2026-10-29T12:00:00.000Z";

/// What the parser gave out, in order: (hour, model, key, tokens); key None for a plain row.
#[derive(Default)]
struct Got {
    out: Vec<(i64, String, Option<String>, Tokens)>,
    cancel: bool,
}

impl Out for Got {
    fn row(&mut self, time: i64, model: &str, tokens: &Tokens) -> Result<(), Error> {
        self.out
            .push((time.div_euclid(3600) * 3600, model.into(), None, *tokens));
        Ok(())
    }

    fn event(&mut self, time: i64, model: &str, key: &str, tokens: &Tokens) -> Result<(), Error> {
        self.out.push((
            time.div_euclid(3600) * 3600,
            model.into(),
            Some(key.into()),
            *tokens,
        ));
        Ok(())
    }

    fn cancelled(&self) -> bool {
        self.cancel
    }
}

fn parse(lines: &[Value]) -> Got {
    let text: String = lines.iter().map(|l| format!("{l}\n")).collect();
    let mut got = Got::default();
    transcript::parse(&mut LineReader::new(text.as_bytes()), &mut got).unwrap();
    got
}

/// [input, output, cache read, cache write, reasoning, total, samples].
fn t(v: [u64; 7]) -> Tokens {
    Tokens::from_values(v)
}

/// An assistant line of one content block: usage [input, cache write, cache read, output].
fn block(at: &str, id: &str, request: Option<&str>, session: &str, u: [u64; 4]) -> Value {
    let mut line = json!({"type": "assistant", "sessionId": session, "timestamp": at,
        "cwd": "/home/CANARY-FOLDER/p",
        "message": {"id": id, "role": "assistant", "model": "claude-sonnet-4-5",
            "content": [{"type": "text", "text": "CANARY-REPLY"}],
            "usage": {"input_tokens": u[0], "cache_creation_input_tokens": u[1],
                "cache_read_input_tokens": u[2], "output_tokens": u[3]}}});
    if let Some(r) = request {
        line["requestId"] = json!(r);
    }
    line
}

#[test]
fn keeps_the_latest_claude_message_usage_instead_of_counting_updates() {
    // The old app's case: two lines of message "one", output 10 then 20: 100 + 20 = 120, once.
    let lines: Vec<Value> = [10, 20]
        .iter()
        .map(|output| {
            json!({"timestamp": NOON, "message": {"id": "one", "model": "claude",
                "usage": {"input_tokens": 100, "output_tokens": output}}})
        })
        .collect();
    let got = parse(&lines);
    // No request or session ID: a plain row of the file.
    assert_eq!(
        got.out,
        vec![(OCT29, "claude".into(), None, t([100, 20, 0, 0, 0, 120, 1]))]
    );
}

#[test]
fn parses_claude_message_usage() {
    // The old app's case: 11 + 7 + 5 + 3 = 26.
    let got = parse(&[
        json!({"timestamp": NOON, "message": {"model": "claude-sonnet",
        "usage": {"input_tokens": 11, "cache_creation_input_tokens": 5,
            "cache_read_input_tokens": 3, "output_tokens": 7}}}),
    ]);
    assert_eq!(
        got.out,
        vec![(
            OCT29,
            "claude-sonnet".into(),
            None,
            t([11, 7, 3, 5, 0, 26, 1])
        )]
    );
}

#[test]
fn a_response_of_several_content_blocks_is_one_event_keyed_by_its_ids() {
    // Three blocks of one response, the last with the final output: input 10, output 60, cache
    // read 1,000, cache write 200, total 1,270, once.
    let got = parse(&[
        block(NOON, "msg_1", Some("req_1"), "s-1", [10, 200, 1000, 50]),
        block(NOON, "msg_1", Some("req_1"), "s-1", [10, 200, 1000, 50]),
        block(NOON, "msg_1", Some("req_1"), "s-1", [10, 200, 1000, 60]),
    ]);
    // The key: FNV-1a 128 of "request", "msg_1", "req_1" joined by 0xff, worked out apart from
    // this code (a Python implementation of the published FNV-1a).
    let key = "bf287f1984716c4b7cfef7bb83c47b3b";
    assert_eq!(
        got.out,
        vec![(
            OCT29,
            "claude-sonnet-4-5".into(),
            Some(key.into()),
            t([10, 60, 1000, 200, 0, 1270, 1])
        )]
    );
}

#[test]
fn a_branch_copy_of_a_message_has_the_same_key_whatever_its_session() {
    // `/branch` copies the history into a new session: the message keeps its ID and request ID.
    let original = parse(&[block(NOON, "msg_1", Some("req_1"), "s-1", [1, 0, 0, 1])]);
    let branch = parse(&[block(NOON, "msg_1", Some("req_1"), "s-2", [1, 0, 0, 1])]);
    assert_eq!(original.out[0].2, branch.out[0].2);
    // Another response of the same message ID (a new request) is another event.
    let other = parse(&[block(NOON, "msg_1", Some("req_2"), "s-1", [1, 0, 0, 1])]);
    assert_ne!(original.out[0].2, other.out[0].2);
}

#[test]
fn without_a_request_id_the_key_is_the_message_and_its_session() {
    let got = parse(&[
        block(NOON, "msg_2", None, "s-2", [1, 0, 0, 1]),
        // Neither request nor session: a plain row.
        json!({"timestamp": NOON, "message": {"id": "msg_3", "model": "m",
            "usage": {"input_tokens": 2}}}),
    ]);
    // FNV-1a 128 of "session", "msg_2", "s-2", worked out apart from this code.
    assert_eq!(
        got.out[0].2.as_deref(),
        Some("b5f29f2fcecaac4498eaa45c868b2ce5")
    );
    assert_eq!(got.out[1].2, None);
    assert_eq!(got.out.len(), 2);
}

#[test]
fn messages_come_out_in_the_order_they_first_appear_at_their_last_lines_time() {
    let got = parse(&[
        block(NOON, "msg_a", Some("r"), "s", [1, 0, 0, 0]),
        block(NOON, "msg_b", Some("r"), "s", [2, 0, 0, 0]),
        // msg_a's last line is an hour later: it counts in that hour.
        block(
            "2026-10-29T13:05:00Z",
            "msg_a",
            Some("r"),
            "s",
            [3, 0, 0, 0],
        ),
    ]);
    let got: Vec<(i64, u64)> = got.out.iter().map(|o| (o.0, o.3.input)).collect();
    assert_eq!(got, vec![(OCT29 + 3600, 3), (OCT29, 2)]);
}

#[test]
fn a_synthetic_message_has_no_model() {
    let mut line = block(NOON, "c0ffee", None, "s", [0, 0, 0, 3]);
    line["message"]["model"] = json!("<synthetic>");
    let got = parse(&[line]);
    assert_eq!(got.out[0].1, "");
    assert_eq!(got.out[0].3, t([0, 3, 0, 0, 0, 3, 1]));
}

#[test]
fn lines_without_a_message_id_count_as_they_are() {
    let got = parse(&[
        // A line's own usage (no message object), twice: two rows of 5 + 1.
        json!({"timestamp": NOON, "model": "m", "usage": {"input_tokens": 5, "output_tokens": 1}}),
        json!({"timestamp": NOON, "model": "m", "usage": {"input_tokens": 5, "output_tokens": 1}}),
        // A list of records: each counted, at its own time.
        json!([{"timestamp": NOON, "usage": {"output_tokens": 4}},
            {"timestamp": "2026-10-29T13:00:00Z", "usage": {"output_tokens": 6}}]),
        // A `messages` list.
        json!({"messages": [{"timestamp": NOON, "usage": {"input_tokens": 7}}]}),
    ]);
    let got: Vec<(i64, String, u64)> = got
        .out
        .iter()
        .map(|o| (o.0, o.1.clone(), o.3.total))
        .collect();
    assert_eq!(
        got,
        vec![
            (OCT29, "m".into(), 6),
            (OCT29, "m".into(), 6),
            (OCT29, "".into(), 4),
            (OCT29 + 3600, "".into(), 6),
            (OCT29, "".into(), 7),
        ]
    );
}

#[test]
fn skips_what_is_not_usage_or_has_no_time() {
    let got = parse(&[
        json!({"type": "user", "timestamp": NOON, "message": {"role": "user", "content": "CANARY-PROMPT"}}),
        json!({"type": "summary", "summary": "CANARY-SUMMARY", "leafUuid": "x"}),
        // A message object with no usage: the line's own usage is not read instead.
        json!({"timestamp": NOON, "usage": {"input_tokens": 9}, "message": {"id": "m", "content": []}}),
        // No readable time.
        json!({"message": {"id": "msg_t", "usage": {"input_tokens": 9}}, "timestamp": "yesterday"}),
        json!({"usage": {"input_tokens": 9}}),
        json!("not an object"),
    ]);
    assert!(got.out.is_empty(), "{:?}", got.out);
    // Not JSON at all.
    let mut none = Got::default();
    transcript::parse(&mut LineReader::new(&b"{{\n\n"[..]), &mut none).unwrap();
    assert!(none.out.is_empty());
}

#[test]
fn keeps_no_prompt_reply_folder_or_raw_id() {
    let got = parse(&[block(
        NOON,
        "msg_RAW-ID",
        Some("req_RAW-ID"),
        "s-RAW-ID",
        [1, 0, 0, 1],
    )]);
    let text = format!("{:?}", got.out);
    for canary in ["CANARY", "RAW-ID"] {
        assert!(!text.contains(canary), "{text}");
    }
    // The positive control: the same text holds what it does carry.
    assert!(text.contains("claude-sonnet-4-5"));
}

#[test]
fn stops_when_the_job_is_cancelled() {
    let mut got = Got {
        cancel: true,
        ..Got::default()
    };
    let err = transcript::parse(&mut LineReader::new(&b"{}\n"[..]), &mut got).unwrap_err();
    assert_eq!(err.code, "cancelled");
}

#[test]
fn the_hash_is_fnv1a_128() {
    // The published FNV-1a 128 values for "", "a" and "foobar".
    assert_eq!(hash(&[""]), "6c62272e07bb014262b821756295c58d");
    assert_eq!(hash(&["a"]), "d228cb696f1a8caf78912b704e4a8964");
    assert_eq!(hash(&["foobar"]), "343e1662793c64bf6f0d3597ba446f18");
}

fn folder(name: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("usage-claude-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

fn write(path: &Path, text: &str) {
    std::fs::create_dir_all(path.parent().unwrap()).unwrap();
    std::fs::write(path, text).unwrap();
}

#[test]
fn reads_from_the_claude_home_and_names_no_folder_in_a_file_id() {
    let home = folder("home");
    let root = home.join(".claude").join("projects");
    assert_eq!(Claude.default_roots(&home), vec![root.clone()]);
    let file = root.join("-home-user-proj").join("s-1.jsonl");
    let sub = root
        .join("-home-user-proj")
        .join("s-1")
        .join("subagents")
        .join("agent-1.jsonl");
    write(&file, "{}\n");
    write(&sub, "{}\n");
    write(&root.join("loose.jsonl"), "{}\n");
    // The project folder's hash, worked out apart from this code.
    let folder_id = "b876720a7b305d3b1f909e3dbbdecd07";
    let id = Claude.file_id_under(&root, &file).unwrap();
    assert_eq!(id, format!("{folder_id}/s-1.jsonl"));
    let sub_id = Claude.file_id_under(&root, &sub).unwrap();
    assert_eq!(sub_id, format!("{folder_id}/s-1/subagents/agent-1.jsonl"));
    assert!(!id.contains("home") && !sub_id.contains("home"));
    let roots = vec![root.clone()];
    assert_eq!(Claude.locate(&roots, &id), Some(file.clone()));
    assert_eq!(Claude.locate(&roots, &sub_id), Some(sub));
    assert_eq!(
        Claude.locate(&roots, "loose.jsonl"),
        Some(root.join("loose.jsonl"))
    );
    // An ID naming no transcript under the roots.
    let other = format!("{}/s-1.jsonl", hash(&["-home-user-other"]));
    for bad in [
        other.as_str(),
        "-home-user-proj/s-1.jsonl",
        &format!("{folder_id}/../{folder_id}/s-1.jsonl"),
        &format!("{folder_id}//s-1.jsonl"),
        &file.to_string_lossy(),
        "missing.jsonl",
    ] {
        assert_eq!(Claude.locate(&roots, bad), None, "{bad}");
    }
    std::fs::remove_dir_all(home).unwrap();
}
