//! Where a page webview sits over the window. On Windows and macOS a window's child webviews have
//! positions of their own. On Linux, Tauri packs them one under another in the window's box and
//! ignores their bounds, so the app moves the window's webview into an overlay and lays each page
//! webview over it, at the page area's place. GTK gives an overlay child its natural size (for a
//! webview, its document's) unless the overlay's `get-child-position` handler sets its place, so
//! the app's handler gives each page webview exactly the place the app put it at. Runs on the main
//! thread (sync commands do).

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
    use gtk::glib::{self, translate::from_glib_borrow};
    use gtk::prelude::*;
    use std::cell::RefCell;
    use std::collections::HashMap;

    /// A place in the overlay, in whole pixels: x, y, width, height.
    type Place = (i32, i32, i32, i32);

    thread_local! {
        /// The page webviews' widgets and places, by label. GTK widgets live on the main thread
        /// only.
        static PLACED: RefCell<HashMap<String, (gtk::Widget, Place)>> = RefCell::new(HashMap::new());
    }

    fn place_of(b: Bounds) -> Place {
        (
            b.x.max(0.0).round() as i32,
            b.y.max(0.0).round() as i32,
            b.width.max(1.0).round() as i32,
            b.height.max(1.0).round() as i32,
        )
    }

    /// Puts w at p, or hides it when there is no p.
    fn put(w: &gtk::Widget, p: Option<Place>) {
        let Some(p) = p else {
            w.hide();
            return;
        };
        // Its minimum too: GTK warns when it gives a widget less than that.
        w.set_size_request(p.2, p.3);
        // The overlay lays it out again, at its new place (child_position).
        w.queue_resize();
        w.show();
    }

    /// The overlay's `get-child-position` handler: a page webview's place is the one the app put
    /// it at, whatever size its document asks for. Other children keep GTK's default.
    unsafe extern "C" fn child_position(
        _: *mut gtk::ffi::GtkOverlay,
        widget: *mut gtk::ffi::GtkWidget,
        allocation: *mut gtk::gdk::ffi::GdkRectangle,
        _: glib::ffi::gpointer,
    ) -> glib::ffi::gboolean {
        let widget: glib::translate::Borrowed<gtk::Widget> = unsafe { from_glib_borrow(widget) };
        let place = PLACED.with(|p| {
            p.try_borrow()
                .ok()?
                .values()
                .find(|(w, _)| *w == *widget)
                .map(|(_, place)| *place)
        });
        match place {
            Some((x, y, width, height)) => {
                // GTK passes a rectangle for the handler to fill.
                let a = unsafe { &mut *allocation };
                (a.x, a.y, a.width, a.height) = (x, y, width, height);
                glib::ffi::GTRUE
            }
            None => glib::ffi::GFALSE,
        }
    }

    /// Moves the page webview just added to window's box over the window's webview.
    pub fn adopt(window: &tauri::Window, label: &str, b: Option<Bounds>) -> Result<(), String> {
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
                // SAFETY: child_position has the signal's signature, and the closure is a
                // zero-sized box that connect_raw frees when the handler goes.
                unsafe {
                    glib::signal::connect_raw::<()>(
                        o.as_ptr().cast(),
                        c"get-child-position".as_ptr(),
                        Some(std::mem::transmute::<*const (), unsafe extern "C" fn()>(
                            child_position as *const (),
                        )),
                        Box::into_raw(Box::new(())),
                    );
                }
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
        // All of the place child_position gives it, with no margins or alignment inside.
        page.set_halign(gtk::Align::Fill);
        page.set_valign(gtk::Align::Fill);
        // A page placed hidden keeps a place of one pixel until it is placed.
        let place = place_of(b.unwrap_or(Bounds {
            x: 0.0,
            y: 0.0,
            width: 1.0,
            height: 1.0,
        }));
        PLACED.with(|p| {
            p.borrow_mut()
                .insert(label.to_string(), (page.clone(), place))
        });
        overlay.add_overlay(&page);
        put(&page, b.map(|_| place));
        Ok(())
    }

    pub fn place(label: &str, b: Option<Bounds>) -> Result<(), String> {
        let placed = PLACED.with(|p| {
            let mut p = p.borrow_mut();
            let (w, place) = p.get_mut(label)?;
            // Hidden, it keeps its last place.
            if let Some(b) = b {
                *place = place_of(b);
            }
            Some((w.clone(), b.map(|_| *place)))
        });
        match placed {
            Some((w, place)) => {
                put(&w, place);
                Ok(())
            }
            None => Err(format!("no page webview {label}")),
        }
    }

    pub fn forget(label: &str) {
        PLACED.with(|p| p.borrow_mut().remove(label));
    }

    #[cfg(feature = "native-test")]
    pub fn allocation(label: &str) -> Option<Bounds> {
        PLACED.with(|p| {
            let p = p.borrow();
            let (w, _) = p.get(label).filter(|(w, _)| w.is_visible())?;
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

    /// The height the page webview asks for itself, without its margins: for a webview, its
    /// document's content height. WebKit does not tell GTK when that changes, so GTK is made to
    /// measure it again first (as any new layout would).
    #[cfg(feature = "native-test")]
    pub fn natural_height(label: &str) -> Option<f64> {
        PLACED.with(|p| {
            let p = p.borrow();
            let (w, _) = p.get(label)?;
            w.queue_resize();
            let (_, natural) = w.preferred_height();
            Some((natural - w.margin_top() - w.margin_bottom()) as f64)
        })
    }

    pub fn forget_all() {
        PLACED.with(|p| p.borrow_mut().clear());
    }
}

/// Puts the page webview labelled label, just added to window, at b; none hides it.
pub fn adopt(window: &Window, label: &str, b: Option<Bounds>) -> Result<(), String> {
    #[cfg(target_os = "linux")]
    return gtk_overlay::adopt(window, label, b);
    #[cfg(not(target_os = "linux"))]
    {
        use tauri::Manager as _;
        match (b, window.get_webview(label)) {
            (None, Some(w)) => w.hide().map_err(|e| e.to_string()),
            _ => Ok(()),
        }
    }
}

/// Moves the page webview to b; none hides it until it is placed again.
pub fn place(webview: &Webview, b: Option<Bounds>) -> Result<(), String> {
    #[cfg(target_os = "linux")]
    return gtk_overlay::place(webview.label(), b);
    #[cfg(not(target_os = "linux"))]
    {
        let Some(b) = b else {
            return webview.hide().map_err(|e| e.to_string());
        };
        webview
            .set_bounds(tauri::Rect {
                position: tauri::LogicalPosition::new(b.x.max(0.0), b.y.max(0.0)).into(),
                size: tauri::LogicalSize::new(b.width.max(1.0), b.height.max(1.0)).into(),
            })
            .and_then(|_| webview.show())
            .map_err(|e| e.to_string())
    }
}

/// Drops what the app holds of a page webview that closed.
pub fn forget(label: &str) {
    #[cfg(target_os = "linux")]
    gtk_overlay::forget(label);
    #[cfg(not(target_os = "linux"))]
    let _ = label;
}

/// Where the page webview labelled label actually is, in the window's overlay, if it shows
/// (Linux only).
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

/// The page webview's own height request, its document's height (Linux only).
#[cfg(feature = "native-test")]
pub fn natural_height(label: &str) -> Option<f64> {
    #[cfg(target_os = "linux")]
    return gtk_overlay::natural_height(label);
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
