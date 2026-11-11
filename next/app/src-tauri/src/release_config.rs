//! Validate final release inputs, not a source config before Tauri merges overrides.
use crate::trust::{read_bounded, TrustConfig, COMPILED_TEST_TRUST, PRODUCTION_TRUST};
use serde_json::Value;
use std::io::Read;
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

// Stream with overlap so markers split across chunks are still detected; no artifact-sized alloc.
fn scan_artifact(path: &Path) -> Result<bool, String> {
    let meta =
        fs::symlink_metadata(path).map_err(|e| format!("artifact missing/unreadable: {e}"))?;
    if !meta.is_file() {
        return Err("artifact must be a regular file".into());
    }
    let forbidden: [&[u8]; 3] = [
        b"PLUXY_TEST_BUILD_DO_NOT_RELEASE",
        b"dev.pluxy.desktop.test",
        b"pluxy Test",
    ];
    let receipt = env!("PLUXY_BUILD_RECEIPT").as_bytes();
    let overlap = receipt
        .len()
        .max(forbidden.iter().map(|m| m.len()).max().unwrap())
        - 1;
    let mut file = fs::File::open(path).map_err(|e| e.to_string())?;
    let mut window = Vec::new();
    let mut buffer = [0; 64 * 1024];
    let mut found = false;
    loop {
        let n = file.read(&mut buffer).map_err(|e| e.to_string())?;
        if n == 0 {
            break;
        }
        window.extend_from_slice(&buffer[..n]);
        if forbidden
            .iter()
            .any(|m| window.windows(m.len()).any(|w| w == *m))
        {
            return Err("test build marker/identity in artifact".into());
        }
        found |= window.windows(receipt.len()).any(|w| w == receipt);
        if window.len() > overlap {
            window.drain(..window.len() - overlap);
        }
    }
    Ok(found)
}
