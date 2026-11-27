use super::*;
#[cfg(unix)]
use std::os::unix::fs::PermissionsExt;

/// The fixture's program: a shell script, or on Windows a file named `.exe` that starts `MZ`, which
/// makes it a program for review (it never runs).
const TOOL: &str = if cfg!(windows) { "tool.exe" } else { "tool" };
#[cfg(unix)]
const EXIT_0: &[u8] = b"#!/bin/sh\nexit 0\n";
#[cfg(unix)]
const EXIT_1: &[u8] = b"#!/bin/sh\nexit 1\n";
#[cfg(windows)]
const EXIT_0: &[u8] = b"MZ fixture 0";
#[cfg(windows)]
const EXIT_1: &[u8] = b"MZ fixture 1";

/// Writes a program at path: executable by its owner (on Windows its `.exe` or `.com` name and its
/// first bytes, `MZ`, make it one).
fn program(path: &Path, bytes: &[u8]) {
    fs::write(path, bytes).unwrap();
    #[cfg(unix)]
    fs::set_permissions(path, fs::Permissions::from_mode(0o700)).unwrap();
}
/// Makes a settings file the app writes private: mode 0600 on Unix; on Windows the protected,
/// owner-only access list the app gives it (a file made in the fixture's folder takes up that
/// folder's list, which is not private).
fn owner_only(path: &Path) {
    #[cfg(unix)]
    fs::set_permissions(path, fs::Permissions::from_mode(0o600)).unwrap();
    #[cfg(windows)]
    crate::private_windows::make_private(&crate::private_windows::tests::open(path), false)
        .unwrap();
}
/// Changes path's metadata, not its bytes: its mode, or on Windows its read-only attribute.
fn change_metadata(path: &Path) {
    #[cfg(unix)]
    fs::set_permissions(path, fs::Permissions::from_mode(0o500)).unwrap();
    #[cfg(windows)]
    {
        let mut p = fs::metadata(path).unwrap().permissions();
        p.set_readonly(true);
        fs::set_permissions(path, p).unwrap();
    }
}
/// Removes a fixture's folder; on Windows first clears the read-only attribute removal refuses.
fn remove(dir: &Path) {
    #[cfg(windows)]
    for e in fs::read_dir(dir).unwrap().flatten() {
        let mut p = e.metadata().unwrap().permissions();
        if p.readonly() {
            #[allow(clippy::permissions_set_readonly_false)]
            p.set_readonly(false);
            fs::set_permissions(e.path(), p).unwrap();
        }
    }
    fs::remove_dir_all(dir).unwrap();
}
/// Whether folder or file path is private as the settings need (Windows).
#[cfg(windows)]
fn private_path(path: &Path) -> bool {
    windows_files::private(&windows_files::open_any(path).unwrap(), "fixture").is_ok()
}

fn fixture() -> (PathBuf, Confirmations, Action) {
    fixture_in(&std::env::temp_dir())
}
fn fixture_in(root: &Path) -> (PathBuf, Confirmations, Action) {
    let dir = root.join(format!("pluxy-confirm-{}", crate::managed::random_key()));
    fs::create_dir(&dir).unwrap();
    let exe = dir.join(TOOL);
    program(&exe, EXIT_0);
    let package = Package {
        id: "fixture/tool".into(),
        source: "fixture".into(),
        digest: "a".repeat(64),
        signer: "fixture".into(),
        contract: "1".into(),
        generation: 1,
    };
    let action = Action {
        what: "Start fixture".into(),
        chain: vec![package.clone()],
        executor: package.clone(),
        native_tool: true,
        files: vec![],
        launch: Some(Launch {
            executable: exe,
            argv: vec![TOOL.into(), "--flag".into()],
            cwd: dir.clone(),
            program: None,
            opens: None,
            terminal_adapter: package,
            adapter_program: None,
            environment_revision: "1".into(),
            environment_review: vec![],
            secret_uses: vec![],
        }),
        environment: None,
    };
    let c = Confirmations::open(dir.join("settings.json")).unwrap();
    (dir, c, action)
}
fn approve(c: &mut Confirmations, a: Action, remember: bool) -> (String, String) {
    let p = c.prepare(a, Origin::NativeUi).unwrap();
    let id = c.queue(p).unwrap();
    let t = c.finish(&id, true, remember).unwrap().unwrap();
    (id, t)
}
#[test]
fn tokens_are_exact_single_use_cancelled_expired_and_revocable() {
    let (dir, mut c, a) = fixture();
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    let id = c.queue(p).unwrap();
    assert!(c.finish(&id, false, true).unwrap().is_none());
    assert!(c.issued.is_empty());
    let (id, t) = approve(&mut c, a.clone(), false);
    let allowed = c.consume(&id, &t).unwrap();
    assert!(allowed.executable().is_some());
    assert!(allowed.cwd().is_some());
    assert!(c.consume(&id, &t).is_err());
    let (id, t) = approve(&mut c, a.clone(), false);
    assert!(c.consume("wrong action", &t).is_err());
    assert!(c.consume(&id, &t).is_err());
    let (id, t) = approve(&mut c, a.clone(), false);
    c.issued.get_mut(&t).unwrap().expires = Instant::now();
    assert!(c.consume(&id, &t).is_err());
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    let id = c.queue(p).unwrap();
    c.pending.get_mut(&id).unwrap().created = ago(LIFETIME);
    assert!(c.finish(&id, true, true).is_err());
    let (id, t) = approve(&mut c, a.clone(), true);
    assert!(c.finish(&id, true, false).is_err());
    c.revoke(&c.approvals[0].id.clone()).unwrap();
    assert!(c.consume(&id, &t).is_err());
    let (id, t) = approve(&mut c, a.clone(), false);
    c.invalidate();
    assert!(c.consume(&id, &t).is_err());
    let (id, t) = approve(&mut c, a.clone(), false);
    fs::write(&a.launch.as_ref().unwrap().executable, b"changed").unwrap();
    assert!(c.consume(&id, &t).is_err());
    remove(&dir);
}
/// A confirmation says first, in plain words, what will happen: the headline native code wrote, or
/// else its description and that the add-on has full access; where it runs comes before even
/// that. The exact action, the same text it showed before, follows as its details: nothing of it
/// is left out, and the plain words bind nothing.
#[test]
fn a_confirmation_says_what_happens_first_and_keeps_the_details_below() {
    let (dir, mut c, a) = fixture();
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    let (words, details) = p.review().unwrap();
    assert_eq!(words, format!("Start fixture\n\n{}", text::FULL_ACCESS));
    let details = details.unwrap();
    assert_eq!(details, review_text(&p.exact).unwrap());
    assert!(details.contains("\"argv\"") && details.contains("\"digest\""));
    let in_wsl = Action {
        environment: Some("Runs in WSL: distribution Ubuntu.".into()),
        ..a.clone()
    };
    let mut p = c.prepare(in_wsl.clone(), Origin::NativeUi).unwrap();
    p.set_headline(format!(
        "Fixture add-on: {}\nIt gets \"Key\" from this device's vault.",
        one_line("Start it\nIt is safe")
    ));
    let (words, details) = p.review().unwrap();
    assert_eq!(
        words,
        "Runs in WSL: distribution Ubuntu.\n\nFixture add-on: Start it\\u000aIt is safe\nIt gets \"Key\" from this device's vault."
    );
    assert_eq!(details.unwrap(), review_text(&p.exact).unwrap());
    assert!(!words.contains('{') && !words.contains("native tool") && !words.contains("core"));
    // The headline binds nothing: what is confirmed and remembered is the exact action, which
    // the same action prepared without a headline matches.
    let id = c.queue(p).unwrap();
    let t = c.finish(&id, true, true).unwrap().unwrap();
    c.consume(&id, &t).unwrap();
    let again = c.prepare(in_wsl, Origin::NativeUi).unwrap();
    assert!(c
        .remembered(
            again,
            Gesture {
                created: Instant::now(),
                origin: Origin::NativeUi,
            },
        )
        .is_ok());
    remove(&dir);
}
/// The moment d before now.
fn ago(d: Duration) -> Instant {
    Instant::now()
        .checked_sub(d)
        .expect("the clock runs longer than d")
}
#[test]
fn a_shown_confirmation_waits_for_the_person_and_an_unshown_one_lapses() {
    let (dir, mut c, a) = fixture();
    // Queued 61 seconds ago and shown a second later: the person has read it for a minute, and
    // their Confirm counts.
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    let id = c.queue(p).unwrap();
    c.shown(&id);
    let p = c.pending.get_mut(&id).unwrap();
    p.created = ago(Duration::from_secs(61));
    p.shown = Some(ago(Duration::from_secs(60)));
    let t = c.finish(&id, true, false).unwrap().unwrap();
    c.consume(&id, &t).unwrap();
    // Shown nine and a half minutes ago: still open.
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    let id = c.queue(p).unwrap();
    c.shown(&id);
    c.pending.get_mut(&id).unwrap().shown = Some(ago(Duration::from_secs(570)));
    let t = c.finish(&id, true, false).unwrap().unwrap();
    c.consume(&id, &t).unwrap();
    // Open for `SHOWN_LIFETIME`: closed, as cancelled.
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    let id = c.queue(p).unwrap();
    c.shown(&id);
    c.pending.get_mut(&id).unwrap().shown = Some(ago(SHOWN_LIFETIME));
    assert_eq!(
        c.finish(&id, true, false).err().as_deref(),
        Some("confirmation expired or revoked")
    );
    // Never shown in a minute: gone, with its pending place.
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    let id = c.queue(p).unwrap();
    c.pending.get_mut(&id).unwrap().created = ago(LIFETIME);
    assert!(c.finish(&id, true, false).is_err());
    assert!(c.pending.is_empty());
    // Shown twice (a second dialog asked for it): the first showing counts.
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    let id = c.queue(p).unwrap();
    c.shown(&id);
    let first = c.pending[&id].shown;
    c.shown(&id);
    assert_eq!(c.pending[&id].shown, first);
    // A native caller waits out the whole of it.
    assert!(WAIT > LIFETIME + SHOWN_LIFETIME);
    remove(&dir);
}
#[test]
fn remembered_identity_binds_every_field_and_caches_only_unchanged_metadata() {
    let (dir, mut c, a) = fixture();
    let (id, t) = approve(&mut c, a.clone(), true);
    c.consume(&id, &t).unwrap();
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    assert_eq!(c.hashes, 1);
    let t = c
        .remembered(
            p,
            Gesture {
                created: Instant::now(),
                origin: Origin::NativeUi,
            },
        )
        .ok()
        .unwrap();
    let id = c.token_action(&t).unwrap().to_string();
    c.consume(&id, &t).unwrap();
    let mut changes = vec![];
    let mut changed = a.clone();
    changed.chain[0].source.push('x');
    changes.push(changed);
    let mut changed = a.clone();
    changed.chain.push(changed.executor.clone());
    changes.push(changed);
    let mut changed = a.clone();
    changed.executor.digest.push('x');
    changes.push(changed);
    let mut changed = a.clone();
    changed.launch.as_mut().unwrap().argv.reverse();
    changes.push(changed);
    let mut changed = a.clone();
    changed.launch.as_mut().unwrap().cwd = dir.parent().unwrap().into();
    changes.push(changed);
    let mut changed = a.clone();
    changed
        .launch
        .as_mut()
        .unwrap()
        .environment_revision
        .push('x');
    changes.push(changed);
    let mut changed = a.clone();
    changed.launch.as_mut().unwrap().terminal_adapter.generation += 1;
    changes.push(changed);
    let mut changed = a.clone();
    changed
        .launch
        .as_mut()
        .unwrap()
        .secret_uses
        .push(SecretUse {
            reference: "opaque".into(),
            recipient: "final-agent".into(),
            purpose: "env".into(),
        });
    changes.push(changed);
    for changed in changes {
        let p = c.prepare(changed, Origin::NativeUi).unwrap();
        assert!(c
            .remembered(
                p,
                Gesture {
                    created: Instant::now(),
                    origin: Origin::NativeUi,
                }
            )
            .is_err());
    }
    let exe = &a.launch.as_ref().unwrap().executable;
    change_metadata(exe);
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    assert_eq!(c.hashes, 2);
    assert!(c
        .remembered(
            p,
            Gesture {
                created: Instant::now(),
                origin: Origin::NativeUi,
            }
        )
        .is_err());
    fs::rename(exe, dir.join("old")).unwrap();
    program(exe, b"MZ replacement");
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    assert_eq!(c.hashes, 3);
    assert!(c
        .remembered(
            p,
            Gesture {
                created: Instant::now(),
                origin: Origin::NativeUi,
            }
        )
        .is_err());
    // Persistence is real settings, with a private file; unchanged metadata still hashes once
    // in a new process before using persisted approval.
    let (id, t) = approve(&mut c, a.clone(), true);
    c.consume(&id, &t).unwrap();
    let mut loaded = Confirmations::open(dir.join("settings.json")).unwrap();
    let p = loaded.prepare(a, Origin::NativeUi).unwrap();
    assert!(loaded
        .remembered(
            p,
            Gesture {
                created: Instant::now(),
                origin: Origin::NativeUi,
            }
        )
        .is_ok());
    #[cfg(unix)]
    assert_eq!(
        fs::metadata(dir.join("settings.json")).unwrap().mode() & 0o777,
        0o600
    );
    #[cfg(windows)]
    assert!(private_path(&dir.join("settings.json")));
    remove(&dir);
}
#[test]
fn page_starts_never_consult_remembered_approvals_and_bound_pending_and_rate() {
    let (dir, mut c, a) = fixture();
    let (id, t) = approve(&mut c, a.clone(), true);
    c.consume(&id, &t).unwrap();
    let p = c.prepare_page(a.clone()).unwrap();
    let id = c.queue(p).unwrap();
    assert!(c.issued.is_empty());
    for _ in 0..10 {
        let p = c.prepare_page(a.clone()).unwrap();
        assert!(c.queue(p).is_err());
    }
    assert_eq!(c.pending.len(), 1);
    c.finish(&id, false, false).unwrap();
    let p = c.prepare_page(a.clone()).unwrap();
    assert!(c.queue(p).is_err());
    c.last_page
        .values_mut()
        .for_each(|t| *t = Instant::now() - Duration::from_secs(2));
    let p = c.prepare_page(a).unwrap();
    assert!(c.queue(p).is_ok());
    remove(&dir);
}

#[test]
fn corrupt_settings_start_empty_and_preserve_the_bad_file() {
    let (dir, c, _) = fixture();
    let settings = c.settings.clone();
    fs::write(&settings, b"{broken JSON").unwrap();
    owner_only(&settings);
    let loaded = Confirmations::open(settings.clone()).unwrap();
    assert!(loaded.approvals.is_empty());
    assert!(!settings.exists());
    let backups: Vec<_> = fs::read_dir(&dir)
        .unwrap()
        .filter_map(Result::ok)
        .filter(|e| {
            e.file_name()
                .to_string_lossy()
                .starts_with("settings.json.corrupt-")
        })
        .collect();
    assert_eq!(backups.len(), 1);
    assert_eq!(fs::read(backups[0].path()).unwrap(), b"{broken JSON");
    remove(&dir);
}

#[test]
fn non_utf8_paths_are_rejected_before_review() {
    let (dir, mut c, mut a) = fixture();
    // Bytes that are not UTF-8, or on Windows a lone surrogate.
    #[cfg(unix)]
    let name = {
        use std::os::unix::ffi::OsStringExt;
        std::ffi::OsString::from_vec(vec![b'f', 0xff])
    };
    #[cfg(windows)]
    let name = {
        use std::os::windows::ffi::OsStringExt;
        std::ffi::OsString::from_wide(&[u16::from(b'f'), 0xd800])
    };
    let file = dir.join(name);
    // macOS file systems refuse the name itself; the path is refused before it is looked up.
    #[cfg(not(target_os = "macos"))]
    fs::write(&file, b"fixture").unwrap();
    a.files.push(file);
    assert_eq!(
        c.prepare(a, Origin::NativeUi).err().as_deref(),
        Some("review requires UTF-8 paths"),
        "non-UTF-8 paths must be refused before review"
    );
    remove(&dir);
}

#[cfg(unix)]
#[test]
fn settings_version_permissions_and_symlinks_fail_closed() {
    use std::os::unix::fs::symlink;
    let (dir, mut c, a) = fixture();
    approve(&mut c, a.clone(), true);
    let json: serde_json::Value = serde_json::from_slice(&fs::read(&c.settings).unwrap()).unwrap();
    assert_eq!(json["version"], 1);
    // Old unversioned data and unsupported new versions require new review; preserve originals.
    for bytes in [b"[]".as_slice(), b"{\"version\":2,\"approvals\":[]}"] {
        fs::write(&c.settings, bytes).unwrap();
        owner_only(&c.settings);
        let loaded = Confirmations::open(c.settings.clone()).unwrap();
        assert!(loaded.approvals.is_empty());
        assert!(loaded.startup_warning.is_some());
        assert!(!c.settings.exists());
    }
    fs::write(&c.settings, b"{}").unwrap();
    fs::set_permissions(&c.settings, fs::Permissions::from_mode(0o666)).unwrap();
    let mut loaded = Confirmations::open(c.settings.clone()).unwrap();
    assert!(loaded.approvals.is_empty());
    assert!(loaded.persistence_blocked);
    let p = loaded.prepare(a.clone(), Origin::NativeUi).unwrap();
    let id = loaded.queue(p).unwrap();
    let token = loaded.finish(&id, true, true).unwrap().unwrap();
    assert!(loaded.saving_warning(&id).is_some());
    loaded.consume(&id, &token).unwrap();
    assert_eq!(fs::read(&c.settings).unwrap(), b"{}");
    fs::remove_file(&c.settings).unwrap();
    let target = dir.join("target");
    fs::write(&target, b"private bytes").unwrap();
    symlink(&target, &c.settings).unwrap();
    let loaded = Confirmations::open(c.settings.clone()).unwrap();
    assert!(loaded.approvals.is_empty());
    assert!(loaded.persistence_blocked);
    assert_eq!(fs::read(target).unwrap(), b"private bytes");
    assert!(fs::symlink_metadata(&c.settings).unwrap().is_symlink());
    // Ownership check uses fstat, not a path check; a foreign uid is refused too.
    let file = File::open(dir.join(TOOL)).unwrap();
    assert!(private_file(&file).is_ok());
    if unsafe { libc::geteuid() } == 0 {
        use std::os::fd::AsRawFd;
        assert_eq!(unsafe { libc::fchown(file.as_raw_fd(), 65534, !0) }, 0);
        assert!(private_file(&file).is_err());
    }
    remove(&dir);
}

/// Windows: the same refusals, with an access list letting Everyone write in place of a mode,
/// and a symbolic link (a reparse point) in place of a Unix one.
#[cfg(windows)]
#[test]
fn settings_version_access_lists_and_reparse_points_fail_closed() {
    let (dir, mut c, a) = fixture();
    approve(&mut c, a.clone(), true);
    let json: serde_json::Value = serde_json::from_slice(&fs::read(&c.settings).unwrap()).unwrap();
    assert_eq!(json["version"], 1);
    for bytes in [b"[]".as_slice(), b"{\"version\":2,\"approvals\":[]}"] {
        fs::write(&c.settings, bytes).unwrap();
        owner_only(&c.settings);
        let loaded = Confirmations::open(c.settings.clone()).unwrap();
        assert!(loaded.approvals.is_empty());
        assert!(loaded.startup_warning.is_some());
        assert!(!c.settings.exists());
    }
    fs::write(&c.settings, b"{}").unwrap();
    // What the folder passes down is not protected, so a file made there is not private yet.
    assert!(!private_path(&c.settings));
    crate::private_windows::make_private(&crate::private_windows::tests::open(&c.settings), false)
        .unwrap();
    assert!(
        private_path(&c.settings),
        "the control: a protected file is private"
    );
    windows_files::let_everyone_write(&c.settings);
    assert!(!private_path(&c.settings));
    let mut loaded = Confirmations::open(c.settings.clone()).unwrap();
    assert!(loaded.approvals.is_empty());
    assert!(loaded.persistence_blocked);
    let p = loaded.prepare(a.clone(), Origin::NativeUi).unwrap();
    let id = loaded.queue(p).unwrap();
    let token = loaded.finish(&id, true, true).unwrap().unwrap();
    assert!(loaded.saving_warning(&id).is_some());
    loaded.consume(&id, &token).unwrap();
    assert_eq!(fs::read(&c.settings).unwrap(), b"{}");
    fs::remove_file(&c.settings).unwrap();
    let target = dir.join("target");
    fs::write(&target, b"private bytes").unwrap();
    match std::os::windows::fs::symlink_file(&target, &c.settings) {
        Ok(()) => {
            let loaded = Confirmations::open(c.settings.clone()).unwrap();
            assert!(loaded.approvals.is_empty());
            assert!(loaded.persistence_blocked);
            assert_eq!(fs::read(&target).unwrap(), b"private bytes");
            assert!(fs::symlink_metadata(&c.settings).unwrap().is_symlink());
        }
        // Creating a symbolic link needs a privilege (or developer mode) this account may lack.
        Err(e) => eprintln!("SKIP symbolic-link settings: {e}"),
    }
    // The privacy check reads the open handle: a private file passes through it.
    owner_only(&dir.join(TOOL));
    let file = File::open(dir.join(TOOL)).unwrap();
    assert!(private_file(&file).is_ok());
    remove(&dir);
}

#[test]
fn settings_create_private_parent() {
    let (dir, _, a) = fixture();
    let parent = dir.join("private");
    let mut c = Confirmations::open(parent.join("settings.json")).unwrap();
    approve(&mut c, a, true);
    #[cfg(unix)]
    assert_eq!(fs::metadata(parent).unwrap().mode() & 0o777, 0o700);
    #[cfg(windows)]
    {
        assert!(private_path(&parent));
        // Made private as the temporary file it was, before it replaced the settings.
        assert!(private_path(&parent.join("settings.json")));
    }
    remove(&dir);
}

#[test]
fn review_escapes_unicode_format_characters_and_rejects_non_utf8_resolutions() {
    let (dir, mut c, mut a) = fixture();
    let chars = [
        '\u{ad}',
        '\u{61c}',
        '\u{200b}',
        '\u{200c}',
        '\u{200d}',
        '\u{200e}',
        '\u{200f}',
        '\u{202a}',
        '\u{202b}',
        '\u{202c}',
        '\u{202d}',
        '\u{202e}',
        '\u{2060}',
        '\u{2066}',
        '\u{2067}',
        '\u{2068}',
        '\u{2069}',
        '\u{feff}',
        '\u{e0001}',
    ];
    a.what.extend(chars);
    a.chain[0].id.push('\u{202e}');
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    let review = p.shown_text();
    for ch in chars {
        assert!(!review.contains(ch));
    }
    for escaped in [
        "\\u00ad",
        "\\u061c",
        "\\u200b",
        "\\u202e",
        "\\ufeff",
        "\\udb40\\udc01",
    ] {
        assert!(review.contains(escaped), "missing {escaped}");
    }
    assert!(!p.notice().contains('\u{202e}'));
    // A link that resolves to a name that is not UTF-8; macOS file systems refuse such names.
    #[cfg(all(unix, not(target_os = "macos")))]
    {
        use std::os::unix::{ffi::OsStringExt, fs::symlink};
        let invalid = dir.join(std::ffi::OsString::from_vec(vec![b'd', 0xff]));
        fs::create_dir(&invalid).unwrap();
        let link = dir.join("utf8-link");
        symlink(&invalid, &link).unwrap();
        a.launch.as_mut().unwrap().cwd = link;
        assert!(c.prepare(a, Origin::NativeUi).is_err());
    }
    #[cfg(any(windows, target_os = "macos"))]
    let _ = a;
    remove(&dir);
}

#[test]
fn page_import_origins_and_expired_gestures_cannot_use_remembered_approval() {
    let (dir, mut c, a) = fixture();
    let (id, token) = approve(&mut c, a.clone(), true);
    c.consume(&id, &token).unwrap();
    for origin in [Origin::Page, Origin::Import] {
        let p = c.prepare(a.clone(), origin).unwrap();
        assert!(c
            .remembered(
                p,
                Gesture {
                    created: Instant::now(),
                    origin: Origin::NativeUi,
                }
            )
            .is_err());
        assert!(c.issued.is_empty());
    }
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    assert!(c
        .remembered(
            p,
            Gesture {
                created: Instant::now() - Duration::from_secs(5),
                origin: Origin::NativeUi,
            }
        )
        .is_err());
    for origin in [Origin::Main, Origin::Tray, Origin::NativeUi] {
        let p = c.prepare(a.clone(), origin).unwrap();
        let token = c
            .remembered(
                p,
                Gesture {
                    created: Instant::now(),
                    origin: Origin::NativeUi,
                },
            )
            .ok()
            .unwrap();
        let id = c.token_action(&token).unwrap().to_string();
        c.consume(&id, &token).unwrap();
    }
    remove(&dir);
}

#[test]
fn page_pending_limit_cannot_exhaust_trusted_capacity() {
    let (dir, mut c, a) = fixture();
    for i in 0..PAGE_PENDING_LIMIT {
        let mut page = a.clone();
        page.chain[0].id = format!("page/{i}");
        let p = c.prepare_page(page).unwrap();
        c.queue(p).unwrap();
    }
    let p = c.prepare_page(a.clone()).unwrap();
    assert!(c.queue(p).is_err());
    for _ in 0..TRUSTED_PENDING_LIMIT {
        let p = c.prepare(a.clone(), Origin::Main).unwrap();
        c.queue(p).unwrap();
    }
    let p = c.prepare(a.clone(), Origin::Main).unwrap();
    assert!(c.queue(p).is_err());
    assert_eq!(c.pending.len(), PAGE_PENDING_LIMIT + TRUSTED_PENDING_LIMIT);
    remove(&dir);
}

#[test]
fn self_update_supersedes_old_recipe_approval_and_outstanding_tokens() {
    let (dir, mut c, a) = fixture();
    let (_, old_token) = approve(&mut c, a.clone(), true);
    let old_approval = c.approvals[0].id.clone();
    let exe = &a.launch.as_ref().unwrap().executable;
    fs::rename(exe, dir.join("old-tool")).unwrap();
    program(exe, EXIT_1);
    approve(&mut c, a, true);
    assert_eq!(c.approvals.len(), 1);
    assert_ne!(c.approvals[0].id, old_approval);
    assert!(!c.issued.contains_key(&old_token));
    remove(&dir);
}

#[cfg(target_os = "linux")]
#[test]
fn opened_script_fexecve_requires_clearing_cloexec_and_uses_dev_fd_path() {
    use std::os::fd::AsRawFd;
    let target = std::env::var_os("CARGO_TARGET_TMPDIR")
        .map(PathBuf::from)
        .or_else(|| std::env::var_os("CARGO_TARGET_DIR").map(PathBuf::from))
        .unwrap_or_else(|| std::env::current_exe().unwrap().parent().unwrap().into());
    fs::create_dir_all(&target).unwrap();
    // The folder of this test binary allows exec when the binary was started in the usual way,
    // so a mount that forbids exec on the other two cannot make the test skip its check.
    let binary_dir: PathBuf = std::env::current_exe().unwrap().parent().unwrap().into();
    let mut executable_root = None;
    for root in [target, binary_dir, std::env::temp_dir()] {
        let f = File::open(&root).unwrap();
        let mut vfs = std::mem::MaybeUninit::<libc::statvfs>::uninit();
        assert_eq!(
            unsafe { libc::fstatvfs(f.as_raw_fd(), vfs.as_mut_ptr()) },
            0
        );
        if unsafe { vfs.assume_init() }.f_flag & libc::ST_NOEXEC == 0 {
            executable_root = Some(root);
            break;
        }
    }
    // No skip: a test that returns here without launching proves nothing, in CI or anywhere.
    let root = executable_root.expect(
        "no folder allows exec: the target folder, the folder of this test binary and the system \
         temporary folder are all mounted noexec, so the fexecve launch check cannot run",
    );
    let (dir, mut c, a) = fixture_in(&root);
    let exe = &a.launch.as_ref().unwrap().executable;
    fs::write(
        exe,
        b"#!/bin/sh\ncase \"$0\" in /dev/fd/*) exit 42;; *) exit 43;; esac\n",
    )
    .unwrap();
    let (id, token) = approve(&mut c, a, false);
    let authorized = c.consume(&id, &token).unwrap();
    let fd = authorized.executable().unwrap().as_raw_fd();
    let flags = unsafe { libc::fcntl(fd, libc::F_GETFD) };
    assert_ne!(flags & libc::FD_CLOEXEC, 0);
    let arg = std::ffi::CString::new("fixture").unwrap();
    let argv = [arg.as_ptr(), std::ptr::null()];
    let envp = [std::ptr::null()];
    for clear in [false, true] {
        let pid = unsafe { libc::fork() };
        assert!(pid >= 0);
        if pid == 0 {
            // Only async-signal-safe calls between fork and exec/_exit.
            unsafe {
                if clear && libc::fcntl(fd, libc::F_SETFD, flags & !libc::FD_CLOEXEC) == -1 {
                    libc::_exit(99);
                }
                libc::fexecve(fd, argv.as_ptr(), envp.as_ptr());
                libc::_exit(if !clear && *libc::__errno_location() == libc::ENOENT {
                    41
                } else {
                    99
                });
            }
        }
        let mut status = 0;
        assert_eq!(unsafe { libc::waitpid(pid, &mut status, 0) }, pid);
        assert!(libc::WIFEXITED(status));
        assert_eq!(libc::WEXITSTATUS(status), if clear { 42 } else { 41 });
    }
    remove(&dir);
}

#[test]
fn failed_remembering_preserves_one_time_confirmation() {
    let (dir, mut c, a) = fixture();
    c.persistence_blocked = true;
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    let id = c.queue(p).unwrap();
    assert!(
        !c.review(&id).unwrap().1,
        "disabled persistence must hide Remember"
    );
    let token = c.finish(&id, true, true).unwrap().unwrap();
    assert!(c.saving_warning(&id).is_some());
    c.consume(&id, &token).unwrap();
    assert!(c.approvals.is_empty());
    c.persistence_blocked = false;
    // Reproduce a real save failure after the native review was prepared.
    let p = c.prepare(a, Origin::NativeUi).unwrap();
    let id = c.queue(p).unwrap();
    fs::create_dir(&c.settings).unwrap();
    let token = c.finish(&id, true, true).unwrap().unwrap();
    assert!(c.saving_warning(&id).is_some());
    c.consume(&id, &token).unwrap();
    assert!(c.approvals.is_empty());
    remove(&dir);
}

#[test]
fn review_escapes_controls_and_pango_line_separators() {
    let (dir, mut c, mut a) = fixture();
    let chars = [
        '\u{7f}', '\u{80}', '\u{85}', '\u{9f}', '\u{2028}', '\u{2029}',
    ];
    a.what.extend(chars);
    let p = c.prepare(a, Origin::NativeUi).unwrap();
    let review = p.shown_text();
    for ch in chars {
        assert!(!review.contains(ch));
    }
    for escaped in [
        "\\u007f", "\\u0080", "\\u0085", "\\u009f", "\\u2028", "\\u2029",
    ] {
        assert!(review.contains(escaped));
    }
    assert!(
        review.contains("\n  "),
        "host JSON formatting must stay readable"
    );
    remove(&dir);
}

#[test]
fn startup_warning_escapes_bad_file_text() {
    let (dir, mut c, _) = fixture();
    c.settings = dir.join("settings-\u{85}\u{202e}\u{2028}.json");
    fs::write(
        &c.settings,
        "{\"version\":\"unsafe\u{85}\u{202e}\u{2028}\",\"approvals\":[]}",
    )
    .unwrap();
    owner_only(&c.settings);
    let loaded = Confirmations::open(c.settings.clone()).unwrap();
    let warning = loaded.startup_warning.unwrap();
    assert!(warning.contains("\\u0085"));
    assert!(warning.contains("\\u202e"));
    assert!(warning.contains("\\u2028"));
    assert!(
        !warning.contains('\u{85}')
            && !warning.contains('\u{202e}')
            && !warning.contains('\u{2028}')
    );
    remove(&dir);
}

#[cfg(windows)]
#[test]
fn writable_settings_parent_is_tightened_on_open_and_save() {
    let (dir, mut c, a) = fixture();
    windows_files::let_everyone_write(&dir);
    assert!(!private_path(&dir));
    let loaded = Confirmations::open(c.settings.clone()).unwrap();
    assert!(!loaded.persistence_blocked);
    assert!(private_path(&dir));
    windows_files::let_everyone_write(&dir);
    approve(&mut c, a, true);
    assert!(private_path(&dir));
    remove(&dir);
}

#[cfg(unix)]
#[test]
fn writable_settings_parent_is_tightened_on_open_and_save() {
    let (dir, mut c, a) = fixture();
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o777)).unwrap();
    let loaded = Confirmations::open(c.settings.clone()).unwrap();
    assert!(!loaded.persistence_blocked);
    assert_eq!(fs::metadata(&dir).unwrap().mode() & 0o777, 0o700);
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o777)).unwrap();
    approve(&mut c, a, true);
    assert_eq!(fs::metadata(&dir).unwrap().mode() & 0o777, 0o700);
    remove(&dir);
}

/// macOS: a 0600 settings file an extended access list lets others change is not trusted, as a
/// group- or world-writable one is not; the folder is emptied of entries on open, so a new
/// settings file inherits none.
#[cfg(target_os = "macos")]
#[test]
fn settings_with_an_access_list_are_not_trusted_and_the_folder_is_emptied() {
    let (dir, mut c, a) = fixture();
    let has = |p: &Path| crate::private_macos::has_entries(&File::open(p).unwrap()).unwrap();
    approve(&mut c, a.clone(), true);
    assert!(c.settings.exists());
    crate::private_macos::add_entry(&c.settings, "everyone allow write");
    let loaded = Confirmations::open(c.settings.clone()).unwrap();
    assert!(loaded.approvals.is_empty());
    assert!(loaded.persistence_blocked);
    fs::remove_file(&c.settings).unwrap();
    crate::private_macos::add_entry(&dir, "everyone allow add_file,file_inherit");
    let loaded = Confirmations::open(c.settings.clone()).unwrap();
    assert!(!loaded.persistence_blocked);
    assert!(!has(&dir));
    // Saving empties the folder too: an entry added after the open is not passed on to the
    // settings file the save makes.
    crate::private_macos::add_entry(&dir, "everyone allow add_file,file_inherit");
    assert!(has(&dir));
    approve(&mut c, a, true);
    assert!(c.settings.exists());
    assert!(!has(&dir));
    assert!(!has(&c.settings));
    remove(&dir);
}

#[test]
fn an_app_action_is_reviewed_by_its_description_alone() {
    let (dir, mut c, a) = fixture();
    let what = "Rotate the management key of http://127.0.0.1:8400.\nSecond line\u{2028}";
    let p = c.prepare_app(what.into(), 7).unwrap();
    assert_eq!(
        p.shown_text(),
        "Rotate the management key of http://127.0.0.1:8400.\nSecond line\\u2028"
    );
    let id = c.queue(p).unwrap();
    assert!(!c.review(&id).unwrap().1, "an app action offered Remember");
    let t = c.finish(&id, true, false).unwrap().unwrap();
    assert!(c.consume(&id, &t).is_ok());
    // A launch is still reviewed in full.
    let review = c.prepare(a, Origin::NativeUi).unwrap().shown_text();
    assert!(review.contains("\"digest\"") && review.contains("\"argv\""));
    remove(&dir);
}

/// Adopting a pluxy program is reviewed in plain words alone: what the app does, the program, its
/// size and digest, and that it is not signed; no raw action and no "native tool".
#[test]
fn the_adopt_review_says_in_plain_words_what_runs() {
    let (dir, mut c, _) = fixture();
    let program_path = dir.join("pluxy");
    program(&program_path, &vec![b'x'; 2_500_000]);
    let sha256 = "0123456789abcdef".repeat(4);
    let app = Package {
        id: "pluxy app".into(),
        source: "the app's window".into(),
        digest: String::new(),
        signer: String::new(),
        contract: "the app's own core".into(),
        generation: 0,
    };
    let action = Action {
        what: crate::core_updates::adopt_what(&program_path, 2_500_000, &sha256),
        chain: vec![app],
        executor: Package {
            id: "custom core".into(),
            source: program_path.display().to_string(),
            digest: sha256.clone(),
            signer: String::new(),
            contract: "core program".into(),
            generation: 0,
        },
        native_tool: true,
        files: vec![program_path.clone()],
        launch: None,
        environment: None,
    };
    let review = c.prepare_plain(action.clone()).unwrap().shown_text();
    assert_eq!(
        review,
        format!(
            "Run this program as pluxy?\n\nThe app copies {} (2.5 MB, SHA-256 0123456789ab) into its own folder and runs the copy. It is not signed: use only a program you trust.",
            program_path.display()
        )
    );
    for word in ["native tool", "core", "{", "\\u"] {
        assert!(!review.contains(word), "{word}: {review}");
    }
    // Its notice keeps the lines too.
    let notice = c.prepare_plain(action.clone()).unwrap().notice();
    assert!(
        notice.starts_with(
            "Asked by: pluxy app\nWhat: Run this program as pluxy?\n\nThe app copies "
        ),
        "{notice}"
    );
    // A path cannot add lines of its own to the review.
    let odd = dir.join("pluxy\nIt is signed by pluxy.");
    let what = crate::core_updates::adopt_what(&odd, 2_500_000, &sha256);
    assert_eq!(what.lines().count(), 3, "{what}");
    assert!(what.contains("pluxy\\u000aIt is signed"), "{what}");
    // The file is still bound: replaced, the review is refused.
    let p = c.prepare_plain(action).unwrap();
    fs::remove_file(&program_path).unwrap();
    program(&program_path, b"other");
    assert!(p.check().is_err(), "a replaced program was still accepted");
    remove(&dir);
}

/// The launch notice puts who asked and what on lines of their own, never `\u000a`, while
/// what a requester wrote still cannot add lines of its own.
#[test]
fn the_launch_notice_holds_real_lines() {
    let (dir, mut c, a) = fixture();
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    assert_eq!(p.notice(), "Asked by: fixture/tool\nWhat: Start fixture");
    let forged = Action {
        what: "Start fixture\nOutcome: confirmed".into(),
        ..a
    };
    let notice = c.prepare(forged, Origin::NativeUi).unwrap().notice();
    assert_eq!(
        notice,
        "Asked by: fixture/tool\nWhat: Start fixture\\u000aOutcome: confirmed"
    );
    assert_eq!(notice.lines().count(), 2, "{notice}");
    remove(&dir);
}

#[test]
fn an_app_confirmation_that_never_finishes_is_withdrawn() {
    let (dir, mut c, _) = fixture();
    let p = c
        .prepare_app("Rotate the management key.".into(), 7)
        .unwrap();
    let id = c.queue(p).unwrap();
    assert!(c.pending.contains_key(&id));
    let c = std::sync::Mutex::new(c);
    // No result by the deadline: it fails, and does not keep one of the pending places.
    let deadline = Instant::now();
    std::thread::sleep(Duration::from_millis(5));
    let e = super::wait_app(&c, &id, deadline).unwrap_err();
    assert!(e.contains("did not finish"), "{e}");
    assert!(
        !c.lock().unwrap().pending.contains_key(&id),
        "the timed-out confirmation is still pending"
    );
    // The control: a confirmed one is waited for and spent.
    let mut g = c.lock().unwrap();
    let p = g
        .prepare_app("Rotate the management key.".into(), 7)
        .unwrap();
    let id = g.queue(p).unwrap();
    let t = g.finish(&id, true, false).unwrap().unwrap();
    g.results.insert(id.clone(), Ok(Some(t.clone())));
    drop(g);
    assert_eq!(
        super::wait_app(&c, &id, Instant::now() + Duration::from_secs(5)),
        Ok(true)
    );
    assert!(
        c.lock().unwrap().consume(&id, &t).is_err(),
        "token spent twice"
    );
    remove(&dir);
}

#[test]
fn the_remember_text_names_the_secrets_a_launch_uses() {
    let (dir, mut c, mut a) = fixture();
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    assert_eq!(
        p.remember_text(),
        "_Remember this exact executable, argv and cwd"
    );
    a.launch.as_mut().unwrap().secret_uses.push(SecretUse {
        reference: "s-0123".into(),
        recipient: "fixture/tool".into(),
        purpose: "api_key".into(),
    });
    let p = c.prepare(a, Origin::NativeUi).unwrap();
    assert_eq!(
        p.remember_text(),
        "_Remember this exact executable, argv, cwd and secrets: s-0123 for fixture/tool's api__key"
    );
    let _ = fs::remove_dir_all(dir);
}

/// Action a opening file f, which its argv names, inside folder w, with opener in f's folder.
#[cfg(target_os = "linux")]
fn opening(a: &Action, opener: &Path, f: &Path, w: &Path) -> Action {
    let mut a = a.clone();
    let l = a.launch.as_mut().unwrap();
    l.executable = opener.into();
    l.argv = vec![f.to_string_lossy().into_owned()];
    l.cwd = f.parent().unwrap().into();
    l.opens = Some((f.into(), w.into()));
    a
}

/// A private file at p holding bytes.
#[cfg(target_os = "linux")]
fn private(p: &Path, bytes: &[u8]) {
    fs::write(p, bytes).unwrap();
    fs::set_permissions(p, fs::Permissions::from_mode(0o600)).unwrap();
}

/// The fixture's folder with an opener `xdg-open` the confirmations accept, a folder `codex`
/// holding `sessions/rollout.jsonl` and a private file outside it.
#[cfg(target_os = "linux")]
fn open_fixture() -> (PathBuf, Confirmations, Action, PathBuf, PathBuf, PathBuf) {
    let (dir, mut c, a) = fixture();
    let dir = fs::canonicalize(&dir).unwrap();
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o700)).unwrap();
    let bin = dir.join("bin");
    fs::create_dir(&bin).unwrap();
    let opener = bin.join("xdg-open");
    fs::write(&opener, b"#!/bin/sh\nexit 0\n").unwrap();
    fs::set_permissions(&opener, fs::Permissions::from_mode(0o700)).unwrap();
    c.opener_folders = vec![bin];
    let within = dir.join("codex");
    let sessions = within.join("sessions");
    fs::create_dir_all(&sessions).unwrap();
    for d in [&within, &sessions] {
        fs::set_permissions(d, fs::Permissions::from_mode(0o700)).unwrap();
    }
    let file = sessions.join("rollout.jsonl");
    private(&file, b"{\"type\":\"session_meta\"}\n");
    private(&dir.join("private.txt"), b"not the tool's\n");
    (dir, c, a, opener, within, file)
}

#[cfg(target_os = "linux")]
#[test]
fn a_launch_opens_only_a_plain_file_inside_its_folder() {
    let (dir, mut c, a, opener, within, file) = open_fixture();
    let sessions = within.join("sessions");
    let outside = dir.join("private.txt");
    std::os::unix::fs::symlink(&outside, sessions.join("link.jsonl")).unwrap();
    let script = sessions.join("run.sh");
    fs::write(&script, b"#!/bin/sh\n").unwrap();
    fs::set_permissions(&script, fs::Permissions::from_mode(0o700)).unwrap();
    private(
        &sessions.join("page.jsonl"),
        b"<html><body><script>x</script></body></html>\n",
    );
    private(
        &sessions.join("app.desktop"),
        b"[Desktop Entry]\nType=Application\nExec=true\n",
    );
    private(
        &sessions.join("notes.txt"),
        b"[Desktop Entry]\nType=Application\n",
    );
    let shared = dir.join("shared");
    fs::create_dir(&shared).unwrap();
    fs::set_permissions(&shared, fs::Permissions::from_mode(0o770)).unwrap();
    private(&shared.join("a.jsonl"), b"{}\n");
    std::os::unix::fs::symlink(&within, dir.join("codex-link")).unwrap();

    // Inside: its headline names the resolved file, its type, the folder and the opener.
    let p = c
        .prepare(opening(&a, &opener, &file, &within), Origin::Import)
        .unwrap();
    let review = p.shown_text();
    let headline = format!(
        "Opens the file {} (text/plain, inside {}) with {}.",
        file.display(),
        within.display(),
        opener.display()
    );
    assert!(review.contains(&headline), "{review}");
    // Text whose name says nothing: still plain text.
    assert!(c
        .prepare(
            opening(&a, &opener, &sessions.join("notes.txt"), &within),
            Origin::Import
        )
        .is_ok());

    let dotted = within.join("../private.txt");
    let home_parent = std::env::var_os("HOME")
        .and_then(|h| fs::canonicalize(h).ok())
        .and_then(|h| h.parent().map(PathBuf::from));
    let mut cases: Vec<(PathBuf, PathBuf, &str)> = vec![
        (outside.clone(), within.clone(), "inside its within folder"),
        (dotted, within.clone(), "inside its within folder"),
        (
            sessions.join("link.jsonl"),
            within.clone(),
            "inside its within folder",
        ),
        (script, within.clone(), "must not be executable"),
        (sessions.clone(), within.clone(), "regular file"),
        (
            sessions.join("page.jsonl"),
            within.clone(),
            "plain text, not text/html",
        ),
        (sessions.join("app.desktop"), within.clone(), ".desktop"),
        (
            shared.join("a.jsonl"),
            shared.clone(),
            "writable by no one else",
        ),
        (file.clone(), PathBuf::from("/"), "a folder above it"),
        (file.clone(), file.clone(), "not a folder"),
        (file.clone(), dir.join("codex-link"), "resolved paths"),
    ];
    if let Some(h) = home_parent {
        cases.push((file.clone(), h, "a folder above it"));
    }
    if let Some(h) = std::env::var_os("HOME").filter(|h| Path::new(h).is_dir()) {
        cases.push((file.clone(), PathBuf::from(h), "a folder above it"));
    }
    for (f, w, says) in cases {
        let e = c
            .prepare(opening(&a, &opener, &f, &w), Origin::Import)
            .err()
            .unwrap_or_else(|| panic!("{} inside {} was accepted", f.display(), w.display()));
        assert!(
            e.contains(says),
            "{} inside {}: {e}",
            f.display(),
            w.display()
        );
    }
    let _ = fs::remove_dir_all(dir);
}

/// The home-folder rule takes the resolved home folder, so these tests need no `HOME` change (the
/// real lookup falls back to the passwd entry, which a test cannot remove).
#[cfg(target_os = "linux")]
#[test]
fn a_within_folder_is_refused_when_the_home_folder_cannot_be_found() {
    for within in ["/home", "/home/someone/work", "/"] {
        let e = within_home_rule(Path::new(within), None).unwrap_err();
        assert!(
            e.contains("home folder could not be found"),
            "{within}: {e}"
        );
    }
}

#[cfg(target_os = "linux")]
#[test]
fn a_within_folder_is_checked_against_the_home_folder_when_it_is_found() {
    let home = Path::new("/home/someone");
    for within in ["/", "/home", "/home/someone"] {
        let e = within_home_rule(Path::new(within), Some(home)).unwrap_err();
        assert!(e.contains("a folder above it"), "{within}: {e}");
    }
    for within in ["/home/someone/work", "/home/other", "/srv/data"] {
        assert!(
            within_home_rule(Path::new(within), Some(home)).is_ok(),
            "{within}"
        );
    }
}

#[cfg(target_os = "linux")]
#[test]
fn a_launch_that_opens_a_file_has_the_openers_shape() {
    let (dir, mut c, a, opener, within, file) = open_fixture();
    let ok = opening(&a, &opener, &file, &within);
    assert!(c.prepare(ok.clone(), Origin::Import).is_ok());
    let mut changes: Vec<(Action, &str)> = vec![];
    let mut x = ok.clone();
    x.launch.as_mut().unwrap().argv.push("--extra".into());
    changes.push((x, "runs exactly"));
    let mut x = ok.clone();
    x.launch.as_mut().unwrap().argv.insert(0, "open".into());
    changes.push((x, "runs exactly"));
    let mut x = ok.clone();
    x.launch.as_mut().unwrap().cwd = within.clone();
    changes.push((x, "folder of the file"));
    let mut x = ok.clone();
    // The fixture's own program, not an opener.
    x.launch.as_mut().unwrap().executable = dir.join("tool");
    changes.push((x, "runs xdg-open or gio"));
    let mut x = ok.clone();
    x.launch.as_mut().unwrap().program = Some(opener.clone());
    changes.push((x, "no terminal program"));
    for (x, says) in changes {
        let e = c.prepare(x, Origin::Import).err().unwrap();
        assert!(e.contains(says), "{says}: {e}");
    }
    // gio takes `open` before the file.
    let gio = opener.with_file_name("gio");
    fs::copy(&opener, &gio).unwrap();
    let mut g = opening(&a, &gio, &file, &within);
    g.launch.as_mut().unwrap().argv.insert(0, "open".into());
    assert!(c.prepare(g, Origin::Import).is_ok());
    let _ = fs::remove_dir_all(dir);
}

#[cfg(target_os = "linux")]
#[test]
fn every_folder_between_an_opened_file_and_its_folder_must_be_the_persons_own() {
    let (dir, mut c, a, opener, within, _) = open_fixture();
    // A clean nested path passes.
    let deep = within.join("sessions/2026/11");
    fs::create_dir_all(&deep).unwrap();
    for d in [within.join("sessions/2026"), deep.clone()] {
        fs::set_permissions(&d, fs::Permissions::from_mode(0o700)).unwrap();
    }
    let nested = deep.join("rollout.jsonl");
    private(&nested, b"{}\n");
    assert!(c
        .prepare(opening(&a, &opener, &nested, &within), Origin::Import)
        .is_ok());
    // A folder between them that others may write is refused, whichever it is and however they
    // may write it: the file and the folder itself are the person's own.
    for (folder, mode) in [
        (within.join("sessions"), 0o770),
        (within.join("sessions/2026"), 0o707),
        (deep.clone(), 0o777),
    ] {
        fs::set_permissions(&folder, fs::Permissions::from_mode(mode)).unwrap();
        let e = c
            .prepare(opening(&a, &opener, &nested, &within), Origin::Import)
            .err()
            .unwrap_or_else(|| panic!("{} at {mode:o} was accepted", folder.display()));
        assert!(
            e.contains("writable by no one else") && e.contains(&folder.display().to_string()),
            "{}: {e}",
            folder.display()
        );
        fs::set_permissions(&folder, fs::Permissions::from_mode(0o700)).unwrap();
    }
    assert!(c
        .prepare(opening(&a, &opener, &nested, &within), Origin::Import)
        .is_ok());
    let _ = fs::remove_dir_all(dir);
}

#[cfg(target_os = "linux")]
#[test]
fn a_confirmed_open_is_refused_once_a_folder_between_its_file_and_folder_turns_writable() {
    let (dir, mut c, a, opener, within, file) = open_fixture();
    let sessions = within.join("sessions");
    let ok = opening(&a, &opener, &file, &within);
    let (id, t) = approve(&mut c, ok.clone(), false);
    assert!(c.consume(&id, &t).is_ok());
    // Made writable by the group after the confirmation: the file's own metadata did not change.
    let (id, t) = approve(&mut c, ok.clone(), false);
    fs::set_permissions(&sessions, fs::Permissions::from_mode(0o770)).unwrap();
    let e = c.consume(&id, &t).err().unwrap();
    assert!(e.contains("writable by no one else"), "{e}");
    fs::set_permissions(&sessions, fs::Permissions::from_mode(0o700)).unwrap();
    // Replaced by a link to another folder of its own.
    let (id, t) = approve(&mut c, ok, false);
    fs::rename(&sessions, within.join("moved")).unwrap();
    std::os::unix::fs::symlink(within.join("moved"), &sessions).unwrap();
    assert!(c.consume(&id, &t).is_err());
    let _ = fs::remove_dir_all(dir);
}

#[cfg(target_os = "linux")]
#[test]
fn a_confirmed_open_is_refused_once_its_file_or_folder_changed() {
    let (dir, mut c, a, opener, within, file) = open_fixture();
    let ok = opening(&a, &opener, &file, &within);
    let (id, t) = approve(&mut c, ok.clone(), false);
    c.consume(&id, &t).unwrap();
    // Written in place.
    let (id, t) = approve(&mut c, ok.clone(), false);
    fs::OpenOptions::new()
        .append(true)
        .open(&file)
        .unwrap()
        .write_all(b"{\"more\":1}\n")
        .unwrap();
    assert!(c.consume(&id, &t).is_err());
    // Its mode changed.
    let (id, t) = approve(&mut c, ok.clone(), false);
    fs::set_permissions(&file, fs::Permissions::from_mode(0o640)).unwrap();
    assert!(c.consume(&id, &t).is_err());
    fs::set_permissions(&file, fs::Permissions::from_mode(0o600)).unwrap();
    // Another file at its name.
    let (id, t) = approve(&mut c, ok.clone(), false);
    fs::remove_file(&file).unwrap();
    private(&file, b"{}\n");
    assert!(c.consume(&id, &t).is_err());
    // Its folder replaced by a link to another.
    let (id, t) = approve(&mut c, ok, false);
    fs::rename(&within, dir.join("moved")).unwrap();
    std::os::unix::fs::symlink(dir.join("moved"), &within).unwrap();
    assert!(c.consume(&id, &t).is_err());
    let _ = fs::remove_dir_all(dir);
}
#[test]
fn where_an_action_runs_is_shown_first_and_bound_into_a_remembered_approval() {
    let (dir, mut c, a) = fixture();
    let bob = Action {
        environment: Some("Runs in WSL: distribution Ubuntu, user bob (/home/bob).".into()),
        ..a.clone()
    };
    let p = c.prepare(bob.clone(), Origin::NativeUi).unwrap();
    let review = p.shown_text();
    assert!(
        review.starts_with(
            "Runs in WSL: distribution Ubuntu, user bob (/home/bob).\n\nStart fixture\n\nThis add-on runs as you"
        ),
        "{review}"
    );
    let plain = c.prepare(a.clone(), Origin::NativeUi).unwrap().shown_text();
    assert!(!plain.contains("Runs in WSL"), "{plain}");
    let (id, t) = approve(&mut c, bob.clone(), true);
    c.consume(&id, &t).unwrap();
    let gesture = || Gesture {
        created: Instant::now(),
        origin: Origin::NativeUi,
    };
    // The approval remembered for bob stands for bob only: not for alice, not for this computer.
    let alice = Action {
        environment: Some("Runs in WSL: distribution Ubuntu, user alice (/home/alice).".into()),
        ..a.clone()
    };
    for other in [alice, a] {
        let p = c.prepare(other, Origin::NativeUi).unwrap();
        assert!(c.remembered(p, gesture()).is_err());
    }
    let p = c.prepare(bob, Origin::NativeUi).unwrap();
    assert!(c.remembered(p, gesture()).is_ok());
    remove(&dir);
}

/// Windows starts only program images: a file named `.exe` or `.com` that starts `MZ`. A batch
/// file, or a file named `.exe` that is not an image, is no program for review.
#[cfg(windows)]
#[test]
fn only_program_images_are_programs() {
    let (dir, mut c, action) = fixture();
    for (name, bytes, ok) in [
        ("p.exe", b"MZ image".as_slice(), true),
        ("p.com", b"MZ".as_slice(), true),
        ("p.cmd", b"MZ".as_slice(), false),
        ("p.bat", b"@exit /b 0\r\n".as_slice(), false),
        ("q.exe", b"not an image".as_slice(), false),
    ] {
        let p = dir.join(name);
        program(&p, bytes);
        let mut a = action.clone();
        a.launch.as_mut().unwrap().executable = p;
        let r = c.prepare(a, Origin::NativeUi);
        assert_eq!(r.is_ok(), ok, "{name}: {:?}", r.err());
    }
    remove(&dir);
}

/// Makes action's launch open file inside within, as the execution program prepares it here: on
/// Windows its own program (the fixture's, as `pluxy/execution`'s) in its `open` mode, on macOS
/// `/usr/bin/open`.
#[cfg(not(target_os = "linux"))]
fn opening(action: &mut Action, file: &Path, within: &Path) {
    let shown = |p: &Path| PathBuf::from(open_os::plain(&fs::canonicalize(p).unwrap()).unwrap());
    opening_named(action, &shown(file), &shown(within));
}

/// `opening`, with file and within named exactly as given, not as a person reads them resolved.
#[cfg(not(target_os = "linux"))]
fn opening_named(action: &mut Action, file: &Path, within: &Path) {
    let (f, w) = (file.to_path_buf(), within.to_path_buf());
    let l = action.launch.as_mut().unwrap();
    #[cfg(windows)]
    {
        l.argv = vec!["open".into(), "--".into(), f.to_string_lossy().into_owned()];
        l.adapter_program = Some(l.executable.clone());
        l.terminal_adapter.id = crate::launcher::PACKAGE.into();
    }
    #[cfg(target_os = "macos")]
    {
        l.executable = "/usr/bin/open".into();
        l.argv = vec![f.to_string_lossy().into_owned()];
    }
    l.cwd = f.parent().unwrap().into();
    l.opens = Some((f, w));
}

/// Windows and macOS: a launch opens a file by its extension's type, with the system's opener,
/// and says so; another extension, another opener, other arguments and a root as the within
/// folder are refused.
#[cfg(not(target_os = "linux"))]
#[test]
fn opens_a_file_by_its_extension() {
    let (dir, mut c, action) = fixture();
    let within = dir.join("sessions");
    fs::create_dir(&within).unwrap();
    // Its content does not decide its type: the extension does.
    let file = within.join("rollout.jsonl");
    fs::write(&file, "<html><script></script></html>").unwrap();
    let mut a = action.clone();
    opening(&mut a, &file, &within);
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    let review = p.shown_text();
    let system = if cfg!(windows) { "Windows" } else { "macOS" };
    assert!(
        review.contains("(application/jsonl, inside")
            && review.contains(&format!("with the app {system} opens .jsonl files with")),
        "{review}"
    );
    #[cfg(windows)]
    assert!(p.windowless, "the open mode starts with no console");

    let html = within.join("page.html");
    fs::write(&html, "x").unwrap();
    let mut b = action.clone();
    opening(&mut b, &html, &within);
    let e = c.prepare(b, Origin::NativeUi).err().expect("refused");
    assert!(
        e.contains("must be .txt, .json, .jsonl, .ndjson, .md or .log"),
        "{e}"
    );

    let mut b = a.clone();
    b.launch.as_mut().unwrap().argv.push("--extra".into());
    let e = c.prepare(b, Origin::NativeUi).err().expect("refused");
    assert!(e.contains("an open launch runs exactly"), "{e}");

    let mut b = a.clone();
    #[cfg(windows)]
    {
        b.launch.as_mut().unwrap().adapter_program = None;
    }
    #[cfg(target_os = "macos")]
    {
        b.launch.as_mut().unwrap().executable = dir.join(TOOL);
    }
    let e = c.prepare(b, Origin::NativeUi).err().expect("refused");
    assert!(e.contains("a launch that opens a file runs"), "{e}");

    let mut b = a.clone();
    let root = fs::canonicalize(&within).unwrap();
    let root = PathBuf::from(open_os::plain(root.ancestors().last().unwrap()).unwrap());
    b.launch.as_mut().unwrap().opens.as_mut().unwrap().1 = root;
    let e = c.prepare(b, Origin::NativeUi).err().expect("refused");
    assert!(e.contains("cannot be a root"), "{e}");
    remove(&within);
    remove(&dir);
}

/// The open launch of a file `rollout.jsonl` in a folder `2026` inside the within folder
/// `sessions` of a fixture: the fixture, its confirmations, the launch, the within folder, the
/// folder between and the file.
#[cfg(not(target_os = "linux"))]
fn nested_open() -> (PathBuf, Confirmations, Action, PathBuf, PathBuf, PathBuf) {
    let (dir, c, mut a) = fixture();
    let within = dir.join("sessions");
    let between = within.join("2026");
    fs::create_dir_all(&between).unwrap();
    let file = between.join("rollout.jsonl");
    fs::write(&file, "{}\n").unwrap();
    opening(&mut a, &file, &within);
    (dir, c, a, within, between, file)
}

/// Lets others write path (on: a folder, file or the within folder), or takes that back: on
/// Windows an Everyone write entry; on macOS group write on a folder between, otherwise an access
/// list entry. What the refusal then says.
#[cfg(not(target_os = "linux"))]
fn others_write(path: &Path, kind: &str, on: bool) -> &'static str {
    #[cfg(windows)]
    {
        let _ = kind;
        let args: &[&str] = if on {
            &["/grant", "*S-1-1-0:(W)"]
        } else {
            &["/remove:g", "*S-1-1-0"]
        };
        assert!(crate::private_windows::tests::icacls(path, args));
        "must be your own, writable by no one else"
    }
    #[cfg(target_os = "macos")]
    {
        let chmod = |args: &[&str]| {
            let ok = std::process::Command::new("/bin/chmod")
                .args(args)
                .arg(path)
                .status()
                .unwrap()
                .success();
            assert!(ok, "chmod {args:?} {}", path.display());
        };
        match (kind, on) {
            ("between", true) => chmod(&["775"]),
            ("between", false) => chmod(&["755"]),
            ("file", true) => chmod(&["+a", "group:everyone allow write"]),
            (_, true) => chmod(&["+a", "group:everyone allow add_file"]),
            (_, false) => chmod(&["-N"]),
        }
        if kind == "between" {
            "must be your own, writable by no one else"
        } else {
            "has an access list, which may let others write it"
        }
    }
}

/// Windows and macOS: the file a launch opens, the folder it must be inside and every folder
/// between them must be the person's own and writable by no one else.
#[cfg(not(target_os = "linux"))]
#[test]
fn every_folder_of_an_opened_file_must_be_writable_by_no_one_else() {
    let (dir, mut c, a, within, between, file) = nested_open();
    assert!(c.prepare(a.clone(), Origin::NativeUi).is_ok());
    let shown = |p: &Path| open_os::plain(&fs::canonicalize(p).unwrap()).unwrap();
    for (path, kind, what) in [
        (
            &between,
            "between",
            format!("the folder {}", shown(&between)),
        ),
        (&within, "within", "a launch's within folder".to_string()),
        (&file, "file", "the file a launch opens".to_string()),
    ] {
        let says = others_write(path, kind, true);
        let r = c.prepare(a.clone(), Origin::NativeUi);
        others_write(path, kind, false);
        let e = r
            .err()
            .unwrap_or_else(|| panic!("{kind} others may write was accepted"));
        assert!(e.contains(&what) && e.contains(says), "{kind}: {e}");
        assert!(
            c.prepare(a.clone(), Origin::NativeUi).is_ok(),
            "{kind} put back"
        );
    }
    remove(&within);
    remove(&dir);
}

/// Windows and macOS: a confirmed open is refused at its use once a folder between its file and
/// the folder it must be inside turned writable by others after the confirmation: by the folder
/// rule, which names that folder, the file itself untouched.
#[cfg(not(target_os = "linux"))]
#[test]
fn a_confirmed_open_is_refused_once_a_folder_between_turns_writable() {
    let (dir, mut c, a, within, between, file) = nested_open();
    #[cfg(not(windows))]
    let _ = &file;
    let (id, t) = approve(&mut c, a.clone(), false);
    assert!(c.consume(&id, &t).is_ok());
    let (id, t) = approve(&mut c, a, false);
    #[cfg(windows)]
    let (before, undo) = (file_state(&file), everyone_writes_only(&between));
    #[cfg(target_os = "macos")]
    others_write(&between, "between", true);
    #[cfg(windows)]
    let after = file_state(&file);
    let r = c.consume(&id, &t);
    #[cfg(windows)]
    undo();
    #[cfg(target_os = "macos")]
    others_write(&between, "between", false);
    #[cfg(windows)]
    assert_eq!(before, after, "the folder's change touched the file");
    let e = r.err().expect("refused");
    let folder = open_os::plain(&fs::canonicalize(&between).unwrap()).unwrap();
    assert!(
        e.contains(&format!(
            "the folder {folder} of the file a launch opens must be your own, writable by no one else"
        )),
        "{e}"
    );
    remove(&within);
    remove(&dir);
}

/// Windows: file's access list (`icacls`, without its path) and its change and last-write times,
/// as `Metadata` compares them.
#[cfg(windows)]
fn file_state(file: &Path) -> (String, i64, i64) {
    let out = std::process::Command::new("icacls")
        .arg(file)
        .output()
        .unwrap();
    let text = String::from_utf8_lossy(&out.stdout);
    let shown = file.display().to_string();
    let acl: Vec<&str> = text
        .lines()
        .map(|l| l.strip_prefix(&shown).unwrap_or(l).trim())
        .filter(|l| !l.is_empty() && !l.starts_with("Successfully processed"))
        .collect();
    let i = windows_files::info(&windows_files::open_any(file).unwrap()).unwrap();
    (acl.join("; "), i.ctime, i.mtime)
}

/// Windows: adds to folder's access list an entry letting Everyone write it (`FILE_WRITE_DATA`,
/// adding files), inherited by nothing, with `SetFileSecurityW`, which sets only the folder's own
/// list (unlike `SetNamedSecurityInfo`, it does not go through what is inside). Returns what puts
/// the folder's list back.
#[cfg(windows)]
fn everyone_writes_only(folder: &Path) -> impl FnOnce() {
    use std::os::windows::ffi::OsStrExt;
    use windows_sys::Win32::Security::Authorization::{
        SetEntriesInAclW, EXPLICIT_ACCESS_W, GRANT_ACCESS, NO_MULTIPLE_TRUSTEE, TRUSTEE_IS_SID,
        TRUSTEE_IS_WELL_KNOWN_GROUP, TRUSTEE_W,
    };
    use windows_sys::Win32::Security::{
        CreateWellKnownSid, GetFileSecurityW, GetSecurityDescriptorDacl,
        InitializeSecurityDescriptor, SetFileSecurityW, SetSecurityDescriptorDacl, WinWorldSid,
        ACL, DACL_SECURITY_INFORMATION, NO_INHERITANCE, SECURITY_DESCRIPTOR, SECURITY_MAX_SID_SIZE,
    };
    let name: Vec<u16> = folder.as_os_str().encode_wide().chain([0]).collect();
    // The folder's descriptor (its list only) as it is, to put back.
    let mut n = 0u32;
    // SAFETY: asks for the size only.
    unsafe {
        GetFileSecurityW(
            name.as_ptr(),
            DACL_SECURITY_INFORMATION,
            std::ptr::null_mut(),
            0,
            &mut n,
        )
    };
    let mut old = vec![0u64; (n as usize).div_ceil(8)];
    // SAFETY: old holds n bytes, aligned for a descriptor.
    let got = unsafe {
        GetFileSecurityW(
            name.as_ptr(),
            DACL_SECURITY_INFORMATION,
            old.as_mut_ptr().cast(),
            n,
            &mut n,
        )
    };
    assert_ne!(got, 0, "{}", std::io::Error::last_os_error());
    let (mut present, mut defaulted, mut list) = (0, 0, std::ptr::null_mut::<ACL>());
    // SAFETY: old is a self-relative descriptor; list points into it.
    let got = unsafe {
        GetSecurityDescriptorDacl(
            old.as_mut_ptr().cast(),
            &mut present,
            &mut list,
            &mut defaulted,
        )
    };
    assert!(got != 0 && present != 0, "the folder has no access list");
    let mut sid = [0u8; SECURITY_MAX_SID_SIZE as usize];
    let mut size = sid.len() as u32;
    // SAFETY: sid holds size bytes.
    let got = unsafe {
        CreateWellKnownSid(
            WinWorldSid,
            std::ptr::null_mut(),
            sid.as_mut_ptr().cast(),
            &mut size,
        )
    };
    assert_ne!(got, 0, "{}", std::io::Error::last_os_error());
    let entry = EXPLICIT_ACCESS_W {
        grfAccessPermissions: 0x0002, // FILE_WRITE_DATA, FILE_ADD_FILE
        grfAccessMode: GRANT_ACCESS,
        grfInheritance: NO_INHERITANCE,
        Trustee: TRUSTEE_W {
            pMultipleTrustee: std::ptr::null_mut(),
            MultipleTrusteeOperation: NO_MULTIPLE_TRUSTEE,
            TrusteeForm: TRUSTEE_IS_SID,
            TrusteeType: TRUSTEE_IS_WELL_KNOWN_GROUP,
            ptstrName: sid.as_mut_ptr().cast(),
        },
    };
    let mut new: *mut ACL = std::ptr::null_mut();
    // SAFETY: one valid entry and the folder's list; the new list is LocalAlloc'd into new.
    let r = unsafe { SetEntriesInAclW(1, &entry, list, &mut new) };
    assert_eq!(r, 0, "SetEntriesInAclW: {r}");
    let mut sd = SECURITY_DESCRIPTOR::default();
    let psd: *mut core::ffi::c_void = (&mut sd as *mut SECURITY_DESCRIPTOR).cast();
    // SAFETY: psd points at sd, which lives past the call that uses it; new too.
    let got = unsafe {
        InitializeSecurityDescriptor(psd, 1) != 0
            && SetSecurityDescriptorDacl(psd, 1, new, 0) != 0
            && SetFileSecurityW(name.as_ptr(), DACL_SECURITY_INFORMATION, psd) != 0
    };
    let e = std::io::Error::last_os_error();
    // SAFETY: new came from SetEntriesInAclW and is not used after this.
    unsafe { windows_sys::Win32::Foundation::LocalFree(new.cast()) };
    assert!(got, "setting the folder's access list: {e}");
    move || {
        // SAFETY: old is the folder's descriptor as it was, self-relative.
        let got = unsafe {
            SetFileSecurityW(
                name.as_ptr(),
                DACL_SECURITY_INFORMATION,
                old.as_mut_ptr().cast(),
            )
        };
        assert_ne!(got, 0, "{}", std::io::Error::last_os_error());
    }
}

/// Windows: the file a launch opens is on a local drive, named by its plain path (`C:\…`, which
/// Windows reads as the same file), names no alternate data stream and has a path shorter than
/// 260 characters.
#[cfg(windows)]
#[test]
fn an_opened_file_is_named_by_a_plain_short_local_path() {
    let (dir, mut c, action) = fixture();
    let within = dir.join("sessions");
    fs::create_dir(&within).unwrap();
    let file = within.join("notes.txt");
    fs::write(&file, "x").unwrap();
    let mut a = action.clone();
    opening(&mut a, &file, &within);
    assert!(c.prepare(a, Origin::NativeUi).is_ok());
    let resolved = fs::canonicalize(&within).unwrap();
    let plain = PathBuf::from(open_os::plain(&resolved).unwrap());
    // A stream of notes.txt named as a .txt file.
    let stream = PathBuf::from(format!("{}:x.txt", plain.join("notes.txt").display()));
    fs::write(&stream, "y").unwrap();
    let mut long = plain.clone();
    while long.as_os_str().len() < 260 {
        long = long.join("a".repeat(60));
    }
    fs::create_dir_all(&long).unwrap();
    fs::write(long.join("notes.txt"), "z").unwrap();
    // A folder name ending in a dot, which only `\\?\` names: without it, Windows drops the dot.
    let dotted = resolved.join("odd.");
    fs::create_dir(&dotted).unwrap();
    fs::write(dotted.join("notes.txt"), "w").unwrap();
    for (f, says) in [
        (stream, "must not name an alternate data stream"),
        (long.join("notes.txt"), "shorter than 260 characters"),
        (fs::canonicalize(&file).unwrap(), "by their resolved paths"),
        (dotted.join("notes.txt"), "Windows reads the same without"),
    ] {
        let mut b = action.clone();
        opening_named(&mut b, &f, &plain);
        let e = c
            .prepare(b, Origin::NativeUi)
            .err()
            .unwrap_or_else(|| panic!("{} was accepted", f.display()));
        assert!(e.contains(says), "{}: {e}", f.display());
    }
    fs::remove_file(dotted.join("notes.txt")).unwrap();
    fs::remove_dir(&dotted).unwrap();
    fs::remove_dir_all(&within).unwrap();
    remove(&dir);
}

/// macOS: the file a launch opens has no execute bit and none of the attributes that can choose
/// another app for it; another attribute is no matter.
#[cfg(target_os = "macos")]
#[test]
fn an_opened_file_has_no_execute_bit_or_app_choosing_attribute() {
    use std::os::unix::ffi::OsStrExt;
    let (dir, mut c, a, within, _, file) = nested_open();
    assert!(c.prepare(a.clone(), Origin::NativeUi).is_ok());
    let path = std::ffi::CString::new(file.as_os_str().as_bytes()).unwrap();
    let set = |name: &str, value: &[u8]| {
        let n = std::ffi::CString::new(name).unwrap();
        // SAFETY: path and n are NUL-terminated; value holds value.len() bytes.
        let r = unsafe {
            libc::setxattr(
                path.as_ptr(),
                n.as_ptr(),
                value.as_ptr().cast(),
                value.len(),
                0,
                0,
            )
        };
        assert_eq!(r, 0, "setxattr {name}: {}", std::io::Error::last_os_error());
    };
    let unset = |name: &str| {
        let n = std::ffi::CString::new(name).unwrap();
        // SAFETY: path and n are NUL-terminated.
        let r = unsafe { libc::removexattr(path.as_ptr(), n.as_ptr(), 0) };
        assert_eq!(
            r,
            0,
            "removexattr {name}: {}",
            std::io::Error::last_os_error()
        );
    };
    // Finder's information is 32 bytes, and all zeros reads as none: a type and a creator.
    let mut finder = [0u8; 32];
    finder[..8].copy_from_slice(b"TEXTttxt");
    for (name, value) in [
        ("com.apple.FinderInfo", &finder[..]),
        ("com.apple.ResourceFork", b"fork".as_slice()),
        ("com.apple.LaunchServices.OpenWith", b"bplist00".as_slice()),
    ] {
        set(name, value);
        let r = c.prepare(a.clone(), Origin::NativeUi);
        unset(name);
        let e = r.err().unwrap_or_else(|| panic!("{name} was accepted"));
        assert!(e.contains(&format!("carries {name}")), "{name}: {e}");
        assert!(
            c.prepare(a.clone(), Origin::NativeUi).is_ok(),
            "{name} removed"
        );
    }
    set("com.example.note", b"x");
    assert!(
        c.prepare(a.clone(), Origin::NativeUi).is_ok(),
        "another attribute"
    );
    unset("com.example.note");
    fs::set_permissions(&file, fs::Permissions::from_mode(0o744)).unwrap();
    let r = c.prepare(a.clone(), Origin::NativeUi);
    fs::set_permissions(&file, fs::Permissions::from_mode(0o644)).unwrap();
    let e = r.err().expect("an execute bit refused");
    assert!(e.contains("must not be executable"), "{e}");
    assert!(c.prepare(a, Origin::NativeUi).is_ok());
    remove(&within);
    remove(&dir);
}

/// Windows: only `pluxy/execution`'s own program opens a file, and only it starts with no
/// console in its `open` or `app` mode; another package's own program does neither.
#[cfg(windows)]
#[test]
fn only_the_execution_programs_own_program_opens_files_or_starts_windowless() {
    let (dir, mut c, a, within, _, _) = nested_open();
    assert!(c.prepare(a.clone(), Origin::NativeUi).unwrap().windowless);
    let mut other = a.clone();
    other.launch.as_mut().unwrap().terminal_adapter.id = "example/other".into();
    let e = c.prepare(other, Origin::NativeUi).err().expect("refused");
    assert!(
        e.contains("runs pluxy/execution's own program in its open mode")
            && e.contains("example/other"),
        "{e}"
    );
    // Its `app` mode, which opens no file.
    let mut app = a.clone();
    let l = app.launch.as_mut().unwrap();
    l.argv = vec!["app".into(), "--".into(), "example".into()];
    l.cwd = dir.clone();
    l.opens = None;
    assert!(c.prepare(app.clone(), Origin::NativeUi).unwrap().windowless);
    app.launch.as_mut().unwrap().terminal_adapter.id = "example/other".into();
    assert!(!c.prepare(app, Origin::NativeUi).unwrap().windowless);
    remove(&within);
    remove(&dir);
}

/// Windows and macOS: the within folder may not be a root, the home folder or a folder above it,
/// and is refused when the home folder cannot be resolved to check that.
#[cfg(not(target_os = "linux"))]
#[test]
fn the_within_folder_is_refused_when_the_home_folder_cannot_be_resolved() {
    let (dir, _, _) = fixture();
    let w = fs::canonicalize(&dir).unwrap();
    let home = w.parent().unwrap();
    assert!(open_os::root_or_home(&w, Some(home)).is_ok());
    for within in [home, home.parent().unwrap()] {
        let e = open_os::root_or_home(within, Some(home)).unwrap_err();
        assert!(e.contains("a folder above it"), "{}: {e}", within.display());
    }
    // A root, against a stand-in home on no root at all, so that only the root rule can refuse
    // it (every real home is below a root, which the home rule refuses too).
    let root = w.ancestors().last().unwrap();
    assert!(open_os::root_or_home(&w, Some(Path::new("stand-in-home"))).is_ok());
    let e = open_os::root_or_home(root, Some(Path::new("stand-in-home"))).unwrap_err();
    assert!(e.contains("cannot be a root"), "{}: {e}", root.display());
    let e = open_os::root_or_home(&w, None).unwrap_err();
    assert!(e.contains("home folder could not be resolved"), "{e}");
    remove(&dir);
}
