//! Signed test core releases, made at run time: real cores built with Go (with the version they
#![allow(dead_code)] // each test program uses part of this
//! report set at link time), packed as the app installs them, described and signed with a key
//! generated for the test. The private key stays in memory.

use ed25519_dalek::{Signer, SigningKey, VerifyingKey};
use pluxy_app::artifact::{hex, PublisherKeys};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

pub fn next_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../..")
}

/// The test's publisher key, trusted for one source, as the app's trust store would trust a
/// root bound to that source; nothing else is trusted.
pub struct Keys {
    pub key: SigningKey,
    pub key_id: String,
    pub source: String,
}

impl Keys {
    pub fn new(source: &Path) -> Keys {
        let mut seed = [0u8; 32];
        getrandom::fill(&mut seed).unwrap();
        Keys {
            key: SigningKey::from_bytes(&seed),
            key_id: format!("test-{}", hex(&seed[..4])),
            source: source.display().to_string(),
        }
    }
}

impl PublisherKeys for Keys {
    fn publisher_key(
        &self,
        source: &str,
        key_id: &str,
        publisher: &str,
    ) -> Result<VerifyingKey, String> {
        if source == self.source && key_id == self.key_id && publisher == "pluxy" {
            Ok(self.key.verifying_key())
        } else {
            Err("unknown publisher key or changed source".into())
        }
    }
}

/// Builds core into dir/pluxy, reporting version.
pub fn build_core(dir: &Path, version: &str) {
    std::fs::create_dir_all(dir).unwrap();
    let ok = Command::new("go")
        .args(["build", "-buildvcs=false", "-ldflags"])
        .arg(format!(
            "-X github.com/stdotwork/pluxy/next/internal/buildinfo.Version={version}"
        ))
        .arg("-o")
        .arg(dir.join("pluxy"))
        .arg("./cmd/pluxy")
        .current_dir(next_dir())
        .stderr(Stdio::inherit())
        .status()
        .unwrap()
        .success();
    assert!(ok, "building core {version}");
}

/// A shell script standing in for a core that breaks its config (`-config <path>`) and exits.
pub fn bad_core(dir: &Path) {
    use std::os::unix::fs::PermissionsExt;
    std::fs::create_dir_all(dir).unwrap();
    let p = dir.join("pluxy");
    std::fs::write(
        &p,
        "#!/bin/sh\nset -e\nprintf 'not json' > \"$2\"\necho 'pluxy: bad candidate broke its config' >&2\nexit 3\n",
    )
    .unwrap();
    std::fs::set_permissions(&p, std::fs::Permissions::from_mode(0o755)).unwrap();
}

/// folder as a gzip tar archive, entries named `./...` as `tar -czf a.tar.gz -C folder .` does.
pub fn pack(folder: &Path) -> Vec<u8> {
    let gz = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::fast());
    let mut b = tar::Builder::new(gz);
    b.append_dir_all(".", folder).unwrap();
    b.into_inner().unwrap().finish().unwrap()
}

pub fn platform() -> String {
    pluxy_app::cores::platform()
}

/// Writes archive beside the index in dir and returns its release entry, signed by keys
/// (or with a broken signature).
pub fn release(dir: &Path, keys: &Keys, version: &str, archive: &[u8], good: bool) -> Value {
    let name = format!("pluxy-core-{version}.tar.gz");
    std::fs::write(dir.join(&name), archive).unwrap();
    let descriptor = json!({
        "id": "pluxy/core",
        "version": version,
        "tier": "native",
        "platform": platform(),
        "size": archive.len(),
        "sha256": hex(&Sha256::digest(archive)),
    })
    .to_string();
    let mut sig = keys.key.sign(descriptor.as_bytes()).to_bytes();
    if !good {
        sig[0] ^= 0xff;
    }
    json!({
        "product": "core",
        "version": version,
        "channel": "stable",
        "published": "2026-11-01T00:00:00Z",
        "artifacts": [{
            "platform": platform(),
            "url": name,
            "descriptor": descriptor,
            "signature": hex(&sig),
            "key_id": keys.key_id,
        }],
    })
}

pub fn write_index(path: &Path, releases: &[Value]) {
    std::fs::write(
        path,
        serde_json::to_vec_pretty(&json!({"format": 1, "releases": releases})).unwrap(),
    )
    .unwrap();
}
