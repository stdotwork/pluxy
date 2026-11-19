use super::*;
use std::fs;
#[cfg(unix)]
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

/// Asserts p is private to this user: 0700 (a folder) or 0600 (a file) on Unix, and on macOS
/// with no extended access list entry; on Windows by the rule of `private_windows.rs`.
fn assert_private(p: &Path) {
    #[cfg(unix)]
    {
        let m = fs::metadata(p).unwrap();
        let want = if m.is_dir() { 0o700 } else { 0o600 };
        assert_eq!(m.permissions().mode() & 0o777, want, "{}", p.display());
    }
    #[cfg(target_os = "macos")]
    assert!(
        !crate::private_macos::has_entries(&fs::File::open(p).unwrap()).unwrap(),
        "{} has an access list entry",
        p.display()
    );
    #[cfg(windows)]
    assert!(crate::private_windows::tests::private(p), "{}", p.display());
}

/// Writes a file into the vault as the vault would (private), behind its back.
fn plant(p: &Path, b: &[u8]) {
    crate::managed::write_private(p, b).unwrap();
    #[cfg(windows)]
    crate::private_windows::make_private(&crate::private_windows::tests::open(p), false).unwrap();
}

/// Lets others read p.
fn readable_by_others(p: &Path) {
    #[cfg(unix)]
    fs::set_permissions(p, fs::Permissions::from_mode(0o644)).unwrap();
    #[cfg(windows)]
    crate::private_windows::tests::readable_by_everyone(p);
}

/// Makes link a link to the folder target: a symlink on Unix, a junction on Windows.
fn link_folder(target: &Path, link: &Path) {
    #[cfg(unix)]
    std::os::unix::fs::symlink(target, link).unwrap();
    #[cfg(windows)]
    crate::private_windows::tests::junction(target, link);
}

#[test]
fn a_client_keys_binding_is_kept_and_listed_without_its_value() {
    let (d, v) = vault();
    let binding = ClientKey {
        core: CoreTarget {
            mode: crate::conn::Mode::Managed,
            origin: "http://127.0.0.1:8400".into(),
        },
        key_id: Some("0123456789abcdef01234567".into()),
        masked: "sk******89".into(),
        client: "pluxy/agent".into(),
    };
    let plain = v.add_secret("plain", KEY).unwrap();
    let id = v.add_client_key("agent", KEY, binding.clone()).unwrap();
    // Read back by the next app: the binding with it, a plain secret still without one.
    let again = Vault::open(d.0.join("vault"));
    assert_eq!(again.secret(&id).unwrap().client_key, Some(binding));
    assert_eq!(again.secret(&plain).unwrap().client_key, None);
    let list = serde_json::to_value(again.secrets()).unwrap();
    assert!(list[0].get("client_key").is_none(), "{list}");
    assert_eq!(list[1]["client_key"]["core"]["mode"], "managed");
    assert!(!list.to_string().contains(KEY));
    assert!(again
        .add_client_key("", KEY, again.secret(&id).unwrap().client_key.unwrap())
        .is_err());
}

#[test]
fn a_full_or_broken_vault_says_so_before_anything_is_made() {
    let (d, v) = vault();
    assert!(v.can_add().is_ok());
    for i in 0..MAX_SECRETS {
        v.add_secret(&format!("s{i}"), KEY).unwrap();
    }
    assert!(v.can_add().unwrap_err().contains("at most"));
    fs::write(d.0.join("vault").join("secrets.json"), b"not json").unwrap();
    assert!(Vault::open(d.0.join("vault")).can_add().is_err());
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
    assert_private(&d.0.join("vault"));
    assert_private(&file);
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
    plant(&file, b"{not json");
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
    readable_by_others(&file);
    assert!(Vault::open(d.0.join("vault")).broken().is_some());
}

#[test]
fn secrets_others_may_read_are_refused() {
    let (d, v) = vault();
    v.add_secret("client key", KEY).unwrap();
    let file = d.0.join("vault").join("secrets.json");
    readable_by_others(&file);
    let refused = Vault::open(d.0.join("vault"));
    assert!(
        refused.broken().unwrap().contains("no one else may read"),
        "{:?}",
        refused.broken()
    );
    assert!(refused.secrets().is_empty());
}

#[test]
fn a_vault_folder_others_may_use_is_made_private_again() {
    let (d, v) = vault();
    let id = v.add_secret("client key", KEY).unwrap();
    let root = d.0.join("vault");
    #[cfg(unix)]
    fs::set_permissions(&root, fs::Permissions::from_mode(0o755)).unwrap();
    #[cfg(windows)]
    crate::private_windows::tests::readable_by_everyone(&root);
    let again = Vault::open(root.clone());
    assert_eq!(again.value(&id).as_deref(), Some(KEY));
    assert_private(&root);
}

/// macOS: a 0600 secrets file that an extended access list lets others read is refused like a
/// 0644 one, and left as it is.
#[cfg(target_os = "macos")]
#[test]
fn secrets_an_access_list_lets_others_read_are_refused() {
    let (d, v) = vault();
    v.add_secret("client key", KEY).unwrap();
    let file = d.0.join("vault").join("secrets.json");
    let before = fs::read(&file).unwrap();
    crate::private_macos::add_entry(&file, "everyone allow read");
    assert_eq!(
        fs::metadata(&file).unwrap().permissions().mode() & 0o777,
        0o600
    );
    let refused = Vault::open(d.0.join("vault"));
    assert!(
        refused.broken().unwrap().contains("no one else may read"),
        "{:?}",
        refused.broken()
    );
    assert!(refused.secrets().is_empty());
    assert_eq!(fs::read(&file).unwrap(), before);
}

/// macOS: a vault folder whose access list others may use, and would pass on to new files, is
/// emptied when the vault opens it, as a too-open mode is tightened; what is written after has
/// no entry.
#[cfg(target_os = "macos")]
#[test]
fn a_vault_folder_with_an_access_list_is_emptied_and_passes_nothing_on() {
    let (d, v) = vault();
    let id = v.add_secret("client key", KEY).unwrap();
    let root = d.0.join("vault");
    crate::private_macos::add_entry(&root, "everyone allow list,file_inherit,directory_inherit");
    let again = Vault::open(root.clone());
    assert_eq!(again.value(&id).as_deref(), Some(KEY));
    assert_private(&root);
    // A save checks the folder again: an entry added since the open is emptied before the new
    // secrets file is made in it, so the file takes none.
    crate::private_macos::add_entry(&root, "everyone allow list,file_inherit,directory_inherit");
    again
        .add_secret("another", "sk-vault-test-other-0123456789")
        .unwrap();
    assert_private(&root);
    assert_private(&root.join("secrets.json"));
}

#[test]
fn a_name_too_long_is_refused_never_cut_to_another() {
    let (d, _v) = vault();
    let root = Folder::root(&d.0.join("vault"), true).unwrap().unwrap();
    root.write("a", b"short").unwrap();
    // 32769 characters: 65538 bytes, which a 16-bit count would wrap to 2, the name "a".
    let long = "a".repeat(32769);
    assert!(root.read(&long).is_err());
    assert!(root.write(&long, b"other").is_err());
    assert!(root.remove(&long).is_err());
    assert!(root.folder(&long, true).is_err());
    assert_eq!(root.read("a").unwrap().as_deref(), Some(&b"short"[..]));
}

/// Makes link a symbolic link to the file target. False when this account may not (on Windows it
/// takes a privilege or developer mode).
fn link_file(target: &Path, link: &Path) -> bool {
    #[cfg(unix)]
    let made = std::os::unix::fs::symlink(target, link);
    #[cfg(windows)]
    let made = std::os::windows::fs::symlink_file(target, link);
    match made {
        Ok(()) => true,
        Err(e) => {
            eprintln!("SKIPPED: this account cannot make a symbolic link: {e}");
            false
        }
    }
}

#[test]
fn a_file_link_in_the_vault_is_never_followed() {
    let (d, v) = vault();
    v.add_secret("client key", KEY).unwrap();
    let path = d.0.join("vault");
    let target = d.0.join("target");
    fs::write(&target, b"outside").unwrap();
    let link = path.join("link");
    if !link_file(&target, &link) {
        return;
    }
    let root = Folder::root(&path, false).unwrap().unwrap();
    assert!(root.read("link").is_err());
    // Removing it removes the link itself, never what it names.
    root.remove("link").unwrap();
    assert!(fs::symlink_metadata(&link).is_err());
    assert_eq!(fs::read(&target).unwrap(), b"outside");
    // Writing it puts a private file of the vault's in the link's place; what it named is as it was.
    assert!(link_file(&target, &link));
    root.write("link", b"inside").unwrap();
    assert!(!fs::symlink_metadata(&link).unwrap().is_symlink());
    assert_private(&link);
    assert_eq!(root.read("link").unwrap().as_deref(), Some(&b"inside"[..]));
    assert_eq!(fs::read(&target).unwrap(), b"outside");
    // The vault's secrets as a link: refused, not read.
    let secrets = path.join("secrets.json");
    fs::remove_file(&secrets).unwrap();
    assert!(link_file(&target, &secrets));
    let refused = Vault::open(path.clone());
    assert!(refused.broken().is_some());
    assert_eq!(fs::read(&target).unwrap(), b"outside");
}

/// Windows: a data folder whose entries forbid setting an access list on what is made in it (OWNER
/// RIGHTS denied WRITE_DAC) gets no vault: nothing private can be made there, so nothing is.
#[cfg(windows)]
#[test]
fn a_data_folder_that_forbids_private_files_keeps_no_vault() {
    let t = crate::private_windows::tests::temp();
    assert!(crate::private_windows::tests::icacls(
        &t.0,
        &["/deny", "*S-1-3-4:(OI)(CI)(IO)(WDAC)"]
    ));
    let v = Vault::open(t.0.join("vault"));
    assert!(v.add_secret("client key", KEY).is_err());
    assert!(fs::symlink_metadata(t.0.join("vault")).is_err());
}

/// Windows: a vault file another program holds open is replaced when that program lets it be
/// deleted (`FILE_SHARE_DELETE`); when it does not, the write fails, the file stays as it was and
/// no temporary file is left.
#[cfg(windows)]
#[test]
fn a_vault_file_held_open_elsewhere_is_replaced_or_left_whole() {
    use std::os::windows::fs::OpenOptionsExt;
    use windows_sys::Win32::Storage::FileSystem::{
        FILE_SHARE_DELETE, FILE_SHARE_READ, FILE_SHARE_WRITE,
    };
    let (d, _v) = vault();
    let path = d.0.join("vault");
    let root = Folder::root(&path, true).unwrap().unwrap();
    root.write("f", b"one").unwrap();
    let hold = |share| {
        fs::OpenOptions::new()
            .read(true)
            .share_mode(share)
            .open(path.join("f"))
            .unwrap()
    };
    let held = hold(FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE);
    root.write("f", b"two").unwrap();
    assert_eq!(root.read("f").unwrap().as_deref(), Some(&b"two"[..]));
    drop(held);
    let held = hold(FILE_SHARE_READ);
    assert!(root.write("f", b"three").is_err());
    assert_eq!(root.names().unwrap(), vec!["f".to_string()]);
    drop(held);
    assert_eq!(root.read("f").unwrap().as_deref(), Some(&b"two"[..]));
}

#[test]
fn only_plain_names_are_vault_names() {
    for name in ["", ".", "..", "a/b", "a\0b"] {
        assert!(check_name(name).is_err(), "{name:?}");
    }
    for name in [
        "index.json",
        ".index.json.0123456789abcdef.tmp",
        "packages",
        "b-0f",
    ] {
        assert!(check_name(name).is_ok(), "{name:?}");
    }
    // What Windows paths cannot name plainly, refused there (and checked everywhere).
    for name in [
        "CON", "con", "nul.txt", "Aux.json", "PRN", "COM1", "lpt9.x", "COM²", "nul .txt", "a:b",
        "a\\b", "x.", "x ", "a*b", "a?b", "a<b", "a>b", "a|b", "a\"b", "a\tb",
    ] {
        assert!(windows_refuses(name), "{name:?}");
        assert_eq!(check_name(name).is_err(), cfg!(windows), "{name:?}");
    }
    for name in [
        "COM10",
        "COM0",
        "console",
        "nulls",
        "index.json",
        ".x.tmp",
        "LPT",
    ] {
        assert!(!windows_refuses(name), "{name:?}");
    }
}

/// Windows: a folder the vault holds cannot be renamed or removed under it.
#[cfg(windows)]
#[test]
fn a_held_vault_folder_cannot_be_moved() {
    let (d, _v) = vault();
    let root = d.0.join("vault");
    let held = Folder::root(&root, true).unwrap().unwrap();
    let packages = held.folder("packages", true).unwrap().unwrap();
    assert!(fs::rename(&root, d.0.join("moved")).is_err());
    assert!(fs::rename(root.join("packages"), root.join("other")).is_err());
    assert_private(&root.join("packages"));
    drop(packages);
    drop(held);
    fs::rename(&root, d.0.join("moved")).unwrap();
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
    assert_private(&file);
    assert_private(&dir);
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
        plant(&dir.join(name), b"orphan");
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
    plant(&dir.join("index.json"), b"{not json");
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
    link_folder(&elsewhere, &dir);
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
    link_folder(&d.0.join("moved"), &root);
    assert!(Vault::open(root).broken().is_some());
}
