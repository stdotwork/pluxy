//! Production roots are compiled, never imported from HOME. Test roots require both gates.
use crate::artifact::decode_hex;
#[cfg(feature = "artifact-test-trust")]
use crate::artifact::valid_publisher;
use ed25519_dalek::VerifyingKey;
use serde::Deserialize;
use std::{fs, path::Path};

pub const MARKER_FILE: &str = ".pluxy-test-trust-profile.json";
pub const PROFILE_FILE: &str = ".pluxy-test-trust-roots.json";
pub const COMPILED_TEST_TRUST: bool = cfg!(feature = "artifact-test-trust");
pub const PRODUCTION_TRUST: &str = include_str!("../trust/production.json");

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct TrustConfig {
    pub roots: Vec<Root>,
    pub updater: Option<UpdaterRoot>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct Root {
    pub kind: String,
    pub publisher: String,
    pub source: String,
    pub key_id: String,
    pub public_key: String,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct UpdaterRoot {
    pub kind: String,
    pub public_key: String,
}

pub struct TrustStore {
    config: TrustConfig,
}
impl TrustStore {
    /// home is the process HOME selected by the native runner, never a page-supplied path.
    /// Production builds ignore it completely. A missing marker leaves trust unconfigured.
    pub fn load(home: &Path) -> Result<Self, String> {
        #[allow(unused_mut)]
        let mut config: TrustConfig =
            serde_json::from_str(PRODUCTION_TRUST).map_err(|e| e.to_string())?;
        // Production keys are intentionally not configured yet. Reject accidental additions
        // until their release policy and validation are implemented together.
        if !config.roots.is_empty() || config.updater.is_some() {
            return Err("production trust is not configured".into());
        }
        #[cfg(feature = "artifact-test-trust")]
        if home.join(MARKER_FILE).exists() {
            check_profile(home)?;
            let bytes = read_bounded(&home.join(PROFILE_FILE))?;
            config = serde_json::from_slice(&bytes).map_err(|e| e.to_string())?;
            for root in &config.roots {
                if root.kind != "test"
                    || !valid_publisher(&root.publisher)
                    || root.source.is_empty()
                    || root.key_id.is_empty()
                {
                    return Err("invalid test publisher root".into());
                }
                let key = VerifyingKey::from_bytes(&decode_hex::<32>(&root.public_key)?)
                    .map_err(|_| "invalid publisher public key")?;
                if key.is_weak() {
                    return Err("weak publisher public key".into());
                }
            }
            if let Some(updater) = &config.updater {
                if updater.kind != "test" || updater.public_key.is_empty() {
                    return Err("invalid test updater root".into());
                }
            }
        }
        #[cfg(not(feature = "artifact-test-trust"))]
        let _ = home;
        Ok(Self { config })
    }
    pub fn require_native(&self) -> Result<(), String> {
        if self.config.roots.is_empty() {
            Err("native-tier trust not configured".into())
        } else {
            Ok(())
        }
    }
    /// Only the public key. Tauri must verify updater artifacts with its own signature scheme.
    pub fn updater_key(&self) -> Result<&str, String> {
        self.config
            .updater
            .as_ref()
            .map(|u| u.public_key.as_str())
            .ok_or_else(|| "updater trust not configured".into())
    }
    pub(crate) fn publisher_key(
        &self,
        source: &str,
        key_id: &str,
        publisher: &str,
    ) -> Result<VerifyingKey, String> {
        self.require_native()?;
        let mut matches = self
            .config
            .roots
            .iter()
            .filter(|r| r.source == source && r.key_id == key_id && r.publisher == publisher);
        let root = matches
            .next()
            .ok_or("unknown publisher key or changed source")?;
        if matches.next().is_some() {
            return Err("ambiguous publisher key".into());
        }
        VerifyingKey::from_bytes(&decode_hex::<32>(&root.public_key)?)
            .map_err(|_| "invalid publisher public key".into())
    }
}

#[derive(Deserialize, serde::Serialize)]
#[serde(deny_unknown_fields)]
struct Marker {
    format: u32,
    home: String,
    purpose: String,
}

/// Runner utility: writes only a marker, never roots or private keys. Available in normal test
/// builds so the normal-build refusal control can present an otherwise valid marker.
pub fn write_test_marker(home: &Path) -> Result<(), String> {
    use std::io::Write;
    #[cfg(unix)]
    use std::os::unix::fs::OpenOptionsExt;
    let home = home.canonicalize().map_err(|e| e.to_string())?;
    let marker = Marker {
        format: 1,
        home: home.to_string_lossy().into(),
        purpose: "pluxy-artifact-test-trust".into(),
    };
    let mut options = fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    options.mode(0o600);
    let mut file = options
        .open(home.join(MARKER_FILE))
        .map_err(|e| e.to_string())?;
    file.write_all(&serde_json::to_vec(&marker).map_err(|e| e.to_string())?)
        .map_err(|e| e.to_string())
}
#[cfg(feature = "artifact-test-trust")]
fn check_profile(home: &Path) -> Result<(), String> {
    use std::os::unix::fs::MetadataExt;
    let real = home.canonicalize().map_err(|e| e.to_string())?;
    let process_home = std::env::var_os("HOME").ok_or("test trust requires HOME")?;
    if std::path::PathBuf::from(process_home)
        .canonicalize()
        .map_err(|e| e.to_string())?
        != real
    {
        return Err("test trust profile must be the process HOME".into());
    }
    let temp = std::env::temp_dir()
        .canonicalize()
        .map_err(|e| e.to_string())?;
    if real == temp || !real.starts_with(&temp) {
        return Err("test trust requires a temporary HOME".into());
    }
    let meta = fs::symlink_metadata(home).map_err(|e| e.to_string())?;
    if !meta.is_dir() || meta.uid() != unsafe { libc::geteuid() } || meta.mode() & 0o077 != 0 {
        return Err("test HOME must be a private owned directory".into());
    }
    for name in [MARKER_FILE, PROFILE_FILE] {
        let m = fs::symlink_metadata(home.join(name)).map_err(|e| e.to_string())?;
        if !m.is_file() || m.uid() != meta.uid() || m.mode() & 0o022 != 0 {
            return Err(
                "test trust files must be owned regular files, not writable by others".into(),
            );
        }
    }
    let marker: Marker = serde_json::from_slice(&read_bounded(&home.join(MARKER_FILE))?)
        .map_err(|e| e.to_string())?;
    if marker.format != 1
        || marker.home != real.to_string_lossy()
        || marker.purpose != "pluxy-artifact-test-trust"
    {
        return Err("test trust profile marker mismatch".into());
    }
    Ok(())
}
pub(crate) fn read_bounded(path: &Path) -> Result<Vec<u8>, String> {
    use std::io::Read;
    let mut file = fs::File::open(path).map_err(|e| format!("{}: {e}", path.display()))?;
    let mut bytes = Vec::new();
    file.by_ref()
        .take(64 * 1024 + 1)
        .read_to_end(&mut bytes)
        .map_err(|e| e.to_string())?;
    if bytes.len() > 64 * 1024 {
        return Err("trust input exceeds 64 KiB".into());
    }
    Ok(bytes)
}
