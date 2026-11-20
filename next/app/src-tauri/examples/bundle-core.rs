//! The packaging step's check of the core the app comes with (`bundled_core::prepare`), which
//! package.ts runs before it builds the app:
//!
//!   bundle-core <core source index> <version> <key ID> <publisher key, 64 hex digits> <out>
//!
//! It finds the release of core version for this computer in the core source's index (a full
//! path), checks its archive against its descriptor signed with the publisher key, trusted for
//! that source and key ID only, unpacks it into `<out>/core` (which must not exist) and writes its
//! manifest to `<out>/bundled-core.json`, which build.rs compiles into the app
//! (`PLUXY_BUNDLED_CORE`). The unpacked files are made readable by everyone, as a package's are.
use ed25519_dalek::VerifyingKey;
use pluxy_app::artifact::{decode_hex, PublisherKeys};
use std::path::Path;

struct Key {
    source: String,
    key_id: String,
    key: VerifyingKey,
}

impl PublisherKeys for Key {
    fn publisher_key(
        &self,
        source: &str,
        key_id: &str,
        publisher: &str,
    ) -> Result<VerifyingKey, String> {
        if source == self.source && key_id == self.key_id && publisher == "pluxy" {
            Ok(self.key)
        } else {
            Err(format!(
                "the core release is signed by key {key_id} of {publisher} from {source}, which is not the key given"
            ))
        }
    }
}

/// Unix: folders and executable files 0755, other files 0644.
fn readable(path: &Path) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let m = std::fs::symlink_metadata(path)?;
        let exec = m.permissions().mode() & 0o100 != 0;
        let mode = if m.is_dir() || exec { 0o755 } else { 0o644 };
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(mode))?;
        if m.is_dir() {
            for e in std::fs::read_dir(path)? {
                readable(&e?.path())?;
            }
        }
    }
    #[cfg(not(unix))]
    let _ = path;
    Ok(())
}

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let [index, version, key_id, public_key, out] = &args[..] else {
        eprintln!("usage: bundle-core <core source index> <version> <key ID> <publisher key, 64 hex digits> <out>");
        std::process::exit(2);
    };
    let result = (|| -> Result<String, String> {
        let key = decode_hex::<32>(public_key).map_err(|e| format!("the publisher key: {e}"))?;
        let key = VerifyingKey::from_bytes(&key).map_err(|e| format!("the publisher key: {e}"))?;
        let keys = Key {
            source: index.clone(),
            key_id: key_id.clone(),
            key,
        };
        let out = Path::new(out);
        let core = out.join("core");
        let manifest = pluxy_app::bundled_core::prepare(&keys, index, version, &core)?;
        readable(&core).map_err(|e| format!("{}: {e}", core.display()))?;
        let path = out.join("bundled-core.json");
        std::fs::write(&path, manifest.to_json())
            .map_err(|e| format!("{}: {e}", path.display()))?;
        Ok(format!(
            "bundle-core: core {version} checked against its signed descriptor ({} files) in {}; manifest {}",
            manifest.files.len(),
            core.display(),
            path.display()
        ))
    })();
    match result {
        Ok(said) => println!("{said}"),
        Err(e) => {
            eprintln!("bundle-core: refused: {e}");
            std::process::exit(1);
        }
    }
}
