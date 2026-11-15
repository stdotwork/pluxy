//! Launch recipes: the terminals pluxy knows on Linux, the openers that open a file in the
//! person's own app, and how a typed launch becomes the executable, argv and cwd the person
//! confirms. Nothing here starts a process.

use pluxy_program::Error;
use serde_json::{json, Value};
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};

/// A terminal: its ID (its program's name), its label and the arguments before the command it
/// runs. The order is Automatic's, as in the old desktop app; `xfce4-terminal` and `mate-terminal`
/// take the rest of the command line after `-x` (their `-e` takes one command string).
pub struct Terminal {
    pub id: &'static str,
    pub label: &'static str,
    pub flags: &'static [&'static str],
}

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
pub const OPENERS: &[(&str, &[&str])] = &[("xdg-open", &[]), ("gio", &["open"])];

/// Where an open launch looks for its opener when the caller names no search path (a tool has no
/// launch environment of its own): the system's program folders.
pub const SYSTEM_PATH: &[&str] = &[
    "/usr/local/bin",
    "/usr/bin",
    "/bin",
    "/run/current-system/sw/bin",
];

/// Whether p is an executable regular file.
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
        .filter(|d| d.starts_with('/'))
        .map(PathBuf::from)
        .collect()
}

/// The first executable named name in folders.
fn find(name: &str, folders: &[PathBuf]) -> Option<PathBuf> {
    folders.iter().map(|d| d.join(name)).find(|p| executable(p))
}

/// `terminals`: Automatic, then each terminal found on the search path, with its path.
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
        if !executable(&p) {
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
    if name.starts_with('/') {
        return absolute("the program", name);
    }
    if name.is_empty() || name.contains('/') {
        return Err(Error::invalid_request(
            "the program is a name or an absolute path",
        ));
    }
    find(name, folders).ok_or_else(|| Error::not_found(format!("{name} is not on the PATH")))
}

/// A path's text, refused when it is not UTF-8: the host confirms UTF-8 paths only.
fn text(what: &str, p: &Path) -> Result<String, Error> {
    p.to_str()
        .map(String::from)
        .ok_or_else(|| Error::invalid_request(format!("{what} is not a UTF-8 path")))
}

/// An open launch: `<opener> <file>` (or `gio open <file>`) in the file's folder, the opener the
/// first of `OPENERS` found on the search path (else in `SYSTEM_PATH`). It names the file (the
/// resolved path, which is its argument) and the folder it must be inside (`within`, resolved),
/// which the host checks, shows and binds again. The file must be a regular file with no execute
/// bit inside that folder; the folder must not be `/` or the home folder.
fn open(input: &Value) -> Result<Value, Error> {
    if let Some((k, _)) = input
        .as_object()
        .into_iter()
        .flatten()
        .find(|(k, _)| !["kind", "file", "within", "search_path"].contains(&k.as_str()))
    {
        return Err(Error::invalid_request(format!(
            "an open launch takes file, within and search_path, not {k}"
        )));
    }
    let resolve = |what: &str, k: &str| -> Result<PathBuf, Error> {
        let p = input[k].as_str().unwrap_or_default();
        if !p.starts_with('/') {
            return Err(Error::invalid_request(format!(
                "{what} must be an absolute path"
            )));
        }
        std::fs::canonicalize(p).map_err(|e| Error::not_found(format!("{what} {p}: {e}")))
    };
    let within = resolve("the within folder", "within")?;
    if !within.is_dir() {
        return Err(Error::invalid_request(format!(
            "the within folder {} is not a folder",
            within.display()
        )));
    }
    let home = std::env::var_os("HOME").and_then(|h| std::fs::canonicalize(h).ok());
    if within == Path::new("/") || Some(&within) == home.as_ref() {
        return Err(Error::invalid_request(
            "the within folder cannot be / or the home folder",
        ));
    }
    let file = resolve("the file to open", "file")?;
    let meta = std::fs::metadata(&file).map_err(|e| Error::not_found(e.to_string()))?;
    if !meta.is_file() {
        return Err(Error::invalid_request(
            "the file to open must be a regular file",
        ));
    }
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
    let folders = match input.get("search_path") {
        Some(_) => folders(input),
        None => SYSTEM_PATH.iter().map(PathBuf::from).collect(),
    };
    let (path, flags) = OPENERS
        .iter()
        .find_map(|(name, flags)| find(name, &folders).map(|p| (p, *flags)))
        .ok_or_else(|| Error::not_found("no opener (xdg-open or gio) found"))?;
    let file = text("the file to open", &file)?;
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
        "within": text("the within folder", &within)?,
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
    let folders = folders(input);
    let program = program(input, &folders)?;
    let args: Vec<String> = input["args"]
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
    if !cwd.starts_with('/') || !Path::new(cwd).is_dir() {
        return Err(Error::invalid_request(format!(
            "the working folder {cwd:?} is not an existing absolute folder"
        )));
    }
    let program_text = program.to_string_lossy().into_owned();
    match input["kind"].as_str() {
        Some("direct") => Ok(json!({"executable": program_text, "argv": args, "cwd": cwd})),
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

#[cfg(test)]
#[path = "recipe_tests.rs"]
mod tests;
