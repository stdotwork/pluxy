//! Launch recipes: the terminals pluxy knows on Linux and how a typed launch becomes the
//! executable, argv and cwd the person confirms. Nothing here starts a process.

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

/// `prepare`: a terminal launch runs `<terminal> <flags> <program> <args>`, naming the program it
/// runs (`program`) so the host binds it too; a direct launch runs `<program> <args>`.
pub fn prepare(input: &Value) -> Result<Value, Error> {
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
            "a launch's kind is terminal or direct",
        )),
    }
}

#[cfg(test)]
#[path = "recipe_tests.rs"]
mod tests;
