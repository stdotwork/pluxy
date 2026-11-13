//! The router: every call of a native tool's operation passes here, from the window
//! (`program_call`), from a page (its `native.call` method, which never reaches the window) and
//! from another tool (a `call` frame on its pipe, `program::Outbound`). Each is decided against
//! the catalog as it is then (`device::Catalog::main_grant`, `native_grant`, `page_grant`): the
//! caller's identity is the host's (the window, the page webview's binding, the tool's run),
//! never the call's. The router sets the call's chain, refuses a call back into the chain and
//! chains longer than `MAX_CHAIN`, checks the input against the operation's schema, and asks for
//! a fresh native confirmation of a write- or launch-class call naming the whole chain and the
//! exact input. A launch from a chain the window started with a native gesture may use the
//! person's remembered approval of exactly that chain and launch; a chain a page started never
//! does. The call then goes to the program host pinned to the release it was granted for.
//!
//! A page's call in flight is cancelled when its package's import consent is revoked or its page
//! closes, and its answer is delivered only if the call is still granted then. A call cancelled
//! while its native confirmation is open withdraws the confirmation; a confirmed call is granted
//! again before it runs. The rules that need no app are in `route_rules.rs`.
//!
//! Linux only, as the program host.

use crate::confirmation::{self, Gesture};
use crate::device::{self, Grant, Refused};
use crate::program::{self, CallError, Cancel, Context, Hop, Outbound};
use crate::App;
use serde_json::{json, Value};
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tauri::{AppHandle, Manager};

#[path = "route_rules.rs"]
mod rules;
pub use rules::Routes;
/// How long a call waits for its native confirmation, which expires after a minute.
const CONFIRMATION: Duration = Duration::from_secs(75);
/// The largest input, as the program host takes it.
const MAX_INPUT: usize = 64 << 10;

/// The router's record of a call, kept in its `Context`: the page that started its chain, if a
/// page did, each hop as native confirmations name it, and the native gesture that started it,
/// until a launch uses it.
struct Origin {
    page: Option<String>,
    packages: Vec<confirmation::Package>,
    gesture: Arc<Mutex<Option<Gesture>>>,
}

fn fail(code: &str, message: impl Into<String>) -> CallError {
    CallError {
        code: code.into(),
        message: message.into(),
        unknown: false,
    }
}

fn refused(r: Refused) -> CallError {
    fail(r.code, r.message)
}

fn roots() -> crate::trust::TrustStore {
    crate::trust::TrustStore::current()
}

/// The window's call of package's operation op with input. With a gesture (a native event
/// handler's), a launch may use a remembered approval.
pub fn call_main(
    app: &AppHandle,
    call: program::Call,
    package: &str,
    op: &str,
    input: Value,
    gesture: Option<Gesture>,
) -> Result<Value, CallError> {
    let grant = || {
        app.state::<App>()
            .device
            .lock()
            .unwrap()
            .main_grant(package, op, &roots())
            .map_err(refused)
    };
    let origin = Origin {
        page: None,
        packages: vec![device::main_window()],
        gesture: Arc::new(Mutex::new(gesture)),
    };
    dispatch(
        app,
        call,
        grant()?,
        &grant,
        vec![Hop::Main],
        origin,
        input,
        Cancel::default(),
    )
}

/// The router the program host gives tools' own calls to.
pub fn route(app: AppHandle) -> program::Route {
    Arc::new(move |o| outbound(&app, o))
}

fn outbound(app: &AppHandle, o: Outbound) -> Result<Value, CallError> {
    let parent = o.context.extra.downcast_ref::<Origin>().ok_or_else(|| {
        fail(
            "permission",
            "the call it serves was not made by the router",
        )
    })?;
    let Hop::Package {
        id,
        sha256,
        generation,
    } = &o.caller
    else {
        return Err(fail(
            "permission",
            "only a tool's run makes calls of its own",
        ));
    };
    let chain = rules::extend(&o.context.chain, &o.caller, &o.package)?;
    let grant = || {
        app.state::<App>()
            .device
            .lock()
            .unwrap()
            .native_grant((id, sha256, *generation), &o.package, &o.op, &roots())
            .map_err(refused)
    };
    let (g, caller) = grant()?;
    let mut packages = parent.packages.clone();
    packages.push(caller.package());
    let origin = Origin {
        page: parent.page.clone(),
        packages,
        gesture: Arc::clone(&parent.gesture),
    };
    // Registered like any call, so `cancel_all` reaches it too.
    let call = app.state::<App>().programs.begin_call(None)?;
    let regrant = || grant().map(|(g, _)| g);
    dispatch(app, call, g, &regrant, chain, origin, o.input, o.cancel)
}

/// A page's `native.call` with params `{package, op, input}`, call id of page label's document
/// generation: answered to the page, never through the window.
pub fn call_page(app: &AppHandle, label: &str, generation: &str, id: &str, params: &Value) {
    let outcome = page_call(app, label, params);
    let msg = match outcome {
        Ok(v) => json!({"v": 0, "id": id, "result": v}),
        Err(e) => {
            json!({"v": 0, "id": id, "error": {"code": page_code(&e.code), "message": e.message}})
        }
    };
    let _ = crate::deliver_page(
        app,
        label,
        generation,
        json!({"type": "message", "data": msg}),
    );
}

/// The page channel's code for a call error (`../../ui/extension-pages/README.md`).
fn page_code(code: &str) -> &'static str {
    match code {
        "permission" | "cancelled" => "forbidden",
        "invalid_request" => "bad_request",
        _ => "unavailable",
    }
}

fn page_call(app: &AppHandle, label: &str, params: &Value) -> Result<Value, CallError> {
    let st = app.state::<App>();
    let binding = st
        .pages
        .lock()
        .unwrap()
        .get(label)
        .cloned()
        .ok_or_else(|| fail("permission", "the page is closed"))?;
    let (caller, sha256) = binding
        .extension
        .strip_prefix("device:")
        .zip(binding.device.as_deref())
        .and_then(|(caller, folder)| Some((caller.to_string(), release_of(caller, folder)?)))
        .ok_or_else(|| {
            fail(
                "permission",
                "only pages of packages on this device call native operations",
            )
        })?;
    let (package, op) = (
        params["package"].as_str().unwrap_or_default(),
        params["op"].as_str().unwrap_or_default(),
    );
    let grant = || {
        st.device
            .lock()
            .unwrap()
            .page_grant((&caller, &sha256), package, op, &roots())
            .map_err(refused)
    };
    // Registered before it is granted (`rules::page_call`), so no revocation slips between.
    let r = rules::page_call(
        &st.routes,
        &caller,
        label,
        package,
        || {
            let g = grant()?;
            let r = st
                .device
                .lock()
                .unwrap()
                .requester(&caller, &roots())
                .ok_or_else(|| fail("permission", format!("{caller} is not installed")))?;
            Ok((g, r))
        },
        |(g, requester), cancel| {
            let origin = Origin {
                page: Some(label.into()),
                packages: vec![requester],
                gesture: Arc::new(Mutex::new(None)),
            };
            let chain = vec![Hop::Page {
                id: caller.clone(),
                sha256: sha256.clone(),
                page: binding.page.clone(),
            }];
            st.programs.begin_call(None).and_then(|call| {
                dispatch(
                    app,
                    call,
                    g,
                    &grant,
                    chain,
                    origin,
                    params["input"].clone(),
                    cancel,
                )
            })
        },
    );
    // Changed while it ran: nothing it answered is delivered.
    grant()?;
    r
}

/// The release digest of a device page's folder, `asset_url(caller, digest, "")`.
fn release_of(caller: &str, folder: &str) -> Option<String> {
    let digest = folder.trim_end_matches('/').rsplit('/').next()?;
    (device::asset_url(caller, digest, "") == folder).then(|| digest.to_string())
}

/// Checks input against grant's operation, has a write or launch confirmed, grants it again once
/// confirmed (regrant must give the same release and operation), and calls it in a context with
/// chain and origin, pinned to the granted release.
#[allow(clippy::too_many_arguments)]
fn dispatch(
    app: &AppHandle,
    call: program::Call,
    grant: Grant,
    regrant: &dyn Fn() -> Result<Grant, CallError>,
    chain: Vec<Hop>,
    origin: Origin,
    input: Value,
    cancel: Cancel,
) -> Result<Value, CallError> {
    if serde_json::to_vec(&input).map_or(usize::MAX, |b| b.len()) > MAX_INPUT {
        return Err(fail(
            "invalid_request",
            format!("the input is larger than {MAX_INPUT} bytes"),
        ));
    }
    program::schema::check(&grant.op.input_schema, &input, "input")
        .map_err(|e| fail("invalid_request", e))?;
    if authorize(app, &grant, &origin, &input, &call.cancelled_by(&cancel))? {
        // Asked: the catalog may have changed while the person answered.
        let now = regrant()?;
        if now.target != grant.target || now.op != grant.op {
            return Err(fail(
                "permission",
                format!(
                    "{}'s {} changed while it was being confirmed; call it again",
                    grant.target.id(),
                    grant.op.id
                ),
            ));
        }
    }
    let context = Context {
        chain,
        expect: Some((grant.target.sha256().into(), grant.target.installed_at())),
        extra: Box::new(origin),
    };
    app.state::<App>().programs.run_call_with(
        call,
        grant.target.id(),
        &grant.op.id,
        input,
        Arc::new(context),
        cancel,
    )
}

/// The launch a launch-class input names.
fn launch_of(
    input: &Value,
    executor: &confirmation::Package,
) -> Result<confirmation::Launch, CallError> {
    let path = |k: &str| {
        input[k]
            .as_str()
            .map(PathBuf::from)
            .ok_or_else(|| fail("invalid_request", format!("a launch names its {k}")))
    };
    let argv = input["argv"]
        .as_array()
        .and_then(|a| {
            a.iter()
                .map(|v| v.as_str().map(String::from))
                .collect::<Option<Vec<_>>>()
        })
        .ok_or_else(|| fail("invalid_request", "a launch names its argv, as strings"))?;
    Ok(confirmation::Launch {
        executable: path("executable")?,
        argv,
        cwd: path("cwd")?,
        terminal_adapter: executor.clone(),
        // shortcut: no launch environment is passed yet; P8h1's environment profile replaces
        // this revision.
        environment_revision: "none".into(),
        environment_review: vec![],
        secret_uses: vec![],
    })
}

/// A read goes ahead. A write or launch needs the person's native confirmation of the whole
/// chain and the exact input, now; a launch from a chain the window started with a native
/// gesture may use a remembered approval of exactly that instead. A cancel while the
/// confirmation is open withdraws it. Whether it asked.
fn authorize(
    app: &AppHandle,
    grant: &Grant,
    origin: &Origin,
    input: &Value,
    cancel: &Cancel,
) -> Result<bool, CallError> {
    let effect = grant.op.effect.as_str();
    if effect == "read" {
        return Ok(false);
    }
    let executor = grant.target.package();
    let launch = if effect == "launch" {
        Some(launch_of(input, &executor)?)
    } else {
        None
    };
    let action = confirmation::Action {
        what: format!(
            "{} {}: {} ({}). Input: {}",
            grant.target.id(),
            grant.op.id,
            grant.op.description,
            effect,
            input
        ),
        chain: origin.packages.clone(),
        executor,
        native_tool: true,
        files: vec![],
        launch,
    };
    let refuse = |e: String| fail("permission", e);
    let st = app.state::<App>();
    let page = origin.page.is_some();
    // The rate and pending limits first, then any executable hashed without the lock.
    st.confirmations
        .lock()
        .unwrap()
        .room(page, &action.chain[0].id)
        .map_err(refuse)?;
    if let Some(l) = &action.launch {
        let h = confirmation::Hashed::of(&l.executable).map_err(refuse)?;
        st.confirmations.lock().unwrap().warm(h);
    }
    let id = if page {
        let p = st
            .confirmations
            .lock()
            .unwrap()
            .prepare_page(action)
            .map_err(refuse)?;
        confirmation::page_start(app, p).map_err(refuse)?
    } else {
        let mut c = st.confirmations.lock().unwrap();
        let p = c.prepare_main(action).map_err(refuse)?;
        let gesture = origin.gesture.lock().unwrap().take();
        let p = match gesture {
            Some(g) => match c.remembered(p, g) {
                Ok(token) => {
                    let id = c.token_action(&token).unwrap_or_default().to_string();
                    // shortcut: the confirmed launch's opened executable and cwd (`Authorized`)
                    // are dropped here; P8h1's spawn takes them over and launches from them.
                    return c.consume(&id, &token).map(|_| false).map_err(refuse);
                }
                Err(p) => *p,
            },
            None => p,
        };
        let id = c.queue(p).map_err(refuse)?;
        drop(c);
        confirmation::request(app, id.clone()).map_err(refuse)?;
        id
    };
    confirmed(app, &id, cancel)
        .map(|_| true)
        .map_err(|e| fail("cancelled", e))
}

/// Waits for the person's answer to the native confirmation of prepared action id, which its
/// dialog's own handler leaves for native code, and spends its single-use token. Once cancel is
/// set, the confirmation is withdrawn and its dialog closes.
pub(crate) fn confirmed(app: &AppHandle, id: &str, cancel: &Cancel) -> Result<(), String> {
    let st = app.state::<App>();
    let outcome = rules::wait(
        || st.confirmations.lock().unwrap().take_result(id),
        cancel,
        CONFIRMATION,
        || st.confirmations.lock().unwrap().withdraw(id),
    )?;
    match outcome {
        // shortcut: the confirmed launch's opened executable and cwd (`Authorized`) are dropped
        // here; P8h1's spawn takes them over and launches from them.
        Ok(Some(token)) => st
            .confirmations
            .lock()
            .unwrap()
            .consume(id, &token)
            .map(|_| ()),
        Ok(None) => Err("the native confirmation was cancelled".into()),
        Err(e) => Err(e),
    }
}
