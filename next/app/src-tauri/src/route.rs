//! The router: every call of a native tool's operation passes here, from the window
//! (`program_call`), from a page (its `native.call` method, which never reaches the window) and
//! from another tool (a `call` frame on its pipe, `program::Outbound`). Each is decided against
//! the catalog as it is then (`device::Catalog::main_grant`, `native_grant`, `page_grant`): the
//! caller's identity is the host's (the window, the page webview's binding, the tool's run),
//! never the call's. The router sets the call's chain, refuses a call back into the chain and
//! chains longer than `MAX_CHAIN`, checks the input against the operation's schema, and asks for
//! a fresh native confirmation of a write- or launch-class call naming the whole chain and the
//! exact input. Only the window's own direct call, with a native gesture, may use the person's
//! remembered approval of exactly that launch. A call with a tool hop in its chain is prepared as
//! an import, and one a page started as a page's Start: neither uses or saves a remembered
//! approval, and their dialogs offer no Remember. The call then goes to the program host pinned
//! to the release it was granted for.
//!
//! A page's call in flight is cancelled when its package's import consent is revoked or its page
//! closes, and its answer is delivered only if the call is still granted then. A call cancelled
//! while its native confirmation is open withdraws the confirmation; a confirmed call is granted
//! again before it runs. The rules that need no app are in `route_rules.rs`.
//!
//! A tool's `jobs` frame asks the host's job service (`jobs.rs`) for the call it serves: the
//! registrations of a kind it consumes (`device::Catalog::registrations`), to start a job of one
//! (`job_grant`), or to cancel one of its jobs. A start is decided as a call of the producer's
//! operation with the consumer as caller: the same chain, input and confirmation rules, pinned to
//! the producer's release. It is answered with the job's ID at once; the producer's call and the
//! deliveries to the consumer's deliver operation then run on a thread of the job's own, each
//! batch granted again first, and the consumer is leased until the job ends.
//!
//! An operation's secret fields (`secrets` in its manifest) carry secrets from the vault
//! (`vault.rs`). The window fills one with a vault secret's ID; a tool, only with a reference it was
//! given in the call it serves, to an operation its import allows secret references for; a page,
//! never. A secret or reference anywhere else in an input is refused. A call that delivers a
//! secret's value, or one the window gives a secret to, asks a native confirmation naming each
//! secret, even for a read. The target then gets, in each secret field, a new reference bound to
//! the secret, its release and this call; a `value` field's secret also travels in the call's frame,
//! and nowhere else. The references end with the call.
//!
//! Linux only, as the program host.

use crate::confirmation::{self, Gesture};
use crate::device::{self, Grant, Refused};
use crate::program::{self, CallError, Cancel, Context, Hop, Outbound};
use crate::vault::{Holder, Vault};
use crate::App;
use serde_json::{json, Value};
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tauri::{AppHandle, Manager};

#[path = "jobs.rs"]
mod jobs;
#[path = "route_rules.rs"]
mod rules;
pub use rules::Routes;
/// How long a call waits for its native confirmation, which expires after a minute.
const CONFIRMATION: Duration = Duration::from_secs(75);
/// The largest input, as the program host takes it.
const MAX_INPUT: usize = 64 << 10;

/// The router's record of a call, kept in its `Context`: each hop as native confirmations name
/// it, and the native gesture the window's own direct call started it with, until a launch uses it.
struct Origin {
    packages: Vec<confirmation::Package>,
    gesture: Arc<Mutex<Option<Gesture>>>,
    /// The vault's token of the call: references given in it are bound to it.
    token: u64,
}

/// Who fills a call's secret fields: the window, with vault secret IDs; a page, never; a tool,
/// with references it was given in the call (token) it serves.
enum Filler {
    Window,
    Page,
    Tool { holder: Holder, call: u64 },
}

/// A secret field of a call, filled: its vault secret, the secret's label, and whether the target
/// gets the secret's value (or a reference only).
struct Filled {
    field: String,
    secret: String,
    label: String,
    value: bool,
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
        packages: vec![device::main_window()],
        gesture: Arc::new(Mutex::new(gesture)),
        token: 0,
    };
    dispatch(
        app,
        call,
        grant()?,
        &grant,
        vec![Hop::Main],
        origin,
        Filler::Window,
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
    if o.jobs {
        return jobs_request(app, &o, parent, (id, sha256, *generation));
    }
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
        packages,
        // A call through a tool never uses a gesture: it is an import.
        gesture: Arc::new(Mutex::new(None)),
        token: 0,
    };
    let filler = Filler::Tool {
        holder: Holder {
            id: id.clone(),
            sha256: sha256.clone(),
            generation: *generation,
        },
        call: parent.token,
    };
    // Registered like any call, so `cancel_all` reaches it too.
    let call = app.state::<App>().programs.begin_call(None)?;
    let regrant = || grant().map(|(g, _)| g);
    dispatch(
        app, call, g, &regrant, chain, origin, filler, o.input, o.cancel,
    )
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
                packages: vec![requester],
                gesture: Arc::new(Mutex::new(None)),
                token: 0,
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
                    Filler::Page,
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

/// The one refusal of an input that is not valid for grant's operation, whatever is wrong with it:
/// it does not say whether a string was a secret's value or a reference.
fn invalid_input(grant: &Grant) -> CallError {
    fail(
        "invalid_request",
        format!(
            "{}'s {} refuses this input: it does not match the operation's input schema, or it holds a secret or secret reference outside the operation's secret fields",
            grant.target.id(),
            grant.op.id
        ),
    )
}

/// The secret fields filler fills in input, for grant's operation; refused when a secret or a
/// reference is anywhere else in it, or filler may not fill them so.
fn secret_fields(
    vault: &Vault,
    grant: &Grant,
    input: &Value,
    filler: &Filler,
) -> Result<Vec<Filled>, CallError> {
    let (target, op) = (grant.target.id(), &grant.op.id);
    let refuse = |m: String| fail("permission", m);
    let secret_field = |k: &str| grant.op.secrets.contains_key(k);
    if rules::stray_secret(input, &secret_field, &|s| vault.carries(s)) {
        return Err(invalid_input(grant));
    }
    let mut filled = Vec::new();
    let fields = input.as_object().into_iter().flatten();
    for (field, v) in fields {
        let Some(how) = grant.op.secrets.get(field) else {
            continue;
        };
        // The input schema makes each secret field a string.
        let s = v.as_str().ok_or_else(|| invalid_input(grant))?;
        let secret = match filler {
            Filler::Window => vault.secret(s).map(|x| x.id).ok_or_else(|| {
                fail(
                    "invalid_request",
                    format!("{field} names no secret in the vault"),
                )
            })?,
            Filler::Page => {
                return Err(refuse(format!(
                    "a page cannot pass secrets: {target}'s {op} {field}"
                )))
            }
            Filler::Tool { holder, call } => {
                if !grant.passes_secrets {
                    return Err(refuse(format!(
                        "{}'s import of {target} does not allow secret references for {op}",
                        holder.id
                    )));
                }
                vault.present(s, holder, *call).map_err(|e| {
                    refuse(format!(
                        "{target}'s {op} {field}: the secret reference is refused: {e}"
                    ))
                })?
            }
        };
        let label = vault.secret(&secret).map(|x| x.label).unwrap_or_default();
        filled.push(Filled {
            field: field.clone(),
            secret,
            label,
            value: how == "value",
        });
    }
    Ok(filled)
}

/// Checks input against grant's operation and its secret fields, has a write, a launch or a
/// secret's delivery confirmed, grants it again once confirmed (regrant must give the same release
/// and operation), and calls it in a context with chain and origin, pinned to the granted release,
/// with each secret field holding a new reference for the target.
#[allow(clippy::too_many_arguments)]
fn dispatch(
    app: &AppHandle,
    call: program::Call,
    grant: Grant,
    regrant: &dyn Fn() -> Result<Grant, CallError>,
    chain: Vec<Hop>,
    mut origin: Origin,
    filler: Filler,
    mut input: Value,
    cancel: Cancel,
) -> Result<Value, CallError> {
    let filled = checked(
        app,
        &grant,
        regrant,
        &chain,
        &origin,
        &filler,
        &input,
        &call.cancelled_by(&cancel),
    )?;
    let vault = Arc::clone(&app.state::<App>().vault);
    // Each secret field gets a reference for the target's release, in this call.
    let token = vault.begin_call();
    origin.token = token;
    let holder = Holder {
        id: grant.target.id().into(),
        sha256: grant.target.sha256().into(),
        generation: grant.target.installed_at(),
    };
    let mut secrets = Vec::new();
    for f in &filled {
        let minted = vault.mint(&f.secret, &holder, token).and_then(|r| {
            if !f.value {
                return Some(r);
            }
            let v = vault.value(&f.secret)?;
            secrets.push((r.clone(), v));
            Some(r)
        });
        let Some(r) = minted else {
            vault.end_call(token);
            return Err(fail(
                "permission",
                format!("the secret for {} was removed from the vault", f.field),
            ));
        };
        input[f.field.as_str()] = Value::String(r);
    }
    let context = Context {
        chain,
        expect: Some((grant.target.sha256().into(), grant.target.installed_at())),
        extra: Box::new(origin),
        feed: None,
        turn_bounded: false,
        secrets,
    };
    let r = app.state::<App>().programs.run_call_with(
        call,
        grant.target.id(),
        &grant.op.id,
        input,
        Arc::new(context),
        cancel,
    );
    vault.end_call(token);
    r
}

/// Checks input against grant's operation and the secret fields filler fills in it, has a write,
/// a launch or a secret's delivery confirmed (a cancel withdraws the confirmation), and grants it
/// again once confirmed: regrant must give the same release and operation. Gives the fields filled.
#[allow(clippy::too_many_arguments)]
fn checked(
    app: &AppHandle,
    grant: &Grant,
    regrant: &dyn Fn() -> Result<Grant, CallError>,
    chain: &[Hop],
    origin: &Origin,
    filler: &Filler,
    input: &Value,
    cancel: &Cancel,
) -> Result<Vec<Filled>, CallError> {
    if serde_json::to_vec(input).map_or(usize::MAX, |b| b.len()) > MAX_INPUT {
        return Err(fail(
            "invalid_request",
            format!("the input is larger than {MAX_INPUT} bytes"),
        ));
    }
    program::schema::check(&grant.op.input_schema, input, "input")
        .map_err(|_| invalid_input(grant))?;
    let vault = Arc::clone(&app.state::<App>().vault);
    let filled = secret_fields(&vault, grant, input, filler)?;
    // A secret's value delivered, or any secret the window gives, is the person's decision.
    let ask = filled
        .iter()
        .any(|f| f.value || matches!(filler, Filler::Window));
    let preparation = rules::preparation(chain);
    if authorize(app, grant, origin, preparation, input, &filled, ask, cancel)? {
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
    Ok(filled)
}

/// Consumer caller's request to the host's job service, as o names it.
fn jobs_request(
    app: &AppHandle,
    o: &Outbound,
    parent: &Origin,
    caller: (&str, &str, u64),
) -> Result<Value, CallError> {
    let st = app.state::<App>();
    match o.op.as_str() {
        "registrations" => {
            let kind = o.input["kind"].as_str().unwrap_or_default();
            let regs = st
                .device
                .lock()
                .unwrap()
                .registrations(caller, kind, &roots())
                .map_err(refused)?;
            Ok(json!({"registrations": regs}))
        }
        "start" => start_job(app, o, parent, caller),
        "cancel" => {
            let job = o.input["job"].as_str().unwrap_or_default();
            st.routes.jobs.cancel(caller.0, job)?;
            Ok(json!({"cancelled": job}))
        }
        op => Err(fail(
            "unsupported",
            format!("the host's job service has no request {op:?}"),
        )),
    }
}

/// Consumer caller's start of a job of `kind`, of the registration o's input names (`package`,
/// `sha256`, `generation`), with `input`: granted and confirmed as a call of the producer's
/// operation, then answered with the job's ID while the job runs on a thread of its own.
fn start_job(
    app: &AppHandle,
    o: &Outbound,
    parent: &Origin,
    caller: (&str, &str, u64),
) -> Result<Value, CallError> {
    let st = app.state::<App>();
    let i = &o.input;
    let kind = i["kind"].as_str().unwrap_or_default().to_string();
    let (producer, sha256, generation) = (
        i["package"].as_str().unwrap_or_default(),
        i["sha256"].as_str().unwrap_or_default(),
        i["generation"].as_u64().unwrap_or_default(),
    );
    let grant = || {
        st.device
            .lock()
            .unwrap()
            .job_grant(caller, &kind, (producer, sha256, generation), &roots())
            .map_err(refused)
    };
    // Counted, and reachable by a revocation, before it is granted and confirmed: the bounds hold
    // for the confirmations too, and a change while it starts refuses it.
    let (job, (g, chain, origin, input, lease)) =
        st.routes.jobs.start(caller.0, producer, &kind, || {
            let g = grant()?;
            let chain = rules::extend(&o.context.chain, &o.caller, producer)?;
            let mut packages = parent.packages.clone();
            packages.push(g.consumer.package());
            let origin = Origin {
                packages,
                // A job is started by a tool: it never uses a gesture.
                gesture: Arc::new(Mutex::new(None)),
                token: 0,
            };
            let input = i["input"].clone();
            // A job's operation has no secret fields (`device::Catalog` refuses them at install),
            // so a secret or reference anywhere in its input is refused here.
            let filler = Filler::Tool {
                holder: Holder {
                    id: caller.0.into(),
                    sha256: caller.1.into(),
                    generation: caller.2,
                },
                call: parent.token,
            };
            let filled = checked(
                app,
                &g.producer,
                &|| grant().map(|g| g.producer),
                &chain,
                &origin,
                &filler,
                &input,
                &o.cancel,
            )?;
            if !filled.is_empty() {
                return Err(fail("permission", "a job's operation takes no secrets"));
            }
            // The consumer stays up for the job's deliveries.
            let lease = st.programs.lease(caller.0)?;
            Ok((g, chain, origin, input, lease))
        })?;
    let id = job.id.clone();
    let (a, j) = (app.clone(), Arc::clone(&job));
    let spawned = std::thread::Builder::new()
        .name("pluxy-job".into())
        .spawn(move || {
            run_job(&a, &j, g, chain, origin, input, &kind);
            drop(lease);
            a.state::<App>().routes.jobs.remove(&j);
        });
    if let Err(e) = spawned {
        st.routes.jobs.remove(&job);
        return Err(fail("unavailable", format!("starting the job: {e}")));
    }
    Ok(json!({"job": id}))
}

/// Runs job of kind: the producer's call, as g grants it in chain, beside the pump delivering
/// its batches and its end to the consumer's deliver operation, each delivery pinned to the
/// consumer's release, with the job's chain and then the producer as its chain.
fn run_job(
    app: &AppHandle,
    job: &Arc<jobs::Job>,
    g: device::JobGrant,
    chain: Vec<Hop>,
    origin: Origin,
    input: Value,
    kind: &str,
) {
    let st = app.state::<App>();
    let programs = &st.programs;
    let source = serde_json::to_value(&g.registration).unwrap_or(Value::Null);
    let c = (
        g.consumer.id().to_string(),
        g.consumer.sha256().to_string(),
        g.consumer.installed_at(),
    );
    let p = (
        g.producer.target.id().to_string(),
        g.producer.target.sha256().to_string(),
        g.producer.target.installed_at(),
    );
    let mut delivery_chain = chain.clone();
    delivery_chain.push(Hop::Package {
        id: p.0.clone(),
        sha256: p.1.clone(),
        generation: p.2,
    });
    let mut delivery_packages = origin.packages.clone();
    delivery_packages.push(g.producer.target.package());
    let call = Context {
        chain,
        expect: Some((p.1.clone(), p.2)),
        extra: Box::new(origin),
        feed: Some(Arc::clone(job) as Arc<dyn program::Feed>),
        turn_bounded: false,
        secrets: Vec::new(),
    };
    let op = g.producer.op.id.clone();
    let producer = p.0.clone();
    // Both sides still the releases it was granted for.
    let valid = || {
        st.device
            .lock()
            .unwrap()
            .job_grant((&c.0, &c.1, c.2), kind, (&p.0, &p.1, p.2), &roots())
            .map(|_| ())
            .map_err(refused)
    };
    let consumer_valid = || {
        st.device
            .lock()
            .unwrap()
            .consumer((&c.0, &c.1, c.2), kind, &roots())
            .is_ok()
    };
    let vault = Arc::clone(&st.vault);
    let deliver = |mut envelope: Value, end: bool| {
        // A deliver operation has no secret fields: a secret or reference a producer put in a
        // batch is refused like any input's, which ends the job; an end that holds one is
        // delivered as failed, without it.
        if rules::stray_secret(&envelope, &|_| false, &|s| vault.carries(s)) {
            let e = fail(
                "invalid_request",
                format!(
                    "{}'s {} refuses this delivery: it holds a secret or secret reference",
                    c.0, g.deliver.id
                ),
            );
            if !end {
                return Err(e);
            }
            envelope = json!({"job": envelope["job"], "kind": envelope["kind"],
                "event": "failed", "error": {"code": e.code, "message": e.message},
                "batches": envelope["batches"], "source": envelope["source"]});
        }
        let context = Context {
            chain: delivery_chain.clone(),
            expect: Some((c.1.clone(), c.2)),
            extra: Box::new(Origin {
                packages: delivery_packages.clone(),
                gesture: Arc::new(Mutex::new(None)),
                token: 0,
            }),
            feed: None,
            // A consumer busy past the startup limit fails the job rather than hold it.
            turn_bounded: true,
            secrets: Vec::new(),
        };
        // A batch's delivery ends with the job; its end is delivered after.
        let cancel = if end {
            Cancel::default()
        } else {
            Cancel::default().with(&job.cancel)
        };
        programs
            .begin_call(None)
            .and_then(|call| {
                programs.run_call_with(
                    call,
                    &c.0,
                    &g.deliver.id,
                    envelope,
                    Arc::new(context),
                    cancel,
                )
            })
            .map(|_| ())
            .map_err(|mut e| {
                if e.message.contains("stayed busy") {
                    e.message.push_str(
                        ": a consumer must answer the call that started a job before its deliveries can arrive, and never wait for its own job inside a call",
                    );
                }
                e
            })
    };
    std::thread::scope(|s| {
        s.spawn(move || {
            let r = programs.begin_call(None).and_then(|registered| {
                programs.run_call_with(
                    registered,
                    &producer,
                    &op,
                    input,
                    Arc::new(call),
                    Cancel::default().with(&job.cancel),
                )
            });
            job.finished(r);
        });
        jobs::pump(job, &source, deliver, valid, consumer_valid);
    });
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

/// What a confirmation says of a call's secret fields.
fn secret_text(target: &str, filled: &[Filled]) -> String {
    if filled.is_empty() {
        return String::new();
    }
    let each: Vec<String> = filled
        .iter()
        .map(|f| {
            format!(
                "{} ({}) to {target}'s {}, {}",
                f.label,
                f.secret,
                f.field,
                if f.value {
                    "which receives the secret itself"
                } else {
                    "as a reference only"
                }
            )
        })
        .collect();
    format!(" Secrets: {}.", each.join("; "))
}

/// A read goes ahead, unless secret (a secret's delivery) asks. A write or launch needs the
/// person's native confirmation of the whole chain and the exact input, and of the secrets filled,
/// now; a launch the window made directly, with a native gesture, may use a remembered approval
/// of exactly that instead, the same secrets' IDs included (a launch's secrets are references,
/// never values). A call with a tool hop is prepared as an import (a secret's delivery
/// to a tool through another included): it always asks, and its dialog offers no Remember. A
/// cancel while the confirmation is open withdraws it. Whether it asked.
#[allow(clippy::too_many_arguments)]
fn authorize(
    app: &AppHandle,
    grant: &Grant,
    origin: &Origin,
    preparation: rules::Preparation,
    input: &Value,
    filled: &[Filled],
    secret: bool,
    cancel: &Cancel,
) -> Result<bool, CallError> {
    let effect = grant.op.effect.as_str();
    if effect == "read" && !secret {
        return Ok(false);
    }
    let executor = grant.target.package();
    let launch = if effect == "launch" {
        // Manifests refuse it too: a launch hands over no value, so a remembered launch approval
        // (bound to its secrets' IDs below) never stands for a value's delivery.
        if filled.iter().any(|f| f.value) {
            return Err(fail(
                "permission",
                "a launch's secret fields hold references only, never a secret's value",
            ));
        }
        let mut l = launch_of(input, &executor)?;
        // A remembered launch is bound to the secrets it uses too.
        l.secret_uses = filled
            .iter()
            .map(|f| confirmation::SecretUse {
                reference: f.secret.clone(),
                recipient: grant.target.id().into(),
                purpose: f.field.clone(),
            })
            .collect();
        Some(l)
    } else {
        None
    };
    let action = confirmation::Action {
        what: format!(
            "{} {}: {} ({}). Input: {}{}",
            grant.target.id(),
            grant.op.id,
            grant.op.description,
            effect,
            input,
            secret_text(grant.target.id(), filled)
        ),
        chain: origin.packages.clone(),
        executor,
        native_tool: true,
        files: vec![],
        launch,
    };
    let refuse = |e: String| fail("permission", e);
    let st = app.state::<App>();
    // A page's or an import's confirmation is queued with the page limits and never remembered.
    let page = preparation != rules::Preparation::Main;
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
    let id = if preparation == rules::Preparation::Page {
        let p = st
            .confirmations
            .lock()
            .unwrap()
            .prepare_page(action)
            .map_err(refuse)?;
        confirmation::page_start(app, p).map_err(refuse)?
    } else if preparation == rules::Preparation::Import {
        let mut c = st.confirmations.lock().unwrap();
        let p = c.prepare_import(action).map_err(refuse)?;
        let id = c.queue(p).map_err(refuse)?;
        drop(c);
        confirmation::request(app, id.clone()).map_err(refuse)?;
        id
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
