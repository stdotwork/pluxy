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

/// Package id's folder in the vault of d.
fn package_dir(d: &Dir, id: &str) -> PathBuf {
    d.0.join("vault")
        .join("packages")
        .join(Vault::package_name(id))
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
        None,
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
}

#[test]
fn overlapping_values_are_redacted_whole() {
    let (_d, v) = vault();
    // One value inside another, and two that overlap: none of either is left.
    v.add_secret("short", "abcd1234").unwrap();
    v.add_secret("long", "xxabcd1234yy").unwrap();
    v.add_secret("tail", "1234yyzz99").unwrap();
    let out = v.redact("<xxabcd1234yyzz99> <abcd1234>");
    assert_eq!(out, "<[redacted]> <[redacted]>");
    for part in ["abcd", "1234", "yy", "zz99"] {
        assert!(!out.contains(part), "{out}");
    }
}

#[test]
fn a_removed_secret_stays_redacted_until_the_app_restarts() {
    let (d, v) = vault();
    let s = v.add_secret("client key", KEY).unwrap();
    v.remove_secret(&s).unwrap();
    assert_eq!(v.redact(&format!("late {KEY}")), "late [redacted]");
    assert!(v.carries(KEY));
    // The next app never had it.
    let again = Vault::open(d.0.join("vault"));
    assert!(!again.carries(KEY));
}

#[test]
fn journals_are_begun_ended_once_and_named_until_then() {
    let (d, v) = vault();
    let w = holder("pluxy/w", 1);
    let begin = |v: &Vault| {
        v.request(
            &w,
            "apply",
            None,
            &json!({"op": "journal.begin", "intent": {"file": "/x"}}),
        )
    };
    assert_eq!(
        v.request(
            &w,
            "",
            Some(&[]),
            &json!({"op": "journal.begin", "intent": {}})
        )
        .unwrap_err()
        .code,
        "permission"
    );
    let j = begin(&v).unwrap()["journal"].as_str().unwrap().to_string();
    // Open: named for describe, and no second one begins.
    assert_eq!(v.open_journals("pluxy/w").unwrap()[0]["id"], j);
    assert_eq!(
        v.open_journals("pluxy/w").unwrap()[0]["intent"],
        json!({"file": "/x"})
    );
    assert_eq!(begin(&v).unwrap_err().code, "unavailable");
    assert!(v.open_journals("pluxy/other").unwrap().is_empty());
    // Kept on disk, privately: the app's next run names it too.
    let v = Vault::open(d.0.join("vault"));
    assert_eq!(v.open_journals("pluxy/w").unwrap().len(), 1);
    let told = [j.clone()];
    let end = |outcome: &str| {
        v.request(
            &w,
            "",
            Some(&told),
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
    assert!(v.open_journals("pluxy/w").unwrap().is_empty());
    let views = v.journals("pluxy/w").unwrap();
    assert_eq!(views.len(), 1);
    assert!(views[0].reconciled && views[0].outcome.as_deref() == Some("committed"));
    assert!(!serde_json::to_string(&views).unwrap().contains("/x"));
    // Committed in its call: not reconciled.
    let j2 = begin(&v).unwrap()["journal"].as_str().unwrap().to_string();
    v.request(
        &w,
        "apply",
        None,
        &json!({"op": "journal.commit", "journal": j2}),
    )
    .unwrap();
    assert!(
        !v.journals("pluxy/w")
            .unwrap()
            .iter()
            .find(|x| x.id == j2)
            .unwrap()
            .reconciled
    );
}

#[test]
fn a_journal_intent_or_backup_label_holding_a_secret_or_reference_is_refused() {
    let (d, v) = vault();
    let w = holder("pluxy/w", 1);
    let s = v.add_secret("client key", KEY).unwrap();
    let r = v.mint(&s, &w, v.begin_call()).unwrap();
    let data = base64::engine::general_purpose::STANDARD.encode(b"bytes");
    let refused_without_naming = |e: Refused| {
        assert_eq!(e.code, "invalid_request");
        assert!(!e.message.contains(KEY) && !e.message.contains(&r), "{e:?}");
    };
    for intent in [
        json!({"note": format!("use {KEY} here")}),
        json!({"nested": [{"deep": r}]}),
        json!({KEY: 1}),
        json!({"ref": format!("<{r}>")}),
    ] {
        let e = v
            .request(
                &w,
                "apply",
                None,
                &json!({"op": "journal.begin", "intent": intent}),
            )
            .unwrap_err();
        refused_without_naming(e);
    }
    for label in [format!("copy of {KEY}"), r.clone()] {
        let e = v
            .request(
                &w,
                "apply",
                None,
                &json!({"op": "backup.put", "label": label, "data": data}),
            )
            .unwrap_err();
        refused_without_naming(e);
    }
    // Nothing was kept, on disk either.
    assert!(v.open_journals("pluxy/w").unwrap().is_empty());
    let listed = v
        .request(&w, "apply", None, &json!({"op": "backup.list"}))
        .unwrap();
    assert_eq!(listed["backups"], json!([]));
    let again = Vault::open(d.0.join("vault"));
    assert!(again.open_journals("pluxy/w").unwrap().is_empty());
    // Text without either is kept as before.
    v.request(
        &w,
        "apply",
        None,
        &json!({"op": "journal.begin", "intent": {"file": "/x"}}),
    )
    .unwrap();
    v.request(
        &w,
        "apply",
        None,
        &json!({"op": "backup.put", "label": "original", "data": data}),
    )
    .unwrap();
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
            None,
            &json!({"op": "backup.put", "label": "original", "data": data}),
        )
        .unwrap()["backup"]
        .as_str()
        .unwrap()
        .to_string();
    let got = v
        .request(&w, "apply", None, &json!({"op": "backup.get", "backup": b}))
        .unwrap();
    assert_eq!(got["data"], data);
    assert_eq!(got["size"], 14);
    let list = v
        .request(&w, "apply", None, &json!({"op": "backup.list"}))
        .unwrap();
    assert_eq!(list["backups"][0]["backup"], b);
    // Another package has none of them, even another release of the same is the same package.
    let e = v
        .request(
            &holder("pluxy/other", 1),
            "apply",
            None,
            &json!({"op": "backup.get", "backup": b}),
        )
        .unwrap_err();
    assert_eq!(e.code, "not_found");
    assert!(v
        .request(
            &holder("pluxy/w", 2),
            "apply",
            None,
            &json!({"op": "backup.get", "backup": b})
        )
        .is_ok());
    let dir = package_dir(&d, "pluxy/w");
    let file = dir.join(&b);
    assert_eq!(mode(&file), 0o600);
    assert_eq!(mode(&dir), 0o700);
    assert!(file.starts_with(&d.0));
    let big = base64::engine::general_purpose::STANDARD.encode(vec![0u8; MAX_IMAGE + 1]);
    assert!(v
        .request(&w, "apply", None, &json!({"op": "backup.put", "data": big}))
        .is_err());
    v.request(
        &w,
        "apply",
        None,
        &json!({"op": "backup.delete", "backup": b}),
    )
    .unwrap();
    assert!(!file.exists());
    assert_eq!(
        v.request(&w, "apply", None, &json!({"op": "nope"}))
            .unwrap_err()
            .code,
        "unsupported"
    );
}

#[test]
fn a_journal_is_ended_in_a_call_only_by_the_release_that_began_it() {
    let (_d, v) = vault();
    let w1 = holder("pluxy/w", 1);
    let j = v
        .request(
            &w1,
            "apply",
            None,
            &json!({"op": "journal.begin", "intent": {}}),
        )
        .unwrap()["journal"]
        .as_str()
        .unwrap()
        .to_string();
    // Another release's call cannot end it, and is not told what is open.
    let e = v
        .request(
            &holder("pluxy/w", 2),
            "apply",
            None,
            &json!({"op": "journal.commit", "journal": j}),
        )
        .unwrap_err();
    assert_eq!(e.code, "not_found");
    let e2 = v
        .request(
            &holder("pluxy/w", 2),
            "apply",
            None,
            &json!({"op": "journal.begin", "intent": {}}),
        )
        .unwrap_err();
    for e in [&e, &e2] {
        assert!(!e.message.contains(&j), "{}", e.message);
    }
    // Its next describe, told of it, may end it, whichever release it is.
    let told = [j.clone()];
    v.request(
        &holder("pluxy/w", 2),
        "",
        Some(&told),
        &json!({"op": "journal.end", "journal": j, "outcome": "rolled_back"}),
    )
    .unwrap();
    let e = v
        .request(
            &w1,
            "apply",
            None,
            &json!({"op": "journal.end", "journal": j, "outcome": "committed"}),
        )
        .unwrap_err();
    assert!(!e.message.contains(&j), "{}", e.message);
}

#[test]
fn describe_reads_backups_and_ends_only_the_journals_it_was_told_of() {
    let (_d, v) = vault();
    let w = holder("pluxy/w", 1);
    let begin = || {
        v.request(
            &w,
            "apply",
            None,
            &json!({"op": "journal.begin", "intent": {}}),
        )
        .unwrap()["journal"]
            .as_str()
            .unwrap()
            .to_string()
    };
    let j = begin();
    let data = base64::engine::general_purpose::STANDARD.encode(b"x");
    for op in [
        json!({"op": "backup.put", "data": data}),
        json!({"op": "backup.delete", "backup": "b-0"}),
        json!({"op": "journal.begin", "intent": {}}),
    ] {
        let e = v
            .request(&w, "", Some(std::slice::from_ref(&j)), &op)
            .unwrap_err();
        assert_eq!(e.code, "permission", "{op}");
    }
    assert!(v
        .request(
            &w,
            "",
            Some(std::slice::from_ref(&j)),
            &json!({"op": "backup.list"})
        )
        .is_ok());
    // Not told of it: not ended.
    let e = v
        .request(
            &w,
            "",
            Some(&[]),
            &json!({"op": "journal.end", "journal": j, "outcome": "committed"}),
        )
        .unwrap_err();
    assert_eq!(e.code, "not_found");
    assert_eq!(v.open_journals("pluxy/w").unwrap().len(), 1);
}

#[test]
fn files_the_records_do_not_name_are_removed_and_damaged_records_fail() {
    let (d, v) = vault();
    let w = holder("pluxy/w", 1);
    v.request(&w, "apply", None, &json!({"op": "backup.list"}))
        .unwrap();
    v.request(
        &w,
        "apply",
        None,
        &json!({"op": "journal.begin", "intent": {}}),
    )
    .unwrap();
    let dir = package_dir(&d, "pluxy/w");
    for name in ["b-00000000000000000000000000000000", ".index.json.0123.tmp"] {
        crate::managed::write_private(&dir.join(name), b"orphan").unwrap();
    }
    v.request(&w, "apply", None, &json!({"op": "backup.list"}))
        .unwrap();
    let mut left: Vec<String> = fs::read_dir(&dir)
        .unwrap()
        .map(|e| e.unwrap().file_name().into_string().unwrap())
        .collect();
    left.sort();
    assert_eq!(left, vec!["index.json".to_string()]);
    // Records that cannot be read are an error, not "no journals".
    crate::managed::write_private(&dir.join("index.json"), b"{not json").unwrap();
    assert!(v.open_journals("pluxy/w").unwrap_err().contains("damaged"));
    assert!(v.journals("pluxy/w").is_err());
}

#[test]
fn a_vault_folder_replaced_by_a_link_is_refused_not_followed() {
    let (d, v) = vault();
    let w = holder("pluxy/w", 1);
    v.request(
        &w,
        "apply",
        None,
        &json!({"op": "journal.begin", "intent": {}}),
    )
    .unwrap();
    let dir = package_dir(&d, "pluxy/w");
    let elsewhere = d.0.join("elsewhere");
    fs::rename(&dir, &elsewhere).unwrap();
    std::os::unix::fs::symlink(&elsewhere, &dir).unwrap();
    let data = base64::engine::general_purpose::STANDARD.encode(b"x");
    let e = v
        .request(
            &w,
            "apply",
            None,
            &json!({"op": "backup.put", "data": data}),
        )
        .unwrap_err();
    assert_eq!(e.code, "unavailable");
    assert!(v.open_journals("pluxy/w").is_err());
    // Nothing was written through the link.
    let mut there: Vec<String> = fs::read_dir(&elsewhere)
        .unwrap()
        .map(|e| e.unwrap().file_name().into_string().unwrap())
        .collect();
    there.sort();
    assert_eq!(there, vec!["index.json".to_string()]);
    // The vault folder itself as a link is refused too.
    let root = d.0.join("vault");
    fs::rename(&root, d.0.join("moved")).unwrap();
    std::os::unix::fs::symlink(d.0.join("moved"), &root).unwrap();
    assert!(Vault::open(root).broken().is_some());
}
