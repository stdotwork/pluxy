use super::*;
use std::os::unix::fs::symlink;

struct Tmp(PathBuf);
impl Drop for Tmp {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}
fn tmp() -> Tmp {
    let d = std::env::temp_dir().join(format!("pluxy-access-{}", random_id().unwrap()));
    fs::create_dir_all(&d).unwrap();
    // Canonical, so targets compare as the broker resolves them.
    Tmp(fs::canonicalize(d).unwrap())
}

const SOURCE: &str = "/registries/signed.json";

fn signed() -> Signed {
    Signed {
        publisher: "example".into(),
        key_id: "k1".into(),
        public_key: "ab".repeat(32),
        source: SOURCE.into(),
        descriptor: "{}".into(),
        signature: "00".repeat(64),
    }
}

fn facts() -> Facts {
    Facts {
        id: "example/files".into(),
        enabled: true,
        source: Some(SOURCE.into()),
        sha256: "11".repeat(32),
        signed: Some(signed()),
        declared: vec![
            Declared {
                id: "config".into(),
                kind: "file".into(),
                operations: vec!["read".into()],
            },
            Declared {
                id: "tree".into(),
                kind: "subtree".into(),
                operations: vec!["list".into(), "read".into(), "write".into()],
            },
        ],
    }
}

/// A store in dir/app with the fixture signer approved.
fn store(dir: &Path) -> Store {
    let mut s = Store::open(&dir.join("app"));
    s.approve(&signed()).unwrap();
    s
}

fn grant(s: &mut Store, f: &Facts, resource: &str, path: &Path) -> Grant {
    let d = s.may_grant(f, resource).unwrap();
    let r = resolve(path, &d.kind).unwrap();
    s.add(f, &d, &r).unwrap()
}

/// One page call, as the broker makes it: begin, I/O, finish.
fn call(s: &mut Store, f: &Facts, method: &str, params: Value) -> Result<Value, Denial> {
    let b = s.begin(f, &f.sha256, method, &params)?;
    let (r, opened) = run(&b);
    let out = s.finish(&b, r, opened);
    s.delivered(&b);
    out
}

fn text(v: &Value) -> String {
    let b = base64::engine::general_purpose::STANDARD
        .decode(v["data"].as_str().unwrap())
        .unwrap();
    String::from_utf8(b).unwrap()
}

#[test]
fn file_grant_follows_a_chosen_link_and_reads_only_its_target() {
    let t = tmp();
    fs::create_dir(t.0.join("stow")).unwrap();
    fs::write(t.0.join("stow/config"), "config bytes").unwrap();
    fs::write(t.0.join("stow/sibling"), "sibling bytes").unwrap();
    symlink("stow/config", t.0.join("config")).unwrap();
    let (mut s, f) = (store(&t.0), facts());
    let g = grant(&mut s, &f, "config", &t.0.join("config"));
    assert_eq!(g.selected, t.0.join("config"));
    assert_eq!(g.target, t.0.join("stow/config"));
    assert_eq!(g.link.as_ref().unwrap().text, "stow/config");
    assert_eq!(g.operations, ["read"]);
    let r = call(&mut s, &f, "resources.read", json!({"resource": "config"})).unwrap();
    assert_eq!(text(&r), "config bytes");
    assert_eq!(r["eof"], true);
    assert_eq!(r["size"], 12);
    // Reading from an offset, and past the end.
    let r = call(
        &mut s,
        &f,
        "resources.read",
        json!({"resource": "config", "offset": 7}),
    )
    .unwrap();
    assert_eq!(text(&r), "bytes");
    let r = call(
        &mut s,
        &f,
        "resources.read",
        json!({"resource": "config", "offset": 99}),
    )
    .unwrap();
    assert_eq!((text(&r).as_str(), r["eof"].as_bool()), ("", Some(true)));
    // A path with a file grant, an undeclared resource, unknown parameters: bad requests.
    for p in [
        json!({"resource": "config", "path": "../sibling"}),
        json!({"resource": "undeclared"}),
        json!({"resource": "config", "extra": 1}),
        json!({"resource": "config", "offset": -1}),
        json!({"resource": 1}),
        json!([]),
    ] {
        assert_eq!(
            call(&mut s, &f, "resources.read", p.clone()).unwrap_err(),
            Denial::BadRequest,
            "{p}"
        );
    }
    assert_eq!(
        call(&mut s, &f, "resources.list", json!({"resource": "config"})).unwrap_err(),
        Denial::BadRequest
    );
    assert_eq!(s.in_flight(&g.id), 0);
}

#[test]
fn a_retargeted_link_or_replaced_target_ends_the_grant() {
    let t = tmp();
    fs::create_dir(t.0.join("stow")).unwrap();
    fs::write(t.0.join("stow/config"), "config").unwrap();
    fs::write(t.0.join("stow/sibling"), "sibling").unwrap();
    symlink("stow/config", t.0.join("config")).unwrap();
    let (mut s, f) = (store(&t.0), facts());
    grant(&mut s, &f, "config", &t.0.join("config"));
    let read = |s: &mut Store| call(s, &f, "resources.read", json!({"resource": "config"}));
    assert!(read(&mut s).is_ok());
    let relink = |to: &str| {
        let tmp = t.0.join("config.new");
        symlink(to, &tmp).unwrap();
        fs::rename(&tmp, t.0.join("config")).unwrap();
    };
    relink("stow/sibling");
    assert_eq!(read(&mut s).unwrap_err(), Denial::Changed);
    // Put back: still changed, until granted again.
    relink("stow/config");
    assert_eq!(read(&mut s).unwrap_err(), Denial::Changed);
    assert!(s.grants().next().unwrap().changed);
    grant(&mut s, &f, "config", &t.0.join("config"));
    assert_eq!(s.grants().count(), 1);
    assert!(read(&mut s).is_ok());
    // The target replaced by rename, as editors save: changed.
    fs::write(t.0.join("stow/new"), "replaced").unwrap();
    fs::rename(t.0.join("stow/new"), t.0.join("stow/config")).unwrap();
    assert_eq!(read(&mut s).unwrap_err(), Denial::Changed);
    // A plain file swapped for a link is a change too.
    fs::write(t.0.join("plain"), "plain").unwrap();
    grant(&mut s, &f, "config", &t.0.join("plain"));
    assert!(read(&mut s).is_ok());
    fs::remove_file(t.0.join("plain")).unwrap();
    symlink("stow/sibling", t.0.join("plain")).unwrap();
    assert_eq!(read(&mut s).unwrap_err(), Denial::Changed);
    // Gone: unavailable.
    fs::write(t.0.join("gone"), "gone").unwrap();
    grant(&mut s, &f, "config", &t.0.join("gone"));
    fs::remove_file(t.0.join("gone")).unwrap();
    assert_eq!(read(&mut s).unwrap_err(), Denial::Unavailable);
}

#[test]
fn a_folder_grant_stays_inside_its_folder() {
    let t = tmp();
    let tree = t.0.join("tree");
    fs::create_dir_all(tree.join("inner")).unwrap();
    fs::write(tree.join("top.txt"), "top").unwrap();
    fs::write(tree.join("inner/ok.txt"), "inner").unwrap();
    fs::write(t.0.join("outside.txt"), "outside").unwrap();
    symlink("inner/ok.txt", tree.join("inside")).unwrap();
    symlink("../outside.txt", tree.join("escape")).unwrap();
    symlink(t.0.join("outside.txt"), tree.join("absolute")).unwrap();
    symlink("..", tree.join("up")).unwrap();
    let (mut s, f) = (store(&t.0), facts());
    let g = grant(&mut s, &f, "tree", &tree);
    // Only the operations this app can grant.
    assert_eq!(g.operations, ["list", "read"]);
    let read = |s: &mut Store, p: &str| {
        call(
            s,
            &f,
            "resources.read",
            json!({"resource": "tree", "path": p}),
        )
    };
    assert_eq!(text(&read(&mut s, "top.txt").unwrap()), "top");
    assert_eq!(text(&read(&mut s, "inner/ok.txt").unwrap()), "inner");
    assert_eq!(text(&read(&mut s, "inside").unwrap()), "inner");
    let outside = t.0.join("outside.txt").display().to_string();
    for p in [
        "../outside.txt",
        "inner/../../outside.txt",
        outside.as_str(),
        "escape",
        "absolute",
        "up/outside.txt",
        "./top.txt",
        "inner//ok.txt",
        "",
        "inner",
    ] {
        assert_eq!(read(&mut s, p).unwrap_err(), Denial::Refused, "{p}");
    }
    assert_eq!(read(&mut s, "missing").unwrap_err(), Denial::Unavailable);
    assert_eq!(
        call(&mut s, &f, "resources.read", json!({"resource": "tree"})).unwrap_err(),
        Denial::BadRequest
    );
    let list = call(
        &mut s,
        &f,
        "resources.list",
        json!({"resource": "tree", "path": ""}),
    )
    .unwrap();
    let names: Vec<(&str, &str)> = list["entries"]
        .as_array()
        .unwrap()
        .iter()
        .map(|e| (e["name"].as_str().unwrap(), e["kind"].as_str().unwrap()))
        .collect();
    assert_eq!(
        names,
        [
            ("absolute", "link"),
            ("escape", "link"),
            ("inner", "folder"),
            ("inside", "link"),
            ("top.txt", "file"),
            ("up", "link")
        ]
    );
    let inner = call(
        &mut s,
        &f,
        "resources.list",
        json!({"resource": "tree", "path": "inner"}),
    )
    .unwrap();
    assert_eq!(inner["entries"][0]["name"], "ok.txt");
    assert_eq!(inner["entries"][0]["size"], 5);
    for p in ["..", "up", "escape"] {
        assert!(call(
            &mut s,
            &f,
            "resources.list",
            json!({"resource": "tree", "path": p})
        )
        .is_err());
    }
    // The folder itself replaced: changed.
    fs::rename(&tree, t.0.join("tree.old")).unwrap();
    fs::create_dir(&tree).unwrap();
    assert_eq!(read(&mut s, "top.txt").unwrap_err(), Denial::Changed);
}

#[test]
fn grants_bind_package_release_source_and_signer() {
    let t = tmp();
    fs::write(t.0.join("file"), "bytes").unwrap();
    let (mut s, f) = (store(&t.0), facts());
    grant(&mut s, &f, "config", &t.0.join("file"));
    let read =
        |s: &mut Store, f: &Facts| call(s, f, "resources.read", json!({"resource": "config"}));
    assert!(read(&mut s, &f).is_ok());
    // Another package with the same resource ID.
    let mut other = facts();
    other.id = "example/other".into();
    assert_eq!(read(&mut s, &other).unwrap_err(), Denial::NotGranted);
    // A newer release of the same package.
    let mut newer = facts();
    newer.sha256 = "22".repeat(32);
    assert_eq!(read(&mut s, &newer).unwrap_err(), Denial::NotGranted);
    // A page of a release other than the active one.
    let b = s.begin(
        &f,
        &"22".repeat(32),
        "resources.read",
        &json!({"resource": "config"}),
    );
    assert_eq!(b.err(), Some(Denial::NotGranted));
    // Disabled.
    let mut off = facts();
    off.enabled = false;
    assert_eq!(read(&mut s, &off).unwrap_err(), Denial::NotGranted);
    // The same release from another source: its signer is not approved there.
    let mut mirror = facts();
    mirror.source = Some("/registries/mirror.json".into());
    mirror.signed.as_mut().unwrap().source = "/registries/mirror.json".into();
    assert_eq!(read(&mut s, &mirror).unwrap_err(), Denial::NotGranted);
    assert!(s
        .may_grant(&mirror, "config")
        .unwrap_err()
        .contains("signer"));
    // Another key from the same publisher and source.
    let mut rekeyed = facts();
    rekeyed.signed.as_mut().unwrap().public_key = "cd".repeat(32);
    assert_eq!(read(&mut s, &rekeyed).unwrap_err(), Denial::NotGranted);
    // Unsigned, or a signature naming another source than the package's.
    let mut unsigned = facts();
    unsigned.signed = None;
    assert!(s
        .may_grant(&unsigned, "config")
        .unwrap_err()
        .contains("not signed"));
    let mut elsewhere = facts();
    elsewhere.source = Some("/registries/mirror.json".into());
    assert!(s.may_grant(&elsewhere, "config").is_err());
    assert_eq!(read(&mut s, &elsewhere).unwrap_err(), Denial::NotGranted);
    assert!(read(&mut s, &f).is_ok());
    // Removing the package removes its grants.
    s.remove_package("example/files").unwrap();
    assert_eq!(s.grants().count(), 0);
    assert_eq!(read(&mut s, &f).unwrap_err(), Denial::NotGranted);
}

#[test]
fn revoking_answers_calls_in_flight_revoked() {
    let t = tmp();
    fs::write(t.0.join("file"), "bytes").unwrap();
    let (mut s, f) = (store(&t.0), facts());
    let g = grant(&mut s, &f, "config", &t.0.join("file"));
    let b = s
        .begin(
            &f,
            &f.sha256,
            "resources.read",
            &json!({"resource": "config"}),
        )
        .unwrap();
    let (r, opened) = run(&b);
    assert!(r.is_ok());
    assert_eq!(s.revoke(&g.id).unwrap(), 1);
    assert_eq!(s.finish(&b, r, opened).unwrap_err(), Denial::Revoked);
    assert_eq!(s.in_flight(&g.id), 1);
    s.delivered(&b);
    assert_eq!(s.in_flight(&g.id), 0);
    assert!(s.revoke(&g.id).is_err());
    assert_eq!(
        call(&mut s, &f, "resources.read", json!({"resource": "config"})).unwrap_err(),
        Denial::NotGranted
    );
    // A grant replaced while a call was in flight: that call is revoked too.
    let g = grant(&mut s, &f, "config", &t.0.join("file"));
    let b = s
        .begin(
            &f,
            &f.sha256,
            "resources.read",
            &json!({"resource": "config"}),
        )
        .unwrap();
    grant(&mut s, &f, "config", &t.0.join("file"));
    let (r, opened) = run(&b);
    assert_eq!(s.finish(&b, r, opened).unwrap_err(), Denial::Revoked);
    s.delivered(&b);
    assert_eq!(s.in_flight(&g.id), 0);
}

#[test]
fn saved_grants_survive_a_restart_and_session_only_ones_do_not() {
    let t = tmp();
    fs::write(t.0.join("file"), "bytes").unwrap();
    let f = facts();
    let mut s = store(&t.0);
    grant(&mut s, &f, "config", &t.0.join("file"));
    // A document portal file, as resolve marks it.
    let d = s.may_grant(&f, "tree").unwrap();
    fs::create_dir(t.0.join("dir")).unwrap();
    let mut r = resolve(&t.0.join("dir"), "subtree").unwrap();
    r.portal = true;
    let portal = s.add(&f, &d, &r).unwrap();
    assert!(portal.session_only);
    assert_eq!(s.grants().count(), 2);
    let saved = fs::read_to_string(t.0.join("app/access.json")).unwrap();
    assert!(!saved.contains(&portal.id) && saved.contains("\"config\""));
    let mode = fs::metadata(t.0.join("app/access.json"))
        .unwrap()
        .permissions()
        .mode();
    assert_eq!(mode & 0o777, 0o600);

    let mut again = Store::open(&t.0.join("app"));
    assert!(again.warning.is_none());
    assert_eq!(again.signers().len(), 1);
    assert_eq!(again.grants().count(), 1);
    let r = call(
        &mut again,
        &f,
        "resources.read",
        json!({"resource": "config"}),
    )
    .unwrap();
    assert_eq!(text(&r), "bytes");
    let status = again.status(&f, &f.sha256);
    assert_eq!(status["resources"][0]["granted"], true);
    assert_eq!(status["resources"][1]["granted"], false);
    // A file replaced while the app was not running is refused when it starts again.
    fs::write(t.0.join("new"), "new").unwrap();
    fs::rename(t.0.join("new"), t.0.join("file")).unwrap();
    let mut third = Store::open(&t.0.join("app"));
    assert_eq!(
        call(
            &mut third,
            &f,
            "resources.read",
            json!({"resource": "config"})
        )
        .unwrap_err(),
        Denial::Changed
    );
}

#[test]
fn a_damaged_file_is_moved_aside() {
    let t = tmp();
    let dir = t.0.join("config");
    fs::create_dir(&dir).unwrap();
    fs::write(dir.join("access.json"), "{not json").unwrap();
    let s = Store::open(&dir);
    assert!(s.warning.as_deref().unwrap().contains("moved to"));
    assert_eq!(s.grants().count(), 0);
    assert!(!dir.join("access.json").exists());
    assert!(fs::read_dir(&dir).unwrap().any(|e| e
        .unwrap()
        .file_name()
        .to_string_lossy()
        .starts_with(ASIDE)));
    fs::write(
        dir.join("access.json"),
        r#"{"version":9,"revision":0,"signers":[],"grants":[]}"#,
    )
    .unwrap();
    assert!(Store::open(&dir)
        .warning
        .unwrap()
        .contains("version 9 is not supported"));
    // A link in its place is not followed.
    fs::write(
        t.0.join("elsewhere"),
        r#"{"version":0,"revision":0,"signers":[],"grants":[]}"#,
    )
    .unwrap();
    symlink(t.0.join("elsewhere"), dir.join("access.json")).unwrap();
    assert!(Store::open(&dir).warning.is_some());
}

#[test]
fn what_can_be_chosen() {
    let t = tmp();
    fs::write(t.0.join("file"), "x").unwrap();
    assert!(resolve(&t.0.join("file"), "subtree").is_err());
    assert!(resolve(&t.0, "file").is_err());
    assert!(resolve(Path::new("relative"), "file").is_err());
    assert!(resolve(Path::new("/"), "subtree")
        .unwrap_err()
        .contains("whole home"));
    assert!(resolve(&t.0.join("missing"), "file").is_err());
    let r = resolve(&t.0.join("file"), "file").unwrap();
    assert!(!r.portal && r.link.is_none());
    // A path under this session's document portal mount.
    let mount = portal_mount();
    assert!(is_portal(&mount.join("abc").join("file")));
    assert!(!is_portal(&t.0.join("file")));
    let undeclared = s_without_ops();
    assert!(undeclared.contains("none of its operations"));
}

fn s_without_ops() -> String {
    let t = tmp();
    let s = store(&t.0);
    let mut f = facts();
    f.declared[0].operations = vec!["write".into()];
    s.may_grant(&f, "config").unwrap_err()
}

#[test]
fn a_folder_holding_the_home_folder_or_the_portal_store_is_refused() {
    // The process's own HOME: its parent holds it, so granting the parent grants all of it.
    let home = fs::canonicalize(std::env::var_os("HOME").expect("HOME")).unwrap();
    let parent = home.parent().unwrap();
    assert!(resolve(parent, "subtree")
        .unwrap_err()
        .contains("whole home"));
    assert!(resolve(&home, "subtree")
        .unwrap_err()
        .contains("whole home"));
    // The document portal's store, and every folder holding it.
    let t = tmp();
    let runtime = t.0.join("runtime");
    let doc = runtime.join("doc");
    fs::create_dir_all(doc.join("abc")).unwrap();
    fs::create_dir_all(t.0.join("work")).unwrap();
    for (target, refused) in [
        (&runtime, true),
        (&doc, true),
        (&t.0, true),
        (&doc.join("abc"), false),
        (&t.0.join("work"), false),
    ] {
        let r = resolve_with(target, "subtree", Some(&home), &doc);
        assert_eq!(r.is_err(), refused, "{}: {r:?}", target.display());
    }
    // With home inside the temporary folder: it and its parents are refused, a folder in it is
    // not.
    let fake_home = t.0.join("work").join("me");
    fs::create_dir_all(fake_home.join("projects")).unwrap();
    for (target, refused) in [
        (fake_home.clone(), true),
        (t.0.join("work"), true),
        (fake_home.join("projects"), false),
    ] {
        let r = resolve_with(&target, "subtree", Some(&fake_home), &doc);
        assert_eq!(r.is_err(), refused, "{}: {r:?}", target.display());
    }
}

#[test]
fn revoking_waits_until_a_read_is_delivered() {
    let t = tmp();
    fs::write(t.0.join("file"), "bytes").unwrap();
    let (mut s, f) = (store(&t.0), facts());
    let g = grant(&mut s, &f, "config", &t.0.join("file"));
    let b = s
        .begin(
            &f,
            &f.sha256,
            "resources.read",
            &json!({"resource": "config"}),
        )
        .unwrap();
    let (r, opened) = run(&b);
    assert!(s.finish(&b, r, opened).is_ok());
    // Read, not yet delivered to the page: a revocation still waits for it.
    assert_eq!(s.in_flight(&g.id), 1);
    s.delivered(&b);
    assert_eq!(s.in_flight(&g.id), 0);
}

#[test]
fn a_passing_error_does_not_end_a_grant() {
    if unsafe { libc::geteuid() } == 0 {
        // Permissions do not stop root.
        return;
    }
    let t = tmp();
    let dir = t.0.join("dir");
    fs::create_dir(&dir).unwrap();
    fs::write(dir.join("file"), "bytes").unwrap();
    let (mut s, f) = (store(&t.0), facts());
    let g = grant(&mut s, &f, "config", &dir.join("file"));
    let outer = t.0.join("outer");
    fs::create_dir_all(outer.join("tree")).unwrap();
    fs::write(outer.join("tree").join("in"), "in").unwrap();
    grant(&mut s, &f, "tree", &outer.join("tree"));
    // The folder holding the file cannot be searched for now (EACCES): unavailable, not changed.
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o000)).unwrap();
    let e = call(&mut s, &f, "resources.read", json!({"resource": "config"})).unwrap_err();
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o700)).unwrap();
    assert_eq!(e, Denial::Unavailable);
    let r = call(&mut s, &f, "resources.read", json!({"resource": "config"})).unwrap();
    assert_eq!(text(&r), "bytes");
    assert!(s.grants().all(|x| !x.changed), "{:?}", g.id);
    // The same for a granted folder whose own folder cannot be searched for now.
    fs::set_permissions(&outer, fs::Permissions::from_mode(0o000)).unwrap();
    let e = call(
        &mut s,
        &f,
        "resources.read",
        json!({"resource": "tree", "path": "in"}),
    )
    .unwrap_err();
    fs::set_permissions(&outer, fs::Permissions::from_mode(0o700)).unwrap();
    assert_eq!(e, Denial::Unavailable);
    let r = call(
        &mut s,
        &f,
        "resources.read",
        json!({"resource": "tree", "path": "in"}),
    )
    .unwrap();
    assert_eq!(text(&r), "in");
    assert!(s.grants().all(|x| !x.changed));
    assert!(!fs::read_to_string(t.0.join("app").join(FILE))
        .unwrap()
        .contains("\"changed\": true"));
}

/// Records mount for every grant of the store, as if the targets were granted on that mount.
fn granted_on(s: &mut Store, mount: Option<u64>) {
    for g in s.saved.grants.iter_mut() {
        g.mount = mount;
    }
}

#[cfg(target_os = "linux")]
#[test]
fn reads_never_cross_onto_another_mount() {
    let t = tmp();
    fs::write(t.0.join("file"), "bytes").unwrap();
    fs::create_dir(t.0.join("tree")).unwrap();
    fs::write(t.0.join("tree").join("in"), "in").unwrap();
    let (mut s, f) = (store(&t.0), facts());
    let g = grant(&mut s, &f, "config", &t.0.join("file"));
    let mount = g.mount.expect("a Linux grant records its mount");
    let read_in = json!({"resource": "tree", "path": "in"});
    grant(&mut s, &f, "tree", &t.0.join("tree"));
    // The positive control: on the mount they were granted on, both read.
    assert_eq!(
        text(&call(&mut s, &f, "resources.read", read_in.clone()).unwrap()),
        "in"
    );
    // The folder is open now; a file in it on another mount than the grant's is refused.
    granted_on(&mut s, Some(mount + 1));
    assert_eq!(
        call(&mut s, &f, "resources.read", read_in.clone()).unwrap_err(),
        Denial::Refused
    );
    // Opening a target on another mount than the one granted: changed.
    assert_eq!(
        call(&mut s, &f, "resources.read", json!({"resource": "config"})).unwrap_err(),
        Denial::Changed
    );
    let mut fresh = store(&t.0);
    grant(&mut fresh, &f, "tree", &t.0.join("tree"));
    granted_on(&mut fresh, Some(mount + 1));
    assert_eq!(
        call(
            &mut fresh,
            &f,
            "resources.list",
            json!({"resource": "tree", "path": ""})
        )
        .unwrap_err(),
        Denial::Changed
    );
}

#[test]
fn a_listing_stops_at_its_limit_and_says_so() {
    let t = tmp();
    let tree = t.0.join("tree");
    fs::create_dir(&tree).unwrap();
    for i in 0..MAX_ENTRIES {
        fs::write(tree.join(format!("f{i:04}")), "").unwrap();
    }
    let (mut s, f) = (store(&t.0), facts());
    grant(&mut s, &f, "tree", &tree);
    let list = |s: &mut Store| {
        call(
            s,
            &f,
            "resources.list",
            json!({"resource": "tree", "path": ""}),
        )
        .unwrap()
    };
    let r = list(&mut s);
    assert_eq!(
        (r["entries"].as_array().unwrap().len(), &r["truncated"]),
        (MAX_ENTRIES, &json!(false))
    );
    fs::write(tree.join("one-more"), "").unwrap();
    let r = list(&mut s);
    assert_eq!(
        (r["entries"].as_array().unwrap().len(), &r["truncated"]),
        (MAX_ENTRIES, &json!(true))
    );
}

#[cfg(target_os = "linux")]
#[test]
fn the_person_asks_in_the_main_window() {
    let f = facts();
    let a = super::super::confirmation(&f, "Grant".into(), vec![PathBuf::from("/x")]);
    // The request comes from the app's main window, never from the package itself; the package
    // is what the action is for.
    assert_eq!(a.chain.len(), 1);
    assert_eq!(a.chain[0].id, "main window");
    assert_eq!(a.chain[0].digest, "");
    assert_eq!(a.executor.id, "example/files");
    assert_eq!(a.executor.digest, f.sha256);
    assert!(a.executor.signer.contains(&"ab".repeat(32)));
    assert!(!a.native_tool && a.launch.is_none());
    assert_eq!(a.files, [PathBuf::from("/x")]);
}
