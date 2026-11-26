//! Shared by build.rs, the release validator (examples/validate-release-config.rs) and their
//! tests. Match tauri-build's config loading exactly.
/// The merged config, and the config files it was read from (the base file, and the platform's
/// file when there is one).
pub fn merged(
    root: &std::path::Path,
    target: &str,
    overlay: Option<&str>,
) -> Result<(serde_json::Value, Vec<std::path::PathBuf>), Box<dyn std::error::Error>> {
    let (mut config, files) = tauri_utils::config::parse::read_from(
        tauri_utils::platform::Target::from_triple(target),
        root,
    )?;
    if let Some(overlay) = overlay {
        json_patch::merge(&mut config, &serde_json::from_str(overlay)?);
    }
    Ok((config, files))
}

/// Refuses a build's config snapshot (`app-tauri-config.json`) that differs from the config files
/// in `root` as they are now, merged as build.rs merges them: a build that missed a config change,
/// such as a platform file added after the build script last ran.
#[allow(dead_code)] // build.rs includes this file too, and does not call it.
pub fn check_current(
    snapshot: &serde_json::Value,
    root: &std::path::Path,
    target: &str,
    overlay: Option<&str>,
) -> Result<(), String> {
    let (current, _) =
        merged(root, target, overlay).map_err(|e| format!("source Tauri config: {e}"))?;
    if &current != snapshot {
        return Err(format!(
            "Tauri config snapshot differs from the config files in {}: the build is stale",
            root.display()
        ));
    }
    Ok(())
}
