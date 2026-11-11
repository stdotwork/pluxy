use super::*;
use std::os::unix::fs::PermissionsExt;

fn fixture() -> (PathBuf, Confirmations, Action) {
    let dir = std::env::temp_dir().join(format!("pluxy-confirm-{}", crate::managed::random_key()));
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
    let p = c.prepare(a).unwrap();
    let id = c.queue(p, false).unwrap();
    let t = c.finish(&id, true, remember).unwrap().unwrap();
    (id, t)
}
#[test]
fn tokens_are_exact_single_use_cancelled_expired_and_revocable() {
    let (dir, mut c, a) = fixture();
    let p = c.prepare(a.clone()).unwrap();
    let id = c.queue(p, false).unwrap();
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
    let p = c.prepare(a.clone()).unwrap();
    let id = c.queue(p, false).unwrap();
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
    let p = c.prepare(a.clone()).unwrap();
    assert_eq!(c.hashes, 1);
    let t = c.remembered(p, Gesture::native_event()).ok().unwrap();
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
        let p = c.prepare(changed).unwrap();
        assert!(c.remembered(p, Gesture::native_event()).is_err());
    }
    let exe = &a.launch.as_ref().unwrap().executable;
    fs::set_permissions(exe, fs::Permissions::from_mode(0o500)).unwrap();
    let p = c.prepare(a.clone()).unwrap();
    assert_eq!(c.hashes, 2);
    assert!(c.remembered(p, Gesture::native_event()).is_err());
    fs::rename(exe, dir.join("old")).unwrap();
    fs::write(exe, b"replacement").unwrap();
    fs::set_permissions(exe, fs::Permissions::from_mode(0o700)).unwrap();
    let p = c.prepare(a.clone()).unwrap();
    assert_eq!(c.hashes, 3);
    assert!(c.remembered(p, Gesture::native_event()).is_err());
    // Persistence is real settings, with a private file; unchanged metadata still hashes once
    // in a new process before using persisted approval.
    let (id, t) = approve(&mut c, a.clone(), true);
    c.consume(&id, &t).unwrap();
    let mut loaded = Confirmations::open(dir.join("settings.json")).unwrap();
    let p = loaded.prepare(a).unwrap();
    assert!(loaded.remembered(p, Gesture::native_event()).is_ok());
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
    let p = c.prepare(a.clone()).unwrap();
    let id = c.queue(p, true).unwrap();
    assert!(c.issued.is_empty());
    for _ in 0..10 {
        let p = c.prepare(a.clone()).unwrap();
        assert!(c.queue(p, true).is_err());
    }
    assert_eq!(c.pending.len(), 1);
    c.finish(&id, false, false).unwrap();
    let p = c.prepare(a.clone()).unwrap();
    assert!(c.queue(p, true).is_err());
    c.last_page
        .values_mut()
        .for_each(|t| *t = Instant::now() - Duration::from_secs(2));
    let p = c.prepare(a).unwrap();
    assert!(c.queue(p, true).is_ok());
    fs::remove_dir_all(dir).unwrap();
}
