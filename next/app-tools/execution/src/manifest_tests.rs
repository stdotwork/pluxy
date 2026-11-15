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
fn only_launch_is_exported_so_importers_never_reach_runtimes() {
    assert_eq!(
        manifest()["app"]["exports"],
        json!({"contract": 1, "operations": ["launch"]})
    );
}

#[test]
fn stopping_or_restarting_a_runtime_is_a_write_and_reading_them_is_not() {
    let runtime = operation("runtime");
    assert_eq!(runtime["effect"], "read");
    assert_eq!(
        runtime["input_schema"]["properties"]["op"]["enum"],
        json!(["status", "list"])
    );
    let control = operation("control");
    assert_eq!(control["effect"], "write");
    assert_eq!(
        control["input_schema"]["properties"]["op"]["enum"],
        json!(["stop", "restart"])
    );
    assert_eq!(
        control["input_schema"]["required"],
        json!(["op", "runtime"])
    );
}
