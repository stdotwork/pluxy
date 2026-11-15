use crate::{config_of, desktop_in, pinned_read};
use pluxy_program::Error;
use pluxy_transactions::sha256_of;
use serde_json::{json, Value};
use std::cell::Cell;

fn tmp() -> std::path::PathBuf {
    let d = std::env::temp_dir().join(format!(
        "opencode-main-test-{}-{:?}",
        std::process::id(),
        std::thread::current().id()
    ));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    d
}

/// A preview whose pin says sha256 of pinned, and then the bytes read.
fn read_of(pinned: &[u8], read: &[u8]) -> (Value, Option<Vec<u8>>) {
    (
        json!({"targets": [{"sha256": sha256_of(Some(pinned))}]}),
        Some(read.to_vec()),
    )
}

#[test]
fn the_config_read_back_is_the_pinned_files_or_the_read_is_refused() {
    let n = Cell::new(0);
    let (_, bytes) = pinned_read(|| {
        n.set(n.get() + 1);
        Ok::<_, Error>(if n.get() == 1 {
            read_of(b"{}", b"{\"x\": 1}")
        } else {
            read_of(b"{\"x\": 1}", b"{\"x\": 1}")
        })
    })
    .unwrap();
    assert_eq!((n.get(), bytes.unwrap()), (2, b"{\"x\": 1}".to_vec()));
    let e = pinned_read(|| Ok(read_of(b"{}", b"[]"))).unwrap_err();
    assert!(e.message.contains("changed while it was read"), "{e}");
    let (v, b) = pinned_read(|| Ok((json!({"targets": [{"sha256": null}]}), None))).unwrap();
    assert_eq!((v["targets"][0]["sha256"].clone(), b), (Value::Null, None));
}

#[test]
fn configs_opencode_would_not_take_are_reported_not_failed() {
    let v = config_of(
        Some(b"{\"$schema\": \"https://example.invalid/x.json\"}"),
        None,
    );
    assert_eq!(v["state"], "refused");
    assert!(
        v["reason"].as_str().unwrap().contains("another schema"),
        "{v}"
    );
    let v = config_of(Some(b"{'theme': 'x'}"), None);
    assert_eq!(v["state"], "refused");
    assert!(v["reason"].as_str().unwrap().contains("JSONC"), "{v}");
    assert_eq!(config_of(None, None)["state"], "not-configured");
}

#[test]
fn the_desktop_app_is_the_2x_executable_else_the_1x_one() {
    let d = tmp();
    assert_eq!(desktop_in(&d), d.join("ai.opencode"));
    std::fs::write(d.join("ai.opencode.desktop"), "").unwrap();
    assert_eq!(desktop_in(&d), d.join("ai.opencode.desktop"));
    std::fs::write(d.join("ai.opencode"), "").unwrap();
    assert_eq!(desktop_in(&d), d.join("ai.opencode"));
    std::fs::remove_dir_all(d).unwrap();
}
