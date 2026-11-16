//! Windows' launch recipes. The terminal is a console window of the launch's own, which the
//! execution program runs the program in (`launch.rs`): Windows shows it in the person's default
//! terminal app (Windows Terminal or the console host). pluxy hands no terminal or shell a command
//! line: cmd.exe and wt.exe read theirs their own way. Programs are PE images (`.exe`, `.com`);
//! a batch file runs only as the npm command shim it is (`shim.rs`).

use super::{text, AUTOMATIC};
use crate::shim::{self, Shim};
use pluxy_program::Error;
use serde_json::{json, Value};
use std::io::Read;
use std::path::{Path, PathBuf};

/// A Windows terminal kind: the console window the program runs in, then (after it ends) the
/// shell the window stays at, if any, from Windows' System32 folder.
struct Kind {
    id: &'static str,
    label: &'static str,
    shell: Option<&'static str>,
}

/// In Automatic's order: PowerShell first, as the old desktop app's Automatic.
const KINDS: &[Kind] = &[
    Kind {
        id: "powershell",
        label: "PowerShell",
        shell: Some(r"WindowsPowerShell\v1.0\powershell.exe"),
    },
    Kind {
        id: "cmd",
        label: "Command Prompt",
        shell: Some("cmd.exe"),
    },
    Kind {
        id: "console",
        label: "Console window",
        shell: None,
    },
];

/// The extensions a program is looked up with, in Windows' default PATHEXT order, restricted to
/// what pluxy runs: PE images, and batch files read as shims.
const EXTENSIONS: &[&str] = &["com", "exe", "bat", "cmd"];

/// Programs that read their command line their own way (not by the C runtime's rules), so the
/// arguments confirmed would not be the ones they see: run only with none.
const OWN_PARSERS: &[&str] = &[
    "cmd.exe",
    "wt.exe",
    "conhost.exe",
    "explorer.exe",
    "rundll32.exe",
    "mshta.exe",
    "wscript.exe",
    "cscript.exe",
    "msiexec.exe",
];

/// The files an open launch opens, by extension (Windows picks the app by it), with the content
/// type shown: the plain-text types of Linux's list.
pub const OPEN_TYPES: &[(&str, &str)] = &[
    ("txt", "text/plain"),
    ("json", "application/json"),
    ("jsonl", "application/jsonl"),
    ("ndjson", "application/x-ndjson"),
    ("md", "text/markdown"),
    ("log", "text/x-log"),
];

fn ext(p: &Path) -> Option<String> {
    p.extension()
        .and_then(|e| e.to_str())
        .map(str::to_ascii_lowercase)
}

/// Whether p is an executable file: a regular file named `.exe` or `.com` that is a PE image
/// (starts with `MZ`).
pub fn executable(p: &Path) -> bool {
    let named = matches!(ext(p).as_deref(), Some("exe" | "com"));
    let mut magic = [0u8; 2];
    named
        && std::fs::metadata(p).is_ok_and(|m| m.is_file())
        && std::fs::File::open(p).is_ok_and(|mut f| f.read_exact(&mut magic).is_ok())
        && magic == *b"MZ"
}

/// Whether p is a batch file (`.bat`, `.cmd`).
pub fn batch(p: &Path) -> bool {
    matches!(ext(p).as_deref(), Some("bat" | "cmd")) && p.is_file()
}

/// The first program named name in folders: name as given when it has an extension, else name
/// with each of `EXTENSIONS` in turn, in each folder in order.
pub fn find(name: &str, folders: &[PathBuf]) -> Option<PathBuf> {
    let ok = |p: &PathBuf| executable(p) || batch(p);
    if Path::new(name).extension().is_some() {
        return folders.iter().map(|d| d.join(name)).find(ok);
    }
    folders
        .iter()
        .flat_map(|d| {
            EXTENSIONS
                .iter()
                .map(move |e| d.join(format!("{name}.{e}")))
        })
        .find(ok)
}

/// What a program found or named runs: itself, or for a command shim the program it names (a
/// native one) or Node with the arguments before its script and the script. Node is the
/// `node.exe` beside the shim, else the first on folders.
pub fn resolve(program: &Path, folders: &[PathBuf]) -> Result<(PathBuf, Vec<String>), Error> {
    if !batch(program) {
        return Ok((program.to_path_buf(), Vec::new()));
    }
    let shown = program.display();
    let refuse = || Error::invalid_request(format!("{shown} {}", shim::NOT_A_SHIM));
    let mut bytes = Vec::new();
    std::fs::File::open(program)
        .and_then(|f| f.take(64 * 1024 + 1).read_to_end(&mut bytes))
        .map_err(|e| Error::not_found(format!("{shown}: {e}")))?;
    if bytes.len() > 64 * 1024 {
        return Err(refuse());
    }
    let text = String::from_utf8(bytes).map_err(|_| refuse())?;
    let dir = std::fs::canonicalize(program.parent().unwrap_or(Path::new(".")))
        .map_err(|e| Error::not_found(format!("{shown}: {e}")))?;
    // The shim's target, resolved, must be a file inside the shim's folder.
    let inside = |target: &str| -> Result<PathBuf, Error> {
        let t = std::fs::canonicalize(dir.join(target))
            .map_err(|e| Error::not_found(format!("{shown} runs {target}: {e}")))?;
        if !t.starts_with(&dir) || !t.is_file() {
            return Err(refuse());
        }
        Ok(PathBuf::from(text_of(&t)?))
    };
    match shim::parse(&text).ok_or_else(refuse)? {
        Shim::Native { target } => {
            let p = inside(&target)?;
            if !executable(&p) {
                return Err(Error::invalid_request(format!(
                    "{shown} runs {}, which is not an executable file",
                    p.display()
                )));
            }
            Ok((p, Vec::new()))
        }
        Shim::Node { args, target } => {
            let script = inside(&target)?;
            let beside = dir.join("node.exe");
            let node = if executable(&beside) {
                beside
            } else {
                find("node.exe", folders)
                    .filter(|p| executable(p))
                    .ok_or_else(|| {
                        Error::not_found(format!(
                            "{shown} runs Node, and node.exe is not on the PATH"
                        ))
                    })?
            };
            let mut before = args;
            before.push(text_of(&script)?);
            Ok((node, before))
        }
    }
}

fn text_of(p: &Path) -> Result<String, Error> {
    text("a path", p)
}

/// The final path of p (links, junctions and short 8.3 names resolved), in its plain form: what
/// a launch confirms and runs, and what `own_parser` judges.
pub fn canonical(what: &str, p: &Path) -> Result<PathBuf, Error> {
    let r = std::fs::canonicalize(p)
        .map_err(|e| Error::not_found(format!("{what} {}: {e}", p.display())))?;
    Ok(PathBuf::from(text(what, &r)?))
}

/// A launch's working folder: an existing absolute folder, as its final plain path.
pub fn working_folder(cwd: &str) -> Result<String, Error> {
    let refuse = || {
        Error::invalid_request(format!(
            "the working folder {cwd:?} is not an existing absolute folder"
        ))
    };
    if !Path::new(cwd).is_absolute() {
        return Err(refuse());
    }
    let d = std::fs::canonicalize(cwd).map_err(|_| refuse())?;
    if !d.is_dir() {
        return Err(refuse());
    }
    text("the working folder", &d)
}

/// Refused when program reads its command line its own way and args is not empty.
pub fn own_parser(program: &Path, args: &[String]) -> Result<(), Error> {
    let name = program
        .file_name()
        .and_then(|n| n.to_str())
        .unwrap_or_default()
        .to_ascii_lowercase();
    if !args.is_empty() && OWN_PARSERS.contains(&name.as_str()) {
        return Err(Error::invalid_request(format!(
            "{name} reads its command line its own way, so the arguments you confirm would not \
             be the ones it runs; it starts only with no arguments"
        )));
    }
    Ok(())
}

/// Windows' System32 folder, as Windows reports it (`GetSystemDirectoryW`): never from the
/// environment, which for a launch is the launch's own (its `SystemRoot` could name any folder).
fn system32() -> Option<PathBuf> {
    use windows_sys::Win32::System::SystemInformation::GetSystemDirectoryW;
    let mut buf = [0u16; 261];
    // SAFETY: buf holds buf.len() characters.
    let n = unsafe { GetSystemDirectoryW(buf.as_mut_ptr(), buf.len() as u32) } as usize;
    if n == 0 || n >= buf.len() {
        return None;
    }
    let d = PathBuf::from(String::from_utf16(&buf[..n]).ok()?);
    (d.is_absolute() && d.is_dir()).then_some(d)
}

fn shell(k: &Kind) -> Option<PathBuf> {
    k.shell
        .map(|s| system32().map(|d| d.join(s)))
        .map(|p| p.filter(|p| p.is_file()))?
}

/// Whether kind k is there: the console window always; a shell when System32 has it.
fn present(k: &Kind) -> bool {
    k.shell.is_none() || shell(k).is_some()
}

/// The terminals there are, after Automatic.
pub fn terminals() -> Vec<Value> {
    KINDS
        .iter()
        .filter(|k| present(k))
        .map(|k| match shell(k) {
            Some(p) => json!({"id": k.id, "label": k.label, "path": p}),
            None => json!({"id": k.id, "label": k.label}),
        })
        .collect()
}

/// The execution program's own path, plain: the terminal and opener of Windows' launches.
pub fn this_program() -> Result<String, Error> {
    let p = std::env::current_exe()
        .and_then(std::fs::canonicalize)
        .map_err(|e| Error::internal(format!("the execution program's path: {e}")))?;
    text("the execution program", &p)
}

/// A terminal launch of program with args in terminal wanted: the execution program in its
/// `terminal` mode.
pub fn terminal(
    wanted: &str,
    program: &str,
    args: &[String],
) -> Result<(String, Vec<String>), Error> {
    let k = KINDS
        .iter()
        .filter(|k| wanted == AUTOMATIC || k.id == wanted)
        .find(|k| present(k))
        .ok_or_else(|| {
            if KINDS.iter().any(|k| k.id == wanted) {
                Error::not_found(format!("the terminal {wanted} is not on this computer"))
            } else {
                Error::invalid_request(format!("no terminal {wanted}"))
            }
        })?;
    let mut argv: Vec<String> = ["terminal", "--with", k.id, "--", program]
        .iter()
        .map(|s| s.to_string())
        .collect();
    argv.extend(args.iter().cloned());
    Ok((this_program()?, argv))
}

/// The shell terminal kind id stays at once its program ends, for `launch.rs`.
pub fn kind_shell(id: &str) -> Option<Option<PathBuf>> {
    KINDS
        .iter()
        .find(|k| k.id == id)
        .map(|k| k.shell.and_then(|_| shell(k)))
}

/// The opener of a resolved file, after Windows' rules for it: a type of `OPEN_TYPES`, an app
/// that opens that type, no alternate stream, a local path shorter than 260 characters. The
/// opener is the execution program in its `open` mode.
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
    if shown.starts_with(r"\\") {
        return Err(Error::invalid_request(
            "the file to open must be on a local drive, not a network path",
        ));
    }
    if shown[2..].contains(':') {
        return Err(Error::invalid_request(
            "the file to open must not name an alternate data stream",
        ));
    }
    // MAX_PATH counts UTF-16 units: a character outside the Basic Multilingual Plane is two.
    if shown.encode_utf16().count() >= 260 {
        return Err(Error::invalid_request(
            "the file to open must have a path shorter than 260 characters",
        ));
    }
    if !opens(&e) {
        return Err(Error::not_found(format!(
            "no app opens .{e} files on this computer: choose one in Windows' default apps settings"
        )));
    }
    Ok((this_program()?, vec!["open".into(), "--".into(), shown]))
}

/// Whether Windows has an app that opens files with extension e (no dot): its file type's `open`
/// verb has a command or a `DelegateExecute` handler. Unknown types' defaults (the "Open with"
/// dialog) do not count.
pub fn opens(e: &str) -> bool {
    use windows_sys::Win32::UI::Shell::{
        AssocQueryStringW, ASSOCF_INIT_IGNOREUNKNOWN, ASSOCF_NOTRUNCATE, ASSOCSTR_COMMAND,
        ASSOCSTR_DELEGATEEXECUTE,
    };
    let class = wide(&format!(".{e}"));
    let verb = wide("open");
    [ASSOCSTR_COMMAND, ASSOCSTR_DELEGATEEXECUTE]
        .iter()
        .any(|what| {
            let mut buf = vec![0u16; 4096];
            let mut len = buf.len() as u32;
            // SAFETY: the strings are NUL-terminated and buf holds len characters.
            let hr = unsafe {
                AssocQueryStringW(
                    ASSOCF_INIT_IGNOREUNKNOWN | ASSOCF_NOTRUNCATE,
                    *what,
                    class.as_ptr(),
                    verb.as_ptr(),
                    buf.as_mut_ptr(),
                    &mut len,
                )
            };
            hr == 0 && len > 1
        })
}

/// s as a NUL-terminated wide string.
pub fn wide(s: &str) -> Vec<u16> {
    s.encode_utf16().chain([0]).collect()
}

/// An app a direct launch names instead of a program: `shell:AppsFolder\<AUMID>`, a packaged
/// (Store) app such as the Codex app, started by the execution program's `app` mode. It takes
/// no arguments; its environment is the system's (the launch's is refused in `launch`).
pub fn app(input: &Value) -> Result<Option<Value>, Error> {
    let Some(aumid) = input["program"]
        .as_str()
        .and_then(|p| p.strip_prefix(r"shell:AppsFolder\"))
    else {
        return Ok(None);
    };
    if !aumid_ok(aumid) {
        return Err(Error::invalid_request(format!(
            "{aumid} is not an app's ID (family_publisher!app)"
        )));
    }
    if input["args"].as_array().is_some_and(|a| !a.is_empty()) || input.get("override").is_some() {
        return Err(Error::invalid_request(
            "an app takes no arguments and no override",
        ));
    }
    let cwd = working_folder(input["cwd"].as_str().unwrap_or_default())?;
    Ok(Some(json!({
        "executable": this_program()?,
        "argv": ["app", "--", aumid],
        "cwd": cwd,
    })))
}

/// Whether a is an AppUserModelID of a packaged app: `<name>_<publisher hash>!<app>`, the
/// publisher hash 13 lower-case letters and digits, names of letters, digits, `.` and `-`.
pub fn aumid_ok(a: &str) -> bool {
    let Some((family, app)) = a.split_once('!') else {
        return false;
    };
    let Some((name, publisher)) = family.rsplit_once('_') else {
        return false;
    };
    let word = |s: &str, extra: &str| {
        !s.is_empty()
            && s.chars()
                .all(|c| c.is_ascii_alphanumeric() || extra.contains(c))
    };
    word(name, ".-")
        && publisher.len() == 13
        && publisher
            .chars()
            .all(|c| c.is_ascii_digit() || c.is_ascii_lowercase())
        && word(app, ".")
}
