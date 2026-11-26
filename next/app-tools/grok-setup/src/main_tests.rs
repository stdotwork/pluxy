use crate::{pinned_read, settings_of};
use pluxy_program::Error;
use pluxy_transactions::sha256_of;
use serde_json::{json, Value};
use std::cell::Cell;

/// A preview whose pin says sha256 of pinned, and then the bytes read.
fn read_of(pinned: &[u8], read: &[u8]) -> (Value, Option<Vec<u8>>) {
    (
        json!({"targets": [{"sha256": sha256_of(Some(pinned))}]}),
        Some(read.to_vec()),
    )
}

#[test]
fn the_config_read_back_is_the_pinned_file_or_the_read_is_refused() {
    // The file changed between the pin and the read once: read again, and the second read holds.
    let n = Cell::new(0);
    let (_, bytes) = pinned_read(|| {
        n.set(n.get() + 1);
        Ok::<_, Error>(if n.get() == 1 {
            read_of(b"", b"[ui]\n")
        } else {
            read_of(b"[ui]\n", b"[ui]\n")
        })
    })
    .unwrap();
    assert_eq!((n.get(), bytes.unwrap()), (2, b"[ui]\n".to_vec()));
    // Changed again: refused.
    let e = pinned_read(|| Ok(read_of(b"", b"[ui]\n"))).unwrap_err();
    assert!(e.message.contains("changed while it was read"), "{e}");
    // No file on either side is the same file.
    let (v, b) = pinned_read(|| Ok((json!({"targets": [{"sha256": null}]}), None))).unwrap();
    assert_eq!((v["targets"][0]["sha256"].clone(), b), (Value::Null, None));
}

#[test]
fn a_config_grok_build_would_not_take_is_reported_not_failed() {
    let v = settings_of(Some(b"[models\ndefault = \"x\"\n"), None);
    assert_eq!(v["state"], "refused");
    assert!(v["reason"].as_str().unwrap().contains("not TOML"), "{v}");
    let v = settings_of(Some(b"models = \"grok-4.6\"\n"), None);
    assert_eq!(v["state"], "refused");
    assert!(
        v["reason"]
            .as_str()
            .unwrap()
            .contains("models is not a table"),
        "{v}"
    );
    assert_eq!(settings_of(None, None)["state"], "not-configured");
}
