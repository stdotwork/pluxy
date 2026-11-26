//! A model alias in the connected core, for a model row of the Claude Desktop page
//! (`claudeDesktop.tsx`): Claude Desktop sends a row's alias to core, so core must serve the
//! original model under that name too.
//!
//! The app asks core through the management API it is signed in to, after a native confirmation,
//! as a client key's capture does (`client_keys.rs`): it reads which accounts serve the model
//! (`GET /models`, `GET /accounts`), then adds `{"name": model, "alias": alias, "fork": true}` to
//! the `model_alias` of each account added by login that serves it (`PATCH /accounts/{id}`, the
//! call the web panel's account settings use). With `fork` the model keeps its own name. An
//! account set in core's config is changed only in core's config, so it is named and left alone;
//! a model only such accounts serve is refused. An alias core already serves for anything else is
//! refused, never replaced; one every account already has is reported, unasked.
//!
//! Once written, core must list the alias on those accounts; otherwise (a prefix the accounts are
//! asked with, an exclusion, or core not answering) each account's previous `model_alias` is put
//! back. Clear integration and Restore Backup of the Claude Desktop tool leave an alias made here:
//! core's config is not the tool's file. Linux only for now.

use crate::client_keys::{connected, fresh};
use crate::conn::Caller;
use serde_json::{json, Value};
use std::time::{Duration, Instant};
use tauri::AppHandle;

/// How long core may take to list an alias once it is written.
const LISTED_WITHIN: Duration = Duration::from_secs(5);

/// What an alias changes in core, worked out from its lists.
#[derive(Debug, PartialEq)]
pub struct Plan {
    /// The accounts added by login that serve the model and have no such alias yet, each with
    /// its `model_alias` as core lists it now (null: none).
    pub accounts: Vec<(String, Value)>,
    /// The accounts added by login that already have the alias.
    pub have: Vec<String>,
    /// The accounts set in core's config that serve the model, which are not changed.
    pub config: Vec<String>,
}

impl Plan {
    /// The IDs of the accounts the alias is added to.
    fn ids(&self) -> Vec<String> {
        self.accounts.iter().map(|(id, _)| id.clone()).collect()
    }
}

/// Whether s can be a model name or an alias: 1 to 256 printable characters, no spaces.
fn name_ok(s: &str) -> bool {
    !s.is_empty() && s.len() <= 256 && s.bytes().all(|b| b.is_ascii_graphic())
}

/// The accounts core lists as serving name (`GET /models`), each once, empty IDs left out; None
/// when it serves no such model.
fn serving(models: &Value, name: &str) -> Option<Vec<String>> {
    let m = models["models"]
        .as_array()?
        .iter()
        .find(|m| m["id"] == name)?;
    let mut ids: Vec<String> = vec![];
    for id in m["accounts"].as_array().into_iter().flatten() {
        if let Some(id) = id.as_str().filter(|id| !id.is_empty()) {
            if !ids.iter().any(|i| i == id) {
                ids.push(id.to_string());
            }
        }
    }
    Some(ids)
}

/// What making alias a name of model changes, from core's `GET /models` and `GET /accounts`
/// replies; the refusal says why nothing may change.
pub fn plan(model: &str, alias: &str, models: &Value, accounts: &Value) -> Result<Plan, String> {
    if !name_ok(model) || !name_ok(alias) {
        return Err("a model and its alias are names of 1 to 256 characters with no spaces".into());
    }
    // Core matches a model's name regardless of case, so such an alias would rename nothing.
    if alias.eq_ignore_ascii_case(model) {
        return Err(format!(
            "{alias} is the model's own name: no alias is needed"
        ));
    }
    let serves =
        serving(models, model).ok_or_else(|| format!("core serves no model named {model}"))?;
    let views = accounts["accounts"].as_array().cloned().unwrap_or_default();
    let mut p = Plan {
        accounts: vec![],
        have: vec![],
        config: vec![],
    };
    for id in &serves {
        let Some(v) = views.iter().find(|a| a["id"] == id.as_str()) else {
            continue;
        };
        if v["source"] != "login" {
            p.config.push(id.clone());
            continue;
        }
        let rules = v["model_alias"].as_array().cloned().unwrap_or_default();
        let named = |r: &Value| {
            r["alias"]
                .as_str()
                .is_some_and(|a| a.eq_ignore_ascii_case(alias))
        };
        match rules.iter().find(|r| named(r)) {
            // Core matches a rule's model regardless of case, and its alias as written.
            Some(r)
                if r["alias"] == alias
                    && r["name"]
                        .as_str()
                        .is_some_and(|n| n.eq_ignore_ascii_case(model)) =>
            {
                p.have.push(id.clone())
            }
            Some(r) => {
                return Err(format!(
                    "account {id} already has the alias {} for {}: choose another alias",
                    r["alias"].as_str().unwrap_or_default(),
                    r["name"].as_str().unwrap_or_default()
                ))
            }
            None => p.accounts.push((id.clone(), v["model_alias"].clone())),
        }
    }
    if p.accounts.is_empty() && p.have.is_empty() {
        if p.config.is_empty() {
            return Err(format!("core lists no account serving {model}"));
        }
        return Err(format!(
            "{model} is served only by accounts set in core's config ({}): add the alias to their model_alias there",
            p.config.join(", ")
        ));
    }
    // A name core serves already is taken unless only accounts with this very alias serve it.
    if let Some(on) = serving(models, alias) {
        if on.iter().any(|a| !p.have.contains(a)) {
            return Err(format!(
                "core already serves {alias} (on {}): choose another alias",
                on.join(", ")
            ));
        }
    }
    Ok(p)
}

/// Account IDs as the native confirmation shows them: core's, so each control character is
/// escaped and none can add a line to the dialog (`conn.rs`, shown_path).
fn shown(ids: &[String]) -> String {
    ids.iter()
        .map(|id| crate::conn::shown_path(id))
        .collect::<Vec<_>>()
        .join(", ")
}

/// The native confirmation's text: what core gets, where, and what stays.
fn summary(origin: &str, model: &str, alias: &str, p: &Plan) -> String {
    let which = if p.accounts.len() == 1 {
        "its account"
    } else {
        "its accounts"
    };
    let list = shown(&p.ids());
    let mut s = format!(
        "Add the model alias {alias} on the core at {origin}: requests for {alias} go to {model} on {which} {list}, added by login. {model} keeps its own name."
    );
    if !p.config.is_empty() {
        s.push_str(&format!(
            " The accounts set in core's config that serve {model} ({}) are not changed: add the alias there.",
            shown(&p.config)
        ));
    }
    s.push_str(" Clear and Restore backup leave this alias in core; remove it on the account's settings in core's panel.");
    s
}

/// id as one segment of a management path.
fn segment(id: &str) -> String {
    id.bytes()
        .map(|b| match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => {
                (b as char).to_string()
            }
            _ => format!("%{b:02X}"),
        })
        .collect()
}

/// Why core refused a call, from its reply.
fn why(r: &crate::client::Reply) -> String {
    r.error
        .as_ref()
        .map(|e| e.message.clone())
        .unwrap_or_else(|| format!("core answered {}", r.status))
}

/// Core's reply to `GET path`, or why it gave none.
fn get(caller: &Caller, path: &str) -> Result<Value, String> {
    let r = caller.call("GET", path, None);
    match (r.status, r.body.as_ref()) {
        (200, Some(b)) => Ok(b.clone()),
        _ => Err(format!("core did not answer GET {path}: {}", why(&r))),
    }
}

/// Core's `GET /models` and `GET /accounts` replies.
fn lists(caller: &Caller) -> Result<(Value, Value), String> {
    Ok((get(caller, "/models")?, get(caller, "/accounts")?))
}

/// Sets each account's model_alias back to the list it had; the accounts it could not set back.
fn put_back(caller: &Caller, done: &[(String, Value)]) -> Vec<String> {
    done.iter()
        .filter(|(id, list)| {
            let body = json!({ "model_alias": list });
            let path = format!("/accounts/{}", segment(id));
            caller.call("PATCH", &path, Some(&body)).status != 200
        })
        .map(|(id, _)| id.clone())
        .collect()
}

/// What the window gets: the alias, its model, the accounts that have it, the config accounts
/// left alone, and whether it was there already.
fn view(model: &str, alias: &str, p: &Plan, already: bool) -> Value {
    let mut on = p.have.clone();
    on.extend(p.ids());
    json!({"model": model, "alias": alias, "accounts": on, "config_accounts": p.config, "already": already})
}

/// What a retry says, after a refusal that changed nothing.
const AGAIN: &str = "Create the alias again.";

/// Makes alias a name of model in the connected core, on the accounts added by login that serve
/// it, once the person confirms. Run off the window's thread.
pub fn create(app: &AppHandle, model: &str, alias: &str) -> Result<Value, String> {
    let (model, alias) = (model.trim(), alias.trim());
    let (pin, target) = connected(app)?;
    let (models, accounts) = lists(&fresh(app, &pin, AGAIN)?)?;
    let asked = plan(model, alias, &models, &accounts)?;
    if asked.accounts.is_empty() {
        return Ok(view(model, alias, &asked, true));
    }
    let what = summary(&target.origin, model, alias, &asked);
    if !crate::confirmation::confirm_app(app, what, pin.revision)? {
        return Err("cancelled".into());
    }
    write(fresh(app, &pin, AGAIN), model, alias, &asked, LISTED_WITHIN)
}

/// Adds the alias the person confirmed (asked) through caller, the connection as it was when
/// they were asked (an error when it changed since, which sends nothing), if core's accounts are
/// still as asked; then core must list it on them within wait, or each account's previous
/// `model_alias` is put back.
fn write(
    caller: Result<Caller, String>,
    model: &str,
    alias: &str,
    asked: &Plan,
    wait: Duration,
) -> Result<Value, String> {
    let caller = caller?;
    let (models, accounts) = lists(&caller)?;
    let now = plan(model, alias, &models, &accounts)?;
    if now.ids() != asked.ids() || now.config != asked.config {
        return Err(format!(
            "core's accounts changed while you were asked. {AGAIN}"
        ));
    }
    let mut done: Vec<(String, Value)> = vec![];
    for (id, list) in &now.accounts {
        let mut rules = list.as_array().cloned().unwrap_or_default();
        rules.push(json!({"name": model, "alias": alias, "fork": true}));
        let path = format!("/accounts/{}", segment(id));
        let r = caller.call("PATCH", &path, Some(&json!({ "model_alias": rules })));
        if r.status != 200 {
            return Err(undone(
                &caller,
                &done,
                format!("core did not add the alias to {id}: {}", why(&r)),
            ));
        }
        done.push((id.clone(), list.clone()));
    }
    // Core lists the alias on every account it was added to, or it is taken out again: at once
    // when core cannot be asked (not signed in, not reached), after wait when it does not list it.
    let deadline = Instant::now() + wait;
    loop {
        let models = match get(&caller, "/models") {
            Ok(m) => m,
            Err(e) => {
                return Err(undone(
                    &caller,
                    &done,
                    format!("whether core serves {alias} could not be read ({e})"),
                ))
            }
        };
        if serving(&models, alias).is_some_and(|on| now.ids().iter().all(|id| on.contains(id))) {
            return Ok(view(model, alias, &now, false));
        }
        if Instant::now() > deadline {
            return Err(undone(
                &caller,
                &done,
                format!("core does not serve {alias} on {} with the alias added (a prefix those accounts are asked with, or an exclusion, may stop it)", now.ids().join(", ")),
            ));
        }
        std::thread::sleep(Duration::from_millis(200));
    }
}

/// e, with each account written so far set back, or named when it could not be.
fn undone(caller: &Caller, done: &[(String, Value)], e: String) -> String {
    if done.is_empty() {
        return e;
    }
    match put_back(caller, done).as_slice() {
        [] => format!("{e}; the alias was taken out again"),
        left => format!(
            "{e}; taking it out again failed on {}: remove the alias from their model_alias on core",
            left.join(", ")
        ),
    }
}

#[cfg(test)]
#[path = "core_aliases_tests.rs"]
mod tests;
