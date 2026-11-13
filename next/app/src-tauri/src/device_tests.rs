use super::*;
use std::io::Write;

/// No native-tier root: what a normal build has.
struct NoRoots;
impl crate::artifact::PublisherKeys for NoRoots {
    fn publisher_key(
        &self,
        _: &str,
        _: &str,
        _: &str,
    ) -> Result<ed25519_dalek::VerifyingKey, String> {
        Err("native-tier trust not configured".into())
    }
}
impl Roots for NoRoots {
    fn configured(&self) -> bool {
        false
    }
    fn curated(&self, _: &str) -> bool {
        false
    }
    fn reserved(&self, _: &str) -> bool {
        false
    }
}

fn temp() -> PathBuf {
    let mut rnd = [0u8; 8];
    getrandom::fill(&mut rnd).unwrap();
    let base = std::env::var_os("CARGO_TARGET_TMPDIR")
        .map(PathBuf::from)
        .unwrap_or_else(std::env::temp_dir);
    let d = base.join(format!("pluxy-device-{}", hex(&rnd)));
    fs::create_dir_all(&d).unwrap();
    d
}

fn dirs(root: &Path) -> Dirs {
    Dirs {
        root: root.join("device-packages"),
        registries: root.join("device-registries.json"),
    }
}

fn manifest(id: &str, version: &str, extra: Value) -> Vec<u8> {
    let mut m = serde_json::json!({
        "manifest_version": 1, "id": id, "version": version,
        "app": {"version": 0, "tier": "web", "host_versions": [0],
            "ui": {"version": 0, "dir": "pages", "pages": [{"id": "p", "title": "P", "short": "P", "path": "index.html"}]}}
    });
    if let Value::Object(o) = extra {
        for (k, v) in o {
            m["app"][k] = v;
        }
    }
    serde_json::to_vec(&m).unwrap()
}

/// A gzip-compressed tar of (name, bytes) files, with names written into the header as given.
fn archive(files: &[(&str, &[u8])]) -> Vec<u8> {
    let mut b = tar::Builder::new(Vec::new());
    for (name, data) in files {
        let mut h = tar::Header::new_old();
        h.as_old_mut().name[..name.len()].copy_from_slice(name.as_bytes());
        h.set_size(data.len() as u64);
        h.set_mode(0o644);
        h.set_entry_type(tar::EntryType::Regular);
        h.set_cksum();
        b.append(&h, *data).unwrap();
    }
    let tar = b.into_inner().unwrap();
    let mut gz = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::fast());
    gz.write_all(&tar).unwrap();
    gz.finish().unwrap()
}

fn package(id: &str, version: &str) -> Vec<u8> {
    let m = manifest(id, version, Value::Null);
    archive(&[
        ("pluxy-extension.json", &m),
        ("pages/index.html", format!("<p>{version}").as_bytes()),
    ])
}

fn local(name: &str) -> Source {
    Source {
        kind: "local".into(),
        name: name.into(),
        index: None,
    }
}

fn registry(name: &str, index: &str) -> Source {
    Source {
        kind: "registry".into(),
        name: name.into(),
        index: Some(index.into()),
    }
}

fn install(c: &mut Catalog, bytes: &[u8], source: Source) -> Result<(), String> {
    let cand = stage(&c.dirs, bytes, source, None, &NoRoots)?;
    c.commit(cand)
}

fn store_names(d: &Dirs) -> BTreeSet<String> {
    fs::read_dir(d.store())
        .map(|r| {
            r.flatten()
                .map(|e| e.file_name().into_string().unwrap())
                .collect()
        })
        .unwrap_or_default()
}

fn digest(b: &[u8]) -> String {
    hex(&Sha256::digest(b))
}

#[test]
fn shared_manifest_fixtures_agree_with_core() {
    let dir = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../protocol/testdata/app-manifests");
    let cases: Vec<Value> =
        serde_json::from_slice(&fs::read(dir.join("cases.json")).unwrap()).unwrap();
    assert!(cases.len() >= 10, "the shared cases were not read");
    for case in cases {
        let file = case["file"].as_str().unwrap();
        let m: Manifest = serde_json::from_slice(&fs::read(dir.join(file)).unwrap()).unwrap();
        let got = validate(&m);
        assert_eq!(
            got.is_ok(),
            case["manifest_valid"] == true,
            "{file}: {:?}",
            got.err()
        );
    }
}

#[test]
fn ids_versions_and_paths() {
    for ok in ["example/notes", "a.b/c-d", "0/9"] {
        assert!(valid_id(ok), "{ok}");
    }
    for bad in [
        "example",
        "Example/notes",
        "-a/b",
        "a/b/c",
        "a/",
        "/b",
        "a/.b",
    ] {
        assert!(!valid_id(bad), "{bad}");
    }
    assert!(valid_version("1.0.0+b_1-x"));
    assert!(!valid_version(".1") && !valid_version("") && !valid_version(&"1".repeat(65)));
    for bad in ["", "/a", "a/../b", "..", "a//b", "./a", "a\\b"] {
        assert!(!local_path(bad), "{bad}");
    }
    assert!(local_path("a/b.html"));
}

#[test]
fn install_swap_failed_candidate_rollback_and_remove() {
    let root = temp();
    let d = dirs(&root);
    let mut c = Catalog::open(d.clone(), false);
    let v1 = package("example/notes", "1.0.0");
    let v2 = package("example/notes", "2.0.0");
    install(&mut c, &v1, local("notes-1.tar.gz")).unwrap();
    let p = c.get("example/notes").unwrap();
    assert_eq!(p.active.sha256, digest(&v1));
    assert!(p.enabled && p.last_good.is_none());
    assert_eq!(p.active.files.len(), 1);
    let (url, folder, title) = c.page("example/notes", "p").unwrap();
    assert_eq!(
        url,
        format!(
            "pluxy-app://localhost/example/notes/{}/index.html",
            digest(&v1)
        )
    );
    assert!(url.starts_with(&folder) && title == "P");
    assert_eq!(
        c.asset("example/notes", &digest(&v1), "index.html")
            .unwrap()
            .0,
        b"<p>1.0.0"
    );

    install(&mut c, &v2, local("notes-1.tar.gz")).unwrap();
    let p = c.get("example/notes").unwrap();
    assert_eq!(p.active.sha256, digest(&v2));
    assert_eq!(p.last_good.as_ref().unwrap().sha256, digest(&v1));
    assert!(c
        .asset("example/notes", &digest(&v1), "index.html")
        .is_none());
    assert_eq!(store_names(&d), BTreeSet::from([digest(&v1), digest(&v2)]));

    // Candidates that fail leave the catalog and store as they were.
    let bad_host = archive(&[
        (
            "pluxy-extension.json",
            &manifest(
                "example/notes",
                "3.0.0",
                serde_json::json!({"host_versions": [1]}),
            ),
        ),
        ("pages/index.html", b"x"),
    ]);
    let e = install(&mut c, &bad_host, local("notes-1.tar.gz")).unwrap_err();
    assert!(e.contains("host contract"), "{e}");
    let other_source = package("example/notes", "4.0.0");
    let e = install(
        &mut c,
        &other_source,
        registry("r", "/elsewhere/index.json"),
    )
    .unwrap_err();
    assert!(e.contains("remove it first"), "{e}");
    let e = install(&mut c, &v2, local("notes-1.tar.gz")).unwrap_err();
    assert!(e.contains("already installed"), "{e}");
    assert_eq!(c.get("example/notes").unwrap().active.sha256, digest(&v2));
    assert_eq!(store_names(&d), BTreeSet::from([digest(&v1), digest(&v2)]));
    assert!(fs::read_dir(d.staging()).unwrap().next().is_none());

    // The catalog persists; roll back makes 1.0.0 active again.
    let mut c = Catalog::open(d.clone(), false);
    c.rollback("example/notes").unwrap();
    let p = c.get("example/notes").unwrap();
    assert_eq!(p.active.sha256, digest(&v1));
    assert_eq!(p.last_good.as_ref().unwrap().sha256, digest(&v2));

    c.set_enabled("example/notes", false).unwrap();
    assert!(c.page("example/notes", "p").is_err());
    assert!(c
        .asset("example/notes", &digest(&v1), "index.html")
        .is_none());
    c.set_enabled("example/notes", true).unwrap();

    c.remove("example/notes").unwrap();
    assert!(c.get("example/notes").is_none() && store_names(&d).is_empty());
    assert!(Catalog::open(d, false).view().packages.is_empty());
    done(root);
}

#[test]
fn reserved_native_and_other_packages_are_refused_before_the_store() {
    let root = temp();
    let d = dirs(&root);
    let mut c = Catalog::open(d.clone(), false);
    let e = install(&mut c, &package("pluxy/notes", "1.0.0"), local("x.tar.gz")).unwrap_err();
    assert!(e.contains("reserved"), "{e}");
    // The positive control on the same route.
    install(
        &mut c,
        &package("example/notes", "1.0.0"),
        local("x.tar.gz"),
    )
    .unwrap();

    let native = archive(&[(
        "pluxy-extension.json",
        &serde_json::to_vec(&serde_json::json!({
            "manifest_version": 1, "id": "example/tool", "version": "1",
            "app": {"version": 0, "tier": "native", "host_versions": [0],
                "programs": {"linux/amd64": "bin/t"},
                "operations": [{"id": "x", "description": "d", "input_schema": {}, "output_schema": {}}],
                "state": {"version": 0, "readable_versions": [0]}}
        }))
        .unwrap(),
    )]);
    let e = install(&mut c, &native, local("t.tar.gz")).unwrap_err();
    assert!(e.contains("native tool"), "{e}");
    let provider = archive(&[(
        "pluxy-extension.json",
        br#"{"manifest_version":0,"id":"example/p","version":"1","protocol":[0],"programs":{"linux/amd64":"p"}}"#,
    )]);
    let e = install(&mut c, &provider, local("p.tar.gz")).unwrap_err();
    assert!(e.contains("install it in core"), "{e}");
    let missing_page = archive(&[(
        "pluxy-extension.json",
        &manifest("example/x", "1", Value::Null),
    )]);
    assert!(install(&mut c, &missing_page, local("m.tar.gz")).is_err());
    for (name, data) in [
        ("../evil", b"x".as_slice()),
        ("/abs", b"x".as_slice()),
        ("pages/../../x", b"x".as_slice()),
    ] {
        let a = archive(&[
            (
                "pluxy-extension.json",
                &manifest("example/y", "1", Value::Null),
            ),
            ("pages/index.html", b"x"),
            (name, data),
        ]);
        let e = install(&mut c, &a, local("y.tar.gz")).unwrap_err();
        assert!(e.contains("leaves the package folder"), "{name}: {e}");
    }
    assert!(install(&mut c, b"not gzip", local("z.tar.gz")).is_err());
    assert_eq!(store_names(&d).len(), 1);
    assert!(fs::read_dir(d.staging()).unwrap().next().is_none());
    done(root);
}

#[test]
fn links_devices_and_duplicates_are_refused() {
    let root = temp();
    let d = dirs(&root);
    let m = manifest("example/l", "1", Value::Null);
    for kind in [
        tar::EntryType::Symlink,
        tar::EntryType::Link,
        tar::EntryType::Char,
        tar::EntryType::Fifo,
    ] {
        let mut b = tar::Builder::new(Vec::new());
        let mut h = tar::Header::new_gnu();
        h.set_size(m.len() as u64);
        h.set_mode(0o644);
        h.set_path("pluxy-extension.json").unwrap();
        h.set_cksum();
        b.append(&h, m.as_slice()).unwrap();
        let mut h = tar::Header::new_gnu();
        h.set_entry_type(kind);
        h.set_size(0);
        h.set_path("pages/index.html").unwrap();
        h.set_link_name("/etc/passwd").unwrap();
        h.set_cksum();
        b.append(&h, std::io::empty()).unwrap();
        let mut gz = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::fast());
        gz.write_all(&b.into_inner().unwrap()).unwrap();
        let e = stage(&d, &gz.finish().unwrap(), local("l"), None, &NoRoots).unwrap_err();
        assert!(e.contains("not a regular file or folder"), "{kind:?}: {e}");
    }
    let dup = archive(&[
        ("pluxy-extension.json", &m),
        ("pages/index.html", b"a"),
        ("pages/index.html", b"b"),
    ]);
    assert!(stage(&d, &dup, local("d"), None, &NoRoots).is_err());
    done(root);
}

#[test]
fn changed_files_are_not_served_or_rolled_back_to() {
    let root = temp();
    let d = dirs(&root);
    let mut c = Catalog::open(d.clone(), false);
    let v1 = package("example/notes", "1.0.0");
    install(&mut c, &v1, local("n")).unwrap();
    install(&mut c, &package("example/notes", "2.0.0"), local("n")).unwrap();
    let file = d.store().join(digest(&v1)).join("pages/index.html");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(file.parent().unwrap(), fs::Permissions::from_mode(0o700)).unwrap();
        fs::set_permissions(&file, fs::Permissions::from_mode(0o600)).unwrap();
    }
    fs::write(&file, b"tampered").unwrap();
    let e = c.rollback("example/notes").unwrap_err();
    assert!(e.contains("changed since it was installed"), "{e}");
    assert_eq!(c.get("example/notes").unwrap().active.version, "2.0.0");
    // A file changed in the active release is not served.
    c.remove("example/notes").unwrap();
    install(&mut c, &v1, local("n")).unwrap();
    assert!(c
        .asset("example/notes", &digest(&v1), "index.html")
        .is_some());
    let file = d.store().join(digest(&v1)).join("pages/index.html");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(file.parent().unwrap(), fs::Permissions::from_mode(0o700)).unwrap();
        fs::set_permissions(&file, fs::Permissions::from_mode(0o600)).unwrap();
    }
    fs::write(&file, b"tampered").unwrap();
    assert!(c
        .asset("example/notes", &digest(&v1), "index.html")
        .is_none());
    assert!(c
        .asset("example/notes", &digest(&v1), "pluxy-extension.json")
        .is_none());
    done(root);
}

#[test]
fn safe_mode_lists_but_never_serves() {
    let root = temp();
    let d = dirs(&root);
    let mut c = Catalog::open(d.clone(), false);
    let v1 = package("example/notes", "1.0.0");
    install(&mut c, &v1, local("n")).unwrap();
    let safe = Catalog::open(d, true);
    let v = safe.view();
    assert!(v.safe_mode && v.packages.len() == 1);
    assert!(safe
        .page("example/notes", "p")
        .unwrap_err()
        .contains("safe mode"));
    assert!(safe
        .asset("example/notes", &digest(&v1), "index.html")
        .is_none());
    done(root);
}

#[test]
fn a_damaged_catalog_is_moved_aside() {
    let root = temp();
    let d = dirs(&root);
    fs::create_dir_all(&d.root).unwrap();
    fs::write(d.catalog(), b"{not json").unwrap();
    let c = Catalog::open(d.clone(), false);
    assert!(c.warning.as_deref().is_some_and(|w| w.contains("moved")));
    assert!(c.view().packages.is_empty());
    assert!(!d.catalog().exists());
    done(root);
}

#[test]
fn registry_pins_reserved_ids_and_paths() {
    let root = temp();
    let d = dirs(&root);
    let reg = root.join("reg");
    fs::create_dir_all(&reg).unwrap();
    let v1 = package("example/notes", "1.0.0");
    fs::write(reg.join("n1.tar.gz"), &v1).unwrap();
    let p = platform();
    let index = serde_json::json!({"registry_version": 0, "packages": {
        "example/notes": {"releases": [
            {"version": "2.0.0", "artifacts": {p.clone(): {"url": "n1.tar.gz", "sha256": digest(&v1), "size": v1.len()}}},
            {"version": "1.0.0", "artifacts": {p.clone(): {"url": "n1.tar.gz", "sha256": digest(&v1), "size": v1.len()}}},
            {"version": "0.9.0", "artifacts": {p.clone(): {"url": "n1.tar.gz", "sha256": "00", "size": v1.len()}}},
            {"version": "0.8.0", "artifacts": {p.clone(): {"url": "../n1.tar.gz", "sha256": digest(&v1), "size": v1.len()}}}
        ]},
        "pluxy/notes": {"releases": [{"version": "1.0.0", "artifacts": {p.clone(): {"url": "missing.tar.gz", "sha256": "00", "size": 1}}}]}
    }});
    fs::write(reg.join("index.json"), serde_json::to_vec(&index).unwrap()).unwrap();
    fs::write(
        &d.registries,
        serde_json::to_vec(&serde_json::json!({"version": 0, "registries": [
            {"name": "fixture", "url": reg.join("index.json")}]}))
        .unwrap(),
    )
    .unwrap();
    let offers = offers(&d, &NoRoots).unwrap();
    assert_eq!(offers[0].packages.len(), 2);
    assert!(offers[0]
        .packages
        .iter()
        .any(|o| o.id == "pluxy/notes" && o.refused.is_some()));

    let e = stage_from_registry(&d, &NoRoots, "fixture", "pluxy/notes", None).unwrap_err();
    assert!(e.contains("reserved"), "{e}");
    // The archive holds 1.0.0, not the newest release the index lists.
    let e = stage_from_registry(&d, &NoRoots, "fixture", "example/notes", None).unwrap_err();
    assert!(e.contains("not example/notes 2.0.0"), "{e}");
    let e =
        stage_from_registry(&d, &NoRoots, "fixture", "example/notes", Some("0.9.0")).unwrap_err();
    assert!(e.contains("not the one the registry lists"), "{e}");
    let e =
        stage_from_registry(&d, &NoRoots, "fixture", "example/notes", Some("0.8.0")).unwrap_err();
    assert!(e.contains("inside the index's folder"), "{e}");
    let mut c = Catalog::open(d.clone(), false);
    let cand =
        stage_from_registry(&d, &NoRoots, "fixture", "example/notes", Some("1.0.0")).unwrap();
    assert_eq!(cand.source.kind, "registry");
    c.commit(cand).unwrap();
    assert_eq!(c.get("example/notes").unwrap().active.sha256, digest(&v1));
    done(root);
}

/// Removes a test's folder, the store's read-only releases in it too.
fn done(root: PathBuf) {
    #[cfg(unix)]
    thaw(&root);
    fs::remove_dir_all(&root).unwrap();
}

/// Sets every folder under dir, and dir, writable again, so a test can change or remove it.
#[cfg(unix)]
fn thaw(dir: &Path) {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(dir, fs::Permissions::from_mode(0o700)).unwrap();
    for e in fs::read_dir(dir).unwrap().flatten() {
        if e.file_type().unwrap().is_dir() {
            thaw(&e.path());
        }
    }
}

#[test]
fn names_with_a_drive_or_stream_never_reach_the_store() {
    let root = temp();
    let d = dirs(&root);
    let m = manifest("example/w", "1", Value::Null);
    // Each would name a drive or an alternate data stream on Windows.
    for name in ["C:/x", "C:x", "pages/index.html:stream"] {
        let a = archive(&[
            ("pluxy-extension.json", &m),
            ("pages/index.html", b"x"),
            (name, b"x"),
        ]);
        let e = stage(&d, &a, local("w"), None, &NoRoots).unwrap_err();
        assert!(e.contains("leaves the package folder"), "{name}: {e}");
    }
    // A folder entry too, not only files.
    let mut b = tar::Builder::new(Vec::new());
    let mut h = tar::Header::new_gnu();
    h.set_size(m.len() as u64);
    h.set_mode(0o644);
    h.set_path("pluxy-extension.json").unwrap();
    h.set_cksum();
    b.append(&h, m.as_slice()).unwrap();
    let mut h = tar::Header::new_gnu();
    h.set_entry_type(tar::EntryType::Directory);
    h.set_size(0);
    h.set_mode(0o755);
    h.set_path("D:").unwrap();
    h.set_cksum();
    b.append(&h, std::io::empty()).unwrap();
    let mut gz = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::fast());
    gz.write_all(&b.into_inner().unwrap()).unwrap();
    let e = stage(&d, &gz.finish().unwrap(), local("w"), None, &NoRoots).unwrap_err();
    assert!(e.contains("leaves the package folder"), "{e}");
    // The ui folder is checked before it is joined to the staged folder.
    let ui = serde_json::json!({"ui": {"version": 0, "dir": "C:/pages",
        "pages": [{"id": "p", "title": "P", "short": "P", "path": "index.html"}]}});
    let a = archive(&[
        ("pluxy-extension.json", &manifest("example/w", "1", ui)),
        ("pages/index.html", b"x"),
    ]);
    let e = stage(&d, &a, local("w"), None, &NoRoots).unwrap_err();
    assert!(e.contains("must be a folder inside the package"), "{e}");
    // The positive control on the same route.
    stage(&d, &package("example/w", "1"), local("w"), None, &NoRoots).unwrap();
    done(root);
}

#[test]
fn page_files_with_spaces_and_unicode_are_served_at_their_address() {
    let root = temp();
    let d = dirs(&root);
    let mut c = Catalog::open(d.clone(), false);
    let name = "my page #1 é.html";
    let ui = serde_json::json!({"ui": {"version": 0, "dir": "pages",
        "pages": [{"id": "p", "title": "P", "short": "P", "path": name}]}});
    let m = manifest("example/notes", "1", ui);
    let file = format!("pages/{name}");
    let a = archive(&[("pluxy-extension.json", &m), (&file, b"hello")]);
    install(&mut c, &a, local("n")).unwrap();
    let (page, folder, _) = c.page("example/notes", "p").unwrap();
    // As the webview asks for it: the address's path, still percent-encoded.
    let u = url::Url::parse(&page).unwrap();
    assert!(u.fragment().is_none() && u.query().is_none(), "{page}");
    let (id, sha, got) = asset_request(&folder, u.path()).unwrap();
    assert_eq!(
        (id.as_str(), sha.as_str(), got.as_str()),
        ("example/notes", digest(&a).as_str(), name)
    );
    assert_eq!(c.asset(&id, &sha, &got).unwrap().0, b"hello");
    // An encoded separator or NUL, or another folder, asks for nothing.
    let base = url::Url::parse(&folder).unwrap().path().to_string();
    for bad in [
        "a%2Fb.html",
        "a%5Cb.html",
        "a%00.html",
        "a%zz.html",
        "%C3.html",
    ] {
        assert!(
            asset_request(&folder, &format!("{base}{bad}")).is_none(),
            "{bad}"
        );
    }
    assert!(asset_request(&folder, "/example/notes/other/index.html").is_none());
    done(root);
}

#[test]
fn go_platform_names() {
    assert_eq!(go_platform("macos", "aarch64"), "darwin/arm64");
    assert_eq!(go_platform("linux", "x86_64"), "linux/amd64");
    assert_eq!(go_platform("windows", "x86"), "windows/386");
}

#[cfg(unix)]
#[test]
fn installed_releases_are_read_only_to_their_top_folder() {
    use std::os::unix::fs::PermissionsExt;
    let root = temp();
    let d = dirs(&root);
    let mut c = Catalog::open(d.clone(), false);
    // No folder entries: `pages/` is made for its file.
    let v1 = archive(&[
        (
            "pluxy-extension.json",
            &manifest("example/notes", "1", Value::Null),
        ),
        ("pages/index.html", b"x"),
    ]);
    install(&mut c, &v1, local("n")).unwrap();
    let release = d.store().join(digest(&v1));
    let mut seen = 0;
    let mut todo = vec![release.clone()];
    while let Some(p) = todo.pop() {
        let m = fs::symlink_metadata(&p).unwrap();
        assert_eq!(
            m.permissions().mode() & 0o222,
            0,
            "{} is writable",
            p.display()
        );
        seen += 1;
        if m.is_dir() {
            todo.extend(fs::read_dir(&p).unwrap().flatten().map(|e| e.path()));
        }
    }
    // The release folder, pages/, and the two files.
    assert_eq!(seen, 4);
    done(root);
}

#[test]
fn a_catalog_that_cannot_be_read_keeps_the_store() {
    let root = temp();
    let d = dirs(&root);
    let mut c = Catalog::open(d.clone(), false);
    let v1 = package("example/notes", "1.0.0");
    install(&mut c, &v1, local("n")).unwrap();
    drop(c);
    fs::write(d.catalog(), b"{not json").unwrap();
    let c = Catalog::open(d.clone(), false);
    assert!(
        c.warning.as_deref().is_some_and(|w| w.contains("moved")),
        "{:?}",
        c.warning
    );
    assert!(c.view().packages.is_empty());
    assert_eq!(store_names(&d), BTreeSet::from([digest(&v1)]));
    // Still kept at the next start, while the damaged catalog is kept aside.
    let mut c = Catalog::open(d.clone(), false);
    assert!(
        c.warning.as_deref().is_some_and(|w| w.contains("kept at")),
        "{:?}",
        c.warning
    );
    install(&mut c, &package("example/other", "1"), local("o")).unwrap();
    c.remove("example/other").unwrap();
    assert!(store_names(&d).contains(&digest(&v1)));
    done(root);

    // A catalog that is not a file at all: kept the same way.
    let root = temp();
    let d = dirs(&root);
    let mut c = Catalog::open(d.clone(), false);
    install(&mut c, &v1, local("n")).unwrap();
    drop(c);
    fs::remove_file(d.catalog()).unwrap();
    fs::create_dir(d.catalog()).unwrap();
    let c = Catalog::open(d.clone(), false);
    assert!(c.warning.is_some());
    assert_eq!(store_names(&d), BTreeSet::from([digest(&v1)]));
    done(root);
}

#[cfg(unix)]
#[test]
fn a_catalog_that_cannot_be_moved_aside_is_not_replaced() {
    use std::os::unix::fs::PermissionsExt;
    // Folder permissions do not stop root.
    if unsafe { libc::geteuid() } == 0 {
        return;
    }
    let root = temp();
    let d = dirs(&root);
    fs::create_dir_all(&d.root).unwrap();
    fs::write(d.catalog(), b"{not json").unwrap();
    fs::set_permissions(&d.root, fs::Permissions::from_mode(0o500)).unwrap();
    let mut c = Catalog::open(d.clone(), false);
    assert!(
        c.warning
            .as_deref()
            .is_some_and(|w| w.contains("can be changed")),
        "{:?}",
        c.warning
    );
    // Writable again, as a folder fixed by hand: the catalog this app opened still saves
    // nothing over the one it could not read.
    fs::set_permissions(&d.root, fs::Permissions::from_mode(0o700)).unwrap();
    let e = install(&mut c, &package("example/notes", "1"), local("n")).unwrap_err();
    assert!(e.contains("can be changed"), "{e}");
    assert_eq!(fs::read(d.catalog()).unwrap(), b"{not json");
    done(root);
}

#[test]
fn a_damaged_release_folder_is_replaced_when_installed_again() {
    let root = temp();
    let d = dirs(&root);
    let mut c = Catalog::open(d.clone(), false);
    let v1 = package("example/notes", "1.0.0");
    install(&mut c, &v1, local("n")).unwrap();
    install(&mut c, &package("example/notes", "2.0.0"), local("n")).unwrap();
    let release = d.store().join(digest(&v1));
    #[cfg(unix)]
    thaw(&release);
    let file = release.join("pages/index.html");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&file, fs::Permissions::from_mode(0o600)).unwrap();
    }
    fs::write(&file, b"tampered").unwrap();
    // 1.0.0, the last good release, installed again: its checked copy replaces the folder.
    install(&mut c, &v1, local("n")).unwrap();
    assert_eq!(
        c.asset("example/notes", &digest(&v1), "index.html")
            .unwrap()
            .0,
        b"<p>1.0.0"
    );
    done(root);
}

#[test]
fn a_local_package_updates_from_a_file_of_another_name() {
    let root = temp();
    let d = dirs(&root);
    let mut c = Catalog::open(d.clone(), false);
    install(
        &mut c,
        &package("example/notes", "1.0.0"),
        local("notes-1.0.0.tar.gz"),
    )
    .unwrap();
    install(
        &mut c,
        &package("example/notes", "2.0.0"),
        local("notes-2.0.0.tar.gz"),
    )
    .unwrap();
    let p = c.get("example/notes").unwrap();
    assert_eq!(p.active.version, "2.0.0");
    assert_eq!(p.source, local("notes-2.0.0.tar.gz"));
    // Another source is still refused: a registry, or the same registry name at another index.
    let e = install(
        &mut c,
        &package("example/notes", "3.0.0"),
        registry("r", "/a/index.json"),
    )
    .unwrap_err();
    assert!(e.contains("remove it first"), "{e}");
    install(
        &mut c,
        &package("example/reg", "1"),
        registry("r", "/a/index.json"),
    )
    .unwrap();
    let e = install(
        &mut c,
        &package("example/reg", "2"),
        registry("r", "/b/index.json"),
    )
    .unwrap_err();
    assert!(e.contains("remove it first"), "{e}");
    install(
        &mut c,
        &package("example/reg", "2"),
        registry("r", "/a/index.json"),
    )
    .unwrap();
    done(root);
}

#[cfg(unix)]
#[test]
fn folders_that_cannot_be_deleted_are_reported() {
    use std::os::unix::fs::PermissionsExt;
    if unsafe { libc::geteuid() } == 0 {
        return;
    }
    let root = temp();
    let d = dirs(&root);
    let mut c = Catalog::open(d.clone(), false);
    let v1 = package("example/notes", "1.0.0");
    install(&mut c, &v1, local("n")).unwrap();
    assert!(c.take_cleanup().is_none());
    fs::set_permissions(d.store(), fs::Permissions::from_mode(0o500)).unwrap();
    // The package is removed; its folder, which the store's permissions keep, is reported.
    c.remove("example/notes").unwrap();
    assert!(c.get("example/notes").is_none());
    let left = c.take_cleanup().unwrap();
    assert!(left.contains(&digest(&v1)), "{left}");
    assert!(c.take_cleanup().is_none());
    fs::set_permissions(d.store(), fs::Permissions::from_mode(0o700)).unwrap();
    assert_eq!(store_names(&d), BTreeSet::from([digest(&v1)]));
    done(root);
}

#[cfg(unix)]
#[test]
fn registry_files_must_be_regular_files_in_the_index_folder() {
    let root = temp();
    let d = dirs(&root);
    let reg = root.join("reg");
    fs::create_dir_all(&reg).unwrap();
    let v1 = package("example/notes", "1.0.0");
    let mkfifo = |p: &Path| {
        let c = std::ffi::CString::new(p.to_str().unwrap()).unwrap();
        assert_eq!(unsafe { libc::mkfifo(c.as_ptr(), 0o600) }, 0);
    };
    mkfifo(&reg.join("fifo.tar.gz"));
    fs::write(root.join("outside.tar.gz"), &v1).unwrap();
    std::os::unix::fs::symlink(root.join("outside.tar.gz"), reg.join("link.tar.gz")).unwrap();
    // A folder of the index's that is a link out of it.
    std::os::unix::fs::symlink(&root, reg.join("sub")).unwrap();
    fs::create_dir(reg.join("pkgs")).unwrap();
    fs::write(reg.join("pkgs/n1.tar.gz"), &v1).unwrap();
    let p = platform();
    let art = |url: &str| serde_json::json!({p.clone(): {"url": url, "sha256": digest(&v1), "size": v1.len()}});
    let index = serde_json::json!({"registry_version": 0, "packages": {
        "example/notes": {"releases": [
            {"version": "3.0.0", "artifacts": art("fifo.tar.gz")},
            {"version": "2.0.0", "artifacts": art("link.tar.gz")},
            {"version": "1.5.0", "artifacts": art("sub/outside.tar.gz")},
            {"version": "1.0.0", "artifacts": art("pkgs/n1.tar.gz")}
        ]}
    }});
    fs::write(reg.join("index.json"), serde_json::to_vec(&index).unwrap()).unwrap();
    mkfifo(&reg.join("fifo.json"));
    fs::write(
        &d.registries,
        serde_json::to_vec(&serde_json::json!({"version": 0, "registries": [
            {"name": "fixture", "url": reg.join("index.json")},
            {"name": "fifo", "url": reg.join("fifo.json")}]}))
        .unwrap(),
    )
    .unwrap();
    // Each in a thread: a blocking open would never return.
    let within = |f: Box<dyn FnOnce() -> String + Send>| {
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || tx.send(f()));
        rx.recv_timeout(Duration::from_secs(10))
            .expect("a FIFO was waited on")
    };
    let dd = d.clone();
    let e = within(Box::new(move || {
        stage_from_registry(&dd, &NoRoots, "fixture", "example/notes", Some("3.0.0")).unwrap_err()
    }));
    assert!(e.contains("not a regular file"), "{e}");
    let dd = d.clone();
    let e = within(Box::new(move || {
        offers(&dd, &NoRoots).unwrap()[1]
            .error
            .clone()
            .unwrap_or_default()
    }));
    assert!(e.contains("not a regular file"), "{e}");
    for v in ["2.0.0", "1.5.0"] {
        let e = stage_from_registry(&d, &NoRoots, "fixture", "example/notes", Some(v)).unwrap_err();
        assert!(
            e.contains("not a file inside the index's folder"),
            "{v}: {e}"
        );
    }
    // The positive control on the same route, through a folder of the index's.
    stage_from_registry(&d, &NoRoots, "fixture", "example/notes", Some("1.0.0")).unwrap();
    done(root);
}

#[cfg(unix)]
#[test]
fn a_catalog_folder_that_cannot_be_listed_keeps_the_store() {
    use std::os::unix::fs::PermissionsExt;
    if unsafe { libc::geteuid() } == 0 {
        return;
    }
    let root = temp();
    let d = dirs(&root);
    let mut c = Catalog::open(d.clone(), false);
    let v1 = package("example/notes", "1.0.0");
    install(&mut c, &v1, local("n")).unwrap();
    drop(c);
    fs::write(d.catalog(), b"{not json").unwrap();
    // The damaged catalog can be moved aside, but the folder cannot be listed.
    fs::set_permissions(&d.root, fs::Permissions::from_mode(0o300)).unwrap();
    let c = Catalog::open(d.clone(), false);
    fs::set_permissions(&d.root, fs::Permissions::from_mode(0o700)).unwrap();
    assert!(
        c.warning.as_deref().is_some_and(|w| w.contains("moved")),
        "{:?}",
        c.warning
    );
    assert_eq!(store_names(&d), BTreeSet::from([digest(&v1)]));
    done(root);
}

#[cfg(unix)]
#[test]
fn a_store_that_cannot_be_listed_is_reported() {
    use std::os::unix::fs::PermissionsExt;
    if unsafe { libc::geteuid() } == 0 {
        return;
    }
    let root = temp();
    let d = dirs(&root);
    let mut c = Catalog::open(d.clone(), false);
    install(&mut c, &package("example/notes", "1.0.0"), local("n")).unwrap();
    fs::set_permissions(d.store(), fs::Permissions::from_mode(0o300)).unwrap();
    c.remove("example/notes").unwrap();
    fs::set_permissions(d.store(), fs::Permissions::from_mode(0o700)).unwrap();
    let left = c.take_cleanup().unwrap_or_default();
    assert!(
        left.contains("could not list") && left.contains("store"),
        "{left:?}"
    );
    done(root);
}

// ---- Signed native-tier admission (device_admission.rs) ----

/// Native-tier trust with one test root, for keys generated by the test.
struct TestRoots {
    publisher: String,
    source: String,
    key_id: String,
    key: ed25519_dalek::VerifyingKey,
}
impl crate::artifact::PublisherKeys for TestRoots {
    fn publisher_key(
        &self,
        source: &str,
        key_id: &str,
        publisher: &str,
    ) -> Result<ed25519_dalek::VerifyingKey, String> {
        if (source, key_id, publisher) == (&self.source, &self.key_id, &self.publisher) {
            Ok(self.key)
        } else {
            Err("unknown publisher key or changed source".into())
        }
    }
}
impl Roots for TestRoots {
    fn configured(&self) -> bool {
        true
    }
    fn curated(&self, source: &str) -> bool {
        source == self.source
    }
    fn reserved(&self, publisher: &str) -> bool {
        publisher == self.publisher
    }
}

fn signing_key() -> ed25519_dalek::SigningKey {
    let mut seed = [0u8; 32];
    getrandom::fill(&mut seed).unwrap();
    ed25519_dalek::SigningKey::from_bytes(&seed)
}

/// A native tool's archive: its manifest and an executable program for this computer.
fn native_package(id: &str, version: &str) -> Vec<u8> {
    let m = serde_json::to_vec(&serde_json::json!({
        "manifest_version": 1, "id": id, "version": version,
        "app": {"version": 0, "tier": "native", "host_versions": [0],
            "programs": {platform(): "bin/tool"},
            "operations": [{"id": "inspect", "description": "Report its state",
                "input_schema": {"type": "object"}, "output_schema": {"type": "object"}}],
            "state": {"version": 0, "readable_versions": [0]}}
    }))
    .unwrap();
    let mut b = tar::Builder::new(Vec::new());
    for (name, data, mode) in [
        ("pluxy-extension.json", m.as_slice(), 0o644),
        (
            "bin/tool",
            format!("#!/bin/sh\n# {version}\n").as_bytes(),
            0o755,
        ),
    ] {
        let mut h = tar::Header::new_gnu();
        h.set_path(name).unwrap();
        h.set_size(data.len() as u64);
        h.set_mode(mode);
        h.set_entry_type(tar::EntryType::Regular);
        h.set_cksum();
        b.append(&h, data).unwrap();
    }
    let mut gz = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::fast());
    gz.write_all(&b.into_inner().unwrap()).unwrap();
    gz.finish().unwrap()
}

/// An index artifact for bytes at file; signed by key over a descriptor naming (id, version).
fn signed_artifact(
    file: &str,
    bytes: &[u8],
    signed: Option<(&ed25519_dalek::SigningKey, &str, &str)>,
) -> Value {
    use ed25519_dalek::Signer;
    let mut a = serde_json::json!({"url": file, "sha256": digest(bytes), "size": bytes.len()});
    if let Some((key, id, version)) = signed {
        let descriptor = serde_json::to_string(&serde_json::json!({
            "id": id, "version": version, "tier": "native", "platform": platform(),
            "size": bytes.len(), "sha256": digest(bytes)}))
        .unwrap();
        a["key_id"] = "k1".into();
        a["signature"] = hex(&key.sign(descriptor.as_bytes()).to_bytes()).into();
        a["descriptor"] = descriptor.into();
    }
    a
}

/// A curated registry (`curated`, the root's source) and one no root names (`other`), both
/// listing pluxy/tool 1.0.0 and 1.1.0 signed by the root's key, and refusals' fixtures.
fn native_fixture(root: &Path) -> (Dirs, TestRoots) {
    let d = dirs(root);
    let reg = root.join("reg");
    fs::create_dir_all(&reg).unwrap();
    let key = signing_key();
    let other_key = signing_key();
    let v1 = native_package("pluxy/tool", "1.0.0");
    let v2 = native_package("pluxy/tool", "1.1.0");
    let unsigned = native_package("pluxy/unsigned", "1.0.0");
    let forged = native_package("pluxy/forged", "1.0.0");
    let renamed = native_package("pluxy/renamed", "1.0.0");
    let lookalike = native_package("example/native", "1.0.0");
    for (f, b) in [
        ("v1", &v1),
        ("v2", &v2),
        ("unsigned", &unsigned),
        ("forged", &forged),
        ("renamed", &renamed),
        ("lookalike", &lookalike),
    ] {
        fs::write(reg.join(f), b).unwrap();
    }
    let p = platform();
    let one = |a: Value| serde_json::json!({"releases": [{"version": "1.0.0", "artifacts": {p.clone(): a}}]});
    let tool = serde_json::json!({"releases": [
        {"version": "1.1.0", "artifacts": {p.clone(): signed_artifact("v2", &v2, Some((&key, "pluxy/tool", "1.1.0")))}},
        {"version": "1.0.0", "artifacts": {p.clone(): signed_artifact("v1", &v1, Some((&key, "pluxy/tool", "1.0.0")))}}
    ]});
    let curated = serde_json::json!({"registry_version": 0, "packages": {
        "pluxy/tool": tool,
        "pluxy/unsigned": one(signed_artifact("unsigned", &unsigned, None)),
        "pluxy/forged": one(signed_artifact("forged", &forged, Some((&other_key, "pluxy/forged", "1.0.0")))),
        // Signed, but its descriptor names another release.
        "pluxy/renamed": one(signed_artifact("renamed", &renamed, Some((&key, "pluxy/renamed", "2.0.0")))),
        "example/native": one(signed_artifact("lookalike", &lookalike, None)),
    }});
    let other = serde_json::json!({"registry_version": 0, "packages": {"pluxy/tool": tool,
        "example/native": one(signed_artifact("lookalike", &lookalike, None))}});
    fs::write(
        reg.join("curated.json"),
        serde_json::to_vec(&curated).unwrap(),
    )
    .unwrap();
    fs::write(reg.join("other.json"), serde_json::to_vec(&other).unwrap()).unwrap();
    fs::write(
        &d.registries,
        serde_json::to_vec(&serde_json::json!({"version": 0, "registries": [
            {"name": "curated", "url": reg.join("curated.json")},
            {"name": "other", "url": reg.join("other.json")}]}))
        .unwrap(),
    )
    .unwrap();
    let roots = TestRoots {
        publisher: "pluxy".into(),
        source: reg.join("curated.json").display().to_string(),
        key_id: "k1".into(),
        key: key.verifying_key(),
    };
    (d, roots)
}

#[test]
fn only_a_curated_signed_native_tool_installs() {
    let root = temp();
    let (d, roots) = native_fixture(&root);
    let refused = |registry: &str, id: &str, roots: &dyn Roots| {
        stage_from_registry(&d, roots, registry, id, Some("1.0.0"))
            .err()
            .unwrap_or_else(|| panic!("{registry} {id} was staged"))
    };
    // The same signed entry, digest and all, from a source no root names.
    let e = refused("other", "pluxy/tool", &roots);
    assert!(e.contains("not curated"), "{e}");
    let e = refused("other", "example/native", &roots);
    assert!(e.contains("native tool"), "{e}");
    let e = refused("curated", "example/native", &roots);
    assert!(e.contains("native tool"), "{e}");
    let e = refused("curated", "pluxy/unsigned", &roots);
    assert!(e.contains("needs a curated first-party signature"), "{e}");
    let e = refused("curated", "pluxy/forged", &roots);
    assert!(e.contains("invalid descriptor signature"), "{e}");
    let e = refused("curated", "pluxy/renamed", &roots);
    assert!(e.contains("names pluxy/renamed 2.0.0"), "{e}");
    // A build with no roots: nothing is curated.
    let e = refused("curated", "pluxy/tool", &NoRoots);
    assert!(e.contains("not configured"), "{e}");
    // A local file is never curated, even signed and with the reserved publisher's ID; a root's
    // publisher is reserved as pluxy is.
    let bytes = fs::read(root.join("reg").join("v1")).unwrap();
    let e = stage(&d, &bytes, local("v1"), None, &roots).err().unwrap();
    assert!(e.contains("reserved"), "{e}");
    let acme = TestRoots {
        publisher: "acme".into(),
        ..native_fixture(&root.join("acme")).1
    };
    let e = stage(&d, &package("acme/page", "1"), local("p"), None, &acme)
        .err()
        .unwrap();
    assert!(e.contains("reserved"), "{e}");
    stage(&d, &package("example/page", "1"), local("p"), None, &acme).unwrap();
    assert!(store_names(&d).is_empty());

    // The positive control on the same route: installed, disabled, not admitted.
    let mut c = Catalog::open(d.clone(), false);
    c.commit(stage_from_registry(&d, &roots, "curated", "pluxy/tool", Some("1.0.0")).unwrap())
        .unwrap();
    let v = c.view();
    let p = v.packages.iter().find(|p| p.id == "pluxy/tool").unwrap();
    assert_eq!(
        (p.trust, p.tier.as_str(), p.enabled, p.authorized),
        ("curated", "native", false, false)
    );
    assert_eq!(p.active.signer.as_deref(), Some("pluxy key k1"));
    let e = c.admitted("pluxy/tool", &roots).unwrap_err();
    assert!(e.contains("disabled"), "{e}");
    let e = c.set_enabled("pluxy/tool", true).unwrap_err();
    assert!(e.contains("native confirmation"), "{e}");
    done(root);
}

#[test]
fn admission_is_confirmed_per_release_and_checked_each_time() {
    let root = temp();
    let (d, roots) = native_fixture(&root);
    let mut c = Catalog::open(d.clone(), false);
    let install = |c: &mut Catalog, v: &str| {
        c.commit(stage_from_registry(&d, &roots, "curated", "pluxy/tool", Some(v)).unwrap())
    };
    install(&mut c, "1.0.0").unwrap();
    let sha = c.get("pluxy/tool").unwrap().active.sha256.clone();

    // What the native confirmation shows, then the admission it confirms.
    let a = c.native_candidate("pluxy/tool", &roots).unwrap();
    let dir = d.store().join(&sha);
    assert_eq!(
        (&a.sha256, &a.dir, &a.program),
        (&sha, &dir, &dir.join("bin").join("tool"))
    );
    assert_eq!(
        (
            a.signed.publisher.as_str(),
            a.signed.key_id.as_str(),
            a.signed.source.as_str()
        ),
        ("pluxy", "k1", roots.source.as_str())
    );
    assert_eq!(a.public_key, hex(roots.key.as_bytes()));
    assert_eq!(a.operations[0].id, "inspect");
    let mut changed = a.clone();
    changed.version = "9".into();
    let e = c.admit(&changed, &roots).unwrap_err();
    assert!(e.contains("changed while it was being confirmed"), "{e}");
    assert!(c.admitted("pluxy/tool", &roots).is_err());
    c.admit(&a, &roots).unwrap();
    assert_eq!(c.admitted("pluxy/tool", &roots).unwrap(), a);
    assert!(c.view().packages[0].authorized);
    // Admitted is checked again each time: against the roots and in safe mode.
    let e = c.admitted("pluxy/tool", &NoRoots).unwrap_err();
    assert!(e.contains("not configured"), "{e}");
    let e = Catalog::open(d.clone(), true)
        .admitted("pluxy/tool", &roots)
        .unwrap_err();
    assert!(e.contains("safe mode"), "{e}");
    // The admission is kept with the catalog.
    assert_eq!(
        Catalog::open(d.clone(), false)
            .admitted("pluxy/tool", &roots)
            .unwrap(),
        a
    );

    // Disabling ends it.
    c.set_enabled("pluxy/tool", false).unwrap();
    assert!(c.get("pluxy/tool").unwrap().admission.is_none());
    assert!(c.admitted("pluxy/tool", &roots).is_err());
    let a = c.native_candidate("pluxy/tool", &roots).unwrap();
    c.admit(&a, &roots).unwrap();
    let e = c.native_candidate("pluxy/tool", &roots).unwrap_err();
    assert!(e.contains("enabled already"), "{e}");

    // A new release, and rolling back, each wait disabled for a fresh confirmation.
    install(&mut c, "1.1.0").unwrap();
    let p = c.get("pluxy/tool").unwrap();
    assert!(!p.enabled && p.admission.is_none(), "{p:?}");
    let b = c.native_candidate("pluxy/tool", &roots).unwrap();
    c.admit(&b, &roots).unwrap();
    c.rollback("pluxy/tool").unwrap();
    let p = c.get("pluxy/tool").unwrap();
    assert!(
        !p.enabled && p.admission.is_none() && p.active.sha256 == sha,
        "{p:?}"
    );
    let a = c.native_candidate("pluxy/tool", &roots).unwrap();
    c.admit(&a, &roots).unwrap();
    c.admitted("pluxy/tool", &roots).unwrap();

    // A changed file in the store: no longer admitted.
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        thaw(&dir);
        let tool = dir.join("bin").join("tool");
        fs::set_permissions(&tool, fs::Permissions::from_mode(0o755)).unwrap();
        fs::write(&tool, "#!/bin/sh\necho changed\n").unwrap();
        let e = c.admitted("pluxy/tool", &roots).unwrap_err();
        assert!(e.contains("changed since they were installed"), "{e}");
    }
    done(root);
}
