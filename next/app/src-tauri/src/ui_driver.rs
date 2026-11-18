//! How the native test finds, reads and works the app's native dialogs (key entry, the rotated
//! key's window, the confirmation, its settings and the launch notice), the same on every
//! platform, so `key_entry_test.rs`, `rotation_test.rs` and `confirmation_native.rs` run one test
//! body on Linux, Windows and macOS. Every function is called from the test's own thread, never
//! the main thread.
//!
//! Input goes where a person's would, as close to the keyboard as each platform allows:
//! - Linux (`ui_driver_linux.rs`): XTEST through `xdotool`, secret bytes on its stdin;
//! - Windows (`ui_driver_windows.rs`): `SendInput` when the dialog has the foreground, otherwise
//!   the same key messages posted to its focused control; either way through the app's message
//!   loop, the dialog hook, `IsDialogMessageW` and the controls;
//! - macOS (`ui_driver_macos.rs`): key and mouse events made in the app and given to AppKit's
//!   own dispatch (the application when the panel is the key window, otherwise the panel); an
//!   access key is a click on its button, as macOS has none.
//!
//! Each input says which way it went; `input_counts` gives the totals for the session's log.
use serde_json::Value;
use std::sync::atomic::{AtomicUsize, Ordering};
use tauri::AppHandle;

#[cfg(target_os = "linux")]
#[path = "ui_driver_linux.rs"]
mod platform;
#[cfg(target_os = "macos")]
#[path = "ui_driver_macos.rs"]
mod platform;
#[cfg(windows)]
#[path = "ui_driver_windows.rs"]
mod platform;

#[cfg(not(target_os = "linux"))]
pub(crate) use platform::close_box;
pub(crate) use platform::{
    cancel_focused, clipboard, count, fixture_button, press, secret_hidden, texts, type_text,
};
#[cfg(windows)]
pub(crate) use platform::{post_click, uia_canary};

/// A key a person presses in a dialog.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Key {
    /// Alt and the letter (GTK's and Windows' access keys; a click on the button on macOS).
    Access(char),
    Escape,
    Tab,
}

/// How many inputs went each way: [(way, count)]. On macOS, `performClick` counts the clicks
/// whose mouse events did not reach the control (`dialog_macos::inspect::click`).
static WAYS: [(&str, AtomicUsize); 6] = [
    ("xtest", AtomicUsize::new(0)),
    ("sendinput", AtomicUsize::new(0)),
    ("posted", AtomicUsize::new(0)),
    ("application", AtomicUsize::new(0)),
    ("window", AtomicUsize::new(0)),
    ("performClick", AtomicUsize::new(0)),
];

/// How many access-key presses found their control disabled and waited for it (`until_enabled`).
static WAITED: AtomicUsize = AtomicUsize::new(0);

/// How long a press waits for its control to be enabled: a page's Confirm is disabled for its
/// first second.
const ENABLED_WITHIN: std::time::Duration = std::time::Duration::from_secs(10);

/// Waits until the control with access key c in the dialog titled title is enabled, as
/// `enabled` (the platform's) reads it: a press on a disabled control is dropped (it answers
/// nothing, and the dialog waits out its lifetime), so a press must not go before. Fails naming
/// the control when it stays disabled for ENABLED_WITHIN, or when no control has that key.
fn until_enabled(
    title: &str,
    c: char,
    enabled: impl Fn() -> Result<Option<bool>, String>,
) -> Result<(), String> {
    let deadline = std::time::Instant::now() + ENABLED_WITHIN;
    let mut waited = false;
    loop {
        match enabled()? {
            Some(true) => break,
            Some(false) if std::time::Instant::now() < deadline => {
                waited = true;
                std::thread::sleep(std::time::Duration::from_millis(50));
            }
            Some(false) => {
                return Err(format!(
                    "the control with access key {c} in {title:?} stayed disabled for {} s",
                    ENABLED_WITHIN.as_secs()
                ))
            }
            None => return Err(format!("no control in {title:?} has the access key {c}")),
        }
    }
    if waited {
        WAITED.fetch_add(1, Ordering::SeqCst);
    }
    Ok(())
}

/// How many access-key presses so far waited for their control to be enabled.
pub(crate) fn waited() -> usize {
    WAITED.load(Ordering::SeqCst)
}

fn went(way: &str) {
    if let Some((_, n)) = WAYS.iter().find(|(w, _)| *w == way) {
        n.fetch_add(1, Ordering::SeqCst);
    }
}

/// The inputs each way so far, for the session's `input` step.
pub(crate) fn input_counts() -> Value {
    let mut m = serde_json::Map::new();
    for (way, n) in &WAYS {
        m.insert((*way).into(), n.load(Ordering::SeqCst).into());
    }
    m.insert("waited_for_enabled".into(), waited().into());
    m.insert("platform".into(), std::env::consts::OS.into());
    Value::Object(m)
}

/// Whether a dialog titled title is shown.
pub(crate) fn shown(app: &AppHandle, title: &str) -> bool {
    count(app, title) > 0
}

/// Waits for a dialog titled title.
pub(crate) fn wait_shown(app: &AppHandle, title: &str) -> Result<(), String> {
    super::wait_for(title, || shown(app, title).then_some(()))
}
