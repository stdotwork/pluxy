//! Expected bytes here are written out by hand, never worked out by the package's code.

use crate::hermes::{apply, read_back, yaml11_not_text, Settings};
use crate::yaml::{leaves, Yamls};
use crate::{default_home, settings};
use pluxy_transactions::Merge;
use serde_json::json;

/// A person's config.yaml: their own endpoint selected with a key variable, the old app's legacy
/// provider, other settings and comments.
const CONFIG: &str = "# The person's Hermes Agent settings.
model:
  provider: ollama-local # their own endpoint
  default: llama3.3
  key_env: OLLAMA_KEY
providers:
  # A local model server.
  ollama-local:
    api: http://127.0.0.1:11434/v1
    key_env: OLLAMA_KEY
    models:
      llama3.3:
        context_length: 131072
custom_providers:
  - name: cpa-gui
    base_url: http://127.0.0.1:8317/v1
    api_mode: chat_completions
terminal:
  backend: local
  cwd: \".\"
toolsets: [hermes-cli]
_config_version: 53
";

const APPLIED: &str = "# The person's Hermes Agent settings.
model:
  provider: custom:pluxy # their own endpoint
  default: gpt-6.1-sol
  api_mode: chat_completions
providers:
  # A local model server.
  ollama-local:
    api: http://127.0.0.1:11434/v1
    key_env: OLLAMA_KEY
    models:
      llama3.3:
        context_length: 131072
  pluxy:
    api: http://127.0.0.1:41999/v1
    api_key: k-1
    transport: chat_completions
    default_model: gpt-6.1-sol
custom_providers:
  - name: cpa-gui
    base_url: http://127.0.0.1:8317/v1
    api_mode: chat_completions
terminal:
  backend: local
  cwd: \".\"
toolsets: [hermes-cli]
_config_version: 53
";

fn s(model: &str) -> Settings {
    Settings {
        base_url: "http://127.0.0.1:41999/v1".into(),
        key: "k-1".into(),
        model: model.into(),
    }
}

fn text(b: Vec<u8>) -> String {
    String::from_utf8(b).unwrap()
}

#[test]
fn apply_adds_pluxys_provider_and_selects_it_keeping_comments_order_and_the_rest() {
    let out = text(apply(Some(CONFIG.as_bytes()), &s("gpt-6.1-sol")).unwrap());
    assert_eq!(out, APPLIED);
}

#[test]
fn apply_takes_out_what_would_win_over_pluxys_values_and_keeps_the_persons_own_keys() {
    let before = "providers:\n  pluxy:\n    base_url: http://old/v1\n    api_mode: codex_responses\n    key_cmd: pass show x\n    enabled: false\n    models:\n      my-model:\n        context_length: 32768\nmodel:\n  provider: custom\n  base_url: http://old/v1\n  api_key: old\n  api_key_env: OLD\n";
    let out = text(apply(Some(before.as_bytes()), &s("m")).unwrap());
    assert_eq!(
        out,
        "providers:\n  pluxy:\n    base_url: http://old/v1\n    models:\n      my-model:\n        context_length: 32768\n    api: http://127.0.0.1:41999/v1\n    api_key: k-1\n    transport: chat_completions\n    default_model: m\nmodel:\n  provider: custom:pluxy\n  default: m\n  api_mode: chat_completions\n"
    );
}

#[test]
fn apply_turns_a_plain_text_model_into_the_mapping_and_creates_absent_files() {
    let out = text(apply(Some(b"model: llama3.3\n"), &s("m")).unwrap());
    assert_eq!(
        out,
        "model:\n  provider: custom:pluxy\n  default: m\n  api_mode: chat_completions\nproviders:\n  pluxy:\n    api: http://127.0.0.1:41999/v1\n    api_key: k-1\n    transport: chat_completions\n    default_model: m\n"
    );
    let created = text(apply(None, &s("m")).unwrap());
    assert_eq!(
        created,
        "providers:\n  pluxy:\n    api: http://127.0.0.1:41999/v1\n    api_key: k-1\n    transport: chat_completions\n    default_model: m\nmodel:\n  provider: custom:pluxy\n  default: m\n  api_mode: chat_completions\n"
    );
}

#[test]
fn clear_takes_out_pluxys_values_keeping_the_persons_edits() {
    // The person changes their terminal backend and adds per-model metadata under pluxy.
    let current = APPLIED
        .replace("backend: local", "backend: docker")
        .replace(
            "    default_model: gpt-6.1-sol\n",
            "    default_model: gpt-6.1-sol\n    models:\n      gpt-6.1-sol:\n        context_length: 400000\n",
        );
    let out = Yamls
        .merge(
            Some(CONFIG.as_bytes()),
            Some(APPLIED.as_bytes()),
            Some(current.as_bytes()),
        )
        .unwrap()
        .unwrap();
    assert_eq!(
        text(out),
        CONFIG.replace("backend: local", "backend: docker").replace(
            "custom_providers:",
            "  pluxy:\n    models:\n      gpt-6.1-sol:\n        context_length: 400000\ncustom_providers:"
        )
    );
    // Untouched since the apply: the original bytes come back.
    assert_eq!(
        Yamls
            .merge(
                Some(CONFIG.as_bytes()),
                Some(APPLIED.as_bytes()),
                Some(APPLIED.as_bytes())
            )
            .unwrap()
            .unwrap(),
        CONFIG.as_bytes()
    );
}

#[test]
fn clear_of_a_created_file_removes_it() {
    let applied = apply(None, &s("a")).unwrap();
    assert_eq!(
        Yamls.merge(None, Some(&applied), Some(&applied)).unwrap(),
        None
    );
}

#[test]
fn files_hermes_would_not_take_are_refused() {
    let refused = |c: &str, says: &str| {
        let e = read_back(Some(c.as_bytes()), None).unwrap_err();
        assert!(e.to_string().contains(says), "{c:?}: {e}");
        assert!(apply(Some(c.as_bytes()), &s("m")).is_err(), "{c:?}");
    };
    refused("providers:\n  - pluxy\n", "providers");
    refused("providers:\n  pluxy:\n    api: a\n    api: b\n", "YAML");
    refused("a: 1\n---\nb: 2\n", "more than one");
    refused("- a\n", "top level");
    refused("providers:\n  pluxy: text\n", "pluxy provider");
    refused("model: [a]\n", "model");
    refused("model:\n  provider: 3\n", "model provider");
    refused("model:\n  default: [x]\n", "model default");
    refused("a: !custom x\n", "tag");
    refused("a:\n\tb: 1\n", "YAML");
}

#[test]
fn read_back_says_what_the_file_holds_never_the_key() {
    let v = read_back(Some(CONFIG.as_bytes()), None).unwrap();
    assert_eq!(
        v,
        json!({"state": "not-configured", "base_url": null, "key_set": false,
            "provider": "ollama-local", "model": null, "legacy": true})
    );
    let v = read_back(Some(APPLIED.as_bytes()), Some("http://127.0.0.1:41999")).unwrap();
    assert_eq!(
        v,
        json!({"state": "configured", "base_url": "http://127.0.0.1:41999/v1", "key_set": true,
            "provider": "custom:pluxy", "model": "gpt-6.1-sol", "legacy": true})
    );
    assert!(!v.to_string().contains("k-1"));
    // Another core's address, another provider selected, or a key command: needs an update.
    let other = read_back(Some(APPLIED.as_bytes()), Some("http://h:1")).unwrap();
    assert_eq!(other["state"], "needs-update");
    let elsewhere = APPLIED.replace("provider: custom:pluxy", "provider: openrouter");
    assert_eq!(
        read_back(Some(elsewhere.as_bytes()), None).unwrap()["state"],
        "needs-update"
    );
    let cmd = APPLIED.replace("    api_key: k-1\n", "    api_key: k-1\n    key_cmd: x\n");
    assert_eq!(
        read_back(Some(cmd.as_bytes()), None).unwrap()["state"],
        "needs-update"
    );
    // Hermes's other spelling for the key is pluxy's too.
    let bare = APPLIED.replace("provider: custom:pluxy", "provider: Pluxy");
    assert_eq!(
        read_back(Some(bare.as_bytes()), None).unwrap()["state"],
        "configured"
    );
}

#[test]
fn settings_refuse_values_hermes_would_read_as_something_else_or_expand() {
    let input = |model: &str| json!({"base_url": "http://127.0.0.1:41999/", "model": model});
    let ok = settings(&input("gpt-6.1-sol"), "sk-abc").unwrap();
    assert_eq!(ok.base_url, "http://127.0.0.1:41999/v1");
    for bad_key in ["", "a b", "${OPENAI_API_KEY}", "yes", "12345"] {
        assert!(settings(&input("gpt"), bad_key).is_err(), "{bad_key:?}");
    }
    for bad in [
        "",
        "On",
        "no",
        "NULL",
        "~",
        "1.5",
        "1_000",
        "2026-11-14",
        "1:30",
        "x y",
        "a${B}",
    ] {
        assert!(settings(&input(bad), "sk-abc").is_err(), "{bad:?}");
    }
    assert!(settings(&json!({"base_url": "ftp://x", "model": "m"}), "sk-abc").is_err());
    for good in [
        "qwen3:8b",
        "o3",
        "anthropic/claude-opus-5-5",
        "gpt-6.1-sol",
        "yesterday",
    ] {
        assert!(settings(&input(good), "sk-abc").is_ok(), "{good:?}");
    }
    assert!(!yaml11_not_text("custom:pluxy"));
    assert!(!yaml11_not_text("chat_completions"));
    assert!(!yaml11_not_text("http://127.0.0.1:41999/v1"));
}

#[test]
fn a_key_with_yaml_syntax_is_quoted_and_reads_back_as_itself() {
    let mut x = s("m");
    x.key = "k:#1".into();
    let out = apply(None, &x).unwrap();
    let v = crate::yaml::parse(Some(&out)).unwrap();
    assert_eq!(v["providers"]["pluxy"]["api_key"], "k:#1");
}

#[test]
fn the_default_file_follows_hermess_active_profile() {
    let home = std::env::temp_dir().join(format!("hermes-setup-test-{}", std::process::id()));
    let root = home.join(".hermes");
    std::fs::create_dir_all(root.join("profiles").join("work")).unwrap();
    // The test's own HOME: no other test reads it.
    std::env::set_var("HOME", &home);
    assert_eq!(default_home().unwrap(), root);
    std::fs::write(root.join("active_profile"), "default\n").unwrap();
    assert_eq!(default_home().unwrap(), root);
    std::fs::write(root.join("active_profile"), "\u{feff}Work\n").unwrap();
    assert_eq!(default_home().unwrap(), root.join("profiles").join("work"));
    std::fs::write(root.join("active_profile"), "gone\n").unwrap();
    assert!(default_home()
        .unwrap_err()
        .to_string()
        .contains("active profile"));
    std::fs::write(root.join("active_profile"), "../x\n").unwrap();
    assert!(default_home().is_err());
    std::fs::remove_dir_all(&home).unwrap();
}

#[test]
fn a_backup_diff_reads_settings_by_name() {
    let l = leaves(b"model:\n  default: a\nproviders:\n  pluxy:\n    api_key: k\n").unwrap();
    let paths: Vec<Vec<String>> = l.into_iter().map(|(p, _)| p).collect();
    assert_eq!(
        paths,
        [
            vec!["model", "default"],
            vec!["providers", "pluxy", "api_key"],
        ]
        .map(|p| p.into_iter().map(String::from).collect::<Vec<_>>())
    );
    assert_eq!(leaves(b"a: [1\n"), None);
}

#[test]
fn apply_keeps_the_persons_model_metadata_under_pluxys_provider() {
    // The native check's stow file holds the same bytes (next/app/native/hermes.ts).
    let before =
        "providers:\n  pluxy:\n    models:\n      my-model:\n        context_length: 32768\n";
    let out = text(apply(Some(before.as_bytes()), &s("gpt-6.1-sol")).unwrap());
    assert_eq!(
        out,
        "providers:\n  pluxy:\n    models:\n      my-model:\n        context_length: 32768\n    api: http://127.0.0.1:41999/v1\n    api_key: k-1\n    transport: chat_completions\n    default_model: gpt-6.1-sol\nmodel:\n  provider: custom:pluxy\n  default: gpt-6.1-sol\n  api_mode: chat_completions\n"
    );
}

#[test]
fn apply_after_clear_keeps_the_persons_edits() {
    // The native check's clear step applies again over the cleared file (next/app/native/hermes.ts).
    let edited = |s: &str| {
        s.replace("backend: local", "backend: docker")
            .replace("custom_providers:", "  pluxy:\n    models:\n      gpt-6.1-sol:\n        context_length: 400000\ncustom_providers:")
    };
    let cleared = edited(CONFIG);
    let out = text(apply(Some(cleared.as_bytes()), &s("gpt-6.1-sol")).unwrap());
    assert_eq!(
        out,
        APPLIED
            .replace("backend: local", "backend: docker")
            .replace(
            "  pluxy:\n    api:",
            "  pluxy:\n    models:\n      gpt-6.1-sol:\n        context_length: 400000\n    api:"
        )
    );
}
