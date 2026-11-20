//! Expected bytes here are written out by hand, never worked out by the package's code.

use crate::hermes::{apply, read_back, yaml11_not_text, Settings};
use crate::yaml::{leaves, Yamls};
use crate::{default_home, recipe, settings};
use pluxy_transactions::Merge;
use serde_json::json;
use std::ffi::OsString;
use std::path::{Path, PathBuf};

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
    let before = "providers:\n  pluxy:\n    base_url: http://old/v1\n    url: http://older/v1\n    model: old-model\n    baseUrl: http://camel/v1\n    apiKey: camel\n    apiMode: codex_responses\n    keyEnv: CAMEL\n    apiKeyEnv: CAMEL\n    defaultModel: camel-model\n    api_mode: codex_responses\n    key_cmd: pass show x\n    enabled: false\n    models:\n      my-model:\n        context_length: 32768\nmodel:\n  provider: custom\n  base_url: http://old/v1\n  api_key: old\n  api_key_env: OLD\n";
    let out = text(apply(Some(before.as_bytes()), &s("m")).unwrap());
    assert_eq!(
        out,
        "providers:\n  pluxy:\n    models:\n      my-model:\n        context_length: 32768\n    api: http://127.0.0.1:41999/v1\n    api_key: k-1\n    transport: chat_completions\n    default_model: m\nmodel:\n  provider: custom:pluxy\n  default: m\n  api_mode: chat_completions\n"
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
    // An address, model or camelCase key that Hermes reads before (or for) pluxy's: an update.
    for extra in [
        "base_url: http://x/v1",
        "url: http://x/v1",
        "model: other",
        "baseUrl: http://x/v1",
        "apiKey: other",
        "apiMode: codex_responses",
        "keyEnv: K",
        "apiKeyEnv: K",
        "defaultModel: other",
    ] {
        let over = APPLIED.replace(
            "    api_key: k-1\n",
            &format!("    api_key: k-1\n    {extra}\n"),
        );
        assert_eq!(
            read_back(Some(over.as_bytes()), None).unwrap()["state"],
            "needs-update",
            "{extra}"
        );
    }
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

/// An environment holding only vars.
fn env(vars: &[(&str, &Path)]) -> impl Fn(&str) -> Option<OsString> {
    let vars: Vec<(String, OsString)> = vars
        .iter()
        .map(|(k, v)| (k.to_string(), v.as_os_str().to_owned()))
        .collect();
    move |k| vars.iter().find(|(n, _)| n == k).map(|(_, v)| v.clone())
}

fn temp(name: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!("hermes-setup-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    d
}

#[test]
fn the_default_file_follows_hermess_active_profile_on_each_system() {
    let home = temp("profile-home");
    let local = temp("profile-local");
    let vars = env(&[("HOME", &home), ("LOCALAPPDATA", &local)]);
    // hermes_constants.py: ~/.hermes, and %LOCALAPPDATA%\hermes on Windows.
    for (os, root) in [
        ("linux", home.join(".hermes")),
        ("macos", home.join(".hermes")),
        ("windows", local.join("hermes")),
    ] {
        std::fs::create_dir_all(root.join("profiles").join("work")).unwrap();
        // Linux and macOS share ~/.hermes: each starts with no active profile.
        let _ = std::fs::remove_file(root.join("active_profile"));
        assert_eq!(default_home(os, &vars).unwrap(), root, "{os}");
        std::fs::write(root.join("active_profile"), "default\n").unwrap();
        assert_eq!(default_home(os, &vars).unwrap(), root, "{os}");
        std::fs::write(root.join("active_profile"), "\u{feff}Work\n").unwrap();
        assert_eq!(
            default_home(os, &vars).unwrap(),
            root.join("profiles").join("work"),
            "{os}"
        );
        std::fs::write(root.join("active_profile"), "gone\n").unwrap();
        assert!(default_home(os, &vars)
            .unwrap_err()
            .to_string()
            .contains("active profile"));
        std::fs::write(root.join("active_profile"), "../x\n").unwrap();
        assert!(default_home(os, &vars).is_err());
    }
    // Windows with no LOCALAPPDATA, a blank one (Hermes strips it) or one that is not a full path:
    // AppData\Local in the home folder, as Hermes falls back.
    let fallback = home.join("AppData").join("Local").join("hermes");
    assert_eq!(
        default_home("windows", &env(&[("HOME", &home)])).unwrap(),
        fallback
    );
    for bad in ["  ", "AppData\\Local", "relative"] {
        let vars = env(&[("HOME", &home), ("LOCALAPPDATA", Path::new(bad))]);
        assert_eq!(default_home("windows", &vars).unwrap(), fallback, "{bad:?}");
    }
    assert!(default_home("linux", &env(&[("LOCALAPPDATA", &local)])).is_err());
    std::fs::remove_dir_all(&home).unwrap();
    std::fs::remove_dir_all(&local).unwrap();
}

#[test]
fn the_recipe_starts_the_installers_hermes_or_the_one_on_path() {
    let home = temp("recipe-home");
    let local = temp("recipe-local");
    let vars = env(&[("HOME", &home), ("LOCALAPPDATA", &local)]);
    let program = |os: &str| recipe(os, &vars).unwrap()["program"].clone();
    for os in ["linux", "macos", "windows"] {
        assert_eq!(program(os), "hermes", "{os}");
    }
    // The installers' copies: install.sh's ~/.local/bin/hermes, install.ps1's
    // %LOCALAPPDATA%\hermes\bin\hermes.exe. Each is taken where this system would run it.
    let unix = home.join(".local").join("bin").join("hermes");
    let windows = local.join("hermes").join("bin").join("hermes.exe");
    for p in [&unix, &windows] {
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(p, "MZ").unwrap();
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(program("linux"), "hermes", "not executable");
        std::fs::set_permissions(&unix, std::fs::Permissions::from_mode(0o755)).unwrap();
        std::fs::set_permissions(&windows, std::fs::Permissions::from_mode(0o755)).unwrap();
    }
    // A file without an extension is no program on Windows.
    let unix_program = if cfg!(unix) {
        json!(unix)
    } else {
        json!("hermes")
    };
    assert_eq!(program("linux"), unix_program);
    assert_eq!(program("macos"), unix_program);
    assert_eq!(program("windows"), json!(windows));
    let r = recipe("windows", &vars).unwrap();
    assert_eq!(
        (r["client"].clone(), r["kind"].clone(), r["args"].clone()),
        (json!("Hermes Agent"), json!("terminal"), json!([]))
    );
    std::fs::remove_dir_all(&home).unwrap();
    std::fs::remove_dir_all(&local).unwrap();
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

#[test]
fn the_yaml_1_1_check_refuses_only_what_yaml_1_1_reads_as_something_else() {
    // Text in YAML 1.1: hex keys, UUIDs, model IDs with digits.
    for text in [
        "0a1b2c3d4e5f60718293a4b5c6d7e8f9",
        "deadbeef0123",
        "123e4567-e89b-12d3-a456-426614174000",
        "4o",
        "0o",
        "1.5-pro",
        "2026-11-14-preview",
        "yEs",
        "12e",
        "0x",
        "-",
        ".5x",
    ] {
        assert!(!yaml11_not_text(text), "{text:?} is text");
    }
    // Not text in YAML 1.1.
    for other in [
        "",
        "y",
        "N",
        "Yes",
        "OFF",
        "~",
        "Null",
        "=",
        "0",
        "-12",
        "+7",
        "1_000",
        "0b101",
        "0x1F",
        "0o17",
        "017",
        "1:30",
        "190:20:30",
        "1.5",
        "-1.",
        "1e5",
        "6.8523015e+5",
        ".5",
        ".5e+3",
        "1:30.5",
        ".inf",
        "-.Inf",
        ".NaN",
        "2026-11-14",
        "2001-12-14t21:59:43.10-05:00",
        "2001-12-14T21:59:43Z",
        "2001-1-4T1:59:43",
    ] {
        assert!(yaml11_not_text(other), "{other:?} is not text");
    }
}

#[test]
fn apply_replaces_a_document_that_is_only_null_with_a_mapping() {
    for doc in ["~\n", "null\n", "# my settings\n---\n~\n"] {
        let out = text(apply(Some(doc.as_bytes()), &s("m")).unwrap());
        let v = crate::yaml::parse(Some(out.as_bytes())).unwrap();
        assert_eq!(v["model"]["provider"], "custom:pluxy", "{doc:?}: {out}");
        assert_eq!(v["providers"]["pluxy"]["api_key"], "k-1", "{doc:?}: {out}");
    }
    // A null pluxy entry is an absent one.
    let out = text(apply(Some(b"providers:\n  pluxy: ~\n"), &s("m")).unwrap());
    assert!(out.contains("    api_key: k-1\n"), "{out}");
}
