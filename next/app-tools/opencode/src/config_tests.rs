use crate::config::{apply, leaves, parse, read_back, Checked, Model, Settings};
use crate::config_in;
use pluxy_transactions::Merge;
use serde_json::json;

fn settings() -> Settings {
    Settings {
        base_url: "http://127.0.0.1:41999".into(),
        key: "KEY".into(),
        model: "gpt-6.1-sol".into(),
        models: vec![
            Model {
                id: "gpt-6.1-sol".into(),
                name: "GPT-6.1 Sol".into(),
                attachment: true,
            },
            Model {
                id: "deepseek-v4".into(),
                name: "deepseek-v4".into(),
                attachment: false,
            },
        ],
    }
}

const PERSON: &str = r#"{
  // The person's own config, with comments.
  "$schema": "https://opencode.ai/config.json",
  "theme": "tokyonight", /* trailing comment */
  "provider": {
    "anthropic": {
      "options": { "timeout": 600000 }
    },
  },
  "model": "anthropic/claude-sonnet-5-5",
  "futureSetting": [1, 2.50, 12345678901234567890],
}
"#;

const APPLIED: &str = r#"{
  // The person's own config, with comments.
  "$schema": "https://opencode.ai/config.json",
  "theme": "tokyonight", /* trailing comment */
  "provider": {
    "anthropic": {
      "options": { "timeout": 600000 }
    },
    "pluxy": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "pluxy",
      "options": {
        "baseURL": "http://127.0.0.1:41999/v1",
        "apiKey": "KEY",
      },
      "models": {
        "gpt-6.1-sol": {
          "name": "GPT-6.1 Sol",
          "attachment": true,
          "modalities": {
            "input": ["text", "image", "pdf"],
            "output": ["text"],
          },
        },
        "deepseek-v4": {
          "name": "deepseek-v4",
          "modalities": {
            "input": ["text"],
            "output": ["text"],
          },
        },
      },
    },
  },
  "model": "pluxy/gpt-6.1-sol",
  "futureSetting": [1, 2.50, 12345678901234567890],
}
"#;

fn s(b: &[u8]) -> String {
    String::from_utf8(b.to_vec()).unwrap()
}

#[test]
fn apply_keeps_comments_formatting_and_other_settings_in_place() {
    let out = apply(Some(PERSON.as_bytes()), &settings()).unwrap();
    assert_eq!(s(&out), APPLIED);
}

#[test]
fn apply_to_no_file_writes_a_new_config() {
    let out = apply(None, &settings()).unwrap();
    let v: serde_json::Value = serde_json::from_slice(&out).unwrap();
    assert_eq!(v["$schema"], "https://opencode.ai/config.json");
    assert_eq!(v["model"], "pluxy/gpt-6.1-sol");
}

#[test]
fn clear_takes_out_only_the_untouched_values() {
    let applied = apply(Some(PERSON.as_bytes()), &settings()).unwrap();
    // The person changes their theme and their default model after the apply.
    let edited = s(&applied)
        .replace("\"tokyonight\"", "\"nord\"")
        .replace("\"pluxy/gpt-6.1-sol\"", "\"anthropic/claude-opus-5-5\"");
    let out = Checked
        .merge(
            Some(PERSON.as_bytes()),
            Some(&applied),
            Some(edited.as_bytes()),
        )
        .unwrap()
        .unwrap();
    assert_eq!(
        s(&out),
        PERSON
            .replace("\"tokyonight\"", "\"nord\"")
            .replace("anthropic/claude-sonnet-5-5", "anthropic/claude-opus-5-5")
    );
}

#[test]
fn read_back_shows_the_state_and_models_never_the_key() {
    let r = read_back(Some(APPLIED.as_bytes()), Some("http://127.0.0.1:41999")).unwrap();
    assert_eq!(r["state"], "configured");
    assert_eq!(r["model"], "gpt-6.1-sol");
    assert_eq!(r["key_set"], true);
    assert_eq!(
        r["models"],
        json!([
            {"id": "gpt-6.1-sol", "name": "GPT-6.1 Sol", "attachment": true},
            {"id": "deepseek-v4", "name": "deepseek-v4", "attachment": false}
        ])
    );
    assert!(!r.to_string().contains("KEY"));
    let other = read_back(Some(APPLIED.as_bytes()), Some("http://10.0.0.1:1")).unwrap();
    assert_eq!(other["state"], "needs-update");
    let none = read_back(Some(PERSON.as_bytes()), None).unwrap();
    assert_eq!(none["state"], "not-configured");
    assert_eq!(none["default_model"], "anthropic/claude-sonnet-5-5");
}

#[test]
fn configs_opencode_would_not_take_are_refused() {
    for bad in [
        "{'theme': 'x'}",
        "{theme: \"x\"}",
        "{\"a\": 1 \"b\": 2}",
        "[1]",
        "{\"a\": 1, \"a\": 2}",
        "{\"$schema\": \"https://example.invalid/agent.json\"}",
        "{\"provider\": []}",
        "{\"provider\": {\"pluxy\": {\"models\": []}}}",
        "{\"model\": 1}",
    ] {
        assert!(parse(Some(bad.as_bytes())).is_err(), "{bad}");
        assert!(apply(Some(bad.as_bytes()), &settings()).is_err(), "{bad}");
    }
    assert!(parse(Some(b"  \n")).is_ok());
    assert_eq!(leaves(PERSON.as_bytes()).unwrap().len(), 5);
}

#[test]
fn the_default_file_is_the_one_opencode_writes() {
    let d = std::env::temp_dir().join(format!("opencode-default-{}", std::process::id()));
    std::fs::create_dir_all(&d).unwrap();
    assert_eq!(config_in(&d), d.join("opencode.jsonc"));
    std::fs::write(d.join("opencode.json"), "{}").unwrap();
    assert_eq!(config_in(&d), d.join("opencode.json"));
    std::fs::write(d.join("opencode.jsonc"), "{}").unwrap();
    assert_eq!(config_in(&d), d.join("opencode.jsonc"));
    std::fs::remove_dir_all(&d).unwrap();
}

#[test]
fn clear_of_a_file_apply_created_removes_it_and_keeps_the_persons_additions() {
    let applied = apply(None, &settings()).unwrap();
    assert_eq!(
        Checked.merge(None, Some(&applied), Some(&applied)).unwrap(),
        None
    );
    // The person adds a theme (and a comment) after the apply: only that stays.
    let edited = s(&applied).replacen("{\n", "{\n  // mine\n  \"theme\": \"nord\",\n", 1);
    let out = Checked
        .merge(None, Some(&applied), Some(edited.as_bytes()))
        .unwrap()
        .unwrap();
    assert_eq!(s(&out), "{\n  // mine\n  \"theme\": \"nord\"\n}\n");
}

#[test]
fn models_no_longer_listed_go_and_kept_entries_keep_their_own_settings() {
    let before = r#"{"provider": {"pluxy": {"models": {"old": {"name": "old"}, "deepseek-v4": {"limit": {"context": 128000}}}, "options": {"timeout": 5}}}}"#;
    let out = apply(Some(before.as_bytes()), &settings()).unwrap();
    let v: serde_json::Value = serde_json::from_slice(&out).unwrap();
    let models = &v["provider"]["pluxy"]["models"];
    assert!(models.get("old").is_none());
    assert_eq!(models["deepseek-v4"]["limit"]["context"], 128000);
    assert_eq!(v["provider"]["pluxy"]["options"]["timeout"], 5);
}
