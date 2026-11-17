use super::*;
use serde_json::json;

fn check_output() -> Value {
    json!({"distro": "Ubuntu", "user": "bob", "uid": 1001, "home": "/home/bob", "arch": "amd64",
        "wsl": 2, "automount": "/mnt/",
        "drives": [{"letter": "c", "path": "/mnt/c", "exec": true},
                   {"letter": "d", "path": "/mnt/d", "exec": false}]})
}

fn record() -> Record {
    Record::from_check("Ubuntu", "bob", &check_output(), 7).unwrap()
}

#[test]
fn the_window_names_this_computer_or_exactly_a_wsl_distribution_and_user() {
    assert_eq!(Environment::from_window(None).unwrap(), Environment::Local);
    assert_eq!(
        Environment::from_window(Some(&Value::Null)).unwrap(),
        Environment::Local
    );
    let wsl = json!({"wsl": {"distro": "Ubuntu", "user": "bob"}});
    assert_eq!(
        Environment::from_window(Some(&wsl)).unwrap(),
        Environment::Wsl {
            distro: "Ubuntu".into(),
            user: "bob".into()
        }
    );
    for bad in [
        json!("wsl"),
        json!({"wsl": {"distro": "Ubuntu"}}),
        json!({"wsl": {"distro": "Ubuntu", "user": "bob", "home": "/root"}}),
        json!({"wsl": {"distro": "Ubuntu", "user": "bob"}, "local": true}),
        json!({"local": {"distro": "Ubuntu", "user": "bob"}}),
        json!({"wsl": {"distro": "-d", "user": "bob"}}),
        json!({"wsl": {"distro": "Ubuntu", "user": "Bob"}}),
        json!({"wsl": {"distro": "Ub:untu", "user": "bob"}}),
        json!({"wsl": {"distro": 1, "user": "bob"}}),
    ] {
        assert!(Environment::from_window(Some(&bad)).is_err(), "{bad}");
    }
    assert_eq!(
        Environment::from_window(Some(&wsl)).unwrap().to_window(),
        wsl
    );
}

#[test]
fn an_instance_in_wsl_has_its_own_key_and_vault_folder_suffix() {
    let local = Instance::local("pluxy/claude-code");
    assert_eq!(local.key(), "pluxy/claude-code");
    assert_eq!(local.vault_suffix(), "");
    let bob = Instance {
        package: "pluxy/claude-code".into(),
        environment: Environment::wsl("Ubuntu", "bob").unwrap(),
    };
    let alice = Instance {
        package: "pluxy/claude-code".into(),
        environment: Environment::wsl("Ubuntu", "alice").unwrap(),
    };
    // `@` and the first 32 hex digits of SHA-256 of `wsl\0Ubuntu\0bob`.
    let d = Sha256::digest(b"wsl\0Ubuntu\0bob");
    let hex: String = d.iter().map(|b| format!("{b:02x}")).collect();
    assert_eq!(bob.vault_suffix(), format!("@{}", &hex[..32]));
    assert_eq!(bob.key(), format!("pluxy/claude-code@{}", &hex[..32]));
    assert_ne!(bob.key(), alice.key());
    assert!(!bob.vault_suffix().contains("Ubuntu") && !bob.vault_suffix().contains("bob"));
    assert_eq!(bob.name(), "pluxy/claude-code in WSL Ubuntu/bob");
}

#[test]
fn a_record_is_what_check_gave_for_that_distribution_and_user() {
    let r = record();
    assert_eq!(
        (r.distro.as_str(), r.user.as_str(), r.uid, r.home.as_str()),
        ("Ubuntu", "bob", 1001, "/home/bob")
    );
    assert_eq!(r.platform(), "linux/amd64");
    assert_eq!(r.wsl, Some(2));
    assert_eq!(r.checked_at, 7);
    assert_eq!(
        r.confirmation_line(),
        r#"Runs in WSL: distribution "Ubuntu", user "bob" (/home/bob)."#
    );
    // The names are quoted: a distribution named like the rest of a line (a name holds no `"`)
    // reads as one name.
    let mut o = check_output();
    o["distro"] = json!("Ubuntu, user root (root)");
    let odd = Record::from_check("Ubuntu, user root (root)", "bob", &o, 0).unwrap();
    assert_eq!(
        odd.confirmation_line(),
        r#"Runs in WSL: distribution "Ubuntu, user root (root)", user "bob" (/home/bob)."#
    );
    let with = |k: &str, v: Value| {
        let mut o = check_output();
        o[k] = v;
        Record::from_check("Ubuntu", "bob", &o, 0)
    };
    assert!(with("user", json!("alice")).is_err());
    assert!(with("distro", json!("Other")).is_err());
    assert!(with("home", json!("home/bob")).is_err());
    assert!(with("arch", json!("riscv64")).is_err());
    assert!(with("automount", json!("mnt/")).is_err());
    assert!(with(
        "drives",
        json!([{"letter": "cd", "path": "/mnt/c", "exec": true}])
    )
    .is_err());
    assert!(with(
        "drives",
        json!([{"letter": "c", "path": "mnt/c", "exec": true}])
    )
    .is_err());
    assert!(with("automount", Value::Null).is_ok());
    assert!(Record::from_check("-d", "bob", &check_output(), 0).is_err());
}

#[test]
fn a_tool_runs_from_the_windows_disk_through_the_drive_wsl_mounts() {
    let r = record();
    assert_eq!(
        r.linux_path(Path::new(
            r"C:\Users\Bob Smith\AppData\Local\pluxy\store\ab\bin\claude-code"
        ))
        .unwrap(),
        "/mnt/c/Users/Bob Smith/AppData/Local/pluxy/store/ab/bin/claude-code"
    );
    assert_eq!(
        r.linux_path(Path::new(r"\\?\c:\x\y")).unwrap(),
        "/mnt/c/x/y"
    );
    let noexec = r.linux_path(Path::new(r"D:\x")).unwrap_err();
    assert!(noexec.contains("noexec"), "{noexec}");
    let missing = r.linux_path(Path::new(r"E:\x")).unwrap_err();
    assert!(
        missing.contains("does not mount Windows drive E:"),
        "{missing}"
    );
    for bad in [
        r"\\wsl.localhost\Ubuntu\home\bob",
        r"\\server\share\x",
        r"C:x",
        r"C:\a\..\b",
        "/mnt/c/x",
    ] {
        assert!(r.linux_path(Path::new(bad)).is_err(), "{bad}");
    }
}

#[test]
fn a_tool_in_wsl_gets_the_contract_through_wslenv_and_nothing_else() {
    let r = record();
    assert_eq!(
        r.tool_args("/mnt/c/s/bin/claude-code", "/mnt/c/s"),
        [
            "-d",
            "Ubuntu",
            "-u",
            "bob",
            "--cd",
            "/mnt/c/s",
            "--exec",
            "/mnt/c/s/bin/claude-code"
        ]
    );
    let env = r
        .tool_env(
            "pluxy/claude-code",
            &"cd".repeat(32),
            "0",
            Some(Path::new(r"C:\Windows")),
        )
        .unwrap();
    let names: Vec<&str> = env.iter().map(|(k, _)| k.as_str()).collect();
    assert_eq!(
        names,
        [
            "SystemRoot",
            "PLUXY_APP_CONTRACT_VERSIONS",
            "PLUXY_EXTENSION_ID",
            "PLUXY_APP_ARTIFACT_SHA256",
            "PLUXY_TOOL_DATA_DIR",
            "PLUXY_ENVIRONMENT",
            "PLUXY_ENVIRONMENT_ACCOUNT",
            "WSLENV"
        ]
    );
    let get = |k: &str| env.iter().find(|(n, _)| n == k).unwrap().1.clone();
    assert_eq!(get("SystemRoot"), r"C:\Windows");
    assert_eq!(get("PLUXY_EXTENSION_ID"), "pluxy/claude-code");
    assert_eq!(
        get("PLUXY_TOOL_DATA_DIR"),
        "/home/bob/.local/state/pluxy/tools/pluxy/claude-code"
    );
    assert_eq!(get("PLUXY_ENVIRONMENT"), "wsl:Ubuntu:bob");
    // The record's user number and home: the tool checks them too, so a user made again under
    // the name, or a home moved, runs nothing.
    assert_eq!(get("PLUXY_ENVIRONMENT_ACCOUNT"), "1001:/home/bob");
    assert_eq!(
        get("WSLENV"),
        "PLUXY_APP_CONTRACT_VERSIONS/u:PLUXY_EXTENSION_ID/u:PLUXY_APP_ARTIFACT_SHA256/u:\
         PLUXY_TOOL_DATA_DIR/u:PLUXY_ENVIRONMENT/u:PLUXY_ENVIRONMENT_ACCOUNT/u"
    );
    assert!(r.tool_env("claude-code", "x", "0", None).is_err());
}

#[test]
fn records_are_kept_in_their_file_one_per_distribution_and_user() {
    let dir = std::env::temp_dir().join(format!("pluxy-env-{}", crate::managed::random_key()));
    let file = dir.join(FILE);
    let mut e = Environments::open(file.clone());
    assert!(e.all().is_empty());
    e.put(record()).unwrap();
    let mut again = record();
    again.home = "/home/bob2".into();
    e.put(again).unwrap();
    let mut alice = record();
    alice.user = "alice".into();
    e.put(alice).unwrap();
    let e = Environments::open(file.clone());
    assert_eq!(e.all().len(), 2);
    let bob = Environment::wsl("Ubuntu", "bob").unwrap();
    assert_eq!(e.get(&bob).unwrap().home, "/home/bob2");
    assert!(e.get(&Environment::Local).is_none());
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = fs::metadata(&file).unwrap().permissions().mode();
        assert_eq!(mode & 0o077, 0, "{mode:o}");
    }
    let mut e = e;
    assert!(e.remove(&bob).unwrap());
    assert!(!e.remove(&bob).unwrap());
    assert_eq!(Environments::open(file.clone()).all().len(), 1);
    fs::write(&file, b"{not json").unwrap();
    assert!(Environments::open(file).all().is_empty());
    let _ = fs::remove_dir_all(dir);
}

#[test]
fn what_wsl_exe_printed_instead_of_a_frame_reads_as_text() {
    let utf16: Vec<u8> = "Error: 0xd0000225\r\nError code: Wsl/Service/0xd0000225\r\n"
        .encode_utf16()
        .flat_map(|u| u.to_le_bytes())
        .collect();
    let said = wsl_said(&utf16);
    assert_eq!(said, "Error: 0xd0000225 Error code: Wsl/Service/0xd0000225");
    assert!(wsl_refused_start(&said));
    assert_eq!(
        wsl_said(b"There is no distribution with the supplied name.\n"),
        "There is no distribution with the supplied name."
    );
    assert!(!wsl_refused_start("User not found."));
    assert_eq!(wsl_said(&[b'x'; 2000]).chars().count(), 512);
}

/// What `pluxy/wsl`'s `check` answers for bob in Ubuntu: its own test checks that `check` gives
/// exactly this file, and the record made of it keeps every field.
#[test]
fn a_record_reads_every_field_pluxy_wsl_check_answers() {
    let answer: Value =
        serde_json::from_str(include_str!("../../../app-tools/wsl/testdata/check.json")).unwrap();
    assert_eq!(
        Record::from_check("Ubuntu", "bob", &answer, 7).unwrap(),
        Record {
            distro: "Ubuntu".into(),
            user: "bob".into(),
            uid: 1001,
            home: "/home/bob".into(),
            arch: "amd64".into(),
            wsl: Some(2),
            automount: Some("/mnt/".into()),
            drives: vec![
                Drive {
                    letter: "c".into(),
                    path: "/mnt/c".into(),
                    exec: true
                },
                Drive {
                    letter: "d".into(),
                    path: "/mnt/d".into(),
                    exec: false
                },
            ],
            checked_at: 7,
        }
    );
}
