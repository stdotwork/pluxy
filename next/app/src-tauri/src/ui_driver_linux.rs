//! The UI driver on Linux: XTEST input through `xdotool`, and the GTK widgets read on the GTK
//! thread (what the native tests did before the driver).
use super::{went, Key};
use crate::native_test::{on_main, wait_for};
use gtk::prelude::*;
use std::process::{Command, Stdio};
use tauri::AppHandle;

fn xdotool(args: &[&str]) -> Result<String, String> {
    let out = Command::new("xdotool")
        .args(args)
        .output()
        .map_err(|_| "could not run xdotool")?;
    if !out.status.success() {
        return Err(format!(
            "xdotool {args:?}: {}",
            String::from_utf8_lossy(&out.stderr)
        ));
    }
    Ok(String::from_utf8_lossy(&out.stdout).trim().to_string())
}

/// The X windows titled title that are shown.
fn found(title: &str) -> Vec<String> {
    xdotool(&["search", "--onlyvisible", "--name", &format!("^{title}$")])
        .map(|s| {
            s.lines()
                .filter(|l| !l.is_empty())
                .map(String::from)
                .collect()
        })
        .unwrap_or_default()
}

pub(crate) fn count(_app: &AppHandle, title: &str) -> usize {
    found(title).len()
}

/// Focuses the last shown window titled title that xdotool lists, once there is one (as the
/// native tests did before the driver: GTK may leave a window it closed listed for a moment).
fn focus(title: &str) -> Result<(), String> {
    let id = wait_for(title, || found(title).pop())?;
    xdotool(&["windowfocus", "--sync", &id]).map(|_| ())
}

/// The access key (mnemonic) of a GTK label: the character after its single `_`.
fn mnemonic(label: &str) -> Option<char> {
    let mut chars = label.chars();
    while let Some(ch) = chars.next() {
        if ch == '_' {
            match chars.next() {
                Some('_') => {}
                other => return other,
            }
        }
    }
    None
}

/// Whether the button or check box of the dialog titled title with access key c is sensitive;
/// None when no control has that key.
fn access_enabled(app: &AppHandle, title: &str, c: char) -> Result<Option<bool>, String> {
    let title = title.to_string();
    on_main(app, move || {
        let mut found = None;
        for w in windows(&title) {
            walk(w.upcast_ref(), &mut |w| {
                if let Some(b) = w.downcast_ref::<gtk::Button>() {
                    let key = b
                        .label()
                        .filter(|_| b.uses_underline())
                        .and_then(|l| mnemonic(&l));
                    if key.is_some_and(|k| k.eq_ignore_ascii_case(&c)) {
                        found = Some(b.is_sensitive());
                    }
                }
            });
        }
        found
    })
    .ok_or_else(|| "the main thread did not answer".into())
}

pub(crate) fn press(app: &AppHandle, title: &str, key: Key) -> Result<(), String> {
    if let Key::Access(c) = key {
        super::until_enabled(title, c, || access_enabled(app, title, c))?;
    }
    focus(title)?;
    let keys = match key {
        Key::Access(c) => format!("alt+{c}"),
        Key::Escape => "Escape".into(),
        Key::Tab => "Tab".into(),
    };
    xdotool(&["key", "--clearmodifiers", &keys])?;
    went("xtest");
    Ok(())
}

/// Types text into the focused entry. The bytes go to xdotool on stdin, never in its arguments.
pub(crate) fn type_text(_app: &AppHandle, title: &str, text: &str) -> Result<(), String> {
    use std::io::Write;
    focus(title)?;
    let mut child = Command::new("xdotool")
        .args(["type", "--clearmodifiers", "--file", "-"])
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|_| "could not start native input")?;
    child
        .stdin
        .take()
        .ok_or("no input pipe")?
        .write_all(text.as_bytes())
        .map_err(|_| "could not type native input")?;
    if !child.wait().map_err(|_| "native input failed")?.success() {
        return Err("native input failed".into());
    }
    went("xtest");
    Ok(())
}

/// The shown GTK windows titled title.
fn windows(title: &str) -> Vec<gtk::Window> {
    gtk::Window::list_toplevels()
        .into_iter()
        .filter_map(|w| w.downcast::<gtk::Window>().ok())
        .filter(|w| w.title().as_deref() == Some(title) && w.is_visible())
        .collect()
}

fn walk(widget: &gtk::Widget, f: &mut dyn FnMut(&gtk::Widget)) {
    f(widget);
    if let Some(container) = widget.downcast_ref::<gtk::Container>() {
        for child in container.children() {
            walk(&child, f);
        }
    }
}

pub(crate) fn texts(app: &AppHandle, title: &str) -> Result<Vec<String>, String> {
    let title = title.to_string();
    on_main(app, move || {
        windows(&title)
            .into_iter()
            .map(|w| {
                let mut out = Vec::new();
                walk(w.upcast_ref(), &mut |c| {
                    if let Some(label) = c.downcast_ref::<gtk::Label>() {
                        out.push(label.text().to_string());
                    }
                });
                out.join("\n")
            })
            .collect()
    })
    .ok_or_else(|| "no native window audit".into())
}

pub(crate) fn secret_hidden(app: &AppHandle, title: &str) -> Result<bool, String> {
    let title = title.to_string();
    on_main(app, move || {
        let mut hidden = false;
        for w in windows(&title) {
            walk(w.upcast_ref(), &mut |c| {
                if let Some(entry) = c.downcast_ref::<gtk::Entry>() {
                    hidden = !EntryExt::is_visible(entry);
                }
            });
        }
        hidden
    })
    .ok_or_else(|| "could not inspect the native entry".into())
}

/// The clipboard's text; Linux has no concealed marker to read (None).
pub(crate) fn clipboard(app: &AppHandle) -> (Option<String>, Option<bool>) {
    let text = on_main(app, || {
        gtk::Clipboard::get(&gtk::gdk::SELECTION_CLIPBOARD)
            .wait_for_text()
            .map(|s| s.to_string())
    })
    .flatten();
    (text, None)
}

/// Whether the Cancel answer's button of the dialog titled title has the focus.
pub(crate) fn cancel_focused(app: &AppHandle, title: &str) -> Result<bool, String> {
    let title = title.to_string();
    on_main(app, move || {
        windows(&title)
            .into_iter()
            .find_map(|w| w.downcast::<gtk::Dialog>().ok())
            .and_then(|d| d.widget_for_response(gtk::ResponseType::Cancel))
            .is_some_and(|b| b.is_focus())
    })
    .ok_or_else(|| "could not inspect the native dialog".into())
}

/// Shows a native window titled title with one button, `_Start`, and presses it with Alt+S: its
/// click handler, on the main thread, calls f, as a trusted native launcher's would.
pub(crate) fn fixture_button(
    app: &AppHandle,
    title: &str,
    f: impl FnOnce() + Send + 'static,
) -> Result<(), String> {
    let name = title.to_string();
    let f = std::sync::Mutex::new(Some(f));
    app.run_on_main_thread(move || {
        let w = gtk::Window::new(gtk::WindowType::Toplevel);
        w.set_title(&name);
        let button = gtk::Button::with_mnemonic("_Start");
        w.add(&button);
        let weak = w.downgrade();
        button.connect_clicked(move |_| {
            if let Some(f) = f.lock().unwrap().take() {
                f();
            }
            if let Some(w) = weak.upgrade() {
                w.close();
            }
        });
        w.show_all();
    })
    .map_err(|e| e.to_string())?;
    press(app, title, Key::Access('s'))
}
