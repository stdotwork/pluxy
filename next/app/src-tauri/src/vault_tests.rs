use super::*;
use std::os::unix::fs::PermissionsExt;

const KEY: &str = "sk-vault-test-canary-0123456789";

struct Dir(PathBuf);

impl Drop for Dir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn vault() -> (Dir, Vault) {
    let d = std::env::temp_dir().join(format!("pluxy-vault-{}", crate::managed::random_key()));
    let v = Vault::open(d.join("vault"));
    (Dir(d), v)
}

fn holder(id: &str, generation: u64) -> Holder {
    Holder {
        id: id.into(),
        sha256: "ab".repeat(32),
        generation,
    }
}

fn mode(p: &Path) -> u32 {
    fs::metadata(p).unwrap().permissions().mode() & 0o777
}

#[test]
fn secrets_are_kept_privately_listed_without_values_and_read_back() {
    let (d, v) = vault();
    assert!(v.add_secret("short", "1234567").is_err());
    assert!(v.add_secret("ctl", "abcdefgh\n").is_err());
    assert!(v.add_secret("", KEY).is_err());
    let id = v.add_secret("client key", KEY).unwrap();
    assert!(is_opaque("s", &id));
    let list = serde_json::to_string(&v.secrets()).unwrap();
    assert!(list.contains("client key") && !list.contains(KEY));
    assert_eq!(v.value(&id).as_deref(), Some(KEY));
    let file = d.0.join("vault").join("secrets.json");
    assert_eq!(mode(&d.0.join("vault")), 0o700);
    assert_eq!(mode(&file), 0o600);
    // Read back by the next app.
    let again = Vault::open(d.0.join("vault"));
    assert_eq!(again.value(&id).as_deref(), Some(KEY));
    again.remove_secret(&id).unwrap();
    assert!(again.remove_secret(&id).is_err());
    assert!(Vault::open(d.0.join("vault")).secrets().is_empty());
}

#[test]
fn a_damaged_vault_keeps_no_secret_and_changes_nothing() {
    let (d, v) = vault();
    v.add_secret("client key", KEY).unwrap();
    let file = d.0.join("vault").join("secrets.json");
    crate::managed::write_private(&file, b"{not json").unwrap();
    let broken = Vault::open(d.0.join("vault"));
    assert!(broken.broken().is_some());
    assert!(broken.secrets().is_empty());
    assert!(broken.add_secret("other", KEY).is_err());
    let r = broken.request(
        &holder("pluxy/w", 1),
        "apply",
        false,
        &json!({"op": "backup.list"}),
    );
    assert_eq!(r.unwrap_err().code, "unavailable");
    assert_eq!(fs::read(&file).unwrap(), b"{not json");
    // A file others may read is refused too.
    fs::set_permissions(&file, fs::Permissions::from_mode(0o644)).unwrap();
    assert!(Vault::open(d.0.join("vault")).broken().is_some());
}

#[test]
fn a_reference_is_bound_to_its_secret_holder_release_and_call() {
    let (_d, v) = vault();
    let s = v.add_secret("client key", KEY).unwrap();
    let w = holder("pluxy/writer", 1);
    let call = v.begin_call();
    let r = v.mint(&s, &w, call).unwrap();
    assert!(is_opaque("r", &r));
    assert_eq!(v.present(&r, &w, call).unwrap(), s);
    let e = v.present(&r, &holder("pluxy/exec", 1), call).unwrap_err();
    assert!(e.contains("given to pluxy/writer, not pluxy/exec"), "{e}");
    let e = v.present(&r, &holder("pluxy/writer", 2), call).unwrap_err();
    assert!(e.contains("another release"), "{e}");
    let other = Holder {
        sha256: "cd".repeat(32),
        ..w.clone()
    };
    assert!(v
        .present(&r, &other, call)
        .unwrap_err()
        .contains("another release"));
    let e = v.present(&r, &w, v.begin_call()).unwrap_err();
    assert!(e.contains("another call"), "{e}");
    assert!(v.present("r-0", &w, call).is_err());
    // It ends with its call.
    v.end_call(call);
    assert!(v
        .present(&r, &w, call)
        .unwrap_err()
        .contains("another call"));
    // A reference to a removed secret is refused, and none is minted.
    let call = v.begin_call();
    let r = v.mint(&s, &w, call).unwrap();
    v.remove_secret(&s).unwrap();
    assert!(v.present(&r, &w, call).unwrap_err().contains("removed"));
    assert!(v.mint(&s, &w, call).is_none());
}

#[test]
fn values_and_references_are_redacted_and_found() {
    let (_d, v) = vault();
    let s = v.add_secret("client key", KEY).unwrap();
    let r = v.mint(&s, &holder("pluxy/w", 1), v.begin_call()).unwrap();
    let text = format!("key {KEY} ref {r}");
    assert_eq!(v.redact(&text), "key [redacted] ref [redacted]");
    assert!(v.carries(&text) && v.carries(&r) && !v.carries("plain"));
    let out = v.redact_value(&json!({"a": [KEY], r.clone(): 1}));
    assert!(!out.to_string().contains(KEY) && !out.to_string().contains(&r));
}

#[test]
fn journals_are_begun_ended_once_and_named_until_then() {
    let (d, v) = vault();
    let w = holder("pluxy/w", 1);
    let begin = |v: &Vault| {
        v.request(
            &w,
            "apply",
            false,
            &json!({"op": "journal.begin", "intent": {"file": "/x"}}),
        )
    };
    assert!(v
        .request(&w, "", true, &json!({"op": "journal.begin", "intent": {}}))
        .is_err());
    let j = begin(&v).unwrap()["journal"].as_str().unwrap().to_string();
    // Open: named for describe, and no second one begins.
    assert_eq!(v.open_journals("pluxy/w")[0]["id"], j);
    assert_eq!(
        v.open_journals("pluxy/w")[0]["intent"],
        json!({"file": "/x"})
    );
    assert_eq!(begin(&v).unwrap_err().code, "unavailable");
    assert!(v.open_journals("pluxy/other").is_empty());
    // Kept on disk, privately: the app's next run names it too.
    let v = Vault::open(d.0.join("vault"));
    assert_eq!(v.open_journals("pluxy/w").len(), 1);
    let end = |outcome: &str| {
        v.request(
            &w,
            "",
            true,
            &json!({"op": "journal.end", "journal": j, "outcome": outcome}),
        )
    };
    assert_eq!(
        end("committed").unwrap(),
        json!({"journal": j, "outcome": "committed"})
    );
    // Again with the same outcome: the same answer, marked; another outcome is refused.
    assert_eq!(end("committed").unwrap()["again"], true);
    assert_eq!(end("rolled_back").unwrap_err().code, "invalid_request");
    assert!(v.open_journals("pluxy/w").is_empty());
    let views = v.journals("pluxy/w");
    assert_eq!(views.len(), 1);
    assert!(views[0].reconciled && views[0].outcome.as_deref() == Some("committed"));
    assert!(!serde_json::to_string(&views).unwrap().contains("/x"));
    // Committed in its call: not reconciled.
    let j2 = begin(&v).unwrap()["journal"].as_str().unwrap().to_string();
    v.request(
        &w,
        "apply",
        false,
        &json!({"op": "journal.commit", "journal": j2}),
    )
    .unwrap();
    assert!(
        !v.journals("pluxy/w")
            .iter()
            .find(|x| x.id == j2)
            .unwrap()
            .reconciled
    );
}

#[test]
fn backups_are_the_packages_own_private_files() {
    let (d, v) = vault();
    let w = holder("pluxy/w", 1);
    let data = base64::engine::general_purpose::STANDARD.encode(b"original bytes");
    let b = v
        .request(
            &w,
            "apply",
            false,
            &json!({"op": "backup.put", "label": "original", "data": data}),
        )
        .unwrap()["backup"]
        .as_str()
        .unwrap()
        .to_string();
    let got = v
        .request(
            &w,
            "apply",
            false,
            &json!({"op": "backup.get", "backup": b}),
        )
        .unwrap();
    assert_eq!(got["data"], data);
    assert_eq!(got["size"], 14);
    let list = v
        .request(&w, "apply", false, &json!({"op": "backup.list"}))
        .unwrap();
    assert_eq!(list["backups"][0]["backup"], b);
    // Another package has none of them, even another release of the same is the same package.
    let e = v
        .request(
            &holder("pluxy/other", 1),
            "apply",
            false,
            &json!({"op": "backup.get", "backup": b}),
        )
        .unwrap_err();
    assert_eq!(e.code, "not_found");
    assert!(v
        .request(
            &holder("pluxy/w", 2),
            "apply",
            false,
            &json!({"op": "backup.get", "backup": b})
        )
        .is_ok());
    let file = v.package_dir("pluxy/w").join(&b);
    assert_eq!(mode(&file), 0o600);
    assert_eq!(mode(&v.package_dir("pluxy/w")), 0o700);
    assert!(file.starts_with(&d.0));
    let big = base64::engine::general_purpose::STANDARD.encode(vec![0u8; MAX_IMAGE + 1]);
    assert!(v
        .request(
            &w,
            "apply",
            false,
            &json!({"op": "backup.put", "data": big})
        )
        .is_err());
    v.request(
        &w,
        "apply",
        false,
        &json!({"op": "backup.delete", "backup": b}),
    )
    .unwrap();
    assert!(!file.exists());
    assert_eq!(
        v.request(&w, "apply", false, &json!({"op": "nope"}))
            .unwrap_err()
            .code,
        "unsupported"
    );
}
