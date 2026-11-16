//! What a launch may carry on Windows and macOS, before the host is asked (`fits`).

use super::fits;
use serde_json::json;

fn launch() -> serde_json::Value {
    json!({"executable": "/x/execution", "argv": ["terminal"], "cwd": "/"})
}

#[test]
fn a_terminal_launch_takes_no_standard_input() {
    let mut i = launch();
    assert!(fits(&i, true, false).is_ok());
    i["stdin"] = json!("key");
    let e = fits(&i, true, false).unwrap_err();
    assert_eq!(e.code, "invalid_request");
    assert!(e.message.contains("no standard input"), "{}", e.message);
    // Any other launch with input is the host's to judge.
    assert!(fits(&i, false, false).is_ok());
}

#[test]
fn an_app_launch_carries_nothing_but_the_app() {
    let mut bare = launch();
    bare["lifetime"] = json!("detached");
    bare["environment"] = json!({"set": {}, "unset": [], "inherit": [], "secrets": {}});
    assert!(fits(&bare, false, true).is_ok());
    for (k, v) in [
        ("environment", json!({"set": {"A": "1"}})),
        ("environment", json!({"inherit": ["HOME"]})),
        ("environment", json!({"unset": ["PATH"]})),
        ("environment", json!({"secrets": {"KEY": "key"}})),
        ("stdin", json!("key")),
        ("key", json!("ref-1")),
        ("lifetime", json!("managed")),
    ] {
        let mut i = bare.clone();
        i[k] = v.clone();
        let e = fits(&i, false, true).unwrap_err();
        assert_eq!(e.code, "invalid_request", "{k}: {v}");
        assert!(
            e.message.starts_with("an app launch takes no"),
            "{}",
            e.message
        );
        // The same fields on a launch that is not an app's are not refused here.
        assert!(fits(&i, false, false).is_ok(), "{k}: {v}");
    }
}
