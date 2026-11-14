//! Expected bytes are written out by hand, never worked out by the code under test.

use crate::files::{anthropic_name, apply, parse, read_back, Checked, File, Settings};
use pluxy_transactions::Merge;
use serde_json::{json, Value};

const ID: &str = "00000000-0000-4000-8000-000000831700";

fn input() -> Value {
    json!({
        "base_url": "http://127.0.0.1:41999",
        "models": [
            {"model": "claude-opus-5-5", "alias": "", "one_m": false},
            {"model": "gpt-6.1-sol", "alias": "claude-sonnet-5-sol", "one_m": true},
            {"model": "claude-sonnet-5-5", "alias": "CLAUDE-SONNET-5-5", "one_m": false},
            {"model": "anthropic/claude-x", "alias": "", "one_m": false},
        ],
    })
}

fn settings() -> Settings {
    Settings::from_input(&input(), "the-key").unwrap()
}

const PROFILE_BEFORE: &str = r#"{
  "coworkEgressAllowedHosts": [
    "*"
  ],
  "inferenceProvider": "bedrock",
  "selfHostedUrl": "https://session.example.invalid",
  "big": 12345678901234567890
}
"#;

const PROFILE_AFTER: &str = r#"{
  "coworkEgressAllowedHosts": [
    "*"
  ],
  "inferenceProvider": "gateway",
  "big": 12345678901234567890,
  "inferenceGatewayBaseUrl": "http://127.0.0.1:41999",
  "inferenceGatewayApiKey": "the-key",
  "inferenceGatewayAuthScheme": "bearer",
  "inferenceCredentialKind": "static",
  "modelDiscoveryEnabled": false,
  "bootstrapEnabled": false,
  "disableDeploymentModeChooser": true,
  "inferenceModels": [
    {
      "name": "claude-opus-5-5",
      "anthropicFamilyTier": "opus",
      "isFamilyDefault": true
    },
    {
      "name": "claude-sonnet-5-sol",
      "labelOverride": "gpt-6.1-sol",
      "anthropicFamilyTier": "sonnet",
      "isFamilyDefault": true,
      "supports1m": true,
      "prefer1m": true
    },
    {
      "name": "claude-sonnet-5-5",
      "anthropicFamilyTier": "sonnet",
      "isFamilyDefault": false
    },
    {
      "name": "anthropic/claude-x"
    }
  ]
}
"#;

#[test]
fn the_profile_points_claude_desktop_at_the_core_and_keeps_the_rest() {
    let out = apply(File::Profile, Some(PROFILE_BEFORE.as_bytes()), &settings()).unwrap();
    assert_eq!(String::from_utf8(out).unwrap(), PROFILE_AFTER);
}

#[test]
fn the_deployment_choice_becomes_third_party_and_a_missing_file_is_created() {
    let before = "{\n  \"deploymentMode\": \"1p\",\n  \"x\": 1\n}\n";
    let out = apply(File::Deployment, Some(before.as_bytes()), &settings()).unwrap();
    assert_eq!(
        String::from_utf8(out).unwrap(),
        "{\n  \"deploymentMode\": \"3p\",\n  \"x\": 1\n}\n"
    );
    let out = apply(File::Deployment, None, &settings()).unwrap();
    assert_eq!(
        String::from_utf8(out).unwrap(),
        "{\n  \"deploymentMode\": \"3p\"\n}\n"
    );
}

#[test]
fn the_index_applies_pluxy_and_keeps_the_persons_configurations() {
    let before = r#"{"hybridPointer": {"bootstrapUrl": "https://c.invalid"}, "appliedId": "a", "entries": [{"id": "a", "name": "Mine"}, {"id": "00000000-0000-4000-8000-000000831700", "name": "EasyCLIProxyAPI", "custom": true}]}"#;
    let out = apply(File::Meta, Some(before.as_bytes()), &settings()).unwrap();
    let want = format!(
        "{{\n  \"appliedId\": \"{ID}\",\n  \"entries\": [\n    {{\n      \"id\": \"a\",\n      \"name\": \"Mine\"\n    }},\n    {{\n      \"id\": \"{ID}\",\n      \"name\": \"pluxy\",\n      \"custom\": true\n    }}\n  ]\n}}\n"
    );
    assert_eq!(String::from_utf8(out).unwrap(), want);
}

#[test]
fn files_claude_desktop_would_not_take_are_refused() {
    for (file, bytes, says) in [
        (File::Deployment, "{\n // no\n}", "not JSON"),
        (File::Deployment, "[]", "not a JSON object"),
        (File::Profile, r#"{"inferenceModels": {}}"#, "not a list"),
        (File::Meta, r#"{"entries": {"id": "x"}}"#, "entries"),
        (File::Meta, r#"{"entries": ["x"]}"#, "entries"),
        (File::Meta, r#"{"appliedId": 3}"#, "appliedId"),
    ] {
        let e = parse(file, Some(bytes.as_bytes())).unwrap_err();
        assert!(e.message.contains(says), "{bytes}: {}", e.message);
    }
    // Missing or empty files are supported, empty ones.
    assert!(parse(File::Meta, None).unwrap().is_empty());
    assert!(parse(File::Profile, Some(b" \n")).unwrap().is_empty());
}

#[test]
fn model_names_follow_claude_desktops_own_rule() {
    for ok in [
        "claude-opus-5-5",
        "claude-sonnet-5-sol",
        "anthropic/claude-x",
        "us.anthropic.claude-haiku-5-5",
        "opus",
        "sonnet-5.5",
        "my-opus-route",
    ] {
        assert!(anthropic_name(ok), "{ok}");
    }
    for no in [
        "gpt-6.1-sol",
        "claude-gpt-route",
        "gemini-3.8-flash",
        "claude-ling",
        "claude-k2.5",
        "claude-phi4",
        "llama-opus",
        "plain-model",
    ] {
        assert!(!anthropic_name(no), "{no}");
    }
}

#[test]
fn rows_without_a_claude_name_or_twice_the_same_name_are_refused() {
    let mut i = input();
    i["models"][1]["alias"] = json!("");
    let e = Settings::from_input(&i, "k").unwrap_err();
    assert!(e.message.contains("gpt-6.1-sol"), "{}", e.message);
    let mut i = input();
    i["models"][3]["model"] = json!("claude-opus-5-5");
    let e = Settings::from_input(&i, "k").unwrap_err();
    assert!(e.message.contains("listed twice"), "{}", e.message);
    let mut i = input();
    i["models"] = json!([]);
    assert!(Settings::from_input(&i, "k").is_err());
    let mut i = input();
    i["models"][0]["model"] = json!("claude opus");
    assert!(Settings::from_input(&i, "k").is_err());
    assert!(Settings::from_input(&input(), "").is_err());
}

#[test]
fn a_preview_reads_the_rows_back_and_says_whether_the_files_are_configured() {
    let s = settings();
    let d = apply(File::Deployment, None, &s).unwrap();
    let p = apply(File::Profile, None, &s).unwrap();
    let m = apply(File::Meta, None, &s).unwrap();
    let v = read_back(Some(&d), Some(&p), Some(&m), Some("http://127.0.0.1:41999")).unwrap();
    assert_eq!(v["state"], "configured");
    assert_eq!(v["key_set"], true);
    assert!(!v.to_string().contains("the-key"));
    assert_eq!(
        v["models"],
        json!([
            {"model": "claude-opus-5-5", "alias": "", "one_m": false},
            {"model": "gpt-6.1-sol", "alias": "claude-sonnet-5-sol", "one_m": true},
            {"model": "claude-sonnet-5-5", "alias": "", "one_m": false},
            {"model": "anthropic/claude-x", "alias": "", "one_m": false},
        ])
    );
    let other = read_back(Some(&d), Some(&p), Some(&m), Some("http://other:1")).unwrap();
    assert_eq!(other["state"], "needs-update");
    // A hybrid pointer wins over appliedId: not configured as pluxy's.
    let pointed = br#"{"appliedId": "00000000-0000-4000-8000-000000831700", "hybridPointer": {}}"#;
    let v = read_back(Some(&d), Some(&p), Some(pointed), None).unwrap();
    assert_eq!(v["state"], "needs-update");
    let v = read_back(None, None, None, None).unwrap();
    assert_eq!(v["state"], "not-configured");
    assert_eq!(v["models"], json!([]));
}

#[test]
fn clear_takes_out_a_key_claude_desktop_sealed_and_pluxys_entry_but_keeps_the_persons_edits() {
    let s = settings();
    // The profile, which did not exist: sealed in place, it still goes.
    let applied = apply(File::Profile, None, &s).unwrap();
    let sealed = String::from_utf8(applied.clone())
        .unwrap()
        .replace("\"the-key\"", "\"sealed-v1:AAAA\"");
    let out = Checked
        .merge(None, Some(&applied), Some(sealed.as_bytes()))
        .unwrap();
    assert_eq!(out, None);
    // The index: appliedId and the hybrid pointer come back, pluxy's entry goes, the person's new
    // entry stays.
    let original = br#"{"appliedId": "a", "entries": [{"id": "a", "name": "Mine"}], "hybridPointer": {"u": 1}}"#;
    let applied = apply(File::Meta, Some(original), &s).unwrap();
    let mut now: Value = serde_json::from_slice(&applied).unwrap();
    now["entries"]
        .as_array_mut()
        .unwrap()
        .push(json!({"id": "b", "name": "New"}));
    let now = serde_json::to_vec(&now).unwrap();
    let out = Checked
        .merge(Some(original), Some(&applied), Some(&now))
        .unwrap()
        .unwrap();
    assert_eq!(
        String::from_utf8(out).unwrap(),
        "{\n  \"appliedId\": \"a\",\n  \"entries\": [\n    {\n      \"id\": \"a\",\n      \"name\": \"Mine\"\n    },\n    {\n      \"id\": \"b\",\n      \"name\": \"New\"\n    }\n  ],\n  \"hybridPointer\": {\n    \"u\": 1\n  }\n}\n"
    );
    // The deployment file: the choice comes back, the person's edit stays.
    let original = b"{\"deploymentMode\": \"1p\", \"x\": 1}";
    let applied = apply(File::Deployment, Some(original), &s).unwrap();
    let now = String::from_utf8(applied.clone())
        .unwrap()
        .replace("\"x\": 1", "\"x\": 2");
    let out = Checked
        .merge(Some(original), Some(&applied), Some(now.as_bytes()))
        .unwrap()
        .unwrap();
    assert_eq!(
        String::from_utf8(out).unwrap(),
        "{\n  \"deploymentMode\": \"1p\",\n  \"x\": 2\n}\n"
    );
    // A file Claude Desktop would not take is refused first.
    let applied_meta = apply(File::Meta, None, &s).unwrap();
    assert!(Checked
        .merge(None, Some(&applied_meta), Some(b"{\"entries\": {}}"))
        .is_err());
}
