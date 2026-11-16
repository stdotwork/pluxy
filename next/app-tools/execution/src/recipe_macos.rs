//! macOS' launch recipes. The terminal is Terminal: the execution program in its `terminal` mode
//! opens a `.command` file in Terminal through LaunchServices, and the program, its arguments,
//! working folder and environment reach Terminal's window over a private socket, never as text a
//! shell reads (`launch.rs`). No AppleScript, so no Automation prompt. Files open in the app
//! LaunchServices has for their type, through `/usr/bin/open`.

use super::{text, AUTOMATIC};
use pluxy_program::Error;
use serde_json::{json, Value};
use std::ffi::{c_void, CString};
use std::os::unix::ffi::OsStrExt;
use std::path::{Path, PathBuf};

/// Where Terminal is: `/System/Applications/Utilities` since macOS 10.15 (the other is older).
const TERMINAL: &[&str] = &[
    "/System/Applications/Utilities/Terminal.app",
    "/Applications/Utilities/Terminal.app",
];

/// The opener: macOS' own, on its sealed system volume.
const OPEN: &str = "/usr/bin/open";

/// The files an open launch opens, by extension (LaunchServices picks the app by it), with the
/// content type shown: the plain-text types of Linux's list.
pub const OPEN_TYPES: &[(&str, &str)] = &[
    ("txt", "text/plain"),
    ("json", "application/json"),
    ("jsonl", "application/jsonl"),
    ("ndjson", "application/x-ndjson"),
    ("md", "text/markdown"),
    ("log", "text/x-log"),
];

/// Extended attributes that can make LaunchServices open a file in another app than its type's:
/// classic type and creator codes, and a per-file "Open with".
const APP_CHOOSING: &[&str] = &[
    "com.apple.FinderInfo",
    "com.apple.ResourceFork",
    "com.apple.LaunchServices.OpenWith",
];

fn ext(p: &Path) -> Option<String> {
    p.extension()
        .and_then(|e| e.to_str())
        .map(str::to_ascii_lowercase)
}

/// Terminal's bundle, when the system has it.
pub fn terminal_app() -> Option<&'static str> {
    TERMINAL.iter().copied().find(|p| Path::new(p).is_dir())
}

/// The terminals there are, after Automatic.
pub fn terminals() -> Vec<Value> {
    terminal_app()
        .map(|p| json!({"id": "terminal", "label": "Terminal", "path": p}))
        .into_iter()
        .collect()
}

/// The execution program's own path, resolved: the terminal of macOS' terminal launches.
pub fn this_program() -> Result<String, Error> {
    let p = std::env::current_exe()
        .and_then(std::fs::canonicalize)
        .map_err(|e| Error::internal(format!("the execution program's path: {e}")))?;
    text("the execution program", &p)
}

/// A terminal launch of program with args in terminal wanted: the execution program in its
/// `terminal` mode, naming Terminal's bundle.
pub fn terminal(
    wanted: &str,
    program: &str,
    args: &[String],
) -> Result<(String, Vec<String>), Error> {
    if wanted != AUTOMATIC && wanted != "terminal" {
        return Err(Error::invalid_request(format!("no terminal {wanted}")));
    }
    let app = terminal_app().ok_or_else(|| Error::not_found("Terminal is not on this Mac"))?;
    // The program and arguments are handed over to Terminal's window whole: refused here when
    // they alone are over what a hand-over carries (the environment is added at the launch).
    let mut handed = vec![program.to_string()];
    handed.extend(args.iter().cloned());
    crate::handover::Spec {
        argv: handed,
        cwd: String::new(),
        env: Vec::new(),
    }
    .encode()
    .map_err(Error::invalid_request)?;
    let mut argv: Vec<String> = [
        "terminal", "--with", "terminal", "--app", app, "--", program,
    ]
    .iter()
    .map(|s| s.to_string())
    .collect();
    argv.extend(args.iter().cloned());
    Ok((this_program()?, argv))
}

/// Whether app is a bundle a launch may name: Terminal, as the terminal of `launch.rs`.
pub fn terminal_bundle(app: &str) -> bool {
    TERMINAL.contains(&app) && Path::new(app).is_dir()
}

/// The opener of a resolved file, after macOS' rules for it: a type of `OPEN_TYPES`, no
/// attribute that can choose another app, and an app LaunchServices opens that type with.
pub fn opener(file: &Path) -> Result<(String, Vec<String>), Error> {
    let shown = text("the file to open", file)?;
    let e = ext(file).unwrap_or_default();
    if !OPEN_TYPES.iter().any(|(x, _)| *x == e) {
        return Err(Error::invalid_request(format!(
            "the file to open must be {}",
            OPEN_TYPES
                .iter()
                .map(|(x, _)| format!(".{x}"))
                .collect::<Vec<_>>()
                .join(", ")
        )));
    }
    if let Some(a) = attributes(file)?
        .into_iter()
        .find(|a| APP_CHOOSING.contains(&a.as_str()))
    {
        return Err(Error::invalid_request(format!(
            "the file to open carries {a}, which can open it in another app than its type's"
        )));
    }
    if !opens(file) {
        return Err(Error::not_found(format!(
            "no app opens .{e} files on this Mac: choose one with Finder's Get Info, Open with"
        )));
    }
    Ok((OPEN.into(), vec![shown]))
}

/// The names of file's extended attributes (file not followed if it is a link).
pub fn attributes(file: &Path) -> Result<Vec<String>, Error> {
    let c = CString::new(file.as_os_str().as_bytes())
        .map_err(|_| Error::invalid_request("a NUL in the path"))?;
    let fail = |what: &str| {
        Error::not_found(format!(
            "{}: {what}: {}",
            file.display(),
            std::io::Error::last_os_error()
        ))
    };
    // SAFETY: c is NUL-terminated; a null buffer asks for the size.
    let n = unsafe { libc::listxattr(c.as_ptr(), std::ptr::null_mut(), 0, libc::XATTR_NOFOLLOW) };
    if n < 0 {
        return Err(fail("its attributes"));
    }
    let mut buf = vec![0u8; n as usize];
    // SAFETY: buf holds n bytes.
    let n = unsafe {
        libc::listxattr(
            c.as_ptr(),
            buf.as_mut_ptr().cast(),
            buf.len(),
            libc::XATTR_NOFOLLOW,
        )
    };
    if n < 0 {
        return Err(fail("its attributes"));
    }
    buf.truncate(n as usize);
    Ok(buf
        .split(|b| *b == 0)
        .filter(|n| !n.is_empty())
        .map(|n| String::from_utf8_lossy(n).into_owned())
        .collect())
}

#[link(name = "CoreFoundation", kind = "framework")]
extern "C" {
    fn CFURLCreateFromFileSystemRepresentation(
        allocator: *const c_void,
        buffer: *const u8,
        length: isize,
        is_directory: u8,
    ) -> *const c_void;
    fn CFRelease(cf: *const c_void);
}

#[link(name = "CoreServices", kind = "framework")]
extern "C" {
    fn LSCopyDefaultApplicationURLForURL(
        url: *const c_void,
        roles: u32,
        error: *mut *const c_void,
    ) -> *const c_void;
}

/// `kLSRolesAll`.
const ROLES_ALL: u32 = 0xFFFF_FFFF;

/// Whether LaunchServices has an app that opens file (the one `open` would use).
pub fn opens(file: &Path) -> bool {
    let bytes = file.as_os_str().as_bytes();
    // SAFETY: CoreFoundation copies the bytes; each object made here is released once.
    unsafe {
        let url = CFURLCreateFromFileSystemRepresentation(
            std::ptr::null(),
            bytes.as_ptr(),
            bytes.len() as isize,
            0,
        );
        if url.is_null() {
            return false;
        }
        let app = LSCopyDefaultApplicationURLForURL(url, ROLES_ALL, std::ptr::null_mut());
        CFRelease(url);
        if app.is_null() {
            return false;
        }
        CFRelease(app);
        true
    }
}

/// Where an app bundle a direct launch names may be: the system's, everyone's and the person's
/// applications folders.
fn app_folders() -> Vec<PathBuf> {
    let mut v = vec![
        PathBuf::from("/System/Applications"),
        PathBuf::from("/Applications"),
    ];
    if let Some(home) = std::env::var_os("HOME") {
        v.push(Path::new(&home).join("Applications"));
    }
    v
}

/// An app a direct launch names instead of a program: a full path to an application bundle
/// (`….app`) in an applications folder, such as the Codex app, opened by `/usr/bin/open -a`
/// (LaunchServices starts it, or brings it forward when it runs). It takes no arguments; its
/// environment is LaunchServices' (the launch's is refused in `launch`).
pub fn app(input: &Value) -> Result<Option<Value>, Error> {
    let Some(p) = input["program"]
        .as_str()
        .filter(|p| p.starts_with('/') && Path::new(p).extension().is_some_and(|e| e == "app"))
    else {
        return Ok(None);
    };
    let bundle =
        std::fs::canonicalize(p).map_err(|e| Error::not_found(format!("the app {p}: {e}")))?;
    if !bundle.join("Contents/Info.plist").is_file() {
        return Err(Error::invalid_request(format!("{p} is not an application")));
    }
    if !app_folders()
        .iter()
        .filter_map(|d| std::fs::canonicalize(d).ok())
        .any(|d| bundle.parent() == Some(d.as_path()))
    {
        return Err(Error::invalid_request(format!(
            "the app {p} must be in /Applications, ~/Applications or /System/Applications"
        )));
    }
    if input["args"].as_array().is_some_and(|a| !a.is_empty()) || input.get("override").is_some() {
        return Err(Error::invalid_request(
            "an app takes no arguments and no override",
        ));
    }
    let cwd = input["cwd"].as_str().unwrap_or_default();
    if !cwd.starts_with('/') || !Path::new(cwd).is_dir() {
        return Err(Error::invalid_request(format!(
            "the working folder {cwd:?} is not an existing absolute folder"
        )));
    }
    Ok(Some(json!({
        "executable": OPEN,
        "argv": ["-a", text("the app", &bundle)?],
        "cwd": cwd,
    })))
}

/// Whether a launch is an app launch made by `app`: `/usr/bin/open -a <bundle>`.
pub fn is_app_launch(executable: &str, argv: &[Value]) -> bool {
    executable == OPEN && argv.first().is_some_and(|a| a == "-a")
}
