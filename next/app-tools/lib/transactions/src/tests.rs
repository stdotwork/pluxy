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
        "{\n  \"model\": \"m\",\n  \"theme\": \"light\"\n}\n"
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
    relink(&alt);
    let e = tx.apply("s", &p, &v, &Json, &mut none).unwrap_err();
    assert!(e.message.contains("changed"), "{e}");
    // Back to the same text, but another link: still refused (its identity is pinned).
    relink(Path::new("../../dotfiles/x/settings.json"));
    let e = tx.apply("s", &p, &v, &Json, &mut none).unwrap_err();
    assert!(e.message.contains("changed"), "{e}");
    // Replaced by rename with the same bytes.
    let p = pins(&tx, "s", &[Selection::File(link.clone())]);
    let tmp = target.with_extension("editor");
    std::fs::copy(&target, &tmp).unwrap();
    std::fs::rename(&tmp, &target).unwrap();
    let e = tx.apply("s", &p, &v, &Json, &mut none).unwrap_err();
    assert!(e.message.contains("changed"), "{e}");
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
