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
//! on core is refused there), and once core has answered, the connection must be unchanged and
//! the secret still in the vault. The value then travels only in the tool's call frame, as any
//! vault secret's does. A launch's key given to the process it starts is a delivery to the tool
//! whose launch it is: checked so at the call, and again by `launch_value` whenever the host
//! resolves it (the spawn, a managed runtime's start and each restart); once all of a launch's
//! secrets are resolved, they are checked once more right before the spawn (`Launches`).
//!
//! It runs wherever the native stack does (Linux, Windows and macOS): capture and entry, and the
//! checks the router and launches make.

use crate::conn::{Caller, Connection};
use crate::program::CallError;
use crate::spawn::{Given, Values};
use crate::vault::{ClientKey, CoreTarget, Vault};
use crate::App;
use serde_json::{json, Value};
use std::sync::atomic::Ordering;
use std::sync::Mutex;
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

/// The connection as a capture or an entry finds it before asking the person.
#[derive(Clone, Debug, PartialEq)]
pub(crate) struct Pinned {
    target: Option<CoreTarget>,
    pub(crate) revision: u64,
    signed_in: bool,
}

impl Pinned {
    fn of(c: &crate::conn::Connection) -> Pinned {
        Pinned {
            target: c.target(),
            revision: c.revision(),
            signed_in: c.signed_in(),
        }
    }
}

/// Whether the connection is now as it was before: connected to the same core, on the same
/// revision (no sign-out, key change or switch since), and signed in.
fn unchanged(before: &Pinned, now: &Pinned) -> bool {
    now.target.is_some()
        && now.target == before.target
        && now.revision == before.revision
        && now.signed_in
}

/// The connection, read once under its lock: connected to a core and signed in.
pub(crate) fn connected(app: &AppHandle) -> Result<(Pinned, CoreTarget), String> {
    let st = app.state::<App>();
    let c = st.conn.lock().unwrap();
    let pin = Pinned::of(&c);
    let target = pin
        .target
        .clone()
        .ok_or("the app is not connected to a core")?;
    if !pin.signed_in {
        return Err("not signed in".into());
    }
    Ok((pin, target))
}

/// What calls core now, once the person has answered: only when the connection is unchanged
/// since pin; again says what to do otherwise.
pub(crate) fn fresh(
    app: &AppHandle,
    pin: &Pinned,
    again: &str,
) -> Result<crate::conn::Caller, String> {
    let st = app.state::<App>();
    let c = st.conn.lock().unwrap();
    if !unchanged(pin, &Pinned::of(&c)) {
        return Err(format!("The connection changed. {again}"));
    }
    Ok(c.caller())
}

/// Whether id, core's ID of a key, is one the app sends back in a path: 1 to 64 letters, digits,
/// `-` or `_` (core's are 24 hexadecimal digits).
fn usable_id(id: &str) -> bool {
    !id.is_empty()
        && id.len() <= 64
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
}

/// The new key in core's reply to `POST /client-keys`, and its binding to target and client. Its
/// masked form is the app's own, never core's text; a reply whose key or ID is not usable is
/// refused, naming what it can of a key it may have left on core.
fn from_reply(
    body: &Value,
    target: CoreTarget,
    client: &str,
) -> Result<(String, ClientKey), String> {
    let Some(value) = body["value"].as_str().filter(|k| usable(k)) else {
        return Err("core's reply holds no usable client key".into());
    };
    let Some(id) = body["key"]["id"].as_str().filter(|i| usable_id(i)) else {
        return Err(format!(
            "core's reply names no usable ID for its new client key, so the app did not keep it; delete the key {} on the core's Client keys page",
            masked(value)
        ));
    };
    Ok((
        value.to_string(),
        ClientKey {
            core: target,
            key_id: Some(id.to_string()),
            masked: masked(value),
            client: client.into(),
        },
    ))
}

/// The error of a capture whose key the vault could not keep (e): deleted on core again (undone),
/// or left there, named by its ID and masked form so the person can delete it.
fn not_kept(e: &str, binding: &ClientKey, undone: bool) -> String {
    if undone {
        return format!("the client key could not be kept ({e}), so it was deleted on core again");
    }
    format!(
        "the client key could not be kept ({e}), and deleting it on core failed: delete the key {} (ID {}) on the core's Client keys page",
        binding.masked,
        binding.key_id.as_deref().unwrap_or("unknown")
    )
}

/// Asks core for a new client key for client after the person confirms, and keeps it in the
/// vault as label; gives the window's view of it. Run off the window's thread.
pub fn capture(app: &AppHandle, label: &str, client: &str) -> Result<Value, String> {
    check(app, label, client)?;
    let st = app.state::<App>();
    // Before core makes a key: one the vault cannot keep would be left on core.
    st.vault.can_add()?;
    let (pin, target) = connected(app)?;
    let origin = target.origin.clone();
    let what = format!(
        "Create a new client key on the core at {origin} for {client}, and keep it in this device's vault as \"{label}\". Core adds it to its client keys at once; the key is not shown. {client} gets it only after you confirm each use."
    );
    if !crate::confirmation::confirm_app(app, what, pin.revision)? {
        return Err("cancelled".into());
    }
    let caller = fresh(app, &pin, "Capture the key again.")?;
    let r = caller.call("POST", "/client-keys", None);
    if r.status != 201 {
        let why = r
            .error
            .map(|e| e.message)
            .unwrap_or_else(|| format!("core answered {}", r.status));
        return Err(format!("core did not make a client key: {why}"));
    }
    let (value, binding) = from_reply(&r.body.unwrap_or_default(), target, client)?;
    match st.vault.add_client_key(label, &value, binding.clone()) {
        Ok(secret) => Ok(view(secret, label, &binding)),
        Err(e) => {
            // Not kept: it must not stay a working key of core's that nothing knows.
            let path = format!("/client-keys/{}", binding.key_id.as_deref().unwrap_or(""));
            let undone = caller.call("DELETE", &path, None).status == 204;
            Err(not_kept(&e, &binding, undone))
        }
    }
}

/// Reads an existing client key of the connected core for client from native key entry and keeps
/// it in the vault as label once core takes it; gives the window's view of it. Run off the
/// window's thread.
pub fn enter(app: &AppHandle, label: &str, client: &str) -> Result<Value, String> {
    check(app, label, client)?;
    let (pin, target) = connected(app)?;
    let origin = target.origin.clone();
    let st = app.state::<App>();
    if st.key_entry_open.swap(true, Ordering::SeqCst) {
        return Err("native key entry is already open".into());
    }
    struct Open<'a>(&'a std::sync::atomic::AtomicBool);
    impl Drop for Open<'_> {
        fn drop(&mut self) {
            self.0.store(false, Ordering::SeqCst);
        }
    }
    let open = Open(&st.key_entry_open);
    let input = crate::key_entry::request_as(
        app,
        ENTRY,
        format!(
            "An existing client key of the core at {origin} for {client}, to keep in this device's vault as \"{label}\". {client} gets it only after you confirm each use."
        ),
        false,
    );
    drop(open);
    let input = match input {
        Ok(Some(input)) => input,
        Ok(None) => return Err("cancelled".into()),
        Err(e) => return Err(e.into()),
    };
    let key = input.key.as_str();
    let caller = fresh(app, &pin, "Enter the key again.")?;
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
            [id] if usable_id(id) => Some(id.to_string()),
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

/// Secret's client-key binding: none for a plain secret; refused when the secret is gone.
fn binding_of(vault: &Vault, secret: &str, label: &str) -> Result<Option<ClientKey>, CallError> {
    vault
        .secret(secret)
        .map(|s| s.client_key)
        .ok_or_else(|| refuse(format!("the secret \"{label}\" was removed from the vault")))
}

/// Checks the use of vault secret secret (labelled label) in a call of recipient: nothing for a
/// secret that is no client key. A client key goes from the window (window) only to its tool, and
/// its value (deliver) only to its tool; the app must be connected to its core; and before its
/// value is delivered, that core must still take it, with the connection unchanged and the secret
/// still in the vault once core has answered.
pub fn admit(
    app: &AppHandle,
    secret: &str,
    label: &str,
    recipient: &str,
    window: bool,
    deliver: bool,
) -> Result<(), CallError> {
    let st = app.state::<App>();
    admitted(
        &st.conn,
        &st.vault,
        (secret, label, recipient),
        window,
        deliver,
        |caller, key| caller.client_key_status(key),
    )
    .map(|_| ())
}

/// admit's check of secret, label and recipient, with status asking core whether it takes a key
/// now (`Caller::client_key_status`). For a client key whose value may be delivered, the
/// connection revision its check held on.
fn admitted(
    conn: &Mutex<Connection>,
    vault: &Vault,
    (secret, label, recipient): (&str, &str, &str),
    window: bool,
    deliver: bool,
    status: impl FnOnce(&Caller, &str) -> Result<u16, String>,
) -> Result<Option<u64>, CallError> {
    let Some(binding) = binding_of(vault, secret, label)? else {
        return Ok(None);
    };
    if (window || deliver) && recipient != binding.client {
        return Err(refuse(format!(
            "\"{label}\" is a client key for {}; {recipient} cannot get it",
            binding.client
        )));
    }
    let origin = &binding.core.origin;
    // Checked before anything is sent: the key never goes to another core.
    let (now, revision, caller) = {
        let c = conn.lock().unwrap();
        (c.target(), c.revision(), c.caller())
    };
    if now.as_ref() != Some(&binding.core) {
        return Err(refuse(format!(
            "\"{label}\" is a client key of the core at {origin}, and the app is connected to another core now"
        )));
    }
    if !deliver {
        return Ok(None);
    }
    let value = vault
        .value(secret)
        .ok_or_else(|| refuse(format!("the secret \"{label}\" was removed from the vault")))?;
    let answer = status(&caller, &value);
    // Let go before the connection's lock is taken again: a restart holds that lock while it
    // binds the address this caller may hold (`Caller::call_or_retry`).
    drop(caller);
    match answer {
        Ok(200) => {}
        Ok(401) => {
            return Err(refuse(format!(
                "the core at {origin} no longer accepts the client key \"{label}\": it was deleted there"
            )))
        }
        Ok(s) => {
            return Err(refuse(format!(
                "the core at {origin} refuses the client key \"{label}\" ({s})"
            )))
        }
        Err(e) => {
            return Err(CallError {
                code: "unavailable".into(),
                message: format!(
                    "whether the core at {origin} still accepts the client key \"{label}\" is not known: {e}"
                ),
                unknown: false,
            })
        }
    }
    // Core was asked without the connection's lock, for as long as its answer took: the
    // connection and the vault are checked again, under that lock, before the key counts as
    // taken.
    let c = conn.lock().unwrap();
    if c.revision() != revision || c.target().as_ref() != Some(&binding.core) {
        return Err(refuse(format!(
            "the app's connection changed while the core at {origin} was asked about the client key \"{label}\""
        )));
    }
    if vault.secret(secret).is_none() {
        return Err(refuse(format!(
            "the secret \"{label}\" was removed from the vault"
        )));
    }
    Ok(Some(revision))
}

/// Vault secret secret's value for a launch of recipient's (the tool whose launch it is), at the
/// moment the host gives it to the launched process (its spawn, a runtime's start or restart),
/// and the connection revision its check held on (a client key's): checked first as a delivery is
/// (`admit`, deliver), so it is refused, with no earlier value to fall back on, once the secret is
/// gone, and a client key once it is another tool's, of a core the app is not connected to now,
/// or no longer taken by that core.
pub fn launch_value(
    app: &AppHandle,
    secret: &str,
    recipient: &str,
) -> Result<(String, Option<u64>), String> {
    let st = app.state::<App>();
    let gone = || "a secret of the launch was removed from the vault".to_string();
    let label = st.vault.secret(secret).ok_or_else(gone)?.label;
    let revision = admitted(
        &st.conn,
        &st.vault,
        (secret, &label, recipient),
        false,
        true,
        |caller, key| caller.client_key_status(key),
    )
    .map_err(|e| e.message)?;
    Ok((st.vault.value(secret).ok_or_else(gone)?, revision))
}

/// Whether a launch may still be given the secrets resolved for it (given), right before its
/// spawn: each still in the vault, and the connection still on the revision each client key's
/// check held on. Read under the connection's lock.
fn still_given(conn: &Mutex<Connection>, vault: &Vault, given: &[Given]) -> Result<(), String> {
    let c = conn.lock().unwrap();
    if given
        .iter()
        .any(|(_, r)| r.is_some_and(|r| r != c.revision()))
    {
        return Err(
            "the app's connection changed after the launch's client keys were checked".into(),
        );
    }
    if given.iter().any(|(id, _)| vault.secret(id).is_none()) {
        return Err("a secret of the launch was removed from the vault".into());
    }
    Ok(())
}

/// The host's `spawn::Values`: each secret of a launch checked as a delivery to the tool whose
/// launch it is when it resolves (`launch_value`), and all of them once more right before the
/// spawn (`still_given`).
pub struct Launches(pub AppHandle);

impl Values for Launches {
    fn value(&self, package: &str, id: &str) -> Result<(String, Option<u64>), String> {
        launch_value(&self.0, id, package)
    }

    fn still(&self, given: &[Given]) -> Result<(), String> {
        let st = self.0.state::<App>();
        still_given(&st.conn, &st.vault, given)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::conn::Mode;

    fn target(origin: &str) -> CoreTarget {
        CoreTarget {
            mode: Mode::Local,
            origin: origin.into(),
        }
    }

    #[test]
    fn a_changed_connection_is_refused_after_the_person_answers() {
        let before = Pinned {
            target: Some(target("http://127.0.0.1:8400")),
            revision: 3,
            signed_in: true,
        };
        assert!(unchanged(&before, &before.clone()));
        // Signed out meanwhile, on the same core and revision as the check sees it.
        let signed_out = Pinned {
            signed_in: false,
            ..before.clone()
        };
        assert!(!unchanged(&before, &signed_out));
        let other_revision = Pinned {
            revision: 4,
            ..before.clone()
        };
        assert!(!unchanged(&before, &other_revision));
        let other_core = Pinned {
            target: Some(target("http://127.0.0.1:8401")),
            ..before.clone()
        };
        assert!(!unchanged(&before, &other_core));
        let gone = Pinned {
            target: None,
            ..before.clone()
        };
        assert!(!unchanged(&gone, &gone.clone()));
    }

    #[test]
    fn a_reply_is_masked_by_the_app_and_its_id_must_be_usable() {
        let key = "sk-ABCDEFGHIJKLMNOPQRSTUVWXYZ";
        let reply =
            |id: &str| json!({"value": key, "key": {"id": id, "masked": format!("{key}\u{7}")}});
        let (value, b) = from_reply(
            &reply("0123456789abcdef01234567"),
            target("http://c"),
            "pluxy/a",
        )
        .unwrap();
        assert_eq!(value, key);
        assert_eq!(b.masked, "sk******YZ");
        assert_eq!(b.key_id.as_deref(), Some("0123456789abcdef01234567"));
        assert_eq!(b.client, "pluxy/a");
        for bad in ["", "../status", "a/b", "a?b", "a b", "%2e", &"a".repeat(65)] {
            let e = from_reply(&reply(bad), target("http://c"), "pluxy/a").unwrap_err();
            assert!(e.contains("sk******YZ") && !e.contains(key), "{bad:?}: {e}");
        }
        assert!(from_reply(
            &json!({"value": "a b", "key": {"id": "x"}}),
            target("c"),
            "p"
        )
        .is_err());
    }

    #[test]
    fn a_key_left_on_core_is_named_by_its_id() {
        let b = ClientKey {
            core: target("http://c"),
            key_id: Some("0123456789abcdef01234567".into()),
            masked: "sk******YZ".into(),
            client: "pluxy/a".into(),
        };
        let left = not_kept("full", &b, false);
        assert!(left.contains("0123456789abcdef01234567") && left.contains("sk******YZ"));
        assert!(not_kept("full", &b, true).contains("deleted on core again"));
    }

    #[test]
    fn a_removed_secret_is_refused_not_passed() {
        let d = std::env::temp_dir().join(format!("pluxy-ck-{}", crate::managed::random_key()));
        let v = Vault::open(d.join("vault"));
        let plain = v.add_secret("plain", "plain-secret-value").unwrap();
        assert_eq!(binding_of(&v, &plain, "plain").unwrap(), None);
        let b = ClientKey {
            core: target("http://c"),
            key_id: None,
            masked: "sk******YZ".into(),
            client: "pluxy/a".into(),
        };
        let bound = v
            .add_client_key("bound", "sk-ABCDEFGHIJKLMNOPQRSTUVWXYZ", b.clone())
            .unwrap();
        assert_eq!(binding_of(&v, &bound, "bound").unwrap(), Some(b));
        v.remove_secret(&bound).unwrap();
        let e = binding_of(&v, &bound, "bound").unwrap_err();
        assert_eq!(e.code, "permission");
        assert!(e.message.contains("removed"), "{}", e.message);
        let _ = std::fs::remove_dir_all(d);
    }

    const KEY: &str = "sk-ABCDEFGHIJKLMNOPQRSTUVWXYZ";

    /// A vault holding a client key for pluxy/a of a local core and a plain secret, and a
    /// connection to that core (nothing is sent to it): its folder, the vault, the connection, and
    /// the two secrets' IDs.
    fn fixture(name: &str) -> (std::path::PathBuf, Vault, Mutex<Connection>, String, String) {
        let d =
            std::env::temp_dir().join(format!("pluxy-ck-{name}-{}", crate::managed::random_key()));
        let dirs = crate::conn::Dirs {
            config: d.join("config"),
            core: d.join("core"),
            cores: d.join("cores"),
        };
        crate::conn::Settings {
            mode: Some(Mode::Local),
            url: Some("http://127.0.0.1:9".into()),
            key: Some(crate::managed::random_key()),
            ..Default::default()
        }
        .save(&dirs.settings())
        .unwrap();
        let conn = Connection::open(dirs, None);
        let core = conn.target().expect("connected");
        let vault = Vault::open(d.join("vault"));
        let bound = vault
            .add_client_key(
                "bound",
                KEY,
                ClientKey {
                    core,
                    key_id: None,
                    masked: masked(KEY),
                    client: "pluxy/a".into(),
                },
            )
            .unwrap();
        let plain = vault.add_secret("plain", "plain-secret-value").unwrap();
        (d, vault, Mutex::new(conn), bound, plain)
    }

    /// The connection switched to another core and back to this one: the same core, a newer
    /// revision.
    fn away_and_back(conn: &Mutex<Connection>) {
        let mut c = conn.lock().unwrap();
        let here = c.target().unwrap().origin;
        for url in ["http://127.0.0.1:10".to_string(), here] {
            c.change(crate::conn::Change {
                mode: Some(Mode::Local),
                url: Some(url),
                ..Default::default()
            })
            .unwrap();
        }
    }

    #[test]
    fn a_delivery_is_refused_when_the_connection_or_the_vault_changed_while_core_was_asked() {
        let (d, vault, conn, bound, plain) = fixture("asked");
        let deliver = |status: &dyn Fn() -> u16| {
            admitted(
                &conn,
                &vault,
                (&bound, "bound", "pluxy/a"),
                false,
                true,
                |_, key| {
                    assert_eq!(key, KEY);
                    Ok(status())
                },
            )
        };
        // Nothing changed while core answered: taken, on the revision its check held on.
        let revision = conn.lock().unwrap().revision();
        assert_eq!(deliver(&|| 200).unwrap(), Some(revision));
        // Signed out meanwhile, on the same core.
        let e = deliver(&|| {
            conn.lock().unwrap().sign_out().unwrap();
            200
        })
        .unwrap_err();
        assert!(
            e.code == "permission" && e.message.contains("connection changed"),
            "{}",
            e.message
        );
        // Switched to another core and back meanwhile: the same core, but not the connection the
        // check began on.
        let e = deliver(&|| {
            away_and_back(&conn);
            200
        })
        .unwrap_err();
        assert!(
            e.code == "permission" && e.message.contains("connection changed"),
            "{}",
            e.message
        );
        // A plain secret, or a client key given without its value, asks core nothing and holds
        // on no revision.
        let unasked = |_: &Caller, _: &str| -> Result<u16, String> { panic!("core was asked") };
        assert_eq!(
            admitted(
                &conn,
                &vault,
                (&plain, "plain", "pluxy/b"),
                false,
                true,
                unasked
            )
            .unwrap(),
            None
        );
        assert_eq!(
            admitted(
                &conn,
                &vault,
                (&bound, "bound", "pluxy/a"),
                true,
                false,
                unasked
            )
            .unwrap(),
            None
        );
        // Removed from the vault meanwhile.
        let e = deliver(&|| {
            vault.remove_secret(&bound).unwrap();
            200
        })
        .unwrap_err();
        assert!(
            e.code == "permission" && e.message.contains("removed"),
            "{}",
            e.message
        );
        let _ = std::fs::remove_dir_all(d);
    }

    #[test]
    fn a_launch_is_refused_right_before_its_spawn_once_the_connection_or_the_vault_changed() {
        let (d, vault, conn, bound, plain) = fixture("spawn");
        let at = conn.lock().unwrap().revision();
        let given = vec![(bound.clone(), Some(at)), (plain.clone(), None)];
        still_given(&conn, &vault, &given).unwrap();
        // The connection switched away and back since the client key's check.
        away_and_back(&conn);
        let e = still_given(&conn, &vault, &given).unwrap_err();
        assert!(e.contains("connection changed"), "{e}");
        // A plain secret is bound to no connection.
        still_given(&conn, &vault, &given[1..]).unwrap();
        // Two client keys checked on different revisions: the connection changed between them.
        let now = conn.lock().unwrap().revision();
        let mixed = vec![(bound.clone(), Some(now)), (bound.clone(), Some(at))];
        assert!(still_given(&conn, &vault, &mixed).is_err());
        still_given(&conn, &vault, &[(bound.clone(), Some(now))]).unwrap();
        // A secret removed from the vault since it resolved, a plain one too.
        vault.remove_secret(&plain).unwrap();
        let e =
            still_given(&conn, &vault, &[(bound.clone(), Some(now)), (plain, None)]).unwrap_err();
        assert!(e.contains("removed"), "{e}");
        let _ = std::fs::remove_dir_all(d);
    }

    #[test]
    fn masks_as_core_lists_keys() {
        assert_eq!(masked("sk-ABCDEFGHIJKLMNOPQRSTUVWXYZ"), "sk******YZ");
        assert_eq!(masked("abcdefgh"), "ab******gh");
        assert_eq!(masked("short"), "**********");
        assert_eq!(masked("ébcdefgĥ"), "éb******gĥ");
    }
}
