use super::*;
use std::os::unix::fs::PermissionsExt;

fn fixture() -> (PathBuf, Confirmations, Action) {
    fixture_in(&std::env::temp_dir())
}
fn fixture_in(root: &Path) -> (PathBuf, Confirmations, Action) {
    let dir = root.join(format!("pluxy-confirm-{}", crate::managed::random_key()));
    fs::create_dir(&dir).unwrap();
    let exe = dir.join("tool");
    fs::write(&exe, b"#!/bin/sh\nexit 0\n").unwrap();
    fs::set_permissions(&exe, fs::Permissions::from_mode(0o700)).unwrap();
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
            argv: vec!["tool".into(), "--flag".into()],
            cwd: dir.clone(),
            terminal_adapter: package,
            environment_revision: "1".into(),
            environment_review: vec![],
            secret_uses: vec![],
        }),
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
    c.pending.get_mut(&id).unwrap().expires = Instant::now();
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
    fs::remove_dir_all(dir).unwrap();
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
                    created: Instant::now()
                }
            )
            .is_err());
    }
    let exe = &a.launch.as_ref().unwrap().executable;
    fs::set_permissions(exe, fs::Permissions::from_mode(0o500)).unwrap();
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    assert_eq!(c.hashes, 2);
    assert!(c
        .remembered(
            p,
            Gesture {
                created: Instant::now()
            }
        )
        .is_err());
    fs::rename(exe, dir.join("old")).unwrap();
    fs::write(exe, b"replacement").unwrap();
    fs::set_permissions(exe, fs::Permissions::from_mode(0o700)).unwrap();
    let p = c.prepare(a.clone(), Origin::NativeUi).unwrap();
    assert_eq!(c.hashes, 3);
    assert!(c
        .remembered(
            p,
            Gesture {
                created: Instant::now()
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
                created: Instant::now()
            }
        )
        .is_ok());
    assert_eq!(
        fs::metadata(dir.join("settings.json")).unwrap().mode() & 0o777,
        0o600
    );
    fs::remove_dir_all(dir).unwrap();
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
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn corrupt_settings_start_empty_and_preserve_the_bad_file() {
    let (dir, c, _) = fixture();
    let settings = c.settings.clone();
    fs::write(&settings, b"{broken JSON").unwrap();
    fs::set_permissions(&settings, fs::Permissions::from_mode(0o600)).unwrap();
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
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn non_utf8_paths_are_rejected_before_review() {
    use std::os::unix::ffi::OsStringExt;
    let (dir, mut c, mut a) = fixture();
    let file = dir.join(std::ffi::OsString::from_vec(vec![b'f', 0xff]));
    fs::write(&file, b"fixture").unwrap();
    a.files.push(file);
    assert!(
        c.prepare(a, Origin::NativeUi).is_err(),
        "non-UTF-8 paths must be refused before review"
    );
    fs::remove_dir_all(dir).unwrap();
}

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
        fs::set_permissions(&c.settings, fs::Permissions::from_mode(0o600)).unwrap();
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
    let file = File::open(dir.join("tool")).unwrap();
    assert!(private_file(&file).is_ok());
    if unsafe { libc::geteuid() } == 0 {
        use std::os::fd::AsRawFd;
        assert_eq!(unsafe { libc::fchown(file.as_raw_fd(), 65534, !0) }, 0);
        assert!(private_file(&file).is_err());
    }
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn settings_create_private_parent() {
    let (dir, _, a) = fixture();
    let parent = dir.join("private");
    let mut c = Confirmations::open(parent.join("settings.json")).unwrap();
    approve(&mut c, a, true);
    assert_eq!(fs::metadata(parent).unwrap().mode() & 0o777, 0o700);
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn review_escapes_unicode_format_characters_and_rejects_non_utf8_resolutions() {
    use std::os::unix::{ffi::OsStringExt, fs::symlink};
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
    let review = p.review().unwrap();
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
    let invalid = dir.join(std::ffi::OsString::from_vec(vec![b'd', 0xff]));
    fs::create_dir(&invalid).unwrap();
    let link = dir.join("utf8-link");
    symlink(&invalid, &link).unwrap();
    a.launch.as_mut().unwrap().cwd = link;
    assert!(c.prepare(a, Origin::NativeUi).is_err());
    fs::remove_dir_all(dir).unwrap();
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
                    created: Instant::now()
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
                created: Instant::now() - Duration::from_secs(5)
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
                },
            )
            .ok()
            .unwrap();
        let id = c.token_action(&token).unwrap().to_string();
        c.consume(&id, &token).unwrap();
    }
    fs::remove_dir_all(dir).unwrap();
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
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn self_update_supersedes_old_recipe_approval_and_outstanding_tokens() {
    let (dir, mut c, a) = fixture();
    let (_, old_token) = approve(&mut c, a.clone(), true);
    let old_approval = c.approvals[0].id.clone();
    let exe = &a.launch.as_ref().unwrap().executable;
    fs::rename(exe, dir.join("old-tool")).unwrap();
    fs::write(exe, b"#!/bin/sh\nexit 1\n").unwrap();
    fs::set_permissions(exe, fs::Permissions::from_mode(0o700)).unwrap();
    approve(&mut c, a, true);
    assert_eq!(c.approvals.len(), 1);
    assert_ne!(c.approvals[0].id, old_approval);
    assert!(!c.issued.contains_key(&old_token));
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn opened_script_fexecve_requires_clearing_cloexec_and_uses_dev_fd_path() {
    use std::os::fd::AsRawFd;
    let target = std::env::var_os("CARGO_TARGET_TMPDIR")
        .map(PathBuf::from)
        .or_else(|| std::env::var_os("CARGO_TARGET_DIR").map(PathBuf::from))
        .unwrap_or_else(|| std::env::current_exe().unwrap().parent().unwrap().into());
    fs::create_dir_all(&target).unwrap();
    let mut executable_root = None;
    for root in [target, std::env::temp_dir()] {
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
    let Some(root) = executable_root else {
        eprintln!("SKIP fexecve fixture: target temporary directory and system temporary directory are mounted noexec");
        return;
    };
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
    fs::remove_dir_all(dir).unwrap();
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
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn review_escapes_controls_and_pango_line_separators() {
    let (dir, mut c, mut a) = fixture();
    let chars = [
        '\u{7f}', '\u{80}', '\u{85}', '\u{9f}', '\u{2028}', '\u{2029}',
    ];
    a.what.extend(chars);
    let p = c.prepare(a, Origin::NativeUi).unwrap();
    let review = p.review().unwrap();
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
    fs::remove_dir_all(dir).unwrap();
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
    fs::set_permissions(&c.settings, fs::Permissions::from_mode(0o600)).unwrap();
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
    fs::remove_dir_all(dir).unwrap();
}

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
    fs::remove_dir_all(dir).unwrap();
}
