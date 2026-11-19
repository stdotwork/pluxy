//! `prepare`: the exact `wsl.exe` launch of a program inside a distribution as a user, for the
//! person to confirm and the host to spawn. It answers `{"launch", "kind"}`: `launch` is exactly
//! `pluxy/execution`'s `launch` input (`executable`, then `argv` without a program name, a Windows
//! `cwd`, and the `program` shown), which the host passes on as it is; `kind` (`terminal` or
//! `direct`) stays beside it, and the host applies it before it calls execution: a terminal launch
//! gets a console window of its own, no standard input and no managed lifetime.
//! The program and folder are found inside the distribution, as that user, by one fixed script;
//! no login shell is started, so a program only an rc file puts on the PATH is reached by its
//! full path (`override`).

use pluxy_program::Error;
use serde_json::{json, Value};

/// Run as `sh -c PREPARE sh <program> <override> <cwd>`: with an override, that file if it is an
/// executable file; else the program found by `command -v` on the PATH `wsl.exe --exec` gives,
/// which must be an absolute path to an executable file; then the folder (the user's home when
/// none is given), absolute and enterable. It prints NUL-separated fields: `ok`, the program and
/// the folder, or one word naming what was not found.
pub const PREPARE: &str = r#"p=$1 o=$2 c=$3
if [ -n "$o" ]; then
  if [ -f "$o" ] && [ -x "$o" ]; then p=$o; else printf 'override\0'; exit 0; fi
else
  p=$(command -v "$p") || p=
  case $p in /*) [ -f "$p" ] && [ -x "$p" ] || p= ;; *) p= ;; esac
  if [ -z "$p" ]; then printf 'program\0'; exit 0; fi
fi
[ -n "$c" ] || c=$HOME
case $c in /*) ;; *) printf 'cwd\0'; exit 0 ;; esac
if [ -d "$c" ] && (cd "$c") 2>/dev/null; then printf 'ok\0%s\0%s\0' "$p" "$c"; else printf 'cwd\0'; fi"#;

/// What a launch asks for, checked: the program's bare name or an absolute override, its
/// arguments and the folder (none: the user's home).
#[derive(Debug, PartialEq, Eq)]
pub struct Request {
    pub kind: String,
    pub program: String,
    pub over: String,
    pub args: Vec<String>,
    pub cwd: String,
}

fn no_nul(s: &str, what: &str) -> Result<(), Error> {
    if s.contains('\0') {
        Err(Error::invalid_request(format!("{what} holds a NUL")))
    } else {
        Ok(())
    }
}

impl Request {
    pub fn from_input(input: &Value) -> Result<Request, Error> {
        let s = |k: &str| input[k].as_str().unwrap_or_default().to_string();
        let kind = s("kind");
        if kind != "terminal" && kind != "direct" {
            return Err(Error::invalid_request(
                "a WSL launch's kind is terminal or direct",
            ));
        }
        let (program, over, cwd) = (s("program"), s("override"), s("cwd"));
        if over.is_empty() {
            if program.is_empty() {
                return Err(Error::invalid_request("name the program, or its full path"));
            }
            if program.contains('/') || program.starts_with('-') {
                return Err(Error::invalid_request(
                    "the program is a name found on the PATH; give a full path as the override",
                ));
            }
        } else if !over.starts_with('/') {
            return Err(Error::invalid_request(
                "the program's full path inside WSL starts with /",
            ));
        }
        if !cwd.is_empty() && !cwd.starts_with('/') {
            return Err(Error::invalid_request(
                "the working folder inside WSL is a path starting with /",
            ));
        }
        let args: Vec<String> = match &input["args"] {
            Value::Null => Vec::new(),
            Value::Array(a) => a
                .iter()
                .map(|v| {
                    v.as_str()
                        .map(String::from)
                        .ok_or_else(|| Error::invalid_request("args are strings"))
                })
                .collect::<Result<_, _>>()?,
            _ => return Err(Error::invalid_request("args are a list")),
        };
        no_nul(&program, "the program")?;
        no_nul(&over, "the program's path")?;
        no_nul(&cwd, "the working folder")?;
        if args.iter().any(|a| a.contains('\0')) {
            return Err(Error::invalid_request("an argument holds a NUL"));
        }
        Ok(Request {
            kind,
            program,
            over,
            args,
            cwd,
        })
    }

    /// The `wsl.exe` arguments that run [`PREPARE`] for this request as user in distro.
    pub fn resolve_args(&self, distro: &str, user: &str) -> Vec<String> {
        let mut a: Vec<String> = [
            "-d", distro, "-u", user, "--exec", "/bin/sh", "-c", PREPARE, "sh",
        ]
        .iter()
        .map(|s| s.to_string())
        .collect();
        a.extend([self.program.clone(), self.over.clone(), self.cwd.clone()]);
        a
    }

    /// The launch from what [`PREPARE`] printed: `wsl.exe` (at `executable`) runs the program found
    /// in the folder found, as user in distro, with the request's arguments; `cwd` is the Windows
    /// folder `wsl.exe` starts in.
    pub fn launch(
        &self,
        printed: &[u8],
        distro: &str,
        user: &str,
        executable: &str,
        cwd: &str,
    ) -> Result<Value, Error> {
        let fields: Vec<&[u8]> = printed.split(|b| *b == 0).collect();
        let text = |b: &[u8]| String::from_utf8(b.to_vec()).ok();
        match fields.first().copied() {
            Some(b"ok") if fields.len() == 4 && fields[3].is_empty() => {}
            Some(b"override") => {
                return Err(Error::not_found(format!(
                    "{} is not an executable file in WSL {distro} for {user}",
                    self.over
                )))
            }
            Some(b"program") => {
                return Err(Error::not_found(format!(
                    "{} is not on the PATH of {user} in WSL {distro} (programs set up in a shell \
                     profile are not found: give the full path)",
                    self.program
                )))
            }
            Some(b"cwd") => {
                return Err(Error::not_found(format!(
                    "the working folder {} is not a folder {user} can open in WSL {distro}",
                    if self.cwd.is_empty() {
                        "(home)"
                    } else {
                        &self.cwd
                    }
                )))
            }
            _ => {
                return Err(Error::unavailable(
                    "the distribution's answer is unreadable",
                ))
            }
        }
        let (Some(program), Some(folder)) = (text(fields[1]), text(fields[2])) else {
            return Err(Error::unavailable(
                "the program's or folder's path is not UTF-8",
            ));
        };
        if !program.starts_with('/') || !folder.starts_with('/') {
            return Err(Error::unavailable(
                "the distribution's answer is unreadable",
            ));
        }
        let mut argv: Vec<String> = [
            "-d", distro, "-u", user, "--cd", &folder, "--exec", &program,
        ]
        .iter()
        .map(|s| s.to_string())
        .collect();
        argv.extend(self.args.iter().cloned());
        Ok(json!({
            "launch": {"executable": executable, "argv": argv, "cwd": cwd, "program": program},
            "kind": self.kind
        }))
    }
}

#[cfg(test)]
#[path = "prepare_tests.rs"]
mod tests;
