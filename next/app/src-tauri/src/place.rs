//! Where a page webview sits over the window. On Windows and macOS a window's child webviews have
//! positions of their own. On Linux, Tauri packs them one under another in the window's box and
//! ignores their bounds, so the app moves the window's webview into an overlay and lays each page
//! webview over it, at the page area's place. Runs on the main thread (sync commands do).

use tauri::{Webview, Window};

/// A place in the window, in logical pixels.
#[derive(Clone, Copy, Debug, serde::Serialize)]
pub struct Bounds {
    pub x: f64,
    pub y: f64,
    pub width: f64,
    pub height: f64,
}

#[cfg(target_os = "linux")]
mod gtk_overlay {
    use super::Bounds;
    use gtk::prelude::*;
    use std::cell::RefCell;
    use std::collections::HashMap;

    thread_local! {
        /// The page webviews' widgets, by label. GTK widgets live on the main thread only.
        static PLACED: RefCell<HashMap<String, gtk::Widget>> = RefCell::new(HashMap::new());
    }

    fn put(w: &gtk::Widget, b: Bounds) {
        w.set_margin_start(b.x.max(0.0).round() as i32);
        w.set_margin_top(b.y.max(0.0).round() as i32);
        w.set_size_request(
            b.width.max(1.0).round() as i32,
            b.height.max(1.0).round() as i32,
        );
    }

    /// Moves the page webview just added to window's box over the window's webview.
    pub fn adopt(window: &tauri::Window, label: &str, b: Bounds) -> Result<(), String> {
        let vbox = window.default_vbox().map_err(|e| e.to_string())?;
        let children = vbox.children();
        let overlay = match children
            .iter()
            .find_map(|c| c.clone().downcast::<gtk::Overlay>().ok())
        {
            Some(o) => o,
            None => {
                // The first page: the window's webview, the box's first child, goes under an
                // overlay that fills the box as it did.
                let main = children.first().ok_or("the window has no webview")?.clone();
                let o = gtk::Overlay::new();
                vbox.remove(&main);
                o.add(&main);
                vbox.pack_start(&o, true, true, 0);
                o.show();
                o
            }
        };
        let page = vbox
            .children()
            .into_iter()
            .find(|c| !c.is::<gtk::Overlay>())
            .ok_or("the page webview is not in the window")?;
        vbox.remove(&page);
        page.set_halign(gtk::Align::Start);
        page.set_valign(gtk::Align::Start);
        put(&page, b);
        overlay.add_overlay(&page);
        page.show();
        PLACED.with(|p| p.borrow_mut().insert(label.to_string(), page));
        Ok(())
    }

    pub fn place(label: &str, b: Bounds) -> Result<(), String> {
        PLACED.with(|p| match p.borrow().get(label) {
            Some(w) => {
                put(w, b);
                Ok(())
            }
            None => Err(format!("no page webview {label}")),
        })
    }

    pub fn forget(label: &str) {
        PLACED.with(|p| p.borrow_mut().remove(label));
    }

    #[cfg(feature = "native-test")]
    pub fn allocation(label: &str) -> Option<Bounds> {
        PLACED.with(|p| {
            let p = p.borrow();
            let w = p.get(label)?;
            let a = w.allocation();
            // Relative to the window, as the page area is.
            let (x, y) = w.translate_coordinates(&w.toplevel()?, 0, 0)?;
            Some(Bounds {
                x: x as f64,
                y: y as f64,
                width: a.width() as f64,
                height: a.height() as f64,
            })
        })
    }

    pub fn forget_all() {
        PLACED.with(|p| p.borrow_mut().clear());
    }
}

/// Puts the page webview labelled label, just added to window, at b.
pub fn adopt(window: &Window, label: &str, b: Bounds) -> Result<(), String> {
    #[cfg(target_os = "linux")]
    return gtk_overlay::adopt(window, label, b);
    #[cfg(not(target_os = "linux"))]
    {
        let _ = (window, label, b);
        Ok(())
    }
}

/// Moves the page webview to b.
pub fn place(webview: &Webview, b: Bounds) -> Result<(), String> {
    #[cfg(target_os = "linux")]
    return gtk_overlay::place(webview.label(), b);
    #[cfg(not(target_os = "linux"))]
    webview
        .set_bounds(tauri::Rect {
            position: tauri::LogicalPosition::new(b.x.max(0.0), b.y.max(0.0)).into(),
            size: tauri::LogicalSize::new(b.width.max(1.0), b.height.max(1.0)).into(),
        })
        .map_err(|e| e.to_string())
}

/// Drops what the app holds of a page webview that closed.
pub fn forget(label: &str) {
    #[cfg(target_os = "linux")]
    gtk_overlay::forget(label);
    #[cfg(not(target_os = "linux"))]
    let _ = label;
}

/// Where the page webview labelled label actually is, in the window's overlay (Linux only).
#[cfg(feature = "native-test")]
pub fn allocation(label: &str) -> Option<Bounds> {
    #[cfg(target_os = "linux")]
    return gtk_overlay::allocation(label);
    #[cfg(not(target_os = "linux"))]
    {
        let _ = label;
        None
    }
}

/// Drops everything, when the window closed.
pub fn forget_all() {
    #[cfg(target_os = "linux")]
    gtk_overlay::forget_all();
}
