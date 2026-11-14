//! Client keys for native tools (`../app-host/README.md`, "Client keys"). A client key is the key
//! a client program sends core with its requests; it is a secret apart from the management key,
//! kept in the vault (`vault.rs`) and bound to the core it belongs to (the connection's mode and
//! origin), to core's ID of it then and to the one native tool it is for (`vault::ClientKey`).
//!
//! It is obtained natively only, never through the window: `capture` asks a native confirmation,
//! then the app itself asks core for a new key (`POST /client-keys`) with the management key and
//! keeps core's reply in the vault; `enter` reads an existing key from native key entry and keeps
//! it once core takes it. The window gets the secret's ID, label and binding, never the key.
//!
//! At every use the router (`route.rs`) checks the binding with `admit`: the window gives the key
//! only to its tool, and its value is delivered only to that tool; the app must be connected to
//! the core it belongs to, which is checked before anything is sent anywhere; and before its
//! value is delivered, that core must still take it (`Caller::client_key_status`: a key deleted
//! on core is refused there). The value then travels only in the tool's call frame, as any vault
//! secret's does.
//!
//! Linux only, as the vault and native key entry.

use crate::program::CallError;
use crate::vault::{ClientKey, CoreTarget, Vault};
use crate::App;
use serde_json::{json, Value};
use std::sync::atomic::Ordering;
use tauri::{AppHandle, Manager};

/// The native key entry's title.
const ENTRY: &str = "pluxy client key";

/// A key as core lists it: all but its first and last two characters hidden; a key shorter than
/// 8 characters hidden whole (`maskKey` in next/internal/core/manageconfig.go).
fn masked(key: &str) -> String {
    let c: Vec<char> = key.chars().collect();
    if c.len() < 8 {
        return "*".repeat(10);
    }
    format!(
        "{}{}{}",
        c[..2].iter().collect::<String>(),
        "*".repeat(6),
        c[c.len() - 2..].iter().collect::<String>()
    )
}

/// Whether core can have sent key: what a bearer token can carry, of a sane length.
fn usable(key: &str) -> bool {
    !key.is_empty() && key.len() <= 1024 && key.bytes().all(|b| b.is_ascii_graphic())
}

/// Checks that label can name a secret and client is a native tool on this device.
fn check(app: &AppHandle, label: &str, client: &str) -> Result<(), String> {
    Vault::check_label(label)?;
    let native = app
        .state::<App>()
        .device
        .lock()
        .unwrap()
        .get(client)
        .is_some_and(|p| p.active.tier == "native");
    if !native {
        return Err(format!("{client} is not a native tool on this device"));
    }
    Ok(())
}

/// What the window gets for a kept client key.
fn view(id: String, label: &str, binding: &ClientKey) -> Value {
    json!({"id": id, "label": label, "client_key": binding})
}

/// The core the app is connected to and what calls it, signed in.
fn connected(app: &AppHandle) -> Result<(CoreTarget, crate::conn::Caller, String), String> {
    let st = app.state::<App>();
    let c = st.conn.lock().unwrap();
    let target = c.target().ok_or("the app is not connected to a core")?;
    if !c.signed_in() {
        return Err("not signed in".into());
    }
    let shown = target.origin.clone();
    Ok((target, c.caller(), shown))
}

/// Asks core for a new client key for client after the person confirms, and keeps it in the
/// vault as label; gives the window's view of it. Run off the window's thread.
pub fn capture(app: &AppHandle, label: &str, client: &str) -> Result<Value, String> {
    check(app, label, client)?;
    let (target, caller, origin) = connected(app)?;
    let generation = app.state::<App>().conn.lock().unwrap().revision();
    let what = format!(
        "Create a new client key on the core at {origin} for {client}, and keep it in this device's vault as \"{label}\". Core adds it to its client keys at once; the key is not shown. {client} gets it only after you confirm each use."
    );
    if !crate::confirmation::confirm_app(app, what, generation)? {
        return Err("cancelled".into());
    }
    {
        let st = app.state::<App>();
        let c = st.conn.lock().unwrap();
        if c.revision() != generation || c.target().as_ref() != Some(&target) {
            return Err("The connection changed. Capture the key again.".into());
        }
    }
    let r = caller.call("POST", "/client-keys", None);
    if r.status != 201 {
        let why = r
            .error
            .map(|e| e.message)
            .unwrap_or_else(|| format!("core answered {}", r.status));
        return Err(format!("core did not make a client key: {why}"));
    }
    let body = r.body.unwrap_or_default();
    let (Some(value), Some(id)) = (
        body["value"].as_str().filter(|k| usable(k)),
        body["key"]["id"].as_str(),
    ) else {
        return Err("core's reply holds no usable client key".into());
    };
    let binding = ClientKey {
        core: target,
        key_id: Some(id.to_string()),
        masked: body["key"]["masked"]
            .as_str()
            .map_or_else(|| masked(value), String::from),
        client: client.into(),
    };
    match app
        .state::<App>()
        .vault
        .add_client_key(label, value, binding.clone())
    {
        Ok(secret) => Ok(view(secret, label, &binding)),
        Err(e) => {
            // Not kept: it must not stay a working key of core's that nothing knows.
            let path = format!("/client-keys/{id}");
            let undone = caller.call("DELETE", &path, None).status == 204;
            Err(if undone {
                format!("the client key could not be kept ({e}), so it was deleted on core again")
            } else {
                format!("the client key could not be kept ({e}), and deleting it on core failed: delete the key {} on the core's Client keys page", binding.masked)
            })
        }
    }
}

/// Reads an existing client key of the connected core for client from native key entry and keeps
/// it in the vault as label once core takes it; gives the window's view of it. Run off the
/// window's thread.
pub fn enter(app: &AppHandle, label: &str, client: &str) -> Result<Value, String> {
    check(app, label, client)?;
    let (target, caller, origin) = connected(app)?;
    let st = app.state::<App>();
    if st.key_entry_open.swap(true, Ordering::SeqCst) {
        return Err("native key entry is already open".into());
    }
    let input = crate::key_entry::request_as(
        app,
        ENTRY,
        format!(
            "An existing client key of the core at {origin} for {client}, to keep in this device's vault as \"{label}\". {client} gets it only after you confirm each use."
        ),
        false,
    );
    st.key_entry_open.store(false, Ordering::SeqCst);
    let input = match input {
        Ok(Some(input)) => input,
        Ok(None) => return Err("cancelled".into()),
        Err(e) => return Err(e.into()),
    };
    let key = input.key.as_str();
    if st.conn.lock().unwrap().target().as_ref() != Some(&target) {
        return Err("The connection changed. Enter the key again.".into());
    }
    match caller.client_key_status(key)? {
        200 => {}
        401 | 403 => {
            return Err(format!(
                "the core at {origin} does not accept this key as a client key"
            ))
        }
        s => return Err(format!("the core at {origin} answered {s}")),
    }
    // Core's ID of it: the one key core lists with its masked form, if only one has it.
    let mask = masked(key);
    let listed = caller.call("GET", "/client-keys", None);
    let ids: Vec<&str> = listed
        .body
        .as_ref()
        .and_then(|b| b["keys"].as_array())
        .into_iter()
        .flatten()
        .filter(|k| k["masked"] == mask.as_str())
        .filter_map(|k| k["id"].as_str())
        .collect();
    let binding = ClientKey {
        core: target,
        key_id: match ids.as_slice() {
            [id] => Some(id.to_string()),
            _ => None,
        },
        masked: mask,
        client: client.into(),
    };
    let secret = st.vault.add_client_key(label, key, binding.clone())?;
    Ok(view(secret, label, &binding))
}

fn refuse(message: String) -> CallError {
    CallError {
        code: "permission".into(),
        message,
        unknown: false,
    }
}

/// Checks the use of vault secret secret (labelled label) in a call of recipient: nothing for a
/// secret that is no client key. A client key goes from the window (window) only to its tool, and
/// its value (deliver) only to its tool; the app must be connected to its core; and before its
/// value is delivered, that core must still take it.
pub fn admit(
    app: &AppHandle,
    secret: &str,
    label: &str,
    recipient: &str,
    window: bool,
    deliver: bool,
) -> Result<(), CallError> {
    let st = app.state::<App>();
    let Some(binding) = st.vault.secret(secret).and_then(|s| s.client_key) else {
        return Ok(());
    };
    if (window || deliver) && recipient != binding.client {
        return Err(refuse(format!(
            "\"{label}\" is a client key for {}; {recipient} cannot get it",
            binding.client
        )));
    }
    let origin = &binding.core.origin;
    // Checked before anything is sent: the key never goes to another core.
    let (now, caller) = {
        let c = st.conn.lock().unwrap();
        (c.target(), c.caller())
    };
    if now.as_ref() != Some(&binding.core) {
        return Err(refuse(format!(
            "\"{label}\" is a client key of the core at {origin}, and the app is connected to another core now"
        )));
    }
    if !deliver {
        return Ok(());
    }
    let value = st
        .vault
        .value(secret)
        .ok_or_else(|| refuse(format!("the secret \"{label}\" was removed from the vault")))?;
    match caller.client_key_status(&value) {
        Ok(200) => Ok(()),
        Ok(401) => Err(refuse(format!(
            "the core at {origin} no longer accepts the client key \"{label}\": it was deleted there"
        ))),
        Ok(s) => Err(refuse(format!(
            "the core at {origin} refuses the client key \"{label}\" ({s})"
        ))),
        Err(e) => Err(CallError {
            code: "unavailable".into(),
            message: format!(
                "whether the core at {origin} still accepts the client key \"{label}\" is not known: {e}"
            ),
            unknown: false,
        }),
    }
}

#[cfg(test)]
mod tests {
    use super::masked;

    #[test]
    fn masks_as_core_lists_keys() {
        assert_eq!(masked("sk-ABCDEFGHIJKLMNOPQRSTUVWXYZ"), "sk******YZ");
        assert_eq!(masked("abcdefgh"), "ab******gh");
        assert_eq!(masked("short"), "**********");
        assert_eq!(masked("ébcdefgĥ"), "éb******gĥ");
    }
}
