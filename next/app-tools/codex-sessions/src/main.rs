//! pluxy/codex-sessions: reads Codex's local sessions for pluxy's desktop app, read-only. Its
//! operations (README.md): `list` (read), `detail` and `export` (disclose: each call asks the
//! person), `open` (read: opens a rollout in the person's own app through `pluxy/execution`,
//! whose launch the person confirms) and `settings` (write: the Codex home folder, kept in the
//! tool's data folder).

mod reader;

use pluxy_program::{run, Call, Error, Handler, SaveOutcome};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

/// The settings file in the tool's data folder.
const SETTINGS: &str = "settings.json";

struct Sessions {
    /// The Codex home to read instead of the one the app's environment gives, for tests.
    home: Option<PathBuf>,
}

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
    if !home.is_empty() && !Path::new(home).is_absolute() {
        return Err(Error::invalid_request("the Codex home must be a full path"));
    }
    let tmp = data.join(format!(".{SETTINGS}.tmp"));
    let _ = std::fs::remove_file(&tmp);
    let fail = |e: std::io::Error| Error::unavailable(format!("could not save the settings: {e}"));
    let mut f = pluxy_files::create_new_private(&tmp).map_err(fail)?;
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

/// The error a call of `detail`, `export` or `open` ends with.
fn failure(e: reader::DetailError) -> Error {
    match e {
        reader::DetailError::Invalid(m) => Error::invalid_request(m),
        reader::DetailError::NotFound(m) => Error::not_found(m),
        reader::DetailError::Unavailable(m) => Error::unavailable(m),
        reader::DetailError::Cancelled => Error::cancelled(),
    }
}

/// The program that prepares and starts a launch, which this one imports (`prepare` and `launch`).
const EXECUTION: &str = "pluxy/execution";

/// The path's text: the host confirms UTF-8 paths only.
fn utf8(p: &Path) -> Result<String, Error> {
    p.to_str()
        .map(String::from)
        .ok_or_else(|| Error::unavailable("the session's rollout path is not UTF-8"))
}

/// Opens the rollout file inside within in the person's own app, through execution: it prepares
/// an open launch of exactly these resolved paths, then starts that launch as prepared, detached,
/// with no environment, standard input or secret. The call that starts it is the one the person
/// confirms (the confirmation names this program and execution, the file, its type, the folder and
/// the opener); this program never spawns anything, and a refusal or a cancelled confirmation is
/// the call's error.
fn open_in_local_app(call: &Call, file: &Path, within: &Path) -> Result<Value, Error> {
    let (file, within) = (utf8(file)?, utf8(within)?);
    #[allow(unused_mut)]
    let mut request = json!({"kind": "open", "file": file, "within": within});
    // The native test's stand-in opener, from a folder only the test names (never in a release).
    #[cfg(feature = "native-test")]
    if let Some(folder) = test_opener_folder()? {
        request["search_path"] = json!([folder]);
    }
    let mut launch = call.call(EXECUTION, "prepare", request)?;
    if launch["file"] != file || launch["within"] != within {
        return Err(Error::internal(
            "the prepared launch does not open the rollout asked for",
        ));
    }
    launch["lifetime"] = json!("detached");
    call.call(EXECUTION, "launch", launch)?;
    Ok(json!({"outcome": "opened"}))
}

/// The folder the native test's stand-in opener is in, named by the file `opener-folder` in the
/// tool's data folder, which only the test writes.
#[cfg(feature = "native-test")]
fn test_opener_folder() -> Result<Option<String>, Error> {
    let Ok(data) = data_dir() else {
        return Ok(None);
    };
    match std::fs::read_to_string(data.join("opener-folder")) {
        Ok(f) => Ok(Some(f.trim().to_string()).filter(|f| !f.is_empty())),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(Error::unavailable(format!("the opener folder: {e}"))),
    }
}

impl Sessions {
    /// The Codex home to read, and whether it is custom.
    fn home(&self) -> Result<(PathBuf, bool), Error> {
        match &self.home {
            Some(home) => Ok((home.clone(), false)),
            None => codex_home(),
        }
    }
}

impl Handler for Sessions {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        match call.op.as_str() {
            "list" => {
                let (home, _) = self.home()?;
                Ok(reader::list(
                    &home,
                    count(&call.input, "offset", 0),
                    count(&call.input, "limit", reader::DEFAULT_PAGE_SIZE),
                )
                .to_json())
            }
            "detail" => {
                let (home, _) = self.home()?;
                let id = call.input["id"].as_str().unwrap_or_default();
                reader::detail(
                    &home,
                    id,
                    count(&call.input, "offset", 0),
                    count(&call.input, "limit", reader::DEFAULT_PAGE_SIZE),
                    &|| call.cancelled(),
                )
                .map_err(failure)
            }
            // The rollout's bytes go to the host's save dialog and never into the answer, which
            // says only whether the person saved the file.
            "export" => {
                let (home, _) = self.home()?;
                let id = call.input["id"].as_str().unwrap_or_default();
                let (id, bytes) = reader::export_bytes(&home, id).map_err(failure)?;
                let today = SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .map_or(0, |d| d.as_secs());
                Ok(
                    match call.save_file(&reader::export_name(&id, today), &bytes)? {
                        SaveOutcome::Saved { size } => json!({"outcome": "saved", "size": size}),
                        SaveOutcome::Cancelled => json!({"outcome": "cancelled"}),
                    },
                )
            }
            // Only the rollout's resolved path and its folder leave this program, for execution to
            // prepare and the host to confirm; the answer says only that the launch was started.
            "open" => {
                let (home, _) = self.home()?;
                let id = call.input["id"].as_str().unwrap_or_default();
                let (file, within) = reader::open_target(&home, id).map_err(failure)?;
                open_in_local_app(call, &file, &within)
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
    if let Err(e) = run(Sessions { home: None }) {
        eprintln!("codex-sessions: {e}");
        std::process::exit(1);
    }
}

#[cfg(test)]
mod export_tests;
#[cfg(test)]
mod open_tests;
