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
fn nothing_is_exported_so_no_other_package_previews_files_or_writes_the_settings() {
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
