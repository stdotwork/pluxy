//! Validate final release inputs, not a source config before Tauri merges overrides.
use crate::trust::{read_bounded, TrustConfig, COMPILED_TEST_TRUST, PRODUCTION_TRUST};
use serde_json::Value;
use std::io::{Read, Seek, SeekFrom};
use std::{
    fs,
    path::{Path, PathBuf},
};

#[derive(Clone)]
pub struct ReleaseInputs {
    pub tauri_config: PathBuf,
    pub compiled_trust: PathBuf,
    pub packaged_trust: PathBuf,
    /// JSON array of all resolved cargo feature names for the app.
    pub features: PathBuf,
    /// Complete staged trust directory; only empty trust JSON is currently permitted.
    pub bundled_trust: PathBuf,
    pub artifacts: Vec<PathBuf>,
}
pub fn validate(inputs: &ReleaseInputs) -> Result<(), String> {
    if COMPILED_TEST_TRUST || cfg!(feature = "native-test") {
        return Err("release validator was compiled with a test feature".into());
    }
    empty_trust(PRODUCTION_TRUST.as_bytes())?;
    if inputs.artifacts.is_empty() {
        return Err("release validation requires every artifact output path".into());
    }
    for path in [
        &inputs.tauri_config,
        &inputs.compiled_trust,
        &inputs.packaged_trust,
        &inputs.features,
    ] {
        reject_test_path(path)?;
    }
    for path in &inputs.artifacts {
        reject_test_path(path)?;
    }
    let config = json_file(&inputs.tauri_config)?;
    let _: tauri::Config = serde_json::from_slice(&read_bounded(&inputs.tauri_config)?)
        .map_err(|_| "invalid final Tauri configuration")?;
    if config.get("identifier").and_then(Value::as_str) != Some("dev.pluxy.desktop")
        || config.get("productName").and_then(Value::as_str) != Some("pluxy")
    {
        return Err("release identifier/product must be dev.pluxy.desktop/pluxy".into());
    }
    if let Some(updater) = config.pointer("/plugins/updater") {
        if updater != &Value::Null && updater != &serde_json::json!({}) {
            return Err("production updater trust not configured".into());
        }
    }
    if config
        .pointer("/bundle/createUpdaterArtifacts")
        .is_some_and(|v| v != &Value::Bool(false))
    {
        return Err("updater artifacts require production trust".into());
    }
    reject_test(&config)?;
    if let Some(name) = config.get("mainBinaryName").and_then(Value::as_str) {
        reject_test_path(Path::new(name))?;
    }
    // Trust resources declared by the final config must be checked even when omitted from the
    // separately staged trust directory. Reject unsupported globs rather than silently skipping.
    if let Some(resources) = config.pointer("/bundle/resources") {
        let names: Vec<&str> = if let Some(a) = resources.as_array() {
            a.iter()
                .map(|v| v.as_str().ok_or("resource path must be a string"))
                .collect::<Result<_, _>>()?
        } else if let Some(o) = resources.as_object() {
            o.keys().map(String::as_str).collect()
        } else {
            return Err("invalid bundle resources".into());
        };
        for name in names {
            if name.contains(['*', '?', '[']) {
                return Err("release trust validation requires expanded resource paths".into());
            }
            let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(name);
            scan_trust(&path)?;
        }
    }
    empty_trust(&read_bounded(&inputs.compiled_trust)?)?;
    empty_trust(&read_bounded(&inputs.packaged_trust)?)?;
    let features = json_file(&inputs.features)?;
    let features = features.as_array().ok_or("features must be a JSON array")?;
    for feature in features {
        let feature = feature.as_str().ok_or("feature must be a string")?;
        if feature.to_ascii_lowercase().contains("test")
            || !feature
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || b"_-".contains(&b))
        {
            return Err("test cargo feature in release".into());
        }
    }
    scan_trust(&inputs.bundled_trust)?;
    // Compare with this validator's actual build. A stale OUT_DIR or source-only config cannot
    // describe a different app and still pass. Receipt presence binds at least the app executable.
    if config
        != serde_json::from_str::<Value>(include_str!(concat!(
            env!("OUT_DIR"),
            "/app-tauri-config.json"
        )))
        .unwrap()
    {
        return Err("Tauri config differs from compiled merged config".into());
    }
    if read_bounded(&inputs.compiled_trust)? != PRODUCTION_TRUST.as_bytes() {
        return Err("compiled trust snapshot mismatch".into());
    }
    if features
        != serde_json::from_str::<Value>(include_str!(concat!(
            env!("OUT_DIR"),
            "/app-cargo-features.json"
        )))
        .unwrap()
        .as_array()
        .unwrap()
    {
        return Err("cargo feature snapshot mismatch".into());
    }
    let mut receipt_found = false;
    for artifact in &inputs.artifacts {
        receipt_found |= scan_artifact(artifact)?;
    }
    if !receipt_found {
        return Err(
            "app artifact lacks matching build receipt; include the actual app executable".into(),
        );
    }
    Ok(())
}
fn json_file(path: &Path) -> Result<Value, String> {
    serde_json::from_slice(&read_bounded(path)?)
        .map_err(|_| format!("{}: invalid JSON release input", path.display()))
}
fn empty_trust(bytes: &[u8]) -> Result<(), String> {
    let _: Value = serde_json::from_slice(bytes).map_err(|_| "invalid trust JSON")?;

    let config: TrustConfig = serde_json::from_slice(bytes)
        .map_err(|_| "invalid trust schema or private/unknown fields")?;
    if config.roots.iter().any(|r| r.kind != "production")
        || config
            .updater
            .as_ref()
            .is_some_and(|r| r.kind != "production")
    {
        return Err("test root in release trust".into());
    }
    if !config.roots.is_empty() || config.updater.is_some() {
        return Err(
            "production publisher/updater trust not configured; roots must be empty".into(),
        );
    }
    Ok(())
}
fn reject_test(v: &Value) -> Result<(), String> {
    match v {
        Value::String(s) => {
            let s = s.to_ascii_lowercase();
            if s.contains("private key")
                || s.contains("private_key")
                || s.contains("secret key")
                || s.contains("secret_key")
                || s.contains("pluxy test")
                || s.contains("dev.pluxy.desktop.test")
                || s.contains("test-trust")
                || s == "test"
            {
                return Err("test/private material in release config".into());
            }
        }
        Value::Array(a) => {
            for v in a {
                reject_test(v)?;
            }
        }
        Value::Object(o) => {
            for (key, value) in o {
                let k = key.to_ascii_lowercase().replace(['_', '-'], "");
                if k.contains("private")
                    || k.contains("secret")
                    || k.contains("test")
                    || k == "pubkey"
                    || k == "publickey"
                {
                    return Err("test/private/key input is not configured for release".into());
                }
                reject_test(value)?;
            }
        }
        _ => {}
    }
    Ok(())
}
fn reject_test_path(path: &Path) -> Result<(), String> {
    let name = path
        .file_name()
        .unwrap_or_default()
        .to_string_lossy()
        .to_ascii_lowercase();
    if name.contains("-test")
        || name.contains("pluxy test")
        || name.contains(".test")
        || name.contains("test-trust")
        || name.contains("private")
        || name.contains("secret")
    {
        return Err("test/private release input or artifact path".into());
    }
    Ok(())
}
fn scan_trust(path: &Path) -> Result<(), String> {
    reject_test_path(path)?;
    let meta = fs::symlink_metadata(path).map_err(|e| e.to_string())?;
    if meta.is_dir() {
        for entry in fs::read_dir(path).map_err(|e| e.to_string())? {
            scan_trust(&entry.map_err(|e| e.to_string())?.path())?;
        }
    } else if meta.is_file() && path.extension().is_some_and(|e| e == "json") {
        // A closed schema rejects PEM, binary seeds, extra fields and unknown bundled files.
        empty_trust(&read_bounded(path)?)?;
    } else {
        return Err("bundled trust may contain only regular empty trust JSON files".into());
    }
    Ok(())
}

/// Bytes no release artifact may hold, in a file given or in any file inside an AppImage given.
const FORBIDDEN: [(&[u8], &str); 4] = [
    (
        b"PLUXY_TEST_BUILD_DO_NOT_RELEASE",
        "test build marker/identity in artifact",
    ),
    (
        b"dev.pluxy.desktop.test",
        "test build marker/identity in artifact",
    ),
    (b"pluxy Test", "test build marker/identity in artifact"),
    // Tauri updater keys and signatures are base64 of minisign text that starts
    // "untrusted comment:"; production updater trust is not configured, so any one is a test key.
    (
        b"dW50cnVzdGVkIGNvbW1lbnQ6",
        "updater key or signature in artifact",
    ),
];

/// Checks one artifact: its bytes and, for an AppImage, every file inside it. Says whether it
/// holds this build's receipt.
fn scan_artifact(path: &Path) -> Result<bool, String> {
    let meta =
        fs::symlink_metadata(path).map_err(|e| format!("artifact missing/unreadable: {e}"))?;
    if !meta.is_file() {
        return Err("artifact must be a regular file".into());
    }
    let what = path.display().to_string();
    let file = fs::File::open(path).map_err(|e| format!("{what}: {e}"))?;
    match appimage_offset(path)? {
        None => scan_stream(file, &what),
        // The runtime part as bytes; the squashfs image by the files in it.
        Some(offset) => Ok(scan_stream(file.take(offset), &what)? | scan_appimage(path, offset)?),
    }
}

/// Streams reader with overlap, so markers split across reads are still found; no
/// artifact-sized allocation. Says whether the build's receipt is in it.
fn scan_stream(mut reader: impl Read, what: &str) -> Result<bool, String> {
    let receipt = env!("PLUXY_BUILD_RECEIPT").as_bytes();
    let forbidden: Vec<&[u8]> = FORBIDDEN.iter().map(|(m, _)| *m).collect();
    let overlap = FORBIDDEN
        .iter()
        .map(|(m, _)| m.len())
        .chain([receipt.len()])
        .max()
        .unwrap()
        - 1;
    let mut buffer = vec![0; 64 * 1024 + overlap];
    let mut kept = 0;
    let mut found = false;
    loop {
        let n = reader
            .read(&mut buffer[kept..])
            .map_err(|e| format!("{what}: {e}"))?;
        if n == 0 {
            break;
        }
        let window = &buffer[..kept + n];
        if let Some(i) = first_of(window, &forbidden) {
            return Err(format!("{what}: {}", FORBIDDEN[i].1));
        }
        found = found || first_of(window, &[receipt]).is_some();
        kept = overlap.min(window.len());
        let end = window.len();
        buffer.copy_within(end - kept..end, 0);
    }
    Ok(found)
}

/// The index of a needle found in hay. One pass, comparing only where a needle's first byte is:
/// the validator is a debug build, and an AppImage holds hundreds of megabytes.
fn first_of(hay: &[u8], needles: &[&[u8]]) -> Option<usize> {
    let mut starts = [false; 256];
    for needle in needles {
        starts[needle[0] as usize] = true;
    }
    for (i, b) in hay.iter().enumerate() {
        if starts[*b as usize] {
            if let Some(k) = needles.iter().position(|n| hay[i..].starts_with(n)) {
                return Some(k);
            }
        }
    }
    None
}

/// Where an AppImage's squashfs image starts, read from its ELF header, never by running it.
/// None for a file that is not an AppImage (such as the app executable). Refuses an AppImage
/// that is not of type 2, or whose image is not where the header ends.
fn appimage_offset(path: &Path) -> Result<Option<u64>, String> {
    let what = path.display();
    let mut file = fs::File::open(path).map_err(|e| format!("{what}: {e}"))?;
    let mut header = Vec::new();
    file.by_ref()
        .take(64)
        .read_to_end(&mut header)
        .map_err(|e| format!("{what}: {e}"))?;
    // The AppImage specification's magic: an ELF file with "AI" and the type at offset 8.
    if header.len() < 16 || header[..4] != *b"\x7fELF" || header[8..10] != *b"AI" {
        return Ok(None);
    }
    if header[10] != 2 {
        return Err(format!(
            "{what}: AppImage type {} cannot be checked, only type 2",
            header[10]
        ));
    }
    let field = |at: usize, len: usize| -> Result<u64, String> {
        let bytes = header
            .get(at..at + len)
            .ok_or_else(|| format!("{what}: short ELF header"))?;
        let mut value = 0u64;
        for (i, b) in bytes.iter().enumerate() {
            let shift = if header[5] == 1 { i } else { len - 1 - i };
            value |= u64::from(*b) << (8 * shift);
        }
        Ok(value)
    };
    if header[5] != 1 && header[5] != 2 {
        return Err(format!("{what}: unknown ELF byte order"));
    }
    // The runtime's own size: its section header table ends it (e_shoff + e_shentsize * e_shnum).
    let (shoff, shentsize, shnum) = match header[4] {
        1 => (field(0x20, 4)?, field(0x2e, 2)?, field(0x30, 2)?),
        2 => (field(0x28, 8)?, field(0x3a, 2)?, field(0x3c, 2)?),
        _ => return Err(format!("{what}: unknown ELF class")),
    };
    let offset = shentsize
        .checked_mul(shnum)
        .and_then(|n| n.checked_add(shoff))
        .ok_or_else(|| format!("{what}: ELF header out of range"))?;
    let mut magic = [0; 4];
    file.seek(SeekFrom::Start(offset))
        .and_then(|_| file.read_exact(&mut magic))
        .map_err(|_| format!("{what}: no squashfs image where its runtime ends"))?;
    if magic != *b"hsqs" {
        return Err(format!("{what}: no squashfs image where its runtime ends"));
    }
    Ok(Some(offset))
}

/// Unpacks an AppImage's squashfs image (unsquashfs, from offset) into a private temporary
/// folder and checks every file in it; the folder is removed after.
fn scan_appimage(path: &Path, offset: u64) -> Result<bool, String> {
    let what = path.display().to_string();
    let mut random = [0; 12];
    getrandom::fill(&mut random).map_err(|e| e.to_string())?;
    let dir = std::env::temp_dir().join(format!(
        "pluxy-release-scan-{}",
        crate::artifact::hex(&random)
    ));
    let mut builder = fs::DirBuilder::new();
    #[cfg(unix)]
    std::os::unix::fs::DirBuilderExt::mode(&mut builder, 0o700);
    builder
        .create(&dir)
        .map_err(|e| format!("{}: {e}", dir.display()))?;
    let result = unpack_and_scan(path, offset, &dir.join("root"), &what);
    let _ = fs::remove_dir_all(&dir);
    result
}

fn unpack_and_scan(path: &Path, offset: u64, root: &Path, what: &str) -> Result<bool, String> {
    let out = std::process::Command::new("unsquashfs")
        .args([
            "-no-progress",
            "-no-xattrs",
            "-offset",
            &offset.to_string(),
            "-dest",
        ])
        .arg(root)
        .arg(path)
        .output()
        .map_err(|e| {
            format!("{what}: unsquashfs (package squashfs-tools) checks AppImages: {e}")
        })?;
    if !out.status.success() {
        return Err(format!(
            "{what}: unsquashfs could not unpack it: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        ));
    }
    scan_tree(root, root, what)
}

/// Checks a folder unpacked from an AppImage: names, link targets and contents, and trust JSON.
fn scan_tree(root: &Path, dir: &Path, appimage: &str) -> Result<bool, String> {
    let mut found = false;
    for entry in fs::read_dir(dir).map_err(|e| format!("{appimage}: {e}"))? {
        let path = entry.map_err(|e| format!("{appimage}: {e}"))?.path();
        let what = format!(
            "{appimage}!{}",
            path.strip_prefix(root).unwrap_or(&path).display()
        );
        let name = path
            .file_name()
            .unwrap_or_default()
            .to_string_lossy()
            .to_ascii_lowercase();
        // Only pluxy's own test names: bundled system libraries may say "secret" or "-test".
        if name.contains("pluxy") && name.contains("test") {
            return Err(format!("{what}: test file name in package"));
        }
        let meta = fs::symlink_metadata(&path).map_err(|e| format!("{what}: {e}"))?;
        if meta.is_dir() {
            found |= scan_tree(root, &path, appimage)?;
        } else if meta.is_symlink() {
            // Never followed: its target is checked as text.
            let target = fs::read_link(&path).map_err(|e| format!("{what}: {e}"))?;
            let target = target.to_string_lossy().to_ascii_lowercase();
            if target.contains("pluxy") && target.contains("test") {
                return Err(format!("{what}: link to a test file name in package"));
            }
        } else if meta.is_file() {
            let file = fs::File::open(&path).map_err(|e| format!("{what}: {e}"))?;
            found |= scan_stream(file, &what)?;
            if name.ends_with(".json") && meta.len() <= 64 * 1024 {
                let bytes = fs::read(&path).map_err(|e| format!("{what}: {e}"))?;
                // Only trust-shaped JSON parses (closed schema); it must be empty.
                if serde_json::from_slice::<TrustConfig>(&bytes).is_ok() {
                    empty_trust(&bytes).map_err(|e| format!("{what}: {e}"))?;
                }
            }
        } else {
            return Err(format!("{what}: not a regular file, folder or link"));
        }
    }
    Ok(found)
}
