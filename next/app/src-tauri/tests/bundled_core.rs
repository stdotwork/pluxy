//! The core that comes with the app (`bundled_core.rs`), with real cores built by Go and releases
//! signed at run time: the packaging step's check of the named release, the first start's copy
//! into the app's cores, the damaged and conflict refusals, and which selection the app moves
//! when its version changes. The native test (`native/bundled-session.ts`) runs the same on the
//! real test package.
// Every core here starts from `/proc/self/fd`, which only Linux has.
#![cfg(target_os = "linux")]

#[path = "support/releases.rs"]
mod releases;

use pluxy_app::bundled_core::{self, Bundled};
use pluxy_app::bundled_manifest::Manifest;
use pluxy_app::conn::{bundled_move, BundledMove, Connection, CoreChange, Dirs, Mode};
use pluxy_app::cores::{Chooser, Kind, State, Store};
use releases::*;
use std::path::{Path, PathBuf};
use std::sync::OnceLock;

struct Temp(PathBuf);
impl std::ops::Deref for Temp {
    type Target = Path;
    fn deref(&self) -> &Path {
        &self.0
    }
}
impl Drop for Temp {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn temp(name: &str) -> Temp {
    let d = std::env::temp_dir().join(format!(
        "pluxy-app-bundled-{name}-{}",
        pluxy_app::managed::random_key()
    ));
    std::fs::create_dir_all(&d).unwrap();
    Temp(d)
}

/// Core releases 1.0.0 and 2.0.0 as folders (the program and a file under `bundled/`), and a
/// 3.0.0 that counts its starts in `../starts` (beside the core's own folder, its working folder)
/// and exits; built once.
fn built() -> &'static Path {
    static DIR: OnceLock<PathBuf> = OnceLock::new();
    DIR.get_or_init(|| {
        let d = Path::new(env!("CARGO_TARGET_TMPDIR"))
            .join(format!("bundled-core-{}", pluxy_app::managed::random_key()));
        for v in ["1.0.0", "2.0.0"] {
            build_core(&d.join(v), v);
            std::fs::create_dir_all(d.join(v).join("bundled")).unwrap();
            std::fs::write(d.join(v).join("bundled/notes.txt"), format!("core {v}")).unwrap();
        }
        script_core(
            &d.join("3.0.0"),
            "echo started >> ../starts\necho 'pluxy: this core never starts' >&2\nexit 3",
        );
        std::fs::create_dir_all(d.join("3.0.0").join("bundled")).unwrap();
        std::fs::write(d.join("3.0.0").join("bundled/notes.txt"), "core 3.0.0").unwrap();
        d
    })
}

/// A core source offering the built releases, signed by keys made for it, with 2.1.0 badly
/// signed.
struct Source {
    _dir: Temp,
    index: String,
    keys: Keys,
}

fn source() -> Source {
    let dir = temp("source");
    let index = dir.join("index.json");
    let keys = Keys::new(&index);
    let b = built();
    let releases = vec![
        release(&dir, &keys, "1.0.0", &pack(&b.join("1.0.0")), true),
        release(&dir, &keys, "2.0.0", &pack(&b.join("2.0.0")), true),
        release(&dir, &keys, "3.0.0", &pack(&b.join("3.0.0")), true),
        release(&dir, &keys, "2.1.0", &pack(&b.join("2.0.0")), false),
    ];
    write_index(&index, &releases);
    Source {
        index: index.display().to_string(),
        keys,
        _dir: dir,
    }
}

/// An installed app of version app_version that came with core version, packaged as the AppImage
/// holds it (`bundled_core::place`): its manifest made at package time from the signed release.
fn app(root: &Path, src: &Source, version: &str, app_version: &str) -> Bundled {
    std::fs::create_dir_all(root).unwrap();
    let unpacked = root.join(format!("unpacked-{app_version}"));
    let manifest = bundled_core::prepare(&src.keys, &src.index, version, &unpacked).unwrap();
    let exe = root.join(format!("app-{app_version}/usr/bin/pluxy-app"));
    let place = bundled_core::place(&exe, "pluxy").unwrap();
    std::fs::create_dir_all(exe.parent().unwrap()).unwrap();
    std::fs::copy(unpacked.join("pluxy"), &place.program).unwrap();
    copy_tree(&unpacked.join("bundled"), &place.bundled);
    Bundled {
        manifest,
        place,
        app_version: app_version.into(),
    }
}

fn copy_tree(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for e in std::fs::read_dir(from).unwrap().flatten() {
        if e.file_type().unwrap().is_dir() {
            copy_tree(&e.path(), &to.join(e.file_name()));
        } else {
            std::fs::copy(e.path(), to.join(e.file_name())).unwrap();
        }
    }
}

fn dirs(root: &Path) -> Dirs {
    Dirs {
        config: root.join("config"),
        core: root.join("core"),
        cores: root.join("cores"),
    }
}

/// The app's connection in managed mode at its start, with the core that came with it.
fn start(root: &Path, bundled: &Bundled) -> Connection {
    let d = dirs(root);
    std::fs::create_dir_all(&d.config).unwrap();
    std::fs::write(d.settings(), r#"{"mode":"managed"}"#).unwrap();
    Connection::open_with_bundled(d, None, true, Some(bundled.clone()))
}

fn state(root: &Path) -> State {
    Store::open(&root.join("cores")).unwrap().state().unwrap()
}

fn running(conn: &mut Connection) -> Option<String> {
    let v = conn.view();
    let core = v.core.unwrap();
    (core.state == "running").then(|| core.version.unwrap())
}

fn exe(pid: u32) -> PathBuf {
    std::fs::read_link(format!("/proc/{pid}/exe")).unwrap_or_default()
}

#[test]
fn packaging_checks_the_named_release_against_its_signed_descriptor() {
    let root = temp("prepare");
    let src = source();
    let m = bundled_core::prepare(&src.keys, &src.index, "1.0.0", &root.join("ok")).unwrap();
    assert_eq!(m.version, "1.0.0");
    let mut paths: Vec<&str> = m.files.iter().map(|f| f.path.as_str()).collect();
    paths.sort();
    assert_eq!(paths, ["bundled/notes.txt", "pluxy"]);
    let notes = m
        .files
        .iter()
        .find(|f| f.path == "bundled/notes.txt")
        .unwrap();
    assert_eq!(
        notes.sha256,
        pluxy_app::artifact::hex(&<sha2::Sha256 as sha2::Digest>::digest(b"core 1.0.0"))
    );
    assert!(
        m.files
            .iter()
            .find(|f| f.path == "pluxy")
            .unwrap()
            .executable
    );
    assert_eq!(Manifest::parse(m.to_json().as_bytes(), "pluxy").unwrap(), m);

    // A signature that does not verify, a version the source does not offer, and a key not
    // trusted for this source are refused, and nothing is unpacked.
    for (version, keys, why) in [
        ("2.1.0", &src.keys, "signature"),
        ("9.9.9", &src.keys, "offers no core 9.9.9"),
        (
            "1.0.0",
            &Keys::new(Path::new("/elsewhere")),
            "unknown publisher key",
        ),
    ] {
        let out = root.join(format!("refused-{version}-{why}"));
        let e = bundled_core::prepare(keys, &src.index, version, &out).unwrap_err();
        assert!(e.contains(why), "{version}: {e}");
        assert!(
            !out.join("pluxy").exists(),
            "{version}: unpacked though refused"
        );
    }
}

#[test]
fn a_fresh_profile_runs_the_bundled_core_from_the_store_and_copies_only_its_files() {
    let root = temp("fresh");
    let src = source();
    let b = app(&root, &src, "1.0.0", "0.1.0");
    // Beside the listed files: a file the manifest does not list (an older install's leftover).
    std::fs::write(b.place.bundled.join("extra.txt"), "not listed").unwrap();
    let profile = root.join("profile");
    let mut conn = start(&profile, &b);
    assert_eq!(
        running(&mut conn).as_deref(),
        Some("1.0.0"),
        "{:?}",
        conn.view()
    );
    let store = Store::open(&profile.join("cores")).unwrap();
    let pid = conn.core_pid().unwrap();
    assert!(
        exe(pid).starts_with(std::fs::canonicalize(store.folder("1.0.0")).unwrap()),
        "the core runs {:?}, not the store's copy",
        exe(pid)
    );
    let v = conn.versions();
    assert_eq!(v.installed.len(), 1);
    assert_eq!(v.installed[0].kind, Kind::Bundled);
    assert_eq!(v.bundled.as_ref().unwrap().version, "1.0.0");
    assert!(v.bundled.unwrap().problem.is_none());
    assert!(store.folder("1.0.0").join("bundled/notes.txt").exists());
    assert!(
        !store.folder("1.0.0").join("bundled/extra.txt").exists(),
        "a file the manifest does not list was copied"
    );
    let st = state(&profile);
    assert_eq!(st.chosen_by, Some(Chooser::App));
    assert_eq!(st.bundled_for.as_deref(), Some("0.1.0"));
    conn.stop_core();

    // A store copy changed since is copied in again at the next start.
    let notes = store.folder("1.0.0").join("bundled/notes.txt");
    let mut perms = std::fs::metadata(&notes).unwrap().permissions();
    std::os::unix::fs::PermissionsExt::set_mode(&mut perms, 0o600);
    std::fs::set_permissions(&notes, perms).unwrap();
    std::fs::write(&notes, "changed").unwrap();
    drop(conn);
    let mut conn = start(&profile, &b);
    assert_eq!(
        running(&mut conn).as_deref(),
        Some("1.0.0"),
        "{:?}",
        conn.view()
    );
    assert_eq!(std::fs::read(&notes).unwrap(), b"core 1.0.0");
    conn.stop_core();
}

#[test]
fn a_damaged_bundled_core_is_refused_and_nothing_is_installed() {
    let root = temp("damaged");
    let src = source();
    for (name, damage) in [
        (
            "changed",
            Box::new(|b: &Bundled| {
                std::fs::write(b.place.bundled.join("notes.txt"), "core 1.0.x").unwrap()
            }) as Box<dyn Fn(&Bundled)>,
        ),
        (
            "link",
            Box::new(|b: &Bundled| {
                let real = b.place.bundled.join("../notes-real.txt");
                std::fs::rename(b.place.bundled.join("notes.txt"), &real).unwrap();
                std::os::unix::fs::symlink(&real, b.place.bundled.join("notes.txt")).unwrap();
            }),
        ),
        (
            "missing",
            Box::new(|b: &Bundled| std::fs::remove_file(&b.place.program).unwrap()),
        ),
    ] {
        let b = app(&root.join(name), &src, "1.0.0", "0.1.0");
        damage(&b);
        let profile = root.join(name).join("profile");
        let mut conn = start(&profile, &b);
        let v = conn.view();
        let core = v.core.clone().unwrap();
        assert_eq!(core.state, "missing", "{name}: {v:?}");
        let detail = core.detail.unwrap();
        assert!(
            detail.contains("The core that came with the app (core 1.0.0) is damaged")
                && detail.contains("reinstall or update the app"),
            "{name}: {detail}"
        );
        let versions = conn.versions();
        assert!(versions.installed.is_empty(), "{name}: {versions:?}");
        assert_eq!(
            versions.bundled.unwrap().problem.as_deref(),
            Some(detail.as_str())
        );
    }
}

#[test]
fn a_store_version_with_other_files_is_a_conflict_and_stays() {
    let root = temp("conflict");
    let src = source();
    // Core 1.0.0 from another source, whose archive has no bundled/ folder.
    let other = temp("conflict-source");
    let index = other.join("index.json");
    let keys = Keys::new(&index);
    let program_only = other.join("program-only");
    std::fs::create_dir_all(&program_only).unwrap();
    std::fs::copy(built().join("1.0.0/pluxy"), program_only.join("pluxy")).unwrap();
    write_index(
        &index,
        &[release(&other, &keys, "1.0.0", &pack(&program_only), true)],
    );
    let profile = root.join("profile");
    let mut conn = Connection::open(dirs(&profile), None);
    conn.change(pluxy_app::conn::Change {
        mode: Some(Mode::Managed),
        core_source: Some(index.display().to_string()),
        ..Default::default()
    })
    .unwrap();
    let staged = conn.prepare_install(&keys, "1.0.0").unwrap();
    conn.apply(CoreChange::Install(staged)).unwrap();
    conn.stop_core();
    drop(conn);

    let b = app(&root, &src, "1.0.0", "0.1.0");
    let mut conn = Connection::open_with_bundled(dirs(&profile), None, true, Some(b));
    // The version installed before still runs; the app says why it did not install its own.
    assert_eq!(running(&mut conn).as_deref(), Some("1.0.0"));
    let v = conn.versions();
    let problem = v.bundled.unwrap().problem.unwrap();
    assert!(
        problem.contains(
            "hold a core 1.0.0 with other files than the core 1.0.0 that came with the app"
        ) && !problem.contains("damaged"),
        "{problem}"
    );
    assert_eq!(v.installed.len(), 1);
    assert_eq!(v.installed[0].kind, Kind::Signed);
    let store = Store::open(&profile.join("cores")).unwrap();
    assert!(!store.folder("1.0.0").join("bundled").exists());
    conn.stop_core();
}

#[test]
fn the_app_moves_only_its_own_selection_once_per_change_of_its_version() {
    let s = |selected: Option<&str>, by: Option<Chooser>, applied: Option<&str>| State {
        selected: selected.map(String::from),
        chosen_by: by,
        bundled_for: applied.map(String::from),
        ..State::default()
    };
    let app = Some(Chooser::App);
    // A fresh profile gets the bundled core.
    assert_eq!(
        bundled_move(&s(None, None, None), "1.0.0", "0.1.0"),
        BundledMove::Switch
    );
    // Once applied for this app version, nothing moves, also after it failed to start.
    assert_eq!(
        bundled_move(&s(Some("1.0.0"), app, Some("0.2.0")), "2.0.0", "0.2.0"),
        BundledMove::Stay
    );
    assert_eq!(
        bundled_move(&s(None, None, Some("0.2.0")), "2.0.0", "0.2.0"),
        BundledMove::Stay
    );
    // A new app version, up or down, moves a selection the app made.
    assert_eq!(
        bundled_move(&s(Some("1.0.0"), app, Some("0.1.0")), "2.0.0", "0.2.0"),
        BundledMove::Switch
    );
    assert_eq!(
        bundled_move(&s(Some("2.0.0"), app, Some("0.2.0")), "1.0.0", "0.1.0"),
        BundledMove::Switch
    );
    // ...or only records the new app version when it already is its bundled core.
    assert_eq!(
        bundled_move(&s(Some("2.0.0"), app, Some("0.1.0")), "2.0.0", "0.2.0"),
        BundledMove::Record
    );
    // A selection the person made, or one with no chooser recorded, stays.
    for by in [Some(Chooser::Person), None] {
        assert_eq!(
            bundled_move(&s(Some("1.0.0"), by, Some("0.1.0")), "2.0.0", "0.2.0"),
            BundledMove::Record
        );
    }
    // Each local build (package.ts local) is a new app version, the same as its core's: the
    // rebuilt app moves its own selection to its core.
    let (a, b) = ("0.1.0+local.0123456789ab", "0.1.0+local.ba9876543210");
    assert_eq!(
        bundled_move(&s(Some(a), app, Some(a)), b, b),
        BundledMove::Switch
    );
}

#[test]
fn app_updates_move_an_app_selection_and_keep_a_rollback_the_person_chose() {
    let root = temp("versions");
    let src = source();
    let v1 = app(&root, &src, "1.0.0", "0.1.0");
    let v2 = app(&root, &src, "2.0.0", "0.2.0");
    let profile = root.join("profile");

    let mut conn = start(&profile, &v1);
    assert_eq!(running(&mut conn).as_deref(), Some("1.0.0"));
    conn.stop_core();
    drop(conn);
    // A newer app moves its own selection to the core it came with.
    let mut conn = start(&profile, &v2);
    assert_eq!(running(&mut conn).as_deref(), Some("2.0.0"));
    // The person rolls back to the earlier app's bundled core: that stays, with this app and
    // after app version changes either way.
    conn.apply(CoreChange::Rollback("1.0.0".into())).unwrap();
    assert_eq!(state(&profile).chosen_by, Some(Chooser::Person));
    conn.stop_core();
    drop(conn);
    for b in [&v2, &v1, &v2] {
        let mut conn = start(&profile, b);
        assert_eq!(
            running(&mut conn).as_deref(),
            Some("1.0.0"),
            "app {}",
            b.app_version
        );
        // The bundled core of the app that runs stays installed, to switch back to.
        assert!(conn
            .versions()
            .installed
            .iter()
            .any(|i| i.id == b.version()));
        conn.stop_core();
    }

    // In another profile the app made each selection: an older app moves it back.
    let profile = root.join("profile-down");
    let mut conn = start(&profile, &v2);
    assert_eq!(running(&mut conn).as_deref(), Some("2.0.0"));
    conn.stop_core();
    drop(conn);
    let mut conn = start(&profile, &v1);
    assert_eq!(running(&mut conn).as_deref(), Some("1.0.0"));
    assert_eq!(state(&profile).chosen_by, Some(Chooser::App));
    conn.stop_core();
}

#[test]
fn a_bundled_core_that_does_not_start_is_not_tried_again_for_the_same_app_version() {
    let root = temp("failed");
    let src = source();
    let v1 = app(&root, &src, "1.0.0", "0.1.0");
    let v2 = app(&root, &src, "3.0.0", "0.2.0");
    let profile = root.join("profile");
    let mut conn = start(&profile, &v1);
    assert_eq!(running(&mut conn).as_deref(), Some("1.0.0"));
    conn.stop_core();
    drop(conn);

    // 3.0.0 runs from its folder in the store and counts its starts beside it.
    let starts = profile.join("starts");
    for n in 1..=2 {
        let mut conn = start(&profile, &v2);
        assert_eq!(
            running(&mut conn).as_deref(),
            Some("1.0.0"),
            "start {n}: the core that ran before does not run again"
        );
        let st = state(&profile);
        assert_eq!(st.selected.as_deref(), Some("1.0.0"));
        assert_eq!(
            st.failed.as_ref().map(|f| f.version.as_str()),
            Some("3.0.0")
        );
        assert_eq!(st.bundled_for.as_deref(), Some("0.2.0"));
        assert_eq!(
            std::fs::read_to_string(&starts).unwrap().lines().count(),
            1,
            "start {n}: the failed bundled core was tried again"
        );
        conn.stop_core();
    }
}

#[test]
fn the_bundled_program_is_told_by_its_digest() {
    let root = temp("program");
    let src = source();
    let b = app(&root, &src, "1.0.0", "0.1.0");
    assert!(b.is_program(&b.place.program));
    assert!(!b.is_program(&built().join("2.0.0/pluxy")));
}
