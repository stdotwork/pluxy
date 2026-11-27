//! pluxy/pi-setup: sets up Pi to use pluxy's core through the CLIProxyAPI provider adapter it
//! carries, manages that adapter, and starts Pi, for pluxy's desktop app (README.md). Its
//! operations: `preview` (read: pins Pi's three files in an agent folder and pluxy's copy of the
//! adapter, and reads them back, never the key), `apply` (write: installs the adapter when it is
//! not there, then writes the core's address, the client key, the adapter's registration and Pi's
//! default provider and model, and removes the adapter's model cache), `update` (write: replaces
//! the installed adapter the preview pinned with the one this program carries), `uninstall`
//! (write: takes the adapter's registration out and removes the folder the preview pinned),
//! `clear` and `restore` (write), `status` (read), `recipe` (read: how the launcher starts Pi)
//! and the backup browser's standard operations. File writes go through
//! pluxy-transactions: link-preserving, journalled, with the originals and manual backups in the
//! host's vault; the adapter's folder goes through its folder writes: through a handle on the
//! agent folder, the copy moved aside checked against its pin, journalled and reconciled.

mod adapter;
mod pi;

use pi::{PiMerge, Settings};
use pluxy_program::{run, Call, Error, Handler};
use pluxy_transactions::{
    backup_call, data_dir, json_leaves, sha256_of, Folder, Id, Pin, Selection, Target,
    Transactions, Vault,
};
use serde_json::{json, Map, Value};
use std::path::{Path, PathBuf};

/// The one set this tool applies: the adapter's config, Pi's settings and the adapter's model
/// cache, in that order, all in one agent folder.
const SET: &str = "pi";
const FILES: [&str; 3] = [
    "cliproxyapi.json",
    "settings.json",
    "cliproxyapi-models.json",
];

struct Pi;

/// The app's home folder.
fn home() -> Result<PathBuf, Error> {
    std::env::var_os("HOME")
        .filter(|h| !h.is_empty())
        .map(PathBuf::from)
        .ok_or_else(|| Error::unavailable("the app gave no home folder (HOME)"))
}

/// The selections a preview names: its three targets (`file`, which may be a link, as stow makes:
/// it is followed and kept), the three files in a selected agent folder (`folder`, which may
/// itself be a link, as chezmoi can make; files in it may not), or in `~/.pi/agent` (the tool's
/// environment has no PI_CODING_AGENT_DIR: a person who moved the folder selects it). Pi makes
/// its agent folder when it first runs; before that there is nothing to set up, and the tool
/// makes no folder.
fn selections(input: &Value) -> Result<[Selection; 3], Error> {
    if let Some(targets) = input.get("targets") {
        return match targets.as_array().map(Vec::as_slice) {
            Some([a, b, c]) => Ok([
                Selection::from_json(a)?,
                Selection::from_json(b)?,
                Selection::from_json(c)?,
            ]),
            _ => Err(Error::invalid_request(
                "targets holds cliproxyapi.json, settings.json and cliproxyapi-models.json",
            )),
        };
    }
    if let Some(folder) = input.get("folder") {
        let root = PathBuf::from(folder.as_str().unwrap_or_default());
        if !root.is_absolute() {
            return Err(Error::invalid_request("folder is a full path"));
        }
        return Ok(FILES.map(|name| Selection::Root {
            root: root.clone(),
            path: name.to_string(),
        }));
    }
    let agent = home()?.join(".pi").join("agent");
    if !agent.is_dir() {
        return Err(Error::invalid_request(format!(
            "{} does not exist yet: run Pi once (it makes the folder), then read the settings again",
            agent.display()
        )));
    }
    Ok(FILES.map(|name| Selection::File(agent.join(name))))
}

/// A preview and the bytes of cliproxyapi.json and settings.json.
type Read = (Value, [Option<Vec<u8>>; 2]);

/// Runs read (a preview and the two files' bytes) again once when the bytes it read are not the
/// ones it pinned (another program wrote a file between the two reads); refuses if they differ
/// again, so the values shown are always those of the files pinned.
fn pinned_read(mut read: impl FnMut() -> Result<Read, Error>) -> Result<Read, Error> {
    for _ in 0..2 {
        let (v, bytes) = read()?;
        let same =
            (0..2).all(|i| v["targets"][i]["sha256"] == json!(sha256_of(bytes[i].as_deref())));
        if same {
            return Ok((v, bytes));
        }
    }
    Err(Error::unavailable(
        "a configuration file changed while it was read: read it again",
    ))
}

/// A selection's agent folder (as the person gave it, not a link's target) and file name.
fn place(s: &Selection) -> (PathBuf, String) {
    match s {
        Selection::File(f) => (
            f.parent().map(Path::to_path_buf).unwrap_or_default(),
            f.file_name()
                .map(|n| n.to_string_lossy().into_owned())
                .unwrap_or_default(),
        ),
        Selection::Root { root, path } => (root.clone(), path.clone()),
    }
}

/// The agent folder three selections share, refusing any that are not Pi's three files in one
/// folder in their order.
fn agent_of(sels: &[&Selection; 3]) -> Result<PathBuf, Error> {
    let places = sels.map(place);
    let agent = places[0].0.clone();
    // The folder must be one with a parent: never the file system's root (an empty folder field).
    let ok = agent.is_absolute()
        && agent.parent().is_some()
        && places
            .iter()
            .zip(FILES)
            .all(|((a, n), want)| *a == agent && n == want);
    if !ok {
        return Err(Error::invalid_request(
            "targets are cliproxyapi.json, settings.json and cliproxyapi-models.json in one agent folder",
        ));
    }
    Ok(agent)
}

/// Refuses, before any write, pinned files Pi or the plugin would not take (as preview reports
/// them): Clear and Uninstall leave such files to the person, and Restore Backup still works.
fn checked(pins: &[Pin; 3]) -> Result<(), Error> {
    let (_, config) = Target::open(&pins[0])?.read_now()?;
    let (_, settings) = Target::open(&pins[1])?.read_now()?;
    pi::check_files(config.as_deref(), settings.as_deref())
}

/// The input's three pins, and their agent folder.
fn pins(input: &Value) -> Result<([Pin; 3], PathBuf), Error> {
    let p = match input["targets"].as_array().map(Vec::as_slice) {
        Some([a, b, c]) => [Pin::from_json(a)?, Pin::from_json(b)?, Pin::from_json(c)?],
        _ => {
            return Err(Error::invalid_request(
                "targets holds the three pinned files of the agent folder",
            ))
        }
    };
    let agent = agent_of(&[&p[0].selection, &p[1].selection, &p[2].selection])?;
    Ok((p, agent))
}

/// The plugin pin an update or uninstall carries back from the preview: pluxy's copy in the agent
/// folder the targets are in, or None when the preview found no copy of pluxy's there.
fn plugin_pin(input: &Value, agent: &Path) -> Result<Option<Pin>, Error> {
    let v = &input["plugin"];
    if v.is_null() {
        return Ok(None);
    }
    let pin = Pin::from_json(v)?;
    if pin.selection != adapter::selection(agent) {
        return Err(Error::invalid_request(
            "plugin is the preview's pin of pluxy's Pi plugin in the targets' agent folder",
        ));
    }
    Ok(Some(pin))
}

/// The plugin folder an update or uninstall acts on, before any change: pluxy's copy the preview
/// pinned, held open and checked (refused when it is not that copy now: the agent folder's link
/// pointed elsewhere, the agent or plugin folder moved or replaced, another copy put there, the
/// marker changed); or, when the preview found no copy of pluxy's, none, refused if one is there
/// now.
fn plugin_folder(agent: &Path, plugin: Option<&Pin>) -> Result<Option<Folder>, Error> {
    match plugin {
        Some(p) => adapter::pinned(agent, p).map(Some),
        None if matches!(adapter::state(agent), adapter::State::Installed(_)) => Err(
            adapter::changed(agent, "pluxy's copy is there now; the preview found none"),
        ),
        None => Ok(None),
    }
}

/// What an apply's input asks for, checked, with the key.
fn settings(input: &Value, key: &str) -> Result<Settings, Error> {
    let base = input["base_url"].as_str().unwrap_or_default();
    let url_ok = (base.starts_with("http://") || base.starts_with("https://"))
        && base.len() <= 2048
        && !base.chars().any(|c| c.is_whitespace() || c.is_control());
    if !url_ok {
        return Err(Error::invalid_request(
            "base_url is the core's http:// or https:// address",
        ));
    }
    if key.is_empty()
        || key.len() > 4096
        || key.chars().any(|c| c.is_whitespace() || c.is_control())
    {
        return Err(Error::invalid_request(
            "the key is empty or not a usable key",
        ));
    }
    let model = input["model"].as_str().unwrap_or_default();
    if model.is_empty()
        || model.chars().count() > 240
        || model.chars().any(|c| c.is_whitespace() || c.is_control())
    {
        return Err(Error::invalid_request(
            "model is a model ID of at most 240 characters, without spaces",
        ));
    }
    Ok(Settings {
        base_url: pi::base_url_of(base),
        key: key.to_string(),
        model: model.to_string(),
    })
}

/// The agent folder a write's file transaction resolved and pinned, as its pins hold it: the
/// resolved folder of a file selected in a selected agent folder, or of a selected file that is
/// not a link, with that folder's identity. None when every file is a link to another folder (as
/// stow makes) or the pins name different folders: the transaction pinned no one agent folder.
fn pinned_agent(pins: &[Pin; 3]) -> Option<(PathBuf, Id)> {
    let mut named = pins
        .iter()
        .filter(|p| matches!(p.selection, Selection::Root { .. }) || p.link.is_none())
        .map(|p| (p.target.parent().map(Path::to_path_buf), p.parent_id));
    let (folder, id) = named.next()?;
    let folder = folder?;
    named
        .all(|(f, i)| f.as_deref() == Some(folder.as_path()) && i == id)
        .then_some((folder, id))
}

/// Removes pluxy's adapter once settings.json no longer lists it (after a clear, a restore or an
/// uninstall): a folder nothing loads. settings.json is read as the transaction left it, the file
/// it pinned (never through the selection's link again). With plugin (uninstall), only the copy the
/// preview pinned; else pluxy's copy as it is now in the agent folder the transaction pinned (none
/// when it pinned no agent folder). Whether it removed it.
fn tidy(tx: &Transactions, pins: &[Pin; 3], plugin: Option<&Folder>) -> Result<bool, Error> {
    let (_, bytes) = Target::pinned(&pins[1])?.read_now()?;
    // A settings file Pi would not take is left to the person, with the adapter.
    if pi::registered(bytes.as_deref()).unwrap_or(true) {
        return Ok(false);
    }
    match plugin {
        Some(f) => adapter::remove(tx, f).map(|_| true),
        None => match pinned_agent(pins) {
            Some((root, id)) => adapter::remove_in(tx, &root, id),
            None => Ok(false),
        },
    }
}

// Tests only: called once a write's file transaction has ended, before the plugin's folder is
// tidied or a failed Apply's copy removed (where another program could act).
#[cfg(test)]
thread_local! {
    static AFTER_FILES: std::cell::RefCell<Option<Box<dyn FnMut()>>> =
        const { std::cell::RefCell::new(None) };
}

#[cfg(test)]
fn after_files() {
    AFTER_FILES.with(|h| {
        if let Some(f) = h.borrow_mut().as_mut() {
            f()
        }
    });
}

#[cfg(not(test))]
fn after_files() {}

/// Where Pi's installer puts `pi` on os, in the order the launcher looks: on Linux and macOS
/// (`pi.dev/install.sh`) its fallback in Pi's own folder first, then `~/.local/bin` (where it
/// usually goes). On Windows `pi.dev/install.ps1` makes `pi.cmd` launchers, which are not npm's
/// shims, so none is named: `pi` is found on the PATH.
fn installed(os: &str, home: &Path) -> Vec<PathBuf> {
    match os {
        "windows" => Vec::new(),
        _ => vec![home.join(".pi/agent/bin/pi"), home.join(".local/bin/pi")],
    }
}

/// How the launcher starts Pi: `pi` in a terminal, with no arguments or variables (the old app's
/// launch): the first installed copy this system runs (often not on a desktop session's PATH),
/// else `pi` from the PATH.
fn recipe(os: &str, home: &Path) -> Value {
    let program = installed(os, home)
        .into_iter()
        .find(|p| pluxy_files::executable(p))
        .map(|p| p.to_string_lossy().into_owned())
        .unwrap_or_else(|| "pi".to_string());
    json!({"client": "Pi", "kind": "terminal", "program": program, "args": []})
}

/// Reads the three files and the plugin's state, pinning the files and pluxy's copy of the plugin
/// (`preview`).
fn preview(vault: &dyn Vault, data: PathBuf, input: &Value) -> Result<Value, Error> {
    let sels = selections(input)?;
    let agent = agent_of(&[&sels[0], &sels[1], &sels[2]])?;
    let tx = Transactions::new(vault, data);
    let (mut v, [config, settings]) = pinned_read(|| {
        let v = tx.preview(SET, &sels)?;
        let (_, a) = Target::resolve(&sels[0])?.read_now()?;
        let (_, b) = Target::resolve(&sels[1])?.read_now()?;
        Ok((v, [a, b]))
    })?;
    // Files Pi would not take are pinned all the same, so Restore Backup and the
    // backups still reach them: `state` is `refused` and `reason` says why apply and
    // clear refuse them.
    let state = adapter::state(&agent);
    let mut read = pi::read_back(
        config.as_deref(),
        settings.as_deref(),
        input["base_url"].as_str(),
        matches!(state, adapter::State::Installed(_)),
    )
    .unwrap_or_else(|e| json!({"state": "refused", "reason": e.message}));
    read["adapter"] = state.to_json();
    // pluxy's copy is pinned too, for Update plugin and Uninstall plugin to carry back.
    read["adapter"]["pin"] = match state {
        adapter::State::Installed(_) => Target::resolve(&adapter::selection(&agent))?.pin.to_json(),
        _ => Value::Null,
    };
    v["pi"] = read;
    Ok(v)
}

/// Update Pi Plugin (`update`): only the copy the preview pinned, in the agent folder its files
/// are in, written through that agent folder held open (pluxy-transactions' folder writes); the
/// files themselves are not written, so only where they are is checked.
fn update(vault: &dyn Vault, data: PathBuf, input: &Value) -> Result<Value, Error> {
    let (pins, agent) = pins(input)?;
    let plugin = plugin_pin(input, &agent)?.ok_or_else(|| {
        Error::invalid_request(
            "plugin is the preview's pin of pluxy's Pi plugin, which update replaces",
        )
    })?;
    for p in &pins {
        Target::place(p)?;
    }
    let folder = adapter::pinned(&agent, &plugin)?;
    adapter::update(&Transactions::new(vault, data), &folder)?;
    Ok(json!({"outcome": "updated", "version": adapter::VERSION}))
}

/// Uninstall Pi Plugin (`uninstall`): the registration goes through apply, so the set's record
/// (and Clear and Restore after it) stays whole; the connection settings stay (old app). Only the
/// plugin folder the preview pinned is removed, through the agent folder it pinned, held open from
/// the check before the settings write; none when it found no copy of pluxy's.
fn uninstall(vault: &dyn Vault, data: PathBuf, input: &Value) -> Result<Value, Error> {
    let (pins, agent) = pins(input)?;
    let plugin = plugin_pin(input, &agent)?;
    checked(&pins)?;
    let folder = plugin_folder(&agent, plugin.as_ref())?;
    let tx = Transactions::new(vault, data);
    let mut v = tx.apply(
        SET,
        &pins,
        &|i, _, before| match i {
            1 => pi::uninstall_settings(before),
            _ => Ok(before.map(<[u8]>::to_vec)),
        },
        &PiMerge,
        &mut |_| {},
    )?;
    after_files();
    v["adapter_removed"] = json!(match &folder {
        Some(f) => tidy(&tx, &pins, Some(f))?,
        None => false,
    });
    Ok(v)
}

/// Connect (`apply`): installs the adapter first when pluxy's copy is not there, so the
/// registration never names a missing folder, then writes the three files; when that write fails,
/// the copy this apply installed goes again, by its identity, through the agent folder it was put
/// in.
fn apply(
    vault: &dyn Vault,
    data: PathBuf,
    input: &Value,
    secrets: &Map<String, Value>,
) -> Result<Value, Error> {
    let (pins, agent) = pins(input)?;
    let key = input["key"]
        .as_str()
        .and_then(|r| secrets.get(r))
        .and_then(Value::as_str)
        .ok_or_else(|| Error::invalid_request("key names no secret the app delivered"))?;
    let s = settings(input, key)?;
    let (_, config) = Target::open(&pins[0])?.read_now()?;
    let provider = pi::provider_of(config.as_deref())?;
    let tx = Transactions::new(vault, data);
    let installed = adapter::ensure(&tx, &agent)?;
    let r = tx.apply(
        SET,
        &pins,
        &|i, _, before| match i {
            0 => pi::apply_config(before, &s).map(Some),
            1 => pi::apply_settings(before, &s, &provider).map(Some),
            _ => Ok(None),
        },
        &PiMerge,
        &mut |_| {},
    );
    after_files();
    match r {
        Ok(mut v) => {
            v["adapter"] = json!(if installed.is_some() {
                "installed"
            } else {
                "kept"
            });
            Ok(v)
        }
        Err(e) => {
            if let Some(f) = &installed {
                let _ = adapter::remove(&tx, f);
            }
            Err(e)
        }
    }
}

/// Disconnect (`clear`): takes the set's values out of the three files, then removes the adapter
/// from the agent folder they were written in once settings.json no longer lists it.
fn clear(vault: &dyn Vault, data: PathBuf, input: &Value) -> Result<Value, Error> {
    let (pins, _) = pins(input)?;
    checked(&pins)?;
    let tx = Transactions::new(vault, data);
    let mut v = tx.clear(SET, &pins, &PiMerge, &mut |_| {})?;
    after_files();
    v["adapter_removed"] = json!(tidy(&tx, &pins, None)?);
    Ok(v)
}

/// Restore Backup (`restore`) puts the originals back whatever the files hold now (the person's
/// way back always works), then removes the adapter as Disconnect does.
fn restore(vault: &dyn Vault, data: PathBuf, input: &Value) -> Result<Value, Error> {
    let (pins, _) = pins(input)?;
    let tx = Transactions::new(vault, data);
    let mut v = tx.restore(SET, &pins, &mut |_| {})?;
    after_files();
    v["adapter_removed"] = json!(tidy(&tx, &pins, None)?);
    Ok(v)
}

impl Handler for Pi {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        let input = &call.input;
        // The backup browser: manual backups of the three files, diffed by setting name.
        if call.op.starts_with("backup-") {
            let tx = Transactions::new(call, data_dir()?);
            if let Some(r) = backup_call(call, &tx, SET, &json_leaves) {
                return r;
            }
        }
        match call.op.as_str() {
            "preview" => preview(call, data_dir()?, input),
            "apply" => apply(call, data_dir()?, input, &call.secrets),
            "update" => update(call, data_dir()?, input),
            "uninstall" => uninstall(call, data_dir()?, input),
            "clear" => clear(call, data_dir()?, input),
            "restore" => restore(call, data_dir()?, input),
            "status" => Transactions::new(call, data_dir()?).status(),
            "recipe" => Ok(recipe(std::env::consts::OS, &home()?)),
            op => Err(Error::unsupported(format!("no operation {op}"))),
        }
    }

    fn reconcile(&self, call: &Call, _unknown: &[Value], journals: &[Value]) -> Result<(), Error> {
        Transactions::new(call, data_dir()?)
            .reconcile(journals)
            .map(|_| ())
    }
}

#[cfg(test)]
#[path = "pi_tests.rs"]
mod pi_tests;

#[cfg(test)]
#[path = "manifest_tests.rs"]
mod manifest_tests;

fn main() {
    if let Err(e) = run(Pi) {
        eprintln!("pi-setup: {e}");
        std::process::exit(1);
    }
}
