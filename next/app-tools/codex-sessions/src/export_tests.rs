use super::*;
use pluxy_program::{read_frame, serve, write_frame, Description, Identity, MAX_INBOUND};
use std::io::PipeWriter;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

const ID: &str = "019f1234-abcd-7000-8000-0000000000e1";
/// Text that must never appear in an answer, an error or a name.
const CANARY: &str = "canary-session-text-7f3a";

/// A new empty Codex home, removed when dropped.
struct Home(PathBuf);

impl Home {
    fn new() -> Home {
        static N: AtomicU64 = AtomicU64::new(0);
        let p = std::env::temp_dir().join(format!(
            "codex-sessions-export-{}-{}",
            std::process::id(),
            N.fetch_add(1, Ordering::SeqCst)
        ));
        let _ = std::fs::remove_dir_all(&p);
        std::fs::create_dir_all(p.join("sessions/2026/08")).unwrap();
        Home(p)
    }

    /// Writes the rollout of session ID; its path.
    fn rollout(&self, bytes: &[u8]) -> PathBuf {
        let path = self.0.join(format!(
            "sessions/2026/08/rollout-2026-08-04T10-00-00-{ID}.jsonl"
        ));
        std::fs::write(&path, bytes).unwrap();
        path
    }
}

impl Drop for Home {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

/// Standard base64 decoded, written here apart from the library's encoder.
fn unbase64(text: &str) -> Vec<u8> {
    let value = |c: u8| match c {
        b'A'..=b'Z' => c - b'A',
        b'a'..=b'z' => c - b'a' + 26,
        b'0'..=b'9' => c - b'0' + 52,
        b'+' => 62,
        b'/' => 63,
        _ => panic!("not base64: {c}"),
    };
    let mut out = Vec::new();
    for group in text.as_bytes().chunks(4) {
        assert_eq!(group.len(), 4, "base64 is padded to four");
        let digits: Vec<u8> = group
            .iter()
            .take_while(|c| **c != b'=')
            .map(|c| value(*c))
            .collect();
        let n = digits
            .iter()
            .enumerate()
            .fold(0u32, |n, (i, d)| n | (*d as u32) << (18 - 6 * i));
        for i in 0..digits.len() - 1 {
            out.push((n >> (16 - 8 * i)) as u8);
        }
    }
    out
}

/// What the host answers a `save`.
#[derive(Clone)]
enum Answer {
    Saved,
    Cancelled,
    /// An error of this code.
    Error(&'static str),
}

/// What the host saw of one `export` call, and how it ended.
struct Exported {
    /// The `files` frames' ops, in order.
    ops: Vec<String>,
    /// The bytes the appends carried.
    bytes: Vec<u8>,
    /// The suggested name, when a save came.
    name: Option<String>,
    /// The call's last frame: its result or its error.
    end: Value,
}

/// Calls `export` of id on a tool reading home, playing the host: every append is answered with
/// the bytes so far, a save with answer.
fn export(home: &Path, input: Value, answer: Answer) -> Exported {
    let (in_r, in_w) = std::io::pipe().unwrap();
    let (mut out_r, out_w) = std::io::pipe().unwrap();
    let identity = Identity {
        id: "pluxy/codex-sessions".into(),
        sha256: "ab".repeat(32),
    };
    let description =
        Description::from_manifest(include_bytes!("../pluxy-extension.json")).unwrap();
    let tool = Sessions {
        home: Some(home.to_path_buf()),
    };
    let served =
        std::thread::spawn(move || serve(in_r, out_w, &identity, description, Arc::new(tool)));
    let mut to: PipeWriter = in_w;
    let send = |to: &mut PipeWriter, v: Value| write_frame(to, &v, MAX_INBOUND).unwrap();
    assert_eq!(read_frame(&mut out_r).unwrap().unwrap()["type"], "hello");
    send(
        &mut to,
        json!({"type": "call", "id": "c-2", "op": "export", "input": input}),
    );
    let mut seen = Exported {
        ops: Vec::new(),
        bytes: Vec::new(),
        name: None,
        end: Value::Null,
    };
    loop {
        let f = read_frame(&mut out_r).unwrap().expect("a frame");
        match f["type"].as_str().unwrap() {
            "files" => {
                assert_eq!(f["for"], "c-2");
                let op = f["op"].as_str().unwrap().to_string();
                seen.ops.push(op.clone());
                let reply = if op == "append" {
                    assert_eq!(f["input"]["offset"], seen.bytes.len());
                    seen.bytes
                        .extend(unbase64(f["input"]["data"].as_str().unwrap()));
                    json!({"type": "result", "id": f["id"], "output": {"size": seen.bytes.len()}})
                } else {
                    assert_eq!(f["input"].as_object().unwrap().len(), 1);
                    seen.name = f["input"]["name"].as_str().map(String::from);
                    match &answer {
                        Answer::Saved => json!({"type": "result", "id": f["id"],
                            "output": {"outcome": "saved", "size": seen.bytes.len()}}),
                        Answer::Cancelled => json!({"type": "result", "id": f["id"],
                            "output": {"outcome": "cancelled"}}),
                        Answer::Error(code) => json!({"type": "error", "id": f["id"],
                            "error": {"code": code, "message": "the host says no"}}),
                    }
                };
                send(&mut to, reply);
            }
            "result" | "error" => {
                seen.end = f;
                break;
            }
            other => panic!("unexpected {other} frame"),
        }
    }
    drop(to);
    served.join().unwrap().unwrap();
    seen
}

fn code(e: &Exported) -> &str {
    e.end["error"]["code"].as_str().unwrap_or_default()
}

/// No frame of what the tool said holds the canary, the Codex home's path or the file name.
fn says_nothing_of(e: &Exported, home: &Path) {
    let end = e.end.to_string();
    assert!(!end.contains(CANARY), "{end}");
    assert!(!end.contains(&*home.to_string_lossy()), "{end}");
}

#[test]
fn a_rollout_is_saved_byte_for_byte_in_appends_then_one_save_under_the_old_apps_name() {
    let home = Home::new();
    // Longer than one append frame holds, with every byte value of UTF-8 text in it, a CRLF, a NUL
    // and no final newline.
    let mut bytes = Vec::new();
    let mut n = 0;
    while bytes.len() < 2 * pluxy_program::APPEND_BYTES + 1234 {
        bytes.extend_from_slice(
            format!(
                "{{\"n\":{n},\"text\":\"{CANARY} é€😀 {}\"}}\r\n\0",
                "x".repeat(n % 97)
            )
            .as_bytes(),
        );
        n += 1;
    }
    bytes.extend_from_slice(b"{\"last\":true}");
    let path = home.rollout(&bytes);
    let seen = export(&home.0, json!({"id": ID}), Answer::Saved);
    assert_eq!(seen.ops, ["append", "append", "append", "save"]);
    assert_eq!(seen.bytes, bytes, "the saved bytes are the file's");
    assert_eq!(
        std::fs::read(&path).unwrap(),
        bytes,
        "the rollout is untouched"
    );
    let name = seen.name.clone().unwrap();
    let date = name
        .strip_prefix(&format!("session-{ID}-"))
        .and_then(|d| d.strip_suffix(".jsonl"))
        .unwrap_or_else(|| panic!("the name is session-<id>-<date>.jsonl: {name}"));
    assert_eq!(date.len(), 10, "{name}");
    assert!(
        date.char_indices().all(|(i, c)| if i == 4 || i == 7 {
            c == '-'
        } else {
            c.is_ascii_digit()
        }),
        "{name}"
    );
    // What the call answers is the outcome and the size, never the bytes.
    assert_eq!(
        seen.end,
        json!({"type": "result", "id": "c-2", "output": {"outcome": "saved", "size": bytes.len()}})
    );
    says_nothing_of(&seen, &home.0);
}

#[test]
fn a_session_id_with_local_is_exported_under_its_plain_id() {
    let home = Home::new();
    home.rollout(b"{}\n");
    let seen = export(
        &home.0,
        json!({"id": format!("local:{ID}")}),
        Answer::Cancelled,
    );
    assert!(seen.name.unwrap().starts_with(&format!("session-{ID}-")));
}

#[test]
fn a_cancelled_dialog_is_the_outcome_cancelled() {
    let home = Home::new();
    home.rollout(format!("{CANARY}\n").as_bytes());
    let seen = export(&home.0, json!({"id": ID}), Answer::Cancelled);
    assert_eq!(seen.ops, ["append", "save"]);
    assert_eq!(
        seen.end,
        json!({"type": "result", "id": "c-2", "output": {"outcome": "cancelled"}})
    );
    says_nothing_of(&seen, &home.0);
}

#[test]
fn the_hosts_refusal_is_the_calls_error_and_names_no_bytes() {
    let home = Home::new();
    home.rollout(format!("{CANARY}\n").as_bytes());
    let seen = export(&home.0, json!({"id": ID}), Answer::Error("unavailable"));
    assert_eq!(seen.end["type"], "error");
    assert_eq!(code(&seen), "unavailable");
    says_nothing_of(&seen, &home.0);
}

#[test]
fn a_rollout_over_the_limit_or_not_text_asks_the_host_for_nothing() {
    let home = Home::new();
    let path = home.rollout(&vec![b'x'; reader::MAX_EXPORT as usize + 1]);
    let seen = export(&home.0, json!({"id": ID}), Answer::Saved);
    assert!(seen.ops.is_empty(), "no bytes left the tool");
    assert_eq!(code(&seen), "unavailable");
    says_nothing_of(&seen, &home.0);
    std::fs::write(&path, [CANARY.as_bytes(), b"\n\xff\n"].concat()).unwrap();
    let seen = export(&home.0, json!({"id": ID}), Answer::Saved);
    assert!(seen.ops.is_empty());
    assert_eq!(code(&seen), "unavailable");
    says_nothing_of(&seen, &home.0);
}

#[test]
fn a_session_with_no_rollout_a_link_or_a_bad_id_asks_the_host_for_nothing() {
    let home = Home::new();
    // No rollout.
    let seen = export(&home.0, json!({"id": ID}), Answer::Saved);
    assert!(seen.ops.is_empty());
    assert_eq!(code(&seen), "not_found");
    says_nothing_of(&seen, &home.0);
    // A link to a file outside the Codex home, named as a rollout.
    let outside = Home::new();
    let target = outside.0.join("outside.jsonl");
    std::fs::write(&target, format!("{CANARY}\n")).unwrap();
    if links_allowed() {
        make_link(
            &target,
            home.0.join(format!(
                "sessions/2026/08/rollout-2026-08-04T10-00-00-{ID}.jsonl"
            )),
        )
        .unwrap();
        let seen = export(&home.0, json!({"id": ID}), Answer::Saved);
        assert!(seen.ops.is_empty());
        assert_eq!(code(&seen), "not_found");
        says_nothing_of(&seen, &outside.0);
    }
    // IDs that are paths, or none.
    for input in [
        json!({"id": "../etc/passwd"}),
        json!({"id": "a/b"}),
        json!({"id": ""}),
        json!({}),
    ] {
        let seen = export(&home.0, input.clone(), Answer::Saved);
        assert!(seen.ops.is_empty(), "{input}");
        assert_eq!(code(&seen), "invalid_request", "{input}");
    }
}

/// A symbolic link at link to target: on Windows a folder link when target is a folder.
fn make_link(target: impl AsRef<Path>, link: impl AsRef<Path>) -> std::io::Result<()> {
    #[cfg(unix)]
    return std::os::unix::fs::symlink(target, link);
    #[cfg(windows)]
    return if target.as_ref().is_dir() {
        std::os::windows::fs::symlink_dir(target, link)
    } else {
        std::os::windows::fs::symlink_file(target, link)
    };
}

/// Whether this account may make symbolic links (on Windows that takes a privilege or Developer
/// Mode). When it may not, says so on stderr: a test that needs links is then skipped, and the CI
/// summary counts the skip.
fn links_allowed() -> bool {
    #[cfg(unix)]
    return true;
    #[cfg(windows)]
    {
        static ALLOWED: std::sync::OnceLock<bool> = std::sync::OnceLock::new();
        let allowed = *ALLOWED.get_or_init(|| {
            let probe = std::env::temp_dir().join(format!("links-allowed-{}", std::process::id()));
            let _ = std::fs::remove_file(&probe);
            let made = std::os::windows::fs::symlink_file("target", &probe).is_ok();
            let _ = std::fs::remove_file(&probe);
            made
        });
        if !allowed {
            eprintln!("skipped: this account cannot make a symbolic link");
        }
        allowed
    }
}
