use crate::{config_of, pinned_read};
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
fn the_values_read_back_are_the_pinned_file_or_the_read_is_refused() {
    // The file changed between the pin and the read once: read again, and it holds.
    let n = Cell::new(0);
    let (_, bytes) = pinned_read(|| {
        n.set(n.get() + 1);
        Ok::<_, Error>(if n.get() == 1 {
            read_of(b"a: 1\n", b"a: 2\n")
        } else {
            read_of(b"a: 2\n", b"a: 2\n")
        })
    })
    .unwrap();
    assert_eq!((n.get(), bytes.unwrap()), (2, b"a: 2\n".to_vec()));
    // Changed again: refused.
    let e = pinned_read(|| Ok(read_of(b"a: 1\n", b"a: 2\n"))).unwrap_err();
    assert!(e.message.contains("changed while it was read"), "{e}");
    // No file on either side is the same file.
    let (_, b) = pinned_read(|| Ok((json!({"targets": [{"sha256": null}]}), None))).unwrap();
    assert_eq!(b, None);
}

#[test]
fn a_file_hermes_would_not_take_is_reported_refused_not_failed() {
    let v = config_of(Some(b"providers: [a]\n"), None);
    assert_eq!(v["state"], "refused");
    assert!(v["reason"].as_str().unwrap().contains("providers"), "{v}");
    let v = config_of(Some(b"a:\n\tb: 1\n"), None);
    assert_eq!(v["state"], "refused");
    assert!(v["reason"].as_str().unwrap().contains("YAML"), "{v}");
    assert_eq!(config_of(None, None)["state"], "not-configured");
}
