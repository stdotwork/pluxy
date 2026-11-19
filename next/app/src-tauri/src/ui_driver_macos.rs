//! The UI driver on macOS: the dialogs are read on the main thread (`dialog_macos::inspect`);
//! keys and clicks are events made in the app and given to AppKit's own dispatch. macOS has no
//! access keys, so `Key::Access` clicks the button (or check box) whose GTK label has that key.
use super::{went, Key};
use crate::dialog::{After, Spec};
use crate::dialog_macos::{self as dm, inspect};
use crate::native_test::{on_main, wait_for};
use objc2::MainThreadMarker;
use tauri::AppHandle;

/// Virtual key codes (Carbon `kVK_*`).
const ESCAPE: u16 = 53;
const TAB: u16 = 48;
const ANSI_A: u16 = 0;

pub(crate) fn count(app: &AppHandle, title: &str) -> usize {
    let title = title.to_string();
    on_main(app, move || inspect::all(&title).len()).unwrap_or(0)
}

/// The one open dialog titled title, once there is exactly one.
fn one(app: &AppHandle, title: &str) -> Result<u64, String> {
    wait_for(title, || {
        let t = title.to_string();
        on_main(app, move || {
            let all = inspect::all(&t);
            (all.len() == 1).then(|| all[0])
        })
        .flatten()
    })
}

fn mtm() -> Result<MainThreadMarker, String> {
    MainThreadMarker::new().ok_or_else(|| "not on the main thread".into())
}

pub(crate) fn press(app: &AppHandle, title: &str, key: Key) -> Result<(), String> {
    let d = one(app, title)?;
    if let Key::Access(c) = key {
        super::until_enabled(title, c, || {
            on_main(app, move || inspect::access_enabled(d, c))
                .ok_or_else(|| "the main thread did not answer".to_string())
        })?;
    }
    let way = on_main(app, move || -> Result<&'static str, String> {
        let mtm = mtm()?;
        let way = match key {
            Key::Access(c) => {
                let target = inspect::access_keys(d)
                    .into_iter()
                    .find(|(k, _)| k.eq_ignore_ascii_case(&c))
                    .ok_or(format!("no control has the access key {c}"))?;
                inspect::click(mtm, d, target.1)
            }
            Key::Escape => inspect::key_press(mtm, d, ESCAPE, "\u{1b}"),
            Key::Tab => inspect::key_press(mtm, d, TAB, "\t"),
        };
        way.ok_or_else(|| "could not make the input event".into())
    })
    .ok_or("the main thread did not answer")??;
    went(way);
    // The click's mouse up was posted to the queue: let the main thread take it.
    let _ = on_main(app, || ());
    Ok(())
}

pub(crate) fn type_text(app: &AppHandle, title: &str, text: &str) -> Result<(), String> {
    let d = one(app, title)?;
    let chars: Vec<String> = text.chars().map(String::from).collect();
    let n = chars.len();
    let (way, before) = on_main(app, move || -> Result<(&'static str, usize), String> {
        let mtm = mtm()?;
        let before = inspect::secret_len(d).ok_or("the dialog has no hidden entry")?;
        let mut way = "";
        for c in &chars {
            way = inspect::key_press(mtm, d, ANSI_A, c).ok_or("could not make a key event")?;
        }
        Ok((way, before))
    })
    .ok_or("the main thread did not answer")??;
    went(way);
    wait_for("the typed key in the native entry", || {
        on_main(app, move || inspect::secret_len(d))
            .flatten()
            .filter(|len| *len == before + n)
    })
    .map(|_| ())
}

/// Closes the dialog titled title with its close box, as a click on it does (`performClose:`).
pub(crate) fn close_box(app: &AppHandle, title: &str) -> Result<(), String> {
    let d = one(app, title)?;
    on_main(app, move || inspect::perform_close(d))
        .ok_or("the main thread did not answer")?
        .then_some(())
        .ok_or_else(|| "the dialog has gone".into())
}

pub(crate) fn texts(app: &AppHandle, title: &str) -> Result<Vec<String>, String> {
    let title = title.to_string();
    on_main(app, move || {
        inspect::all(&title)
            .into_iter()
            .map(|d| inspect::texts(d).join("\n"))
            .collect()
    })
    .ok_or_else(|| "no native window audit".into())
}

pub(crate) fn secret_hidden(app: &AppHandle, title: &str) -> Result<bool, String> {
    let title = title.to_string();
    on_main(app, move || {
        inspect::all(&title).into_iter().any(inspect::secret_hidden)
    })
    .ok_or_else(|| "could not inspect the native entry".into())
}

/// The pasteboard's text, and whether it is marked concealed.
pub(crate) fn clipboard(app: &AppHandle) -> (Option<String>, Option<bool>) {
    on_main(app, inspect::clipboard)
        .map(|(text, concealed)| (text, Some(concealed)))
        .unwrap_or((None, None))
}

pub(crate) fn cancel_focused(app: &AppHandle, title: &str) -> Result<bool, String> {
    let title = title.to_string();
    on_main(app, move || {
        inspect::all(&title)
            .into_iter()
            .any(inspect::cancel_focused)
    })
    .ok_or_else(|| "could not inspect the native dialog".into())
}

/// Shows a native panel titled title with one button, Start, and clicks it: its press handler,
/// on the main thread, calls f, as a trusted native launcher's would.
pub(crate) fn fixture_button(
    app: &AppHandle,
    title: &str,
    f: impl FnOnce() + Send + 'static,
) -> Result<(), String> {
    let name = title.to_string();
    on_main(app, move || -> Result<(), String> {
        let mut f = Some(f);
        let mut spec = Spec::new(
            &name,
            Box::new(move |_, _| {
                if let Some(f) = f.take() {
                    f();
                }
                After::Close
            }),
        );
        spec.buttons = vec![(1, "_Start".into())];
        spec.default = Some(1);
        spec.focus = Some(1);
        dm::show(mtm()?, spec, None).map(|_| ())
    })
    .ok_or("the main thread did not answer")??;
    press(app, title, Key::Access('s'))
}
