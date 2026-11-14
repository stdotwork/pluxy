//! Expected bytes are written out by hand, never worked out by the code under test.

use crate::config::{apply, base_url_of, leaves, parse, read_back, Checked, Model, Settings};
use pluxy_transactions::Merge;
use serde_json::json;

const ORIGINAL: &str = r#"# Grok Build config, kept by hand
[models]
default = "grok-4.6" # my default

[model.my-local]
model = "qwen-coder"
base_url = "http://127.0.0.1:8080/v1"
api_backend = "responses"

[ui]
future_flag = true
"#;

fn model(id: &str, name: &str, window: u64) -> Model {
    Model {
        model: id.into(),
        name: name.into(),
        context_window: window,
    }
}

fn settings() -> Settings {
    Settings {
        base_url: base_url_of("http://127.0.0.1:41999/"),
        key: "the-key".into(),
        default: "gpt-6.1-sol".into(),
        models: vec![
            model("gpt-6.1-sol", "GPT-6.1 Sol", 400000),
            model("claude-opus-5-5", "claude-opus-5-5", 200000),
        ],
    }
}

/// ORIGINAL after an apply of settings(): the default model replaced (its comment kept), pluxy's
/// entries after the person's own, the unknown table left where it was.
const APPLIED: &str = r#"# Grok Build config, kept by hand
[models]
default = "pluxy/gpt-6.1-sol" # my default

[model.my-local]
model = "qwen-coder"
base_url = "http://127.0.0.1:8080/v1"
api_backend = "responses"

[model."pluxy/gpt-6.1-sol"]
model = "gpt-6.1-sol"
base_url = "http://127.0.0.1:41999/v1"
name = "GPT-6.1 Sol"
api_key = "the-key"
api_backend = "chat_completions"
context_window = 400000

[model."pluxy/claude-opus-5-5"]
model = "claude-opus-5-5"
base_url = "http://127.0.0.1:41999/v1"
name = "claude-opus-5-5"
api_key = "the-key"
api_backend = "chat_completions"
context_window = 200000

[ui]
future_flag = true
"#;

#[test]
fn apply_writes_pluxys_entries_and_keeps_comments_and_unknown_tables() {
    let out = apply(Some(ORIGINAL.as_bytes()), &settings()).unwrap();
    assert_eq!(String::from_utf8(out).unwrap(), APPLIED);
}

#[test]
fn apply_into_no_file_makes_one() {
    let mut s = settings();
    s.models.truncate(1);
    let out = apply(None, &s).unwrap();
    assert_eq!(
        String::from_utf8(out).unwrap(),
        "[models]\ndefault = \"pluxy/gpt-6.1-sol\"\n\n[model.\"pluxy/gpt-6.1-sol\"]\nmodel = \"gpt-6.1-sol\"\nbase_url = \"http://127.0.0.1:41999/v1\"\nname = \"GPT-6.1 Sol\"\napi_key = \"the-key\"\napi_backend = \"chat_completions\"\ncontext_window = 400000\n"
    );
}

#[test]
fn apply_again_is_unchanged_and_drops_entries_no_longer_listed_but_keeps_own_keys() {
    let once = apply(Some(ORIGINAL.as_bytes()), &settings()).unwrap();
    assert_eq!(apply(Some(&once), &settings()).unwrap(), once);
    // The person added a key of their own to a pluxy entry; a model leaves the list.
    let edited = String::from_utf8(once).unwrap().replace(
        "context_window = 400000\n",
        "context_window = 400000\nreasoning = \"high\"\n",
    );
    let mut s = settings();
    s.models.truncate(1);
    s.key = "new-key".into();
    let out = String::from_utf8(apply(Some(edited.as_bytes()), &s).unwrap()).unwrap();
    assert!(out.contains("reasoning = \"high\""), "{out}");
    assert!(out.contains("api_key = \"new-key\""), "{out}");
    assert!(!out.contains("claude-opus-5-5"), "{out}");
    assert!(out.contains("[model.my-local]"), "{out}");
}

#[test]
fn an_older_apps_entries_are_left_and_counted() {
    let old = "[models]\ndefault = \"cpa-gui/a\"\n\n[model.\"cpa-gui/a\"]\nmodel = \"a\"\n";
    let out = String::from_utf8(apply(Some(old.as_bytes()), &settings()).unwrap()).unwrap();
    assert!(
        out.contains("[model.\"cpa-gui/a\"]\nmodel = \"a\"\n"),
        "{out}"
    );
    let r = read_back(Some(old.as_bytes()), None).unwrap();
    assert_eq!(r["legacy"], 1);
    assert_eq!(r["state"], "not-configured");
}

#[test]
fn read_back_shows_the_state_and_never_the_key() {
    let none = read_back(Some(ORIGINAL.as_bytes()), None).unwrap();
    assert_eq!(
        none,
        json!({"state": "not-configured", "base_url": null, "key_set": false,
            "default": "grok-4.6", "model": null, "models": [], "legacy": 0})
    );
    let r = read_back(Some(APPLIED.as_bytes()), Some("http://127.0.0.1:41999")).unwrap();
    assert_eq!(
        r,
        json!({"state": "configured", "base_url": "http://127.0.0.1:41999/v1", "key_set": true,
            "default": "pluxy/gpt-6.1-sol", "model": "gpt-6.1-sol",
            "models": [
                {"model": "gpt-6.1-sol", "name": "GPT-6.1 Sol", "context_window": 400000},
                {"model": "claude-opus-5-5", "name": "claude-opus-5-5", "context_window": 200000}
            ],
            "legacy": 0})
    );
    assert!(!r.to_string().contains("the-key"));
    // Another core's address, or a default the person moved away from pluxy's: needs update.
    let other = read_back(Some(APPLIED.as_bytes()), Some("http://127.0.0.1:5000")).unwrap();
    assert_eq!(other["state"], "needs-update");
    let moved = APPLIED.replace("default = \"pluxy/gpt-6.1-sol\"", "default = \"grok-4.6\"");
    let r = read_back(Some(moved.as_bytes()), None).unwrap();
    assert_eq!(
        (r["state"].clone(), r["model"].clone()),
        (json!("needs-update"), json!(null))
    );
}

#[test]
fn files_grok_build_would_not_take_are_refused() {
    for (bytes, says) in [
        ("[models\ndefault = \"x\"\n", "not TOML"),
        ("models = \"grok-4.6\"\n", "models is not a table"),
        ("[models]\ndefault = 4\n", "models.default is not a string"),
        ("model = []\n", "model is not a table"),
        ("model = { \"pluxy/a\" = \"x\" }\n", "is not a table"),
    ] {
        let e = parse(Some(bytes.as_bytes())).unwrap_err().to_string();
        assert!(e.contains(says), "{bytes:?}: {e}");
        assert!(apply(Some(bytes.as_bytes()), &settings()).is_err());
    }
    assert!(parse(Some(&[0xff, 0xfe])).is_err());
    // Positive controls: an empty file and the person's own config.
    assert!(parse(Some(b"  \n")).unwrap().as_table().is_empty());
    assert!(parse(Some(ORIGINAL.as_bytes())).is_ok());
}

#[test]
fn clear_takes_out_only_untouched_values() {
    let applied = apply(Some(ORIGINAL.as_bytes()), &settings()).unwrap();
    // Untouched since apply: the original comes back exactly.
    assert_eq!(
        Checked
            .merge(Some(ORIGINAL.as_bytes()), Some(&applied), Some(&applied))
            .unwrap()
            .unwrap(),
        ORIGINAL.as_bytes()
    );
    // The person changed a written model's window and their own entry: both stay, the rest goes.
    let current = String::from_utf8(applied.clone())
        .unwrap()
        .replace("context_window = 400000", "context_window = 300000")
        .replace("model = \"qwen-coder\"", "model = \"qwen-coder-2\"");
    let out = Checked
        .merge(
            Some(ORIGINAL.as_bytes()),
            Some(&applied),
            Some(current.as_bytes()),
        )
        .unwrap()
        .unwrap();
    assert_eq!(
        String::from_utf8(out).unwrap(),
        r#"# Grok Build config, kept by hand
[models]
default = "grok-4.6" # my default

[model.my-local]
model = "qwen-coder-2"
base_url = "http://127.0.0.1:8080/v1"
api_backend = "responses"

[model."pluxy/gpt-6.1-sol"]
context_window = 300000

[ui]
future_flag = true
"#
    );
    // A file Apply made goes again.
    let made = apply(None, &settings()).unwrap();
    assert_eq!(Checked.merge(None, Some(&made), Some(&made)).unwrap(), None);
    // A current file Grok Build would not take is refused.
    assert!(Checked
        .merge(Some(ORIGINAL.as_bytes()), Some(&applied), Some(b"[models"))
        .is_err());
}

#[test]
fn leaves_name_settings_by_path() {
    let l = leaves(ORIGINAL.as_bytes()).unwrap();
    let paths: Vec<Vec<String>> = l.into_iter().map(|(p, _)| p).collect();
    assert_eq!(
        paths,
        vec![
            vec!["models".to_string(), "default".into()],
            vec!["model".into(), "my-local".into(), "model".into()],
            vec!["model".into(), "my-local".into(), "base_url".into()],
            vec!["model".into(), "my-local".into(), "api_backend".into()],
            vec!["ui".into(), "future_flag".into()],
        ]
    );
    assert!(leaves(b"[models").is_none());
}

#[test]
fn the_input_is_checked() {
    let ok = json!({
        "base_url": "http://127.0.0.1:41999",
        "model": "a",
        "models": [{"model": "a", "name": "", "context_window": 1000}],
    });
    let s = crate::settings(&ok, "k").unwrap();
    assert_eq!(s.models[0].name, "a");
    assert_eq!(s.base_url, "http://127.0.0.1:41999/v1");
    for (field, bad) in [
        ("model", json!("b")),
        ("model", json!("a b")),
        ("models", json!([])),
        (
            "models",
            json!([{"model": "a", "name": "", "context_window": 1000},
                {"model": "a", "name": "", "context_window": 1000}]),
        ),
        (
            "models",
            json!([{"model": "a", "name": "", "context_window": 0}]),
        ),
        ("base_url", json!("ftp://x")),
    ] {
        let mut i = ok.clone();
        i[field] = bad.clone();
        assert!(crate::settings(&i, "k").is_err(), "{field} {bad}");
    }
    assert!(crate::settings(&ok, "").is_err());
    assert!(crate::settings(&ok, "a key").is_err());
}
