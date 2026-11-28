use crate::{apply, default_selection, is_unmade, pinned_read, preview, settings_of, Default};
use pluxy_program::Error;
use pluxy_transactions::{sha256_of, Selection, Vault};
use serde_json::{json, Map, Value};
use std::cell::{Cell, RefCell};
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

fn tmp() -> PathBuf {
    tmp_named("")
}

fn tmp_named(name: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!(
        "claude-code-test{name}-{}-{:?}",
        std::process::id(),
        std::thread::current().id()
    ));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    d
}

/// The host's vault, in memory: backups and journals, as Connect uses them.
#[derive(Default)]
struct Mem {
    backups: RefCell<BTreeMap<String, String>>,
    journals: RefCell<BTreeMap<String, Option<String>>>,
    next: Cell<u64>,
}

impl Vault for Mem {
    fn vault(&self, op: &str, f: Map<String, Value>) -> Result<Value, Error> {
        let s = |k: &str| f.get(k).and_then(Value::as_str).unwrap_or_default();
        let id = |p: &str| {
            self.next.set(self.next.get() + 1);
            format!("{p}-{}", self.next.get())
        };
        match op {
            "backup.put" => {
                let id = id("b");
                self.backups
                    .borrow_mut()
                    .insert(id.clone(), s("data").into());
                Ok(json!({"backup": id}))
            }
            "backup.get" => {
                let data = self.backups.borrow().get(s("backup")).cloned();
                let data = data.ok_or_else(|| Error::not_found("no backup"))?;
                Ok(json!({"backup": s("backup"), "label": "", "data": data}))
            }
            "journal.begin" => {
                let id = id("j");
                self.journals.borrow_mut().insert(id.clone(), None);
                Ok(json!({"journal": id}))
            }
            "journal.commit" | "journal.end" => {
                let outcome = if op == "journal.commit" {
                    "committed"
                } else {
                    s("outcome")
                };
                self.journals
                    .borrow_mut()
                    .insert(s("journal").into(), Some(outcome.into()));
                Ok(json!({"journal": s("journal"), "outcome": outcome}))
            }
            _ => Err(Error::unsupported(op.to_string())),
        }
    }
}

/// What the page sends to Connect, around the preview it read (its targets, and create_folder
/// when it named one), with the key as the secret secret-1.
fn connect(preview: &Value) -> (Value, Map<String, Value>) {
    let mut input = json!({
        "targets": preview["targets"],
        "base_url": "http://127.0.0.1:41999",
        "key": "secret-1",
        "opus": "walk-model-1",
        "sonnet": "walk-model-2",
        "haiku": "walk-model-1",
        "fable": "",
        "startup_model": "opus",
        "subagent_model": "",
        "max_context_tokens": 200000,
        "auto_compact_pct": 90,
    });
    if let Some(f) = preview.get("create_folder") {
        input["create_folder"] = f.clone();
    }
    let mut secrets = Map::new();
    secrets.insert("secret-1".into(), json!("k-test"));
    (input, secrets)
}

/// The mode a folder made with the system's defaults gets here (what Claude Code gives
/// `~/.claude` when it makes it).
#[cfg(unix)]
fn default_mode(home: &Path) -> u32 {
    use std::os::unix::fs::PermissionsExt;
    let probe = home.join("probe");
    std::fs::create_dir(&probe).unwrap();
    let m = std::fs::metadata(&probe).unwrap().permissions().mode() & 0o777;
    std::fs::remove_dir(probe).unwrap();
    m
}

#[test]
fn a_home_where_claude_code_never_ran_reads_as_not_set_up_and_nothing_is_made() {
    let (home, data) = (tmp(), tmp_named("-data"));
    let folder = home.join(".claude");
    let v = preview(
        &Mem::default(),
        data.clone(),
        &json!({}),
        Some(home.clone().into()),
    )
    .unwrap();
    assert_eq!(v["settings"]["state"], "not-configured", "{v}");
    assert_eq!(
        (v["applied"].clone(), v["edited"].clone()),
        (json!(false), json!([false]))
    );
    assert_eq!(v["create_folder"], json!(folder));
    let file = folder.join("settings.json");
    let target = std::fs::canonicalize(&home)
        .unwrap()
        .join(".claude")
        .join("settings.json");
    assert_eq!(
        v["targets"],
        json!([{"selected": file, "link": null, "target": target,
            "identity": {"link": null, "parent": null, "file": null}, "sha256": null}])
    );
    assert!(is_unmade(&json!({"targets": v["targets"]})));
    assert!(!folder.exists(), "a folder was made");
    // Once Claude Code made its folder, the file in it is read as before.
    std::fs::create_dir(&folder).unwrap();
    match default_selection(Some(home.clone().into())).unwrap() {
        Default::File(s) => assert_eq!(s, Selection::File(file)),
        Default::Unmade { .. } => panic!("the folder is there"),
    }
    let v = preview(&Mem::default(), data, &json!({}), Some(home.clone().into())).unwrap();
    assert!(v.get("create_folder").is_none(), "{v}");
    assert!(!is_unmade(&json!({"targets": v["targets"]})));
    assert!(default_selection(None).is_err());
    std::fs::remove_dir_all(home).unwrap();
}

#[test]
fn connect_makes_claude_codes_folder_as_claude_code_would_and_writes_the_settings() {
    let (home, data) = (tmp(), tmp_named("-data"));
    let folder = home.join(".claude");
    let vault = Mem::default();
    let h = || Some(home.clone().into());
    let v = preview(&vault, data.clone(), &json!({}), h()).unwrap();
    let (input, secrets) = connect(&v);
    apply(&vault, data.clone(), &input, &secrets, h()).unwrap();
    let written = std::fs::read_to_string(folder.join("settings.json")).unwrap();
    let w: Value = serde_json::from_str(&written).unwrap();
    assert_eq!(w["env"]["ANTHROPIC_BASE_URL"], "http://127.0.0.1:41999");
    assert_eq!(w["env"]["ANTHROPIC_AUTH_TOKEN"], "k-test");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = |p: &Path| std::fs::metadata(p).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode(&folder), default_mode(&home));
        assert_eq!(mode(&folder.join("settings.json")), 0o600);
    }
    // Read again, it is connected, and its file is pinned as any other.
    let v = preview(
        &vault,
        data,
        &json!({"base_url": "http://127.0.0.1:41999"}),
        h(),
    )
    .unwrap();
    assert_eq!(v["settings"]["state"], "configured", "{v}");
    assert_eq!(v["applied"], true);
    assert!(v.get("create_folder").is_none());
    std::fs::remove_dir_all(home).unwrap();
}

#[test]
fn connect_makes_only_claude_codes_own_folder_for_the_preview_it_names() {
    let (home, data) = (tmp(), tmp_named("-data"));
    let h = || Some(home.clone().into());
    let v = preview(&Mem::default(), data.clone(), &json!({}), h()).unwrap();
    let (input, secrets) = connect(&v);
    let mut elsewhere = input.clone();
    elsewhere["create_folder"] = json!(home.join("other"));
    let mut moved = input.clone();
    moved["targets"][0]["target"] = json!(home.join("other").join("settings.json"));
    for bad in [elsewhere, moved] {
        let e = apply(&Mem::default(), data.clone(), &bad, &secrets, h()).unwrap_err();
        assert!(
            e.message.contains("create_folder is Claude Code's folder"),
            "{e}"
        );
    }
    // Without create_folder the target is no pin.
    let mut plain = input.clone();
    plain.as_object_mut().unwrap().remove("create_folder");
    assert!(apply(&Mem::default(), data.clone(), &plain, &secrets, h()).is_err());
    // A key the app did not deliver, or a setting out of range: refused before any folder.
    let mut no_key = input.clone();
    no_key["key"] = json!("secret-2");
    let mut no_model = input.clone();
    no_model["opus"] = json!("");
    for bad in [no_key, no_model] {
        assert!(apply(&Mem::default(), data.clone(), &bad, &secrets, h()).is_err());
    }
    let names: Vec<_> = std::fs::read_dir(&home).unwrap().collect();
    assert!(names.is_empty(), "made {names:?}");
    std::fs::remove_dir_all(home).unwrap();
}

#[test]
fn a_folder_made_since_the_preview_is_written_in_but_a_settings_file_made_since_is_not() {
    let (home, data) = (tmp(), tmp_named("-data"));
    let folder = home.join(".claude");
    let h = || Some(home.clone().into());
    let v = preview(&Mem::default(), data.clone(), &json!({}), h()).unwrap();
    let (input, secrets) = connect(&v);
    // Claude Code ran meanwhile and wrote its own settings: they are not the file previewed.
    std::fs::create_dir(&folder).unwrap();
    std::fs::write(folder.join("settings.json"), "{\"model\": \"haiku\"}\n").unwrap();
    let e = apply(&Mem::default(), data.clone(), &input, &secrets, h()).unwrap_err();
    assert!(e.message.contains("changed since it was read"), "{e}");
    assert_eq!(
        std::fs::read_to_string(folder.join("settings.json")).unwrap(),
        "{\"model\": \"haiku\"}\n"
    );
    // Only the folder: the file is still the one previewed (none), so Connect writes it.
    std::fs::remove_file(folder.join("settings.json")).unwrap();
    apply(&Mem::default(), data, &input, &secrets, h()).unwrap();
    assert!(folder.join("settings.json").is_file());
    std::fs::remove_dir_all(home).unwrap();
}

#[test]
fn a_connect_that_fails_takes_away_the_folder_it_made() {
    let home = tmp();
    // The data folder is a file: the write fails after the folder is made.
    let data = tmp_named("-data").join("file");
    std::fs::write(&data, "").unwrap();
    let h = || Some(home.clone().into());
    let v = preview(&Mem::default(), data.clone(), &json!({}), h()).unwrap();
    let (input, secrets) = connect(&v);
    assert!(apply(&Mem::default(), data, &input, &secrets, h()).is_err());
    assert!(!home.join(".claude").exists());
    std::fs::remove_dir_all(home).unwrap();
}

/// A link at at to the folder target: a symbolic link, or on Windows a junction (the folder link
/// any account may make), so these tests run on every account.
fn link_dir(target: &Path, at: &Path) {
    #[cfg(unix)]
    std::os::unix::fs::symlink(target, at).unwrap();
    #[cfg(windows)]
    {
        let out = std::process::Command::new("cmd")
            .args(["/C", "mklink", "/J"])
            .arg(at)
            .arg(target)
            .output()
            .unwrap();
        assert!(out.status.success(), "{out:?}");
    }
}

/// Takes away the link at at that link_dir made.
fn unlink_dir(at: &Path) {
    #[cfg(unix)]
    std::fs::remove_file(at).unwrap();
    #[cfg(windows)]
    std::fs::remove_dir(at).unwrap();
}

#[test]
fn a_link_at_claude_is_read_as_its_folder_and_connect_makes_nothing_through_it() {
    let (home, data) = (tmp(), tmp_named("-data"));
    let elsewhere = tmp_named("-elsewhere");
    let h = || Some(home.clone().into());
    // Before the preview (as a dotfile manager makes it): the folder it links to is read, as
    // before, and Connect makes no folder.
    link_dir(&elsewhere, &home.join(".claude"));
    let v = preview(&Mem::default(), data.clone(), &json!({}), h()).unwrap();
    assert!(v.get("create_folder").is_none(), "{v}");
    assert!(!is_unmade(&json!({"targets": v["targets"]})));
    unlink_dir(&home.join(".claude"));
    // After a preview that found no folder: refused, nothing made through it.
    let v = preview(&Mem::default(), data.clone(), &json!({}), h()).unwrap();
    let (input, secrets) = connect(&v);
    link_dir(&elsewhere, &home.join(".claude"));
    let e = apply(&Mem::default(), data, &input, &secrets, h()).unwrap_err();
    assert!(e.message.contains("changed since it was read"), "{e}");
    let made: Vec<_> = std::fs::read_dir(&elsewhere).unwrap().collect();
    assert!(made.is_empty(), "made {made:?}");
    assert!(std::fs::symlink_metadata(home.join(".claude"))
        .unwrap()
        .file_type()
        .is_symlink());
    unlink_dir(&home.join(".claude"));
    std::fs::remove_dir_all(home).unwrap();
    std::fs::remove_dir_all(elsewhere).unwrap();
}

/// The error Claude Code's program answers a call of op with input, served as the app calls it.
fn call_error(op: &str, input: Value) -> String {
    use pluxy_program::{read_frame, serve, write_frame, Description, Identity, MAX_INBOUND};
    let (in_r, mut in_w) = std::io::pipe().unwrap();
    let (mut out_r, out_w) = std::io::pipe().unwrap();
    let identity = Identity {
        id: "pluxy/claude-code".into(),
        sha256: "ab".repeat(32),
    };
    let description =
        Description::from_manifest(include_bytes!("../pluxy-extension.json")).unwrap();
    let served = std::thread::spawn(move || {
        serve(
            in_r,
            out_w,
            &identity,
            description,
            std::sync::Arc::new(crate::ClaudeCode),
        )
    });
    assert_eq!(read_frame(&mut out_r).unwrap().unwrap()["type"], "hello");
    let call = json!({"type": "call", "id": "c-1", "op": op, "input": input});
    write_frame(&mut in_w, &call, MAX_INBOUND).unwrap();
    let f = read_frame(&mut out_r).unwrap().expect("an answer");
    drop(in_w);
    served.join().unwrap().unwrap();
    assert_eq!(
        (f["type"].as_str(), f["id"].as_str()),
        (Some("error"), Some("c-1")),
        "{f}"
    );
    f["error"]["message"]
        .as_str()
        .unwrap_or_default()
        .to_string()
}

#[test]
fn other_writes_say_in_words_that_the_folder_does_not_exist_yet() {
    let (home, data) = (tmp(), tmp_named("-data"));
    let v = preview(&Mem::default(), data, &json!({}), Some(home.clone().into())).unwrap();
    let input = json!({"targets": v["targets"]});
    // The page maps "is not applied to these targets" to its own words (no copy from before).
    for op in ["clear", "restore"] {
        let e = call_error(op, input.clone());
        assert!(e.contains("is not applied to these targets"), "{op}: {e}");
        assert!(
            e.contains("Claude Code's folder does not exist yet"),
            "{op}: {e}"
        );
    }
    for op in ["backup-create", "backup-restore"] {
        let e = call_error(op, input.clone());
        assert!(e.contains("Connect makes it"), "{op}: {e}");
    }
    std::fs::remove_dir_all(home).unwrap();
}

/// A preview whose pin says sha256 of pinned, and then the bytes read.
fn read_of(pinned: &[u8], read: &[u8]) -> (Value, Option<Vec<u8>>) {
    (
        json!({"targets": [{"sha256": sha256_of(Some(pinned))}]}),
        Some(read.to_vec()),
    )
}

#[test]
fn settings_read_back_are_the_pinned_files_or_the_read_is_refused() {
    // The file changed between the pin and the read once: read again, and the second read holds.
    let n = Cell::new(0);
    let (_, bytes) = pinned_read(|| {
        n.set(n.get() + 1);
        Ok::<_, Error>(if n.get() == 1 {
            read_of(b"{}", b"{\"x\": 1}")
        } else {
            read_of(b"{\"x\": 1}", b"{\"x\": 1}")
        })
    })
    .unwrap();
    assert_eq!((n.get(), bytes.unwrap()), (2, b"{\"x\": 1}".to_vec()));
    // Changed again: refused.
    let e = pinned_read(|| Ok(read_of(b"{}", b"[]"))).unwrap_err();
    assert!(e.message.contains("changed while it was read"), "{e}");
    // No file on either side is the same file.
    let (v, b) = pinned_read(|| Ok((json!({"targets": [{"sha256": null}]}), None))).unwrap();
    assert_eq!((v["targets"][0]["sha256"].clone(), b), (Value::Null, None));
}

#[test]
fn settings_claude_code_would_not_take_are_reported_not_failed() {
    let v = settings_of(
        Some(b"{\"$schema\": \"https://example.invalid/x.json\"}"),
        None,
    );
    assert_eq!(v["state"], "refused");
    assert!(
        v["reason"].as_str().unwrap().contains("another schema"),
        "{v}"
    );
    let v = settings_of(Some(b"{\n  // a comment\n}"), None);
    assert_eq!(v["state"], "refused");
    assert!(v["reason"].as_str().unwrap().contains("not JSON"), "{v}");
    assert_eq!(settings_of(None, None)["state"], "not-configured");
}
