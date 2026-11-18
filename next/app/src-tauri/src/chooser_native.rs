//! How the native test's sessions enter a path in a GTK file chooser (Linux only): one helper
//! for the open choosers (access, device) and the save dialogs (save, sessions).

use super::device::{require, x};
use super::{on_main, wait_for};
use std::sync::mpsc;
use tauri::AppHandle;

/// Enters text in the entry of the chooser whose X window is id, and accepts it. select is the
/// key that puts the cursor in the entry with its text selected (`ctrl+l` opens an open
/// chooser's location entry, `ctrl+a` selects a save dialog's name).
///
/// The text is pasted, in one insert, not typed. Typed a key at a time, the entry completes
/// each folder it reaches inline, the rest of the name selected for the next key to replace; if
/// the entry loses focus at that moment, GTK keeps the completion and moves the cursor after it
/// (gtkentrycompletion.c `accept_completion_callback`), and the keys still to come repeat it
/// (`/tmp/p` became `/tmp/pluxy-app-native-luxy-app-native-…`), so the chooser never answers.
/// The app owns the clipboard, and Return goes only once the chooser has asked it for the text,
/// so the text is in the entry before Return arrives.
pub(super) fn enter(app: &AppHandle, id: &str, select: &str, text: String) -> Result<(), String> {
    let asked = offer(app, text)?;
    x(&["windowfocus", "--sync", id])?;
    x(&["key", "--clearmodifiers", select])?;
    std::thread::sleep(std::time::Duration::from_millis(300));
    x(&["key", "--clearmodifiers", "ctrl+v"])?;
    wait_for("the chooser to ask for the pasted text", || {
        asked.try_recv().ok()
    })?;
    // The answer goes to the X server from the main thread: a round trip there sends it before
    // Return, so the chooser reads it first.
    on_main(app, || {
        if let Some(d) = gtk::gdk::Display::default() {
            d.sync();
        }
    })
    .ok_or("the main thread did not answer")?;
    x(&["key", "--clearmodifiers", "Return"])?;
    Ok(())
}

/// Puts text on the clipboard, owned by this app. The receiver gets a message each time a
/// program asks for it.
fn offer(app: &AppHandle, text: String) -> Result<mpsc::Receiver<()>, String> {
    let (tx, rx) = mpsc::channel();
    let set = on_main(app, move || {
        let targets: Vec<gtk::TargetEntry> = [
            "UTF8_STRING",
            "text/plain;charset=utf-8",
            "STRING",
            "TEXT",
            "text/plain",
        ]
        .iter()
        .map(|t| gtk::TargetEntry::new(t, gtk::TargetFlags::empty(), 0))
        .collect();
        gtk::Clipboard::get(&gtk::gdk::SELECTION_CLIPBOARD).set_with_data(
            &targets,
            move |_, data, _| {
                data.set_text(&text);
                let _ = tx.send(());
            },
        )
    });
    require(set == Some(true), "the clipboard was not set")?;
    Ok(rx)
}
