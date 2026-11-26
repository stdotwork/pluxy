//! Expected values are written out by hand, never worked out by the code under test.

use crate::{pinned_read, settings_of, Checked};
use pluxy_program::Error;
use pluxy_transactions::{sha256_of, Merge};
use serde_json::{json, Value};
use std::cell::Cell;

/// A preview pinning config and catalog as pinned, and then the bytes read.
fn read_of(pinned: [&[u8]; 2], read: [&[u8]; 2]) -> (Value, [Option<Vec<u8>>; 2]) {
    (
        json!({"targets": [
            {"sha256": sha256_of(Some(pinned[0]))},
            {"sha256": sha256_of(Some(pinned[1]))},
        ]}),
        [Some(read[0].to_vec()), Some(read[1].to_vec())],
    )
}

#[test]
fn settings_read_back_are_the_pinned_files_or_the_read_is_refused() {
    // The catalog changed between the pin and the read once: read again, and the second holds.
    let n = Cell::new(0);
    let (_, [config, catalog]) = pinned_read(|| {
        n.set(n.get() + 1);
        Ok::<_, Error>(if n.get() == 1 {
            read_of([b"a = 1", b"{}"], [b"a = 1", b"{\"x\": 1}"])
        } else {
            read_of([b"a = 1", b"{\"x\": 1}"], [b"a = 1", b"{\"x\": 1}"])
        })
    })
    .unwrap();
    assert_eq!(
        (n.get(), config.unwrap(), catalog.unwrap()),
        (2, b"a = 1".to_vec(), b"{\"x\": 1}".to_vec())
    );
    // config.toml changed again: refused.
    let e = pinned_read(|| Ok(read_of([b"a = 1", b"{}"], [b"a = 2", b"{}"]))).unwrap_err();
    assert!(e.message.contains("changed while it was read"), "{e}");
    // No file on either side is the same file.
    let (v, b) = pinned_read(|| {
        Ok((
            json!({"targets": [{"sha256": null}, {"sha256": null}]}),
            [None, None],
        ))
    })
    .unwrap();
    assert_eq!(
        (v["targets"][1]["sha256"].clone(), b),
        (Value::Null, [None, None])
    );
}

#[test]
fn files_codex_would_not_take_are_reported_not_failed() {
    let catalog = br#"{"models": [{"slug": "gpt-6.1-sol"}]}"#;
    for (config, catalog, says) in [
        (&b"model = \n"[..], &catalog[..], "not TOML"),
        (
            b"[model_providers.pluxy]\nwire_api = \"chat\"\n",
            catalog,
            "wire_api",
        ),
        (
            b"model = \"gpt-6.1-sol\"\n",
            b"{\"x\": 1}",
            "no models list",
        ),
    ] {
        let v = settings_of(Some(config), Some(catalog), None);
        assert_eq!(v["state"], "refused", "{v}");
        assert!(v["reason"].as_str().unwrap().contains(says), "{v}");
    }
    let v = settings_of(None, Some(catalog), None);
    assert_eq!(
        (v["state"].clone(), v["models"][0]["slug"].clone()),
        (json!("not-configured"), json!("gpt-6.1-sol"))
    );
}

const APPLIED_CATALOG: &[u8] = br#"{"models": [{"slug": "gpt-6.1-sol"}]}"#;

fn clear(original: Option<&[u8]>, applied: &[u8], current: &[u8]) -> Option<Vec<u8>> {
    Checked
        .merge(original, Some(applied), Some(current))
        .unwrap_or_else(|e| panic!("{e}"))
}

#[test]
fn clear_of_a_catalog_that_was_empty_keeps_the_persons_keys() {
    // The catalog was an empty file; the person added a key after Apply.
    let current = br#"{"models": [{"slug": "gpt-6.1-sol"}], "mine": 1}"#;
    let out = clear(Some(b""), APPLIED_CATALOG, current).unwrap();
    assert_eq!(
        serde_json::from_slice::<Value>(&out).unwrap(),
        json!({"mine": 1})
    );
}

#[test]
fn clear_of_a_file_the_person_emptied_removes_it() {
    // The catalog and config.toml, each emptied to white space after Apply.
    assert_eq!(clear(None, APPLIED_CATALOG, b"\n"), None);
    let original = b"model = \"gpt-6.1-sol\"\n";
    let applied = b"model = \"gpt-6.1-sol\"\nmodel_provider = \"pluxy\"\n";
    assert_eq!(clear(Some(original), applied, b"  \n"), None);
    // Untouched since Apply: the original bytes, white space and all.
    assert_eq!(clear(Some(b" \n"), applied, applied), Some(b" \n".to_vec()));
}
