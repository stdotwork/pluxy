//! What closing the window does, and the window's size and place, kept in `window.json` in the
//! app's config folder, readable only by this user.
//!
//! Closing leaves the app in the tray (Keep in the tray, the default), which destroys the window
//! and its webviews, quits (Quit) or asks (Ask). Without a usable tray the app is never left
//! running with no way back to it: closing quits whatever the choice. In local and remote mode
//! pluxy runs on its own and keeps running. When quitting would stop the app's own pluxy (managed
//! mode), the window asks first, unless the person said not to (`ask_without_tray`, kept apart
//! from the choice so it still holds the day a tray appears).
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
    Ask,
    Exit,
    #[default]
    MinimizeToTray,
}

#[derive(Clone, Copy, Debug, PartialEq, Serialize, Deserialize)]
pub struct Geometry {
    pub width: f64,
    pub height: f64,
    pub x: i32,
    pub y: i32,
}

#[derive(Clone, Copy, Debug, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Preferences {
    pub close_behavior: Close,
    pub geometry: Option<Geometry>,
    /// With no usable tray in managed mode, whether closing asks before it quits and stops the
    /// app's own pluxy. The prompt's "Don't ask again" turns it off; the close choice stays.
    pub ask_without_tray: bool,
}

impl Default for Preferences {
    fn default() -> Preferences {
        Preferences {
            close_behavior: Close::default(),
            geometry: None,
            ask_without_tray: true,
        }
    }
}

/// What closing the window does.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Closing {
    Quit,
    ToTray,
    /// Ask with the window's close prompt (`window.__pluxyClosePrompt()`).
    Ask,
    /// Ask whether to quit, which stops the app's own pluxy (`window.__pluxyClosePrompt("no-tray")`).
    AskWithoutTray,
    /// Nothing: the window stays open.
    KeepOpen,
}

/// What closing does, from the preferences, whether there is a usable tray, and whether the app
/// runs pluxy itself (managed mode), which quitting stops.
pub fn closing(prefs: &Preferences, tray: bool, managed: bool) -> Closing {
    match (tray, prefs.close_behavior) {
        (false, _) if managed && prefs.ask_without_tray => Closing::AskWithoutTray,
        (false, _) | (true, Close::Exit) => Closing::Quit,
        (true, Close::MinimizeToTray) => Closing::ToTray,
        (true, Close::Ask) => Closing::Ask,
    }
}

/// Which close prompt the window showed: the one for Ask (`window.__pluxyClosePrompt()`), or the
/// no-tray one (`window.__pluxyClosePrompt("no-tray")`). Its answer comes back with its kind.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Prompt {
    Ask,
    NoTray,
}

/// What a close prompt the window did not answer within three seconds (its page still loading,
/// or busy) does instead: with a tray the window closes to it, from where it opens again; with
/// none the window stays open, since quitting would stop pluxy without asking.
pub fn unanswered(asked: Closing) -> Closing {
    match asked {
        Closing::Ask => Closing::ToTray,
        _ => Closing::KeepOpen,
    }
}

/// Whether action answers the prompt the window showed: Ask's prompt quits or closes to the tray
/// (which needs a usable tray, asked through tray only then); the no-tray prompt only quits, since
/// keeping the window open sends nothing.
pub fn check(
    kind: Prompt,
    action: Close,
    tray: impl FnOnce() -> Result<(), String>,
) -> Result<(), String> {
    match (kind, action) {
        (_, Close::Ask) => Err("choose Exit app or Minimize to tray".into()),
        (Prompt::NoTray, Close::MinimizeToTray) => {
            Err("with no tray, the window either stays open or closes and stops pluxy".into())
        }
        (Prompt::Ask, Close::MinimizeToTray) => tray().map_err(|e| {
            format!("The system tray is unavailable ({e}); keep the window open or exit the app")
        }),
        (_, Close::Exit) => Ok(()),
    }
}

/// Keeps a close prompt's answer the person asked to remember, by the prompt they answered, not by
/// whether a tray shows by then. Ask's "Remember this choice" becomes the close choice; the
/// no-tray prompt's "Don't ask again" only stops that prompt, so the choice stays as it was.
pub fn remember(prefs: &mut Preferences, kind: Prompt, action: Close) {
    match kind {
        Prompt::Ask => prefs.close_behavior = action,
        Prompt::NoTray => prefs.ask_without_tray = false,
    }
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
    /// Whether closing with no tray in managed mode asks first.
    pub ask_without_tray: bool,
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
    fn choices_use_the_old_apps_names_and_default_to_keep_in_the_tray() {
        assert_eq!(Preferences::default().close_behavior, Close::MinimizeToTray);
        // A file saved before the choice had a default of its own reads the same.
        assert_eq!(
            serde_json::from_str::<Preferences>("{}").unwrap(),
            Preferences::default()
        );
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
            close_behavior: Close::Ask,
            geometry: Some(Geometry {
                width: 900.0,
                height: 640.0,
                x: -1200,
                y: 90,
            }),
            ask_without_tray: false,
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
        assert_eq!(
            (p.close_behavior, p.geometry, p.ask_without_tray),
            (Close::Exit, None, true)
        );
        fs::remove_dir_all(dir).unwrap();
    }

    /// With a tray, closing does what the choice says. With none, closing quits, except in
    /// managed mode, where it stops the app's own pluxy: there it asks first, until the person
    /// says not to.
    #[test]
    fn closing_with_no_tray_asks_only_when_it_would_stop_the_apps_own_pluxy() {
        let prefs = |close_behavior, ask_without_tray| Preferences {
            close_behavior,
            ask_without_tray,
            ..Preferences::default()
        };
        for managed in [true, false] {
            for (choice, want) in [
                (Close::MinimizeToTray, Closing::ToTray),
                (Close::Exit, Closing::Quit),
                (Close::Ask, Closing::Ask),
            ] {
                for ask in [true, false] {
                    assert_eq!(closing(&prefs(choice, ask), true, managed), want);
                }
            }
        }
        for choice in [Close::MinimizeToTray, Close::Exit, Close::Ask] {
            assert_eq!(
                closing(&prefs(choice, true), false, true),
                Closing::AskWithoutTray,
                "{choice:?}"
            );
            assert_eq!(closing(&prefs(choice, false), false, true), Closing::Quit);
            // Local and remote: pluxy keeps running whatever the app does, so it never asks.
            assert_eq!(closing(&prefs(choice, true), false, false), Closing::Quit);
        }
        // The default asks.
        assert_eq!(
            closing(&Preferences::default(), false, true),
            Closing::AskWithoutTray
        );
    }

    #[test]
    fn a_close_prompt_with_no_answer_keeps_the_window_unless_a_tray_holds_the_app() {
        assert_eq!(unanswered(Closing::AskWithoutTray), Closing::KeepOpen);
        assert_eq!(unanswered(Closing::Ask), Closing::ToTray);
    }

    /// "Don't ask again" on the no-tray prompt stops the prompt and leaves the close choice; on
    /// Ask's prompt, Remember this choice saves the choice.
    #[test]
    fn dont_ask_again_without_a_tray_keeps_the_close_choice() {
        for choice in [Close::MinimizeToTray, Close::Ask, Close::Exit] {
            let mut p = Preferences {
                close_behavior: choice,
                ..Preferences::default()
            };
            remember(&mut p, Prompt::NoTray, Close::Exit);
            assert_eq!((p.close_behavior, p.ask_without_tray), (choice, false));
            assert_eq!(closing(&p, false, true), Closing::Quit);
            // The day a tray appears, the choice still holds.
            assert_eq!(
                closing(&p, true, true),
                closing(
                    &Preferences {
                        close_behavior: choice,
                        ..Preferences::default()
                    },
                    true,
                    true
                )
            );
        }
        let mut p = Preferences::default();
        remember(&mut p, Prompt::Ask, Close::Exit);
        assert_eq!((p.close_behavior, p.ask_without_tray), (Close::Exit, true));
    }

    /// The answer is kept by the prompt the person answered. A tray that shows by the time they
    /// answer the no-tray prompt (a desktop's tray host starting late) must not turn "Don't ask
    /// again" with Close and stop pluxy into the close choice Quit.
    #[test]
    fn a_no_tray_answer_is_kept_as_no_tray_even_when_a_tray_shows_by_then() {
        let tray_now = || Ok(());
        for choice in [Close::MinimizeToTray, Close::Ask, Close::Exit] {
            let mut p = Preferences {
                close_behavior: choice,
                ..Preferences::default()
            };
            assert_eq!(check(Prompt::NoTray, Close::Exit, tray_now), Ok(()));
            remember(&mut p, Prompt::NoTray, Close::Exit);
            assert_eq!(
                (p.close_behavior, p.ask_without_tray),
                (choice, false),
                "{choice:?}"
            );
            // With the tray that now shows, closing still does what the choice says.
            assert_eq!(
                closing(&p, true, true),
                closing(
                    &Preferences {
                        close_behavior: choice,
                        ..Preferences::default()
                    },
                    true,
                    true
                )
            );
        }
        // The no-tray prompt has no Keep in the tray; Ask's needs a usable tray.
        assert!(check(Prompt::NoTray, Close::MinimizeToTray, tray_now).is_err());
        assert_eq!(check(Prompt::Ask, Close::MinimizeToTray, tray_now), Ok(()));
        let none = check(Prompt::Ask, Close::MinimizeToTray, || Err("no host".into()));
        assert!(none.is_err_and(|e| e.contains("no host")));
        assert!(check(Prompt::Ask, Close::Ask, tray_now).is_err());
        // The window sends the kind as it names it.
        assert_eq!(
            serde_json::from_str::<Prompt>(r#""no-tray""#).unwrap(),
            Prompt::NoTray
        );
        assert_eq!(
            serde_json::from_str::<Prompt>(r#""ask""#).unwrap(),
            Prompt::Ask
        );
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
