//! The core that comes with the app (README "The app's own core"): a tagged core's signed release,
//! checked when the app is packaged and pinned file by file by a manifest compiled into the app's
//! program (`bundled_manifest.rs`). A build without one (a dev build) has no bundled core.
//!
//! When the app is packaged, `prepare` takes the release of the version the app names from a core
//! source's index, checks it against its signed descriptor with the publisher key, as installing
//! it from a core source would (`cores::unpack_release`), unpacks it and writes the manifest from
//! the files it unpacked. The package carries those files beside the app (`place`); in managed
//! mode the app copies them into its own cores (`cores::Store::install_bundled`), reading only the
//! files the manifest lists, and runs the copy, never the files beside the app.

use crate::bundled_manifest::{Entry, Manifest};
use crate::cores::{self, PROGRAM};
use std::path::{Path, PathBuf};
use std::sync::OnceLock;

/// Where the files of the core that comes with the app are, in the installed app.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Place {
    /// The core's program.
    pub program: PathBuf,
    /// The folder holding what the manifest lists under `bundled/`.
    pub bundled: PathBuf,
}

/// The place of the bundled core for the app program exe, named product (Tauri's product name):
///
/// - Windows: `pluxy.exe` and `bundled\` beside the app's program;
/// - macOS: `Contents/MacOS/pluxy` beside it, and `Contents/Resources/core/bundled/`;
/// - Linux: `pluxy` beside it (`usr/bin` in the AppImage), and `core/bundled/` in the app's
///   resource folder, `usr/lib/<product>/`, where the AppImage bundler puts resources.
///
/// The paths are made from exe's own; nothing is resolved or read here.
pub fn place(exe: &Path, product: &str) -> Option<Place> {
    let dir = exe.parent()?;
    let program = dir.join(PROGRAM);
    let bundled = if cfg!(windows) {
        dir.join("bundled")
    } else if cfg!(target_os = "macos") {
        dir.parent()?.join("Resources").join("core").join("bundled")
    } else {
        let _ = product;
        dir.parent()?
            .join("lib")
            .join(product)
            .join("core")
            .join("bundled")
    };
    Some(Place { program, bundled })
}

/// The manifest compiled into this program (build.rs, from `PLUXY_BUNDLED_CORE` at build time),
/// if it has one. build.rs checked it with the same code, so it reads here.
pub fn compiled() -> Option<&'static Manifest> {
    static MANIFEST: OnceLock<Option<Manifest>> = OnceLock::new();
    const RAW: &str = include_str!(concat!(env!("OUT_DIR"), "/bundled_core.json"));
    MANIFEST
        .get_or_init(|| match RAW.trim() {
            "null" => None,
            raw => match Manifest::parse(raw.as_bytes(), PROGRAM) {
                Ok(m) => Some(m),
                Err(e) => {
                    eprintln!("pluxy-app: {e}");
                    None
                }
            },
        })
        .as_ref()
}

/// The core that comes with this app, as managed mode uses it.
#[derive(Clone, Debug)]
pub struct Bundled {
    pub manifest: Manifest,
    pub place: Place,
    /// This app's version: the app moves a selection it made to its own bundled core once per
    /// change of it.
    pub app_version: String,
}

impl Bundled {
    pub fn version(&self) -> &str {
        &self.manifest.version
    }

    /// Whether the program at path is this bundled core's program: its size and SHA-256.
    pub fn is_program(&self, path: &Path) -> bool {
        let Some(p) = self.manifest.files.iter().find(|f| f.path == PROGRAM) else {
            return false;
        };
        matches!(cores::Store::inspect(path), Ok((sha, size)) if sha == p.sha256 && size == p.size)
    }
}

/// Package time: finds core version in the core source's index (a full path), checks its archive
/// for this computer against its signed descriptor with keys, and unpacks it into out, which must
/// not exist yet, with the store's own rules. Returns the manifest of what it unpacked.
pub fn prepare(
    keys: &impl crate::artifact::PublisherKeys,
    source: &str,
    version: &str,
    out: &Path,
) -> Result<Manifest, String> {
    let (descriptor_sha256, files) = cores::unpack_release(keys, source, version, out)?;
    let m = Manifest {
        version: version.to_string(),
        descriptor_sha256,
        files: files
            .into_iter()
            .map(|f| Entry {
                path: f.path,
                size: f.size,
                sha256: f.sha256,
                executable: f.executable,
            })
            .collect(),
    };
    m.check(PROGRAM)?;
    Ok(m)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_place_follows_each_systems_package_layout() {
        #[cfg(target_os = "linux")]
        assert_eq!(
            place(Path::new("/tmp/.mount_x/usr/bin/pluxy-app"), "pluxy Test"),
            Some(Place {
                program: "/tmp/.mount_x/usr/bin/pluxy".into(),
                bundled: "/tmp/.mount_x/usr/lib/pluxy Test/core/bundled".into(),
            })
        );
        #[cfg(target_os = "macos")]
        assert_eq!(
            place(Path::new("/A/pluxy.app/Contents/MacOS/pluxy-app"), "pluxy"),
            Some(Place {
                program: "/A/pluxy.app/Contents/MacOS/pluxy".into(),
                bundled: "/A/pluxy.app/Contents/Resources/core/bundled".into(),
            })
        );
        #[cfg(windows)]
        assert_eq!(
            place(Path::new(r"C:\A\pluxy\pluxy-app.exe"), "pluxy"),
            Some(Place {
                program: r"C:\A\pluxy\pluxy.exe".into(),
                bundled: r"C:\A\pluxy\bundled".into(),
            })
        );
    }

    fn manifest(files: &[(&str, bool)]) -> Vec<u8> {
        let files: Vec<serde_json::Value> = files
            .iter()
            .map(|(p, x)| serde_json::json!({"path": p, "size": 1, "sha256": "a".repeat(64), "executable": x}))
            .collect();
        serde_json::to_vec(&serde_json::json!({
            "format": 1, "version": "1.2.3", "descriptor_sha256": "b".repeat(64), "files": files,
        }))
        .unwrap()
    }

    #[test]
    fn a_manifest_names_the_program_once_and_the_rest_under_bundled() {
        let ok = Manifest::parse(
            &manifest(&[("pluxy", true), ("bundled/index.json", false)]),
            "pluxy",
        )
        .unwrap();
        assert_eq!(ok.version, "1.2.3");
        assert_eq!(
            Manifest::parse(&ok.to_json().into_bytes(), "pluxy").unwrap(),
            ok
        );
        for (files, why) in [
            (vec![("bundled/index.json", false)], "no pluxy program"),
            (vec![("pluxy", true), ("other", false)], "neither"),
            (vec![("pluxy", true), ("bundled/../x", false)], "not a plain path"),
            (vec![("pluxy", true), ("bundled//x", false)], "not a plain path"),
            (vec![("pluxy", true), ("/bundled/x", false)], "not a plain path"),
            (vec![("pluxy", true), ("bundled/a\\b", false)], "not a plain path"),
            (
                vec![("pluxy", true), ("bundled/A", false), ("bundled/a", false)],
                "named twice",
            ),
        ] {
            let e = Manifest::parse(&manifest(&files), "pluxy").unwrap_err();
            assert!(e.contains(why), "{files:?}: {e}");
        }
        let mut v: serde_json::Value = serde_json::from_slice(&manifest(&[("pluxy", true)])).unwrap();
        v["version"] = "custom-1".into();
        assert!(Manifest::parse(&serde_json::to_vec(&v).unwrap(), "pluxy").is_err());
        v["version"] = "1.0.0".into();
        v["extra"] = 1.into();
        assert!(Manifest::parse(&serde_json::to_vec(&v).unwrap(), "pluxy").is_err());
    }
}
