//! pluxy/codex-sessions: reads Codex's local sessions for pluxy's desktop app, read-only. Its
//! operations (README.md): `list` (read), `detail` (disclose: each call asks the person) and
//! `settings` (write: the Codex home folder, kept in the tool's data folder).

mod reader;

use pluxy_program::{run, Call, Error, Handler};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};

/// The settings file in the tool's data folder.
const SETTINGS: &str = "settings.json";

struct Sessions;

/// The tool's data folder, which the host gives it.
fn data_dir() -> Result<PathBuf, Error> {
    std::env::var_os("PLUXY_TOOL_DATA_DIR")
        .filter(|d| !d.is_empty())
        .map(PathBuf::from)
        .ok_or_else(|| Error::unavailable("the app gave no data folder (PLUXY_TOOL_DATA_DIR)"))
}

/// The custom Codex home set with `settings`, if any.
fn custom_home(data: &Path) -> Result<Option<PathBuf>, Error> {
    match std::fs::read(data.join(SETTINGS)) {
        Ok(b) => {
            let v: Value = serde_json::from_slice(&b)
                .map_err(|e| Error::unavailable(format!("the settings file is broken: {e}")))?;
            Ok(v["codex_home"]
                .as_str()
                .filter(|s| !s.is_empty())
                .map(PathBuf::from))
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(Error::unavailable(format!("the settings file: {e}"))),
    }
}

/// The Codex home to read: the custom one, else `$HOME/.codex`; and whether it is custom.
fn codex_home() -> Result<(PathBuf, bool), Error> {
    if let Some(custom) = data_dir()
        .ok()
        .map(|d| custom_home(&d))
        .transpose()?
        .flatten()
    {
        return Ok((custom, true));
    }
    let home = std::env::var_os("HOME")
        .filter(|h| !h.is_empty())
        .ok_or_else(|| Error::unavailable("the app gave no home folder (HOME)"))?;
    Ok((Path::new(&home).join(".codex"), false))
}

/// Sets (or, with "", clears) the custom Codex home: written whole, readable only by this user.
fn set_home(data: &Path, home: &str) -> Result<(), Error> {
    use std::io::Write;
    use std::os::unix::fs::OpenOptionsExt;
    if !home.is_empty() && !Path::new(home).is_absolute() {
        return Err(Error::invalid_request("the Codex home must be a full path"));
    }
    let tmp = data.join(format!(".{SETTINGS}.tmp"));
    let _ = std::fs::remove_file(&tmp);
    let fail = |e: std::io::Error| Error::unavailable(format!("could not save the settings: {e}"));
    let mut f = std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(&tmp)
        .map_err(fail)?;
    f.write_all(json!({"codex_home": home}).to_string().as_bytes())
        .and_then(|_| f.sync_all())
        .map_err(fail)?;
    std::fs::rename(&tmp, data.join(SETTINGS)).map_err(fail)
}

fn count(input: &Value, key: &str, default: usize) -> usize {
    input[key]
        .as_u64()
        .and_then(|n| usize::try_from(n).ok())
        .unwrap_or(default)
}

impl Handler for Sessions {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        match call.op.as_str() {
            "list" => {
                let (home, _) = codex_home()?;
                Ok(reader::list(
                    &home,
                    count(&call.input, "offset", 0),
                    count(&call.input, "limit", reader::DEFAULT_PAGE_SIZE),
                )
                .to_json())
            }
            "detail" => {
                let (home, _) = codex_home()?;
                let id = call.input["id"].as_str().unwrap_or_default();
                reader::detail(
                    &home,
                    id,
                    count(&call.input, "offset", 0),
                    count(&call.input, "limit", reader::DEFAULT_PAGE_SIZE),
                    &|| call.cancelled(),
                )
                .map_err(|e| match e {
                    reader::DetailError::Invalid(m) => Error::invalid_request(m),
                    reader::DetailError::NotFound(m) => Error::not_found(m),
                    reader::DetailError::Unavailable(m) => Error::unavailable(m),
                    reader::DetailError::Cancelled => Error::cancelled(),
                })
            }
            "settings" => {
                let data = data_dir()?;
                set_home(&data, call.input["codex_home"].as_str().unwrap_or_default())?;
                let (home, custom) = codex_home()?;
                Ok(json!({"codex_home": home, "custom": custom}))
            }
            op => Err(Error::unsupported(format!("no operation {op}"))),
        }
    }
}

fn main() {
    if let Err(e) = run(Sessions) {
        eprintln!("codex-sessions: {e}");
        std::process::exit(1);
    }
}
