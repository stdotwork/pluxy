use crate::{config_of, pinned_read, Selection};
use pluxy_program::Error;
use pluxy_transactions::sha256_of;
use serde_json::{json, Value};
use std::cell::Cell;

fn tmp() -> std::path::PathBuf {
    let d = std::env::temp_dir().join(format!(
        "omp-setup-main-test-{}-{:?}",
        std::process::id(),
        std::thread::current().id()
    ));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    d
}

/// A preview whose pins say sha256 of pinned, and then the bytes read.
fn read_of(pinned: [&[u8]; 2], read: [&[u8]; 2]) -> (Value, [Option<Vec<u8>>; 2]) {
    (
        json!({"targets": [{"sha256": sha256_of(Some(pinned[0]))}, {"sha256": sha256_of(Some(pinned[1]))}]}),
        [Some(read[0].to_vec()), Some(read[1].to_vec())],
    )
}

#[test]
fn the_values_read_back_are_the_pinned_files_or_the_read_is_refused() {
    // The settings file changed between the pin and the read once: read again, and it holds.
    let n = Cell::new(0);
    let (_, bytes) = pinned_read(|| {
        n.set(n.get() + 1);
        Ok::<_, Error>(if n.get() == 1 {
            read_of([b"a: 1\n", b"b: 1\n"], [b"a: 1\n", b"b: 2\n"])
        } else {
            read_of([b"a: 1\n", b"b: 2\n"], [b"a: 1\n", b"b: 2\n"])
        })
    })
    .unwrap();
    assert_eq!(
        (n.get(), bytes[1].clone().unwrap()),
        (2, b"b: 2\n".to_vec())
    );
    // Changed again: refused.
    let e =
        pinned_read(|| Ok(read_of([b"a: 1\n", b"b: 1\n"], [b"a: 2\n", b"b: 1\n"]))).unwrap_err();
    assert!(e.message.contains("changed while it was read"), "{e}");
    // No files on either side are the same files.
    let none = json!({"targets": [{"sha256": null}, {"sha256": null}]});
    let (_, b) = pinned_read(|| Ok((none.clone(), [None, None]))).unwrap();
    assert_eq!(b, [None, None]);
}

#[test]
fn files_oh_my_pi_would_not_take_are_reported_refused_not_failed() {
    let dir = tmp();
    let sels = [
        Selection::File(dir.join("models.yml")),
        Selection::File(dir.join("config.yml")),
    ];
    let v = config_of(&sels, Some(b"providers: [a]\n"), None, None);
    assert_eq!(v["state"], "refused");
    assert!(v["reason"].as_str().unwrap().contains("providers"), "{v}");
    let v = config_of(&sels, None, Some(b"a:\n\tb: 1\n"), None);
    assert_eq!(v["state"], "refused");
    assert!(v["reason"].as_str().unwrap().contains("YAML"), "{v}");
    // A legacy models.json Oh My Pi has not moved yet: refused, with the reason.
    std::fs::write(dir.join("models.json"), "{}").unwrap();
    let v = config_of(&sels, None, None, None);
    assert_eq!(v["state"], "refused");
    assert!(v["reason"].as_str().unwrap().contains("models.json"), "{v}");
    std::fs::remove_file(dir.join("models.json")).unwrap();
    assert_eq!(
        config_of(&sels, None, None, None)["state"],
        "not-configured"
    );
    std::fs::remove_dir_all(dir).unwrap();
}
