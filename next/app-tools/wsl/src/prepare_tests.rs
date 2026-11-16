use super::*;
use serde_json::json;

fn input(v: Value) -> Request {
    Request::from_input(&v).unwrap()
}

fn refused(v: Value) -> Error {
    Request::from_input(&v).unwrap_err()
}

#[test]
fn a_request_names_a_bare_program_or_an_absolute_override() {
    let r = input(json!({"kind": "terminal", "program": "claude", "args": ["--x", ""]}));
    assert_eq!(r.program, "claude");
    assert_eq!(r.args, ["--x", ""]);
    let r = input(json!({"kind": "direct", "override": "/opt/c/bin/claude", "cwd": "/home/bob"}));
    assert_eq!(r.over, "/opt/c/bin/claude");
    for bad in [
        json!({"kind": "open", "program": "claude"}),
        json!({"kind": "terminal"}),
        json!({"kind": "terminal", "program": "bin/claude"}),
        json!({"kind": "terminal", "program": "-x"}),
        json!({"kind": "terminal", "override": "claude"}),
        json!({"kind": "terminal", "program": "claude", "cwd": "C:\\Users"}),
        json!({"kind": "terminal", "program": "claude", "args": ["a\u{0}b"]}),
        json!({"kind": "terminal", "program": "claude", "args": "a"}),
        json!({"kind": "terminal", "program": "cl\u{0}aude"}),
    ] {
        assert_eq!(refused(bad.clone()).code, "invalid_request", "{bad}");
    }
}

#[test]
fn the_launch_runs_the_program_found_in_the_folder_found_as_the_user() {
    let r = input(json!({"kind": "terminal", "program": "claude", "args": ["a b", "\"q\""]}));
    let v = r
        .launch(
            b"ok\0/usr/local/bin/claude\0/home/bob\0",
            "Ubuntu",
            "bob",
            "C:\\Windows\\System32\\wsl.exe",
            "C:\\Users\\me",
        )
        .unwrap();
    assert_eq!(
        v,
        json!({
            "launch": {
                "executable": "C:\\Windows\\System32\\wsl.exe",
                "argv": ["-d", "Ubuntu", "-u", "bob", "--cd", "/home/bob", "--exec",
                    "/usr/local/bin/claude", "a b", "\"q\""],
                "cwd": "C:\\Users\\me",
                "program": "/usr/local/bin/claude"
            },
            "kind": "terminal"
        })
    );
    // The launch is exactly pluxy/execution's launch input, which takes no other field: the host
    // passes it on as it is, and applies kind itself.
    assert_eq!(fits_execution_launch(&v["launch"]), Ok(()));
    // Negative controls on the same check: a field of another type, an item of another type, a
    // field execution does not take, a required one missing.
    let mut bad = v["launch"].clone();
    bad["argv"] = json!("-d Ubuntu");
    assert!(fits_execution_launch(&bad).unwrap_err().contains("argv"));
    let mut bad = v["launch"].clone();
    bad["argv"][0] = json!(1);
    assert!(fits_execution_launch(&bad)
        .unwrap_err()
        .contains("an item of argv"));
    let mut bad = v["launch"].clone();
    bad["kind"] = json!("terminal");
    assert!(fits_execution_launch(&bad)
        .unwrap_err()
        .contains("takes no kind"));
    let mut bad = v["launch"].clone();
    bad.as_object_mut().unwrap().remove("cwd");
    assert!(fits_execution_launch(&bad).unwrap_err().contains("cwd"));
    let a = r.resolve_args("Ubuntu", "bob");
    assert_eq!(
        a[..9],
        ["-d", "Ubuntu", "-u", "bob", "--exec", "/bin/sh", "-c", PREPARE, "sh"]
    );
    assert_eq!(a[9..], ["claude", "", ""]);
}

#[test]
fn what_was_not_found_is_said() {
    let r = input(json!({"kind": "direct", "program": "claude"}));
    let e = |out: &[u8]| {
        r.launch(out, "Ubuntu", "bob", "wsl.exe", "C:\\")
            .unwrap_err()
    };
    assert!(e(b"program\0").message.contains("not on the PATH"));
    assert!(e(b"cwd\0").message.contains("working folder"));
    assert_eq!(e(b"").code, "unavailable");
    assert_eq!(e(b"ok\0relative\0/home\0").code, "unavailable");
    assert_eq!(e(b"ok\0/a\0/b\0extra\0").code, "unavailable");
}

/// A new folder for one test.
#[cfg(unix)]
fn folder(name: &str) -> std::path::PathBuf {
    let d = std::env::temp_dir().join(format!("pluxy-wsl-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    d
}

/// Runs the fixed script as `wsl.exe --exec /bin/sh -c` would, with this PATH and HOME.
#[cfg(unix)]
fn script(path: &std::path::Path, home: &std::path::Path, r: &Request) -> Vec<u8> {
    let out = std::process::Command::new("/bin/sh")
        .args(["-c", PREPARE, "sh", &r.program, &r.over, &r.cwd])
        .env_clear()
        .env("PATH", path)
        .env("HOME", home)
        .output()
        .unwrap();
    assert!(out.status.success(), "{out:?}");
    out.stdout
}

#[cfg(unix)]
#[test]
fn the_script_finds_programs_on_the_path_and_checks_overrides_and_folders() {
    use std::os::unix::fs::PermissionsExt;
    let d = folder("script");
    let (bin, home) = (d.join("bin dir"), d.join("home"));
    std::fs::create_dir_all(&bin).unwrap();
    std::fs::create_dir_all(home.join("work space")).unwrap();
    let exe = bin.join("claude");
    std::fs::write(&exe, "#!/bin/sh\n").unwrap();
    std::fs::set_permissions(&exe, std::fs::Permissions::from_mode(0o755)).unwrap();
    let plain = bin.join("notexec");
    std::fs::write(&plain, "x").unwrap();
    let s = |p: &std::path::Path| p.to_str().unwrap().to_string();
    let launch = |r: &Request| r.launch(&script(&bin, &home, r), "U", "b", "wsl.exe", "C:\\");

    // Found on the PATH; the folder is the home when none is named.
    let r = input(json!({"kind": "terminal", "program": "claude"}));
    let v = launch(&r).unwrap();
    assert_eq!(v["launch"]["program"], s(&exe));
    assert_eq!(v["launch"]["argv"][5], s(&home));
    // A named folder with a space.
    let cwd = s(&home.join("work space"));
    let r = input(json!({"kind": "terminal", "program": "claude", "cwd": cwd}));
    assert_eq!(launch(&r).unwrap()["launch"]["argv"][5], cwd);
    // Not on the PATH, not executable, a shell builtin.
    for p in ["missing", "notexec", "echo"] {
        let r = input(json!({"kind": "terminal", "program": p}));
        assert!(
            launch(&r).unwrap_err().message.contains("not on the PATH"),
            "{p}"
        );
    }
    // An override must be an executable file.
    let r = input(json!({"kind": "direct", "override": s(&exe)}));
    assert_eq!(launch(&r).unwrap()["launch"]["program"], s(&exe));
    for o in [s(&plain), s(&bin), s(&d.join("nope"))] {
        let r = input(json!({"kind": "direct", "override": o}));
        assert!(
            launch(&r)
                .unwrap_err()
                .message
                .contains("not an executable file"),
            "{o}"
        );
    }
    // A folder that is missing, or a file.
    for c in [s(&d.join("nope")), s(&exe)] {
        let r = input(json!({"kind": "direct", "program": "claude", "cwd": c}));
        assert!(
            launch(&r).unwrap_err().message.contains("working folder"),
            "{c}"
        );
    }
    std::fs::remove_dir_all(&d).unwrap();
}

/// Whether launch fits pluxy/execution's launch input schema: only fields it takes, each of its
/// type (and an array's items of theirs), every required one present.
fn fits_execution_launch(launch: &Value) -> Result<(), String> {
    let execution: Value =
        serde_json::from_str(include_str!("../../execution/pluxy-extension.json")).unwrap();
    let schema = &execution["app"]["operations"]
        .as_array()
        .unwrap()
        .iter()
        .find(|o| o["id"] == "launch")
        .unwrap()["input_schema"];
    assert_eq!(schema["additionalProperties"], false);
    let type_of = |v: &Value| match v {
        Value::String(_) => "string",
        Value::Array(_) => "array",
        Value::Object(_) => "object",
        Value::Number(_) => "number",
        Value::Bool(_) => "boolean",
        Value::Null => "null",
    };
    for (k, field) in launch.as_object().unwrap() {
        let want = &schema["properties"][k];
        if !want.is_object() {
            return Err(format!("execution's launch takes no {k}"));
        }
        if want["type"] != type_of(field) {
            return Err(format!("{k} is {}, not {}", type_of(field), want["type"]));
        }
        for item in field.as_array().into_iter().flatten() {
            if want["items"]["type"] != type_of(item) {
                return Err(format!("an item of {k} is {}", type_of(item)));
            }
        }
    }
    for k in schema["required"].as_array().unwrap() {
        if launch.get(k.as_str().unwrap()).is_none() {
            return Err(format!("{k} is missing"));
        }
    }
    Ok(())
}
