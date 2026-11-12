//! What closing the window does, and the window's size and place, kept in `window.json` beside
//! `connection.json`, readable only by this user.
//!
//! Closing asks (Ask every time), quits (Exit App) or leaves the app in the tray (Minimize to
//! Tray), which destroys the window and its webviews. Without a usable tray, closing quits
//! whatever the choice, so the app is never left running with no way back to it.
//!
//! The size is kept in logical pixels and the place in physical ones (monitor layouts, including
//! displays left of the primary one), never while the window is minimized, maximized or full
//! screen. A window opens at the size and place it last had, fitted into the work area of the
//! monitor holding that place.

use crate::managed::write_private;
use serde::{Deserialize, Serialize};
use std::fs;
use std::path::{Path, PathBuf};
use tauri::{AppHandle, Window};

pub const DEFAULT_SIZE: (f64, f64) = (1100.0, 760.0);
pub const MIN_SIZE: (f64, f64) = (640.0, 480.0);

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Close {
    #[default]
    Ask,
    Exit,
    MinimizeToTray,
}

#[derive(Clone, Copy, Debug, PartialEq, Serialize, Deserialize)]
pub struct Geometry {
    pub width: f64,
    pub height: f64,
    pub x: i32,
    pub y: i32,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Preferences {
    pub close_behavior: Close,
    pub geometry: Option<Geometry>,
}

pub struct Store {
    file: PathBuf,
    pub prefs: Preferences,
}

impl Store {
    /// The saved preferences. A damaged file must not stop the app or be overwritten unnoticed:
    /// it is kept aside and the app starts with the defaults.
    pub fn open(file: PathBuf) -> Store {
        let prefs = match fs::read(&file) {
            Ok(bytes) => serde_json::from_slice(&bytes).unwrap_or_else(|e| {
                keep_aside(&file, &e.to_string());
                Preferences::default()
            }),
            Err(e) => {
                if e.kind() != std::io::ErrorKind::NotFound {
                    eprintln!("pluxy-app: cannot read {}: {e}", file.display());
                }
                Preferences::default()
            }
        };
        Store { file, prefs }
    }

    pub fn save(&self) -> Result<(), String> {
        if let Some(dir) = self.file.parent() {
            fs::create_dir_all(dir).map_err(|e| format!("saving window preferences: {e}"))?;
        }
        let bytes = serde_json::to_vec_pretty(&self.prefs).expect("window preferences are JSON");
        write_private(&self.file, &bytes).map_err(|e| format!("saving window preferences: {e}"))
    }
}

/// Where the window is and how big, unless it is minimized, maximized or full screen. Call it
/// without holding the store: off the main thread, each question waits for the main thread.
pub fn current(w: &Window) -> Option<Geometry> {
    let normal = w.is_visible().unwrap_or(false)
        && !w.is_minimized().unwrap_or(true)
        && !w.is_maximized().unwrap_or(true)
        && !w.is_fullscreen().unwrap_or(true);
    if !normal {
        return None;
    }
    let size = w
        .inner_size()
        .ok()?
        .to_logical::<f64>(w.scale_factor().ok()?);
    let place = w.outer_position().ok()?;
    (size.width >= MIN_SIZE.0 && size.height >= MIN_SIZE.1).then(|| Geometry {
        width: size.width.round(),
        height: size.height.round(),
        x: place.x,
        y: place.y,
    })
}

fn keep_aside(file: &Path, why: &str) {
    let aside = file.with_file_name(format!(
        "window.damaged-{}.json",
        crate::managed::random_key()
    ));
    match fs::rename(file, &aside) {
        Ok(()) => eprintln!(
            "pluxy-app: damaged window preferences kept at {}: {why}",
            aside.display()
        ),
        Err(e) => eprintln!(
            "pluxy-app: damaged window preferences: {why}; keeping the file in place: {e}"
        ),
    }
}

/// A monitor's work area, in physical pixels, and its scale.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Area {
    pub x: i32,
    pub y: i32,
    pub width: u32,
    pub height: u32,
    pub scale: f64,
}

/// The size (logical) a window opens at, and its place (physical), fitted into area: no smaller
/// than the minimum, no bigger than the area, and wholly inside it where it fits.
pub fn fit(g: Geometry, area: Option<Area>) -> (f64, f64, Option<(i32, i32)>) {
    let width = g.width.max(MIN_SIZE.0);
    let height = g.height.max(MIN_SIZE.1);
    let Some(a) = area.filter(|a| a.scale.is_finite() && a.scale > 0.0) else {
        return (width, height, None);
    };
    let width = width.min((f64::from(a.width) / a.scale).floor().max(MIN_SIZE.0));
    let height = height.min((f64::from(a.height) / a.scale).floor().max(MIN_SIZE.1));
    let clamp = |at: i32, origin: i32, room: u32, length: f64| {
        let min = i64::from(origin);
        let max = (min + i64::from(room) - (length * a.scale).round() as i64).max(min);
        i64::from(at).clamp(min, max) as i32
    };
    let x = clamp(g.x, a.x, a.width, width);
    let y = clamp(g.y, a.y, a.height, height);
    (width, height, Some((x, y)))
}

/// Where and how big a new window opens: its saved geometry fitted to the monitor holding its
/// place (or the primary one), or the default size where the desktop puts it. The place is in
/// logical pixels, as the window builder takes it.
pub fn opening(app: &AppHandle, saved: Option<Geometry>) -> (f64, f64, Option<(f64, f64)>) {
    let Some(g) = saved else {
        return (DEFAULT_SIZE.0, DEFAULT_SIZE.1, None);
    };
    let monitor = app
        .monitor_from_point(f64::from(g.x), f64::from(g.y))
        .ok()
        .flatten()
        .or_else(|| app.primary_monitor().ok().flatten());
    let area = monitor.map(|m| {
        let w = m.work_area();
        Area {
            x: w.position.x,
            y: w.position.y,
            width: w.size.width,
            height: w.size.height,
            scale: m.scale_factor(),
        }
    });
    let (width, height, place) = fit(g, area);
    let scale = area.map_or(1.0, |a| a.scale);
    (
        width,
        height,
        place.map(|(x, y)| (f64::from(x) / scale, f64::from(y) / scale)),
    )
}

/// Whether the tray icon can be seen: GTK makes a tray icon even with no desktop host to show it,
/// so ask the session's StatusNotifierWatcher whether a host is registered.
#[cfg(target_os = "linux")]
pub fn tray_host() -> Result<(), String> {
    use gtk::gio::{self, DBusCallFlags};
    use gtk::glib::{ToVariant, Variant};
    let bus = gio::bus_get_sync(gio::BusType::Session, gio::Cancellable::NONE)
        .map_err(|e| e.to_string())?;
    let owner = bus
        .call_sync(
            Some("org.freedesktop.DBus"),
            "/org/freedesktop/DBus",
            "org.freedesktop.DBus",
            "NameHasOwner",
            Some(&("org.kde.StatusNotifierWatcher",).to_variant()),
            None,
            DBusCallFlags::NO_AUTO_START,
            500,
            gio::Cancellable::NONE,
        )
        .map_err(|e| e.to_string())?;
    if owner.get::<(bool,)>() != Some((true,)) {
        return Err("no StatusNotifierWatcher owns the session bus name".into());
    }
    let host = bus
        .call_sync(
            Some("org.kde.StatusNotifierWatcher"),
            "/StatusNotifierWatcher",
            "org.freedesktop.DBus.Properties",
            "Get",
            Some(
                &(
                    "org.kde.StatusNotifierWatcher",
                    "IsStatusNotifierHostRegistered",
                )
                    .to_variant(),
            ),
            None,
            DBusCallFlags::NO_AUTO_START,
            500,
            gio::Cancellable::NONE,
        )
        .map_err(|e| e.to_string())?;
    if host.get::<(Variant,)>().and_then(|(v,)| v.get::<bool>()) != Some(true) {
        return Err("StatusNotifierWatcher has no registered host".into());
    }
    Ok(())
}

/// The Windows and macOS desktops always show tray icons.
#[cfg(not(target_os = "linux"))]
pub fn tray_host() -> Result<(), String> {
    Ok(())
}

/// What the settings show.
#[derive(Serialize)]
pub struct View {
    pub close_behavior: Close,
    /// Whether closing can leave the app in the tray now.
    pub tray: bool,
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp() -> PathBuf {
        let dir =
            std::env::temp_dir().join(format!("pluxy-window-{}", crate::managed::random_key()));
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[test]
    fn choices_use_the_old_apps_names_and_default_to_ask() {
        assert_eq!(Preferences::default().close_behavior, Close::Ask);
        for (c, name) in [
            (Close::Ask, "ask"),
            (Close::Exit, "exit"),
            (Close::MinimizeToTray, "minimize-to-tray"),
        ] {
            assert_eq!(serde_json::to_value(c).unwrap(), serde_json::json!(name));
        }
    }

    #[test]
    fn saved_preferences_are_private_and_read_back() {
        let dir = temp();
        let mut s = Store::open(dir.join("config").join("window.json"));
        assert_eq!(s.prefs, Preferences::default());
        s.prefs = Preferences {
            close_behavior: Close::MinimizeToTray,
            geometry: Some(Geometry {
                width: 900.0,
                height: 640.0,
                x: -1200,
                y: 90,
            }),
        };
        s.save().unwrap();
        assert_eq!(Store::open(s.file.clone()).prefs, s.prefs);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = fs::metadata(&s.file).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o600);
        }
        // A file with only the choice (or fields a later app adds) still reads.
        fs::write(&s.file, br#"{"close_behavior":"exit","later":1}"#).unwrap();
        let p = Store::open(s.file.clone()).prefs;
        assert_eq!((p.close_behavior, p.geometry), (Close::Exit, None));
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn a_damaged_file_is_kept_aside_and_the_defaults_used() {
        let dir = temp();
        let file = dir.join("window.json");
        fs::write(&file, br#"{"close_behavior":"hide"}"#).unwrap();
        assert_eq!(Store::open(file.clone()).prefs, Preferences::default());
        assert!(!file.exists());
        let aside: Vec<_> = fs::read_dir(&dir).unwrap().flatten().collect();
        assert_eq!(aside.len(), 1);
        assert_eq!(
            fs::read(aside[0].path()).unwrap(),
            br#"{"close_behavior":"hide"}"#
        );
        fs::remove_dir_all(dir).unwrap();
    }

    const SCREEN: Area = Area {
        x: 0,
        y: 0,
        width: 1280,
        height: 800,
        scale: 1.0,
    };

    fn g(width: f64, height: f64, x: i32, y: i32) -> Geometry {
        Geometry {
            width,
            height,
            x,
            y,
        }
    }

    #[test]
    fn a_window_that_fits_opens_where_it_was() {
        assert_eq!(
            fit(g(900.0, 640.0, 130, 90), Some(SCREEN)),
            (900.0, 640.0, Some((130, 90)))
        );
    }

    #[test]
    fn a_window_off_its_monitor_is_moved_onto_it_and_shrunk_to_fit() {
        // Too far right and down: moved so it ends at the area's edge.
        assert_eq!(
            fit(g(900.0, 640.0, 1000, 700), Some(SCREEN)),
            (900.0, 640.0, Some((380, 160)))
        );
        // Bigger than the area: as big as the area, at its corner.
        assert_eq!(
            fit(g(3000.0, 2000.0, -50, -50), Some(SCREEN)),
            (1280.0, 800.0, Some((0, 0)))
        );
        // A monitor left of the primary one, at scale 2: physical place, logical size.
        let left = Area {
            x: -2560,
            y: 0,
            width: 2560,
            height: 1600,
            scale: 2.0,
        };
        assert_eq!(
            fit(g(1000.0, 700.0, -100, 0), Some(left)),
            (1000.0, 700.0, Some((-2000, 0)))
        );
    }

    #[test]
    fn never_smaller_than_the_minimum() {
        assert_eq!(fit(g(100.0, 100.0, 5, 5), None), (640.0, 480.0, None));
        let tiny = Area {
            width: 500,
            height: 400,
            ..SCREEN
        };
        assert_eq!(
            fit(g(900.0, 640.0, 10, 10), Some(tiny)),
            (640.0, 480.0, Some((0, 0)))
        );
    }
}
