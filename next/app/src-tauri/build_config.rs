//! Shared by build.rs and its merge-order test. Match tauri-build's config loading exactly.
pub fn merged(
    root: &std::path::Path,
    target: &str,
    overlay: Option<&str>,
) -> Result<serde_json::Value, Box<dyn std::error::Error>> {
    let (mut config, _) = tauri_utils::config::parse::read_from(
        tauri_utils::platform::Target::from_triple(target),
        root,
    )?;
    if let Some(overlay) = overlay {
        json_patch::merge(&mut config, &serde_json::from_str(overlay)?);
    }
    Ok(config)
}
