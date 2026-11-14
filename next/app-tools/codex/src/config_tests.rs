//! Expected bytes are written out by hand, never worked out by the code under test.

use crate::config::{apply, base_url_of, leaves, merge, parse, read_back, Agents, Settings};
use crate::Checked;
use pluxy_transactions::Merge;
use serde_json::json;

const ORIGINAL: &str = r#"# Codex config, kept by hand
model = "gpt-6.1-sol" # my default
approval_policy = "on-request"

[model_providers.openai-proxy]
name = "My proxy"
base_url = "https://example.invalid/v1"

[features]
future_flag = true
"#;

fn settings() -> Settings {
    Settings {
        base_url: base_url_of("http://127.0.0.1:41999/"),
        key: "the-key".into(),
        model: "gpt-6.1-terra".into(),
        catalog: "pluxy-model-catalog.json".into(),
        remote_compaction: true,
        agents: Agents::codex_default(),
    }
}

/// ORIGINAL after an apply of settings(): the new root keys after the person's, the provider
/// table and its capabilities at the end; the comment on the model stays.
const APPLIED: &str = r#"# Codex config, kept by hand
model = "gpt-6.1-terra" # my default
approval_policy = "on-request"
model_provider = "pluxy"
model_catalog_json = "pluxy-model-catalog.json"

[model_providers.openai-proxy]
name = "My proxy"
base_url = "https://example.invalid/v1"

[model_providers.pluxy]
name = "pluxy"
base_url = "http://127.0.0.1:41999/v1"
wire_api = "responses"
experimental_bearer_token = "the-key"

[model_providers.pluxy.capabilities]
remote_compaction = "v2"

[features]
future_flag = true
"#;

#[test]
fn apply_writes_the_provider_and_keeps_comments_and_unknown_tables() {
    let out = apply(Some(ORIGINAL.as_bytes()), &settings()).unwrap();
    assert_eq!(String::from_utf8(out).unwrap(), APPLIED);
}

#[test]
fn apply_into_no_file_makes_one() {
    let mut s = settings();
    s.remote_compaction = false;
    let out = apply(None, &s).unwrap();
    assert_eq!(
        String::from_utf8(out).unwrap(),
        "model_provider = \"pluxy\"\nmodel = \"gpt-6.1-terra\"\nmodel_catalog_json = \"pluxy-model-catalog.json\"\n\n[model_providers.pluxy]\nname = \"pluxy\"\nbase_url = \"http://127.0.0.1:41999/v1\"\nwire_api = \"responses\"\nexperimental_bearer_token = \"the-key\"\n"
    );
}

#[test]
fn apply_again_is_unchanged_and_turning_remote_compaction_off_removes_capabilities() {
    let once = apply(Some(ORIGINAL.as_bytes()), &settings()).unwrap();
    assert_eq!(apply(Some(&once), &settings()).unwrap(), once);
    let mut s = settings();
    s.remote_compaction = false;
    let off = String::from_utf8(apply(Some(&once), &s).unwrap()).unwrap();
    assert!(
        !off.contains("capabilities") && !off.contains("remote_compaction"),
        "{off}"
    );
}

#[test]
fn other_auth_of_the_pluxy_provider_goes() {
    let before = "[model_providers.pluxy]\nenv_key = \"X\"\nrequires_openai_auth = true\nstream_max_retries = 9\n";
    let out = String::from_utf8(apply(Some(before.as_bytes()), &settings()).unwrap()).unwrap();
    assert!(!out.contains("env_key") && !out.contains("requires_openai_auth"));
    assert!(out.contains("stream_max_retries = 9"));
}

#[test]
fn agents_are_written_only_when_they_differ_from_codex_defaults_or_the_file_has_them() {
    let out = String::from_utf8(apply(None, &settings()).unwrap()).unwrap();
    assert!(!out.contains("[agents]"));
    let mut s = settings();
    s.agents = Agents {
        enabled: true,
        max_threads: Some(4),
        model: "gpt-6-luna".into(),
        effort: "low".into(),
        interrupt: false,
    };
    let out = String::from_utf8(apply(None, &s).unwrap()).unwrap();
    assert!(out.ends_with(
        "\n[agents]\nenabled = true\ninterrupt_message = false\nmax_concurrent_threads_per_session = 4\ndefault_subagent_model = \"gpt-6-luna\"\ndefault_subagent_reasoning_effort = \"low\"\n"
    ), "{out}");
    // Back to the defaults: the table stays, the emptied settings go.
    let back = String::from_utf8(apply(Some(out.as_bytes()), &settings()).unwrap()).unwrap();
    assert!(
        back.ends_with("\n[agents]\nenabled = true\ninterrupt_message = true\n"),
        "{back}"
    );
}

#[test]
fn clear_keeps_the_persons_edits_and_comments() {
    let applied = apply(Some(ORIGINAL.as_bytes()), &settings()).unwrap();
    // The person changes the model and the retries of the provider, and adds a comment.
    let edited = String::from_utf8(applied.clone())
        .unwrap()
        .replace("model = \"gpt-6.1-terra\"", "model = \"their-model\"")
        .replace(
            "wire_api = \"responses\"\n",
            "wire_api = \"responses\"\n# mine\nstream_max_retries = 2\n",
        );
    let cleared = Checked
        .merge(
            Some(ORIGINAL.as_bytes()),
            Some(&applied),
            Some(edited.as_bytes()),
        )
        .unwrap()
        .unwrap();
    assert_eq!(
        String::from_utf8(cleared).unwrap(),
        r#"# Codex config, kept by hand
model = "their-model" # my default
approval_policy = "on-request"

[model_providers.openai-proxy]
name = "My proxy"
base_url = "https://example.invalid/v1"

[model_providers.pluxy]
# mine
stream_max_retries = 2

[features]
future_flag = true
"#
    );
}

#[test]
fn clear_of_an_untouched_apply_gives_the_original_bytes_and_of_a_made_file_removes_it() {
    let applied = apply(Some(ORIGINAL.as_bytes()), &settings()).unwrap();
    let back = merge(Some(ORIGINAL.as_bytes()), Some(&applied), &applied).unwrap();
    assert_eq!(String::from_utf8(back.unwrap()).unwrap(), ORIGINAL);
    let made = apply(None, &settings()).unwrap();
    assert_eq!(merge(None, Some(&made), &made).unwrap(), None);
}

#[test]
fn files_codex_would_not_take_are_refused() {
    for (bytes, says) in [
        ("model = \n", "not TOML"),
        (
            "[model_providers]\npluxy = 3\n",
            "model_providers.pluxy is not a table",
        ),
        ("[model_providers.pluxy]\nwire_api = \"chat\"\n", "wire_api"),
        ("agents = true\n", "agents is not a table"),
        ("model_providers = 1\n", "model_providers is not a table"),
    ] {
        let e = parse(Some(bytes.as_bytes())).unwrap_err();
        assert!(e.message.contains(says), "{bytes}: {}", e.message);
        assert!(apply(Some(bytes.as_bytes()), &settings()).is_err());
    }
    assert!(parse(Some(b"   \n")).unwrap().is_empty());
}

#[test]
fn read_back_shows_the_state_never_the_key() {
    let none = read_back(Some(ORIGINAL.as_bytes()), None).unwrap();
    assert_eq!(none["state"], "not-configured");
    assert_eq!(none["model"], "gpt-6.1-sol");
    assert_eq!(
        none["agents"],
        json!({"enabled": true, "max_threads": null, "model": "", "effort": "", "interrupt": true})
    );
    let applied = apply(Some(ORIGINAL.as_bytes()), &settings()).unwrap();
    let v = read_back(Some(&applied), Some("http://127.0.0.1:41999")).unwrap();
    assert_eq!(v["state"], "configured");
    assert_eq!(v["key_set"], true);
    assert_eq!(v["provider"], "pluxy");
    assert_eq!(v["remote_compaction"], true);
    assert!(!v.to_string().contains("the-key"));
    let other = read_back(Some(&applied), Some("http://127.0.0.1:1")).unwrap();
    assert_eq!(other["state"], "needs-update");
}

#[test]
fn leaves_name_settings_by_path() {
    let l = leaves(APPLIED.as_bytes()).unwrap();
    let paths: Vec<String> = l.iter().map(|(p, _)| p.join(".")).collect();
    assert_eq!(
        paths,
        [
            "model",
            "approval_policy",
            "model_provider",
            "model_catalog_json",
            "model_providers.openai-proxy.name",
            "model_providers.openai-proxy.base_url",
            "model_providers.pluxy.name",
            "model_providers.pluxy.base_url",
            "model_providers.pluxy.wire_api",
            "model_providers.pluxy.experimental_bearer_token",
            "model_providers.pluxy.capabilities.remote_compaction",
            "features.future_flag",
        ]
    );
    assert!(leaves(b"= nope").is_none());
}
