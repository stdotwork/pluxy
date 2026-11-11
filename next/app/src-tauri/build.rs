// Declares every app command in the app's command manifest. With a manifest, Tauri checks each
// command call against the capabilities, also from the app's own pages; without one, a webview
// with no capability on the app's origin could call any command.
include!("src/command_names.rs");

mod build_config;

fn main() {
    assert!(
        std::env::var_os("CARGO_FEATURE_NATIVE_TEST").is_none()
            || std::env::var("PROFILE").as_deref() == Ok("debug"),
        "native-test fixture actions must never be built in a release profile"
    );
    use sha2::{Digest, Sha256};
    // Package tooling validates these snapshots from the same OUT_DIR as the app build.
    let out = std::path::PathBuf::from(std::env::var_os("OUT_DIR").expect("cargo OUT_DIR"));
    println!("cargo:rerun-if-changed=trust/production.json");
    std::fs::copy("trust/production.json", out.join("app-package-trust.json"))
        .expect("write compiled trust snapshot");
    let mut features: Vec<_> = std::env::vars()
        .filter_map(|(name, _)| {
            name.strip_prefix("CARGO_FEATURE_")
                .map(|s| s.to_ascii_lowercase().replace('_', "-"))
        })
        .collect();
    features.sort();
    // Cargo feature names contain only identifier characters, so no JSON escapes are needed.
    let json = format!(
        "[{}]",
        features
            .iter()
            .map(|s| format!("\"{s}\""))
            .collect::<Vec<_>>()
            .join(",")
    );
    std::fs::write(out.join("app-cargo-features.json"), &json).expect("write feature snapshot");
    println!("cargo:rerun-if-env-changed=TAURI_CONFIG");
    println!("cargo:rerun-if-changed=tauri.conf.json");
    println!("cargo:rerun-if-changed=tauri.linux.conf.json");
    let config = build_config::merged(
        &std::env::current_dir().unwrap(),
        &std::env::var("TARGET").unwrap(),
        std::env::var("TAURI_CONFIG").ok().as_deref(),
    )
    .expect("merged Tauri config");
    let config = serde_json::to_vec(&config).unwrap();
    std::fs::write(out.join("app-tauri-config.json"), &config).expect("write merged config");
    let mut digest = Sha256::new();
    for bytes in [
        &config[..],
        include_bytes!("trust/production.json").as_slice(),
        json.as_bytes(),
    ] {
        digest.update((bytes.len() as u64).to_le_bytes());
        digest.update(bytes);
    }
    let hash: String = digest
        .finalize()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect();
    let receipt = format!("PLUXY_BUILD_RECEIPT_V1:{hash}");
    let generated = format!(
        r#"#[used]
#[no_mangle]
pub static PLUXY_BUILD_RECEIPT: [u8; {}] = *b"{}";
"#,
        receipt.len(),
        receipt
    );
    std::fs::write(out.join("build_receipt.rs"), generated).unwrap();
    println!("cargo:rustc-env=PLUXY_BUILD_RECEIPT={receipt}");
    tauri_build::try_build(
        tauri_build::Attributes::new()
            .app_manifest(tauri_build::AppManifest::new().commands(COMMANDS)),
    )
    .expect("tauri build step");
}
