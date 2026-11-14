use crate::{default_selection, pinned_read, settings_of};
use pluxy_program::Error;
use pluxy_transactions::{sha256_of, Selection};
use serde_json::{json, Value};
use std::cell::Cell;

fn tmp() -> std::path::PathBuf {
    let d = std::env::temp_dir().join(format!(
        "claude-code-test-{}-{:?}",
        std::process::id(),
        std::thread::current().id()
    ));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    d
}

#[test]
fn the_default_file_needs_claude_codes_folder_and_none_is_made() {
    let home = tmp();
    let e = default_selection(Some(home.clone().into())).unwrap_err();
    assert!(e.message.contains("run Claude Code once"), "{e}");
    assert!(!home.join(".claude").exists(), "a folder was made");
    std::fs::create_dir(home.join(".claude")).unwrap();
    assert_eq!(
        default_selection(Some(home.clone().into())).unwrap(),
        Selection::File(home.join(".claude").join("settings.json"))
    );
    assert!(default_selection(None).is_err());
    std::fs::remove_dir_all(home).unwrap();
}

/// A preview whose pin says sha256 of pinned, and then the bytes read.
fn read_of(pinned: &[u8], read: &[u8]) -> (Value, Option<Vec<u8>>) {
    (
        json!({"targets": [{"sha256": sha256_of(Some(pinned))}]}),
        Some(read.to_vec()),
    )
}

#[test]
fn settings_read_back_are_the_pinned_files_or_the_read_is_refused() {
    // The file changed between the pin and the read once: read again, and the second read holds.
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
    // Changed again: refused.
    let e = pinned_read(|| Ok(read_of(b"{}", b"[]"))).unwrap_err();
    assert!(e.message.contains("changed while it was read"), "{e}");
    // No file on either side is the same file.
    let (v, b) = pinned_read(|| Ok((json!({"targets": [{"sha256": null}]}), None))).unwrap();
    assert_eq!((v["targets"][0]["sha256"].clone(), b), (Value::Null, None));
}

#[test]
fn settings_claude_code_would_not_take_are_reported_not_failed() {
    let v = settings_of(
        Some(b"{\"$schema\": \"https://example.invalid/x.json\"}"),
        None,
    );
    assert_eq!(v["state"], "refused");
    assert!(
        v["reason"].as_str().unwrap().contains("another schema"),
        "{v}"
    );
    let v = settings_of(Some(b"{\n  // a comment\n}"), None);
    assert_eq!(v["state"], "refused");
    assert!(v["reason"].as_str().unwrap().contains("not JSON"), "{v}");
    assert_eq!(settings_of(None, None)["state"], "not-configured");
}
