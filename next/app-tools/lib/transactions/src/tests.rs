//! Unit tests on real files in temporary folders, with an in-memory stand-in for the host's vault
//! (the native test drives the real one through the fixture writer).

use super::*;
use std::cell::{Cell, RefCell};
use std::collections::BTreeMap;
use std::os::unix::fs::MetadataExt;

/// The vault's frames as the host answers them, in memory.
#[derive(Default)]
struct Mem {
    backups: RefCell<BTreeMap<String, (String, String)>>,
    journals: RefCell<BTreeMap<String, (Value, Option<String>)>>,
    next: Cell<u64>,
    /// Answers backup.get without the bytes (a broken vault).
    no_data: Cell<bool>,
}

impl Mem {
    fn id(&self, p: &str) -> String {
        self.next.set(self.next.get() + 1);
        format!("{p}-{}", self.next.get())
    }

    /// The open journals as describe names them.
    fn open(&self) -> Vec<Value> {
        self.journals
            .borrow()
            .iter()
            .filter(|(_, (_, o))| o.is_none())
            .map(|(id, (intent, _))| json!({"id": id, "op": "apply", "intent": intent}))
            .collect()
    }

    fn outcome(&self, id: &Value) -> Option<String> {
        self.journals.borrow()[id.as_str().unwrap()].1.clone()
    }
}

impl Vault for Mem {
    fn vault(&self, op: &str, f: Map<String, Value>) -> Result<Value, Error> {
        let s = |k: &str| f[k].as_str().unwrap_or_default().to_string();
        match op {
            "backup.put" => {
                let id = self.id("b");
                self.backups
                    .borrow_mut()
                    .insert(id.clone(), (s("label"), s("data")));
                Ok(json!({"backup": id}))
            }
            "backup.get" => {
                let b = self.backups.borrow();
                let (label, data) = b
                    .get(&s("backup"))
                    .ok_or_else(|| Error::not_found("no backup"))?;
                if self.no_data.get() {
                    return Ok(json!({"backup": s("backup"), "label": label}));
                }
                Ok(json!({"backup": s("backup"), "label": label, "data": data}))
            }
            "backup.list" => Ok(json!({"backups": self.backups.borrow().iter()
                .map(|(id, (label, _))| json!({"backup": id, "label": label})).collect::<Vec<_>>()})),
            "backup.delete" => {
                self.backups.borrow_mut().remove(&s("backup"));
                Ok(json!({}))
            }
            "journal.begin" => {
                if !self.open().is_empty() {
                    return Err(Error::unavailable("a journal is open"));
                }
                let id = self.id("j");
                self.journals
                    .borrow_mut()
                    .insert(id.clone(), (f["intent"].clone(), None));
                Ok(json!({"journal": id}))
            }
            "journal.commit" | "journal.end" => {
                let outcome = if op == "journal.commit" {
                    "committed".into()
                } else {
                    s("outcome")
                };
                let mut j = self.journals.borrow_mut();
                let e = j
                    .get_mut(&s("journal"))
                    .ok_or_else(|| Error::not_found("no journal"))?;
                if e.1.as_ref().is_some_and(|o| *o != outcome) {
                    return Err(Error::invalid_request("ended already"));
                }
                e.1 = Some(outcome.clone());
                Ok(json!({"journal": s("journal"), "outcome": outcome}))
            }
            _ => Err(Error::unsupported(op.to_string())),
        }
    }
}

/// A fresh temporary folder, removed when dropped.
struct Tmp(PathBuf);

impl Tmp {
    fn new() -> Tmp {
        let p = std::env::temp_dir().join(format!("pluxy-tx-{}", target::random_hex().unwrap()));
        std::fs::create_dir_all(&p).unwrap();
        Tmp(std::fs::canonicalize(p).unwrap())
    }
    fn at(&self, rel: &str) -> PathBuf {
        self.0.join(rel)
    }
    fn put(&self, rel: &str, bytes: &str) -> PathBuf {
        let p = self.at(rel);
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(&p, bytes).unwrap();
        p
    }
}

impl Drop for Tmp {
    fn drop(&mut self) {
        let _ = std::process::Command::new("chmod")
            .args(["-R", "u+w"])
            .arg(&self.0)
            .status();
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn read(p: &Path) -> Option<String> {
    std::fs::read_to_string(p).ok()
}

fn link_of(p: &Path) -> (u64, PathBuf) {
    (
        std::fs::symlink_metadata(p).unwrap().ino(),
        std::fs::read_link(p).unwrap(),
    )
}

/// A writer's image: before (an object, or {}) with values set.
fn values(v: Value) -> Box<Image<'static>> {
    Box::new(move |_, _, before| {
        let mut o = before
            .map(Json::parse)
            .transpose()?
            .unwrap_or_else(|| json!({}));
        for (k, val) in v.as_object().unwrap() {
            o[k] = val.clone();
        }
        Json::render(&o).map(Some)
    })
}

/// The stow layout: home/.config/x/settings.json, a relative link into home/dotfiles.
fn stow(t: &Tmp) -> (PathBuf, PathBuf) {
    let target = t.put("dotfiles/x/settings.json", "{\"theme\": \"light\"}\n");
    std::fs::set_permissions(&target, std::fs::Permissions::from_mode(0o640)).unwrap();
    let link = t.at(".config/x/settings.json");
    std::fs::create_dir_all(link.parent().unwrap()).unwrap();
    std::os::unix::fs::symlink("../../dotfiles/x/settings.json", &link).unwrap();
    (link, target)
}

fn pins(tx: &Transactions, set: &str, sel: &[Selection]) -> Vec<Pin> {
    let p = tx.preview(set, sel).unwrap();
    p["targets"]
        .as_array()
        .unwrap()
        .iter()
        .map(|v| Pin::from_json(v).unwrap())
        .collect()
}

fn none(_: usize) {}

#[test]
fn base64_round_trips() {
    for s in ["", "f", "fo", "foo", "foob", "fooba", "foobar"] {
        let e = b64::encode(s.as_bytes());
        assert_eq!(b64::decode(&e).unwrap(), s.as_bytes(), "{e}");
    }
    assert_eq!(b64::encode(b"foobar"), "Zm9vYmFy");
    assert_eq!(b64::encode(b"fo"), "Zm8=");
    assert!(
        b64::decode("Zm9=v").is_none()
            && b64::decode("Z=9v").is_none()
            && b64::decode("Zm*v").is_none()
    );
}

#[test]
fn merge_keeps_the_persons_edits() {
    let o = json!({"theme": "light", "keep": {"a": 1}});
    let a = json!({"theme": "light", "keep": {"a": 1}, "model": "m", "proxy": "p"});
    let c =
        json!({"theme": "light", "keep": {"a": 1}, "model": "mine", "proxy": "p", "editor": "vim"});
    assert_eq!(
        merge_value(Some(&o), Some(&a), Some(&c)),
        Some(json!({"theme": "light", "keep": {"a": 1}, "model": "mine", "editor": "vim"}))
    );
    // Untouched since the apply: the original comes back, absent included.
    assert_eq!(merge_value(None, Some(&a), Some(&a)), None);
    assert_eq!(
        Json.merge(Some(b"x"), Some(b"y"), Some(b"y")).unwrap(),
        Some(b"x".to_vec())
    );
    // Deleted by the person: stays deleted.
    assert_eq!(
        Json.merge(Some(b"{}"), Some(b"{\"a\":1}"), None).unwrap(),
        None
    );
}

#[test]
fn apply_writes_only_the_link_target_and_keeps_the_link() {
    let t = Tmp::new();
    let (link, target) = stow(&t);
    let other = t.put("dotfiles/x/other.json", "{}\n");
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let before = link_of(&link);
    let p = tx.preview("s", &[Selection::File(link.clone())]).unwrap();
    assert_eq!(p["targets"][0]["link"], "../../dotfiles/x/settings.json");
    assert_eq!(p["targets"][0]["target"], json!(target));
    let pins: Vec<Pin> = vec![Pin::from_json(&p["targets"][0]).unwrap()];
    let o = tx
        .apply("s", &pins, &values(json!({"model": "m"})), &Json, &mut none)
        .unwrap();
    assert_eq!(o, json!({"outcome": "applied", "changed": [target]}));
    assert_eq!(
        read(&target).unwrap(),
        "{\n  \"theme\": \"light\",\n  \"model\": \"m\"\n}\n"
    );
    assert_eq!(std::fs::metadata(&target).unwrap().mode() & 0o7777, 0o640);
    assert_eq!(link_of(&link), before);
    assert_eq!(read(&other).unwrap(), "{}\n");
    let names: Vec<_> = std::fs::read_dir(target.parent().unwrap())
        .unwrap()
        .map(|e| e.unwrap().file_name())
        .collect();
    assert_eq!(names.len(), 2, "a temporary file was left: {names:?}");
    // The journal committed; only the original and applied images stay in the vault.
    assert!(mem
        .journals
        .borrow()
        .values()
        .all(|(_, o)| o.as_deref() == Some("committed")));
    let labels: Vec<String> = mem
        .backups
        .borrow()
        .values()
        .map(|(l, _)| l.clone())
        .collect();
    assert_eq!(labels, ["tx original", "tx applied"]);
    // The same pins again: the file changed since, so they are refused.
    let e = tx
        .apply("s", &pins, &values(json!({"model": "n"})), &Json, &mut none)
        .unwrap_err();
    assert!(
        e.message.contains("changed") || e.message.contains("modified"),
        "{e}"
    );
}

#[test]
fn a_root_link_is_followed_but_links_and_parts_inside_it_are_refused() {
    let t = Tmp::new();
    let real = t
        .put("share/root/a.json", "{}\n")
        .parent()
        .unwrap()
        .to_path_buf();
    let outside = t.put("outside.json", "{\"o\": 1}\n");
    std::os::unix::fs::symlink(&outside, real.join("inner.json")).unwrap();
    std::fs::create_dir(real.join("sub")).unwrap();
    std::os::unix::fs::symlink(t.at("share"), real.join("sub/up")).unwrap();
    let root = t.at("root-link");
    std::os::unix::fs::symlink(&real, &root).unwrap();
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let sel = |p: &str| Selection::Root {
        root: root.clone(),
        path: p.into(),
    };
    for (p, says) in [
        ("inner.json", "link"),
        ("sub/up/x.json", "link"),
        ("../outside.json", "path"),
        ("", "path"),
        ("a//b", "path"),
    ] {
        let e = tx.preview("r", &[sel(p)]).unwrap_err();
        assert!(e.message.contains(says), "{p}: {e}");
    }
    let pins = pins(&tx, "r", &[sel("a.json"), sel("sub/b.json")]);
    assert_eq!(pins[0].link.as_deref(), real.to_str());
    tx.apply("r", &pins, &values(json!({"m": true})), &Json, &mut none)
        .unwrap();
    assert_eq!(read(&real.join("a.json")).unwrap(), "{\n  \"m\": true\n}\n");
    assert_eq!(
        read(&real.join("sub/b.json")).unwrap(),
        "{\n  \"m\": true\n}\n"
    );
    assert_eq!(
        std::fs::metadata(real.join("sub/b.json")).unwrap().mode() & 0o7777,
        0o600
    );
    assert_eq!(read(&outside).unwrap(), "{\"o\": 1}\n");
    assert_eq!(std::fs::read_link(&root).unwrap(), real);
}

#[test]
fn retargeted_replaced_or_modified_targets_are_refused() {
    let t = Tmp::new();
    let (link, target) = stow(&t);
    let alt = t.put("dotfiles/alt.json", "{}\n");
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let v = values(json!({"x": 1}));
    let relink = |to: &Path| {
        let tmp = link.with_extension("new");
        std::os::unix::fs::symlink(to, &tmp).unwrap();
        std::fs::rename(&tmp, &link).unwrap();
    };
    // Pointed elsewhere.
    let p = pins(&tx, "s", &[Selection::File(link.clone())]);
    std::fs::hard_link(&link, t.at("held-original")).unwrap();
    relink(&alt);
    let e = tx.apply("s", &p, &v, &Json, &mut none).unwrap_err();
    assert!(e.message.contains("changed"), "{e}");
    // Back to the same text, but another link: still refused (its identity is pinned). The earlier
    // links' inodes are kept in use (hard links to them), so the new link cannot get their numbers:
    // a filesystem that reuses a freed inode number (ext4 does) could otherwise give the new link
    // the old identity, and then nothing tells the two apart (same text, same target, same bytes).
    std::fs::hard_link(&link, t.at("held-alt")).unwrap();
    relink(Path::new("../../dotfiles/x/settings.json"));
    let ino = |p: &str| std::fs::symlink_metadata(t.at(p)).unwrap().ino();
    assert!(![ino("held-original"), ino("held-alt")].contains(&link_of(&link).0));
    let e = tx.apply("s", &p, &v, &Json, &mut none).unwrap_err();
    assert!(e.message.contains("changed"), "{e}");
    // Replaced by rename with the same bytes.
    let p = pins(&tx, "s", &[Selection::File(link.clone())]);
    let tmp = target.with_extension("editor");
    std::fs::copy(&target, &tmp).unwrap();
    std::fs::rename(&tmp, &target).unwrap();
    let e = tx.apply("s", &p, &v, &Json, &mut none).unwrap_err();
    assert!(e.message.contains("changed"), "{e}");
    // Modified in place at once, with the same size: detected by the bytes' SHA-256, whatever the
    // file's times say.
    let p = pins(&tx, "s", &[Selection::File(link.clone())]);
    std::fs::write(&target, "{\"theme\": \"LIGHT\"}\n").unwrap();
    let e = tx.apply("s", &p, &v, &Json, &mut none).unwrap_err();
    assert!(e.message.contains("modified"), "{e}");
    assert_eq!(read(&target).unwrap(), "{\"theme\": \"LIGHT\"}\n");
    // Modified in place.
    let p = pins(&tx, "s", &[Selection::File(link.clone())]);
    std::fs::write(&target, "{\"theme\": \"dark\"}\n").unwrap();
    let e = tx.apply("s", &p, &v, &Json, &mut none).unwrap_err();
    assert!(e.message.contains("modified"), "{e}");
    assert_eq!(read(&target).unwrap(), "{\"theme\": \"dark\"}\n");
    assert_eq!(read(&alt).unwrap(), "{}\n");
    assert!(
        mem.journals.borrow().is_empty(),
        "a refused write began a journal"
    );
}

#[test]
fn clear_keeps_later_edits_and_restore_puts_the_original_back() {
    let t = Tmp::new();
    let (link, target) = stow(&t);
    let created = t.at("dotfiles/x/new.json");
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let sel = [
        Selection::File(link.clone()),
        Selection::File(created.clone()),
    ];
    let p = pins(&tx, "s", &sel);
    tx.apply(
        "s",
        &p,
        &values(json!({"model": "m", "proxy": "p"})),
        &Json,
        &mut none,
    )
    .unwrap();
    // The person's edits: a new key and their own model.
    std::fs::write(
        &target,
        "{\"editor\": \"vim\", \"model\": \"mine\", \"proxy\": \"p\", \"theme\": \"light\"}\n",
    )
    .unwrap();
    let pv = tx.preview("s", &sel).unwrap();
    assert_eq!(
        (pv["applied"].clone(), pv["edited"].clone()),
        (json!(true), json!([true, false]))
    );
    let p = pins(&tx, "s", &sel);
    let o = tx.clear("s", &p, &Json, &mut none).unwrap();
    assert_eq!(o["outcome"], "cleared");
    assert_eq!(
        read(&target).unwrap(),
        "{\n  \"editor\": \"vim\",\n  \"model\": \"mine\",\n  \"theme\": \"light\"\n}\n"
    );
    assert!(!created.exists(), "clear left the file apply created");
    assert_eq!(tx.status().unwrap()["sets"], json!({}));
    assert!(
        mem.backups.borrow().is_empty(),
        "backups left: {:?}",
        mem.backups.borrow()
    );
    // Restore: the exact original bytes, after a re-apply that promotes the person's edits.
    std::fs::write(&target, "{\"theme\": \"light\"}\n").unwrap();
    let p = pins(&tx, "s", &sel);
    tx.apply("s", &p, &values(json!({"model": "m"})), &Json, &mut none)
        .unwrap();
    let p = pins(&tx, "s", &sel);
    tx.apply("s", &p, &values(json!({"model": "m2"})), &Json, &mut none)
        .unwrap();
    let p = pins(&tx, "s", &sel);
    let o = tx.restore("s", &p, &mut none).unwrap();
    assert_eq!(o["outcome"], "restored");
    assert_eq!(read(&target).unwrap(), "{\"theme\": \"light\"}\n");
    assert!(!created.exists());
    // Not applied any more.
    let p = pins(&tx, "s", &[Selection::File(link)]);
    assert!(tx
        .clear("s", &p, &Json, &mut none)
        .unwrap_err()
        .message
        .contains("not applied"));
}

#[test]
fn a_failed_write_rolls_back_but_never_over_a_competing_edit() {
    let t = Tmp::new();
    let a = t.put("one/a.json", "{}\n");
    let b = t.put("two/b.json", "{}\n");
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let sel = [Selection::File(a.clone()), Selection::File(b.clone())];
    let ro = |mode| {
        std::fs::set_permissions(t.at("two"), std::fs::Permissions::from_mode(mode)).unwrap()
    };
    // The second file's folder cannot be written: the first is put back.
    let p = pins(&tx, "s", &sel);
    ro(0o555);
    let e = tx
        .apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap_err();
    ro(0o755);
    assert!(e.message.contains("rolled back"), "{e}");
    assert_eq!(
        (read(&a).unwrap(), read(&b).unwrap()),
        ("{}\n".into(), "{}\n".into())
    );
    let (_, (_, outcome)) = mem
        .journals
        .borrow()
        .iter()
        .next()
        .map(|(k, v)| (k.clone(), v.clone()))
        .unwrap();
    assert_eq!(outcome.as_deref(), Some("rolled_back"));
    // Another program writes the first file once it is written: it is left as that program left it.
    let p = pins(&tx, "s", &sel);
    ro(0o555);
    let e = tx
        .apply("s", &p, &values(json!({"x": 1})), &Json, &mut |_| {
            std::fs::write(&a, "{\"theirs\": 1}\n").unwrap()
        })
        .unwrap_err();
    ro(0o755);
    assert!(
        e.message.contains("rolled back") && e.message.contains("another program"),
        "{e}"
    );
    assert_eq!(read(&a).unwrap(), "{\"theirs\": 1}\n");
}

/// Runs an apply that "crashes" (panics) once written(i) is called for crash; the open journal.
fn crash(
    tx: &Transactions,
    mem: &Mem,
    set: &str,
    sel: &[Selection],
    crash: usize,
    v: Value,
) -> Value {
    let p = pins(tx, set, sel);
    let r = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        tx.apply(set, &p, &values(v), &Json, &mut |i| {
            if i == crash {
                panic!("crash")
            }
        })
    }));
    assert!(r.is_err());
    let open = mem.open();
    assert_eq!(open.len(), 1);
    open[0].clone()
}

#[test]
fn reconcile_rolls_back_commits_and_keeps_competing_edits() {
    let t = Tmp::new();
    let a = t.put("one/a.json", "{}\n");
    let b = t.at("two/b.json");
    std::fs::create_dir_all(b.parent().unwrap()).unwrap();
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let sel = [Selection::File(a.clone()), Selection::File(b.clone())];

    // Crashed after the first of two: rolled back from the backup.
    let j = crash(&tx, &mem, "s", &sel, 0, json!({"x": 1}));
    assert_eq!(read(&a).unwrap(), "{\n  \"x\": 1\n}\n");
    let r = tx.reconcile(std::slice::from_ref(&j)).unwrap();
    assert_eq!(r[0]["outcome"], "rolled_back");
    assert_eq!(r[0]["restored"], json!([a]));
    assert_eq!((read(&a).unwrap(), b.exists()), ("{}\n".into(), false));
    assert_eq!(mem.outcome(&j["id"]).as_deref(), Some("rolled_back"));
    // Delivered again: the same answer, nothing written.
    assert_eq!(
        tx.reconcile(std::slice::from_ref(&j)).unwrap()[0]["outcome"],
        "rolled_back"
    );

    // A competing edit after the crash is kept.
    let j = crash(&tx, &mem, "s", &sel, 0, json!({"x": 2}));
    std::fs::write(&a, "{\"theirs\": 1}\n").unwrap();
    let r = tx.reconcile(&[j]).unwrap();
    assert_eq!(
        (r[0]["outcome"].clone(), r[0]["conflicts"].clone()),
        (json!("rolled_back"), json!([a]))
    );
    assert_eq!(read(&a).unwrap(), "{\"theirs\": 1}\n");
    std::fs::write(&a, "{}\n").unwrap();

    // Crashed after both: committed, and its record kept, so clear undoes it.
    let j = crash(&tx, &mem, "s", &sel, 1, json!({"x": 3}));
    let r = tx.reconcile(std::slice::from_ref(&j)).unwrap();
    assert_eq!(r[0]["outcome"], "committed");
    assert_eq!(mem.outcome(&j["id"]).as_deref(), Some("committed"));
    assert!(tx.status().unwrap()["sets"]["s"].is_object());
    assert_eq!(
        tx.status().unwrap()["recovered"].as_array().unwrap().len(),
        3
    );
    let p = pins(&tx, "s", &sel);
    tx.clear("s", &p, &Json, &mut none).unwrap();
    assert_eq!((read(&a).unwrap(), b.exists()), ("{}\n".into(), false));
}

#[test]
fn a_write_whose_record_was_saved_is_committed_even_if_the_files_changed_since() {
    let t = Tmp::new();
    let a = t.put("a.json", "{}\n");
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let p = pins(&tx, "s", &[Selection::File(a.clone())]);
    tx.apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap();
    // As if journal.commit had not reached the vault: the journal is open, the state names it.
    let id = mem.journals.borrow().keys().next().unwrap().clone();
    let intent = mem.journals.borrow()[&id].0.clone();
    mem.journals.borrow_mut().get_mut(&id).unwrap().1 = None;
    std::fs::write(&a, "{\"later\": 1}\n").unwrap();
    let r = tx
        .reconcile(&[json!({"id": id, "intent": intent})])
        .unwrap();
    assert_eq!(r[0]["outcome"], "committed");
    assert_eq!(read(&a).unwrap(), "{\"later\": 1}\n");
}

/// A setup of two files in two folders, with a vault and data folder.
fn two(t: &Tmp) -> (PathBuf, PathBuf, Mem, PathBuf) {
    let a = t.put("one/a.json", "{}\n");
    let b = t.put("two/b.json", "{}\n");
    let data = t.at("data");
    std::fs::create_dir(&data).unwrap();
    (a, b, Mem::default(), data)
}

fn chmod(p: &Path, mode: u32) {
    std::fs::set_permissions(p, std::fs::Permissions::from_mode(mode)).unwrap();
}

fn mode_of(p: &Path) -> u32 {
    std::fs::metadata(p).unwrap().mode() & 0o7777
}

/// Finding 1: a conflict's original survives the next operation's sweep and restore puts it back.
#[test]
fn a_conflicts_original_survives_and_restore_returns_it() {
    let t = Tmp::new();
    let (a, b, mem, data) = two(&t);
    let other = t.put("three/c.json", "{}\n");
    let tx = Transactions::new(&mem, &data);
    let sel = [Selection::File(a.clone()), Selection::File(b.clone())];
    // In a call: the second file fails, the first was changed by another program meanwhile.
    let p = pins(&tx, "s", &sel);
    chmod(&t.at("two"), 0o555);
    let e = tx
        .apply("s", &p, &values(json!({"x": 1})), &Json, &mut |_| {
            std::fs::write(&a, "{\"theirs\": 1}\n").unwrap()
        })
        .unwrap_err();
    chmod(&t.at("two"), 0o755);
    assert!(e.message.contains("restore still has its original"), "{e}");
    // After a crash: reconcile leaves another program's edit and keeps the original too.
    let j = crash(
        &tx,
        &mem,
        "r",
        &[Selection::File(b.clone())],
        0,
        json!({"y": 1}),
    );
    std::fs::write(&b, "{\"theirs\": 2}\n").unwrap();
    assert_eq!(tx.reconcile(&[j]).unwrap()[0]["conflicts"], json!([b]));
    assert_eq!(tx.status().unwrap()["kept"].as_array().unwrap().len(), 2);
    // Another operation sweeps unreferenced backups.
    let po = pins(&tx, "o", &[Selection::File(other.clone())]);
    tx.apply("o", &po, &values(json!({"z": 1})), &Json, &mut none)
        .unwrap();
    // Restore reaches each kept original after a new preview, and then forgets it.
    let p = pins(&tx, "s", &[Selection::File(a.clone())]);
    assert_eq!(
        tx.restore("s", &p, &mut none).unwrap()["outcome"],
        "restored"
    );
    assert_eq!(read(&a).unwrap(), "{}\n");
    let p = pins(&tx, "r", &[Selection::File(b.clone())]);
    tx.restore("r", &p, &mut none).unwrap();
    assert_eq!(read(&b).unwrap(), "{}\n");
    assert_eq!(tx.status().unwrap()["kept"], json!([]));
    // Only set o's images stay.
    let labels: Vec<String> = mem
        .backups
        .borrow()
        .values()
        .map(|(l, _)| l.clone())
        .collect();
    assert_eq!(labels, ["tx original", "tx applied"]);
}

/// Finding 2: an operation after a failed put-back keeps the open journal's backups, and a
/// missing backup makes reconcile report a conflict instead of failing the start.
#[test]
fn a_failed_put_back_keeps_its_backups_for_reconcile() {
    let t = Tmp::new();
    let (a, b, mem, data) = two(&t);
    let tx = Transactions::new(&mem, &data);
    // Set o, applied, whose restore then changes nothing: a run with no writes, which sweeps.
    let c = t.put("three/c.json", "{}\n");
    let po = pins(&tx, "o", &[Selection::File(c.clone())]);
    tx.apply("o", &po, &values(json!({"z": 1})), &Json, &mut none)
        .unwrap();
    std::fs::write(&c, "{}\n").unwrap();
    let sel = [Selection::File(a.clone()), Selection::File(b.clone())];
    let p = pins(&tx, "s", &sel);
    chmod(&t.at("two"), 0o555);
    // Once a is written its folder becomes read-only too: putting it back fails.
    let e = tx
        .apply("s", &p, &values(json!({"x": 1})), &Json, &mut |_| {
            chmod(&t.at("one"), 0o555)
        })
        .unwrap_err();
    chmod(&t.at("one"), 0o755);
    chmod(&t.at("two"), 0o755);
    assert!(e.message.contains("next start reconciles it"), "{e}");
    let open = mem.open();
    assert_eq!(open.len(), 1);
    let po = pins(&tx, "o", &[Selection::File(c.clone())]);
    tx.restore("o", &po, &mut none).unwrap();
    let r = tx.reconcile(&open).unwrap();
    assert_eq!(
        (r[0]["outcome"].clone(), r[0]["restored"].clone()),
        (json!("rolled_back"), json!([a]))
    );
    assert_eq!(read(&a).unwrap(), "{}\n");
    // A journal whose backup is gone: a conflict, the start goes on.
    // Crashed after the first of two (a single file written would be a finished write).
    let j = crash(
        &tx,
        &mem,
        "s",
        &[Selection::File(a.clone()), Selection::File(b.clone())],
        0,
        json!({"x": 2}),
    );
    let backup = j["intent"]["files"][0]["backup"]
        .as_str()
        .unwrap()
        .to_string();
    mem.backups.borrow_mut().remove(&backup);
    let r = tx.reconcile(&[j]).unwrap();
    assert_eq!(r[0]["conflicts"], json!([a]));
}

/// Finding 3: after a crash, a link made again (a new link, same text) does not turn the
/// writer's own bytes into a conflict: reconcile looks at the pinned target itself.
#[test]
fn reconcile_looks_at_the_pinned_target_when_the_link_was_made_again() {
    let t = Tmp::new();
    let (link, target) = stow(&t);
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let remake = || {
        std::fs::hard_link(
            &link,
            t.at(&format!("held-{}", target::random_hex().unwrap())),
        )
        .unwrap();
        let tmp = link.with_extension("new");
        std::os::unix::fs::symlink("../../dotfiles/x/settings.json", &tmp).unwrap();
        std::fs::rename(&tmp, &link).unwrap();
    };
    let sel = [Selection::File(link.clone())];
    let j = crash(&tx, &mem, "s", &sel, 0, json!({"m": 1}));
    remake();
    let r = tx.reconcile(&[j]).unwrap();
    assert_eq!(
        (r[0]["outcome"].clone(), r[0]["conflicts"].clone()),
        (json!("committed"), json!([]))
    );
    assert!(tx.status().unwrap()["sets"]["s"].is_object());
    // Rolled back the same way: the second file was never written.
    let other = t.put("dotfiles/x/other.json", "{}\n");
    let p = pins(&tx, "s", &sel);
    tx.clear("s", &p, &Json, &mut none).unwrap();
    let j = crash(
        &tx,
        &mem,
        "s",
        &[Selection::File(link.clone()), Selection::File(other)],
        0,
        json!({"m": 2}),
    );
    remake();
    let r = tx.reconcile(&[j]).unwrap();
    assert_eq!(
        (r[0]["restored"].clone(), r[0]["conflicts"].clone()),
        (json!([target]), json!([]))
    );
    assert_eq!(read(&target).unwrap(), "{\"theme\": \"light\"}\n");
}

/// Finding 4: a link pointed elsewhere does not wedge its set: clear or restore through the
/// recorded target itself, or apply again through the link (the old originals kept).
#[test]
fn a_retargeted_link_leaves_a_way_back() {
    let t = Tmp::new();
    let (link, target) = stow(&t);
    let alt = t.put("dotfiles/alt.json", "{\"alt\": true}\n");
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let relink = |to: &Path| {
        let tmp = link.with_extension("new");
        std::os::unix::fs::symlink(to, &tmp).unwrap();
        std::fs::rename(&tmp, &link).unwrap();
    };
    let p = pins(&tx, "s", &[Selection::File(link.clone())]);
    tx.apply("s", &p, &values(json!({"m": 1})), &Json, &mut none)
        .unwrap();
    relink(&alt);
    // Clear through the recorded target file, named itself.
    let p = pins(&tx, "s", &[Selection::File(target.clone())]);
    assert!(tx.preview("s", &[Selection::File(target.clone())]).unwrap()["applied"] == true);
    tx.clear("s", &p, &Json, &mut none).unwrap();
    assert_eq!(read(&target).unwrap(), "{\"theme\": \"light\"}\n");
    // Apply through the link again, then retarget it: a new apply replaces the stale record and
    // keeps its original, which restore of the old target then puts back.
    relink(Path::new("../../dotfiles/x/settings.json"));
    let p = pins(&tx, "s", &[Selection::File(link.clone())]);
    tx.apply("s", &p, &values(json!({"m": 2})), &Json, &mut none)
        .unwrap();
    relink(&alt);
    let p = pins(&tx, "s", &[Selection::File(link.clone())]);
    tx.apply("s", &p, &values(json!({"m": 3})), &Json, &mut none)
        .unwrap();
    assert_eq!(read(&alt).unwrap(), "{\n  \"alt\": true,\n  \"m\": 3\n}\n");
    let p = pins(&tx, "s", &[Selection::File(target.clone())]);
    tx.restore("s", &p, &mut none).unwrap();
    assert_eq!(read(&target).unwrap(), "{\"theme\": \"light\"}\n");
    // A record whose link still resolves is not replaced.
    let p = pins(&tx, "s", &[Selection::File(target.clone())]);
    let e = tx
        .apply("s", &p, &values(json!({"m": 4})), &Json, &mut none)
        .unwrap_err();
    assert!(e.message.contains("applied to other targets"), "{e}");
}

/// Finding 5: temporary files are written 0600, and reconcile removes those a crash left.
#[test]
fn temporary_files_are_private_and_reconcile_removes_them() {
    let t = Tmp::new();
    let (a, _, mem, data) = two(&t);
    chmod(&a, 0o644);
    let tx = Transactions::new(&mem, &data);
    let target = Target::resolve(&Selection::File(a.clone())).unwrap();
    let tmp = Path::new(".a.json.pluxy-0123456789abcdef.tmp");
    target.write_temp(tmp, b"secret").unwrap();
    assert_eq!(mode_of(&t.at("one").join(tmp)), 0o600);
    let unrelated = t.put("one/.a.json.pluxy-notours.tmp", "x");
    let j = crash(
        &tx,
        &mem,
        "s",
        &[
            Selection::File(a.clone()),
            Selection::File(t.at("two/b.json")),
        ],
        0,
        json!({"x": 1}),
    );
    tx.reconcile(&[j]).unwrap();
    assert!(
        !t.at("one").join(tmp).exists(),
        "the temporary file was left"
    );
    assert!(unrelated.exists());
    // The written file keeps the target's mode.
    let p = pins(&tx, "s", &[Selection::File(a.clone())]);
    tx.apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap();
    assert_eq!(mode_of(&a), 0o644);
}

/// Finding 6: the person's key order and numbers' digits are kept.
#[test]
fn key_order_and_number_digits_are_kept() {
    let t = Tmp::new();
    let src = "{\"zeta\": 123456789012345678901234567890, \"alpha\": 0.10000000000000000555111512312578270211815834045, \"mid\": {\"b\": 1, \"a\": 2}}\n";
    let a = t.put("a.json", src);
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let p = pins(&tx, "s", &[Selection::File(a.clone())]);
    tx.apply("s", &p, &values(json!({"new": 1})), &Json, &mut none)
        .unwrap();
    assert_eq!(
        read(&a).unwrap(),
        "{\n  \"zeta\": 123456789012345678901234567890,\n  \"alpha\": 0.10000000000000000555111512312578270211815834045,\n  \"mid\": {\n    \"b\": 1,\n    \"a\": 2\n  },\n  \"new\": 1\n}\n"
    );
    std::fs::write(
        &a,
        read(&a)
            .unwrap()
            .replace("\"mid\"", "\"edited\": true,\n  \"mid\""),
    )
    .unwrap();
    let p = pins(&tx, "s", &[Selection::File(a.clone())]);
    tx.clear("s", &p, &Json, &mut none).unwrap();
    assert_eq!(
        read(&a).unwrap(),
        "{\n  \"zeta\": 123456789012345678901234567890,\n  \"alpha\": 0.10000000000000000555111512312578270211815834045,\n  \"edited\": true,\n  \"mid\": {\n    \"b\": 1,\n    \"a\": 2\n  }\n}\n"
    );
}

/// Finding 7: setuid and setgid are not copied, and a removed file comes back with its own mode.
#[test]
fn modes_are_masked_and_kept_through_removal() {
    let t = Tmp::new();
    let (a, b, mem, data) = two(&t);
    chmod(&a, 0o6755);
    let tx = Transactions::new(&mem, &data);
    let p = pins(&tx, "s", &[Selection::File(a.clone())]);
    tx.apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap();
    assert_eq!(mode_of(&a), 0o755);
    // x did not exist: apply creates it (0600), the person makes it 0644; a restore that removes
    // it, then fails on b, puts it back 0644.
    let x = t.at("one/x.json");
    let p = pins(
        &tx,
        "n",
        &[Selection::File(x.clone()), Selection::File(b.clone())],
    );
    tx.apply("n", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap();
    assert_eq!(mode_of(&x), 0o600);
    chmod(&x, 0o644);
    let p = pins(
        &tx,
        "n",
        &[Selection::File(x.clone()), Selection::File(b.clone())],
    );
    chmod(&t.at("two"), 0o555);
    let e = tx.restore("n", &p, &mut none).unwrap_err();
    chmod(&t.at("two"), 0o755);
    assert!(e.message.contains("rolled back"), "{e}");
    assert_eq!(mode_of(&x), 0o644);
    // Restore after the person removed an original file brings it back with its original mode.
    chmod(&a, 0o640);
    let p = pins(&tx, "m", &[Selection::File(a.clone())]);
    tx.apply("m", &p, &values(json!({"y": 1})), &Json, &mut none)
        .unwrap();
    std::fs::remove_file(&a).unwrap();
    let p = pins(&tx, "m", &[Selection::File(a.clone())]);
    tx.restore("m", &p, &mut none).unwrap();
    assert_eq!(mode_of(&a), 0o640);
}

/// Finding 9: a backup answer without its bytes fails the operation; nothing is written.
#[test]
fn a_backup_without_its_bytes_is_an_error() {
    let t = Tmp::new();
    let (a, _, mem, data) = two(&t);
    let tx = Transactions::new(&mem, &data);
    let p = pins(&tx, "s", &[Selection::File(a.clone())]);
    tx.apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap();
    let applied = read(&a).unwrap();
    mem.no_data.set(true);
    let p = pins(&tx, "s", &[Selection::File(a.clone())]);
    let e = tx.restore("s", &p, &mut none).unwrap_err();
    assert!(e.message.contains("without its bytes"), "{e}");
    assert_eq!(read(&a).unwrap(), applied);
}

/// Finding 10: a pin with any field it does not write is refused, and the fixture writer's
/// manifest uses the library's strict pin schema.
#[test]
fn pins_are_strict() {
    let t = Tmp::new();
    let a = t.put("a.json", "{}\n");
    let pin = Target::resolve(&Selection::File(a)).unwrap().pin.to_json();
    assert!(Pin::from_json(&pin).is_ok());
    let mut extra = pin.clone();
    extra["note"] = json!("approve");
    assert!(Pin::from_json(&extra)
        .unwrap_err()
        .message
        .contains("unknown field"));
    let mut deep = pin.clone();
    deep["identity"]["note"] = json!("x");
    assert!(Pin::from_json(&deep).is_err());
    let manifest: Value = serde_json::from_str(include_str!(
        "../examples/fixture-writer/pluxy-extension.json"
    ))
    .unwrap();
    for op in manifest["app"]["operations"].as_array().unwrap() {
        if ["apply", "clear", "restore"].contains(&op["id"].as_str().unwrap()) {
            assert_eq!(
                op["input_schema"]["properties"]["targets"]["items"],
                pin_schema(),
                "{}",
                op["id"]
            );
            assert!(op["description"].as_str().unwrap().contains("may be lost"));
        }
    }
}
