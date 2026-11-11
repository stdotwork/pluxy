use ed25519_dalek::{Signer, SigningKey};
use pluxy_app::{
    artifact,
    trust::{self, TrustStore},
};
use serde_json::json;
use sha2::{Digest, Sha256};
use std::{fs, io::Cursor, path::PathBuf};

// Isolate HOME in a child instead of mutating the parallel test runner environment.
fn isolated(test: &str) -> bool {
    if std::env::var("PLUXY_TRUST_TEST_CHILD").as_deref() == Ok(test) {
        return false;
    }
    let mut random = [0; 12];
    getrandom::fill(&mut random).unwrap();
    let home = std::env::temp_dir().join(format!("pluxy-signature-{}", artifact::hex(&random)));
    let status = std::process::Command::new(std::env::current_exe().unwrap())
        .args(["--exact", test, "--nocapture"])
        .env("HOME", &home)
        .env("PLUXY_TRUST_TEST_CHILD", test)
        .status()
        .unwrap();
    let _ = fs::remove_dir_all(home);
    assert!(status.success(), "isolated trust test failed");
    true
}

struct Fixture {
    dir: PathBuf,
    key: SigningKey,
    descriptor: Vec<u8>,
    archive: Vec<u8>,
}
impl Fixture {
    fn new() -> Self {
        let mut seed = [0; 32];
        getrandom::fill(&mut seed).unwrap();
        let dir = PathBuf::from(std::env::var_os("HOME").unwrap());
        fs::create_dir(&dir).unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(&dir, fs::Permissions::from_mode(0o700)).unwrap();
        }
        fs::write(dir.join("signing-key"), seed).unwrap();
        let key = SigningKey::from_bytes(&seed);
        let package = dir.join("package");
        fs::create_dir(&package).unwrap();
        fs::write(package.join("pluxy-extension.json"), br#"{"manifest_version":1,"id":"example/notes","version":"0.1.0","app":{"version":0,"tier":"native","host_versions":[0],"programs":{"linux/amd64":"tool"},"operations":[{"id":"inspect","description":"Inspect fixture","input_schema":{},"output_schema":{}}],"state":{"version":0,"readable_versions":[0]}}}"#).unwrap();
        fs::write(package.join("tool"), b"#!/bin/sh\nexit 0\n").unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(package.join("tool"), fs::Permissions::from_mode(0o755)).unwrap();
        }
        assert!(std::process::Command::new("tar")
            .arg("-czf")
            .arg(dir.join("package.tar.gz"))
            .arg("-C")
            .arg(&package)
            .args(["pluxy-extension.json", "tool"])
            .status()
            .unwrap()
            .success());
        let archive = fs::read(dir.join("package.tar.gz")).unwrap();
        let descriptor = serde_json::to_vec(&json!({"id":"example/notes", "version":"0.1.0", "tier":"native", "platform":"linux/amd64", "size":archive.len(), "sha256":artifact::hex(&Sha256::digest(&archive))})).unwrap();
        Self {
            dir,
            key,
            descriptor,
            archive,
        }
    }
    fn profile(&self) {
        let mut updater_seed = [0; 32];
        getrandom::fill(&mut updater_seed).unwrap();
        fs::write(self.dir.join("updater-signing-key"), updater_seed).unwrap();
        let updater_key = SigningKey::from_bytes(&updater_seed).verifying_key();
        let roots = json!({"roots":[{"kind":"test", "publisher":"example", "source":"https://example.invalid/index.json", "key_id":"fixture", "public_key":artifact::hex(self.key.verifying_key().as_bytes())}],"updater":{"kind":"test","public_key":artifact::hex(updater_key.as_bytes())}});
        fs::write(
            self.dir.join(trust::PROFILE_FILE),
            serde_json::to_vec(&roots).unwrap(),
        )
        .unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(
                self.dir.join(trust::PROFILE_FILE),
                fs::Permissions::from_mode(0o600),
            )
            .unwrap();
        }
        let _ = fs::remove_file(self.dir.join(trust::MARKER_FILE));
        trust::write_test_marker(&self.dir).unwrap();
    }
    fn signature(&self, descriptor: &[u8]) -> Vec<u8> {
        self.key.sign(descriptor).to_bytes().to_vec()
    }
    fn verify(
        &self,
        store: &TrustStore,
        descriptor: &[u8],
        signature: &[u8],
        archive: &[u8],
        source: &str,
        platform: &str,
    ) -> Result<artifact::VerifiedArchive, String> {
        artifact::verify_archive(
            store,
            source,
            "fixture",
            descriptor,
            signature,
            platform,
            Cursor::new(archive),
        )
    }
}
impl Drop for Fixture {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.dir).unwrap();
    }
}

#[test]
fn artifact_trust_feature_and_marker_are_both_required() {
    if isolated("artifact_trust_feature_and_marker_are_both_required") {
        return;
    }
    let f = Fixture::new();
    let empty = TrustStore::load(&f.dir).unwrap();
    assert!(empty
        .require_native()
        .unwrap_err()
        .contains("not configured"));
    assert!(empty.updater_key().unwrap_err().contains("not configured"));
    assert!(f
        .verify(
            &empty,
            &f.descriptor,
            &f.signature(&f.descriptor),
            &f.archive,
            "https://example.invalid/index.json",
            "linux/amd64"
        )
        .is_err());
    f.profile();
    let store = TrustStore::load(&f.dir).unwrap();
    if cfg!(feature = "artifact-test-trust") {
        store.require_native().unwrap();
        assert!(!store.updater_key().unwrap().is_empty());
        f.verify(
            &store,
            &f.descriptor,
            &f.signature(&f.descriptor),
            &f.archive,
            "https://example.invalid/index.json",
            "linux/amd64",
        )
        .unwrap();
    } else {
        assert!(store
            .require_native()
            .unwrap_err()
            .contains("not configured"));
        assert!(store.updater_key().unwrap_err().contains("not configured"));
        assert!(f
            .verify(
                &store,
                &f.descriptor,
                &f.signature(&f.descriptor),
                &f.archive,
                "https://example.invalid/index.json",
                "linux/amd64"
            )
            .is_err());
    }
    fs::remove_file(f.dir.join(trust::MARKER_FILE)).unwrap();
    let without_marker = TrustStore::load(&f.dir).unwrap();
    assert!(without_marker.require_native().is_err());
    assert!(without_marker.updater_key().is_err());
}

#[cfg(feature = "artifact-test-trust")]
#[test]
fn artifact_trust_signed_package_and_denial_controls() {
    if isolated("artifact_trust_signed_package_and_denial_controls") {
        return;
    }
    let f = Fixture::new();
    f.profile();
    let store = TrustStore::load(&f.dir).unwrap();
    let source = "https://example.invalid/index.json";
    let verify = |d: &[u8], s: &[u8], a: &[u8], src: &str, platform: &str| {
        f.verify(&store, d, s, a, src, platform)
    };
    let signature = f.signature(&f.descriptor);
    let positive = || verify(&f.descriptor, &signature, &f.archive, source, "linux/amd64").unwrap();
    let valid = positive();
    assert_eq!(valid.descriptor_bytes(), f.descriptor);
    assert_eq!(valid.signature_bytes(), signature);
    // Read the manifest from the verified fixture archive only after verification succeeded.
    let extracted = std::process::Command::new("tar")
        .arg("-xOf")
        .arg(f.dir.join("package.tar.gz"))
        .arg("pluxy-extension.json")
        .output()
        .unwrap();
    assert!(extracted.status.success());
    let manifest = extracted.stdout;
    valid.check_manifest(&manifest).unwrap();
    let mut changed = f.descriptor.clone();
    changed.push(b' ');
    assert!(verify(&changed, &signature, &f.archive, source, "linux/amd64").is_err());
    positive();
    let mut changed = f.archive.clone();
    changed[0] ^= 1;
    assert!(verify(&f.descriptor, &signature, &changed, source, "linux/amd64").is_err());
    positive();
    let mut value: serde_json::Value = serde_json::from_slice(&f.descriptor).unwrap();
    value["size"] = json!(f.archive.len() + 1);
    let wrong_size = serde_json::to_vec(&value).unwrap();
    assert!(verify(
        &wrong_size,
        &f.signature(&wrong_size),
        &f.archive,
        source,
        "linux/amd64"
    )
    .is_err());
    positive();
    assert!(verify(&f.descriptor, &signature, &f.archive, source, "linux/arm64").is_err());
    positive();
    assert!(verify(
        &f.descriptor,
        &signature,
        &f.archive,
        "https://other.invalid/index.json",
        "linux/amd64"
    )
    .is_err());
    positive();
    for (field, value) in [
        ("id", json!("example/other")),
        ("version", json!("0.2.0")),
        ("tier", json!("web")),
    ] {
        let mut wrong: serde_json::Value = serde_json::from_slice(&manifest).unwrap();
        if field == "tier" {
            wrong["app"][field] = value;
        } else {
            wrong[field] = value;
        }
        assert!(valid
            .check_manifest(&serde_json::to_vec(&wrong).unwrap())
            .is_err());
        valid.check_manifest(&manifest).unwrap();
    }
    let mut seed = [0; 32];
    getrandom::fill(&mut seed).unwrap();
    let unknown = SigningKey::from_bytes(&seed).sign(&f.descriptor).to_bytes();
    assert!(verify(&f.descriptor, &unknown, &f.archive, source, "linux/amd64").is_err());
    positive();
    assert!(artifact::verify_archive(
        &store,
        source,
        "unknown",
        &f.descriptor,
        &signature,
        "linux/amd64",
        Cursor::new(&f.archive)
    )
    .is_err());
    positive();
    let mut noncanonical = signature.clone();
    noncanonical[63] |= 0x80;
    assert!(verify(
        &f.descriptor,
        &noncanonical,
        &f.archive,
        source,
        "linux/amd64"
    )
    .is_err());
    positive();
    let mut spaced = f.descriptor.clone();
    spaced.push(10);
    assert_eq!(
        verify(
            &spaced,
            &f.signature(&spaced),
            &f.archive,
            source,
            "linux/amd64"
        )
        .unwrap()
        .descriptor_bytes(),
        spaced
    );
    for malformed in [
        b"not JSON".to_vec(),
        vec![0xff],
        f.descriptor
            .iter()
            .copied()
            .chain(b" trailing".iter().copied())
            .collect(),
    ] {
        assert!(verify(
            &malformed,
            &f.signature(&malformed),
            &f.archive,
            source,
            "linux/amd64"
        )
        .is_err());
        positive();
    }
    for (field, value) in [
        ("id", json!("../notes")),
        ("version", json!("../0.1")),
        ("tier", json!("unknown")),
        ("sha256", json!("AA".repeat(32))),
    ] {
        let mut malformed: serde_json::Value = serde_json::from_slice(&f.descriptor).unwrap();
        malformed[field] = value;
        let malformed = serde_json::to_vec(&malformed).unwrap();
        assert!(verify(
            &malformed,
            &f.signature(&malformed),
            &f.archive,
            source,
            "linux/amd64"
        )
        .is_err());
        positive();
    }
}

#[cfg(feature = "artifact-test-trust")]
#[test]
fn artifact_trust_marker_mismatch_and_weak_key_refuse() {
    if isolated("artifact_trust_marker_mismatch_and_weak_key_refuse") {
        return;
    }
    let f = Fixture::new();
    f.profile();
    TrustStore::load(&f.dir).unwrap().require_native().unwrap();
    fs::write(
        f.dir.join(trust::MARKER_FILE),
        br#"{"format":1,"home":"/other","purpose":"pluxy-artifact-test-trust"}"#,
    )
    .unwrap();
    assert!(TrustStore::load(&f.dir).is_err());
    f.profile();
    TrustStore::load(&f.dir).unwrap().require_native().unwrap();
    let path = f.dir.join(trust::PROFILE_FILE);
    let mut roots: serde_json::Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
    let mut identity = [0u8; 32];
    identity[0] = 1;
    roots["roots"][0]["public_key"] = json!(artifact::hex(&identity));
    fs::write(path, serde_json::to_vec(&roots).unwrap()).unwrap();
    assert!(TrustStore::load(&f.dir).is_err());
    f.profile();
    TrustStore::load(&f.dir).unwrap().require_native().unwrap();
}
