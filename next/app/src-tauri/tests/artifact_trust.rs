#![cfg(debug_assertions)]
use ed25519_dalek::{Signer, SigningKey};
use pluxy_app::{
    artifact,
    trust::{self, TrustStore},
};
use serde_json::json;

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
        let descriptor = serde_json::to_vec(&json!({"id":"example/notes", "version":"0.1.0", "tier":"native", "platform":"linux/amd64", "size":archive.len(), "sha256":String::from_utf8(std::process::Command::new("sha256sum").arg(dir.join("package.tar.gz")).output().unwrap().stdout).unwrap().split_whitespace().next().unwrap()})).unwrap();
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
        let nonce = trust::write_test_marker(&self.dir).unwrap();
        std::env::set_var("PLUXY_TEST_TRUST_NONCE", nonce);
    }
    fn signature(&self, descriptor: &[u8]) -> Vec<u8> {
        self.key.sign(descriptor).to_bytes().to_vec()
    }
    fn verify(
        &self,
        store: &impl artifact::PublisherKeys,
        descriptor: &[u8],
        signature: &[u8],
        archive: &[u8],
        source: &str,
        platform: &str,
    ) -> Result<artifact::VerifiedArchive<Cursor<Vec<u8>>>, String> {
        artifact::verify_trusted_archive(
            store,
            source,
            "fixture",
            descriptor,
            signature,
            platform,
            Cursor::new(archive.to_vec()),
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
        .err()
        .unwrap()
        .contains("native-tier trust not configured"));
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
            .err()
            .unwrap()
            .contains("native-tier trust not configured"));
    }
    fs::remove_file(f.dir.join(trust::MARKER_FILE)).unwrap();
    let without_marker = TrustStore::load(&f.dir).unwrap();
    assert!(without_marker
        .require_native()
        .err()
        .unwrap()
        .contains("native-tier trust not configured"));
    assert!(without_marker
        .updater_key()
        .err()
        .unwrap()
        .contains("updater trust not configured"));
}

#[test]
fn artifact_trust_signed_package_and_denial_controls() {
    if isolated("artifact_trust_signed_package_and_denial_controls") {
        return;
    }
    let f = Fixture::new();
    let store = Approved(f.key.verifying_key());
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
    // Consume the retained, rewound handle; no pathname reopen is needed for extraction.
    let retained = positive().into_archive();
    assert_eq!(retained.position(), 0);
    assert_eq!(retained.into_inner(), f.archive.as_slice());
    valid.check_manifest(&manifest).unwrap();
    let mut changed = f.descriptor.clone();
    changed.push(b' ');
    assert!(
        verify(&changed, &signature, &f.archive, source, "linux/amd64")
            .err()
            .unwrap()
            .contains("invalid descriptor signature")
    );
    positive();
    let mut changed = f.archive.clone();
    changed[0] ^= 1;
    assert!(
        verify(&f.descriptor, &signature, &changed, source, "linux/amd64")
            .err()
            .unwrap()
            .contains("archive SHA-256 differs from signed descriptor")
    );
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
    .err()
    .unwrap()
    .contains("archive size differs from signed descriptor"));
    positive();
    assert!(
        verify(&f.descriptor, &signature, &f.archive, source, "linux/arm64")
            .err()
            .unwrap()
            .contains("descriptor platform does not match selected artifact")
    );
    positive();
    assert!(verify(
        &f.descriptor,
        &signature,
        &f.archive,
        "https://other.invalid/index.json",
        "linux/amd64"
    )
    .err()
    .unwrap()
    .contains("unknown publisher key or changed source"));
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
            .err()
            .unwrap()
            .contains("manifest identity differs from signed descriptor"));
        valid.check_manifest(&manifest).unwrap();
    }
    let mut seed = [0; 32];
    getrandom::fill(&mut seed).unwrap();
    let unknown = SigningKey::from_bytes(&seed).sign(&f.descriptor).to_bytes();
    assert!(
        verify(&f.descriptor, &unknown, &f.archive, source, "linux/amd64")
            .err()
            .unwrap()
            .contains("invalid descriptor signature")
    );
    positive();
    assert!(artifact::verify_trusted_archive(
        &store,
        source,
        "unknown",
        &f.descriptor,
        &signature,
        "linux/amd64",
        Cursor::new(&f.archive)
    )
    .err()
    .unwrap()
    .contains("unknown publisher key or changed source"));
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
    .err()
    .unwrap()
    .contains("invalid descriptor signature"));
    positive();
    // Add the group order L to a valid S. Its top bits remain clear; bit masking alone fails.
    let mut plus_l = signature.clone();
    let order = artifact::decode_hex::<32>(
        "edd3f55c1a631258d69cf7a2def9de1400000000000000000000000000000010",
    )
    .unwrap();
    let mut carry = 0u16;
    for i in 0..32 {
        let sum = plus_l[32 + i] as u16 + order[i] as u16 + carry;
        plus_l[32 + i] = sum as u8;
        carry = sum >> 8;
    }
    assert_eq!(plus_l[63] & 0xe0, 0);
    assert!(
        verify(&f.descriptor, &plus_l, &f.archive, source, "linux/amd64")
            .unwrap_err()
            .contains("signature")
    );
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
        .err()
        .unwrap()
        .contains("invalid UTF-8 descriptor JSON"));
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
        .err()
        .unwrap()
        .contains(match field {
            "id" => "unknown publisher key or changed source",
            "tier" => "invalid UTF-8 descriptor JSON",
            _ => "invalid descriptor identity, platform or SHA-256",
        }));
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
    let nonce = std::env::var("PLUXY_TEST_TRUST_NONCE").unwrap();
    std::env::set_var("TMPDIR", f.dir.parent().unwrap());
    std::env::remove_var("PLUXY_TEST_TRUST_NONCE");
    assert!(TrustStore::load(&f.dir)
        .err()
        .unwrap()
        .contains("marker mismatch"));
    std::env::set_var("PLUXY_TEST_TRUST_NONCE", "wrong");
    assert!(TrustStore::load(&f.dir)
        .err()
        .unwrap()
        .contains("marker mismatch"));
    std::env::set_var("PLUXY_TEST_TRUST_NONCE", nonce);
    TrustStore::load(&f.dir).unwrap().require_native().unwrap();
    fs::write(
        f.dir.join(trust::MARKER_FILE),
        br#"{"format":1,"home":"/other","purpose":"pluxy-artifact-test-trust","nonce":"invalid"}"#,
    )
    .unwrap();
    assert!(TrustStore::load(&f.dir)
        .err()
        .unwrap()
        .contains("test trust profile marker mismatch"));
    f.profile();
    TrustStore::load(&f.dir).unwrap().require_native().unwrap();
    let path = f.dir.join(trust::PROFILE_FILE);
    let mut roots: serde_json::Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
    let mut identity = [0u8; 32];
    identity[0] = 1;
    roots["roots"][0]["public_key"] = json!(artifact::hex(&identity));
    fs::write(path, serde_json::to_vec(&roots).unwrap()).unwrap();
    assert!(TrustStore::load(&f.dir)
        .err()
        .unwrap()
        .contains("weak publisher public key"));
    f.profile();
    TrustStore::load(&f.dir).unwrap().require_native().unwrap();
}

// Native settings/curated lookup is separate from signature verification. This implementation
// holds only a runtime-generated approved public key; it performs the real source/ID checks.
struct Approved(ed25519_dalek::VerifyingKey);
impl artifact::PublisherKeys for Approved {
    fn publisher_key(
        &self,
        source: &str,
        key_id: &str,
        publisher: &str,
    ) -> Result<ed25519_dalek::VerifyingKey, String> {
        if source != "https://example.invalid/index.json"
            || key_id != "fixture"
            || publisher != "example"
        {
            return Err("unknown publisher key or changed source".into());
        }
        Ok(self.0)
    }
}

#[test]
fn independent_archive_digest() {
    let mut seed = [0; 32];
    getrandom::fill(&mut seed).unwrap();
    let key = SigningKey::from_bytes(&seed);
    // NIST/FIPS SHA-256 'abc' known answer: size 3, hash fixed independently of sha2.
    let bytes = br#"{"id":"example/notes","version":"1","tier":"web","platform":"linux/amd64","size":3,"sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}"#;
    let signature = key.sign(bytes).to_bytes();
    let valid = artifact::verify_archive(
        &key.verifying_key(),
        bytes,
        &signature,
        "linux/amd64",
        Cursor::new(b"abc"),
    )
    .unwrap();
    assert_eq!(valid.into_archive().into_inner(), b"abc");
    assert!(artifact::verify_archive(
        &key.verifying_key(),
        bytes,
        &signature,
        "linux/amd64",
        Cursor::new(b"abd")
    )
    .unwrap_err()
    .contains("SHA-256"));
}

#[test]
fn rfc8032_public_known_answers() {
    // RFC 8032 section 7.1 tests 1 and 2, public inputs only.
    for (public, message, signature) in [
        ("d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", "", "e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b"),
        ("3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c", "72", "92a009a9f0d4cab8720e820b5f642540a2b27b5416503f8fb3762223ebdb69da085ac1e43e15996e458f3613d0f11d8c387b2eaeb4302aeeb00d291612bb0c00")
    ] {
        let key = ed25519_dalek::VerifyingKey::from_bytes(&artifact::decode_hex::<32>(public).unwrap()).unwrap();
        let signature = artifact::decode_hex::<64>(signature).unwrap();
        let message: Vec<u8> = message.as_bytes().chunks(2).map(|c| u8::from_str_radix(std::str::from_utf8(c).unwrap(),16).unwrap()).collect();
        artifact::verify_signature(&key, &message, &signature).unwrap();
        let mut bad = signature; bad[0] ^= 1;
        assert!(artifact::verify_signature(&key, &message, &bad).unwrap_err().contains("signature"));
        artifact::verify_signature(&key, &message, &signature).unwrap();
    }
}

#[test]
fn strict_signature_rejects_small_order_forgery() {
    use ed25519_dalek::{Signature, Verifier, VerifyingKey};
    // A = identity (small order), R = identity, S = 0. Lax verification accepts this
    // forged signature for every message; strict verification must reject it directly.
    let mut identity = [0u8; 32];
    identity[0] = 1;
    let key = VerifyingKey::from_bytes(&identity).unwrap();
    let mut forged = [0u8; 64];
    forged[0] = 1;
    let message = b"small-order forgery control";
    key.verify(message, &Signature::from_bytes(&forged))
        .unwrap();
    assert_eq!(
        artifact::verify_signature(&key, message, &forged).unwrap_err(),
        "invalid descriptor signature"
    );
    let mut seed = [0; 32];
    getrandom::fill(&mut seed).unwrap();
    let legitimate = SigningKey::from_bytes(&seed);
    artifact::verify_signature(
        &legitimate.verifying_key(),
        message,
        &legitimate.sign(message).to_bytes(),
    )
    .unwrap();
}

#[cfg(feature = "artifact-test-trust")]
#[test]
fn real_trust_store_lookup_controls() {
    use artifact::PublisherKeys;
    if isolated("real_trust_store_lookup_controls") {
        return;
    }
    let f = Fixture::new();
    f.profile();
    let source = "https://example.invalid/index.json";
    let positive = |store: &TrustStore| {
        assert_eq!(
            store.publisher_key(source, "fixture", "example").unwrap(),
            f.key.verifying_key()
        );
        f.verify(
            store,
            &f.descriptor,
            &f.signature(&f.descriptor),
            &f.archive,
            source,
            "linux/amd64",
        )
        .unwrap();
    };
    let store = TrustStore::load(&f.dir).unwrap();
    positive(&store);
    for (src, key, publisher) in [
        ("https://other.invalid/index.json", "fixture", "example"),
        (source, "unknown", "example"),
        (source, "fixture", "other"),
    ] {
        assert_eq!(
            store.publisher_key(src, key, publisher).unwrap_err(),
            "unknown publisher key or changed source"
        );
        positive(&store);
    }
    let path = f.dir.join(trust::PROFILE_FILE);
    let mut roots: serde_json::Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
    let duplicate = roots["roots"][0].clone();
    roots["roots"].as_array_mut().unwrap().push(duplicate);
    fs::write(path, serde_json::to_vec(&roots).unwrap()).unwrap();
    let ambiguous = TrustStore::load(&f.dir).unwrap();
    assert_eq!(
        ambiguous
            .publisher_key(source, "fixture", "example")
            .unwrap_err(),
        "ambiguous publisher key"
    );
    assert_eq!(
        f.verify(
            &ambiguous,
            &f.descriptor,
            &f.signature(&f.descriptor),
            &f.archive,
            source,
            "linux/amd64"
        )
        .unwrap_err(),
        "ambiguous publisher key"
    );
    f.profile();
    positive(&TrustStore::load(&f.dir).unwrap());
}

// The updater gate (trust::updater_gate) with a real isolated profile: a test build passes only
// as the test package (test identifier, product name and the profile's key compiled in); a
// normal build refuses whatever its config says.
#[test]
fn updater_gate_passes_only_the_test_package() {
    if isolated("updater_gate_passes_only_the_test_package") {
        return;
    }
    let f = Fixture::new();
    let config = |identifier: &str, product: &str, pubkey: &str| -> tauri::Config {
        serde_json::from_value(json!({
            "identifier": identifier,
            "productName": product,
            "plugins": {"updater": {"pubkey": pubkey}},
        }))
        .unwrap()
    };
    let test_id = "dev.pluxy.desktop.test";
    let test_product = "pluxy Test";
    // Without a profile, nothing passes.
    let error = trust::updater_gate(&config(test_id, test_product, "k"), &f.dir).unwrap_err();
    assert!(error.contains("updater trust not configured"), "{error}");
    f.profile();
    let profile: serde_json::Value =
        serde_json::from_slice(&fs::read(f.dir.join(trust::PROFILE_FILE)).unwrap()).unwrap();
    let key = profile["updater"]["public_key"]
        .as_str()
        .unwrap()
        .to_string();
    let package = config(test_id, test_product, &key);
    if !cfg!(feature = "artifact-test-trust") {
        for c in [&package, &config("dev.pluxy.desktop", "pluxy", &key)] {
            let error = trust::updater_gate(c, &f.dir).unwrap_err();
            assert!(error.contains("updater trust not configured"), "{error}");
        }
        return;
    }
    assert_eq!(trust::updater_gate(&package, &f.dir).unwrap(), key);
    for (c, reason) in [
        (
            config("dev.pluxy.desktop", test_product, &key),
            "identifier",
        ),
        (config(test_id, "pluxy", &key), "product name"),
        (
            config(test_id, test_product, "another key"),
            "not the one compiled",
        ),
    ] {
        let error = trust::updater_gate(&c, &f.dir).unwrap_err();
        assert!(error.contains(reason), "expected {reason}, got {error}");
        // The positive control, on the same profile.
        assert_eq!(trust::updater_gate(&package, &f.dir).unwrap(), key);
    }
    fs::remove_file(f.dir.join(trust::MARKER_FILE)).unwrap();
    let error = trust::updater_gate(&package, &f.dir).unwrap_err();
    assert!(error.contains("updater trust not configured"), "{error}");
}
