//! Run on the final merged Tauri config and staged package inputs before packaging/publishing.
#[cfg(debug_assertions)]
use pluxy_app::release_config::{validate, ReleaseInputs};
#[cfg(debug_assertions)]
use std::path::PathBuf;
#[cfg(debug_assertions)]
#[path = "../build_config.rs"]
mod build_config;
#[cfg(debug_assertions)]
fn main() {
    let args: Vec<PathBuf> = std::env::args_os().skip(1).map(PathBuf::from).collect();
    if args.len() < 6 {
        eprintln!("usage: validate-release-config FINAL_TAURI_JSON COMPILED_TRUST_JSON PACKAGED_TRUST_JSON FEATURES_JSON BUNDLED_TRUST_DIR ARTIFACT_PATH...");
        std::process::exit(2);
    }
    let inputs = ReleaseInputs {
        tauri_config: args[0].clone(),
        compiled_trust: args[1].clone(),
        packaged_trust: args[2].clone(),
        features: args[3].clone(),
        bundled_trust: args[4].clone(),
        artifacts: args[5..].to_vec(),
    };
    match validate(&inputs).and_then(|()| current(&inputs.tauri_config)) {
        Ok(()) => {
            println!("release configuration valid: native-tier and updater trust not configured")
        }
        Err(error) => {
            eprintln!("release configuration refused: {error}");
            std::process::exit(1);
        }
    }
}

/// The snapshot must also match the config files as they are now (build.rs's merge, with
/// TAURI_CONFIG from this environment): Cargo reruns the build script only when a config file it
/// read changes, so a build that predates a new platform file still has the old snapshot.
#[cfg(debug_assertions)]
fn current(snapshot: &std::path::Path) -> Result<(), String> {
    let snapshot = std::fs::read(snapshot).map_err(|e| format!("{}: {e}", snapshot.display()))?;
    let snapshot = serde_json::from_slice(&snapshot).map_err(|e| format!("Tauri config: {e}"))?;
    // The merge reads only the system from a target triple; the release build is a host build,
    // like this one.
    let target = if cfg!(target_os = "macos") {
        "apple-darwin"
    } else if cfg!(target_os = "windows") {
        "pc-windows"
    } else {
        "unknown-linux"
    };
    build_config::check_current(
        &snapshot,
        std::path::Path::new(env!("CARGO_MANIFEST_DIR")),
        target,
        std::env::var("TAURI_CONFIG").ok().as_deref(),
    )
}

#[cfg(not(debug_assertions))]
fn main() {
    eprintln!("run release validation with the debug tooling build");
    std::process::exit(2);
}
