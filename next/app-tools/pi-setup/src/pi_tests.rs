//! The bytes here are written by hand (the same as the native check's fixtures, native/pi.ts),
//! never worked out by the code under test.

use crate::adapter::{self, State};
use crate::pi::{self, PiMerge, Settings};
use pluxy_transactions::{hook, Merge, Selection, Step, Transactions};
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

/// A full path on this system: under `/`, or `C:\` on Windows.
fn abs(p: &str) -> PathBuf {
    PathBuf::from(if cfg!(windows) { "C:\\" } else { "/" }).join(p)
}

#[test]
fn targets_are_the_three_files_of_one_folder() {
    let root = |p: &str| Selection::Root {
        root: abs("a"),
        path: p.into(),
    };
    let ok = [
        root("cliproxyapi.json"),
        root("settings.json"),
        root("cliproxyapi-models.json"),
    ];
    assert_eq!(
        crate::agent_of(&[&ok[0], &ok[1], &ok[2]]).unwrap(),
        abs("a")
    );
    let file = |p: &str| Selection::File(abs(p));
    let mixed = [
        file("a/cliproxyapi.json"),
        file("b/settings.json"),
        file("a/cliproxyapi-models.json"),
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
fn files_under(dir: &Path) -> Vec<(String, Vec<u8>)> {
    let mut out = Vec::new();
    let mut stack = vec![dir.to_path_buf()];
    while let Some(d) = stack.pop() {
        for e in std::fs::read_dir(&d).unwrap() {
            let p = e.unwrap().path();
            if p.is_dir() {
                stack.push(p);
            } else {
                // Joined with `/` on every system, as the adapter's own file names are.
                let rel = p
                    .strip_prefix(dir)
                    .unwrap()
                    .components()
                    .map(|c| c.as_os_str().to_string_lossy().into_owned())
                    .collect::<Vec<_>>()
                    .join("/");
                out.push((rel, std::fs::read(&p).unwrap()));
            }
        }
    }
    out.sort();
    out
}

/// Every entry under dir, by relative path, never following a link: each file with its bytes, each
/// folder (an empty one too) and each link with its text, so a link pointed elsewhere, a folder
/// added or a hidden folder left beside the plugin all show.
fn tree(dir: &Path) -> Vec<String> {
    let mut out = Vec::new();
    let mut stack = vec![dir.to_path_buf()];
    while let Some(d) = stack.pop() {
        for e in std::fs::read_dir(&d).unwrap() {
            let p = e.unwrap().path();
            let rel = p
                .strip_prefix(dir)
                .unwrap()
                .components()
                .map(|c| c.as_os_str().to_string_lossy().into_owned())
                .collect::<Vec<_>>()
                .join("/");
            let m = std::fs::symlink_metadata(&p).unwrap();
            if m.file_type().is_symlink() {
                out.push(format!(
                    "{rel} -> {}",
                    std::fs::read_link(&p).unwrap().display()
                ));
            } else if m.is_dir() {
                out.push(format!("{rel}/"));
                stack.push(p);
            } else {
                out.push(format!(
                    "{rel}: {}",
                    String::from_utf8_lossy(&std::fs::read(&p).unwrap())
                ));
            }
        }
    }
    out.sort();
    out
}

/// The pin of pluxy's copy in agent, as a preview takes it.
fn pinned(agent: &Path) -> pluxy_transactions::Pin {
    pluxy_transactions::Target::resolve(&adapter::selection(agent))
        .unwrap()
        .pin
}

/// The agent folder as a write's file transaction pins it: its resolved path and identity.
fn agent_root(agent: &Path) -> (PathBuf, pluxy_transactions::Id) {
    let pin = pluxy_transactions::Target::resolve(&Selection::Root {
        root: agent.to_path_buf(),
        path: "settings.json".into(),
    })
    .unwrap()
    .pin;
    (pin.root.unwrap(), pin.root_id.unwrap())
}

/// The library's operations with vault, and a data folder of their own beside agent's folder.
fn tx<'a>(vault: &'a Mem, agent: &Path) -> Transactions<'a> {
    let name = agent.file_name().unwrap().to_string_lossy();
    Transactions::new(vault, agent.with_file_name(format!("{name}-data")))
}

/// The names in a folder, sorted.
fn names(dir: &Path) -> Vec<String> {
    let mut n: Vec<String> = std::fs::read_dir(dir)
        .unwrap()
        .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
        .collect();
    n.sort();
    n
}

/// The hidden folder an update or removal moved the plugin aside to in agent, if any.
fn aside_in(agent: &Path) -> Option<PathBuf> {
    std::fs::read_dir(agent)
        .unwrap()
        .map(|e| e.unwrap().path())
        .find(|p| {
            let n = p.file_name().unwrap().to_string_lossy();
            n.starts_with(".pluxy-cliproxyapi-provider.") && n.ends_with(".old")
        })
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

    let vault = Mem::default();
    let tx = tx(&vault, &agent);
    assert_eq!(adapter::state(&agent), State::Absent);
    assert!(adapter::ensure(&tx, &agent).unwrap().is_some());
    let dir = agent.join("pluxy-cliproxyapi-provider");
    assert_eq!(files_under(&dir), want);
    assert_eq!(
        adapter::state(&agent),
        State::Installed(Some("1.4.25-pluxy.1".into()))
    );
    // Installed: kept as it is.
    assert!(adapter::ensure(&tx, &agent).unwrap().is_none());

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
    let pin = pinned(&agent);
    adapter::update(&tx, &adapter::pinned(&agent, &pin).unwrap()).unwrap();
    assert_eq!(files_under(&dir), want);
    assert_eq!(names(&agent), ["pluxy-cliproxyapi-provider"]);

    let (root, id) = agent_root(&agent);
    assert!(adapter::remove_in(&tx, &root, id).unwrap());
    assert!(!dir.exists());
    assert!(!adapter::remove_in(&tx, &root, id).unwrap());
    // Nothing there: nothing to update.
    assert!(adapter::pinned(&agent, &pin).is_err());
    // Each change was journalled and committed.
    assert!(vault.open().is_empty() && vault.all_committed());

    // Someone else's folder of that name: refused, never removed.
    std::fs::create_dir(&dir).unwrap();
    std::fs::write(dir.join("package.json"), "{\"name\":\"someone-else\"}\n").unwrap();
    assert_eq!(adapter::state(&agent), State::Foreign);
    assert!(adapter::ensure(&tx, &agent)
        .unwrap_err()
        .message
        .contains("not pluxy's"));
    assert!(adapter::pinned(&agent, &pin).is_err());
    assert!(!adapter::remove_in(&tx, &root, id).unwrap());
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
    let file = |n: &str| json!({"file": abs("s").join(n)});
    let input = json!({"targets": [
        file("cliproxyapi.json"), file("settings.json"), file("cliproxyapi-models.json"),
    ]});
    let home = || Ok(abs("home"));
    let crate::Selected::Files(sels) = crate::selections(&input, &home).unwrap() else {
        panic!("the selected files are read")
    };
    assert_eq!(
        crate::agent_of(&[&sels[0], &sels[1], &sels[2]]).unwrap(),
        abs("s")
    );
    assert!(crate::selections(&json!({"targets": [file("settings.json")]}), &home).is_err());
}

#[test]
fn clear_and_uninstall_refuse_files_pi_or_the_plugin_would_not_take() {
    use pluxy_transactions::Target;
    let agent = temp("checked");
    let pins = |agent: &Path| {
        crate::FILES.map(|n| {
            Target::resolve(&Selection::File(agent.join(n)))
                .unwrap()
                .pin
        })
    };
    // The person's files, and no files at all: taken.
    std::fs::write(agent.join("cliproxyapi.json"), CONFIG_APPLIED).unwrap();
    std::fs::write(agent.join("settings.json"), APPLIED).unwrap();
    crate::checked(&pins(&agent)).unwrap();
    let empty = temp("checked-empty");
    crate::checked(&pins(&empty)).unwrap();
    // JSON of a shape the plugin or Pi would not take, in either file: refused with why.
    for (config, settings, says) in [
        (CONFIG_APPLIED, "{\"defaultModel\": 5}\n", "defaultModel"),
        (
            CONFIG_APPLIED,
            "{\"packages\": {\"npm:x\": true}}\n",
            "packages",
        ),
        ("{\"providerId\": 7}\n", APPLIED, "providerId"),
        ("[]\n", APPLIED, "object"),
        (CONFIG_APPLIED, "{\n  \"theme\": \"dark\",,\n}\n", "JSON"),
    ] {
        std::fs::write(agent.join("cliproxyapi.json"), config).unwrap();
        std::fs::write(agent.join("settings.json"), settings).unwrap();
        let e = crate::checked(&pins(&agent)).unwrap_err();
        assert!(e.message.contains(says), "{says}: {}", e.message);
    }
    std::fs::remove_dir_all(&agent).unwrap();
    std::fs::remove_dir_all(&empty).unwrap();
}

#[test]
fn the_file_systems_root_is_no_agent_folder() {
    // An empty folder field on the page sends empty paths; a bare `/` folder is refused too.
    let at = |d: &str| crate::FILES.map(|n| Selection::File(PathBuf::from(format!("{d}{n}"))));
    // A drive's root on Windows too.
    for d in ["/", "", if cfg!(windows) { "C:\\" } else { "/" }] {
        let s = at(d);
        assert!(crate::agent_of(&[&s[0], &s[1], &s[2]]).is_err(), "{d:?}");
    }
    let root = |p: &str| Selection::Root {
        root: abs(""),
        path: p.into(),
    };
    let s = crate::FILES.map(root);
    assert!(crate::agent_of(&[&s[0], &s[1], &s[2]]).is_err());
}

#[test]
fn removing_the_plugin_leaves_nothing_under_its_name_or_beside_it() {
    let agent = temp("remove");
    let vault = Mem::default();
    let tx = tx(&vault, &agent);
    adapter::ensure(&tx, &agent).unwrap();
    let (root, id) = agent_root(&agent);
    assert!(adapter::remove_in(&tx, &root, id).unwrap());
    assert!(names(&agent).is_empty());
    // An agent folder that is not the one pinned any more: nothing removed.
    adapter::ensure(&tx, &agent).unwrap();
    let other = temp("remove-other");
    assert!(!adapter::remove_in(&tx, &root, agent_root(&other).1).unwrap());
    assert!(adapter::state(&agent) != State::Absent);
    std::fs::remove_dir_all(&other).unwrap();
    std::fs::remove_dir_all(&agent).unwrap();
}

#[test]
fn a_stop_part_way_through_leaves_nothing_hidden_once_the_next_start_reconciles() {
    use Step::*;
    // A stop (as a crash) at each step of an update or removal: the next start's reconcile puts
    // back the old copy until the new one is in place (update) or the marker is gone (removal),
    // and finishes the change after that. Never a hidden folder left beside it.
    for (op, step, outcome) in [
        ("update", Written, "rolled_back"),
        ("update", Checked, "rolled_back"),
        ("update", Moved, "rolled_back"),
        ("update", Placed, "committed"),
        ("remove", Checked, "rolled_back"),
        ("remove", Moved, "rolled_back"),
        ("remove", Unmarked, "committed"),
    ] {
        let case = format!("{op} stopped at {step:?}");
        let base = temp(&format!("stop-{op}-{step:?}"));
        let agent = base.join("agent");
        set_up(&agent, "a");
        let vault = Mem::default();
        let tx = tx(&vault, &agent);
        let old = files_under(&agent.join("pluxy-cliproxyapi-provider"));
        let folder = adapter::pinned(&agent, &pinned(&agent)).unwrap();
        hook(Some(Box::new(move |s| s == step)));
        let r = match op {
            "update" => adapter::update(&tx, &folder),
            _ => adapter::remove(&tx, &folder),
        };
        hook(None);
        drop(folder);
        assert!(r.unwrap_err().message.contains("stopped at"), "{case}");
        let reports = tx.reconcile(&vault.open()).unwrap();
        assert_eq!(reports[0]["outcome"], outcome, "{case}");
        let plugin = agent.join("pluxy-cliproxyapi-provider");
        match (op, outcome) {
            ("update", "committed") => assert_eq!(
                adapter::state(&agent),
                State::Installed(Some(adapter::VERSION.into())),
                "{case}"
            ),
            ("remove", "committed") => assert!(!plugin.exists(), "{case}"),
            _ => assert_eq!(files_under(&plugin), old, "{case}"),
        }
        let want: &[&str] = if (op, outcome) == ("remove", "committed") {
            &["cliproxyapi.json", "settings.json"]
        } else {
            &[
                "cliproxyapi.json",
                "pluxy-cliproxyapi-provider",
                "settings.json",
            ]
        };
        assert_eq!(names(&agent), want, "{case}");
        assert!(vault.open().is_empty(), "{case}");
        std::fs::remove_dir_all(&base).unwrap();
    }
}

#[test]
fn a_plugin_moved_aside_that_cannot_go_back_is_named_and_put_back_by_the_next_start() {
    let base = temp("stuck");
    let agent = base.join("agent");
    set_up(&agent, "a");
    let vault = Mem::default();
    let tx = tx(&vault, &agent);
    let folder = adapter::pinned(&agent, &pinned(&agent)).unwrap();
    // Once the copy is moved aside: its marker rewritten in place (so it is not the copy pinned)
    // and a folder put under the plugin's name (so it cannot go back).
    let at = agent.clone();
    hook(Some(Box::new(move |s| {
        if s == Step::Moved {
            let aside = aside_in(&at).unwrap();
            std::fs::write(aside.join(adapter::MARKER), adapter::marker("9.9.9")).unwrap();
            std::fs::create_dir(at.join("pluxy-cliproxyapi-provider")).unwrap();
        }
        false
    })));
    let e = adapter::update(&tx, &folder).unwrap_err();
    hook(None);
    drop(folder);
    // The error says where the person's copy is (by its resolved path, as the library reads the
    // agent folder); it is all there, and the next start puts it back once the name is free.
    let aside = aside_in(&agent).unwrap();
    let resolved = std::fs::canonicalize(&aside).unwrap();
    assert!(
        e.message.contains(&resolved.display().to_string())
            && e.message.contains("preview it again"),
        "{}",
        e.message
    );
    assert_eq!(std::fs::read(aside.join("whose.txt")).unwrap(), b"a");
    std::fs::remove_dir(agent.join("pluxy-cliproxyapi-provider")).unwrap();
    let reports = tx.reconcile(&vault.open()).unwrap();
    assert_eq!(reports[0]["outcome"], "rolled_back");
    assert_eq!(
        std::fs::read(agent.join("pluxy-cliproxyapi-provider/whose.txt")).unwrap(),
        b"a"
    );
    assert!(aside_in(&agent).is_none());
    std::fs::remove_dir_all(&base).unwrap();
}

#[test]
fn the_recipe_starts_the_installers_pi_where_this_system_runs_it_else_the_one_on_path() {
    let home = temp("recipe");
    let program = |os: &str| crate::recipe(os, &home)["program"].clone();
    for os in ["linux", "macos", "windows"] {
        assert_eq!(program(os), "pi", "{os}");
    }
    // pi.dev/install.sh's copies: ~/.local/bin/pi, and its fallback in Pi's folder first.
    // install.ps1's pi.cmd launcher is never named (it is not an npm shim).
    let write = |p: &Path| {
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(p, "MZ").unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(p, std::fs::Permissions::from_mode(0o755)).unwrap();
        }
    };
    let (local_bin, own) = (home.join(".local/bin/pi"), home.join(".pi/agent/bin/pi"));
    write(&local_bin);
    write(&home.join(".pi/agent/bin/pi.cmd"));
    write(&home.join(".pi/agent/bin/pi.exe"));
    // A file without an extension is no program on Windows.
    let unix = |p: &Path| {
        if cfg!(unix) {
            serde_json::json!(p)
        } else {
            serde_json::json!("pi")
        }
    };
    assert_eq!(program("linux"), unix(&local_bin));
    assert_eq!(program("macos"), unix(&local_bin));
    write(&own);
    assert_eq!(program("linux"), unix(&own));
    assert_eq!(program("macos"), unix(&own));
    assert_eq!(program("windows"), "pi");
    // On Windows no installed copy is looked for at all (also where this test runs on Windows,
    // which runs none of the Unix copies anyway).
    assert!(crate::installed("windows", &home).is_empty());
    std::fs::remove_dir_all(home).unwrap();
}

/// The vault's frames as the host answers them, in memory (as lib/transactions' own tests do).
#[derive(Default)]
struct Mem {
    backups: std::cell::RefCell<std::collections::BTreeMap<String, (String, String)>>,
    journals:
        std::cell::RefCell<std::collections::BTreeMap<String, (serde_json::Value, Option<String>)>>,
    next: std::cell::Cell<u64>,
}

impl Mem {
    /// The open journals as describe names them.
    fn open(&self) -> Vec<serde_json::Value> {
        self.journals
            .borrow()
            .iter()
            .filter(|(_, (_, o))| o.is_none())
            .map(|(id, (intent, _))| serde_json::json!({"id": id, "intent": intent}))
            .collect()
    }

    /// Whether every journal begun is ended as committed.
    fn all_committed(&self) -> bool {
        self.journals
            .borrow()
            .values()
            .all(|(_, o)| o.as_deref() == Some("committed"))
    }
}

impl pluxy_transactions::Vault for Mem {
    fn vault(
        &self,
        op: &str,
        f: serde_json::Map<String, serde_json::Value>,
    ) -> Result<serde_json::Value, pluxy_program::Error> {
        use pluxy_program::Error;
        use serde_json::json;
        let s = |k: &str| {
            f.get(k)
                .and_then(|v| v.as_str())
                .unwrap_or_default()
                .to_string()
        };
        let id = |p: &str| {
            self.next.set(self.next.get() + 1);
            format!("{p}-{}", self.next.get())
        };
        match op {
            "backup.put" => {
                let id = id("b");
                self.backups
                    .borrow_mut()
                    .insert(id.clone(), (s("label"), s("data")));
                Ok(json!({"backup": id}))
            }
            "backup.get" => {
                let b = self.backups.borrow();
                let (label, data) = b
                    .get(&s("backup"))
                    .ok_or_else(|| Error::not_found("no backup"))?;
                Ok(json!({"backup": s("backup"), "label": label, "data": data}))
            }
            "backup.list" => Ok(json!({"backups": self.backups.borrow().iter()
                .map(|(id, (label, _))| json!({"backup": id, "label": label})).collect::<Vec<_>>()})),
            "backup.delete" => {
                self.backups.borrow_mut().remove(&s("backup"));
                Ok(json!({}))
            }
            "journal.begin" => {
                // As the host: no other journal begins while one is open.
                if !self.open().is_empty() {
                    return Err(Error::unavailable("a journal is open"));
                }
                let id = id("j");
                self.journals
                    .borrow_mut()
                    .insert(id.clone(), (f["intent"].clone(), None));
                Ok(json!({"journal": id}))
            }
            "journal.commit" | "journal.end" => {
                let outcome = if op == "journal.commit" {
                    "committed".to_string()
                } else {
                    s("outcome")
                };
                let mut j = self.journals.borrow_mut();
                let e = j
                    .get_mut(&s("journal"))
                    .ok_or_else(|| Error::not_found("no journal"))?;
                if e.1.as_ref().is_some_and(|o| *o != outcome) {
                    return Err(Error::invalid_request("ended already with another outcome"));
                }
                e.1 = Some(outcome.clone());
                Ok(json!({"journal": s("journal"), "outcome": outcome}))
            }
            _ => Err(Error::unsupported(op.to_string())),
        }
    }
}

/// A link at at to the folder target: a symbolic link, or on Windows a junction (the folder link
/// any account may make, as lib/transactions' tests make it), so these tests run on every
/// account.
fn link_dir(target: &Path, at: &Path) {
    #[cfg(unix)]
    std::os::unix::fs::symlink(target, at).unwrap();
    #[cfg(windows)]
    {
        let out = std::process::Command::new("cmd")
            .args(["/C", "mklink", "/J"])
            .arg(at)
            .arg(target)
            .output()
            .unwrap();
        assert!(out.status.success(), "{out:?}");
    }
}

/// Takes away the folder link at at, leaving what it points to.
fn unlink_dir(at: &Path) {
    #[cfg(unix)]
    std::fs::remove_file(at).unwrap();
    #[cfg(windows)]
    std::fs::remove_dir(at).unwrap();
}

/// An agent folder at dir set up by pluxy, with an older copy of the plugin (so the page offers
/// Update plugin) holding a file that says whose it is.
fn set_up(dir: &Path, whose: &str) {
    std::fs::create_dir_all(dir).unwrap();
    std::fs::write(dir.join("cliproxyapi.json"), CONFIG_APPLIED).unwrap();
    std::fs::write(dir.join("settings.json"), APPLIED).unwrap();
    let vault = Mem::default();
    adapter::ensure(
        &Transactions::new(&vault, temp(&format!("set-up-{whose}"))),
        dir,
    )
    .unwrap();
    let plugin = dir.join("pluxy-cliproxyapi-provider");
    std::fs::write(
        plugin.join(".pluxy-adapter.json"),
        adapter::marker("1.4.24-pluxy.1"),
    )
    .unwrap();
    std::fs::write(plugin.join("whose.txt"), whose).unwrap();
}

/// What the Pi page sends for Update plugin and Uninstall plugin from its reading (pi.tsx): the
/// reading's pins of the three files and of pluxy's copy of the plugin.
fn sent(preview: &serde_json::Value) -> serde_json::Value {
    serde_json::json!({"targets": preview["targets"], "plugin": preview["pi"]["adapter"]["pin"]})
}

/// How the agent folder is swapped between the preview and the confirmation.
#[derive(Clone, Copy, Debug)]
enum Swap {
    /// The agent folder is a link, pointed at another agent folder.
    Link,
    /// The agent folder is moved away and another put in its place.
    Folder,
    /// The plugin's folder in it is moved away and another copy put in its place.
    Plugin,
    /// Nothing: the confirmation acts on the folder shown.
    None,
}

/// Every entry under a folder (`tree`).
type Tree = Vec<String>;

/// A confirmation's reply, the whole tree before it and after it, and the folder holding both.
type Confirmed = (
    Result<serde_json::Value, pluxy_program::Error>,
    Tree,
    Tree,
    PathBuf,
);

/// Previews an agent folder (selected as a folder, or as its linked files when files), swaps it,
/// and confirms op with what the page sends: the reply, the whole tree before the confirmation and
/// after it, and the folder shown.
fn confirm_after(op: &str, swap: Swap, files: bool) -> Confirmed {
    let base = temp(&format!("swap-{op}-{swap:?}-{files}"));
    let data = temp(&format!("swap-{op}-{swap:?}-{files}-data"));
    let (a, b, shown) = (base.join("a"), base.join("b"), base.join("agent"));
    set_up(&a, "a");
    set_up(&b, "b");
    match swap {
        Swap::Link => link_dir(&a, &shown),
        _ => std::fs::rename(&a, &shown).unwrap(),
    }
    let input = if files {
        serde_json::json!({"targets": crate::FILES.map(|n| serde_json::json!({"file": shown.join(n)}))})
    } else {
        serde_json::json!({"folder": shown})
    };
    let vault = Mem::default();
    let preview = crate::preview(&vault, data.clone(), &input).unwrap();
    assert_eq!(preview["pi"]["adapter"]["version"], "1.4.24-pluxy.1");
    let plugin = "pluxy-cliproxyapi-provider";
    match swap {
        Swap::Link => {
            unlink_dir(&shown);
            link_dir(&b, &shown);
        }
        Swap::Folder => {
            std::fs::rename(&shown, base.join("moved")).unwrap();
            std::fs::rename(&b, &shown).unwrap();
        }
        Swap::Plugin => {
            std::fs::rename(shown.join(plugin), base.join("moved")).unwrap();
            std::fs::rename(b.join(plugin), shown.join(plugin)).unwrap();
        }
        Swap::None => {}
    }
    let before = tree(&base);
    let r = match op {
        "update" => crate::update(&vault, data.clone(), &sent(&preview)),
        _ => crate::uninstall(&vault, data.clone(), &sent(&preview)),
    };
    let after = tree(&base);
    std::fs::remove_dir_all(&data).unwrap();
    (r, before, after, base)
}

#[test]
fn update_and_uninstall_refuse_a_folder_swapped_since_the_preview() {
    let mut failed = Vec::new();
    for op in ["update", "uninstall"] {
        for (swap, files) in [
            (Swap::Link, false),
            (Swap::Link, true),
            (Swap::Folder, false),
            (Swap::Folder, true),
            (Swap::Plugin, false),
            (Swap::Plugin, true),
        ] {
            let (r, before, after, base) = confirm_after(op, swap, files);
            let case = format!("{op} after {swap:?} (files: {files})");
            // Nothing in either folder changed: no file written, moved or removed.
            if before != after {
                failed.push(format!("{case} changed the folders: {r:?}"));
            }
            match r {
                Err(e) if e.message.contains("preview it again") => {}
                r => failed.push(format!("{case} was not refused as changed: {r:?}")),
            }
            std::fs::remove_dir_all(&base).unwrap();
        }
    }
    assert!(failed.is_empty(), "{}", failed.join("\n"));
}

#[test]
fn update_and_uninstall_act_on_the_folder_the_preview_showed() {
    for files in [false, true] {
        let (r, _, _, base) = confirm_after("update", Swap::None, files);
        assert_eq!(r.unwrap()["outcome"], "updated");
        let plugin = base.join("agent").join("pluxy-cliproxyapi-provider");
        assert_eq!(
            adapter::state(&base.join("agent")),
            State::Installed(Some(adapter::VERSION.into()))
        );
        assert!(!plugin.join("whose.txt").exists());
        // The other agent folder is as it was.
        assert_eq!(
            std::fs::read(base.join("b/pluxy-cliproxyapi-provider/whose.txt")).unwrap(),
            b"b"
        );
        std::fs::remove_dir_all(&base).unwrap();

        let (r, _, _, base) = confirm_after("uninstall", Swap::None, files);
        let v = r.unwrap();
        assert_eq!(v["adapter_removed"], true);
        assert!(!base.join("agent/pluxy-cliproxyapi-provider").exists());
        assert_eq!(
            text(std::fs::read(base.join("agent/settings.json")).unwrap()),
            UNINSTALLED
        );
        assert_eq!(
            std::fs::read(base.join("b/pluxy-cliproxyapi-provider/whose.txt")).unwrap(),
            b"b"
        );
        assert_eq!(
            text(std::fs::read(base.join("b/settings.json")).unwrap()),
            APPLIED
        );
        std::fs::remove_dir_all(&base).unwrap();
    }
}

#[test]
fn uninstall_leaves_a_copy_installed_since_a_preview_that_found_none() {
    let agent = temp("installed-since");
    let data = temp("installed-since-data");
    std::fs::write(agent.join("cliproxyapi.json"), CONFIG_APPLIED).unwrap();
    std::fs::write(agent.join("settings.json"), APPLIED).unwrap();
    let vault = Mem::default();
    let preview =
        crate::preview(&vault, data.clone(), &serde_json::json!({"folder": agent})).unwrap();
    assert_eq!(preview["pi"]["adapter"]["pin"], serde_json::Value::Null);
    adapter::ensure(&tx(&vault, &agent), &agent).unwrap();
    let before = tree(&agent);
    let e = crate::uninstall(&vault, data.clone(), &sent(&preview)).unwrap_err();
    assert!(e.message.contains("preview it again"), "{}", e.message);
    assert!(tree(&agent) == before);
    std::fs::remove_dir_all(&agent).unwrap();
    std::fs::remove_dir_all(&data).unwrap();
}

/// What [`confirm_with`] saw: the reply, the tree just after the hook acted, the tree after the
/// confirmation, the journals it left open, the next start's reports on them and the tree after
/// those, and the folder holding it all.
struct Run {
    r: Result<serde_json::Value, pluxy_program::Error>,
    seen: Tree,
    after: Tree,
    open: usize,
    reports: Vec<serde_json::Value>,
    settled: Tree,
    base: PathBuf,
}

/// Previews an agent folder (a link to a, beside another agent folder b) and confirms op with what
/// the page sends, with hook acting between the steps of the plugin folder's change (as another
/// program would); then reconciles what it left open, as the next start does.
fn confirm_with(op: &str, name: &str, step: Step, act: impl Fn(&Path) + 'static) -> Run {
    let base = temp(name);
    let data = temp(&format!("{name}-data"));
    let (a, b, shown) = (base.join("a"), base.join("b"), base.join("agent"));
    set_up(&a, "a");
    set_up(&b, "b");
    link_dir(&a, &shown);
    let vault = Mem::default();
    let preview =
        crate::preview(&vault, data.clone(), &serde_json::json!({"folder": shown})).unwrap();
    let seen = std::rc::Rc::new(std::cell::RefCell::new(Vec::new()));
    let (at, keep) = (base.clone(), seen.clone());
    hook(Some(Box::new(move |s| {
        if s == step {
            act(&at);
            // Without the new copy an update writes beside the plugin before it moves it: its own,
            // unfinished, which it removes when it refuses.
            let new = |e: &String| {
                e.strip_prefix("a/.pluxy-cliproxyapi-provider.pluxy-")
                    .and_then(|r| r.split('/').next())
                    .is_some_and(|n| n.ends_with(".new"))
            };
            *keep.borrow_mut() = tree(&at).into_iter().filter(|e| !new(e)).collect();
        }
        false
    })));
    let r = match op {
        "update" => crate::update(&vault, data.clone(), &sent(&preview)),
        _ => crate::uninstall(&vault, data.clone(), &sent(&preview)),
    };
    hook(None);
    let after = tree(&base);
    let open = vault.open();
    let reports = Transactions::new(&vault, data.clone())
        .reconcile(&open)
        .unwrap();
    let settled = tree(&base);
    std::fs::remove_dir_all(&data).unwrap();
    let seen = seen.borrow().clone();
    Run {
        r,
        seen,
        after,
        open: open.len(),
        reports,
        settled,
        base,
    }
}

#[test]
fn a_plugin_folder_replaced_after_the_check_is_left_as_it_was_and_the_write_refused() {
    let plugin = "pluxy-cliproxyapi-provider";
    // After the operation checked the copy and before it moved it: the copy replaced by another
    // (b's). The move is refused, the folder under the name is left as it was (b's copy, which
    // moved and went back), and no copy is written or removed.
    for op in ["update", "uninstall"] {
        let run = confirm_with(
            op,
            &format!("late-plugin-{op}"),
            Step::Checked,
            move |base| {
                std::fs::rename(base.join("a").join(plugin), base.join("moved")).unwrap();
                std::fs::rename(base.join("b").join(plugin), base.join("a").join(plugin)).unwrap();
            },
        );
        let e = run.r.unwrap_err();
        assert!(
            e.message.contains("preview it again"),
            "{op}: {}",
            e.message
        );
        assert_eq!(
            run.after, run.seen,
            "{op}: the folders changed after the swap"
        );
        assert_eq!(run.open, 0, "{op}: a journal was left open");
        std::fs::remove_dir_all(&run.base).unwrap();
    }
}

#[test]
fn a_marker_rewritten_in_place_once_moved_aside_is_never_removed() {
    let plugin = "pluxy-cliproxyapi-provider";
    let older = || adapter::marker("1.4.23-pluxy.1");
    // The copy moved aside and its marker rewritten in place (same file, other bytes) at the latest
    // point a test reaches: for an update once the new copy is in place, just before the old copy
    // is checked again and removed; for an uninstall once moved, before what moved is checked and
    // its marker removed. It is not the copy pinned, so it is never removed.
    for (op, step) in [("update", Step::Placed), ("uninstall", Step::Moved)] {
        let run = confirm_with(op, &format!("late-marker-{op}"), step, move |base| {
            let aside = aside_in(&base.join("a")).unwrap();
            std::fs::write(aside.join(adapter::MARKER), older()).unwrap();
        });
        let e = run.r.unwrap_err();
        let aside = aside_name(&run.seen);
        if op == "update" {
            // The new copy is in place; the old one stays aside, all of it, with its new marker,
            // and the next start names it instead of removing it.
            assert!(
                e.message.contains("is in place") && e.message.contains("not the old copy pinned"),
                "{op}: {}",
                e.message
            );
            assert_eq!(
                adapter::state(&run.base.join("a")),
                State::Installed(Some(adapter::VERSION.into()))
            );
            let at = run.base.join(&aside);
            assert_eq!(std::fs::read(at.join("whose.txt")).unwrap(), b"a", "{op}");
            assert_eq!(run.open, 1, "{op}");
            assert_eq!(run.reports[0]["outcome"], "committed", "{op}");
            assert_eq!(
                run.reports[0]["conflicts"],
                serde_json::json!([std::fs::canonicalize(&at).unwrap()]),
                "{op}"
            );
            assert_eq!(
                std::fs::read(at.join(adapter::MARKER)).unwrap(),
                older().as_bytes()
            );
        } else {
            // What moved goes back under its name, as it is now, and nothing else changed.
            assert!(
                e.message.contains("preview it again"),
                "{op}: {}",
                e.message
            );
            let mut back: Tree = run
                .seen
                .iter()
                .map(|e| match e.strip_prefix(&aside) {
                    Some(rest) => format!("a/{plugin}{rest}"),
                    None => e.clone(),
                })
                .collect();
            back.sort();
            assert_eq!(run.after, back, "{op}");
            assert_eq!(run.open, 0, "{op}");
            assert_eq!(
                std::fs::read(run.base.join("a").join(plugin).join(adapter::MARKER)).unwrap(),
                older().as_bytes()
            );
        }
        std::fs::remove_dir_all(&run.base).unwrap();
    }
}

/// The path, under the base folder, of the hidden folder a tree shows in a.
fn aside_name(seen: &Tree) -> String {
    seen.iter()
        .find_map(|e| {
            let p = e.strip_prefix("a/.pluxy-cliproxyapi-provider.")?;
            let end = p.find(".old")?;
            Some(format!("a/.pluxy-cliproxyapi-provider.{}", &p[..end + 4]))
        })
        .unwrap()
}

/// What tree shows of the copy this program carries in the agent folder at prefix: its folders and
/// its files with their bytes (the marker written by hand).
fn carried(prefix: &str) -> Tree {
    let dir = format!("{prefix}/pluxy-cliproxyapi-provider");
    let mut out = vec![format!("{dir}/")];
    for (n, b) in adapter::files() {
        out.push(format!("{dir}/{n}: {}", String::from_utf8_lossy(b)));
        let mut parts: Vec<&str> = n.split('/').collect();
        parts.pop();
        for i in 1..=parts.len() {
            let folder = format!("{dir}/{}/", parts[..i].join("/"));
            if !out.contains(&folder) {
                out.push(folder);
            }
        }
    }
    out.push(format!(
        "{dir}/.pluxy-adapter.json: {{\n  \"package\": \"pluxy/pi-setup\",\n  \"adapter\": \"@router-for-me/pi-cliproxyapi-provider\",\n  \"version\": \"1.4.25-pluxy.1\"\n}}\n"
    ));
    out
}

#[test]
fn a_link_pointed_elsewhere_after_the_check_leaves_the_other_folder_alone() {
    // The agent folder's link pointed at b after the operation checked the copy, or after it moved
    // the copy aside: the operation stays in the folder the preview showed (a, held open), the
    // copy is never left hidden, and nothing else changes anywhere.
    for (op, step) in [
        ("update", Step::Checked),
        ("update", Step::Moved),
        ("uninstall", Step::Checked),
        ("uninstall", Step::Moved),
    ] {
        let name = format!("late-link-{op}-{step:?}");
        let run = confirm_with(op, &name, step, |base| {
            unlink_dir(&base.join("agent"));
            link_dir(&base.join("b"), &base.join("agent"));
        });
        let case = format!("{op} after {step:?}");
        run.r.unwrap_or_else(|e| panic!("{case}: {e}"));
        assert_eq!(run.open, 0, "{case}: a journal was left open");
        // The whole tree: what the hook saw (the link already pointed at b), with a's copy, under
        // its name or aside, replaced by the carried one (update) or gone (uninstall).
        let mut want: Tree = run
            .seen
            .iter()
            .filter(|e| {
                !e.starts_with("a/pluxy-cliproxyapi-provider")
                    && !e.starts_with("a/.pluxy-cliproxyapi-provider.")
            })
            .cloned()
            .collect();
        if op == "update" {
            want.extend(carried("a"));
        }
        want.sort();
        assert_eq!(run.after, want, "{case}");
        std::fs::remove_dir_all(&run.base).unwrap();
    }
}

#[test]
fn a_hidden_copy_swapped_by_another_program_is_left_as_it_is_and_named() {
    let plugin = "pluxy-cliproxyapi-provider";
    // Another program acts at a step of an update or uninstall: it puts its own folder at the new
    // copy's hidden name, under the plugin's name once the new copy is in, or at the hidden name of
    // the copy moved aside. The tool removes or moves nothing but the copies it recorded: the other
    // folder stays as it is, the person's copy is never lost, and the next start names what it
    // cannot settle.
    for (op, step, which) in [
        ("update", Step::Checked, "new"),
        ("update", Step::Placed, "name"),
        ("uninstall", Step::Unmarked, "aside"),
    ] {
        let case = format!("{op}: {which} swapped at {step:?}");
        let put = std::rc::Rc::new(std::cell::RefCell::new(None::<PathBuf>));
        let keep = put.clone();
        let run = confirm_with(op, &format!("hidden-{op}-{which}"), step, move |base| {
            let a = base.join("a");
            let at = match which {
                "new" => std::fs::read_dir(&a)
                    .unwrap()
                    .map(|e| e.unwrap().path())
                    .find(|p| p.to_string_lossy().ends_with(".new"))
                    .unwrap(),
                "name" => a.join(plugin),
                _ => aside_in(&a).unwrap(),
            };
            std::fs::rename(&at, base.join("taken")).unwrap();
            std::fs::create_dir(&at).unwrap();
            std::fs::write(at.join("x.txt"), "x").unwrap();
            *keep.borrow_mut() = Some(at);
        });
        let e = run.r.unwrap_err();
        let a = run.base.join("a");
        let at = put.borrow().clone().unwrap();
        // The other program's folder: where it put it, as it put it, after the operation and
        // after the next start.
        let name = at.file_name().unwrap().to_string_lossy().into_owned();
        let entry = format!("a/{name}/x.txt: x");
        for (when, t) in [
            ("after it", &run.after),
            ("after the next start", &run.settled),
        ] {
            assert!(t.contains(&entry), "{case}, {when}: {} ({t:?})", e.message);
        }
        assert_eq!(run.open, 1, "{case}: {}", e.message);
        // The person's copy: under its name, or aside (another folder is under the name), or
        // where the other program moved it.
        let person = match which {
            "new" => a.join(plugin),
            "name" => run.base.join(aside_name(&run.settled)),
            _ => run.base.join("taken"),
        };
        assert_eq!(
            std::fs::read(person.join("whose.txt")).unwrap(),
            b"a",
            "{case}"
        );
        let named = match which {
            "name" => &person,
            _ => &at,
        };
        assert_eq!(
            run.reports[0]["conflicts"],
            serde_json::json!([std::fs::canonicalize(named).unwrap()]),
            "{case}"
        );
        // b, the other agent folder, is as it was.
        let b = |t: &Tree| {
            t.iter()
                .filter(|e| e.starts_with("b/"))
                .cloned()
                .collect::<Tree>()
        };
        assert_eq!(b(&run.settled), b(&run.seen), "{case}");
        std::fs::remove_dir_all(&run.base).unwrap();
    }
}

/// Sets up base/a, Pi's files as the person had them before pluxy, and base/b, another agent
/// folder holding pluxy's copy of the plugin while its settings.json does not list it; base/agent
/// is a link to a. The vault, the data folder, the base folder, and what the page sends to connect
/// the agent folder as previewed now (with its key's secrets).
fn two_agents(
    name: &str,
) -> (
    Mem,
    PathBuf,
    PathBuf,
    serde_json::Value,
    serde_json::Map<String, serde_json::Value>,
) {
    let base = temp(name);
    let data = temp(&format!("{name}-data"));
    let (a, b, shown) = (base.join("a"), base.join("b"), base.join("agent"));
    std::fs::create_dir_all(&a).unwrap();
    std::fs::write(a.join("cliproxyapi.json"), CONFIG).unwrap();
    std::fs::write(a.join("settings.json"), ORIGINAL).unwrap();
    set_up(&b, "b");
    std::fs::write(b.join("settings.json"), UNINSTALLED).unwrap();
    link_dir(&a, &shown);
    let vault = Mem::default();
    let preview =
        crate::preview(&vault, data.clone(), &serde_json::json!({"folder": shown})).unwrap();
    let connect = serde_json::json!({"targets": preview["targets"], "key": "secret-1",
        "base_url": "http://127.0.0.1:41999/", "model": "gpt-6.1-sol"});
    let mut secrets = serde_json::Map::new();
    secrets.insert("secret-1".into(), "k-test".into());
    (vault, data, base, connect, secrets)
}

/// While it lives, base/agent is pointed at base/b once a write's file transaction has ended.
struct Retargeted;

impl Drop for Retargeted {
    fn drop(&mut self) {
        crate::AFTER_FILES.with(|h| *h.borrow_mut() = None);
    }
}

/// Points base/agent at base/b once the write's file transaction has ended (as another program
/// could), until the returned guard is dropped.
fn retarget_after_files(base: &Path) -> Retargeted {
    let base = base.to_path_buf();
    crate::AFTER_FILES.with(|h| {
        *h.borrow_mut() = Some(Box::new(move || {
            unlink_dir(&base.join("agent"));
            link_dir(&base.join("b"), &base.join("agent"));
        }))
    });
    Retargeted
}

#[test]
fn disconnect_and_restore_remove_the_plugin_from_the_agent_folder_they_wrote() {
    let plugin = "pluxy-cliproxyapi-provider";
    // Connected (pluxy's copy installed in a), then Disconnect or Restore backup, with the agent
    // folder's link pointed at b once the files are written: the plugin goes from a, where the
    // files were written; b's copy, which its settings do not list, stays.
    for op in ["clear", "restore"] {
        let (vault, data, base, connect, secrets) = two_agents(&format!("tidy-{op}"));
        crate::apply(&vault, data.clone(), &connect, &secrets).unwrap();
        assert!(base.join("a").join(plugin).is_dir());
        let shown = base.join("agent");
        let preview =
            crate::preview(&vault, data.clone(), &serde_json::json!({"folder": shown})).unwrap();
        let b_before = tree(&base.join("b"));
        let input = serde_json::json!({"targets": preview["targets"]});
        let off = retarget_after_files(&base);
        let r = match op {
            "clear" => crate::clear(&vault, data.clone(), &input),
            _ => crate::restore(&vault, data.clone(), &input),
        };
        drop(off);
        assert_eq!(tree(&base.join("b")), b_before, "{op}: b changed");
        let v = r.unwrap_or_else(|e| panic!("{op}: {e}"));
        assert_eq!(v["adapter_removed"], true, "{op}");
        assert!(!base.join("a").join(plugin).exists(), "{op}");
        assert!(!text(std::fs::read(base.join("a/settings.json")).unwrap()).contains(plugin));
        assert!(vault.open().is_empty(), "{op}");
        std::fs::remove_dir_all(&base).unwrap();
        std::fs::remove_dir_all(&data).unwrap();
    }
}

#[test]
fn a_failed_connect_removes_the_copy_it_installed_and_nothing_else() {
    let plugin = "pluxy-cliproxyapi-provider";
    // Connect installs pluxy's copy in a, then its file write fails (settings.json changed since the
    // preview); the agent folder's link is pointed at b before the copy is taken away again: the
    // copy it installed goes from a, and b's copy stays.
    let (vault, data, base, connect, secrets) = two_agents("failed-connect");
    std::fs::write(base.join("a/settings.json"), EDITED).unwrap();
    let b_before = tree(&base.join("b"));
    let off = retarget_after_files(&base);
    let e = crate::apply(&vault, data.clone(), &connect, &secrets).unwrap_err();
    drop(off);
    assert_eq!(tree(&base.join("b")), b_before, "b changed");
    assert!(e.message.contains("preview it again"), "{}", e.message);
    assert!(!base.join("a").join(plugin).exists());
    assert_eq!(
        names(&base.join("a")),
        ["cliproxyapi.json", "settings.json"]
    );
    assert!(vault.open().is_empty());
    std::fs::remove_dir_all(&base).unwrap();
    std::fs::remove_dir_all(&data).unwrap();
}

/// A home folder where Pi never ran (no `~/.pi`), its data folder, and the home as a call is
/// given it.
fn fresh_home(name: &str) -> (PathBuf, PathBuf) {
    (temp(&format!("{name}-home")), temp(&format!("{name}-data")))
}

/// What the page sends to Connect around the preview it read (its targets, and create_folder when
/// it named one), with the key as the secret secret-1.
fn connect_input(
    preview: &serde_json::Value,
) -> (
    serde_json::Value,
    serde_json::Map<String, serde_json::Value>,
) {
    let mut input = serde_json::json!({"targets": preview["targets"], "key": "secret-1",
        "base_url": "http://127.0.0.1:41999/", "model": "gpt-6.1-sol"});
    if let Some(f) = preview.get("create_folder") {
        input["create_folder"] = f.clone();
    }
    let mut secrets = serde_json::Map::new();
    secrets.insert("secret-1".into(), "k-test".into());
    (input, secrets)
}

/// The mode a folder made with the system's defaults gets here (what Pi gives `~/.pi` and
/// `~/.pi/agent` when it makes them).
#[cfg(unix)]
fn default_mode(at: &Path) -> u32 {
    use std::os::unix::fs::PermissionsExt;
    let probe = at.join("probe");
    std::fs::create_dir(&probe).unwrap();
    let m = std::fs::metadata(&probe).unwrap().permissions().mode() & 0o777;
    std::fs::remove_dir(probe).unwrap();
    m
}

#[test]
fn a_home_where_pi_never_ran_reads_as_not_set_up_and_nothing_is_made() {
    let (home, data) = fresh_home("unmade-preview");
    let h = home.clone();
    let at = move || Ok(h.clone());
    let v = crate::preview_in(&Mem::default(), data.clone(), &serde_json::json!({}), &at).unwrap();
    let agent = home.join(".pi").join("agent");
    assert_eq!(v["pi"]["state"], "not-configured", "{v}");
    assert_eq!(v["pi"]["adapter"]["installed"], false);
    assert_eq!(v["pi"]["adapter"]["pin"], serde_json::Value::Null);
    assert_eq!(v["applied"], false);
    assert_eq!(v["create_folder"], serde_json::json!(agent));
    let real = std::fs::canonicalize(&home)
        .unwrap()
        .join(".pi")
        .join("agent");
    for (i, name) in crate::FILES.iter().enumerate() {
        assert_eq!(
            v["targets"][i],
            serde_json::json!({"selected": agent.join(name), "link": null,
                "target": real.join(name),
                "identity": {"link": null, "parent": null, "file": null}, "sha256": null})
        );
    }
    assert!(crate::is_unmade(
        &serde_json::json!({"targets": v["targets"]})
    ));
    assert!(!home.join(".pi").exists(), "a folder was made");
    // Once Pi made its agent folder, its files are read as before.
    std::fs::create_dir_all(&agent).unwrap();
    let v = crate::preview_in(&Mem::default(), data, &serde_json::json!({}), &at).unwrap();
    assert!(v.get("create_folder").is_none(), "{v}");
    assert!(!crate::is_unmade(
        &serde_json::json!({"targets": v["targets"]})
    ));
    std::fs::remove_dir_all(&home).unwrap();
}

#[test]
fn connect_makes_pis_agent_folder_as_pi_would_and_sets_pi_up_in_it() {
    // No ~/.pi at all, and ~/.pi without its agent folder.
    for pi_there in [false, true] {
        let (home, data) = fresh_home(&format!("unmade-connect-{pi_there}"));
        if pi_there {
            std::fs::create_dir(home.join(".pi")).unwrap();
        }
        let h = home.clone();
        let at = move || Ok(h.clone());
        let vault = Mem::default();
        let v = crate::preview_in(&vault, data.clone(), &serde_json::json!({}), &at).unwrap();
        let (input, secrets) = connect_input(&v);
        let out = crate::apply_in(&vault, data.clone(), &input, &secrets, &at).unwrap();
        assert_eq!(out["adapter"], "installed");
        let agent = home.join(".pi").join("agent");
        assert!(agent.join("pluxy-cliproxyapi-provider").is_dir());
        let settings = text(std::fs::read(agent.join("settings.json")).unwrap());
        assert!(
            settings.contains("pluxy-cliproxyapi-provider"),
            "{settings}"
        );
        let config = text(std::fs::read(agent.join("cliproxyapi.json")).unwrap());
        assert!(config.contains("http://127.0.0.1:41999"), "{config}");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = |p: &Path| std::fs::metadata(p).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode(&agent), default_mode(&home));
            assert_eq!(mode(&home.join(".pi")), default_mode(&home));
            assert_eq!(mode(&agent.join("cliproxyapi.json")), 0o600);
        }
        // Read again, it is connected, and its files are pinned as any others.
        let again = serde_json::json!({"base_url": "http://127.0.0.1:41999/"});
        let v = crate::preview_in(&vault, data.clone(), &again, &at).unwrap();
        assert_eq!(v["pi"]["state"], "configured", "{v}");
        assert!(v.get("create_folder").is_none());
        assert!(vault.all_committed());
        std::fs::remove_dir_all(&home).unwrap();
        std::fs::remove_dir_all(&data).unwrap();
    }
}

#[test]
fn connect_makes_only_pis_own_agent_folder_for_the_preview_it_names() {
    let (home, data) = fresh_home("unmade-refused");
    let h = home.clone();
    let at = move || Ok(h.clone());
    let v = crate::preview_in(&Mem::default(), data.clone(), &serde_json::json!({}), &at).unwrap();
    let (input, secrets) = connect_input(&v);
    let mut elsewhere = input.clone();
    elsewhere["create_folder"] = serde_json::json!(home.join("other"));
    let mut moved = input.clone();
    moved["targets"][1]["target"] = serde_json::json!(home.join("other").join("settings.json"));
    for bad in [elsewhere, moved] {
        let e = crate::apply_in(&Mem::default(), data.clone(), &bad, &secrets, &at).unwrap_err();
        assert!(
            e.message.contains("create_folder is Pi's agent folder"),
            "{e}"
        );
    }
    // Without create_folder the targets are no pins; a key the app did not deliver or a model
    // that is no model is refused before any folder is made.
    let mut plain = input.clone();
    plain.as_object_mut().unwrap().remove("create_folder");
    let mut no_key = input.clone();
    no_key["key"] = serde_json::json!("secret-2");
    let mut no_model = input.clone();
    no_model["model"] = serde_json::json!("");
    for bad in [plain, no_key, no_model] {
        assert!(crate::apply_in(&Mem::default(), data.clone(), &bad, &secrets, &at).is_err());
    }
    assert!(names(&home).is_empty(), "made {:?}", names(&home));
    // Pi ran meanwhile and made its agent folder: read again first; what Pi wrote stays.
    let agent = home.join(".pi").join("agent");
    std::fs::create_dir_all(&agent).unwrap();
    std::fs::write(agent.join("settings.json"), "{}\n").unwrap();
    let e = crate::apply_in(&Mem::default(), data.clone(), &input, &secrets, &at).unwrap_err();
    assert!(e.message.contains("read the settings again"), "{e}");
    assert_eq!(names(&agent), ["settings.json"]);
    std::fs::remove_dir_all(&home).unwrap();
}

#[test]
fn a_connect_that_fails_takes_away_the_folders_it_made() {
    let (home, data) = fresh_home("unmade-failed");
    // The data folder is a file: the write fails after the folders are made.
    let data = data.join("file");
    std::fs::write(&data, "").unwrap();
    let h = home.clone();
    let at = move || Ok(h.clone());
    let v = crate::preview_in(&Mem::default(), data.clone(), &serde_json::json!({}), &at).unwrap();
    let (input, secrets) = connect_input(&v);
    assert!(crate::apply_in(&Mem::default(), data, &input, &secrets, &at).is_err());
    assert!(!home.join(".pi").exists(), "{:?}", names(&home));
    std::fs::remove_dir_all(&home).unwrap();
}

#[test]
fn other_writes_say_in_words_that_the_agent_folder_does_not_exist_yet() {
    // The page maps "is not applied to these targets" to its own words (no copy from before).
    for op in ["clear", "restore"] {
        assert!(crate::unmade_refusal(op)
            .message
            .contains("is not applied to these targets"));
    }
    for op in ["update", "uninstall", "backup-create", "backup-restore"] {
        assert!(crate::unmade_refusal(op)
            .message
            .contains("Connect makes it"));
    }
}
