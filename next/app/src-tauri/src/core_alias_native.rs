//! The native test's check of a model alias made in core (`core_aliases.rs`) on Windows and macOS
//! (native-test builds with `artifact-test-trust` only), in the vault check's session after the
//! client-key check (`client_key_native.rs`), against the session's test core in local mode
//! (`native/platform-tools.ts`). On Linux the Claude Desktop session checks the same command on the
//! app's own core (`claude_desktop_native.rs`, its `alias` step); that session does not run on
//! these systems. It drives the real app through the window's own command and the real native
//! confirmation, worked through the UI driver:
//!
//! - account: a login account of test/alpha (`e2e/testext`'s `api_key` sign-in) serves
//!   `fake-model`, as the config account `local-1` does (`e2e/panelcore`);
//! - cancel: `core_alias_create` asks a native confirmation naming the alias, the model, the login
//!   account, the config account it leaves alone and that Restore backup leaves the alias;
//!   cancelled, core is unchanged;
//! - confirm: the login account's `model_alias` holds the alias (forked: the model keeps its
//!   name) and core serves both names on it;
//! - again: asked again, it is already there, unasked.
//!
//! One line on stderr: `native-test: core_alias <JSON>`.

use super::admission::{press, require, settle, start};
use super::routing::showing;
use super::vault::{dialog_with, settle_unasked};
use super::{eval, log, wait_for};
use crate::App;
use serde_json::{json, Value};
use std::time::Duration;
use tauri::{AppHandle, Manager, Webview};

/// The login account the check adds.
const ACCOUNT: &str = "alias-login-1";
/// The account of the test core's config that serves the model too.
const CONFIG_ACCOUNT: &str = "local-1";
const MODEL: &str = "fake-model";
const ALIAS: &str = "claude-opus-5-fake";
/// Longer than the confirmation keeps its Confirm button unarmed once it shows.
const ARMED: Duration = Duration::from_millis(1300);

pub(crate) fn run(app: &AppHandle, main: &Webview) -> Result<(), String> {
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("core alias {name}: {e}"));
    let caller = app.state::<App>().conn.lock().unwrap().caller();
    // The accounts core lists as serving name, and the login account's model_alias.
    let served = |name: &str| -> Option<Vec<Value>> {
        caller.call("GET", "/models", None).body?["models"]
            .as_array()?
            .iter()
            .find(|m| m["id"] == name)
            .and_then(|m| m["accounts"].as_array().cloned())
    };
    let aliases = || -> Value {
        caller
            .call("GET", "/accounts", None)
            .body
            .and_then(|b| {
                b["accounts"]
                    .as_array()?
                    .iter()
                    .find(|a| a["id"] == ACCOUNT)
                    .map(|a| a["model_alias"].clone())
            })
            .unwrap_or(json!("no such account"))
    };
    let input = json!({"model": MODEL, "alias": ALIAS});
    let ask = |cancel: bool| -> Result<Value, String> {
        let slot = start(main, "core_alias_create", input.clone())?;
        // Answered without a confirmation: say so now, rather than wait for one.
        wait_for("the alias's native confirmation", || {
            if showing() {
                return Some(Ok(()));
            }
            eval(
                main,
                &format!("window.__pluxyAdmission?.[{}] ?? null", json!(slot)),
            )
            .filter(|v| !v.is_null())
            .map(|r| Err(format!("core_alias_create answered without asking: {r}")))
        })??;
        dialog_with(
            app,
            &[ALIAS, MODEL, ACCOUNT, CONFIG_ACCOUNT, "Restore backup"],
        )?;
        if cancel {
            press("alt+c")?;
        } else {
            std::thread::sleep(ARMED);
            press("alt+o")?;
        }
        settle(main, &slot)
    };
    let mut out = json!({});

    step(
        "account",
        (|| {
            let body = json!({"id": ACCOUNT, "extension": "test/alpha", "method": "api_key",
                "input": {"api_key": crate::managed::random_key()}});
            let r = caller.call("POST", "/accounts", Some(&body));
            require(
                r.status == 202,
                format!("core did not take the login account: {r:?}"),
            )?;
            wait_for("core serving fake-model on the login account", || {
                served(MODEL).filter(|a| a.contains(&json!(ACCOUNT)))
            })?;
            require(
                served(MODEL).is_some_and(|a| a.contains(&json!(CONFIG_ACCOUNT))),
                "the config account does not serve fake-model",
            )?;
            require(
                aliases().is_null() && served(ALIAS).is_none(),
                format!("the login account starts with an alias: {}", aliases()),
            )
        })(),
    )?;

    step(
        "cancel",
        (|| {
            let r = ask(true)?;
            require(
                r["ok"] == false && r["error"] == "cancelled",
                format!("a cancelled alias went ahead: {r}"),
            )?;
            require(
                aliases().is_null() && served(ALIAS).is_none(),
                "a cancelled alias changed core",
            )
        })(),
    )?;

    step(
        "confirm",
        (|| {
            let r = ask(false)?;
            require(
                r["ok"] == true
                    && r["value"]["accounts"] == json!([ACCOUNT])
                    && r["value"]["config_accounts"] == json!([CONFIG_ACCOUNT]),
                format!("the confirmed alias: {r}"),
            )?;
            require(
                aliases() == json!([{"name": MODEL, "alias": ALIAS, "fork": true}]),
                format!("the account's model_alias: {}", aliases()),
            )?;
            require(
                served(ALIAS).is_some_and(|a| a.contains(&json!(ACCOUNT)))
                    && served(MODEL).is_some_and(|a| a.contains(&json!(ACCOUNT))),
                "core does not serve the alias and the model on the account",
            )?;
            out["confirmed"] = r["value"].clone();
            Ok(())
        })(),
    )?;

    step(
        "again",
        (|| {
            let slot = start(main, "core_alias_create", input.clone())?;
            let r = settle_unasked(main, &slot)?;
            require(
                r["ok"] == true && r["value"]["already"] == true,
                format!("an alias core already serves: {r}"),
            )
        })(),
    )?;

    out["passed"] = json!(true);
    log("core_alias", out);
    Ok(())
}
