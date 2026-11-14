//! Expected bytes here are written out by hand, never worked out by the package's code.

use crate::omp::{apply_config, apply_models, read_back, Settings};
use crate::yaml::{leaves, parse, Yamls};
use crate::{settings, unmigrated, Selection};
use pluxy_transactions::Merge;
use serde_json::json;

const MODELS: &str = "# The person's Oh My Pi models.
providers:
  # A local model server.
  ollama:
    baseUrl: http://127.0.0.1:11434/v1 # local
    api: openai-completions
    auth: none
    models:
      - id: llama3.3
        contextWindow: 131072
  easy-cliproxyapi:
    baseUrl: http://127.0.0.1:8317/v1
    api: openai-responses
futureSetting: [1, 2.50, 12345678901234567890]
";

const MODELS_APPLIED: &str = "# The person's Oh My Pi models.
providers:
  # A local model server.
  ollama:
    baseUrl: http://127.0.0.1:11434/v1 # local
    api: openai-completions
    auth: none
    models:
      - id: llama3.3
        contextWindow: 131072
  easy-cliproxyapi:
    baseUrl: http://127.0.0.1:8317/v1
    api: openai-responses
  pluxy:
    baseUrl: http://127.0.0.1:41999/v1
    apiKey: k-1
    api: openai-responses
    discovery:
      type: openai-models-list
futureSetting: [1, 2.50, 12345678901234567890]
";

const CONFIG: &str = "# The person's Oh My Pi settings.
theme:
  dark: tokyo-night
modelRoles:
  default: anthropic/claude-sonnet-5-5:high # their default
  smol: anthropic/claude-haiku-5-5
defaultThinkingLevel: high
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
fn apply_adds_pluxys_provider_after_the_persons_keeping_comments_order_and_digits() {
    let out = text(apply_models(Some(MODELS.as_bytes()), &s("gpt-6.1-sol")).unwrap());
    assert_eq!(out, MODELS_APPLIED);
}

#[test]
fn apply_sets_only_the_default_role_in_place() {
    let out = text(apply_config(Some(CONFIG.as_bytes()), &s("gpt-6.1-sol")).unwrap());
    assert_eq!(
        out,
        CONFIG.replace(
            "default: anthropic/claude-sonnet-5-5:high",
            "default: pluxy/gpt-6.1-sol"
        )
    );
}

#[test]
fn apply_keeps_the_persons_own_entries_under_pluxys_provider() {
    let before = "providers:\n  pluxy:\n    models:\n      - id: my-own-model\n";
    let out = text(apply_models(Some(before.as_bytes()), &s("m")).unwrap());
    assert_eq!(
        out,
        "providers:\n  pluxy:\n    models:\n      - id: my-own-model\n    baseUrl: http://127.0.0.1:41999/v1\n    apiKey: k-1\n    api: openai-responses\n    discovery:\n      type: openai-models-list\n"
    );
    let cfg = text(apply_config(Some(b"theme:\n  dark: nord\n"), &s("gpt-6.1-sol")).unwrap());
    assert_eq!(
        cfg,
        "theme:\n  dark: nord\nmodelRoles:\n  default: pluxy/gpt-6.1-sol\n"
    );
}

#[test]
fn apply_creates_absent_files() {
    let m = text(apply_models(None, &s("gpt-6.1-sol")).unwrap());
    assert_eq!(
        m,
        "providers:\n  pluxy:\n    baseUrl: http://127.0.0.1:41999/v1\n    apiKey: k-1\n    api: openai-responses\n    discovery:\n      type: openai-models-list\n"
    );
    let c = text(apply_config(Some(b""), &s("gpt-6.1-sol")).unwrap());
    assert_eq!(c, "modelRoles:\n  default: pluxy/gpt-6.1-sol\n");
}

#[test]
fn a_key_with_yaml_syntax_is_quoted_and_reads_back_as_itself() {
    let mut x = s("m");
    x.key = "k: #1".into();
    let out = apply_models(None, &x).unwrap();
    let v = parse(Some(&out)).unwrap();
    assert_eq!(v["providers"]["pluxy"]["apiKey"], "k: #1");
}

#[test]
fn clear_takes_out_pluxys_values_keeping_the_persons_edits_and_entries() {
    let applied = MODELS_APPLIED;
    // The person adds their own model entry under pluxy's provider.
    let current = MODELS_APPLIED.replace(
        "      type: openai-models-list\n",
        "      type: openai-models-list\n    models:\n      - id: their-model\n        contextWindow: 65536\n",
    );
    let out = Yamls
        .merge(
            Some(MODELS.as_bytes()),
            Some(applied.as_bytes()),
            Some(current.as_bytes()),
        )
        .unwrap()
        .unwrap();
    assert_eq!(
        text(out),
        MODELS.replace(
            "futureSetting",
            "  pluxy:\n    models:\n      - id: their-model\n        contextWindow: 65536\nfutureSetting"
        )
    );
    // Their theme and smol edits stay; their own default comes back.
    let cfg_applied = CONFIG.replace(
        "default: anthropic/claude-sonnet-5-5:high",
        "default: pluxy/deepseek-v4",
    );
    let cfg_current = cfg_applied
        .replace("dark: tokyo-night", "dark: dracula")
        .replace(
            "smol: anthropic/claude-haiku-5-5",
            "smol: anthropic/claude-opus-5-5",
        );
    let out = Yamls
        .merge(
            Some(CONFIG.as_bytes()),
            Some(cfg_applied.as_bytes()),
            Some(cfg_current.as_bytes()),
        )
        .unwrap()
        .unwrap();
    assert_eq!(
        text(out),
        CONFIG
            .replace("dark: tokyo-night", "dark: dracula")
            .replace(
                "smol: anthropic/claude-haiku-5-5",
                "smol: anthropic/claude-opus-5-5"
            )
    );
}

#[test]
fn clear_of_a_created_file_removes_it_even_after_the_writers_values_were_reordered() {
    let applied = apply_config(None, &s("a")).unwrap();
    // Untouched: the original (no file) comes back.
    assert_eq!(
        Yamls.merge(None, Some(&applied), Some(&applied)).unwrap(),
        None
    );
    // The same value written differently: nothing of the person's is left.
    let current = b"modelRoles: {default: pluxy/a}\n";
    assert_eq!(
        Yamls.merge(None, Some(&applied), Some(current)).unwrap(),
        None
    );
}

#[test]
fn files_oh_my_pi_would_not_take_are_refused() {
    let refused = |m: &str, c: &str, says: &str| {
        let e = read_back(Some(m.as_bytes()), Some(c.as_bytes()), None).unwrap_err();
        assert!(e.to_string().contains(says), "{m:?} {c:?}: {e}");
    };
    refused("providers:\n  - ollama\n", "", "providers");
    refused("providers:\n  pluxy:\n    api: a\n    api: b\n", "", "YAML");
    refused("a: 1\n---\nb: 2\n", "", "more than one");
    refused("- a\n", "", "top level");
    refused("providers:\n  pluxy: text\n", "", "pluxy provider");
    refused("providers:\n  pluxy:\n    discovery: x\n", "", "discovery");
    refused("a: .inf\n", "", "finite");
    refused("a: !custom x\n", "", "tag");
    refused("", "modelRoles: [a]\n", "modelRoles");
    refused("", "modelRoles:\n  default: 3\n", "default model role");
    refused("", "a:\n\tb: 1\n", "YAML");
    // The same writes refuse too, before anything is built.
    assert!(apply_models(Some(b"providers: [a]\n"), &s("m")).is_err());
    assert!(apply_config(Some(b"modelRoles: 1\n"), &s("m")).is_err());
}

#[test]
fn read_back_says_what_the_files_hold_never_the_key() {
    let v = read_back(Some(MODELS.as_bytes()), Some(CONFIG.as_bytes()), None).unwrap();
    assert_eq!(
        v,
        json!({"state": "not-configured", "base_url": null, "key_set": false, "model": null,
            "default_role": "anthropic/claude-sonnet-5-5:high", "legacy": true})
    );
    let cfg = apply_config(Some(CONFIG.as_bytes()), &s("gpt-6.1-sol")).unwrap();
    let v = read_back(
        Some(MODELS_APPLIED.as_bytes()),
        Some(&cfg),
        Some("http://127.0.0.1:41999"),
    )
    .unwrap();
    assert_eq!(
        v,
        json!({"state": "configured", "base_url": "http://127.0.0.1:41999/v1", "key_set": true,
            "model": "gpt-6.1-sol", "default_role": "pluxy/gpt-6.1-sol", "legacy": true})
    );
    assert!(!v.to_string().contains("k-1"));
    // Another core's address, or the default role elsewhere: needs an update.
    let other = read_back(
        Some(MODELS_APPLIED.as_bytes()),
        Some(&cfg),
        Some("http://h:1"),
    )
    .unwrap();
    assert_eq!(other["state"], "needs-update");
    let elsewhere = read_back(
        Some(MODELS_APPLIED.as_bytes()),
        Some(CONFIG.as_bytes()),
        None,
    )
    .unwrap();
    assert_eq!(elsewhere["state"], "needs-update");
}

#[test]
fn settings_refuse_keys_oh_my_pi_would_run_and_selectors_that_are_not_one_model() {
    let input = |model: &str| json!({"base_url": "http://127.0.0.1:41999/", "model": model});
    let ok = settings(&input("gpt-6.1-sol"), "k-1").unwrap();
    assert_eq!(ok.base_url, "http://127.0.0.1:41999/v1");
    assert!(settings(&input("gpt"), "!rm -rf ~").is_err());
    assert!(settings(&input("gpt"), "a b").is_err());
    assert!(settings(&input("gpt"), "").is_err());
    for bad in ["", "a,b", "@slow", "*", "a b", "x\n"] {
        assert!(settings(&input(bad), "k-1").is_err(), "{bad:?}");
    }
    assert!(settings(&json!({"base_url": "ftp://x", "model": "m"}), "k-1").is_err());
    // A thinking suffix or a provider-style ID is a model ID Oh My Pi takes.
    assert!(settings(&input("qwen3:8b"), "k-1").is_ok());
}

#[test]
fn a_legacy_file_oh_my_pi_has_not_migrated_refuses_making_its_yaml() {
    let dir = std::env::temp_dir().join(format!("omp-setup-test-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let sels = || {
        [
            Selection::File(dir.join("models.yml")),
            Selection::File(dir.join("config.yml")),
        ]
    };
    assert!(unmigrated(&sels()).is_ok());
    std::fs::write(dir.join("settings.json"), "{}").unwrap();
    assert!(unmigrated(&sels())
        .unwrap_err()
        .to_string()
        .contains("settings.json"));
    std::fs::write(dir.join("config.yaml"), "").unwrap();
    assert!(unmigrated(&sels()).is_ok());
    std::fs::write(dir.join("models.json"), "{}").unwrap();
    assert!(unmigrated(&sels())
        .unwrap_err()
        .to_string()
        .contains("models.json"));
    std::fs::remove_dir_all(&dir).unwrap();
}

#[test]
fn a_backup_diff_reads_settings_by_name() {
    let l = leaves(CONFIG.as_bytes()).unwrap();
    let paths: Vec<Vec<String>> = l.into_iter().map(|(p, _)| p).collect();
    assert_eq!(
        paths,
        [
            vec!["theme", "dark"],
            vec!["modelRoles", "default"],
            vec!["modelRoles", "smol"],
            vec!["defaultThinkingLevel"],
        ]
        .map(|p| p.into_iter().map(String::from).collect::<Vec<_>>())
    );
    assert_eq!(leaves(b"a: [1\n"), None);
}
