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
//! closes, and its answer is delivered only if the call is still granted then.
//!
//! Linux only, as the program host.

use crate::confirmation::{self, Gesture};
use crate::device::{self, Grant, Refused};
use crate::program::{self, CallError, Cancel, Context, Hop, Outbound};
use crate::App;
use serde_json::{json, Value};
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager};

/// The longest chain: its requester and four tools.
const MAX_CHAIN: usize = 5;
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

struct Inflight {
    caller: String,
    label: String,
    flag: Arc<AtomicBool>,
}

/// The page calls in flight.
#[derive(Default)]
pub struct Routes {
    inflight: Mutex<Vec<Inflight>>,
}

impl Routes {
    /// Cancels package caller's page calls: its import consent was revoked.
    pub fn revoked(&self, caller: &str) {
        for i in self.inflight.lock().unwrap().iter() {
            if i.caller == caller {
                i.flag.store(true, Ordering::SeqCst);
            }
        }
    }

    /// Cancels page label's calls: it closed.
    pub fn page_closed(&self, label: &str) {
        for i in self.inflight.lock().unwrap().iter() {
            if i.label == label {
                i.flag.store(true, Ordering::SeqCst);
            }
        }
    }
}

fn fail(code: &str, message: impl Into<String>) -> CallError {
    CallError {
        code: code.into(),
        message: message.into(),
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
    package: &str,
    op: &str,
    input: Value,
    ticket: Option<&str>,
    gesture: Option<Gesture>,
) -> Result<Value, CallError> {
    let grant = app
        .state::<App>()
        .device
        .lock()
        .unwrap()
        .main_grant(package, op, &roots())
        .map_err(refused)?;
    let origin = Origin {
        page: None,
        packages: vec![device::main_window()],
        gesture: Arc::new(Mutex::new(gesture)),
    };
    dispatch(
        app,
        grant,
        vec![Hop::Main],
        origin,
        input,
        ticket,
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
    let mut chain = o.context.chain.clone();
    chain.push(o.caller.clone());
    if chain
        .iter()
        .any(|h| h.package() == Some(o.package.as_str()))
    {
        let path: Vec<&str> = chain
            .iter()
            .map(|h| h.package().unwrap_or("main window"))
            .collect();
        return Err(fail(
            "permission",
            format!(
                "{id}'s call of {} would go back into its own chain ({})",
                o.package,
                path.join(", ")
            ),
        ));
    }
    if chain.len() >= MAX_CHAIN {
        return Err(fail(
            "permission",
            format!(
                "{id}'s call of {} would make a chain longer than {MAX_CHAIN}",
                o.package
            ),
        ));
    }
    let (grant, caller) = app
        .state::<App>()
        .device
        .lock()
        .unwrap()
        .native_grant((id, sha256, *generation), &o.package, &o.op, &roots())
        .map_err(refused)?;
    let mut packages = parent.packages.clone();
    packages.push(caller.package());
    let origin = Origin {
        page: parent.page.clone(),
        packages,
        gesture: Arc::clone(&parent.gesture),
    };
    dispatch(app, grant, chain, origin, o.input, None, o.cancel)
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
    let grant = |c: &device::Catalog| c.page_grant((&caller, &sha256), package, op, &roots());
    let (g, requester) = {
        let c = st.device.lock().unwrap();
        let g = grant(&c).map_err(refused)?;
        let r = c
            .requester(&caller, &roots())
            .ok_or_else(|| fail("permission", format!("{caller} is not installed")))?;
        (g, r)
    };
    let flag = Arc::new(AtomicBool::new(false));
    st.routes.inflight.lock().unwrap().push(Inflight {
        caller: caller.clone(),
        label: label.into(),
        flag: Arc::clone(&flag),
    });
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
    let r = dispatch(
        app,
        g,
        chain,
        origin,
        params["input"].clone(),
        None,
        Cancel::default().with(&flag),
    );
    st.routes
        .inflight
        .lock()
        .unwrap()
        .retain(|i| !Arc::ptr_eq(&i.flag, &flag));
    // Revoked, closed or changed while it ran: nothing it answered is delivered.
    if flag.load(Ordering::SeqCst) {
        return Err(fail(
            "permission",
            format!("{caller}'s call of {package} was revoked while it ran"),
        ));
    }
    grant(&st.device.lock().unwrap()).map_err(refused)?;
    r
}

/// The release digest of a device page's folder, `asset_url(caller, digest, "")`.
fn release_of(caller: &str, folder: &str) -> Option<String> {
    let digest = folder.trim_end_matches('/').rsplit('/').next()?;
    (device::asset_url(caller, digest, "") == folder).then(|| digest.to_string())
}

/// Checks input against grant's operation, has a write or launch confirmed, and calls it in a
/// context with chain and origin, pinned to the granted release.
fn dispatch(
    app: &AppHandle,
    grant: Grant,
    chain: Vec<Hop>,
    origin: Origin,
    input: Value,
    ticket: Option<&str>,
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
    authorize(app, &grant, &origin, &input)?;
    let context = Context {
        chain,
        expect: Some((grant.target.sha256().into(), grant.target.installed_at())),
        extra: Box::new(origin),
    };
    app.state::<App>().programs.call_with(
        grant.target.id(),
        &grant.op.id,
        input,
        ticket,
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
/// gesture may use a remembered approval of exactly that instead.
fn authorize(
    app: &AppHandle,
    grant: &Grant,
    origin: &Origin,
    input: &Value,
) -> Result<(), CallError> {
    let effect = grant.op.effect.as_str();
    if effect == "read" {
        return Ok(());
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
    let id = if origin.page.is_some() {
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
                    return c.consume(&id, &token).map(|_| ()).map_err(refuse);
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
    confirmed(app, &id).map_err(|e| fail("cancelled", e))
}

/// Waits for the person's answer to the native confirmation of prepared action id, which its
/// dialog's own handler leaves for native code, and spends its single-use token.
pub(crate) fn confirmed(app: &AppHandle, id: &str) -> Result<(), String> {
    let st = app.state::<App>();
    let deadline = Instant::now() + CONFIRMATION;
    let outcome = loop {
        if let Some(r) = st.confirmations.lock().unwrap().take_result(id) {
            break r;
        }
        if Instant::now() > deadline {
            return Err("the native confirmation expired".into());
        }
        std::thread::sleep(Duration::from_millis(100));
    };
    match outcome {
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
