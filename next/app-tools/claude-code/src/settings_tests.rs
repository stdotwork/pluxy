//! Expected bytes are written out by hand, never worked out by the code under test.

use crate::settings::{apply, parse, read_back, split_one_m, Checked, Settings};
use pluxy_transactions::Merge;
use serde_json::{json, Value};

const ORIGINAL: &str = r#"{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "env": {
    "API_TIMEOUT_MS": "1200000",
    "ANTHROPIC_API_KEY": "own-key",
    "ANTHROPIC_MODEL": "their-model"
  },
  "model": "sonnet",
  "big": 12345678901234567890
}
"#;

fn input() -> Value {
    json!({
        "base_url": "http://127.0.0.1:41999",
        "opus": "claude-opus-5-5",
        "sonnet": "gpt-6.1-sol",
        "sonnet_1m": true,
        "haiku": "claude-haiku-5-5[1m]",
        "fable": "",
        "fable_1m": true,
        "startup_model": "opus",
        "subagent_model": "",
        "max_context_tokens": 272000,
        "auto_compact_pct": 75,
        "disable_auto_compact": true,
    })
}

fn settings(input: &Value) -> Settings {
    Settings::from_input(input, "the-key").unwrap()
}

fn applied() -> String {
    String::from_utf8(apply(Some(ORIGINAL.as_bytes()), &settings(&input())).unwrap()).unwrap()
}

#[test]
fn apply_writes_claude_codes_variables_and_keeps_everything_else() {
    // haiku's typed [1m] counts as its 1M switch; an empty fable follows sonnet (its own switch
    // ignored); an empty subagent model leaves the variable out; the person's API key and
    // ANTHROPIC_MODEL go; their timeout, their big number and the key order stay.
    let want = r#"{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "env": {
    "API_TIMEOUT_MS": "1200000",
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:41999",
    "ANTHROPIC_AUTH_TOKEN": "the-key",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-opus-5-5",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "gpt-6.1-sol[1m]",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "claude-haiku-5-5[1m]",
    "ANTHROPIC_DEFAULT_FABLE_MODEL": "gpt-6.1-sol[1m]",
    "ANTHROPIC_DEFAULT_OPUS_MODEL_NAME": "Opus · claude-opus-5-5",
    "ANTHROPIC_DEFAULT_OPUS_MODEL_DESCRIPTION": "Auto-compact window: 272K",
    "ANTHROPIC_DEFAULT_SONNET_MODEL_NAME": "Sonnet · gpt-6.1-sol[1m]",
    "ANTHROPIC_DEFAULT_SONNET_MODEL_DESCRIPTION": "1M enabled · Auto-compact window: 272K",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME": "Haiku · claude-haiku-5-5[1m]",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL_DESCRIPTION": "1M enabled · Auto-compact window: 272K",
    "ANTHROPIC_DEFAULT_FABLE_MODEL_NAME": "Fable · gpt-6.1-sol[1m]",
    "ANTHROPIC_DEFAULT_FABLE_MODEL_DESCRIPTION": "1M enabled · Auto-compact window: 272K",
    "ANTHROPIC_CUSTOM_MODEL_OPTION": "gpt-6.1-sol[1m]",
    "ANTHROPIC_CUSTOM_MODEL_OPTION_NAME": "gpt-6.1-sol[1m]",
    "ANTHROPIC_CUSTOM_MODEL_OPTION_DESCRIPTION": "1M enabled · Auto-compact window: 272K",
    "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
    "CLAUDE_CODE_AUTO_MODE_SERVER": "0",
    "CLAUDE_CODE_MAX_CONTEXT_TOKENS": "272000",
    "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "272000",
    "CLAUDE_AUTOCOMPACT_PCT_OVERRIDE": "75",
    "DISABLE_AUTO_COMPACT": "1"
  },
  "model": "opus",
  "big": 12345678901234567890,
  "autoCompactWindow": 272000,
  "autoCompactEnabled": false
}
"#;
    assert_eq!(applied(), want);
}

#[test]
fn apply_to_no_file_starts_from_an_empty_object_and_empty_choices_remove_their_keys() {
    let mut i = input();
    i["startup_model"] = json!("");
    i["subagent_model"] = json!("worker");
    let out = apply(None, &settings(&i)).unwrap();
    let v: Value = serde_json::from_slice(&out).unwrap();
    assert!(v.get("model").is_none());
    assert_eq!(v["env"]["CLAUDE_CODE_SUBAGENT_MODEL"], "worker");
    // Applying again with an empty subagent model takes it out.
    i["subagent_model"] = json!("");
    let again: Value = serde_json::from_slice(&apply(Some(&out), &settings(&i)).unwrap()).unwrap();
    assert!(again["env"].get("CLAUDE_CODE_SUBAGENT_MODEL").is_none());
    assert_eq!(
        apply(Some(b"  \n"), &settings(&i)).unwrap(),
        apply(None, &settings(&i)).unwrap()
    );
}

#[test]
fn clear_after_the_persons_edits_keeps_them_and_brings_back_what_apply_took() {
    let edited = applied()
        .replace(
            "\"API_TIMEOUT_MS\": \"1200000\"",
            "\"API_TIMEOUT_MS\": \"5\"",
        )
        .replace("\"model\": \"opus\"", "\"model\": \"fable\"");
    let cleared = Checked
        .merge(
            Some(ORIGINAL.as_bytes()),
            Some(applied().as_bytes()),
            Some(edited.as_bytes()),
        )
        .unwrap()
        .unwrap();
    let want = r#"{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "env": {
    "API_TIMEOUT_MS": "5",
    "ANTHROPIC_API_KEY": "own-key",
    "ANTHROPIC_MODEL": "their-model"
  },
  "model": "fable",
  "big": 12345678901234567890
}
"#;
    assert_eq!(String::from_utf8(cleared).unwrap(), want);
}

#[test]
fn settings_claude_code_would_not_take_are_refused() {
    for (bytes, says) in [
        (
            &b"{\"$schema\": \"https://example.invalid/x.json\"}"[..],
            "another schema",
        ),
        (b"{\"$schema\": 1}", "another schema"),
        (b"[1, 2]", "not a JSON object"),
        (b"{\"env\": {\"A\": 1}}", "env is not an object of strings"),
        (b"{\"env\": []}", "env is not an object of strings"),
        (b"{\n  // a comment\n}", "not JSON"),
    ] {
        let e = parse(Some(bytes)).unwrap_err().to_string();
        assert!(e.contains(says), "{e} should say {says}");
        assert!(apply(Some(bytes), &settings(&input())).is_err());
        assert!(read_back(Some(bytes), None).is_err());
        assert!(Checked.merge(None, None, Some(bytes)).is_err());
    }
    assert!(parse(Some(
        b"{\"$schema\": \"https://json.schemastore.org/claude-code-settings.json\"}"
    ))
    .is_ok());
    assert!(parse(None).unwrap().is_empty());
}

#[test]
fn read_back_shows_the_applied_values_and_never_the_key() {
    let v = read_back(Some(applied().as_bytes()), Some("http://127.0.0.1:41999")).unwrap();
    assert_eq!(
        v,
        json!({
            "state": "configured",
            "base_url": "http://127.0.0.1:41999",
            "key_set": true,
            "startup_model": "opus",
            "subagent_model": "",
            "max_context_tokens": 272000,
            "auto_compact_pct": 75,
            "disable_auto_compact": true,
            "opus": "claude-opus-5-5", "opus_1m": false,
            "sonnet": "gpt-6.1-sol", "sonnet_1m": true,
            "haiku": "claude-haiku-5-5", "haiku_1m": true,
            "fable": "gpt-6.1-sol", "fable_1m": true,
        })
    );
    assert!(!v.to_string().contains("the-key"));
    let other = read_back(Some(applied().as_bytes()), Some("http://127.0.0.1:1")).unwrap();
    assert_eq!(other["state"], "needs-update");
    let none = read_back(Some(ORIGINAL.as_bytes()), None).unwrap();
    assert_eq!(none["state"], "not-configured");
    assert_eq!(none["key_set"], false);
    assert_eq!(none["startup_model"], "sonnet");
}

#[test]
fn the_1m_suffix_is_split_in_any_case_and_repeated() {
    assert_eq!(split_one_m("m[1M]"), ("m".to_string(), true));
    assert_eq!(split_one_m(" m [1m][1m] "), ("m".to_string(), true));
    assert_eq!(split_one_m("m"), ("m".to_string(), false));
}

#[test]
fn inputs_out_of_range_are_refused() {
    for (field, value) in [
        ("max_context_tokens", json!(99_999)),
        ("max_context_tokens", json!(1_000_001)),
        ("auto_compact_pct", json!(0)),
        ("auto_compact_pct", json!(101)),
        ("base_url", json!("ftp://core")),
        ("base_url", json!("http://a b")),
        ("opus", json!("")),
        ("sonnet", json!("x\ny")),
        ("haiku", json!("h".repeat(241))),
    ] {
        let mut i = input();
        i[field] = value.clone();
        assert!(
            Settings::from_input(&i, "the-key").is_err(),
            "{field} = {value} was taken"
        );
    }
    assert!(Settings::from_input(&input(), "").is_err());
    assert!(Settings::from_input(&input(), "a key").is_err());
}
