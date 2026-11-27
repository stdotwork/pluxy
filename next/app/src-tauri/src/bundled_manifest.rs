//! The manifest of the core that comes with the app (`bundled_core.rs`): the core version, the
//! SHA-256 of its release's signed descriptor, and each file of the release as it unpacked, by
//! path, size, SHA-256 and execute bit. The packaging step writes it from the release it checked
//! (`bundled_core::prepare`), and build.rs compiles it into the app's program, checked with this
//! same code: build.rs includes this file, so it uses nothing but serde_json.
//!
//! The paths are those of a core version's folder: the program (`pluxy`, on Windows `pluxy.exe`)
//! at the top and everything else under `bundled/`.

use serde_json::{json, Value};

pub const FORMAT: u64 = 1;

/// One file of the core that comes with the app.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Entry {
    pub path: String,
    pub size: u64,
    pub sha256: String,
    pub executable: bool,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Manifest {
    pub version: String,
    pub descriptor_sha256: String,
    pub files: Vec<Entry>,
}

fn sha256_hex(s: &str) -> bool {
    s.len() == 64
        && s.bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

/// A release version the store can hold as a version of its own (`cores::valid_id`), and not in
/// the adopted programs' names.
fn version_ok(v: &str) -> bool {
    !v.is_empty()
        && v.len() <= 64
        && v.as_bytes()[0].is_ascii_alphanumeric()
        && v.bytes()
            .all(|b| b.is_ascii_alphanumeric() || b".+_-".contains(&b))
        && !v.starts_with("custom-")
}

/// A plain relative path with `/` between its parts: no empty, `.` or `..` part, no `\` or `:`,
/// no control character.
fn plain(path: &str) -> bool {
    !path.is_empty()
        && path.split('/').all(|p| {
            !p.is_empty()
                && p != "."
                && p != ".."
                && !p.chars().any(|c| c.is_control() || c == '\\' || c == ':')
        })
}

impl Manifest {
    /// Reads and checks a manifest for a system whose core program is named program.
    pub fn parse(bytes: &[u8], program: &str) -> Result<Manifest, String> {
        let v: Value = serde_json::from_slice(bytes)
            .map_err(|e| format!("the bundled core's manifest is not JSON: {e}"))?;
        let bad = |what: &str| format!("the bundled core's manifest is not valid: {what}");
        let o = v.as_object().ok_or_else(|| bad("not an object"))?;
        if o.keys()
            .any(|k| !["format", "version", "descriptor_sha256", "files"].contains(&k.as_str()))
        {
            return Err(bad("unknown field"));
        }
        if o.get("format").and_then(Value::as_u64) != Some(FORMAT) {
            return Err(bad("format is not 1"));
        }
        let version = o
            .get("version")
            .and_then(Value::as_str)
            .filter(|s| version_ok(s))
            .ok_or_else(|| bad("version"))?
            .to_string();
        let descriptor_sha256 = o
            .get("descriptor_sha256")
            .and_then(Value::as_str)
            .filter(|s| sha256_hex(s))
            .ok_or_else(|| bad("descriptor_sha256"))?
            .to_string();
        let mut files = Vec::new();
        for f in o
            .get("files")
            .and_then(Value::as_array)
            .ok_or_else(|| bad("files"))?
        {
            let f = f
                .as_object()
                .ok_or_else(|| bad("a file is not an object"))?;
            if f.keys()
                .any(|k| !["path", "size", "sha256", "executable"].contains(&k.as_str()))
            {
                return Err(bad("unknown field in a file"));
            }
            files.push(Entry {
                path: f
                    .get("path")
                    .and_then(Value::as_str)
                    .ok_or_else(|| bad("a file's path"))?
                    .to_string(),
                size: f
                    .get("size")
                    .and_then(Value::as_u64)
                    .ok_or_else(|| bad("a file's size"))?,
                sha256: f
                    .get("sha256")
                    .and_then(Value::as_str)
                    .filter(|s| sha256_hex(s))
                    .ok_or_else(|| bad("a file's sha256"))?
                    .to_string(),
                executable: f
                    .get("executable")
                    .and_then(Value::as_bool)
                    .ok_or_else(|| bad("a file's executable"))?,
            });
        }
        let m = Manifest {
            version,
            descriptor_sha256,
            files,
        };
        m.check(program)?;
        Ok(m)
    }

    /// The program at the top and every other file under `bundled/`, each path plain and named
    /// once (on Windows, where names differ only in case are one name, once in any case).
    pub fn check(&self, program: &str) -> Result<(), String> {
        let bad = |what: String| format!("the bundled core's manifest is not valid: {what}");
        let mut seen = std::collections::HashSet::new();
        let mut programs = 0;
        for f in &self.files {
            if !plain(&f.path) {
                return Err(bad(format!("{} is not a plain path", f.path)));
            }
            if !seen.insert(f.path.to_lowercase()) {
                return Err(bad(format!("{} is named twice", f.path)));
            }
            if f.path == program {
                programs += 1;
            } else if !f.path.starts_with("bundled/") {
                return Err(bad(format!(
                    "{} is neither {program} nor under bundled/",
                    f.path
                )));
            }
        }
        if programs != 1 {
            return Err(bad(format!("it has no {program} program")));
        }
        Ok(())
    }

    pub fn to_json(&self) -> String {
        let files: Vec<Value> = self
            .files
            .iter()
            .map(|f| {
                json!({"path": f.path, "size": f.size, "sha256": f.sha256, "executable": f.executable})
            })
            .collect();
        serde_json::to_string_pretty(&json!({
            "format": FORMAT,
            "version": self.version,
            "descriptor_sha256": self.descriptor_sha256,
            "files": files,
        }))
        .expect("JSON")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_local_builds_version_is_a_manifest_version() {
        // package.ts local: <core version>+local.<12 hex digits>.
        assert!(version_ok("0.1.0+local.0123456789ab"));
    }
}
