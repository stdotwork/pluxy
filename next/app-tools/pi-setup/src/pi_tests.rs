//! The bytes here are written by hand (the same as the native check's fixtures, native/pi.ts),
//! never worked out by the code under test.

use crate::adapter::{self, State};
use crate::pi::{self, PiMerge, Settings};
use pluxy_transactions::{Merge, Selection};
use std::path::{Path, PathBuf};

const ORIGINAL: &str = r#"{
  "theme": "dark",
  "defaultProvider": "anthropic",
  "defaultModel": "claude-sonnet-5-5",
  "packages": [
    "npm:pi-subagents",
    "npm:@router-for-me/pi-cliproxyapi-provider@1.4.20",
    {
      "source": "git:github.com/example/pi-tools@v1",
      "skills": []
    }
  ],
  "futureSetting": [
    1,
    2.50,
    12345678901234567890
  ]
}
"#;

const APPLIED: &str = r#"{
  "theme": "dark",
  "defaultProvider": "cliproxyapi",
  "defaultModel": "gpt-6.1-sol",
  "packages": [
    "npm:pi-subagents",
    {
      "source": "git:github.com/example/pi-tools@v1",
      "skills": []
    },
    "./pluxy-cliproxyapi-provider"
  ],
  "futureSetting": [
    1,
    2.50,
    12345678901234567890
  ]
}
"#;

const UNINSTALLED: &str = r#"{
  "theme": "dark",
  "defaultProvider": "cliproxyapi",
  "defaultModel": "gpt-6.1-sol",
  "packages": [
    "npm:pi-subagents",
    {
      "source": "git:github.com/example/pi-tools@v1",
      "skills": []
    }
  ],
  "futureSetting": [
    1,
    2.50,
    12345678901234567890
  ]
}
"#;

const EDITED: &str = r#"{
  "theme": "dark",
  "quietStartup": true,
  "defaultProvider": "cliproxyapi",
  "defaultModel": "gpt-6.1-sol",
  "packages": [
    "npm:pi-subagents",
    {
      "source": "git:github.com/example/pi-tools@v1",
      "skills": []
    },
    "./pluxy-cliproxyapi-provider",
    "npm:pi-mcp"
  ],
  "futureSetting": [
    1,
    2.50,
    12345678901234567890
  ]
}
"#;

const CLEARED: &str = r#"{
  "theme": "dark",
  "quietStartup": true,
  "defaultProvider": "anthropic",
  "defaultModel": "claude-sonnet-5-5",
  "packages": [
    "npm:pi-subagents",
    {
      "source": "git:github.com/example/pi-tools@v1",
      "skills": []
    },
    "npm:pi-mcp",
    "npm:@router-for-me/pi-cliproxyapi-provider@1.4.20"
  ],
  "futureSetting": [
    1,
    2.50,
    12345678901234567890
  ]
}
"#;

const CONFIG: &str =
    "{\n  \"baseUrl\": \"http://127.0.0.1:8317\",\n  \"fast\": true,\n  \"pause\": false\n}\n";
const CONFIG_APPLIED: &str = "{\n  \"baseUrl\": \"http://127.0.0.1:41999\",\n  \"fast\": true,\n  \"pause\": false,\n  \"apiKey\": \"k-test\"\n}\n";
const CACHE: &str = "{\"baseUrl\":\"http://127.0.0.1:8317\",\"models\":[],\"fetchedAt\":1}\n";

fn s() -> Settings {
    Settings {
        base_url: pi::base_url_of("http://127.0.0.1:41999/"),
        key: "k-test".into(),
        model: "gpt-6.1-sol".into(),
    }
}

fn text(b: Vec<u8>) -> String {
    String::from_utf8(b).unwrap()
}

#[test]
fn apply_writes_pluxys_values_and_keeps_everything_else() {
    assert_eq!(
        text(pi::apply_settings(Some(ORIGINAL.as_bytes()), &s(), "cliproxyapi").unwrap()),
        APPLIED
    );
    assert_eq!(
        text(pi::apply_config(Some(CONFIG.as_bytes()), &s()).unwrap()),
        CONFIG_APPLIED
    );
    // From nothing (a chezmoi-style root): the registration, then the defaults.
    assert_eq!(
        text(pi::apply_settings(None, &s(), "cliproxyapi").unwrap()),
        "{\n  \"packages\": [\n    \"./pluxy-cliproxyapi-provider\"\n  ],\n  \"defaultProvider\": \"cliproxyapi\",\n  \"defaultModel\": \"gpt-6.1-sol\"\n}\n"
    );
    // Applying again changes nothing, and an entry the person narrowed stays as it is.
    assert_eq!(
        text(pi::apply_settings(Some(APPLIED.as_bytes()), &s(), "cliproxyapi").unwrap()),
        APPLIED
    );
    let narrowed = "{\n  \"packages\": [\n    {\n      \"source\": \"pluxy-cliproxyapi-provider/\",\n      \"extensions\": [\n        \"extensions/index.ts\"\n      ]\n    }\n  ]\n}\n";
    let out = text(pi::apply_settings(Some(narrowed.as_bytes()), &s(), "mine").unwrap());
    assert!(out.starts_with(&narrowed[..narrowed.len() - 3]), "{out}");
    assert!(out.contains("\"defaultProvider\": \"mine\""));
}

#[test]
fn the_provider_follows_cliproxyapi_json() {
    assert_eq!(pi::provider_of(None).unwrap(), "cliproxyapi");
    assert_eq!(
        pi::provider_of(Some(b"{\"providerId\": \" cpa \"}")).unwrap(),
        "cpa"
    );
    assert_eq!(
        pi::provider_of(Some(b"{\"providerId\": \"\"}")).unwrap(),
        "cliproxyapi"
    );
}

#[test]
fn the_address_is_the_core_root() {
    for (given, want) in [
        ("http://127.0.0.1:41999", "http://127.0.0.1:41999"),
        ("http://127.0.0.1:41999/", "http://127.0.0.1:41999"),
        ("http://127.0.0.1:41999/v1", "http://127.0.0.1:41999"),
        (
            "https://core.example/pluxy/v1/",
            "https://core.example/pluxy",
        ),
    ] {
        assert_eq!(pi::base_url_of(given), want);
    }
}

#[test]
fn uninstall_takes_out_only_the_registration() {
    assert_eq!(
        text(
            pi::uninstall_settings(Some(APPLIED.as_bytes()))
                .unwrap()
                .unwrap()
        ),
        UNINSTALLED
    );
    // Not listed: the bytes as they are, even in another layout.
    let other = b"{\"packages\":[\"npm:x\"]}";
    assert_eq!(
        pi::uninstall_settings(Some(other)).unwrap().unwrap(),
        other.to_vec()
    );
    assert_eq!(pi::uninstall_settings(None).unwrap(), None);
}

#[test]
fn clear_keeps_the_persons_edits_and_brings_back_what_apply_replaced() {
    let m = PiMerge;
    let merged = m
        .merge(
            Some(ORIGINAL.as_bytes()),
            Some(APPLIED.as_bytes()),
            Some(EDITED.as_bytes()),
        )
        .unwrap()
        .unwrap();
    assert_eq!(text(merged), CLEARED);
    // Untouched: the original's bytes exactly.
    assert_eq!(
        m.merge(
            Some(CONFIG.as_bytes()),
            Some(CONFIG_APPLIED.as_bytes()),
            Some(CONFIG_APPLIED.as_bytes())
        )
        .unwrap()
        .unwrap(),
        CONFIG.as_bytes()
    );
    // After an uninstall (its image the applied one): the original's bytes.
    assert_eq!(
        m.merge(
            Some(ORIGINAL.as_bytes()),
            Some(UNINSTALLED.as_bytes()),
            Some(UNINSTALLED.as_bytes())
        )
        .unwrap()
        .unwrap(),
        ORIGINAL.as_bytes()
    );
}

#[test]
fn clear_of_the_cache_apply_removed() {
    let m = PiMerge;
    // Still absent: the original comes back.
    assert_eq!(
        m.merge(Some(CACHE.as_bytes()), None, None)
            .unwrap()
            .unwrap(),
        CACHE.as_bytes()
    );
    // Rewritten by the plugin since: it stays, unparsed.
    assert_eq!(
        m.merge(Some(CACHE.as_bytes()), None, Some(b"not json"))
            .unwrap()
            .unwrap(),
        b"not json".to_vec()
    );
    // Made by the plugin where there was none: removed.
    assert_eq!(m.merge(None, None, Some(CACHE.as_bytes())).unwrap(), None);
}

#[test]
fn files_pi_would_not_take_are_refused() {
    for (bytes, says) in [
        (&b"{\n  \"theme\": \"dark\",\n}\n"[..], "JSON"),
        (b"[]", "object"),
        (b"{\"packages\": {\"npm:x\": true}}", "packages"),
        (b"{\"packages\": [1]}", "packages"),
        (b"{\"defaultModel\": 5}", "defaultModel"),
        (b"\xef\xbb\xbf{}", "byte order mark"),
    ] {
        let e = pi::read_back(None, Some(bytes), None, false).unwrap_err();
        assert!(e.message.contains(says), "{says}: {}", e.message);
        assert!(pi::apply_settings(Some(bytes), &s(), "cliproxyapi").is_err());
    }
    let e = pi::apply_config(Some(b"{\"apiKey\": 1}"), &s()).unwrap_err();
    assert!(e.message.contains("apiKey"));
}

#[test]
fn read_back_never_holds_the_key() {
    let v = pi::read_back(
        Some(CONFIG_APPLIED.as_bytes()),
        Some(APPLIED.as_bytes()),
        Some("http://127.0.0.1:41999/v1"),
        true,
    )
    .unwrap();
    assert!(!v.to_string().contains("k-test"));
    assert_eq!(v["state"], "configured");
    assert_eq!(v["key_set"], true);
    assert_eq!(v["registered"], true);
    assert_eq!(v["legacy"], false);
    let v = pi::read_back(
        Some(CONFIG.as_bytes()),
        Some(ORIGINAL.as_bytes()),
        None,
        false,
    )
    .unwrap();
    assert_eq!(v["state"], "not-configured");
    assert_eq!(v["legacy"], true);
    assert_eq!(v["model"], "claude-sonnet-5-5");
    // Listed, but the plugin is missing: needs an update.
    let v = pi::read_back(
        Some(CONFIG_APPLIED.as_bytes()),
        Some(APPLIED.as_bytes()),
        None,
        false,
    )
    .unwrap();
    assert_eq!(v["state"], "needs-update");
}

#[test]
fn entries_are_recognised_in_every_form() {
    use serde_json::json;
    for e in [
        json!("./pluxy-cliproxyapi-provider"),
        json!("pluxy-cliproxyapi-provider"),
        json!("pluxy-cliproxyapi-provider/"),
        json!({"source": "./pluxy-cliproxyapi-provider", "skills": []}),
    ] {
        assert!(pi::is_ours(&e), "{e}");
    }
    assert!(!pi::is_ours(&json!("../pluxy-cliproxyapi-provider")));
    for e in [
        json!("npm:@router-for-me/pi-cliproxyapi-provider"),
        json!("npm:@router-for-me/pi-cliproxyapi-provider@1.4.20"),
        json!({"source": "npm:@router-for-me/pi-cliproxyapi-provider@latest"}),
    ] {
        assert!(pi::is_legacy(&e), "{e}");
    }
    assert!(!pi::is_legacy(&json!(
        "npm:@router-for-me/pi-cliproxyapi-provider-x"
    )));
}

#[test]
fn targets_are_the_three_files_of_one_folder() {
    let root = |p: &str| Selection::Root {
        root: PathBuf::from("/a"),
        path: p.into(),
    };
    let ok = [
        root("cliproxyapi.json"),
        root("settings.json"),
        root("cliproxyapi-models.json"),
    ];
    assert_eq!(
        crate::agent_of(&[&ok[0], &ok[1], &ok[2]]).unwrap(),
        PathBuf::from("/a")
    );
    let file = |p: &str| Selection::File(PathBuf::from(p));
    let mixed = [
        file("/a/cliproxyapi.json"),
        file("/b/settings.json"),
        file("/a/cliproxyapi-models.json"),
    ];
    assert!(crate::agent_of(&[&mixed[0], &mixed[1], &mixed[2]]).is_err());
    let swapped = [
        root("settings.json"),
        root("cliproxyapi.json"),
        root("cliproxyapi-models.json"),
    ];
    assert!(crate::agent_of(&[&swapped[0], &swapped[1], &swapped[2]]).is_err());
}

/// A fresh folder under the system's temporary folder.
fn temp(name: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!("pi-setup-test-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    d
}

/// Every file under dir, by relative path, with its bytes.
fn tree(dir: &Path) -> Vec<(String, Vec<u8>)> {
    let mut out = Vec::new();
    let mut stack = vec![dir.to_path_buf()];
    while let Some(d) = stack.pop() {
        for e in std::fs::read_dir(&d).unwrap() {
            let p = e.unwrap().path();
            if p.is_dir() {
                stack.push(p);
            } else {
                let rel = p.strip_prefix(dir).unwrap().to_string_lossy().into_owned();
                out.push((rel, std::fs::read(&p).unwrap()));
            }
        }
    }
    out.sort();
    out
}

#[test]
fn the_plugin_is_installed_updated_and_removed_only_as_pluxys() {
    let agent = temp("adapter");
    let mut want: Vec<(String, Vec<u8>)> = adapter::files()
        .iter()
        .map(|(n, b)| (n.to_string(), b.to_vec()))
        .collect();
    want.push((
        ".pluxy-adapter.json".into(),
        b"{\n  \"package\": \"pluxy/pi-setup\",\n  \"adapter\": \"@router-for-me/pi-cliproxyapi-provider\",\n  \"version\": \"1.4.25-pluxy.1\"\n}\n".to_vec(),
    ));
    want.sort();

    assert_eq!(adapter::state(&agent), State::Absent);
    assert!(adapter::update(&agent).is_err());
    assert!(adapter::ensure(&agent).unwrap());
    let dir = agent.join("pluxy-cliproxyapi-provider");
    assert_eq!(tree(&dir), want);
    assert_eq!(
        adapter::state(&agent),
        State::Installed(Some("1.4.25-pluxy.1".into()))
    );
    // Installed: kept as it is.
    assert!(!adapter::ensure(&agent).unwrap());

    // An older copy with a stale file: an update makes it exactly the carried one.
    std::fs::write(
        dir.join(".pluxy-adapter.json"),
        adapter::marker("1.4.24-pluxy.1"),
    )
    .unwrap();
    std::fs::write(dir.join("extensions/old.ts"), "// stale\n").unwrap();
    let v = adapter::state(&agent).to_json();
    assert_eq!(v["update"], true);
    assert_eq!(v["version"], "1.4.24-pluxy.1");
    adapter::update(&agent).unwrap();
    assert_eq!(tree(&dir), want);
    let names: Vec<String> = std::fs::read_dir(&agent)
        .unwrap()
        .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
        .collect();
    assert_eq!(names, ["pluxy-cliproxyapi-provider"]);

    assert!(adapter::remove(&agent).unwrap());
    assert!(!dir.exists());
    assert!(!adapter::remove(&agent).unwrap());

    // Someone else's folder of that name: refused, never removed.
    std::fs::create_dir(&dir).unwrap();
    std::fs::write(dir.join("package.json"), "{\"name\":\"someone-else\"}\n").unwrap();
    assert_eq!(adapter::state(&agent), State::Foreign);
    assert!(adapter::ensure(&agent)
        .unwrap_err()
        .message
        .contains("not pluxy's"));
    assert!(adapter::update(&agent).is_err());
    assert!(!adapter::remove(&agent).unwrap());
    assert!(dir.join("package.json").exists());
    std::fs::remove_dir_all(&agent).unwrap();
}

#[test]
fn the_carried_copy_keeps_its_cache_in_its_own_folder() {
    let (_, stream) = adapter::files()
        .iter()
        .find(|(n, _)| *n == "extensions/codex-stream.ts")
        .unwrap();
    let stream = std::str::from_utf8(stream).unwrap();
    assert!(!stream.contains("tmpdir()"));
    assert!(stream.contains(r#"join(dirname(fileURLToPath(import.meta.url)), "..", ".cache")"#));
}

#[test]
fn a_preview_reads_again_once_when_a_file_changed_between_its_reads() {
    use serde_json::json;
    let pinned = |a: &[u8], b: &[u8]| {
        json!({"targets": [
            {"sha256": pluxy_transactions::sha256_of(Some(a))},
            {"sha256": pluxy_transactions::sha256_of(Some(b))},
        ]})
    };
    // Changed during the first read only: the second read's values are shown.
    let mut n = 0;
    let (_, [a, _]) = crate::pinned_read(|| {
        n += 1;
        let bytes = if n == 1 {
            b"other".to_vec()
        } else {
            b"now".to_vec()
        };
        Ok((pinned(b"now", b"s"), [Some(bytes), Some(b"s".to_vec())]))
    })
    .unwrap();
    assert_eq!(a.as_deref(), Some(&b"now"[..]));
    // Changed during both: refused.
    let e = crate::pinned_read(|| {
        Ok((
            pinned(b"x", b"s"),
            [Some(b"y".to_vec()), Some(b"s".to_vec())],
        ))
    })
    .unwrap_err();
    assert!(e.message.contains("changed while it was read"));
}

#[test]
fn a_preview_takes_the_three_files_selected() {
    use serde_json::json;
    let input = json!({"targets": [
        {"file": "/s/cliproxyapi.json"}, {"file": "/s/settings.json"}, {"file": "/s/cliproxyapi-models.json"},
    ]});
    let sels = crate::selections(&input).unwrap();
    assert_eq!(
        crate::agent_of(&[&sels[0], &sels[1], &sels[2]]).unwrap(),
        PathBuf::from("/s")
    );
    assert!(crate::selections(&json!({"targets": [{"file": "/s/settings.json"}]})).is_err());
}
