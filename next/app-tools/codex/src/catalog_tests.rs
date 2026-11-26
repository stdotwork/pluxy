//! Expected values are written out by hand, never worked out by the code under test; the
//! template's values come from pluxy's Codex client templates, read here on their own.

use crate::catalog::{apply, import, parse, read_back, Model};
use serde_json::{json, Value};

/// pluxy's template for slug, read here independently of the code under test.
fn template(slug: &str) -> Value {
    let all: Value =
        serde_json::from_str(include_str!("../../../extensions/codex/client_models.json")).unwrap();
    all["models"]
        .as_array()
        .unwrap()
        .iter()
        .find(|m| m["slug"] == slug)
        .unwrap()
        .clone()
}

fn model(slug: &str) -> Model {
    Model {
        slug: slug.into(),
        upstream: None,
        non_codex: false,
        display_name: "Sol through pluxy".into(),
        description: "".into(),
        context_window: Some(400000),
        max_context_window: Some(500000),
        percent: Some(90),
        compact: None,
        reasoning: vec!["low".into(), "medium".into(), "none".into()],
        default_reasoning: "medium".into(),
        modalities: vec!["text".into()],
        visible: true,
        tool_search: false,
        system_prompt: None,
    }
}

#[test]
fn a_new_model_is_made_from_its_template_trimmed_with_the_forms_settings() {
    let mut m = model("pluxy-sol");
    m.upstream = Some("gpt-6.1-sol".into());
    let out: Value = serde_json::from_slice(&apply(None, &[m], "gpt-6-luna").unwrap()).unwrap();
    let e = &out["models"][0];
    let t = template("gpt-6.1-sol");
    let keys: Vec<&str> = e.as_object().unwrap().keys().map(String::as_str).collect();
    assert_eq!(
        keys,
        [
            "slug",
            "display_name",
            "description",
            "default_reasoning_level",
            "supported_reasoning_levels",
            "shell_type",
            "visibility",
            "supported_in_api",
            "priority",
            "support_verbosity",
            "default_verbosity",
            "apply_patch_tool_type",
            "web_search_tool_type",
            "truncation_policy",
            "supports_image_detail_original",
            "context_window",
            "max_context_window",
            "experimental_supported_tools",
            "input_modalities",
            "supports_search_tool",
            "tool_mode",
            "default_reasoning_summary",
            "model_messages",
            // The template has neither: the form's values come last.
            "effective_context_window_percent",
            "auto_review_model_override",
        ]
    );
    assert!(t.get("effective_context_window_percent").is_none());
    assert_eq!(e["slug"], "pluxy-sol");
    assert_eq!(e["display_name"], "Sol through pluxy");
    assert_eq!(e["description"], Value::Null);
    assert_eq!(e["context_window"], 400000);
    assert_eq!(e["max_context_window"], 500000);
    assert_eq!(e["effective_context_window_percent"], 90);
    assert_eq!(e["priority"], 0);
    assert_eq!(e["visibility"], "list");
    assert_eq!(e["input_modalities"], json!(["text"]));
    assert_eq!(e["auto_review_model_override"], "gpt-6-luna");
    // Template levels keep their descriptions; a level the template lacks gets the generic one.
    assert_eq!(
        e["supported_reasoning_levels"][0],
        t["supported_reasoning_levels"][0]
    );
    assert_eq!(
        e["supported_reasoning_levels"][2],
        json!({"effort": "none", "description": "No reasoning"})
    );
    assert_eq!(
        e["model_messages"],
        json!({"instructions_template": t["model_messages"]["instructions_template"]})
    );
    for k in [
        "shell_type",
        "truncation_policy",
        "tool_mode",
        "experimental_supported_tools",
        "apply_patch_tool_type",
    ] {
        assert_eq!(e[k], t[k], "{k}");
    }
    assert!(e.get("base_instructions").is_none());
}

#[test]
fn a_model_of_another_provider_gets_the_cores_non_codex_values() {
    let mut m = model("claude-via-sol");
    m.upstream = Some("gpt-6.1-sol".into());
    m.non_codex = true;
    let out: Value = serde_json::from_slice(&apply(None, &[m], "").unwrap()).unwrap();
    let e = &out["models"][0];
    assert_eq!(e["apply_patch_tool_type"], Value::Null);
    assert_eq!(e["tool_mode"], "direct");
    assert_eq!(e["supports_search_tool"], false);
    assert_eq!(e["supports_image_detail_original"], false);
    assert!(e.get("auto_review_model_override").is_none());
}

#[test]
fn an_existing_entry_keeps_its_unknown_fields_and_the_file_its_other_keys() {
    let before = r#"{
  "note": "mine",
  "models": [
    {
      "slug": "own",
      "display_name": "Own",
      "future_field": [1, 2.50],
      "base_instructions": "Be brief.",
      "supported_reasoning_levels": [{"effort": "low", "description": "my low"}],
      "auto_review_model_override": "old"
    },
    {"slug": "dropped", "display_name": "Dropped", "base_instructions": "x"}
  ]
}
"#;
    let mut m = model("own");
    m.reasoning = vec!["low".into()];
    m.default_reasoning = "".into();
    m.visible = false;
    m.description = "described".into();
    m.max_context_window = None;
    let out = String::from_utf8(apply(Some(before.as_bytes()), &[m], "").unwrap()).unwrap();
    assert_eq!(
        out,
        r#"{
  "note": "mine",
  "models": [
    {
      "slug": "own",
      "display_name": "Sol through pluxy",
      "future_field": [
        1,
        2.50
      ],
      "base_instructions": "Be brief.",
      "supported_reasoning_levels": [
        {
          "effort": "low",
          "description": "my low"
        }
      ],
      "description": "described",
      "context_window": 400000,
      "effective_context_window_percent": 90,
      "input_modalities": [
        "text"
      ],
      "visibility": "hide",
      "supports_search_tool": false,
      "priority": 0
    }
  ]
}
"#
    );
}

#[test]
fn a_new_system_prompt_replaces_the_legacy_one() {
    let before =
        r#"{"models": [{"slug": "own", "display_name": "Own", "base_instructions": "old"}]}"#;
    let mut m = model("own");
    m.system_prompt = Some("new prompt".into());
    let out: Value =
        serde_json::from_slice(&apply(Some(before.as_bytes()), &[m], "").unwrap()).unwrap();
    assert_eq!(
        out["models"][0]["model_messages"],
        json!({"instructions_template": "new prompt"})
    );
    assert!(out["models"][0].get("base_instructions").is_none());
}

#[test]
fn refusals() {
    for (bytes, says) in [
        ("[]", "not a JSON object"),
        ("{\"model\": []}", "no models list"),
        ("{\"models\": [{\"name\": \"x\"}]}", "no slug"),
        ("{models}", "not JSON"),
    ] {
        let e = parse(Some(bytes.as_bytes())).unwrap_err();
        assert!(e.message.contains(says), "{bytes}: {}", e.message);
    }
    let e = apply(None, &[model("no-such-template")], "").unwrap_err();
    assert!(e.message.contains("no Codex template"), "{}", e.message);
    let before = r#"{"models": [{"slug": "own", "display_name": "Own"}]}"#;
    let e = apply(Some(before.as_bytes()), &[model("own")], "").unwrap_err();
    assert!(e.message.contains("no system prompt"), "{}", e.message);
}

#[test]
fn a_catalog_over_the_file_limit_is_refused() {
    let many: Vec<Model> = (0..14)
        .map(|i| {
            let mut m = model(&format!("m{i}"));
            m.upstream = Some("gpt-6.1-sol".into());
            m
        })
        .collect();
    let e = apply(None, &many, "").unwrap_err();
    assert!(e.message.contains("list fewer models"), "{}", e.message);
    assert!(apply(None, &many[..8], "").is_ok());
}

#[test]
fn import_lists_template_models_and_the_rest_as_missing() {
    let v = import(&json!([
        {"slug": "gpt-6-luna", "upstream": "gpt-6-luna"},
        {"slug": "alias", "upstream": "gpt-6.1-sol", "display_name": "Alias", "non_codex": true},
        {"slug": "claude-x", "upstream": "claude-x"},
    ]))
    .unwrap();
    assert_eq!(v["missing"], json!(["claude-x"]));
    let models = v["models"].as_array().unwrap();
    assert_eq!(models.len(), 2);
    let t = template("gpt-6-luna");
    assert_eq!(models[0]["slug"], "gpt-6-luna");
    assert_eq!(models[0]["display_name"], t["display_name"]);
    assert_eq!(models[0]["context_window"], t["context_window"]);
    assert_eq!(models[0]["upstream"], "gpt-6-luna");
    assert!(models[0]["system_prompt_chars"].as_u64().unwrap() > 1000);
    assert_eq!(models[1]["display_name"], "Alias");
    assert_eq!(models[1]["non_codex"], true);
    assert_eq!(models[1]["tool_search"], false);
}

#[test]
fn read_back_shows_editable_settings_and_the_approval_model_never_a_prompt() {
    let mut m = model("pluxy-sol");
    m.upstream = Some("gpt-6.1-sol".into());
    let bytes = apply(None, &[m], "gpt-6-luna").unwrap();
    let v = read_back(Some(&bytes)).unwrap();
    assert_eq!(v["approval_model"], "gpt-6-luna");
    let r = &v["models"][0];
    assert_eq!(r["slug"], "pluxy-sol");
    assert_eq!(r["reasoning"], json!(["low", "medium", "none"]));
    assert_eq!(r["default_reasoning"], "medium");
    assert_eq!(r["visible"], true);
    assert!(!v.to_string().contains("instructions"));
    assert_eq!(
        read_back(None).unwrap(),
        json!({"models": [], "approval_model": ""})
    );
}

#[test]
fn model_input_is_checked() {
    let ok = json!({"slug": "a", "display_name": "A", "description": "", "context_window": 100,
        "max_context_window": 200, "effective_context_window_percent": 90,
        "auto_compact_token_limit": 50, "reasoning": ["low"], "default_reasoning": "low",
        "input_modalities": ["text"], "visible": true, "tool_search": true});
    assert!(Model::from_input(&ok).is_ok());
    for (k, v) in [
        ("max_context_window", json!(50)),
        ("effective_context_window_percent", json!(101)),
        ("auto_compact_token_limit", json!(500)),
        ("default_reasoning", json!("high")),
        ("input_modalities", json!([])),
        ("reasoning", json!(["low", "low"])),
        ("display_name", json!("")),
        ("context_window", json!(0)),
    ] {
        let mut bad = ok.clone();
        bad[k] = v;
        assert!(Model::from_input(&bad).is_err(), "{k}");
    }
}
