// Declares every app command in the app's command manifest. With a manifest, Tauri checks each
// command call against the capabilities, also from the app's own pages; without one, a webview
// with no capability on the app's origin could call any command.
include!("src/command_names.rs");

fn main() {
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
    std::fs::write(out.join("app-cargo-features.json"), json).expect("write feature snapshot");
    tauri_build::try_build(
        tauri_build::Attributes::new()
            .app_manifest(tauri_build::AppManifest::new().commands(COMMANDS)),
    )
    .expect("tauri build step");
}
