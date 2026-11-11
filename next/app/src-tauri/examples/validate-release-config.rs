//! Run on the final merged Tauri config and staged package inputs before packaging/publishing.
#[cfg(debug_assertions)]
use pluxy_app::release_config::{validate, ReleaseInputs};
#[cfg(debug_assertions)]
use std::path::PathBuf;
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
    match validate(&inputs) {
        Ok(()) => {
            println!("release configuration valid: native-tier and updater trust not configured")
        }
        Err(error) => {
            eprintln!("release configuration refused: {error}");
            std::process::exit(1);
        }
    }
}

#[cfg(not(debug_assertions))]
fn main() {
    eprintln!("run release validation with the debug tooling build");
    std::process::exit(2);
}
