use super::*;
#[cfg(unix)]
use std::os::unix::fs::PermissionsExt;

/// The fixture's program: a shell script, or on Windows a command script, which its extension
/// makes a program.
const TOOL: &str = if cfg!(windows) { "tool.cmd" } else { "tool" };
#[cfg(unix)]
const EXIT_0: &[u8] = b"#!/bin/sh\nexit 0\n";
#[cfg(unix)]
const EXIT_1: &[u8] = b"#!/bin/sh\nexit 1\n";
#[cfg(windows)]
const EXIT_0: &[u8] = b"@exit /b 0\r\n";
#[cfg(windows)]
const EXIT_1: &[u8] = b"@exit /b 1\r\n";

/// Writes a program at path: executable by its owner (on Windows its extension makes it one).
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
    program(exe, b"replacement");
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
        p.review().unwrap(),
        "Rotate the management key of http://127.0.0.1:8400.\nSecond line\\u2028"
    );
    let id = c.queue(p).unwrap();
    assert!(!c.review(&id).unwrap().1, "an app action offered Remember");
    let t = c.finish(&id, true, false).unwrap().unwrap();
    assert!(c.consume(&id, &t).is_ok());
    // A launch is still reviewed in full.
    let review = c.prepare(a, Origin::NativeUi).unwrap().review().unwrap();
    assert!(review.contains("\"digest\"") && review.contains("\"argv\""));
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

/// Off Linux a launch that opens a file is refused before review: nothing yet checks the file
/// and folder there.
#[cfg(not(target_os = "linux"))]
#[test]
fn a_launch_that_opens_a_file_is_refused_off_linux() {
    let (dir, mut c, mut a) = fixture();
    let file = dir.join("file.txt");
    fs::write(&file, b"x").unwrap();
    a.launch.as_mut().unwrap().opens = Some((file.clone(), dir.clone()));
    let e = c.prepare(a, Origin::NativeUi).err().unwrap();
    assert!(e.contains("Linux only"), "{e}");
    remove(&dir);
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
    let review = p.review().unwrap();
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
