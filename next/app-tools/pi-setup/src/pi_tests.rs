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
fn tree(dir: &Path) -> Vec<(String, Vec<u8>)> {
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

/// The pin of pluxy's copy in agent, as a preview takes it.
fn pinned(agent: &Path) -> pluxy_transactions::Pin {
    pluxy_transactions::Target::resolve(&crate::plugin_selection(agent))
        .unwrap()
        .pin
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
    let pin = pinned(&agent);
    adapter::update(&agent, &pin).unwrap();
    assert_eq!(tree(&dir), want);
    let names: Vec<String> = std::fs::read_dir(&agent)
        .unwrap()
        .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
        .collect();
    assert_eq!(names, ["pluxy-cliproxyapi-provider"]);

    assert!(adapter::remove(&agent, None).unwrap());
    assert!(!dir.exists());
    assert!(!adapter::remove(&agent, None).unwrap());
    // Nothing there: nothing to update.
    assert!(adapter::update(&agent, &pin).is_err());

    // Someone else's folder of that name: refused, never removed.
    std::fs::create_dir(&dir).unwrap();
    std::fs::write(dir.join("package.json"), "{\"name\":\"someone-else\"}\n").unwrap();
    assert_eq!(adapter::state(&agent), State::Foreign);
    assert!(adapter::ensure(&agent)
        .unwrap_err()
        .message
        .contains("not pluxy's"));
    assert!(adapter::update(&agent, &pin).is_err());
    assert!(!adapter::remove(&agent, None).unwrap());
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
    let sels = crate::selections(&input).unwrap();
    assert_eq!(
        crate::agent_of(&[&sels[0], &sels[1], &sels[2]]).unwrap(),
        abs("s")
    );
    assert!(crate::selections(&json!({"targets": [file("settings.json")]})).is_err());
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
    adapter::ensure(&agent).unwrap();
    assert!(adapter::remove(&agent, None).unwrap());
    assert_eq!(std::fs::read_dir(&agent).unwrap().count(), 0);
    // A hidden sibling left by a stop part way through is not the plugin, nor someone else's.
    std::fs::create_dir(agent.join(".pluxy-cliproxyapi-provider.old-1-2")).unwrap();
    assert_eq!(adapter::state(&agent), State::Absent);
    std::fs::remove_dir_all(&agent).unwrap();
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
    journals: std::cell::RefCell<std::collections::BTreeMap<String, Option<String>>>,
    next: std::cell::Cell<u64>,
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
                let id = id("j");
                self.journals.borrow_mut().insert(id.clone(), None);
                Ok(json!({"journal": id}))
            }
            "journal.commit" | "journal.end" => {
                let outcome = if op == "journal.commit" {
                    "committed".to_string()
                } else {
                    s("outcome")
                };
                self.journals
                    .borrow_mut()
                    .insert(s("journal"), Some(outcome.clone()));
                Ok(json!({"journal": s("journal"), "outcome": outcome}))
            }
            _ => Err(Error::unsupported(op.to_string())),
        }
    }
}

/// A symbolic link at at to the folder target.
fn link_dir(target: &Path, at: &Path) {
    #[cfg(unix)]
    std::os::unix::fs::symlink(target, at).unwrap();
    #[cfg(windows)]
    std::os::windows::fs::symlink_dir(target, at).unwrap();
}

/// Whether this account may make symbolic links (on Windows that takes a privilege or Developer
/// Mode); when it may not, says so on stderr and the test that needs them is skipped.
fn links_allowed() -> bool {
    #[cfg(unix)]
    return true;
    #[cfg(windows)]
    {
        let probe = std::env::temp_dir().join(format!("pi-setup-links-{}", std::process::id()));
        let made = std::os::windows::fs::symlink_dir("target", &probe).is_ok();
        let _ = std::fs::remove_dir(&probe);
        if !made {
            eprintln!("skipped: this account cannot make a symbolic link");
        }
        made
    }
}

/// An agent folder at dir set up by pluxy, with an older copy of the plugin (so the page offers
/// Update plugin) holding a file that says whose it is.
fn set_up(dir: &Path, whose: &str) {
    std::fs::create_dir_all(dir).unwrap();
    std::fs::write(dir.join("cliproxyapi.json"), CONFIG_APPLIED).unwrap();
    std::fs::write(dir.join("settings.json"), APPLIED).unwrap();
    adapter::ensure(dir).unwrap();
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

/// Every file under a folder, by relative path, with its bytes (`tree`).
type Tree = Vec<(String, Vec<u8>)>;

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
            #[cfg(unix)]
            std::fs::remove_file(&shown).unwrap();
            #[cfg(windows)]
            std::fs::remove_dir(&shown).unwrap();
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
        "update" => crate::update(&sent(&preview)),
        _ => crate::uninstall(&vault, data.clone(), &sent(&preview)),
    };
    let after = tree(&base);
    std::fs::remove_dir_all(&data).unwrap();
    (r, before, after, base)
}

#[test]
fn update_and_uninstall_refuse_a_folder_swapped_since_the_preview() {
    if !links_allowed() {
        return;
    }
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
    if !links_allowed() {
        return;
    }
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
    adapter::ensure(&agent).unwrap();
    let before = tree(&agent);
    let e = crate::uninstall(&vault, data.clone(), &sent(&preview)).unwrap_err();
    assert!(e.message.contains("preview it again"), "{}", e.message);
    assert!(tree(&agent) == before);
    std::fs::remove_dir_all(&agent).unwrap();
    std::fs::remove_dir_all(&data).unwrap();
}

#[test]
fn a_folder_swapped_after_the_check_is_put_back_not_acted_on() {
    if !links_allowed() {
        return;
    }
    let plugin = "pluxy-cliproxyapi-provider";
    // The plugin's folder replaced, or the agent folder's link pointed elsewhere, after the
    // operation checked the pin and before it moved the folder: the move is refused, the folder
    // there goes back under its name, and nothing is written or removed.
    for swap in [Swap::Plugin, Swap::Link] {
        let base = temp(&format!("late-{swap:?}"));
        let (a, b, shown) = (base.join("a"), base.join("b"), base.join("agent"));
        set_up(&a, "a");
        set_up(&b, "b");
        link_dir(&a, &shown);
        let pin = pinned(&shown);
        match swap {
            Swap::Plugin => {
                std::fs::rename(a.join(plugin), base.join("moved")).unwrap();
                std::fs::rename(b.join(plugin), a.join(plugin)).unwrap();
            }
            _ => {
                #[cfg(unix)]
                std::fs::remove_file(&shown).unwrap();
                #[cfg(windows)]
                std::fs::remove_dir(&shown).unwrap();
                link_dir(&b, &shown);
            }
        }
        let before = tree(&base);
        let e = adapter::update(&shown, &pin).unwrap_err();
        assert!(
            e.message.contains("preview it again"),
            "{swap:?}: {}",
            e.message
        );
        assert!(tree(&base) == before, "update after {swap:?}");
        let e = adapter::remove(&shown, Some(&pin)).unwrap_err();
        assert!(
            e.message.contains("preview it again"),
            "{swap:?}: {}",
            e.message
        );
        assert!(tree(&base) == before, "remove after {swap:?}");
        std::fs::remove_dir_all(&base).unwrap();
    }
}
