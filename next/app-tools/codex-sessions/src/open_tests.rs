//! The `open` operation, served over real pipes to a host played here: it reads the `call` frames
//! the tool sends for `pluxy/execution` (`next/app/app-host/README.md`, "Frames") and answers them
//! by hand, as the host does once the person has confirmed the launch.

use super::*;
use pluxy_program::{read_frame, serve, write_frame, Description, Identity, MAX_INBOUND};
use std::io::PipeWriter;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

const ID: &str = "019f1234-abcd-7000-8000-0000000000e1";
/// Text that must never appear in an answer, an error or a frame.
const CANARY: &str = "canary-session-text-7f3a";

/// A new empty Codex home, removed when dropped.
struct Home(PathBuf);

impl Home {
    fn new() -> Home {
        static N: AtomicU64 = AtomicU64::new(0);
        let p = std::env::temp_dir().join(format!(
            "codex-sessions-open-{}-{}",
            std::process::id(),
            N.fetch_add(1, Ordering::SeqCst)
        ));
        let _ = std::fs::remove_dir_all(&p);
        std::fs::create_dir_all(&p).unwrap();
        Home(std::fs::canonicalize(&p).unwrap())
    }

    /// Writes `bytes` at path (relative to the home), making its folders; its path.
    fn write(&self, path: &str, bytes: &str) -> PathBuf {
        let p = self.0.join(path);
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(&p, bytes).unwrap();
        p
    }

    /// The rollout of session ID in `folder` (`sessions/2026/08`, say).
    fn rollout(&self, folder: &str) -> PathBuf {
        self.write(
            &format!("{folder}/rollout-2026-08-04T10-00-00-{ID}.jsonl"),
            &format!("{{\"text\":\"{CANARY}\"}}\n"),
        )
    }

    /// A database that says session ID's rollout is `path`.
    fn database(&self, path: &str) {
        let dir = self.0.join("sqlite");
        std::fs::create_dir_all(&dir).unwrap();
        let _ = std::fs::remove_file(dir.join("state.db"));
        let c = rusqlite::Connection::open(dir.join("state.db")).unwrap();
        c.execute_batch("CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT)")
            .unwrap();
        c.execute("INSERT INTO threads VALUES (?1, ?2)", [ID, path])
            .unwrap();
    }
}

impl Drop for Home {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn text(p: &Path) -> String {
    p.to_str().unwrap().to_string()
}

/// What the host answers a call of execution's operation.
#[derive(Clone)]
enum Answer {
    /// What `prepare` answers for the file and folder it was asked about: the opener `xdg-open`.
    Prepared,
    /// `prepare` answers this output whatever it was asked.
    Output(Value),
    /// An error of this code.
    Error(&'static str, &'static str),
    /// The launch is started (`{"lifetime": "detached"}`).
    Launched,
}

/// What the host saw of one `open` call, and how it ended.
struct Opened {
    /// The `call` frames the tool sent: (package, op, input), in order.
    calls: Vec<(String, String, Value)>,
    /// Every other frame type the tool sent before its answer.
    others: Vec<String>,
    /// The call's last frame: its result or its error.
    end: Value,
}

/// Calls `open` with input on a tool reading home, playing the host: `prepare` is answered with
/// prepare, `launch` with launch.
fn open_with(home: &Path, input: Value, prepare: Answer, launch: Answer) -> Opened {
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
        json!({"type": "call", "id": "c-2", "op": "open", "input": input}),
    );
    let mut seen = Opened {
        calls: Vec::new(),
        others: Vec::new(),
        end: Value::Null,
    };
    loop {
        let f = read_frame(&mut out_r).unwrap().expect("a frame");
        match f["type"].as_str().unwrap() {
            "call" => {
                assert_eq!(f["for"], "c-2", "a call for the call it serves");
                let package = f["package"].as_str().unwrap().to_string();
                let op = f["op"].as_str().unwrap().to_string();
                let answer = if op == "prepare" { &prepare } else { &launch };
                seen.calls.push((package, op, f["input"].clone()));
                let reply = match answer {
                    Answer::Prepared => {
                        let (file, within) =
                            (f["input"]["file"].clone(), f["input"]["within"].clone());
                        let folder = Path::new(file.as_str().unwrap()).parent().unwrap();
                        json!({"type": "result", "id": f["id"], "output": {
                            "executable": "/usr/bin/xdg-open", "argv": [file],
                            "cwd": folder, "file": file, "within": within}})
                    }
                    Answer::Output(v) => json!({"type": "result", "id": f["id"], "output": v}),
                    Answer::Launched => {
                        json!({"type": "result", "id": f["id"], "output": {"lifetime": "detached"}})
                    }
                    Answer::Error(code, message) => json!({"type": "error", "id": f["id"],
                        "error": {"code": code, "message": message}}),
                };
                send(&mut to, reply);
            }
            "result" | "error" => {
                seen.end = f;
                break;
            }
            other => seen.others.push(other.to_string()),
        }
    }
    drop(to);
    served.join().unwrap().unwrap();
    seen
}

fn open(home: &Path, input: Value) -> Opened {
    open_with(home, input, Answer::Prepared, Answer::Launched)
}

fn code(o: &Opened) -> &str {
    o.end["error"]["code"].as_str().unwrap_or_default()
}

/// Nothing the tool said holds the canary or the Codex home's path.
fn says_nothing_of(o: &Opened, home: &Path) {
    let end = o.end.to_string();
    assert!(!end.contains(CANARY), "{end}");
    assert!(!end.contains(&*home.to_string_lossy()), "{end}");
}

/// The tool asked the host for nothing: no call, and no other frame.
fn asked_nothing(o: &Opened) {
    assert!(o.calls.is_empty(), "{:?}", o.calls);
    assert!(o.others.is_empty(), "{:?}", o.others);
}

fn keys(v: &Value) -> Vec<&str> {
    let mut k: Vec<&str> = v.as_object().unwrap().keys().map(String::as_str).collect();
    k.sort();
    k
}

#[test]
fn a_rollout_is_prepared_then_launched_as_prepared_detached_with_nothing_else() {
    let home = Home::new();
    let file = home.rollout("sessions/2026/08");
    let o = open(&home.0, json!({"id": ID}));
    assert_eq!(o.calls.len(), 2, "{:?}", o.calls);
    let (package, op, prepare) = &o.calls[0];
    assert_eq!(
        (package.as_str(), op.as_str()),
        ("pluxy/execution", "prepare")
    );
    // Exactly the open kind, the rollout and the folder it is in: no search path, no program, no
    // working folder, nothing the caller could steer the opener with.
    assert_eq!(
        *prepare,
        json!({"kind": "open", "file": text(&file), "within": text(&home.0.join("sessions"))})
    );
    let (package, op, launch) = &o.calls[1];
    assert_eq!(
        (package.as_str(), op.as_str()),
        ("pluxy/execution", "launch")
    );
    assert_eq!(
        *launch,
        json!({
            "executable": "/usr/bin/xdg-open",
            "argv": [text(&file)],
            "cwd": text(file.parent().unwrap()),
            "file": text(&file),
            "within": text(&home.0.join("sessions")),
            "lifetime": "detached",
        })
    );
    // No environment, standard input, secret key or program; and the tool asked the host for
    // nothing but these two calls (never a spawn of its own).
    for k in ["environment", "stdin", "key", "program", "search_path"] {
        assert!(launch.get(k).is_none() && prepare.get(k).is_none(), "{k}");
    }
    assert_eq!(keys(launch).len(), 6);
    assert!(o.others.is_empty(), "{:?}", o.others);
    assert_eq!(
        o.end,
        json!({"type": "result", "id": "c-2", "output": {"outcome": "opened"}})
    );
    says_nothing_of(&o, &home.0);
}

#[test]
fn an_archived_rollout_is_inside_the_archived_sessions_folder_not_the_codex_home() {
    let home = Home::new();
    let file = home.rollout("archived_sessions");
    let o = open(&home.0, json!({"id": format!("local:{ID}")}));
    assert_eq!(
        o.calls[0].2,
        json!({"kind": "open", "file": text(&file),
               "within": text(&home.0.join("archived_sessions"))})
    );
    assert_eq!(o.end["type"], "result");
}

#[test]
fn the_paths_named_are_resolved_even_when_the_codex_home_is_reached_through_a_link() {
    if !links_allowed() {
        return;
    }
    let home = Home::new();
    let file = home.rollout("sessions/2026/08");
    let link = home.0.join("link-to-home");
    make_link(&home.0, &link).unwrap();
    let o = open(&link, json!({"id": ID}));
    assert_eq!(
        o.calls[0].2,
        json!({"kind": "open", "file": text(&file), "within": text(&home.0.join("sessions"))})
    );
}

#[test]
fn a_session_with_no_rollout_or_a_bad_id_asks_the_host_for_nothing() {
    let home = Home::new();
    std::fs::create_dir_all(home.0.join("sessions")).unwrap();
    let o = open(&home.0, json!({"id": ID}));
    asked_nothing(&o);
    assert_eq!(code(&o), "not_found");
    says_nothing_of(&o, &home.0);
    for input in [
        json!({"id": "../etc/passwd"}),
        json!({"id": "a/b"}),
        json!({"id": ""}),
        json!({}),
    ] {
        let o = open(&home.0, input.clone());
        asked_nothing(&o);
        assert_eq!(code(&o), "invalid_request", "{input}");
    }
}

#[test]
fn a_file_that_is_not_a_rollout_is_not_opened() {
    let home = Home::new();
    // The database names a .jsonl inside sessions/ that is no rollout file.
    let notes = home.write("sessions/notes.jsonl", &format!("{CANARY}\n"));
    home.database(&text(&notes));
    let o = open(&home.0, json!({"id": ID}));
    asked_nothing(&o);
    assert_eq!(code(&o), "not_found");
    says_nothing_of(&o, &home.0);
}

#[test]
fn a_folder_or_a_link_outside_the_sessions_folders_is_not_opened() {
    if !links_allowed() {
        return;
    }
    let home = Home::new();
    // A folder with a rollout's name.
    let dir = home.0.join(format!(
        "sessions/2026/08/rollout-2026-08-04T10-00-00-{ID}.jsonl"
    ));
    std::fs::create_dir_all(&dir).unwrap();
    let o = open(&home.0, json!({"id": ID}));
    asked_nothing(&o);
    assert_eq!(code(&o), "not_found");
    // The database names the folder.
    home.database(&text(&dir));
    asked_nothing(&open(&home.0, json!({"id": ID})));
    std::fs::remove_dir(&dir).unwrap();
    // A link with a rollout's name to a file outside the Codex home, found by name or named by
    // the database.
    let outside = Home::new();
    let target = outside.write("outside.jsonl", &format!("{CANARY}\n"));
    make_link(&target, &dir).unwrap();
    let o = open(&home.0, json!({"id": ID}));
    asked_nothing(&o);
    assert_eq!(code(&o), "not_found");
    says_nothing_of(&o, &outside.0);
    // A rollout-named file in another folder of the Codex home (config, not sessions).
    std::fs::remove_file(&dir).unwrap();
    let elsewhere = home.write(
        &format!("other/rollout-2026-08-04T10-00-00-{ID}.jsonl"),
        "{}\n",
    );
    home.database(&text(&elsewhere));
    let o = open(&home.0, json!({"id": ID}));
    asked_nothing(&o);
    assert_eq!(code(&o), "not_found");
}

#[test]
fn a_link_to_a_rollout_inside_the_folder_is_opened_as_the_file_it_resolves_to() {
    if !links_allowed() {
        return;
    }
    let home = Home::new();
    let target = home.rollout("sessions/2026/08");
    let link = home.0.join("sessions/2026/rollout-link-to-it.jsonl");
    make_link(&target, &link).unwrap();
    home.database(&text(&link));
    let o = open(&home.0, json!({"id": ID}));
    assert_eq!(o.calls[0].2["file"], text(&target));
    assert_eq!(o.calls[1].2["argv"], json!([text(&target)]));
}

#[test]
fn a_refused_prepare_is_the_calls_error_and_nothing_is_launched() {
    let home = Home::new();
    home.rollout("sessions/2026/08");
    let o = open_with(
        &home.0,
        json!({"id": ID}),
        Answer::Error("extension_unavailable", "pluxy/execution is not enabled"),
        Answer::Launched,
    );
    assert_eq!(o.calls.len(), 1, "no launch after a refused prepare");
    assert_eq!(code(&o), "extension_unavailable");
    says_nothing_of(&o, &home.0);
}

#[test]
fn a_prepare_that_names_another_file_is_never_launched() {
    let home = Home::new();
    home.rollout("sessions/2026/08");
    let other = home.write("sessions/2026/08/rollout-other.jsonl", "{}\n");
    for output in [
        json!({"executable": "/usr/bin/xdg-open", "argv": [text(&other)], "cwd": "/x",
               "file": text(&other), "within": text(&home.0.join("sessions"))}),
        json!({"executable": "/usr/bin/xdg-open", "argv": ["/x"], "cwd": "/x",
               "file": "/x", "within": "/"}),
        json!("not an object"),
    ] {
        let o = open_with(
            &home.0,
            json!({"id": ID}),
            Answer::Output(output.clone()),
            Answer::Launched,
        );
        assert_eq!(o.calls.len(), 1, "{output}");
        assert_eq!(code(&o), "internal", "{output}");
        says_nothing_of(&o, &home.0);
    }
}

#[test]
fn a_launch_the_person_did_not_confirm_is_the_calls_error() {
    let home = Home::new();
    home.rollout("sessions/2026/08");
    let o = open_with(
        &home.0,
        json!({"id": ID}),
        Answer::Prepared,
        Answer::Error("permission", "the native confirmation was cancelled"),
    );
    assert_eq!(o.calls.len(), 2);
    assert_eq!(code(&o), "permission");
    assert_eq!(
        o.end["error"]["message"],
        "the native confirmation was cancelled"
    );
    says_nothing_of(&o, &home.0);
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
