//! Unit tests on real files in temporary folders, with an in-memory stand-in for the host's vault
//! (the native test drives the real one through the fixture writer).

use super::*;
use std::cell::{Cell, RefCell};
use std::collections::BTreeMap;
#[cfg(unix)]
use std::os::unix::fs::MetadataExt as _;
#[cfg(target_os = "linux")]
use std::os::unix::fs::PermissionsExt as _;

/// The vault's frames as the host answers them, in memory.
#[derive(Default)]
struct Mem {
    backups: RefCell<BTreeMap<String, (String, String)>>,
    journals: RefCell<BTreeMap<String, (Value, Option<String>)>>,
    next: Cell<u64>,
    /// Answers backup.get without the bytes (a broken vault).
    no_data: Cell<bool>,
    /// backup.put fails once this many have worked (None: never).
    puts_left: Cell<Option<usize>>,
    /// journal.end fails (the vault cannot be reached once the state was saved).
    fail_end: Cell<bool>,
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
                if let Some(n) = self.puts_left.get() {
                    if n == 0 {
                        return Err(Error::unavailable("the vault is full"));
                    }
                    self.puts_left.set(Some(n - 1));
                }
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
            "journal.end" if self.fail_end.get() => Err(Error::unavailable("no vault")),
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
    /// rel, `/`-separated, under the folder: part by part, since a long-form Windows path
    /// (`\\?\…`, as the folder's canonical path is there) takes `/` as a letter.
    fn at(&self, rel: &str) -> PathBuf {
        let mut p = self.0.clone();
        p.extend(rel.split('/'));
        p
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
        // A folder a failed test left unwritable (on Windows a deny entry does not stop removal).
        #[cfg(unix)]
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

/// A link's own identity and its text.
fn link_of(p: &Path) -> (pluxy_files::Id, PathBuf) {
    (
        pluxy_files::id_of_path(p).unwrap(),
        std::fs::read_link(p).unwrap(),
    )
}

/// The stow layout's link text, in the platform's own separator (Windows does not take `/` in a
/// link's text as one).
#[cfg(unix)]
const REL: &str = "../../dotfiles/x/settings.json";
#[cfg(windows)]
const REL: &str = r"..\..\dotfiles\x\settings.json";

/// A symbolic link at `at` to target: on Windows a folder link when target, read from the link's
/// folder, is a folder.
fn make_link(target: &Path, at: &Path) {
    #[cfg(unix)]
    std::os::unix::fs::symlink(target, at).unwrap();
    #[cfg(windows)]
    if at.parent().unwrap().join(target).is_dir() {
        std::os::windows::fs::symlink_dir(target, at).unwrap();
    } else {
        std::os::windows::fs::symlink_file(target, at).unwrap();
    }
}

/// Whether this account may make symbolic links (on Windows that takes a privilege or Developer
/// Mode). When it may not, says so on stderr: a test that needs links is then skipped, and the CI
/// summary counts the skip.
fn links_allowed() -> bool {
    #[cfg(unix)]
    return true;
    #[cfg(windows)]
    {
        static ALLOWED: std::sync::OnceLock<bool> = std::sync::OnceLock::new();
        let allowed = *ALLOWED.get_or_init(|| {
            let probe = std::env::temp_dir()
                .join(format!("pluxy-tx-links-{}", target::random_hex().unwrap()));
            let made = std::os::windows::fs::symlink_file("target", &probe).is_ok();
            let _ = std::fs::remove_file(&probe);
            made
        });
        if !allowed {
            eprintln!("skipped: this account cannot make a symbolic link");
        }
        allowed
    }
}

/// Windows: a junction at `at` to the folder target, a folder link any account may make.
#[cfg(windows)]
fn junction(target: &Path, at: &Path) {
    let out = std::process::Command::new("cmd")
        .args(["/C", "mklink", "/J"])
        .arg(plain(at))
        .arg(plain(target))
        .output()
        .unwrap();
    assert!(out.status.success(), "{out:?}");
}

/// p without the long-form prefix (`\\?\C:\…` as `C:\…`), as Windows reads a link's absolute
/// text back; p itself elsewhere.
fn plain(p: &Path) -> PathBuf {
    let s = p.to_str().unwrap();
    let short = s
        .strip_prefix(r"\\?\")
        .filter(|r| r.as_bytes().get(1) == Some(&b':'));
    PathBuf::from(short.unwrap_or(s))
}

/// Gives p permissions by Unix mode bits. On Windows a file gets a protected access list allowing
/// this user and SYSTEM everything, and reading to the Users group for the group's read bit and to
/// Everyone for others'; a folder without the owner's write bit gets an entry denying everyone
/// adding to it, which a mode with that bit takes away.
fn chmod(p: &Path, mode: u32) {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(p, std::fs::Permissions::from_mode(mode)).unwrap();
    }
    #[cfg(windows)]
    {
        if p.is_dir() {
            let mut icacls = std::process::Command::new("icacls");
            icacls.arg(plain(p));
            if mode & 0o200 == 0 {
                icacls.args(["/deny", "*S-1-1-0:(WD,AD)"]);
            } else {
                icacls.args(["/remove:d", "*S-1-1-0"]);
            }
            let out = icacls.output().unwrap();
            assert!(out.status.success(), "{out:?}");
            return;
        }
        use std::os::windows::fs::OpenOptionsExt;
        use windows_sys::Win32::Foundation::GENERIC_READ;
        use windows_sys::Win32::Storage::FileSystem::WRITE_DAC;
        let f = std::fs::OpenOptions::new()
            .access_mode(GENERIC_READ | WRITE_DAC)
            .open(p)
            .unwrap();
        pluxy_files::make_private_open(&f).unwrap();
        let mut sddl = pluxy_files::access_list(&f).unwrap();
        if mode & 0o040 != 0 {
            sddl.push_str("(A;;FR;;;BU)");
        }
        if mode & 0o004 != 0 {
            sddl.push_str("(A;;FR;;;WD)");
        }
        pluxy_files::set_access_list(&f, &sddl).unwrap();
    }
}

/// The file p's permissions as Unix mode bits: on Windows read back from an access list [`chmod`]
/// or the library gave (owner-only is this user's and SYSTEM's entries alone, 0600); any other
/// list fails the test.
fn mode_of(p: &Path) -> u32 {
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt;
        std::fs::metadata(p).unwrap().mode() & 0o7777
    }
    #[cfg(windows)]
    {
        let sddl = pluxy_files::access_list(&std::fs::File::open(p).unwrap()).unwrap();
        let (flags, entries) = sddl
            .strip_prefix("D:")
            .and_then(|s| s.split_once('('))
            .unwrap_or_else(|| panic!("{sddl}"));
        assert!(flags.contains('P'), "not protected: {sddl}");
        let mut mode = 0;
        for entry in entries.trim_end_matches(')').split(")(") {
            match entry.split(';').collect::<Vec<_>>()[..] {
                ["A", "", "FA", "", "", "SY"] => {}
                ["A", "", "FA", "", "", _] => mode |= 0o600,
                ["A", "", "FR", "", "", "BU"] => mode |= 0o040,
                ["A", "", "FR", "", "", "WD"] => mode |= 0o004,
                _ => panic!("an entry these tests do not give: {sddl}"),
            }
        }
        mode
    }
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
    chmod(&target, 0o640);
    let link = t.at(".config/x/settings.json");
    std::fs::create_dir_all(link.parent().unwrap()).unwrap();
    make_link(Path::new(REL), &link);
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
    if !links_allowed() {
        return;
    }
    let t = Tmp::new();
    let (link, target) = stow(&t);
    let other = t.put("dotfiles/x/other.json", "{}\n");
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let before = link_of(&link);
    let p = tx.preview("s", &[Selection::File(link.clone())]).unwrap();
    assert_eq!(p["targets"][0]["link"], REL);
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
    // An apply's file is owner-only: the group's read bit goes.
    assert_eq!(mode_of(&target), 0o600);
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
    if links_allowed() {
        root_link(make_link, true);
    }
}

/// Windows: the same with junctions, the folder links any account may make, for the root and the
/// folder link inside it (a file link inside it only where this account may make one).
#[cfg(windows)]
#[test]
fn a_root_junction_is_followed_but_junctions_inside_it_are_refused() {
    root_link(junction, links_allowed());
}

/// A root that is a folder link made by dir_link, with a folder link inside it, and a file link
/// too when file_link.
fn root_link(dir_link: fn(&Path, &Path), file_link: bool) {
    let t = Tmp::new();
    let real = t
        .put("share/root/a.json", "{}\n")
        .parent()
        .unwrap()
        .to_path_buf();
    let outside = t.put("outside.json", "{\"o\": 1}\n");
    if file_link {
        make_link(&outside, &real.join("inner.json"));
    }
    std::fs::create_dir(real.join("sub")).unwrap();
    dir_link(&t.at("share"), &real.join("sub").join("up"));
    let root = t.at("root-link");
    dir_link(&real, &root);
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
        ("a\\b.json", "backslash"),
        ("..\\outside.json", "backslash"),
        ("sub\\up\\x.json", "backslash"),
    ] {
        if p == "inner.json" && !file_link {
            continue;
        }
        let e = tx.preview("r", &[sel(p)]).unwrap_err();
        assert!(e.message.contains(says), "{p}: {e}");
    }
    let pins = pins(&tx, "r", &[sel("a.json"), sel("sub/b.json")]);
    assert_eq!(
        pins[0].link.as_deref().map(|l| plain(Path::new(l))),
        Some(plain(&real))
    );
    tx.apply("r", &pins, &values(json!({"m": true})), &Json, &mut none)
        .unwrap();
    let b = real.join("sub").join("b.json");
    assert_eq!(read(&real.join("a.json")).unwrap(), "{\n  \"m\": true\n}\n");
    assert_eq!(read(&b).unwrap(), "{\n  \"m\": true\n}\n");
    assert_eq!(mode_of(&b), 0o600);
    assert_eq!(read(&outside).unwrap(), "{\"o\": 1}\n");
    assert_eq!(plain(&std::fs::read_link(&root).unwrap()), plain(&real));
}

#[test]
fn retargeted_replaced_or_modified_targets_are_refused() {
    if !links_allowed() {
        return;
    }
    let t = Tmp::new();
    let (link, target) = stow(&t);
    let alt = t.put("dotfiles/alt.json", "{}\n");
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let v = values(json!({"x": 1}));
    let relink = |to: &Path| {
        let tmp = link.with_extension("new");
        make_link(to, &tmp);
        std::fs::rename(&tmp, &link).unwrap();
    };
    // Pointed elsewhere.
    let p = pins(&tx, "s", &[Selection::File(link.clone())]);
    let original = link_of(&link).0;
    #[cfg(unix)]
    std::fs::hard_link(&link, t.at("held-original")).unwrap();
    relink(&alt);
    let e = tx.apply("s", &p, &v, &Json, &mut none).unwrap_err();
    assert!(e.message.contains("changed"), "{e}");
    // Back to the same text, but another link: still refused (its identity is pinned). On Linux and
    // macOS the earlier links' inodes are kept in use (hard links to them), so the new link cannot
    // get their numbers: a filesystem that reuses a freed inode number (ext4 does) could otherwise
    // give the new link the old identity, and then nothing tells the two apart (same text, same
    // target, same bytes). On Windows (NTFS) a file ID carries a sequence number that changes when
    // the ID's record is used again.
    let alt_link = link_of(&link).0;
    #[cfg(unix)]
    std::fs::hard_link(&link, t.at("held-alt")).unwrap();
    relink(Path::new(REL));
    assert!(![original, alt_link].contains(&link_of(&link).0));
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
    if !links_allowed() {
        return;
    }
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
    let ro = |mode| chmod(&t.at("two"), mode);
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
    if !links_allowed() {
        return;
    }
    let t = Tmp::new();
    let (link, target) = stow(&t);
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let remake = || {
        // The old link held, so the new one gets another identity (see the test above).
        #[cfg(unix)]
        std::fs::hard_link(
            &link,
            t.at(&format!("held-{}", target::random_hex().unwrap())),
        )
        .unwrap();
        let tmp = link.with_extension("new");
        make_link(Path::new(REL), &tmp);
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
    if !links_allowed() {
        return;
    }
    let t = Tmp::new();
    let (link, target) = stow(&t);
    let alt = t.put("dotfiles/alt.json", "{\"alt\": true}\n");
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let relink = |to: &Path| {
        let tmp = link.with_extension("new");
        make_link(to, &tmp);
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
    relink(Path::new(REL));
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
    // The written file keeps the target's mode for the owner only.
    let p = pins(&tx, "s", &[Selection::File(a.clone())]);
    tx.apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap();
    assert_eq!(mode_of(&a), 0o600);
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

/// Finding 7: setuid and setgid are not copied, and a removed file comes back with its own mode
/// (on Windows its access list; the execute bits have no twin there).
#[test]
fn modes_are_masked_and_kept_through_removal() {
    let t = Tmp::new();
    let (a, b, mem, data) = two(&t);
    chmod(&a, 0o6755);
    let tx = Transactions::new(&mem, &data);
    let p = pins(&tx, "s", &[Selection::File(a.clone())]);
    tx.apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap();
    assert_eq!(mode_of(&a), if cfg!(unix) { 0o700 } else { 0o600 });
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

/// An apply writes a writer's values, a key among them: the file it writes is owner-only, an
/// existing file's group and other bits taken away; clear and restore keep the mode it has then.
#[test]
fn apply_makes_the_file_owner_only_and_clear_keeps_that() {
    let t = Tmp::new();
    let (a, _, mem, data) = two(&t);
    chmod(&a, 0o644);
    let tx = Transactions::new(&mem, &data);
    let p = pins(&tx, "s", &[Selection::File(a.clone())]);
    tx.apply("s", &p, &values(json!({"key": "k"})), &Json, &mut none)
        .unwrap();
    assert_eq!(mode_of(&a), 0o600);
    // The person's edit, so clear writes the file again.
    let now = read(&a).unwrap();
    std::fs::write(
        &a,
        now.replace("\"key\": \"k\"", "\"key\": \"k\", \"mine\": 1"),
    )
    .unwrap();
    let p = pins(&tx, "s", &[Selection::File(a.clone())]);
    let o = tx.clear("s", &p, &Json, &mut none).unwrap();
    assert_eq!(o["outcome"], "cleared");
    assert!(!read(&a).unwrap().contains("\"key\""));
    assert_eq!(mode_of(&a), 0o600);
    // Restore puts the bytes back and keeps the mode the file has.
    let p = pins(&tx, "s", &[Selection::File(a.clone())]);
    tx.apply("s", &p, &values(json!({"key": "k"})), &Json, &mut none)
        .unwrap();
    chmod(&a, 0o640);
    let p = pins(&tx, "s", &[Selection::File(a.clone())]);
    tx.restore("s", &p, &mut none).unwrap();
    assert_eq!(mode_of(&a), 0o640);
}

/// Finding 9: a backup answer without its bytes fails the operation; nothing is written.
#[test]
fn a_rolled_back_apply_leaves_the_mode_as_it_was() {
    // An apply makes the file owner-only; one rolled back, in the call or by recovery after a
    // crash, puts back the mode the file had as well as its bytes.
    let t = Tmp::new();
    let (a, _, mem, data) = two(&t);
    chmod(&a, 0o644);
    let tx = Transactions::new(&mem, &data);
    let sel = [
        Selection::File(a.clone()),
        Selection::File(t.at("two/b.json")),
    ];
    let p = pins(&tx, "s", &sel);
    chmod(&t.at("two"), 0o555);
    let e = tx
        .apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap_err();
    chmod(&t.at("two"), 0o755);
    assert!(e.message.contains("rolled back"), "{e}");
    assert_eq!((read(&a).unwrap(), mode_of(&a)), ("{}\n".into(), 0o644));
    let j = crash(&tx, &mem, "s", &sel, 0, json!({"x": 2}));
    assert_eq!(mode_of(&a), 0o600);
    let r = tx.reconcile(std::slice::from_ref(&j)).unwrap();
    assert_eq!(r[0]["restored"], json!([a]));
    assert_eq!((read(&a).unwrap(), mode_of(&a)), ("{}\n".into(), 0o644));
}

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

// ---- manual backups (backups.rs)

fn tx_in(t: &Tmp, mem: &Mem) -> PathBuf {
    let data = t.at("data");
    std::fs::create_dir_all(&data).unwrap();
    let _ = mem;
    data
}

fn manual(mem: &Mem) -> Vec<String> {
    mem.backups
        .borrow()
        .iter()
        .filter(|(_, (l, _))| l.starts_with("bk "))
        .map(|(id, _)| id.clone())
        .collect()
}

#[test]
fn a_manual_backup_lists_diffs_by_name_restores_and_deletes() {
    let t = Tmp::new();
    let f = t.put(
        "x/settings.json",
        "{\"theme\": \"light\", \"token\": \"sekrit-value\"}\n",
    );
    let mem = Mem::default();
    let tx = Transactions::new(&mem, tx_in(&t, &mem));
    let sel = [Selection::File(f.clone())];
    let o = tx.backup_create("s", &pins(&tx, "s", &sel)).unwrap();
    let id = o["backup"].as_str().unwrap().to_string();
    assert_eq!(o["deleted"], json!([]));
    let l = tx.backup_list("s").unwrap();
    assert_eq!(l["keep"], KEEP_BACKUPS);
    assert_eq!(l["backups"][0]["id"], json!(id));
    assert_eq!(
        l["backups"][0]["files"],
        json!([{"selected": f, "path": null, "target": f, "exists": true, "size": 44}])
    );
    assert_eq!(tx.backup_list("other").unwrap()["backups"], json!([]));
    assert_eq!(
        tx.backup_diff("s", &id, &json_leaves).unwrap()["same"],
        true
    );
    // The person's edits: a changed value and a new setting.
    std::fs::write(
        &f,
        "{\"theme\": \"dark\", \"token\": \"sekrit-value\", \"editor\": \"vim\"}\n",
    )
    .unwrap();
    let d = tx.backup_diff("s", &id, &json_leaves).unwrap();
    assert_eq!(
        d,
        json!({"backup": id, "same": false, "files": [{"target": f, "exists": true,
            "now_exists": true, "same": false, "readable": true, "more": 0, "changes": [
                {"path": ["theme"], "restore": "changes"},
                {"path": ["editor"], "restore": "removes"}]}]})
    );
    // No value in any answer, and none in the state file.
    let state = read(&t.at("data/transactions.json")).unwrap();
    for text in [l.to_string(), d.to_string(), state] {
        for v in ["light", "dark", "vim", "sekrit-value"] {
            assert!(!text.contains(v), "{v} in {text}");
        }
    }
    let o = tx
        .backup_restore("s", &pins(&tx, "s", &sel), &id, &mut none)
        .unwrap();
    assert_eq!(o, json!({"outcome": "restored", "changed": [f]}));
    assert_eq!(
        read(&f).unwrap(),
        "{\"theme\": \"light\", \"token\": \"sekrit-value\"}\n"
    );
    // Its journal committed, its own backups swept, the manual one kept.
    assert!(mem.open().is_empty());
    assert_eq!(manual(&mem), vec![id.clone()]);
    assert_eq!(mem.backups.borrow().len(), 1);
    assert_eq!(tx.backup_delete("s", &id).unwrap(), json!({"deleted": id}));
    assert_eq!(tx.backup_list("s").unwrap()["backups"], json!([]));
    assert!(mem.backups.borrow().is_empty());
}

#[test]
fn a_backup_of_an_absent_file_removes_it_on_restore() {
    let t = Tmp::new();
    let f = t.at("x/settings.json");
    std::fs::create_dir_all(f.parent().unwrap()).unwrap();
    let mem = Mem::default();
    let tx = Transactions::new(&mem, tx_in(&t, &mem));
    let sel = [Selection::File(f.clone())];
    let id = tx.backup_create("s", &pins(&tx, "s", &sel)).unwrap()["backup"]
        .as_str()
        .unwrap()
        .to_string();
    let l = tx.backup_list("s").unwrap();
    assert_eq!(l["backups"][0]["files"][0]["exists"], false);
    assert_eq!(l["backups"][0]["files"][0]["size"], 0);
    std::fs::write(&f, "{\"a\": {\"b\": 1}}").unwrap();
    let d = tx.backup_diff("s", &id, &json_leaves).unwrap();
    assert_eq!(d["files"][0]["exists"], false);
    assert_eq!(d["files"][0]["now_exists"], true);
    assert_eq!(
        d["files"][0]["changes"],
        json!([{"path": ["a", "b"], "restore": "removes"}])
    );
    tx.backup_restore("s", &pins(&tx, "s", &sel), &id, &mut none)
        .unwrap();
    assert!(!f.exists());
    // An unreadable file is told so, with no settings.
    std::fs::write(&f, "not json").unwrap();
    let d = tx.backup_diff("s", &id, &json_leaves).unwrap();
    assert_eq!(d["files"][0]["readable"], false);
    assert_eq!(d["files"][0]["changes"], json!([]));
}

#[test]
fn retention_keeps_the_newest_ten_and_never_the_librarys_own() {
    let t = Tmp::new();
    let f = t.put("x/settings.json", "{}\n");
    let mem = Mem::default();
    let tx = Transactions::new(&mem, tx_in(&t, &mem));
    let sel = [Selection::File(f.clone())];
    tx.apply(
        "s",
        &pins(&tx, "s", &sel),
        &values(json!({"model": "m"})),
        &Json,
        &mut none,
    )
    .unwrap();
    let own: Vec<String> = mem.backups.borrow().keys().cloned().collect();
    assert_eq!(own.len(), 2, "the record's original and applied images");
    let mut made = Vec::new();
    for _ in 0..KEEP_BACKUPS {
        let o = tx.backup_create("s", &pins(&tx, "s", &sel)).unwrap();
        assert_eq!(o["deleted"], json!([]));
        made.push(o["backup"].as_str().unwrap().to_string());
    }
    // Another set's backups count apart.
    let other = tx.backup_create("t", &pins(&tx, "t", &sel)).unwrap()["backup"].clone();
    let o = tx.backup_create("s", &pins(&tx, "s", &sel)).unwrap();
    assert_eq!(o["deleted"], json!([made[0]]));
    let ids: Vec<Value> = tx.backup_list("s").unwrap()["backups"]
        .as_array()
        .unwrap()
        .iter()
        .map(|b| b["id"].clone())
        .collect();
    assert_eq!(ids.len(), KEEP_BACKUPS);
    assert_eq!(ids[0], o["backup"]);
    assert!(!ids.contains(&json!(made[0])));
    let all = mem.backups.borrow().keys().cloned().collect::<Vec<_>>();
    assert!(!all.contains(&made[0]));
    assert!(own.iter().all(|b| all.contains(b)));
    assert!(all.contains(&other.as_str().unwrap().to_string()));
    // A write's sweep keeps every manual backup.
    tx.clear("s", &pins(&tx, "s", &sel), &Json, &mut none)
        .unwrap();
    assert_eq!(manual(&mem).len(), KEEP_BACKUPS + 1);
    // A manual backup the state file does not name (its delete failed) is swept.
    mem.backups
        .borrow_mut()
        .insert("b-orphan".into(), ("bk s".into(), String::new()));
    tx.apply(
        "s",
        &pins(&tx, "s", &sel),
        &values(json!({"model": "n"})),
        &Json,
        &mut none,
    )
    .unwrap();
    assert!(!mem.backups.borrow().contains_key("b-orphan"));
}

#[test]
fn foreign_ids_other_sets_and_other_files_are_refused() {
    let t = Tmp::new();
    let f = t.put("x/settings.json", "{}\n");
    let g = t.put("y/settings.json", "{}\n");
    let mem = Mem::default();
    let tx = Transactions::new(&mem, tx_in(&t, &mem));
    let sel = [Selection::File(f.clone())];
    tx.apply(
        "s",
        &pins(&tx, "s", &sel),
        &values(json!({"model": "m"})),
        &Json,
        &mut none,
    )
    .unwrap();
    let theirs = mem.backups.borrow().keys().next().unwrap().clone();
    let id = tx.backup_create("s", &pins(&tx, "s", &sel)).unwrap()["backup"]
        .as_str()
        .unwrap()
        .to_string();
    let p = pins(&tx, "s", &sel);
    for (set, b) in [("s", theirs.as_str()), ("t", id.as_str())] {
        let e = tx.backup_diff(set, b, &json_leaves).unwrap_err();
        assert!(
            e.code == "not_found" && e.message.contains("no manual backup"),
            "{e:?}"
        );
        assert!(tx.backup_restore(set, &p, b, &mut none).is_err());
        assert!(tx.backup_delete(set, b).is_err());
    }
    assert!(mem.backups.borrow().contains_key(&theirs));
    let e = tx
        .backup_restore(
            "s",
            &pins(&tx, "s", &[Selection::File(g.clone())]),
            &id,
            &mut none,
        )
        .unwrap_err();
    assert!(e.message.contains("backup of"), "{e:?}");
    assert_eq!(read(&g).unwrap(), "{}\n");
}

#[test]
fn restoring_a_manual_backup_keeps_the_originals_kept_for_restore() {
    let t = Tmp::new();
    let f = t.put("x/settings.json", "{\"a\": 1}\n");
    let mem = Mem::default();
    let data = tx_in(&t, &mem);
    let tx = Transactions::new(&mem, &data);
    let sel = [Selection::File(f.clone())];
    let id = tx.backup_create("s", &pins(&tx, "s", &sel)).unwrap()["backup"]
        .as_str()
        .unwrap()
        .to_string();
    // An original kept after a conflict, for this file.
    let kept = tx.put("original", b"{\"kept\": true}\n").unwrap();
    let mut state: Value = serde_json::from_str(&read(&data.join(STATE)).unwrap()).unwrap();
    state["kept"] = json!([{"set": "s", "selected": f, "path": null, "target": f,
        "original": kept, "original_mode": 420, "written": null, "why": "conflict"}]);
    tx.save(&state).unwrap();
    std::fs::write(&f, "{\"a\": 2}\n").unwrap();
    tx.backup_restore("s", &pins(&tx, "s", &sel), &id, &mut none)
        .unwrap();
    assert_eq!(read(&f).unwrap(), "{\"a\": 1}\n");
    assert_eq!(tx.status().unwrap()["kept"].as_array().unwrap().len(), 1);
    // Restore still reaches it.
    tx.restore("s", &pins(&tx, "s", &sel), &mut none).unwrap();
    assert_eq!(read(&f).unwrap(), "{\"kept\": true}\n");
}

#[test]
fn json_settings_are_leaves_by_path() {
    assert_eq!(json_leaves(b"  \n").unwrap(), vec![]);
    assert!(json_leaves(b"{,}").is_none());
    let l = json_leaves(br#"{"a": {"b": [1, 2], "c": {}}, "d": null}"#).unwrap();
    let paths: Vec<Vec<String>> = l.into_iter().map(|(p, _)| p).collect();
    assert_eq!(
        paths,
        vec![
            vec!["a".to_string(), "b".into()],
            vec!["a".into(), "c".into()],
            vec!["d".into()]
        ]
    );
}

/// Review #298 row 4: an empty root object has no settings, so no change with an empty path.
#[test]
fn an_empty_root_object_has_no_settings() {
    assert_eq!(json_leaves(b"{}").unwrap(), vec![]);
    let t = Tmp::new();
    let f = t.put("x/settings.json", "{}\n");
    let mem = Mem::default();
    let tx = Transactions::new(&mem, tx_in(&t, &mem));
    let sel = [Selection::File(f.clone())];
    let id = tx.backup_create("s", &pins(&tx, "s", &sel)).unwrap()["backup"]
        .as_str()
        .unwrap()
        .to_string();
    std::fs::write(&f, "{\"a\": 1}\n").unwrap();
    let d = tx.backup_diff("s", &id, &json_leaves).unwrap();
    assert_eq!(
        d["files"][0]["changes"],
        json!([{"path": ["a"], "restore": "removes"}])
    );
}

/// Review #298 row 6: a version whose record names no vault backup for a file that existed is an
/// internal error, never an empty file written.
#[test]
fn a_backup_record_naming_no_vault_backup_restores_nothing() {
    let t = Tmp::new();
    let f = t.put("x/settings.json", "{\"a\": 1}\n");
    let mem = Mem::default();
    let data = tx_in(&t, &mem);
    let tx = Transactions::new(&mem, &data);
    let sel = [Selection::File(f.clone())];
    let id = tx.backup_create("s", &pins(&tx, "s", &sel)).unwrap()["backup"]
        .as_str()
        .unwrap()
        .to_string();
    let mut state: Value = serde_json::from_str(&read(&data.join(STATE)).unwrap()).unwrap();
    state["backups"][0]["files"][0]["backup"] = Value::Null;
    tx.save(&state).unwrap();
    std::fs::write(&f, "{\"a\": 2}\n").unwrap();
    let e = tx
        .backup_restore("s", &pins(&tx, "s", &sel), &id, &mut none)
        .unwrap_err();
    assert_eq!(e.code, "internal", "{e:?}");
    assert_eq!(read(&f).unwrap(), "{\"a\": 2}\n");
    assert!(mem.open().is_empty());
}

/// Review #298 row 6: when the state file cannot be saved, the vault backups just put are deleted.
#[test]
fn a_backup_whose_record_cannot_be_saved_leaves_nothing_in_the_vault() {
    let t = Tmp::new();
    let f = t.put("x/settings.json", "{\"a\": 1}\n");
    let mem = Mem::default();
    let data = tx_in(&t, &mem);
    let tx = Transactions::new(&mem, &data);
    let p = pins(&tx, "s", &[Selection::File(f.clone())]);
    chmod(&data, 0o500);
    let r = tx.backup_create("s", &p);
    chmod(&data, 0o700);
    assert!(r.is_err(), "{r:?}");
    assert!(mem.backups.borrow().is_empty());
}

/// A restore of a manual backup keeps the file's permission bits, as clear and restore do; a file
/// made again gets the bits it had when it was backed up.
#[test]
fn restoring_a_manual_backup_keeps_the_files_mode_and_remakes_a_file_with_its_own() {
    let t = Tmp::new();
    let f = t.put("x/settings.json", "{\"a\": 1}\n");
    chmod(&f, 0o640);
    let mem = Mem::default();
    let tx = Transactions::new(&mem, tx_in(&t, &mem));
    let sel = [Selection::File(f.clone())];
    let id = tx.backup_create("s", &pins(&tx, "s", &sel)).unwrap()["backup"]
        .as_str()
        .unwrap()
        .to_string();
    std::fs::write(&f, "{\"a\": 2}\n").unwrap();
    chmod(&f, 0o604);
    tx.backup_restore("s", &pins(&tx, "s", &sel), &id, &mut none)
        .unwrap();
    assert_eq!(
        (read(&f).unwrap(), mode_of(&f)),
        ("{\"a\": 1}\n".into(), 0o604)
    );
    std::fs::remove_file(&f).unwrap();
    tx.backup_restore("s", &pins(&tx, "s", &sel), &id, &mut none)
        .unwrap();
    assert_eq!(
        (read(&f).unwrap(), mode_of(&f)),
        ("{\"a\": 1}\n".into(), 0o640)
    );
}

/// Reaudit 6, #278 finding 1: backups put for an operation that then fails are not left to count
/// against the vault's limit.
#[test]
fn a_failed_operation_leaves_no_backups_behind() {
    let t = Tmp::new();
    let (a, b, mem, data) = two(&t);
    let tx = Transactions::new(&mem, &data);
    let sel = [Selection::File(a.clone()), Selection::File(b.clone())];
    // The fourth backup of apply's images (two per file) cannot be put.
    let p = pins(&tx, "s", &sel);
    mem.puts_left.set(Some(3));
    tx.apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap_err();
    mem.puts_left.set(None);
    assert!(mem.backups.borrow().is_empty(), "{:?}", mem.backups);
    assert_eq!(
        (read(&a).unwrap(), read(&b).unwrap()),
        ("{}\n".into(), "{}\n".into())
    );
    // A journal that cannot begin (one is open): the journal's backups go too.
    mem.journals
        .borrow_mut()
        .insert("j-open".into(), (json!({}), None));
    let e = tx
        .apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap_err();
    assert!(e.message.contains("journal is open"), "{e}");
    assert!(mem.backups.borrow().is_empty(), "{:?}", mem.backups);
}

/// Reaudit 6, #278 finding 2: restoring a kept original over a file that already has it forgets
/// the kept entry and its backups.
#[test]
fn restore_of_a_kept_original_already_in_place_forgets_it() {
    let t = Tmp::new();
    let (_, b, mem, data) = two(&t);
    let tx = Transactions::new(&mem, &data);
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
    assert_eq!(tx.status().unwrap()["kept"].as_array().unwrap().len(), 1);
    // The person put the original back by hand.
    std::fs::write(&b, "{}\n").unwrap();
    let p = pins(&tx, "r", &[Selection::File(b.clone())]);
    let r = tx.restore("r", &p, &mut none).unwrap();
    assert_eq!(r["changed"], json!([]));
    assert_eq!(tx.status().unwrap()["kept"], json!([]));
    assert!(mem.backups.borrow().is_empty(), "{:?}", mem.backups);
}

/// Reaudit 6, #278 finding 3a: a temporary file a crash left is removed even when its target is
/// a link now and so cannot be opened for reconciling.
#[test]
fn reconcile_removes_temporary_files_beside_a_target_that_became_a_link() {
    if !links_allowed() {
        return;
    }
    let t = Tmp::new();
    let (a, b, mem, data) = two(&t);
    let tx = Transactions::new(&mem, &data);
    let sel = [Selection::File(a.clone()), Selection::File(b.clone())];
    let j = crash(&tx, &mem, "s", &sel, 0, json!({"x": 1}));
    let tmp = t.put("one/.a.json.pluxy-0123456789abcdef.tmp", "{\"secret\": 1}");
    let elsewhere = t.put("elsewhere.json", "{}\n");
    std::fs::remove_file(&a).unwrap();
    make_link(&elsewhere, &a);
    let r = tx.reconcile(&[j]).unwrap();
    assert_eq!(r[0]["conflicts"], json!([a]));
    assert!(!tmp.exists(), "the temporary file was left");
    assert_eq!(read(&elsewhere).unwrap(), "{}\n");
}

/// Reaudit 6, #278 finding 3b: a reconcile delivered again, because its journal could not be
/// ended, does not keep an original twice.
#[test]
fn a_reconcile_delivered_again_keeps_each_original_once() {
    let t = Tmp::new();
    let (a, b, mem, data) = two(&t);
    let tx = Transactions::new(&mem, &data);
    let sel = [Selection::File(a.clone()), Selection::File(b.clone())];
    let j = crash(&tx, &mem, "s", &sel, 0, json!({"x": 1}));
    std::fs::write(&a, "{\"theirs\": 1}\n").unwrap();
    mem.fail_end.set(true);
    tx.reconcile(std::slice::from_ref(&j)).unwrap_err();
    mem.fail_end.set(false);
    tx.reconcile(std::slice::from_ref(&j)).unwrap();
    assert_eq!(tx.status().unwrap()["kept"].as_array().unwrap().len(), 1);
}

/// The sweeps of a failed operation (#309) delete only what the operation put: a manual backup
/// the state file names stays, and a Manual Backup that cannot put all its files leaves none.
#[test]
fn a_failed_operations_sweep_keeps_the_manual_backups() {
    let t = Tmp::new();
    let (a, b, mem, data) = two(&t);
    let tx = Transactions::new(&mem, &data);
    let sel = [Selection::File(a.clone()), Selection::File(b.clone())];
    let p = pins(&tx, "s", &sel);
    let id = tx.backup_create("s", &p).unwrap()["backup"].clone();
    let manual_ids = manual(&mem);
    assert_eq!(manual_ids.len(), 2);
    mem.puts_left.set(Some(3));
    tx.apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap_err();
    mem.puts_left.set(None);
    assert_eq!(manual(&mem), manual_ids);
    assert_eq!(mem.backups.borrow().len(), 2);
    mem.journals
        .borrow_mut()
        .insert("j-open".into(), (json!({}), None));
    tx.apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap_err();
    assert_eq!(manual(&mem), manual_ids);
    assert_eq!(tx.backup_list("s").unwrap()["backups"][0]["id"], id);
    // A Manual Backup whose second file cannot be put leaves no backup of the first.
    mem.puts_left.set(Some(1));
    tx.backup_create("s", &p).unwrap_err();
    mem.puts_left.set(None);
    assert_eq!(manual(&mem), manual_ids);
    assert_eq!(
        tx.backup_list("s").unwrap()["backups"]
            .as_array()
            .unwrap()
            .len(),
        1
    );
}

/// One file selected twice is refused, also under two names: hard links to it.
#[test]
fn one_file_under_two_names_is_refused() {
    let t = Tmp::new();
    let a = t.put("x/a.json", "{}\n");
    let b = t.at("x/b.json");
    std::fs::hard_link(&a, &b).unwrap();
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    for sel in [
        [Selection::File(a.clone()), Selection::File(a.clone())],
        [Selection::File(a.clone()), Selection::File(b.clone())],
    ] {
        let e = tx.preview("s", &sel).unwrap_err();
        assert!(e.message.contains("named twice"), "{e}");
    }
}

/// Whether the folder dir ignores case (as macOS and Windows do by default); says so on stderr
/// when it does not, so a test of case is seen to skip.
fn ignores_case(dir: &Path) -> bool {
    let probe = dir.join("case-probe");
    std::fs::write(&probe, "").unwrap();
    let ignores = dir.join("CASE-PROBE").exists();
    std::fs::remove_file(&probe).unwrap();
    if !ignores && cfg!(not(target_os = "linux")) {
        eprintln!("skipped: this folder's file system does not ignore case");
    }
    ignores
}

/// Where the file system ignores case, a file selected in another case is written under its own
/// name, never renamed to the selected spelling, and two spellings of one name are refused, the
/// file there or not.
#[test]
fn a_file_selected_in_another_case_keeps_its_name() {
    let t = Tmp::new();
    let a = t.put("x/settings.json", "{}\n");
    if !ignores_case(&t.at("x")) {
        return;
    }
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let upper = t.at("x/SETTINGS.JSON");
    for sel in [
        Selection::File(upper.clone()),
        Selection::Root {
            root: t.at("x"),
            path: "Settings.Json".into(),
        },
    ] {
        let p = pins(&tx, "s", std::slice::from_ref(&sel));
        assert_eq!(p[0].target, a);
        tx.apply("s", &p, &values(json!({"k": 1})), &Json, &mut none)
            .unwrap();
        let p = pins(&tx, "s", std::slice::from_ref(&sel));
        tx.clear("s", &p, &Json, &mut none).unwrap();
        let names: Vec<_> = std::fs::read_dir(t.at("x"))
            .unwrap()
            .map(|e| e.unwrap().file_name())
            .collect();
        assert_eq!(names, ["settings.json"]);
    }
    for sel in [
        [Selection::File(a.clone()), Selection::File(upper.clone())],
        [
            Selection::File(t.at("x/new.json")),
            Selection::File(t.at("x/New.JSON")),
        ],
    ] {
        let e = tx.preview("s", &sel).unwrap_err();
        assert!(e.message.contains("named twice"), "{e}");
    }
}

/// Windows: a file selected by its short (8.3) name is written under its own long name.
#[cfg(windows)]
#[test]
fn a_file_selected_by_its_short_name_keeps_its_long_name() {
    use std::os::windows::ffi::{OsStrExt, OsStringExt};
    let t = Tmp::new();
    let long = t.put("x/a-long-settings-name.json", "{}\n");
    let wide: Vec<u16> = plain(&long).as_os_str().encode_wide().chain([0]).collect();
    let mut buf = vec![0u16; 1024];
    let n = unsafe {
        windows_sys::Win32::Storage::FileSystem::GetShortPathNameW(
            wide.as_ptr(),
            buf.as_mut_ptr(),
            buf.len() as u32,
        )
    } as usize;
    let short = PathBuf::from(std::ffi::OsString::from_wide(&buf[..n]));
    if n == 0 || short.file_name() == long.file_name() {
        eprintln!("skipped: this volume gives no short names");
        return;
    }
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let short = t.at("x").join(short.file_name().unwrap());
    let p = pins(&tx, "s", &[Selection::File(short.clone())]);
    assert_eq!(p[0].target, long);
    tx.apply("s", &p, &values(json!({"k": 1})), &Json, &mut none)
        .unwrap();
    let names: Vec<_> = std::fs::read_dir(t.at("x"))
        .unwrap()
        .map(|e| e.unwrap().file_name())
        .collect();
    assert_eq!(names, ["a-long-settings-name.json"]);
    let e = tx
        .preview("s", &[Selection::File(long), Selection::File(short)])
        .unwrap_err();
    assert!(e.message.contains("named twice"), "{e}");
}

/// Windows: a file's access list as SDDL, without the `AI` flag, which Windows sets on a list given
/// through SetSecurityInfo (`D:(…)` reads back `D:AI(…)`) and which changes no entry, inheritance
/// or protection.
#[cfg(windows)]
fn acl(p: &Path) -> String {
    let sddl = pluxy_files::access_list(&std::fs::File::open(p).unwrap()).unwrap();
    match sddl.strip_prefix("D:").and_then(|s| s.split_once('(')) {
        Some((flags, rest)) => format!("D:{}({rest}", flags.replace("AI", "")),
        None => sddl,
    }
}

/// Windows: a file whose access list is its folder's (inherited, not protected) gets that list back
/// when an apply is rolled back, and keeps it through a manual backup's restore.
#[cfg(windows)]
#[test]
fn an_inherited_access_list_comes_back_as_it_was() {
    let t = Tmp::new();
    let (a, b, mem, data) = two(&t);
    let inherited = acl(&a);
    assert!(
        !inherited.starts_with("D:P") && inherited.contains(";ID;"),
        "{inherited}"
    );
    let tx = Transactions::new(&mem, &data);
    let sel = [Selection::File(a.clone()), Selection::File(b.clone())];
    let p = pins(&tx, "s", &sel);
    chmod(&t.at("two"), 0o555);
    let e = tx
        .apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap_err();
    chmod(&t.at("two"), 0o755);
    assert!(e.message.contains("rolled back"), "{e}");
    assert_eq!(
        (read(&a).unwrap(), acl(&a)),
        ("{}\n".into(), inherited.clone())
    );
    // A manual backup's restore keeps the list the file has.
    let one = [Selection::File(a.clone())];
    let id = tx.backup_create("s", &pins(&tx, "s", &one)).unwrap()["backup"]
        .as_str()
        .unwrap()
        .to_string();
    std::fs::write(&a, "{\"x\": 2}\n").unwrap();
    tx.backup_restore("s", &pins(&tx, "s", &one), &id, &mut none)
        .unwrap();
    assert_eq!((read(&a).unwrap(), acl(&a)), ("{}\n".into(), inherited));
}

/// The names in folder.
#[cfg(windows)]
fn names_in(folder: &Path) -> Vec<std::ffi::OsString> {
    let mut n: Vec<_> = std::fs::read_dir(folder)
        .unwrap()
        .map(|e| e.unwrap().file_name())
        .collect();
    n.sort();
    n
}

/// Windows: a file another program holds open without letting it be deleted, or one marked
/// read-only, is not replaced: the write fails, the file keeps its bytes, and no temporary file is
/// left. Once the program lets go, or the mark is taken off, the write goes through.
#[cfg(windows)]
#[test]
fn a_file_held_open_or_read_only_is_left_as_it_was() {
    use std::os::windows::fs::OpenOptionsExt;
    let t = Tmp::new();
    let a = t.put("one/a.json", "{}\n");
    let (mem, data) = (Mem::default(), t.at("data"));
    std::fs::create_dir(&data).unwrap();
    let tx = Transactions::new(&mem, &data);
    let sel = [Selection::File(a.clone())];
    // FILE_SHARE_READ only: no other handle may delete or replace it while this is open.
    let held = std::fs::OpenOptions::new()
        .read(true)
        .share_mode(1)
        .open(&a)
        .unwrap();
    let p = pins(&tx, "s", &sel);
    let e = tx
        .apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap_err();
    assert_eq!(
        (read(&a).unwrap(), names_in(&t.at("one"))),
        ("{}\n".into(), vec!["a.json".into()]),
        "{e}"
    );
    drop(held);
    let mut ro = std::fs::metadata(&a).unwrap().permissions();
    ro.set_readonly(true);
    std::fs::set_permissions(&a, ro.clone()).unwrap();
    let p = pins(&tx, "s", &sel);
    let e = tx
        .apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap_err();
    assert_eq!(
        (read(&a).unwrap(), names_in(&t.at("one"))),
        ("{}\n".into(), vec!["a.json".into()]),
        "{e}"
    );
    #[allow(clippy::permissions_set_readonly_false)]
    ro.set_readonly(false);
    std::fs::set_permissions(&a, ro).unwrap();
    let p = pins(&tx, "s", &sel);
    tx.apply("s", &p, &values(json!({"x": 1})), &Json, &mut none)
        .unwrap();
    assert_eq!(read(&a).unwrap(), "{\n  \"x\": 1\n}\n");
}

/// Windows reaches a WSL distribution's files as `\\wsl.localhost\<distribution>\…`: the Windows
/// program refuses those (the package's Linux program serves them, inside the distribution).
#[test]
fn wsl_localhost_paths_are_inside_a_distribution() {
    for p in [
        r"\\wsl.localhost\Ubuntu\home\me\.claude\settings.json",
        r"\\WSL.LocalHost\Ubuntu\home",
        "//wsl.localhost/Ubuntu/home",
        r"\\?\UNC\wsl.localhost\Ubuntu\home",
        r"\\?\unc\Wsl.Localhost\Ubuntu",
        r"\\.\UNC\wsl.localhost\Ubuntu\home",
        // Windows drops a host name's trailing dots and blanks.
        r"\\wsl.localhost.\Ubuntu\home",
        r"\\?\UNC\wsl.localhost. .\Ubuntu\home",
    ] {
        assert!(target::in_wsl(p), "{p}");
    }
    for p in [
        r"\\wsl.localhost.example\share\x",
        r"\\server\wsl.localhost\x",
        r"\\?\UNC\server\wsl.localhost\x",
        r"C:\wsl.localhost\x",
        r"\\?\C:\wsl.localhost\x",
        "/home/me/wsl.localhost/x",
    ] {
        assert!(!target::in_wsl(p), "{p}");
    }
}

/// The older spelling, `\\wsl$\<distribution>\…`, is refused the same way.
#[test]
fn wsl_dollar_paths_are_inside_a_distribution() {
    for p in [
        r"\\wsl$\Ubuntu\home\me\.claude\settings.json",
        r"\\WSL$\Ubuntu\home",
        "//wsl$/Ubuntu/home",
        r"\\?\UNC\wsl$\Ubuntu\home",
        r"\\?\unc\WSL$\Ubuntu",
        r"\\.\UNC\wsl$\Ubuntu\home",
        r"\\wsl$.\Ubuntu\home",
        r"\\?\UNC\WSL$ \Ubuntu\home",
    ] {
        assert!(target::in_wsl(p), "{p}");
    }
    for p in [
        r"\\wsl\Ubuntu\x",
        r"\\wsl$x\Ubuntu\x",
        r"\\server\wsl$\x",
        r"\\?\UNC\server\wsl$\x",
        r"\\?\C:\wsl$\x",
        "/home/me/wsl$/x",
    ] {
        assert!(!target::in_wsl(p), "{p}");
    }
}

/// Windows: a path whose names Windows would read as another file (a stream, a device, a name it
/// trims), or one in a device namespace, is refused, selected as a file, as a root or as a part of a
/// root's path; the plain form of a path (`C:\…`) and the long form (`\\?\C:\…`) are both taken.
#[cfg(windows)]
#[test]
fn windows_paths_that_name_another_file_are_refused() {
    let t = Tmp::new();
    let a = t.put("a.json", "{}\n");
    for p in [
        r"C:\x\a.json:stream",
        r"C:\x\dir.\a.json",
        r"C:\x\a.json.",
        r"C:\x\a.json ",
        r"C:\x\NUL",
        r"C:\x\nul.json",
        r"C:\x\COM1.json",
        r"\\?\C:\x\aux.json",
        r"\\.\C:\x\a.json",
        r"\\?\GLOBALROOT\Device\HarddiskVolume1\x\a.json",
        r"\\.\pipe\a.json",
    ] {
        let e = Target::resolve(&Selection::File(PathBuf::from(p)))
            .err()
            .unwrap();
        assert!(e.message.contains("on Windows"), "{p}: {e}");
        let e = Target::resolve(&Selection::Root {
            root: PathBuf::from(p),
            path: "x.json".into(),
        })
        .err()
        .unwrap();
        assert!(e.message.contains("on Windows"), "{p}: {e}");
    }
    for part in ["a.json:stream", "nul", "x.", "sub/con.json", "lpt1/x.json"] {
        let e = Target::resolve(&Selection::Root {
            root: t.0.clone(),
            path: part.into(),
        })
        .err()
        .unwrap();
        assert!(e.message.contains("on Windows"), "{part}: {e}");
    }
    // Both forms of a good path resolve to the same target.
    let long = Target::resolve(&Selection::File(a.clone())).unwrap();
    let short = Target::resolve(&Selection::File(plain(&a))).unwrap();
    assert_eq!(long.pin.target, short.pin.target);
    assert_eq!(long.pin.target, a);
}

/// Windows: a selected file or root in a WSL distribution is refused before anything is opened.
#[cfg(windows)]
#[test]
fn a_selection_in_a_wsl_distribution_is_refused() {
    for p in [
        r"\\wsl.localhost\Ubuntu\home\me\.claude\settings.json",
        r"\\wsl$\Ubuntu\home\me\.claude\settings.json",
        r"\\?\UNC\WSL.LOCALHOST\Ubuntu\home\me\.claude\settings.json",
    ] {
        let file = Target::resolve(&Selection::File(PathBuf::from(p)))
            .err()
            .unwrap();
        assert!(file.message.contains("WSL distribution"), "{p}: {file}");
        let root = Selection::Root {
            root: PathBuf::from(p).parent().unwrap().to_path_buf(),
            path: "settings.json".into(),
        };
        let root = Target::resolve(&root).err().unwrap();
        assert!(root.message.contains("WSL distribution"), "{p}: {root}");
    }
}

#[test]
fn the_data_folder_is_made_at_the_first_state_write_and_not_before() {
    let t = Tmp::new();
    let file = t.put("home/settings.json", "{}\n");
    // Missing, with a missing folder above it, as in a WSL distribution before the first write.
    let (mem, data) = (
        Mem::default(),
        t.at("home/.local/state/pluxy/tools/pluxy/w"),
    );
    let tx = Transactions::new(&mem, &data);
    assert_eq!(tx.status().unwrap()["sets"], json!({}));
    let p = tx.preview("s", &[Selection::File(file.clone())]).unwrap();
    assert!(!t.at("home/.local").exists(), "a read made the data folder");
    let pins = vec![Pin::from_json(&p["targets"][0]).unwrap()];
    tx.apply("s", &pins, &values(json!({"a": 1})), &Json, &mut none)
        .unwrap();
    for d in [
        t.at("home/.local"),
        t.at("home/.local/state/pluxy"),
        data.clone(),
    ] {
        let m = std::fs::symlink_metadata(&d).unwrap();
        assert!(m.is_dir());
        #[cfg(unix)]
        assert_eq!(m.mode() & 0o7777, 0o700, "{}", d.display());
    }
    #[cfg(unix)]
    assert_eq!(
        std::fs::metadata(data.join(STATE)).unwrap().mode() & 0o7777,
        0o600
    );
    // Windows: the data folder and the state file allow only this user and SYSTEM (the folders
    // above it are as their folder passes down).
    #[cfg(windows)]
    for p in [data.clone(), data.join(STATE)] {
        assert!(pluxy_files::is_private(&p).unwrap(), "{}", p.display());
    }
    assert_eq!(
        tx.status().unwrap()["sets"]["s"]["targets"][0]["target"],
        json!(file)
    );
}

/// A link at `at` to the folder target: on Windows a junction, which any account may make.
fn folder_link(target: &Path, at: &Path) {
    #[cfg(unix)]
    std::os::unix::fs::symlink(target, at).unwrap();
    #[cfg(windows)]
    junction(target, at);
}

#[test]
fn a_link_where_the_data_folder_goes_is_refused() {
    let t = Tmp::new();
    let file = t.put("settings.json", "{}\n");
    std::fs::create_dir(t.at("elsewhere")).unwrap();
    let data = t.at("data");
    folder_link(&t.at("elsewhere"), &data);
    let mem = Mem::default();
    let tx = Transactions::new(&mem, &data);
    let e = tx.status().unwrap_err();
    assert!(e.message.contains("not a folder of this user's"), "{e}");
    let e = tx.preview("s", &[Selection::File(file)]).unwrap_err();
    assert!(e.message.contains("not a folder of this user's"), "{e}");
    assert_eq!(std::fs::read_dir(t.at("elsewhere")).unwrap().count(), 0);
}

/// WSL runs Linux programs: Linux only.
#[cfg(target_os = "linux")]
const MOUNTINFO: &str = "\
1 0 8:32 / / rw,relatime - ext4 /dev/sdc rw
2 1 0:20 / /usr/lib/wsl/drivers ro - 9p drivers ro,aname=drivers
3 1 0:21 / /mnt/wsl rw,relatime shared:1 - tmpfs none rw
4 1 0:52 / /mnt/c rw,noatime - 9p C:\\134 rw,aname=drvfs,uid=1000
5 1 0:54 / /home/bob/win\\040docs rw - drvfs D:\\134docs rw
6 1 0:55 / /home/bob/data rw - ext4 /dev/sdd rw
7 6 0:56 / /home/bob/data/over rw - 9p E:\\134 rw,aname=drvfs
8 1 0:57 / /mnt/cd rw - ext4 /dev/sde rw
";

#[cfg(target_os = "linux")]
#[test]
fn paths_on_windows_drives_are_known_by_their_mount() {
    let on = |p: &str| target::on_windows_drive(MOUNTINFO, Path::new(p));
    // Linux files, a folder that is a mount of its own, a folder beside the drives.
    for p in [
        "/home/bob/.claude",
        "/home/bob/data/x",
        "/",
        "/mnt",
        "/mnt/cfoo",
    ] {
        assert!(!on(p), "{p}");
    }
    // A drive, a drive mounted elsewhere (with a space), one mounted over a Linux folder, mounts
    // in the automount root (WSL's shared tmpfs, and any disk mounted there), and WSL's own 9p
    // mounts.
    for p in [
        "/mnt/c",
        "/mnt/c/Users/me/.claude",
        "/home/bob/win docs/x",
        "/home/bob/data/over/y",
        "/mnt/wsl/shared",
        "/mnt/cd/x",
        "/usr/lib/wsl/drivers/x",
    ] {
        assert!(on(p), "{p}");
    }
    // Drives mounted at the root (automount root `/`): only the drives themselves.
    let at_root = "1 0 8:32 / / rw - ext4 /dev/sdc rw\n2 1 0:52 / /c rw - drvfs C:\\134 rw\n\
                   3 1 0:53 / /home rw - ext4 /dev/sdd rw\n";
    assert!(target::on_windows_drive(at_root, Path::new("/c/x")));
    assert!(!target::on_windows_drive(at_root, Path::new("/home/bob")));
    // As in WSL, with these mounts: refused, and a Linux path is not.
    let (drive, linux) = (Path::new("/mnt/c/x"), Path::new("/home/bob/.claude"));
    target::WSL_MOUNTS.with(|m| *m.borrow_mut() = Some(MOUNTINFO.to_string()));
    let (refused, kept) = (
        target::refuse_windows_drive(&[drive]),
        target::refuse_windows_drive(&[linux]),
    );
    target::WSL_MOUNTS.with(|m| *m.borrow_mut() = None);
    assert_eq!(refused.unwrap_err().message, ON_DRIVE);
    assert!(kept.is_ok());
    // This process as it runs: refused only with WSL's environment set (the WSL job runs this
    // suite inside a distribution with it) and the path on a drive by the real mounts.
    let in_wsl = std::env::var_os("PLUXY_ENVIRONMENT").is_some_and(|v| !v.is_empty());
    let real = std::fs::read_to_string("/proc/self/mountinfo").unwrap();
    for p in [drive, linux] {
        assert_eq!(
            target::refuse_windows_drive(&[p]).is_err(),
            in_wsl && target::on_windows_drive(&real, p),
            "{} (PLUXY_ENVIRONMENT set: {in_wsl})",
            p.display()
        );
    }
}

/// The Linux folder of the real-WSL tests, in the running user's home, removed when dropped.
#[cfg(target_os = "linux")]
fn wsl_home_folder() -> Tmp {
    let home = std::env::var("HOME").expect("HOME");
    assert!(
        std::env::var("PLUXY_ENVIRONMENT").is_ok_and(|e| e.starts_with("wsl:")),
        "the WSL job runs these with PLUXY_ENVIRONMENT"
    );
    let p = Path::new(&home).join(format!("pluxy-tx-{}", target::random_hex().unwrap()));
    std::fs::create_dir(&p).unwrap();
    Tmp(p)
}

/// In a real WSL distribution, as a user that is not its default one (next-app-tools-platforms.yml,
/// WSL job): a file on a Windows drive (`PLUXY_WSL_TEST_DRIVE_DIR`, a Linux path to a folder on
/// one, holding `settings.json`), selected directly, through a root or through a link in the
/// home, is refused; the same selections in the home are pinned.
#[cfg(target_os = "linux")]
#[test]
#[ignore = "runs inside a real WSL distribution (next-app-tools-platforms.yml, WSL job)"]
fn wsl_real_targets_on_a_windows_drive_are_refused() {
    let drive = PathBuf::from(std::env::var("PLUXY_WSL_TEST_DRIVE_DIR").unwrap());
    assert!(drive.join("settings.json").is_file());
    let t = wsl_home_folder();
    let home_file = t.put("cfg/settings.json", "{}\n");
    std::os::unix::fs::symlink(drive.join("settings.json"), t.at("to-drive.json")).unwrap();
    std::os::unix::fs::symlink(&drive, t.at("drive-root")).unwrap();
    let mem = Mem::default();
    let tx = Transactions::new(&mem, t.at("data"));
    let refused = [
        Selection::File(drive.join("settings.json")),
        Selection::Root {
            root: drive.clone(),
            path: "settings.json".into(),
        },
        Selection::File(t.at("to-drive.json")),
        Selection::Root {
            root: t.at("drive-root"),
            path: "settings.json".into(),
        },
    ];
    for s in &refused {
        let e = tx.preview("s", std::slice::from_ref(s)).unwrap_err();
        assert_eq!(
            e.message, "This file is on a Windows drive; set it up from Windows local.",
            "{s:?}"
        );
    }
    // Positive controls on the same route.
    let p = tx
        .preview("s", &[Selection::File(home_file.clone())])
        .unwrap();
    assert_eq!(p["targets"][0]["target"], json!(home_file));
    let p = tx
        .preview(
            "s",
            &[Selection::Root {
                root: t.at("cfg"),
                path: "settings.json".into(),
            }],
        )
        .unwrap();
    assert_eq!(p["targets"][0]["target"], json!(home_file));
    assert!(!t.at("data").exists());
}

/// In a real WSL distribution, as a user that is not its default one: the data folder is made in
/// the home at the first write, owner-only and owned by that user, and an apply's file is 0600,
/// owned by that user, with a link kept as it was.
#[cfg(target_os = "linux")]
#[test]
#[ignore = "runs inside a real WSL distribution (next-app-tools-platforms.yml, WSL job)"]
fn wsl_real_the_first_write_makes_the_data_folder_and_files_are_the_users() {
    let t = wsl_home_folder();
    let target = t.put("dotfiles/settings.json", "{\"theme\": \"light\"}\n");
    std::fs::set_permissions(&target, std::fs::Permissions::from_mode(0o644)).unwrap();
    std::os::unix::fs::symlink("dotfiles/settings.json", t.at("settings.json")).unwrap();
    let data = t.at("state/pluxy/tools/pluxy/fixture-writer");
    let mem = Mem::default();
    let tx = Transactions::new(&mem, &data);
    let p = tx
        .preview("s", &[Selection::File(t.at("settings.json"))])
        .unwrap();
    assert!(!t.at("state").exists(), "a preview made the data folder");
    let pinned = vec![Pin::from_json(&p["targets"][0]).unwrap()];
    tx.apply(
        "s",
        &pinned,
        &values(json!({"model": "m"})),
        &Json,
        &mut none,
    )
    .unwrap();
    // SAFETY: geteuid has no preconditions.
    let me = unsafe { libc::geteuid() };
    assert_ne!(me, 0, "run as a user, not root");
    let m = std::fs::symlink_metadata(&data).unwrap();
    assert!(m.is_dir());
    assert_eq!((m.uid(), m.mode() & 0o7777), (me, 0o700));
    let m = std::fs::metadata(&target).unwrap();
    assert_eq!((m.uid(), m.mode() & 0o7777), (me, 0o600));
    assert_eq!(
        std::fs::read_link(t.at("settings.json")).unwrap(),
        Path::new("dotfiles/settings.json")
    );
    assert!(read(&target).unwrap().contains("\"model\": \"m\""));
    // The apply put a new file in place: restore needs pins from a new preview.
    let again = pins(&tx, "s", &[Selection::File(t.at("settings.json"))]);
    tx.restore("s", &again, &mut none).unwrap();
    assert_eq!(read(&target).unwrap(), "{\"theme\": \"light\"}\n");
}

#[cfg(target_os = "linux")]
/// Runs f as if in WSL with these mounts (`/proc/self/mountinfo` lines after the root's).
fn in_wsl<T>(mounts: &[(&Path, &str)], f: impl FnOnce() -> T) -> T {
    let mut text = String::from("1 0 8:1 / / rw - ext4 /dev/sda rw\n");
    for (i, (point, fstype)) in mounts.iter().enumerate() {
        let point = point.to_str().unwrap().replace(' ', "\\040");
        text.push_str(&format!(
            "{} 1 0:{} / {point} rw - {fstype} C:\\134 rw\n",
            i + 2,
            50 + i
        ));
    }
    target::WSL_MOUNTS.with(|m| *m.borrow_mut() = Some(text));
    let out = f();
    target::WSL_MOUNTS.with(|m| *m.borrow_mut() = None);
    out
}

#[cfg(target_os = "linux")]
const ON_DRIVE: &str = "This file is on a Windows drive; set it up from Windows local.";

#[cfg(target_os = "linux")]
#[test]
fn in_wsl_a_file_mounted_from_a_windows_drive_is_refused_even_alone() {
    let t = Tmp::new();
    let file = t.put("home/settings.json", "{}\n");
    let other = t.put("home/other.json", "{}\n");
    let mem = Mem::default();
    let tx = Transactions::new(&mem, t.at("data"));
    // The file itself is a mount of a drive's file (its folder is not).
    let e = in_wsl(&[(&file, "9p")], || {
        tx.preview("s", &[Selection::File(file.clone())])
    })
    .unwrap_err();
    assert_eq!(e.message, ON_DRIVE);
    let e = in_wsl(&[(&file, "drvfs")], || {
        tx.preview(
            "s",
            &[Selection::Root {
                root: t.at("home"),
                path: "settings.json".into(),
            }],
        )
    })
    .unwrap_err();
    assert_eq!(e.message, ON_DRIVE);
    // Positive control on the same route: the file beside it.
    let p = in_wsl(&[(&file, "9p")], || {
        tx.preview("s", &[Selection::File(other.clone())])
    })
    .unwrap();
    assert_eq!(p["targets"][0]["target"], json!(other));
}

#[cfg(target_os = "linux")]
#[test]
fn in_wsl_a_root_on_a_windows_drive_is_refused() {
    let t = Tmp::new();
    t.put("drive/settings.json", "{}\n");
    t.put("home/settings.json", "{}\n");
    let mem = Mem::default();
    let tx = Transactions::new(&mem, t.at("data"));
    let root = |r: &str| Selection::Root {
        root: t.at(r),
        path: "settings.json".into(),
    };
    let drive = t.at("drive");
    let e = in_wsl(&[(&drive, "9p")], || tx.preview("s", &[root("drive")])).unwrap_err();
    assert_eq!(e.message, ON_DRIVE);
    // A mount in the folder that holds a drive mount (as /mnt/wsl beside /mnt/c), and a root
    // that is a link to the drive.
    let c = t.at("mnt/c");
    std::fs::create_dir_all(&c).unwrap();
    std::os::unix::fs::symlink(&drive, t.at("home/to-drive")).unwrap();
    let e = in_wsl(&[(&c, "9p"), (&drive, "tmpfs")], || {
        tx.preview(
            "s",
            &[Selection::Root {
                root: t.at("drive"),
                path: "settings.json".into(),
            }],
        )
    });
    // drive is not in mnt/, so a tmpfs there is a Linux folder.
    assert!(e.is_ok(), "{e:?}");
    let wslmnt = t.at("mnt/wsl");
    std::fs::create_dir_all(&wslmnt).unwrap();
    std::fs::write(wslmnt.join("settings.json"), "{}\n").unwrap();
    let e = in_wsl(&[(&c, "9p"), (&wslmnt, "tmpfs")], || {
        tx.preview(
            "s",
            &[Selection::Root {
                root: wslmnt.clone(),
                path: "settings.json".into(),
            }],
        )
    })
    .unwrap_err();
    assert_eq!(e.message, ON_DRIVE);
    let e = in_wsl(&[(&drive, "9p")], || {
        tx.preview(
            "s",
            &[Selection::Root {
                root: t.at("home/to-drive"),
                path: "settings.json".into(),
            }],
        )
    })
    .unwrap_err();
    assert_eq!(e.message, ON_DRIVE);
    // Positive control: the root in the home.
    assert!(in_wsl(&[(&drive, "9p")], || tx.preview("s", &[root("home")])).is_ok());
}

#[cfg(target_os = "linux")]
#[test]
fn in_wsl_recovery_does_not_touch_a_journalled_target_now_on_a_windows_drive() {
    let t = Tmp::new();
    let file = t.put("home/settings.json", "{}\n");
    let mem = Mem::default();
    let tx = Transactions::new(&mem, t.at("data"));
    let p = tx.preview("s", &[Selection::File(file.clone())]).unwrap();
    let pin = Pin::from_json(&p["targets"][0]).unwrap();
    // Its folder, or the file itself, mounted from a drive since.
    for point in [t.at("home"), file.clone()] {
        let e = in_wsl(&[(&point, "9p")], || Target::pinned(&pin).map(|_| ())).unwrap_err();
        assert_eq!(e.message, ON_DRIVE, "{}", point.display());
    }
    assert!(
        in_wsl(&[(&t.at("elsewhere"), "9p")], || Target::pinned(&pin)
            .map(|_| ()))
        .is_ok()
    );
}

#[test]
fn a_link_where_the_data_folder_goes_is_refused_with_a_trailing_slash_too() {
    let t = Tmp::new();
    std::fs::create_dir(t.at("elsewhere")).unwrap();
    folder_link(&t.at("elsewhere"), &t.at("data"));
    for data in [
        format!("{}/", t.at("data").display()),
        format!("{}//", t.at("data").display()),
    ] {
        let mem = Mem::default();
        let e = Transactions::new(&mem, PathBuf::from(&data))
            .status()
            .unwrap_err();
        assert!(
            e.message.contains("not a folder of this user's"),
            "{data}: {e}"
        );
    }
    // A real folder named with a trailing slash still works.
    std::fs::create_dir(t.at("real")).unwrap();
    let mem = Mem::default();
    let data = PathBuf::from(format!("{}/", t.at("real").display()));
    assert_eq!(
        Transactions::new(&mem, data).status().unwrap()["sets"],
        json!({})
    );
}

// ---- folders a tool owns inside a selected root (folder.rs)

/// Takes away the folder link at at, leaving what it points to.
fn unlink_folder(at: &Path) {
    #[cfg(unix)]
    std::fs::remove_file(at).unwrap();
    #[cfg(windows)]
    std::fs::remove_dir(at).unwrap();
}

const MARK: &str = ".mark";

/// A copy of a folder, version v: two files and its marker.
fn copy(v: &str) -> Vec<(&'static str, Vec<u8>)> {
    vec![
        ("a.txt", format!("a {v}\n").into_bytes()),
        ("sub/b.txt", format!("b {v}\n").into_bytes()),
        (MARK, format!("{{\"v\": \"{v}\"}}\n").into_bytes()),
    ]
}

fn put(tx: &Transactions, f: &Folder, v: &str) -> Result<(), Error> {
    let c = copy(v);
    let files: Vec<(&str, &[u8])> = c.iter().map(|(n, b)| (*n, b.as_slice())).collect();
    tx.put_folder("s", f, &files).map(|_| ())
}

/// The pin of the folder plug in root, as a preview takes it.
fn plug(root: &Path) -> Pin {
    Target::resolve(&Selection::Root {
        root: root.to_path_buf(),
        path: format!("plug/{MARK}"),
    })
    .unwrap()
    .pin
}

/// Every entry under dir, not following links: files with their bytes, folders (empty ones
/// too), links with their text.
fn entries(dir: &Path) -> Vec<String> {
    let mut out = Vec::new();
    let mut stack = vec![dir.to_path_buf()];
    while let Some(d) = stack.pop() {
        for e in std::fs::read_dir(&d).unwrap() {
            let p = e.unwrap().path();
            let rel = p
                .strip_prefix(dir)
                .unwrap()
                .components()
                .map(|c| c.as_os_str().to_string_lossy().into_owned())
                .collect::<Vec<_>>()
                .join("/");
            let m = std::fs::symlink_metadata(&p).unwrap();
            if m.file_type().is_symlink() {
                out.push(format!(
                    "{rel} -> {}",
                    std::fs::read_link(&p).unwrap().display()
                ));
            } else if m.is_dir() {
                out.push(format!("{rel}/"));
                stack.push(p);
            } else {
                out.push(format!(
                    "{rel}: {}",
                    String::from_utf8_lossy(&std::fs::read(&p).unwrap())
                ));
            }
        }
    }
    out.sort();
    out
}

/// What a folder holding copy v at plug shows in entries.
fn holding(v: &str) -> Vec<String> {
    let mut want = vec![
        "plug/".to_string(),
        "plug/sub/".to_string(),
        format!("plug/a.txt: a {v}\n"),
        format!("plug/sub/b.txt: b {v}\n"),
        format!("plug/{MARK}: {{\"v\": \"{v}\"}}\n"),
    ];
    want.sort();
    want
}

/// Whether every journal the vault began is ended as committed.
fn all_committed(v: &Mem) -> bool {
    v.journals
        .borrow()
        .values()
        .all(|(_, o)| o.as_deref() == Some("committed"))
}

/// The hidden name an operation moved plug aside to in root, if any.
fn aside_in(root: &Path) -> Option<PathBuf> {
    std::fs::read_dir(root)
        .unwrap()
        .map(|e| e.unwrap().path())
        .find(|p| {
            let n = p.file_name().unwrap().to_string_lossy();
            n.starts_with(".plug.pluxy-") && n.ends_with(".old")
        })
}

#[test]
fn a_folder_is_put_replaced_and_removed_in_the_root_pinned_whatever_its_link_points_to_after() {
    let t = Tmp::new();
    let v = Mem::default();
    let tx = Transactions::new(&v, t.at("data"));
    let (a, b, root) = (t.at("a"), t.at("b"), t.at("root"));
    std::fs::create_dir_all(&a).unwrap();
    std::fs::create_dir_all(&b).unwrap();
    // A first copy, a new copy over it and its removal, each with the root's link pointed at b
    // once the place was checked: each stays in a, the folder the root resolved to.
    for step in ["first", "replace", "remove"] {
        let _ = std::fs::symlink_metadata(&root).map(|_| unlink_folder(&root));
        folder_link(&a, &root);
        let f = match step {
            "first" => Folder::empty(&root, "plug", MARK).unwrap(),
            _ => Folder::pinned(&plug(&root)).unwrap(),
        };
        let (r, to) = (root.clone(), b.clone());
        hook(Some(Box::new(move |s| {
            if s == Step::Checked {
                unlink_folder(&r);
                folder_link(&to, &r);
            }
            false
        })));
        let done = match step {
            "first" => put(&tx, &f, "1"),
            "replace" => put(&tx, &f, "2"),
            _ => tx.remove_folder("s", &f),
        };
        hook(None);
        done.unwrap();
        let want = match step {
            "first" => holding("1"),
            "replace" => holding("2"),
            _ => Vec::new(),
        };
        assert_eq!(entries(&a), want, "{step}");
        assert!(entries(&b).is_empty(), "{step}: {:?}", entries(&b));
    }
    assert!(v.open().is_empty() && all_committed(&v));
}

#[test]
fn a_folder_that_is_not_the_one_pinned_is_refused_before_any_change() {
    let t = Tmp::new();
    let v = Mem::default();
    let tx = Transactions::new(&v, t.at("data"));
    let root = t.at("root");
    std::fs::create_dir_all(&root).unwrap();
    put(&tx, &Folder::empty(&root, "plug", MARK).unwrap(), "1").unwrap();
    let pin = plug(&root);
    // The marker rewritten in place (same file, other bytes), the folder replaced by an equal
    // copy: refused as changed, nothing written.
    std::fs::write(root.join("plug").join(MARK), "{\"v\": \"x\"}\n").unwrap();
    let e = Folder::pinned(&pin).err().unwrap();
    assert!(e.message.contains("preview it again"), "{}", e.message);
    std::fs::write(root.join("plug").join(MARK), "{\"v\": \"1\"}\n").unwrap();
    std::fs::rename(root.join("plug"), t.at("moved")).unwrap();
    put(&tx, &Folder::empty(&root, "plug", MARK).unwrap(), "1").unwrap();
    let e = Folder::pinned(&pin).err().unwrap();
    assert!(e.message.contains("preview it again"), "{}", e.message);
    // A first copy where something is already: refused.
    assert!(Folder::empty(&root, "plug", MARK)
        .err()
        .unwrap()
        .message
        .contains("there already"));
    assert!(Folder::pinned(&Pin {
        selection: Selection::Root {
            root: root.clone(),
            path: "plug/sub/b.txt".into(),
        },
        ..pin.clone()
    })
    .is_err());
}

#[test]
fn a_stop_at_any_step_is_reconciled_from_the_folder_the_same_way_each_time() {
    use Step::*;
    // A new copy over an old one: undone until the new copy is in place. A removal: undone until
    // its marker is gone, then finished. A first copy: undone until it is in place.
    for (op, step, outcome) in [
        ("replace", Written, "rolled_back"),
        ("replace", Checked, "rolled_back"),
        ("replace", Moved, "rolled_back"),
        ("replace", Placed, "committed"),
        ("remove", Checked, "rolled_back"),
        ("remove", Moved, "rolled_back"),
        ("remove", Unmarked, "committed"),
        ("first", Written, "rolled_back"),
        ("first", Checked, "rolled_back"),
    ] {
        let case = format!("{op} stopped at {step:?}");
        let t = Tmp::new();
        let v = Mem::default();
        let tx = Transactions::new(&v, t.at("data"));
        let root = t.at("root");
        std::fs::create_dir_all(&root).unwrap();
        if op != "first" {
            put(&tx, &Folder::empty(&root, "plug", MARK).unwrap(), "1").unwrap();
        }
        let f = match op {
            "first" => Folder::empty(&root, "plug", MARK).unwrap(),
            _ => Folder::pinned(&plug(&root)).unwrap(),
        };
        hook(Some(Box::new(move |s| s == step)));
        let r = match op {
            "remove" => tx.remove_folder("s", &f),
            _ => put(&tx, &f, "2"),
        };
        hook(None);
        drop(f);
        assert!(r.unwrap_err().message.contains("stopped at"), "{case}");
        let open = v.open();
        assert_eq!(open.len(), 1, "{case}");
        let want = match (op, outcome) {
            ("replace", "committed") => holding("2"),
            ("remove", "committed") | ("first", _) => Vec::new(),
            _ => holding("1"),
        };
        for round in ["once", "again"] {
            let reports = tx.reconcile(&open).unwrap();
            assert_eq!(reports[0]["outcome"], outcome, "{case}, {round}");
            assert_eq!(reports[0]["conflicts"], json!([]), "{case}, {round}");
            // Nothing hidden is left beside it.
            assert_eq!(entries(&root), want, "{case}, {round}");
        }
        assert!(v.open().is_empty(), "{case}");
    }
}

#[test]
fn a_folder_moved_aside_that_cannot_go_back_is_named_and_put_back_by_the_next_start() {
    for blocker_stays in [false, true] {
        let t = Tmp::new();
        let v = Mem::default();
        let tx = Transactions::new(&v, t.at("data"));
        let root = t.at("root");
        std::fs::create_dir_all(&root).unwrap();
        put(&tx, &Folder::empty(&root, "plug", MARK).unwrap(), "1").unwrap();
        let f = Folder::pinned(&plug(&root)).unwrap();
        // Once moved aside: its marker rewritten in place, so it is not the copy pinned, and a
        // folder put under its name, so it cannot go back.
        let r = root.clone();
        hook(Some(Box::new(move |s| {
            if s == Step::Moved {
                let aside = aside_in(&r).unwrap();
                std::fs::write(aside.join(MARK), "{\"v\": \"x\"}\n").unwrap();
                std::fs::create_dir(r.join("plug")).unwrap();
            }
            false
        })));
        let e = tx.remove_folder("s", &f).unwrap_err();
        hook(None);
        drop(f);
        let aside = aside_in(&root).unwrap();
        assert!(
            e.message.contains(&aside.display().to_string())
                && e.message.contains("next start puts it back"),
            "{}",
            e.message
        );
        // Not removed: all there, aside, and the journal open for the next start.
        assert_eq!(std::fs::read(aside.join("a.txt")).unwrap(), b"a 1\n");
        let open = v.open();
        assert_eq!(open.len(), 1);
        if !blocker_stays {
            std::fs::remove_dir(root.join("plug")).unwrap();
        }
        let reports = tx.reconcile(&open).unwrap();
        assert_eq!(reports[0]["outcome"], "rolled_back");
        if blocker_stays {
            // Still something under the name: left where it is, named in the report, and the
            // journal ended, so the tool still starts.
            assert_eq!(reports[0]["conflicts"], json!([aside]));
            assert!(aside.join("a.txt").exists());
        } else {
            assert_eq!(reports[0]["restored"], json!([root.join("plug")]));
            assert_eq!(
                std::fs::read(root.join("plug").join("a.txt")).unwrap(),
                b"a 1\n"
            );
            assert!(aside_in(&root).is_none());
        }
        assert!(v.open().is_empty());
    }
}

/// The hidden name a put wrote its new copy under in root, if any.
fn new_in(root: &Path) -> Option<PathBuf> {
    std::fs::read_dir(root)
        .unwrap()
        .map(|e| e.unwrap().path())
        .find(|p| {
            let n = p.file_name().unwrap().to_string_lossy();
            n.starts_with(".plug.pluxy-") && n.ends_with(".new")
        })
}

/// Puts another program's folder at at (whatever was there moved to taken first, when given),
/// holding x.txt.
fn swap_in(at: &Path, taken: Option<&Path>) {
    if let Some(to) = taken {
        std::fs::rename(at, to).unwrap();
    }
    std::fs::create_dir(at).unwrap();
    std::fs::write(at.join("x.txt"), "x").unwrap();
}

#[test]
fn a_hidden_copy_swapped_for_another_folder_is_left_as_it_is_and_named() {
    use Step::*;
    // Another program acts on a hidden name, or on the folder's own name, at a step: the operation
    // removes or puts back nothing but the folder of the identity it recorded, says so, and keeps
    // its journal open; the next start leaves the other folder as it is and names it.
    #[derive(Clone, Copy, Debug, PartialEq)]
    enum Act {
        /// The new copy moved away, another folder put at its hidden name.
        SwapNew,
        /// The new copy, once renamed in, moved away and another folder put under the name.
        SwapName,
        /// The old copy moved aside moved away, another folder put at its hidden name.
        SwapAside,
        /// The old copy's marker rewritten in place (same file, other bytes) once moved aside.
        RewriteOld,
    }
    use Act::*;
    for (op, step, act, outcome) in [
        ("replace", Checked, SwapNew, "rolled_back"),
        ("replace", Placed, SwapName, "rolled_back"),
        ("replace", Placed, SwapAside, "committed"),
        ("replace", Placed, RewriteOld, "committed"),
        ("remove", Moved, SwapAside, "rolled_back"),
        ("remove", Unmarked, SwapAside, "rolled_back"),
    ] {
        let case = format!("{op}: {act:?} at {step:?}");
        let t = Tmp::new();
        let v = Mem::default();
        let tx = Transactions::new(&v, t.at("data"));
        let root = t.at("root");
        std::fs::create_dir_all(&root).unwrap();
        put(&tx, &Folder::empty(&root, "plug", MARK).unwrap(), "1").unwrap();
        let f = Folder::pinned(&plug(&root)).unwrap();
        let (r, taken) = (root.clone(), t.at("taken"));
        // Where the other program's folder went.
        let other = std::rc::Rc::new(RefCell::new(None::<PathBuf>));
        let keep = other.clone();
        hook(Some(Box::new(move |s| {
            if s == step {
                let at = match act {
                    SwapNew => new_in(&r).unwrap(),
                    SwapName => r.join("plug"),
                    SwapAside | RewriteOld => aside_in(&r).unwrap(),
                };
                if act == RewriteOld {
                    std::fs::write(at.join(MARK), "{\"v\": \"x\"}\n").unwrap();
                } else {
                    swap_in(&at, Some(&taken));
                }
                *keep.borrow_mut() = Some(at);
            }
            false
        })));
        let done = match op {
            "remove" => tx.remove_folder("s", &f),
            _ => put(&tx, &f, "2"),
        };
        hook(None);
        drop(f);
        let at = other.borrow().clone().unwrap();
        let e = done.unwrap_err();
        assert!(!e.message.contains("stopped at"), "{case}: {}", e.message);
        // Left as it is: the other folder where it was put, or the old copy with its new marker.
        let intact = |when: &str| match act {
            RewriteOld => {
                assert_eq!(
                    std::fs::read(at.join("a.txt")).unwrap(),
                    b"a 1\n",
                    "{case}, {when}"
                );
                assert_eq!(
                    std::fs::read(at.join(MARK)).unwrap(),
                    b"{\"v\": \"x\"}\n",
                    "{case}, {when}"
                );
            }
            _ => assert_eq!(
                std::fs::read(at.join("x.txt")).unwrap(),
                b"x",
                "{case}, {when}"
            ),
        };
        intact("after the operation");
        // What the operation could not settle keeps its journal open for the next start.
        let open = v.open();
        assert_eq!(open.len(), 1, "{case}: {}", e.message);
        let reports = tx.reconcile(&open).unwrap();
        assert_eq!(reports[0]["outcome"], outcome, "{case}");
        // Named: the other folder at a hidden name, or (another folder under the name) the old
        // copy that cannot go back.
        let named = match act {
            SwapName => aside_in(&root).unwrap(),
            _ => at.clone(),
        };
        assert_eq!(reports[0]["conflicts"], json!([named]), "{case}");
        intact("after the next start");
        // The copy pinned is never lost: under its name, aside, or where the other program put it.
        let pinned_copy = match (op, act, step) {
            (_, SwapName, _) => aside_in(&root),
            (_, RewriteOld, _) => Some(at.clone()),
            ("remove", _, _) | ("replace", SwapAside, _) => Some(t.at("taken")),
            _ => Some(root.join("plug")),
        }
        .unwrap();
        assert_eq!(
            std::fs::read(pinned_copy.join("a.txt")).unwrap(),
            b"a 1\n",
            "{case}"
        );
        assert!(v.open().is_empty(), "{case}");
    }
}

/// Makes the folder at at impossible to empty for this account until the returned guard is
/// dropped: on Linux and macOS no write permission in it, on Windows a file in it held open with
/// no sharing. None when the account can write there all the same (root).
fn lock_folder(at: &Path) -> Option<Box<dyn std::any::Any>> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(at, std::fs::Permissions::from_mode(0o500)).unwrap();
        if std::fs::write(at.join(".probe"), "").is_ok() {
            let _ = std::fs::remove_file(at.join(".probe"));
            std::fs::set_permissions(at, std::fs::Permissions::from_mode(0o755)).unwrap();
            return None;
        }
        struct Unlock(PathBuf);
        impl Drop for Unlock {
            fn drop(&mut self) {
                let _ = std::fs::set_permissions(&self.0, std::fs::Permissions::from_mode(0o755));
            }
        }
        Some(Box::new(Unlock(at.to_path_buf())))
    }
    #[cfg(windows)]
    {
        use std::os::windows::fs::OpenOptionsExt;
        let file = std::fs::read_dir(at)
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path();
        let held = std::fs::OpenOptions::new()
            .read(true)
            .share_mode(0)
            .open(file)
            .unwrap();
        Some(Box::new(held))
    }
}

#[test]
fn a_new_copy_that_cannot_be_removed_keeps_its_journal_open_for_the_next_start() {
    for unlocked in [false, true] {
        let t = Tmp::new();
        let v = Mem::default();
        let tx = Transactions::new(&v, t.at("data"));
        let root = t.at("root");
        std::fs::create_dir_all(&root).unwrap();
        let f = Folder::empty(&root, "plug", MARK).unwrap();
        // Once the new copy is written: a folder put under its name (so the put is refused) and
        // the new copy's subfolder made impossible to empty (so it cannot be removed).
        let (r, guard) = (root.clone(), std::rc::Rc::new(RefCell::new(None)));
        let keep = guard.clone();
        hook(Some(Box::new(move |s| {
            if s == Step::Checked {
                *keep.borrow_mut() = Some(lock_folder(&new_in(&r).unwrap().join("sub")));
                std::fs::create_dir(r.join("plug")).unwrap();
            }
            false
        })));
        let e = put(&tx, &f, "1").unwrap_err();
        hook(None);
        drop(f);
        let Some(lock) = guard.borrow_mut().take().unwrap() else {
            println!("skipped: this account can empty a folder without write permission (root)");
            return;
        };
        let new = new_in(&root).unwrap();
        assert!(
            e.message.contains(&new.display().to_string()) && e.message.contains("next start"),
            "{}",
            e.message
        );
        // Not ended as rolled back with the new copy left unrecorded: open for the next start.
        let open = v.open();
        assert_eq!(open.len(), 1, "{}", e.message);
        // What could not be removed is still there (the removal may have taken the rest first,
        // in whatever order the folder lists them).
        assert_eq!(std::fs::read(new.join("sub/b.txt")).unwrap(), b"b 1\n");
        let mut lock = Some(lock);
        if unlocked {
            lock = None;
        }
        let reports = tx.reconcile(&open).unwrap();
        assert_eq!(reports[0]["outcome"], "rolled_back");
        if unlocked {
            assert_eq!(reports[0]["conflicts"], json!([]));
            assert!(new_in(&root).is_none());
        } else {
            // Still not removable: left where it is and named, the journal ended all the same.
            assert_eq!(reports[0]["conflicts"], json!([new]));
            assert!(new.join("sub/b.txt").exists());
        }
        drop(lock);
        assert!(v.open().is_empty());
        // The folder another program put under the name is left as it is.
        assert!(root.join("plug").is_dir());
    }
}
