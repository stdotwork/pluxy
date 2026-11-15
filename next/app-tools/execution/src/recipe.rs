//! Launch recipes: the terminals pluxy knows, the openers that open a file in the person's own
//! app, and how a typed launch becomes the executable, argv and cwd the person confirms. Nothing
//! here starts a process. Linux's terminals and openers are below; Windows' and macOS' are in
//! `recipe_windows.rs` and `recipe_macos.rs` (`os`), where the execution program itself is the
//! terminal and opener (`launch.rs`).

use pluxy_program::Error;
use serde_json::{json, Value};
#[cfg(unix)]
use std::os::unix::fs::{MetadataExt, PermissionsExt};
use std::path::{Path, PathBuf};

#[cfg(windows)]
#[path = "recipe_windows.rs"]
pub(crate) mod os;
#[cfg(target_os = "macos")]
#[path = "recipe_macos.rs"]
pub(crate) mod os;
#[cfg(windows)]
use os::{executable, find};

/// A terminal: its ID (its program's name), its label and the arguments before the command it
/// runs. The order is Automatic's, as in the old desktop app; `xfce4-terminal` and `mate-terminal`
/// take the rest of the command line after `-x` (their `-e` takes one command string).
#[cfg(target_os = "linux")]
pub struct Terminal {
    pub id: &'static str,
    pub label: &'static str,
    pub flags: &'static [&'static str],
}

#[cfg(target_os = "linux")]
pub const TERMINALS: &[Terminal] = &[
    Terminal {
        id: "x-terminal-emulator",
        label: "System terminal",
        flags: &["-e"],
    },
    Terminal {
        id: "gnome-terminal",
        label: "GNOME Terminal",
        flags: &["--"],
    },
    Terminal {
        id: "konsole",
        label: "Konsole",
        flags: &["-e"],
    },
    Terminal {
        id: "xfce4-terminal",
        label: "Xfce Terminal",
        flags: &["-x"],
    },
    Terminal {
        id: "mate-terminal",
        label: "MATE Terminal",
        flags: &["-x"],
    },
    Terminal {
        id: "kitty",
        label: "Kitty",
        flags: &["-e"],
    },
    Terminal {
        id: "alacritty",
        label: "Alacritty",
        flags: &["-e"],
    },
    Terminal {
        id: "ghostty",
        label: "Ghostty",
        flags: &["-e"],
    },
    Terminal {
        id: "xterm",
        label: "XTerm",
        flags: &["-e"],
    },
];

/// The ID of Automatic: the first terminal of `TERMINALS` found.
pub const AUTOMATIC: &str = "automatic";

/// The programs that open a file in the app the desktop opens its type with, and the arguments
/// before the file, in the order tried: the old desktop app's first two (its opener library tries
/// `xdg-open`, then `gio open`).
#[cfg(target_os = "linux")]
pub const OPENERS: &[(&str, &[&str])] = &[("xdg-open", &[]), ("gio", &["open"])];

/// Where an open launch takes its opener from: the system's program folders, the only ones the
/// app accepts an opener from (a caller's search path could name any program `xdg-open`).
#[cfg(target_os = "linux")]
pub const SYSTEM_PATH: &[&str] = &[
    "/usr/local/bin",
    "/usr/bin",
    "/bin",
    "/run/current-system/sw/bin",
];

/// Whether p is an executable regular file.
#[cfg(unix)]
fn executable(p: &Path) -> bool {
    std::fs::metadata(p).is_ok_and(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
}

/// The search path's folders: absolute ones only, as given.
fn folders(input: &Value) -> Vec<PathBuf> {
    input["search_path"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(PathBuf::from)
        .filter(|d| d.is_absolute())
        .collect()
}

/// The first executable named name in folders.
#[cfg(unix)]
fn find(name: &str, folders: &[PathBuf]) -> Option<PathBuf> {
    folders.iter().map(|d| d.join(name)).find(|p| executable(p))
}

/// `terminals`: Automatic, then each terminal found on the search path, with its path. On Windows
/// and macOS, Automatic and each terminal the system has (`os::terminals`).
#[cfg(not(target_os = "linux"))]
pub fn terminals(_input: &Value) -> Value {
    let mut out = vec![json!({"id": AUTOMATIC, "label": "Automatic"})];
    out.extend(os::terminals());
    json!({ "terminals": out })
}

/// `terminals`: Automatic, then each terminal found on the search path, with its path.
#[cfg(target_os = "linux")]
pub fn terminals(input: &Value) -> Value {
    let folders = folders(input);
    let mut out = vec![json!({"id": AUTOMATIC, "label": "Automatic"})];
    for t in TERMINALS {
        if let Some(p) = find(t.id, &folders) {
            out.push(json!({"id": t.id, "label": t.label, "path": p}));
        }
    }
    json!({ "terminals": out })
}

/// The program a launch runs: its override (an absolute path) if given, else program itself when
/// it is an absolute path, else the first executable of that name on the search path.
fn program(input: &Value, folders: &[PathBuf]) -> Result<PathBuf, Error> {
    let absolute = |what: &str, p: &str| -> Result<PathBuf, Error> {
        let p = PathBuf::from(p);
        if !p.is_absolute() {
            return Err(Error::invalid_request(format!(
                "{what} must be an absolute path"
            )));
        }
        if !runnable(&p) {
            return Err(Error::not_found(format!(
                "{what} {} is not an executable file",
                p.display()
            )));
        }
        Ok(p)
    };
    if let Some(o) = input["override"].as_str() {
        return absolute("the executable override", o);
    }
    let name = input["program"].as_str().unwrap_or_default();
    if Path::new(name).is_absolute() {
        return absolute("the program", name);
    }
    if name.is_empty() || name.contains('/') || (cfg!(windows) && name.contains(['\\', ':'])) {
        return Err(Error::invalid_request(
            "the program is a name or an absolute path",
        ));
    }
    find(name, folders).ok_or_else(|| Error::not_found(format!("{name} is not on the PATH")))
}

/// Whether p is a program a launch may run: an executable file; on Windows also a batch file,
/// which is read as the command shim it must be (`os::resolve`).
fn runnable(p: &Path) -> bool {
    #[cfg(windows)]
    return executable(p) || os::batch(p);
    #[cfg(unix)]
    executable(p)
}

/// A path's text, refused when it is not UTF-8: the host confirms UTF-8 paths only. On Windows,
/// its plain form (`C:\…`, not `\\?\C:\…`), refused when that names another file.
fn text(what: &str, p: &Path) -> Result<String, Error> {
    let t = p
        .to_str()
        .ok_or_else(|| Error::invalid_request(format!("{what} is not a UTF-8 path")))?;
    #[cfg(windows)]
    return crate::winpath::plain(t).map_err(|e| Error::invalid_request(format!("{what}: {e}")));
    #[cfg(unix)]
    Ok(t.to_string())
}

/// An open launch, with its opener from `SYSTEM_PATH`. A native-test build also takes the
/// opener from the input's `search_path`, for the native test's stand-in; a release build refuses
/// it.
#[cfg(target_os = "linux")]
fn open(input: &Value) -> Result<Value, Error> {
    #[allow(unused_mut)]
    let mut fields = vec!["kind", "file", "within"];
    #[allow(unused_mut)]
    let mut folders: Vec<PathBuf> = SYSTEM_PATH.iter().map(PathBuf::from).collect();
    #[cfg(feature = "native-test")]
    if input.get("search_path").is_some() {
        fields.push("search_path");
        folders = self::folders(input);
    }
    open_from(input, &fields, &folders)
}

/// An open launch on Windows and macOS: the opener is the system's (`os::opener`), never taken
/// from a search path. A native-test build accepts and ignores the stand-in's `search_path`: the
/// native test uses the system's own route there.
#[cfg(not(target_os = "linux"))]
fn open(input: &Value) -> Result<Value, Error> {
    #[allow(unused_mut)]
    let mut fields = vec!["kind", "file", "within"];
    #[cfg(feature = "native-test")]
    fields.push("search_path");
    open_from(input, &fields, &[])
}

/// Refused unless every folder from file's parent up to and including within (both resolved, file
/// inside within) is a real folder that is the effective user's and writable by neither group nor
/// others, as the host requires: another user could otherwise rename a different file into place
/// before the opener reads it. Told here so a tool learns it before the person is asked.
#[cfg(unix)]
fn folders_own(file: &Path, within: &Path) -> Result<(), Error> {
    // SAFETY: geteuid has no preconditions and cannot fail.
    let me = unsafe { libc::geteuid() };
    for d in file.ancestors().skip(1) {
        let m = std::fs::symlink_metadata(d)
            .map_err(|e| Error::not_found(format!("{}: {e}", d.display())))?;
        if !m.is_dir() || m.uid() != me || m.permissions().mode() & 0o022 != 0 {
            return Err(Error::invalid_request(format!(
                "the folder {} of the file to open must be your own, writable by no one else",
                d.display()
            )));
        }
        if d == within {
            return Ok(());
        }
    }
    Err(Error::invalid_request(
        "the file to open must be inside its within folder",
    ))
}

/// On Windows: every folder from file's parent up to and including within is a real folder (not a
/// link, junction or other reparse point) that no one but the user, SYSTEM and Administrators may
/// change (its owner and access list, `others_may_write`).
#[cfg(windows)]
fn folders_own(file: &Path, within: &Path) -> Result<(), Error> {
    use std::os::windows::fs::MetadataExt;
    /// `FILE_ATTRIBUTE_REPARSE_POINT`.
    const REPARSE_POINT: u32 = 0x400;
    for d in file.ancestors().skip(1) {
        let not_found = |e: std::io::Error| Error::not_found(format!("{}: {e}", d.display()));
        let m = std::fs::symlink_metadata(d).map_err(not_found)?;
        let others = pluxy_files::others_may_write(d).map_err(not_found)?;
        if !m.is_dir() || m.file_attributes() & REPARSE_POINT != 0 || others {
            return Err(Error::invalid_request(format!(
                "the folder {} of the file to open must be a folder of your own (not a link or junction), writable by no one else",
                d.display()
            )));
        }
        if d == within {
            return Ok(());
        }
    }
    Err(Error::invalid_request(
        "the file to open must be inside its within folder",
    ))
}

/// An open launch: `<opener> <file>` (or `gio open <file>`) in the file's folder, the opener the
/// first of `OPENERS` found in folders. It names the file (the resolved path, which is its
/// argument) and the folder it must be inside (`within`, resolved), which the host checks, shows
/// and binds again. The file must be a regular file with no execute bit inside that folder, and
/// that folder and every folder between it and the file must be the user's own and writable by no
/// one else; the folder must not be `/`, the home folder or a folder above it. The input holds
/// only fields.
fn open_from(input: &Value, fields: &[&str], folders: &[PathBuf]) -> Result<Value, Error> {
    if let Some((k, _)) = input
        .as_object()
        .into_iter()
        .flatten()
        .find(|(k, _)| !fields.contains(&k.as_str()))
    {
        return Err(Error::invalid_request(format!(
            "an open launch takes {}, not {k}",
            fields.join(", ")
        )));
    }
    let resolve = |what: &str, k: &str| -> Result<PathBuf, Error> {
        let p = input[k].as_str().unwrap_or_default();
        if !Path::new(p).is_absolute() {
            return Err(Error::invalid_request(format!(
                "{what} must be an absolute path"
            )));
        }
        let r =
            std::fs::canonicalize(p).map_err(|e| Error::not_found(format!("{what} {p}: {e}")))?;
        // On Windows the plain form, the one shown and compared.
        #[cfg(windows)]
        let r = PathBuf::from(text(what, &r)?);
        Ok(r)
    };
    let within = resolve("the within folder", "within")?;
    if !within.is_dir() {
        return Err(Error::invalid_request(format!(
            "the within folder {} is not a folder",
            within.display()
        )));
    }
    let home = std::env::var_os("HOME").and_then(|h| std::fs::canonicalize(h).ok());
    // The root (`/`, or a drive's root on Windows) has no parent. On Windows HOME is the home
    // folder the app gives its tools (the profile folder).
    #[cfg(windows)]
    let home = home
        .map(|h| text("the home folder", &h).map(PathBuf::from))
        .transpose()?;
    if within.parent().is_none() || home.as_ref().is_some_and(|h| h.starts_with(&within)) {
        return Err(Error::invalid_request(
            "the within folder cannot be /, the home folder or a folder above it",
        ));
    }
    let file = resolve("the file to open", "file")?;
    let meta = std::fs::metadata(&file).map_err(|e| Error::not_found(e.to_string()))?;
    if !meta.is_file() {
        return Err(Error::invalid_request(
            "the file to open must be a regular file",
        ));
    }
    #[cfg(unix)]
    if meta.permissions().mode() & 0o111 != 0 {
        return Err(Error::invalid_request(
            "the file to open must not be executable",
        ));
    }
    if !file.starts_with(&within) {
        return Err(Error::invalid_request(
            "the file to open must be inside its within folder",
        ));
    }
    folders_own(&file, &within)?;
    opened(&file, &within, folders)
}

/// An open launch of a checked file: on Windows and macOS with the system's opener (`os::opener`).
#[cfg(not(target_os = "linux"))]
fn opened(file: &Path, within: &Path, _: &[PathBuf]) -> Result<Value, Error> {
    let (opener, argv) = os::opener(file)?;
    let file = text("the file to open", file)?;
    Ok(json!({
        "executable": opener,
        "argv": argv,
        "cwd": text("the file's folder", Path::new(&file).parent().unwrap_or(within))?,
        "file": file,
        "within": text("the within folder", within)?,
    }))
}

/// An open launch of a checked file: on Linux the first of `OPENERS` found in folders.
#[cfg(target_os = "linux")]
fn opened(file: &Path, within: &Path, folders: &[PathBuf]) -> Result<Value, Error> {
    let (path, flags) = OPENERS
        .iter()
        .find_map(|(name, flags)| find(name, folders).map(|p| (p, *flags)))
        .ok_or_else(|| Error::not_found("no opener (xdg-open or gio) found"))?;
    let file = text("the file to open", file)?;
    let cwd = text(
        "the file's folder",
        Path::new(&file).parent().unwrap_or(Path::new("/")),
    )?;
    let mut argv: Vec<String> = flags.iter().map(|f| f.to_string()).collect();
    argv.push(file.clone());
    Ok(json!({
        "executable": text("the opener", &path)?,
        "argv": argv,
        "cwd": cwd,
        "file": file,
        "within": text("the within folder", within)?,
    }))
}

/// `prepare`: a terminal launch runs `<terminal> <flags> <program> <args>`, naming the program it
/// runs (`program`) so the host binds it too; a direct launch runs `<program> <args>`; an open
/// launch runs an opener with the file it opens (`open`).
pub fn prepare(input: &Value) -> Result<Value, Error> {
    if input["kind"] == "open" {
        return open(input);
    }
    for k in ["program", "cwd", "search_path"] {
        if input.get(k).is_none() {
            return Err(Error::invalid_request(format!(
                "a terminal or direct launch names its {k}"
            )));
        }
    }
    #[cfg(not(target_os = "linux"))]
    if input["kind"] == "direct" {
        if let Some(app) = os::app(input)? {
            return Ok(app);
        }
    }
    let folders = folders(input);
    let program = program(input, &folders)?;
    let mut args: Vec<String> = input["args"]
        .as_array()
        .into_iter()
        .flatten()
        .map(|a| {
            a.as_str()
                .map(String::from)
                .ok_or_else(|| Error::invalid_request("args are strings"))
        })
        .collect::<Result<_, _>>()?;
    if args.iter().any(|a| a.contains('\0')) {
        return Err(Error::invalid_request("an argument holds a NUL"));
    }
    let cwd = input["cwd"].as_str().unwrap_or_default();
    if !Path::new(cwd).is_absolute() || !Path::new(cwd).is_dir() {
        return Err(Error::invalid_request(format!(
            "the working folder {cwd:?} is not an existing absolute folder"
        )));
    }
    // On Windows the folder's final path, plain, refused when its plain form is another folder.
    #[cfg(windows)]
    let cwd = &os::working_folder(cwd)?;
    // On Windows a command shim becomes what it runs (Node and its script, or its program), and a
    // program that reads its command line its own way takes no arguments.
    #[cfg(windows)]
    let program = {
        let (program, before) = os::resolve(&program, &folders)?;
        // Judged and confirmed by its final path: a link or short name for cmd.exe is cmd.exe.
        let program = os::canonical("the program", &program)?;
        if !executable(&program) {
            return Err(Error::not_found(format!(
                "the program {} is not an executable file",
                program.display()
            )));
        }
        args.splice(0..0, before);
        os::own_parser(&program, &args)?;
        program
    };
    #[cfg(unix)]
    let _ = &mut args;
    #[cfg(windows)]
    let program_text = text("the program", &program)?;
    #[cfg(unix)]
    let program_text = program.to_string_lossy().into_owned();
    match input["kind"].as_str() {
        Some("direct") => Ok(json!({"executable": program_text, "argv": args, "cwd": cwd})),
        #[cfg(not(target_os = "linux"))]
        Some("terminal") => {
            let wanted = input["terminal"].as_str().unwrap_or(AUTOMATIC);
            let (executable, argv) = os::terminal(wanted, &program_text, &args)?;
            Ok(json!({"executable": executable, "argv": argv, "cwd": cwd, "program": program_text}))
        }
        #[cfg(target_os = "linux")]
        Some("terminal") => {
            let wanted = input["terminal"].as_str().unwrap_or(AUTOMATIC);
            let (t, path) = TERMINALS
                .iter()
                .filter(|t| wanted == AUTOMATIC || t.id == wanted)
                .find_map(|t| find(t.id, &folders).map(|p| (t, p)))
                .ok_or_else(|| {
                    if wanted == AUTOMATIC {
                        Error::not_found("no terminal found on the PATH")
                    } else if TERMINALS.iter().any(|t| t.id == wanted) {
                        Error::not_found(format!("the terminal {wanted} is not on the PATH"))
                    } else {
                        Error::invalid_request(format!("no terminal {wanted}"))
                    }
                })?;
            let mut argv: Vec<String> = t.flags.iter().map(|f| f.to_string()).collect();
            argv.push(program_text.clone());
            argv.extend(args);
            Ok(json!({"executable": path, "argv": argv, "cwd": cwd, "program": program_text}))
        }
        _ => Err(Error::invalid_request(
            "a launch's kind is terminal, direct or open",
        )),
    }
}

#[cfg(all(test, target_os = "macos"))]
#[path = "recipe_macos_tests.rs"]
mod macos_tests;
#[cfg(all(test, target_os = "linux"))]
#[path = "recipe_tests.rs"]
mod tests;
#[cfg(all(test, windows))]
#[path = "recipe_windows_tests.rs"]
mod windows_tests;
