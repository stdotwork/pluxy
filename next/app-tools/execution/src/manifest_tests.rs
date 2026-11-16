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
fn only_prepare_and_launch_are_exported_so_importers_never_reach_runtimes() {
    assert_eq!(
        manifest()["app"]["exports"],
        json!({"contract": 1, "operations": ["prepare", "launch"]})
    );
    assert_eq!(operation("prepare")["effect"], "read");
}

#[test]
fn a_launch_may_name_the_file_it_opens_and_its_folder() {
    let props = &operation("launch")["input_schema"]["properties"];
    assert_eq!(props["file"]["type"], "string");
    assert_eq!(props["within"]["type"], "string");
    let prepare = operation("prepare");
    assert_eq!(
        prepare["input_schema"]["properties"]["kind"]["enum"],
        json!(["terminal", "direct", "open"])
    );
    assert_eq!(prepare["input_schema"]["required"], json!(["kind"]));
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

#[test]
fn only_the_window_alone_is_not_an_import() {
    assert!(!super::imported(&[json!({"kind": "main"})]));
    assert!(super::imported(&[
        json!({"kind": "main"}),
        json!({"kind": "package", "id": "pluxy/codex-sessions"})
    ]));
    assert!(super::imported(&[
        json!({"kind": "page", "id": "example/page"})
    ]));
    assert!(super::imported(&[]));
}
