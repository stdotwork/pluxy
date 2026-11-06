// Declares every app command in the app's command manifest. With a manifest, Tauri checks each
// command call against the capabilities, also from the app's own pages; without one, a webview
// with no capability on the app's origin could call any command.
include!("src/command_names.rs");

fn main() {
    tauri_build::try_build(
        tauri_build::Attributes::new()
            .app_manifest(tauri_build::AppManifest::new().commands(COMMANDS)),
    )
    .expect("tauri build step");
}
