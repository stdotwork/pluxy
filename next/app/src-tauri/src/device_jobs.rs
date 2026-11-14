//! Declared jobs in this device's catalog (`../app-host/README.md`, "Jobs"). A native tool
//! registers, in its manifest, the kinds of data it produces (`produces`: a namespaced kind, the
//! schema version it produces it at and the operation that runs a job of it), and the kinds it
//! consumes (`consumes`: a kind in its own namespace, the schema versions it accepts and the
//! operation the host delivers batches to). A registration is never something a tool says: it is
//! read here, at each request, from the manifest of an enabled, admitted tool's active release,
//! with the package, version, digest, generation, source and signer the catalog admitted it with.
//! Disabling, updating, rolling back, swapping or removing the tool ends its registration.
//!
//! A consumer lists the registrations of a kind it consumes (`Catalog::registrations`) and starts
//! a job naming one exactly (`Catalog::job_grant`), which the router decides as any call. Its
//! deliver operation takes only the host's deliveries: the window, pages and other tools are
//! refused it (`host_only`), and no manifest may export it. Discovery adds no import: a consumer
//! does not depend on its producers.

use super::*;

/// Kinds a package may produce, and consume.
const MAX_KINDS: usize = 8;

/// A kind a manifest says it produces.
#[derive(Deserialize, Default, Clone)]
#[serde(default)]
pub(super) struct ProduceSpec {
    kind: String,
    schema: Option<i64>,
    operation: String,
}

/// A kind a manifest says it consumes.
#[derive(Deserialize, Default, Clone)]
#[serde(default)]
pub(super) struct ConsumeSpec {
    kind: String,
    schema: Vec<i64>,
    deliver: String,
}

/// A kind a native tool produces, as the catalog keeps it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ProduceDecl {
    /// `<package ID>/<stream>`: the consuming package's namespace.
    pub kind: String,
    /// The schema version of what it produces.
    pub schema: i64,
    /// The operation a job of it runs.
    pub operation: String,
}

/// A kind a native tool consumes, as the catalog keeps it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ConsumeDecl {
    /// `<its own ID>/<stream>`.
    pub kind: String,
    /// The schema versions it accepts.
    pub schema: Vec<i64>,
    /// The operation the host delivers each batch and the end of a job to.
    pub deliver: String,
}

/// A registration as the host gives it to a consumer: what the catalog admitted the producer
/// as, never what it says of itself.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct Registration {
    pub package: String,
    pub version: String,
    pub sha256: String,
    /// The release's install time.
    pub generation: u64,
    /// The schema version it produces the kind at.
    pub schema: i64,
    /// The registry index it was installed from: the trust domain of its signature.
    pub source: String,
    pub signer: RegistrationSigner,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct RegistrationSigner {
    pub publisher: String,
    pub key_id: String,
}

impl Registration {
    fn of(a: &Admitted, p: &ProduceDecl) -> Registration {
        Registration {
            package: a.id.clone(),
            version: a.version.clone(),
            sha256: a.sha256.clone(),
            generation: a.installed_at,
            schema: p.schema,
            source: a.signed.source.clone(),
            signer: RegistrationSigner {
                publisher: a.signed.publisher.clone(),
                key_id: a.signed.key_id.clone(),
            },
        }
    }
}

/// A job the catalog allows now: the producer's operation (a call's grant), the consumer and
/// its deliver operation, and the registration the job is of.
#[derive(Clone, Debug)]
pub struct JobGrant {
    pub producer: Grant,
    pub consumer: Admitted,
    pub deliver: OperationDecl,
    pub registration: Registration,
}

fn forbidden(message: String) -> Refused {
    Refused {
        code: "permission",
        message,
    }
}

fn unavailable(message: String) -> Refused {
    Refused {
        code: "extension_unavailable",
        message,
    }
}

/// Whether kind is `<package ID>/<stream>`, giving the package ID.
fn namespace(kind: &str) -> Option<&str> {
    let (ns, stream) = kind.rsplit_once('/')?;
    (valid_id(ns) && valid_page_id(stream)).then_some(ns)
}

/// Checks a native app's `produces` and `consumes` against its operations and exports.
pub(super) fn validate(
    produces: Option<&[ProduceSpec]>,
    consumes: Option<&[ConsumeSpec]>,
    ops: &[Operation],
    exports: Option<&imports::ExportSpec>,
) -> Result<(), String> {
    let declared = |id: &str| ops.iter().any(|o| o.id == id);
    let mut kinds = BTreeSet::new();
    let produces = produces.unwrap_or_default();
    if produces.len() > MAX_KINDS {
        return Err(format!("app.produces lists more than {MAX_KINDS} kinds"));
    }
    for p in produces {
        if namespace(&p.kind).is_none() || p.schema.is_none_or(|s| s < 0) || !declared(&p.operation)
        {
            return Err(
                "app.produces require a kind <package ID>/<stream>, a nonnegative schema and a declared operation"
                    .into(),
            );
        }
        if !kinds.insert(p.kind.as_str()) {
            return Err(format!("app.produces names kind {} more than once", p.kind));
        }
    }
    let mut kinds = BTreeSet::new();
    let consumes = consumes.unwrap_or_default();
    if consumes.len() > MAX_KINDS {
        return Err(format!("app.consumes lists more than {MAX_KINDS} kinds"));
    }
    for c in consumes {
        if namespace(&c.kind).is_none()
            || c.schema.is_empty()
            || c.schema.iter().any(|s| *s < 0)
            || !declared(&c.deliver)
        {
            return Err(
                "app.consumes require a kind <package ID>/<stream>, nonnegative schemas and a declared deliver operation"
                    .into(),
            );
        }
        if !kinds.insert(c.kind.as_str()) {
            return Err(format!("app.consumes names kind {} more than once", c.kind));
        }
        if produces.iter().any(|p| p.operation == c.deliver) {
            return Err(format!(
                "app.produces: operation {} is also a deliver operation, which takes only the host's deliveries",
                c.deliver
            ));
        }
        if exports.is_some_and(|e| e.operations.contains(&c.deliver)) {
            return Err(format!(
                "app.consumes: deliver operation {} takes only the host's deliveries and cannot be exported",
                c.deliver
            ));
        }
    }
    Ok(())
}

/// The kinds package id's app contribution produces and consumes, as the catalog keeps them: a
/// kind it consumes must be in its own namespace.
pub(super) fn decls(id: &str, app: &App) -> Result<(Vec<ProduceDecl>, Vec<ConsumeDecl>), String> {
    let produces = app
        .produces
        .iter()
        .flatten()
        .map(|p| ProduceDecl {
            kind: p.kind.clone(),
            schema: p.schema.unwrap_or_default(),
            operation: p.operation.clone(),
        })
        .collect();
    let consumes: Vec<ConsumeDecl> = app
        .consumes
        .iter()
        .flatten()
        .map(|c| ConsumeDecl {
            kind: c.kind.clone(),
            schema: c.schema.clone(),
            deliver: c.deliver.clone(),
        })
        .collect();
    if let Some(c) = consumes.iter().find(|c| namespace(&c.kind) != Some(id)) {
        return Err(format!(
            "app.consumes kind {} is not in its own namespace ({id}/...)",
            c.kind
        ));
    }
    Ok((produces, consumes))
}

/// The kinds, for a native confirmation: empty when there are none.
pub(super) fn listing(produces: &[ProduceDecl], consumes: &[ConsumeDecl]) -> String {
    let mut out = String::new();
    if !produces.is_empty() {
        let each: Vec<String> = produces
            .iter()
            .map(|p| format!("{} (schema {}, by {})", p.kind, p.schema, p.operation))
            .collect();
        out.push_str(&format!(" Produces: {}.", each.join("; ")));
    }
    if !consumes.is_empty() {
        let each: Vec<String> = consumes
            .iter()
            .map(|c| format!("{} (schemas {:?}, into {})", c.kind, c.schema, c.deliver))
            .collect();
        out.push_str(&format!(" Consumes: {}.", each.join("; ")));
    }
    out
}

/// Whether op of tool a takes only the host's deliveries.
pub(super) fn host_only(a: &Admitted, op: &str) -> Result<(), Refused> {
    if a.consumes.iter().any(|c| c.deliver == op) {
        return Err(forbidden(format!(
            "{}'s {op} takes only the host's job deliveries",
            a.id
        )));
    }
    Ok(())
}

impl Catalog {
    /// Native tool caller, running its release sha256 of generation, as a consumer of kind:
    /// its record and what it declares for kind.
    pub fn consumer(
        &self,
        (caller, sha256, generation): (&str, &str, u64),
        kind: &str,
        roots: &dyn Roots,
    ) -> Result<(Admitted, ConsumeDecl), Refused> {
        let (c, _) = self.admitted_record(caller, roots).map_err(forbidden)?;
        if c.sha256 != sha256 || c.installed_at != generation {
            return Err(forbidden(format!(
                "{caller} changed since this run of it started: its requests are refused"
            )));
        }
        let d = c
            .consumes
            .iter()
            .find(|d| d.kind == kind)
            .cloned()
            .ok_or_else(|| forbidden(format!("{caller} does not consume {kind:?}")))?;
        Ok((c, d))
    }

    /// Whether package from reaches package to through the enabled packages' imports.
    fn reaches(&self, from: &str, to: &str) -> bool {
        let mut seen = BTreeSet::new();
        let mut stack = vec![from.to_string()];
        while let Some(id) = stack.pop() {
            if !seen.insert(id.clone()) {
                continue;
            }
            if let Some(p) = self.packages.get(&id).filter(|p| p.enabled) {
                for i in &p.active.imports {
                    if i.package == to {
                        return true;
                    }
                    stack.push(i.package.clone());
                }
            }
        }
        false
    }

    /// The registrations of kind that consumer caller accepts: each enabled, admitted native
    /// tool, other than caller, whose active release produces kind at a schema caller accepts.
    pub fn registrations(
        &self,
        caller: (&str, &str, u64),
        kind: &str,
        roots: &dyn Roots,
    ) -> Result<Vec<Registration>, Refused> {
        let (_, d) = self.consumer(caller, kind, roots)?;
        let mut out = Vec::new();
        for (id, p) in &self.packages {
            if id == caller.0 || !p.enabled || p.active.tier != "native" {
                continue;
            }
            let Some(decl) = p
                .active
                .produces
                .iter()
                .find(|x| x.kind == kind && d.schema.contains(&x.schema))
            else {
                continue;
            };
            if let Ok((a, _)) = self.admitted_record(id, roots) {
                out.push(Registration::of(&a, decl));
            }
        }
        Ok(out)
    }

    /// A job of kind that consumer caller starts, of the registration it names: producer at
    /// exactly release sha256 of generation, still admitted, producing kind at a schema caller
    /// accepts. Refused when caller reaches the producer through imports: a job of it could then
    /// wait on its own consumer.
    pub fn job_grant(
        &self,
        caller: (&str, &str, u64),
        kind: &str,
        (producer, sha256, generation): (&str, &str, u64),
        roots: &dyn Roots,
    ) -> Result<JobGrant, Refused> {
        let (consumer, d) = self.consumer(caller, kind, roots)?;
        let deliver = consumer
            .operations
            .iter()
            .find(|o| o.id == d.deliver)
            .cloned()
            .ok_or_else(|| {
                forbidden(format!("{} declares no operation {}", caller.0, d.deliver))
            })?;
        let (a, _) = self.admitted_record(producer, roots).map_err(unavailable)?;
        if a.sha256 != sha256 || a.installed_at != generation {
            return Err(forbidden(format!(
                "{producer} is not the release that was listed (it changed, or another was named): list the registrations again"
            )));
        }
        let p = a
            .produces
            .iter()
            .find(|p| p.kind == kind)
            .cloned()
            .ok_or_else(|| forbidden(format!("{producer} does not produce {kind}")))?;
        if !d.schema.contains(&p.schema) {
            return Err(forbidden(format!(
                "{producer} produces {kind} at schema {}, not one of {:?}",
                p.schema, d.schema
            )));
        }
        if self.reaches(caller.0, producer) {
            return Err(forbidden(format!(
                "{} imports {producer}, directly or through other tools: a job of it could wait on its own consumer",
                caller.0
            )));
        }
        // A deliver operation is the host's alone, even named as a job's.
        host_only(&a, &p.operation)?;
        let op = a
            .operations
            .iter()
            .find(|o| o.id == p.operation)
            .cloned()
            .ok_or_else(|| {
                forbidden(format!("{producer} declares no operation {}", p.operation))
            })?;
        Ok(JobGrant {
            registration: Registration::of(&a, &p),
            producer: Grant { target: a, op },
            consumer,
            deliver,
        })
    }
}
