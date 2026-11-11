//! Publisher authentication before extraction. Consumers must use the checked archive handle,
//! check the extracted manifest, and enforce their own admission/consent and tree integrity rules.
use ed25519_dalek::Signature;
use ed25519_dalek::VerifyingKey;
use serde::Deserialize;
use sha2::{Digest, Sha256};
use std::io::{Read, Seek, SeekFrom};

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Tier {
    Web,
    Native,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Descriptor {
    pub id: String,
    pub version: String,
    pub tier: Tier,
    pub platform: String,
    pub size: u64,
    pub sha256: String,
}

/// Only constructed by verify_archive. Exact bytes, source and signer are retained for storage.
#[derive(Debug)]
pub struct VerifiedDescriptor {
    descriptor: Descriptor,
    bytes: Vec<u8>,
    signature: Vec<u8>,
}
impl VerifiedDescriptor {
    pub fn descriptor(&self) -> &Descriptor {
        &self.descriptor
    }
    pub fn descriptor_bytes(&self) -> &[u8] {
        &self.bytes
    }
    pub fn signature_bytes(&self) -> &[u8] {
        &self.signature
    }
    /// Compare identity after safe extraction; this is not full manifest schema validation.
    pub fn check_manifest(&self, bytes: &[u8]) -> Result<(), String> {
        #[derive(Deserialize)]
        struct Manifest {
            manifest_version: u32,
            id: String,
            version: String,
            app: App,
        }
        #[derive(Deserialize)]
        struct App {
            tier: Tier,
        }
        let m: Manifest = serde_json::from_slice(bytes).map_err(|_| "invalid package manifest")?;
        if m.manifest_version != 1
            || m.id != self.descriptor.id
            || m.version != self.descriptor.version
            || m.app.tier != self.descriptor.tier
        {
            return Err("manifest identity differs from signed descriptor".into());
        }
        Ok(())
    }
}

/// Resolve an approved signer separately from cryptographic verification. Implementations bind
/// publisher, key ID and source to native settings or the curated index, never page claims.
pub trait PublisherKeys {
    fn publisher_key(
        &self,
        source: &str,
        key_id: &str,
        publisher: &str,
    ) -> Result<VerifyingKey, String>;
}

/// Verify exact descriptor bytes with an already approved key. Web resource signers from native
/// settings and curated native packages use this same implementation.
pub fn verify_descriptor(
    key: &VerifyingKey,
    bytes: &[u8],
    signature: &[u8],
    platform: &str,
) -> Result<VerifiedDescriptor, String> {
    if bytes.len() > 16 * 1024 {
        return Err("descriptor exceeds 16 KiB".into());
    }
    let d: Descriptor =
        serde_json::from_slice(bytes).map_err(|_| "invalid UTF-8 descriptor JSON")?;
    if !valid_id(&d.id)
        || !valid_version(&d.version)
        || !valid_platform(&d.platform)
        || decode_hex::<32>(&d.sha256).is_err()
    {
        return Err("invalid descriptor identity, platform or SHA-256".into());
    }
    verify_signature(key, bytes, signature)?;
    if d.platform != platform {
        return Err("descriptor platform does not match selected artifact".into());
    }
    Ok(VerifiedDescriptor {
        descriptor: d,
        bytes: bytes.to_vec(),
        signature: signature.to_vec(),
    })
}

/// Strict detached Ed25519 byte verification, also exercised by RFC 8032 public vectors.
pub fn verify_signature(key: &VerifyingKey, bytes: &[u8], signature: &[u8]) -> Result<(), String> {
    let sig = Signature::from_slice(signature).map_err(|_| "signature must be 64 raw bytes")?;
    key.verify_strict(bytes, &sig)
        .map_err(|_| "invalid descriptor signature".into())
}

/// Owns the checked archive handle, rewound for extraction. Prevent concurrent writes, use
/// into_archive instead of reopening a path, and check the manifest before admission.
#[derive(Debug)]
pub struct VerifiedArchive<R> {
    signed: VerifiedDescriptor,
    archive: R,
}
impl<R> std::ops::Deref for VerifiedArchive<R> {
    type Target = VerifiedDescriptor;
    fn deref(&self) -> &Self::Target {
        &self.signed
    }
}
impl<R> VerifiedArchive<R> {
    pub fn into_archive(self) -> R {
        self.archive
    }
}

/// Authenticate before reading archive bytes, stream size/hash checks, and retain the handle.
pub fn verify_archive<R: Read + Seek>(
    key: &VerifyingKey,
    bytes: &[u8],
    signature: &[u8],
    platform: &str,
    mut archive: R,
) -> Result<VerifiedArchive<R>, String> {
    let signed = verify_descriptor(key, bytes, signature, platform)?;
    let d = &signed.descriptor;
    archive
        .seek(SeekFrom::Start(0))
        .map_err(|e| e.to_string())?;
    let mut digest = Sha256::new();
    let mut count = 0u64;
    let mut buf = [0; 64 * 1024];
    loop {
        let n = archive.read(&mut buf).map_err(|e| e.to_string())?;
        if n == 0 {
            break;
        }
        count = count.checked_add(n as u64).ok_or("archive size overflow")?;
        if count > d.size {
            return Err("archive size differs from signed descriptor".into());
        }
        digest.update(&buf[..n]);
    }
    if count != d.size {
        return Err("archive size differs from signed descriptor".into());
    }
    if hex(&digest.finalize()) != d.sha256 {
        return Err("archive SHA-256 differs from signed descriptor".into());
    }
    archive
        .seek(SeekFrom::Start(0))
        .map_err(|e| e.to_string())?;
    Ok(VerifiedArchive { signed, archive })
}

/// Curated/source-bound lookup followed by the same feature-free verifier.
pub fn verify_trusted_archive<R: Read + Seek>(
    store: &impl PublisherKeys,
    source: &str,
    key_id: &str,
    bytes: &[u8],
    signature: &[u8],
    platform: &str,
    archive: R,
) -> Result<VerifiedArchive<R>, String> {
    if bytes.len() > 16 * 1024 {
        return Err("descriptor exceeds 16 KiB".into());
    }
    let d: Descriptor =
        serde_json::from_slice(bytes).map_err(|_| "invalid UTF-8 descriptor JSON")?;
    let key = store.publisher_key(source, key_id, d.id.split('/').next().unwrap_or_default())?;
    verify_archive(&key, bytes, signature, platform, archive)
}

pub fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}
pub fn decode_hex<const N: usize>(text: &str) -> Result<[u8; N], String> {
    if text.len() != N * 2
        || !text
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
    {
        return Err("expected lower-case hexadecimal bytes".into());
    }
    let mut result = [0; N];
    for (i, b) in result.iter_mut().enumerate() {
        *b = u8::from_str_radix(&text[i * 2..i * 2 + 2], 16).map_err(|_| "invalid hex")?;
    }
    Ok(result)
}
fn valid_component(v: &str) -> bool {
    !v.is_empty()
        && (v.as_bytes()[0].is_ascii_lowercase() || v.as_bytes()[0].is_ascii_digit())
        && v.bytes()
            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b".-".contains(&b))
}
#[cfg(feature = "artifact-test-trust")]
pub(crate) fn valid_publisher(v: &str) -> bool {
    valid_component(v)
}
fn valid_id(v: &str) -> bool {
    let p: Vec<_> = v.split('/').collect();
    p.len() == 2 && p.iter().all(|p| valid_component(p))
}
fn valid_version(v: &str) -> bool {
    !v.is_empty()
        && v.len() <= 64
        && v.as_bytes()[0].is_ascii_alphanumeric()
        && v.bytes()
            .all(|b| b.is_ascii_alphanumeric() || b".+_-".contains(&b))
}
fn valid_platform(v: &str) -> bool {
    let p: Vec<_> = v.split('/').collect();
    p.len() == 2
        && p.iter().all(|p| {
            !p.is_empty()
                && p.bytes()
                    .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit())
        })
}
