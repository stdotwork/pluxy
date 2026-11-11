// Declares every app command in the app's command manifest. With a manifest, Tauri checks each
// command call against the capabilities, also from the app's own pages; without one, a webview
// with no capability on the app's origin could call any command.
include!("src/command_names.rs");

fn main() {
    assert!(
        std::env::var_os("CARGO_FEATURE_NATIVE_TEST").is_none()
            || std::env::var("PROFILE").as_deref() == Ok("debug"),
        "native-test fixture actions must never be built in a release profile"
    );
    tauri_build::try_build(
        tauri_build::Attributes::new()
            .app_manifest(tauri_build::AppManifest::new().commands(COMMANDS)),
    )
    .expect("tauri build step");
}
