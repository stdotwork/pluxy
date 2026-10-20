//! Apply Linux graphics compatibility defaults before GTK/WebKit initialization.
//!
//! WebKit's DMA-BUF path can fail with GBM buffer errors, while native Wayland
//! can terminate GTK with a protocol error (#406). Prefer X11 when advertised,
//! but let GTK fall back to Wayland if that display cannot actually be opened.
//!
//! The NVIDIA proprietary driver is the exception: under XWayland its WebKit
//! window stops repainting once a dialog opens and turns black when restored,
//! so Wayland sessions on that driver keep GTK's native Wayland backend.

use std::{env, ffi::OsString, path::Path};

fn defaults(
    get: impl Fn(&str) -> Option<OsString>,
    nvidia_driver: bool,
) -> Vec<(&'static str, &'static str)> {
    let mut values = Vec::new();
    if get("WEBKIT_DISABLE_DMABUF_RENDERER").is_none() {
        values.push(("WEBKIT_DISABLE_DMABUF_RENDERER", "1"));
    }
    let wayland_session = get("WAYLAND_DISPLAY").is_some_and(|value| !value.is_empty());
    if get("GDK_BACKEND").is_none()
        && get("DISPLAY").is_some_and(|value| !value.is_empty())
        && !(wayland_session && nvidia_driver)
    {
        values.push(("GDK_BACKEND", "x11,wayland"));
    }
    values
}

fn nvidia_driver_loaded() -> bool {
    Path::new("/proc/driver/nvidia/version").exists()
        || Path::new("/sys/module/nvidia_drm").exists()
}

/// # Safety
/// Must run on the main thread before any other threads or GUI libraries start.
pub(super) unsafe fn configure() {
    for (key, value) in defaults(|key| env::var_os(key), nvidia_driver_loaded()) {
        // SAFETY: the caller guarantees single-threaded startup.
        unsafe { env::set_var(key, value) };
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn settings_with(
        environment: &[(&str, &str)],
        nvidia_driver: bool,
    ) -> Vec<(&'static str, &'static str)> {
        defaults(
            |key| {
                environment
                    .iter()
                    .find(|(name, _)| *name == key)
                    .map(|(_, value)| OsString::from(value))
            },
            nvidia_driver,
        )
    }

    fn settings(environment: &[(&str, &str)]) -> Vec<(&'static str, &'static str)> {
        settings_with(environment, false)
    }

    #[test]
    fn x11_and_xwayland_sessions_get_both_workarounds_without_arguments() {
        for environment in [
            vec![("DISPLAY", ":0")],
            vec![("DISPLAY", ":1"), ("WAYLAND_DISPLAY", "wayland-0")],
        ] {
            assert_eq!(
                settings(&environment),
                vec![
                    ("WEBKIT_DISABLE_DMABUF_RENDERER", "1"),
                    ("GDK_BACKEND", "x11,wayland"),
                ]
            );
        }
    }

    #[test]
    fn nvidia_wayland_sessions_keep_the_native_wayland_backend() {
        assert_eq!(
            settings_with(&[("DISPLAY", ":1"), ("WAYLAND_DISPLAY", "wayland-0")], true),
            vec![("WEBKIT_DISABLE_DMABUF_RENDERER", "1")]
        );
    }

    #[test]
    fn nvidia_x11_sessions_still_prefer_x11() {
        for environment in [
            vec![("DISPLAY", ":0")],
            vec![("DISPLAY", ":0"), ("WAYLAND_DISPLAY", "")],
        ] {
            assert_eq!(
                settings_with(&environment, true),
                vec![
                    ("WEBKIT_DISABLE_DMABUF_RENDERER", "1"),
                    ("GDK_BACKEND", "x11,wayland"),
                ]
            );
        }
    }

    #[test]
    fn pure_wayland_and_headless_sessions_do_not_force_x11() {
        for environment in [
            vec![],
            vec![("WAYLAND_DISPLAY", "wayland-0")],
            vec![("DISPLAY", ""), ("WAYLAND_DISPLAY", "wayland-0")],
        ] {
            assert_eq!(
                settings(&environment),
                vec![("WEBKIT_DISABLE_DMABUF_RENDERER", "1")]
            );
        }
    }

    #[test]
    fn explicit_backend_does_not_disable_the_independent_webkit_fix() {
        assert_eq!(
            settings(&[("DISPLAY", ":0"), ("GDK_BACKEND", "wayland")]),
            vec![("WEBKIT_DISABLE_DMABUF_RENDERER", "1")]
        );
    }

    #[test]
    fn explicit_renderer_does_not_disable_the_independent_backend_fix() {
        assert_eq!(
            settings(&[("DISPLAY", ":0"), ("WEBKIT_DISABLE_DMABUF_RENDERER", "0")]),
            vec![("GDK_BACKEND", "x11,wayland")]
        );
    }

    #[test]
    fn explicit_overrides_and_inherited_defaults_are_preserved() {
        for nvidia_driver in [false, true] {
            for (backend, renderer) in [("wayland", "0"), ("x11,wayland", "1"), ("", "")] {
                assert!(settings_with(
                    &[
                        ("DISPLAY", ":0"),
                        ("GDK_BACKEND", backend),
                        ("WEBKIT_DISABLE_DMABUF_RENDERER", renderer),
                    ],
                    nvidia_driver,
                )
                .is_empty());
            }
        }
    }
}
