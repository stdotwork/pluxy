use pluxy_transactions::pin_schema;
use serde_json::{json, Value};

fn manifest() -> Value {
    serde_json::from_str(include_str!("../pluxy-extension.json")).unwrap()
}

fn operation(id: &str) -> Value {
    manifest()["app"]["operations"]
        .as_array()
        .unwrap()
        .iter()
        .find(|o| o["id"] == id)
        .cloned()
        .unwrap_or_else(|| panic!("no operation {id}"))
}

#[test]
fn nothing_is_exported_so_no_other_package_previews_files_or_writes_the_config() {
    assert!(manifest()["app"].get("exports").is_none());
}

#[test]
fn only_apply_receives_the_key_and_writes_ask_each_time() {
    for o in manifest()["app"]["operations"].as_array().unwrap() {
        let secrets = o.get("secrets").cloned().unwrap_or(Value::Null);
        if o["id"] == "apply" {
            assert_eq!(secrets, json!({"key": "value"}));
        } else {
            assert!(secrets.is_null(), "{} takes a secret", o["id"]);
        }
    }
    for id in ["apply", "clear", "restore"] {
        let o = operation(id);
        assert_eq!(o["effect"], "write");
        // The confirmation shows the description: it says what a same-instant edit risks.
        assert!(o["description"].as_str().unwrap().ends_with(
            "Saving replaces the file; an edit made in the same instant by another program may be lost."
        ));
    }
    for id in ["preview", "status", "recipe"] {
        assert_eq!(operation(id)["effect"], "read");
    }
}

#[test]
fn writes_take_exactly_one_strict_pin() {
    for id in ["apply", "clear", "restore"] {
        let targets = &operation(id)["input_schema"]["properties"]["targets"];
        assert_eq!(targets["items"], pin_schema(), "{id}");
        assert_eq!(
            (targets["minItems"].clone(), targets["maxItems"].clone()),
            (json!(1), json!(1))
        );
        assert_eq!(operation(id)["input_schema"]["additionalProperties"], false);
    }
}

#[test]
fn the_backup_operations_are_the_librarys_standard_ones() {
    let ops = manifest()["app"]["operations"].as_array().unwrap().clone();
    let backups: Vec<Value> = ops
        .into_iter()
        .filter(|o| o["id"].as_str().unwrap().starts_with("backup-"))
        .collect();
    assert_eq!(backups, pluxy_transactions::backup_operations(1));
}

#[test]
fn the_apply_schema_matches_what_the_tool_accepts() {
    let i = &operation("apply")["input_schema"];
    assert_eq!(
        i["properties"]["models"]["maxItems"],
        json!(crate::MODELS_MAX)
    );
    assert_eq!(
        i["properties"]["models"]["items"]["additionalProperties"],
        false
    );
}

#[test]
fn the_recipe_starts_the_installed_grok_or_the_one_on_path() {
    let want = |program: &str| json!({"client": "Grok Build", "kind": "terminal", "program": program, "args": []});
    for os in ["linux", "macos", "windows"] {
        assert_eq!(crate::recipe(os, None), want("grok"), "{os}");
    }
    let home = std::env::temp_dir().join(format!("grok-setup-recipe-{}", std::process::id()));
    let bin = home.join(".grok").join("bin");
    std::fs::create_dir_all(&bin).unwrap();
    assert_eq!(crate::recipe("linux", Some(&home)), want("grok"));
    // install.sh's grok, and install.ps1's grok.exe on Windows.
    let (grok, exe) = (bin.join("grok"), bin.join("grok.exe"));
    std::fs::write(&grok, "#!/bin/sh\n").unwrap();
    std::fs::write(&exe, "MZ").unwrap();
    #[cfg(unix)]
    {
        // Not executable: not a program to start.
        assert_eq!(crate::recipe("linux", Some(&home)), want("grok"));
        use std::os::unix::fs::PermissionsExt;
        for p in [&grok, &exe] {
            std::fs::set_permissions(p, std::fs::Permissions::from_mode(0o755)).unwrap();
        }
    }
    // A file without an extension is no program on Windows.
    let unix = if cfg!(unix) {
        grok.to_str().unwrap()
    } else {
        "grok"
    };
    assert_eq!(crate::recipe("linux", Some(&home)), want(unix));
    assert_eq!(crate::recipe("macos", Some(&home)), want(unix));
    assert_eq!(
        crate::recipe("windows", Some(&home)),
        want(exe.to_str().unwrap())
    );
    std::fs::remove_dir_all(&home).unwrap();
}
