//! Cross-package imports and exports in this device's catalog (`../app-host/README.md`, "Calls
//! between packages"). A package declares the operations of other packages it may call through
//! the host (`imports`); a native tool declares which of its operations other packages may
//! import, under an export contract (`exports`), and each operation's effect class.
//!
//! A native tool is enabled only when its required imports are enabled tools exporting them under
//! a contract it accepts, and only when its imports do not lead back to it. A curated web package's
//! imports need the person's native consent, bound to its release and to each target's release;
//! an Unverified package never gets one. Each call is granted again here (`Catalog::main_grant`,
//! `native_grant`, `page_grant`) from the records as they are then: the caller's import, the
//! target's export and admitted record, and the consent for a web caller. The caller's identity
//! comes from the host, never from the call.

use super::*;

/// Imports a package may declare.
const MAX_IMPORTS: usize = 16;
/// An operation's effect classes: read needs no confirmation per call; write and launch do.
const EFFECTS: &[&str] = &["read", "write", "launch"];
/// The input fields a launch-class operation names its launch with.
pub const LAUNCH_FIELDS: &[&str] = &["executable", "argv", "cwd"];
/// Secret fields an operation may declare.
const MAX_SECRET_FIELDS: usize = 4;

/// An operation's effect when its manifest names none: it asks before each call.
pub(super) fn write_effect() -> String {
    "write".into()
}

/// An import, as a manifest declares it.
#[derive(Deserialize, Default, Clone)]
#[serde(default)]
pub(super) struct ImportSpec {
    package: String,
    contract: Vec<i64>,
    operations: Vec<String>,
    required: Option<bool>,
    secrets: Vec<String>,
}

/// A native tool's exports, as its manifest declares them.
#[derive(Deserialize, Default, Clone)]
#[serde(default)]
pub(super) struct ExportSpec {
    pub contract: Option<i64>,
    pub operations: Vec<String>,
}

/// An import, as the catalog keeps it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ImportDecl {
    /// The target package's ID.
    pub package: String,
    /// The target's export contracts this package works with.
    pub contract: Vec<i64>,
    /// The target's operations it calls.
    pub operations: Vec<String>,
    /// Required: the package is enabled only with the target enabled. Optional: a call while
    /// the target is missing fails as unavailable.
    pub required: bool,
    /// The operations it may pass secret references to (a native tool's imports only).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub secrets: Vec<String>,
}

/// A native tool's exports, as the catalog keeps them.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ExportDecl {
    pub contract: i64,
    pub operations: Vec<String>,
}

/// A curated web package's confirmed imports: what the person confirmed natively, for this
/// release of it, from this source and signer, and each target at its release then.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Consent {
    pub sha256: String,
    pub installed_at: u64,
    pub source: String,
    pub publisher: String,
    pub key_id: String,
    pub targets: Vec<ConsentTarget>,
    pub confirmed_at: u64,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ConsentTarget {
    pub id: String,
    pub sha256: String,
    pub installed_at: u64,
    pub contract: i64,
    pub operations: Vec<String>,
}

impl Consent {
    /// Whether it is the consent for this package's active release, source and signer.
    fn names(&self, p: &Package, s: &Signed) -> bool {
        self.sha256 == p.active.sha256
            && self.installed_at == p.active.installed_at
            && self.source == s.source
            && self.publisher == s.publisher
            && self.key_id == s.key_id
    }
}

/// A call the catalog allows now: the target's admitted record (its files are checked when the
/// program host starts it) and the operation.
#[derive(Clone, Debug)]
pub struct Grant {
    pub target: Admitted,
    pub op: OperationDecl,
    /// The caller may pass secret references in the operation's secret fields: a native tool
    /// whose import allows it.
    pub passes_secrets: bool,
}

/// Why a call is not allowed: `permission` (the caller may not make it) or
/// `extension_unavailable` (the target cannot take it now), and a message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Refused {
    pub code: &'static str,
    pub message: String,
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

pub(super) fn validate_imports(imports: Option<&[ImportSpec]>, web: bool) -> Result<(), String> {
    let Some(imports) = imports else {
        return Ok(());
    };
    if imports.len() > MAX_IMPORTS {
        return Err(format!(
            "app.imports lists more than {MAX_IMPORTS} packages"
        ));
    }
    let mut packages = BTreeSet::new();
    for i in imports {
        let mut ops = BTreeSet::new();
        if !valid_id(&i.package)
            || !packages.insert(i.package.as_str())
            || i.contract.is_empty()
            || i.contract.iter().any(|c| *c < 0)
            || i.operations.is_empty()
            || i.operations
                .iter()
                .any(|o| !valid_page_id(o) || !ops.insert(o.as_str()))
        {
            return Err(
                "app.imports require unique package IDs, nonnegative contracts and unique operation IDs"
                    .into(),
            );
        }
        let mut seen = BTreeSet::new();
        if i.secrets
            .iter()
            .any(|o| !ops.contains(o.as_str()) || !seen.insert(o.as_str()))
        {
            return Err(
                "app.imports secrets must name operations the import lists, once each".into(),
            );
        }
        if web && !i.secrets.is_empty() {
            return Err("a web package's imports cannot pass secrets".into());
        }
    }
    Ok(())
}

/// An operation's secret fields: at most `MAX_SECRET_FIELDS` string properties of its input,
/// each `value` or `reference`.
pub(super) fn validate_secrets(op: &Operation) -> Result<(), String> {
    let Some(secrets) = &op.secrets else {
        return Ok(());
    };
    if secrets.len() > MAX_SECRET_FIELDS {
        return Err(format!(
            "app.operations {}: at most {MAX_SECRET_FIELDS} secret fields",
            op.id
        ));
    }
    for (field, how) in secrets {
        if how != "value" && how != "reference" {
            return Err(format!(
                "app.operations {}: secret field {field} is value or reference",
                op.id
            ));
        }
        if op.input_schema["type"] != "object"
            || op.input_schema["properties"][field.as_str()]["type"] != "string"
        {
            return Err(format!(
                "app.operations {}: secret field {field} must be a string property of its input",
                op.id
            ));
        }
    }
    Ok(())
}

pub(super) fn validate_effect(op: &Operation) -> Result<(), String> {
    let effect = op.effect.as_deref().unwrap_or("write");
    if !EFFECTS.contains(&effect) {
        return Err(format!(
            "app.operations {}: effect must be read, write or launch",
            op.id
        ));
    }
    if effect == "launch" {
        let required: Vec<&str> = op.input_schema["required"]
            .as_array()
            .map(|r| r.iter().filter_map(Value::as_str).collect())
            .unwrap_or_default();
        if op.input_schema["type"] != "object"
            || LAUNCH_FIELDS.iter().any(|f| !required.contains(f))
        {
            return Err(format!(
                "app.operations {}: a launch operation's input must require executable, argv and cwd",
                op.id
            ));
        }
    }
    Ok(())
}

pub(super) fn validate_exports(e: Option<&ExportSpec>, ops: &[Operation]) -> Result<(), String> {
    let Some(e) = e else {
        return Ok(());
    };
    let mut seen = BTreeSet::new();
    if e.contract.is_none_or(|c| c < 0)
        || e.operations.is_empty()
        || e.operations
            .iter()
            .any(|o| !seen.insert(o.as_str()) || !ops.iter().any(|d| d.id == *o))
    {
        return Err(
            "app.exports require a nonnegative contract and unique declared operation IDs".into(),
        );
    }
    Ok(())
}

/// The imports of package id's app contribution, as the catalog keeps them.
pub(super) fn import_decls(id: &str, app: &App) -> Result<Vec<ImportDecl>, String> {
    let imports: Vec<ImportDecl> = app
        .imports
        .iter()
        .flatten()
        .map(|i| ImportDecl {
            package: i.package.clone(),
            contract: i.contract.clone(),
            operations: i.operations.clone(),
            required: i.required.unwrap_or(true),
            secrets: i.secrets.clone(),
        })
        .collect();
    if imports.iter().any(|i| i.package == id) {
        return Err(format!("{id} imports itself"));
    }
    Ok(imports)
}

/// The imports, for a native confirmation: empty when there are none.
pub(super) fn listing(imports: &[ImportDecl]) -> String {
    if imports.is_empty() {
        return String::new();
    }
    let each: Vec<String> = imports
        .iter()
        .map(|i| {
            format!(
                "{} (export contract {:?}, {}): {}{}",
                i.package,
                i.contract,
                if i.required { "required" } else { "optional" },
                i.operations.join(", "),
                if i.secrets.is_empty() {
                    String::new()
                } else {
                    format!(", passing secret references to {}", i.secrets.join(", "))
                }
            )
        })
        .collect();
    format!(" Imports: {}.", each.join("; "))
}

impl Catalog {
    /// The admitted record of import i's target, when it exports every operation i names under
    /// a contract i accepts.
    fn importable(&self, i: &ImportDecl, roots: &dyn Roots) -> Result<Admitted, Refused> {
        let (a, _) = self
            .admitted_record(&i.package, roots)
            .map_err(unavailable)?;
        let e = a
            .exports
            .as_ref()
            .ok_or_else(|| forbidden(format!("{} exports no operations", i.package)))?;
        if !i.contract.contains(&e.contract) {
            return Err(forbidden(format!(
                "{} exports contract {}, not one of {:?}",
                i.package, e.contract, i.contract
            )));
        }
        if let Some(op) = i.operations.iter().find(|o| !e.operations.contains(o)) {
            return Err(forbidden(format!("{} does not export {op}", i.package)));
        }
        Ok(a)
    }

    /// A path from package id through the enabled packages' imports (required or optional)
    /// back to id, when its imports make one.
    fn cycle(&self, id: &str, imports: &[ImportDecl]) -> Option<Vec<String>> {
        let mut seen = BTreeSet::new();
        let mut stack: Vec<Vec<String>> = imports
            .iter()
            .map(|i| vec![id.to_string(), i.package.clone()])
            .collect();
        while let Some(path) = stack.pop() {
            let last = path.last().expect("a path has its start").clone();
            if last == id {
                return Some(path);
            }
            if !seen.insert(last.clone()) {
                continue;
            }
            if let Some(p) = self.packages.get(&last).filter(|p| p.enabled) {
                for i in &p.active.imports {
                    let mut next = path.clone();
                    next.push(i.package.clone());
                    stack.push(next);
                }
            }
        }
        None
    }

    /// Package id's imports against the tools enabled now: each one's target when it can be
    /// called. A required import that cannot be, or imports leading back to id, refuse it.
    pub(super) fn dependencies(
        &self,
        id: &str,
        imports: &[ImportDecl],
        roots: &dyn Roots,
    ) -> Result<Vec<(ImportDecl, Option<Admitted>)>, String> {
        if let Some(path) = self.cycle(id, imports) {
            return Err(format!(
                "{id}'s imports make a cycle: {}",
                path.join(" imports ")
            ));
        }
        let mut out = Vec::new();
        for i in imports {
            match self.importable(i, roots) {
                Ok(a) => out.push((i.clone(), Some(a))),
                Err(e) if i.required => {
                    return Err(format!(
                        "{id} requires {}'s {}: {}",
                        i.package,
                        i.operations.join(", "),
                        e.message
                    ))
                }
                Err(_) => out.push((i.clone(), None)),
            }
        }
        Ok(out)
    }

    /// What enabling curated web package id's imports would record: refused for a package that
    /// is not curated, for one with no imports, and while a required import cannot be called.
    pub fn consent_candidate(&self, id: &str, roots: &dyn Roots) -> Result<Consent, String> {
        if self.safe_mode {
            return Err("the app runs in safe mode: no native import is allowed".into());
        }
        let p = self
            .packages
            .get(id)
            .ok_or_else(|| format!("{id} is not installed on this device"))?;
        if p.active.tier != "web" {
            return Err(format!(
                "{id} is a native tool: its own enable confirmation covers its imports"
            ));
        }
        if !p.enabled {
            return Err(format!("{id} is disabled"));
        }
        let s = p.curated_signature(id, roots).map_err(|_| {
            format!(
                "{id} is Unverified: only curated first-party web packages may use native imports"
            )
        })?;
        if p.active.imports.is_empty() {
            return Err(format!("{id} declares no native imports"));
        }
        let targets = self
            .dependencies(id, &p.active.imports, roots)?
            .into_iter()
            .filter_map(|(i, a)| {
                let a = a?;
                Some(ConsentTarget {
                    id: a.id.clone(),
                    sha256: a.sha256.clone(),
                    installed_at: a.installed_at,
                    contract: a.exports.as_ref().map_or(0, |e| e.contract),
                    operations: i.operations,
                })
            })
            .collect();
        Ok(Consent {
            sha256: p.active.sha256.clone(),
            installed_at: p.active.installed_at,
            source: s.source.clone(),
            publisher: s.publisher.clone(),
            key_id: s.key_id.clone(),
            targets,
            confirmed_at: 0,
        })
    }

    /// The native confirmation for consent c to web package id's imports: the package (source,
    /// digest, signer) and each target operation with its description and effect class.
    #[cfg(target_os = "linux")]
    pub fn consent_confirmation(
        &self,
        id: &str,
        c: &Consent,
        roots: &dyn Roots,
    ) -> Result<crate::confirmation::Action, String> {
        let p = self
            .packages
            .get(id)
            .ok_or_else(|| format!("{id} is not installed on this device"))?;
        let mut lines = Vec::new();
        for t in &c.targets {
            let (a, _) = self.admitted_record(&t.id, roots)?;
            for op in &t.operations {
                let d = a.operations.iter().find(|o| o.id == *op);
                lines.push(format!(
                    "{} {} {op}: {} ({})",
                    t.id,
                    a.version,
                    d.map_or("", |d| d.description.as_str()),
                    d.map_or("write", |d| d.effect.as_str())
                ));
            }
        }
        Ok(crate::confirmation::Action {
            what: format!(
                "Allow web package {id} {} to call these operations of native tools, which have full access to your user account. Each read is answered without asking again; each write and launch asks. Operations: {}",
                p.active.version,
                lines.join("; ")
            ),
            chain: vec![admission::main_window()],
            executor: crate::confirmation::Package {
                id: id.into(),
                source: format!("registry {} ({})", p.source.name, c.source),
                digest: c.sha256.clone(),
                signer: format!(
                    "{} key {}",
                    c.publisher, c.key_id
                ),
                contract: format!("app host {HOST_CONTRACT}; app {}", p.active.app_version),
                generation: c.installed_at,
            },
            native_tool: false,
            files: vec![],
            launch: None,
        })
    }

    /// Records consent c, which the person confirmed, when it is still what
    /// `consent_candidate` gives.
    pub fn allow_imports(
        &mut self,
        id: &str,
        c: &Consent,
        roots: &dyn Roots,
    ) -> Result<(), String> {
        if self.consent_candidate(id, roots)? != *c {
            return Err(format!(
                "{id} or a package it imports changed while it was being confirmed; allow its imports again"
            ));
        }
        let p = self.packages.get_mut(id).expect("a candidate is installed");
        let was = p.consent.replace(Consent {
            confirmed_at: now(),
            ..c.clone()
        });
        if let Err(e) = self.save() {
            if let Some(p) = self.packages.get_mut(id) {
                p.consent = was;
            }
            return Err(e);
        }
        Ok(())
    }

    /// Ends web package id's import consent.
    pub fn revoke_imports(&mut self, id: &str) -> Result<(), String> {
        let p = self
            .packages
            .get_mut(id)
            .ok_or_else(|| format!("{id} is not installed on this device"))?;
        let was = p.consent.take();
        if let Err(e) = self.save() {
            if let Some(p) = self.packages.get_mut(id) {
                p.consent = was;
            }
            return Err(e);
        }
        Ok(())
    }

    /// Whether web package id has import consent for its active release.
    /// Drops every consent that names target: its admission was cleared or replaced (disabled,
    /// updated, rolled back, removed or swapped), and enabling it again is a new native decision.
    /// Called before saving; a save that fails leaves them dropped, so the person allows them
    /// again (it fails closed).
    pub(super) fn drop_consents_naming(&mut self, target: &str) {
        for p in self.packages.values_mut() {
            if p.consent
                .as_ref()
                .is_some_and(|c| c.targets.iter().any(|t| t.id == target))
            {
                p.consent = None;
            }
        }
    }

    pub fn imports_allowed(&self, id: &str, roots: &dyn Roots) -> bool {
        self.packages.get(id).is_some_and(|p| {
            p.consent
                .as_ref()
                .is_some_and(|c| p.curated_signature(id, roots).is_ok_and(|s| c.names(p, s)))
        })
    }

    /// A call from the window of tool target's operation op: any operation it declares.
    pub fn main_grant(&self, target: &str, op: &str, roots: &dyn Roots) -> Result<Grant, Refused> {
        let (a, _) = self.admitted_record(target, roots).map_err(unavailable)?;
        let op = a
            .operations
            .iter()
            .find(|o| o.id == op)
            .cloned()
            .ok_or_else(|| forbidden(format!("{target} declares no operation {op}")))?;
        Ok(Grant {
            target: a,
            op,
            passes_secrets: false,
        })
    }

    /// A call by native tool caller, running its release sha256 of generation, of target's op:
    /// one it imports and target exports, while caller is still that release. Gives the caller's
    /// record too.
    pub fn native_grant(
        &self,
        (caller, sha256, generation): (&str, &str, u64),
        target: &str,
        op: &str,
        roots: &dyn Roots,
    ) -> Result<(Grant, Admitted), Refused> {
        let (c, _) = self.admitted_record(caller, roots).map_err(forbidden)?;
        if c.sha256 != sha256 || c.installed_at != generation {
            return Err(forbidden(format!(
                "{caller} changed since this run of it started: its calls are refused"
            )));
        }
        let i = c
            .imports
            .iter()
            .find(|i| i.package == target && i.operations.iter().any(|o| o == op))
            .ok_or_else(|| forbidden(format!("{caller} does not import {target}'s {op}")))?;
        let a = self.importable(i, roots)?;
        let op = a
            .operations
            .iter()
            .find(|o| o.id == op)
            .cloned()
            .ok_or_else(|| forbidden(format!("{target} declares no operation {op}")))?;
        let passes_secrets = i.secrets.contains(&op.id);
        Ok((
            Grant {
                target: a,
                op,
                passes_secrets,
            },
            c,
        ))
    }

    /// Package id as native confirmations name a page's requester: a native tool by its admitted
    /// record, a web package by its source, digest and signer (or as Unverified).
    #[cfg(target_os = "linux")]
    pub fn requester(&self, id: &str, roots: &dyn Roots) -> Option<crate::confirmation::Package> {
        let p = self.packages.get(id)?;
        if p.active.tier == "native" {
            return self
                .admitted_record(id, roots)
                .ok()
                .map(|(a, _)| a.package());
        }
        let signer = p.curated_signature(id, roots).map_or_else(
            |_| "Unverified".to_string(),
            |s| format!("{} key {} ({})", s.publisher, s.key_id, s.public_key),
        );
        Some(crate::confirmation::Package {
            id: id.into(),
            source: match &p.source.index {
                Some(index) => format!("registry {} ({index})", p.source.name),
                None => format!("local file {}", p.source.name),
            },
            digest: p.active.sha256.clone(),
            signer,
            contract: format!("app host {HOST_CONTRACT}; app {}", p.active.app_version),
            generation: p.active.installed_at,
        })
    }

    /// A call from a page of package caller, opened on its release sha256, of target's op. A
    /// native tool's page may call its own operations. A web package's page may call what it
    /// imports, only when it is curated and its consent covers caller's release and target's.
    pub fn page_grant(
        &self,
        (caller, sha256): (&str, &str),
        target: &str,
        op: &str,
        roots: &dyn Roots,
    ) -> Result<Grant, Refused> {
        if self.safe_mode {
            return Err(unavailable("the app runs in safe mode".into()));
        }
        let p = self
            .packages
            .get(caller)
            .filter(|p| p.enabled)
            .ok_or_else(|| forbidden(format!("{caller} is not enabled")))?;
        if p.active.sha256 != sha256 {
            return Err(forbidden(format!(
                "this page belongs to a release of {caller} that is no longer active"
            )));
        }
        if p.active.tier == "native" {
            if target != caller {
                return Err(forbidden(format!(
                    "a native tool's page calls only its own tool's operations, not {target}'s"
                )));
            }
            return self.main_grant(target, op, roots);
        }
        let s = p.curated_signature(caller, roots).map_err(|_| {
            forbidden(format!(
                "{caller} is Unverified: only curated first-party web packages may use native imports"
            ))
        })?;
        let i = p
            .active
            .imports
            .iter()
            .find(|i| i.package == target && i.operations.iter().any(|o| o == op))
            .ok_or_else(|| forbidden(format!("{caller} does not import {target}'s {op}")))?;
        let t = p
            .consent
            .as_ref()
            .filter(|c| c.names(p, s))
            .and_then(|c| {
                c.targets
                    .iter()
                    .find(|t| t.id == target && t.operations.iter().any(|o| o == op))
            })
            .ok_or_else(|| {
                forbidden(format!(
                    "{caller}'s native imports are not allowed; allow them on the Device extensions page"
                ))
            })?;
        let a = self.importable(i, roots)?;
        if a.sha256 != t.sha256 || a.installed_at != t.installed_at {
            return Err(forbidden(format!(
                "{target} changed since {caller}'s imports were allowed; allow them again"
            )));
        }
        let op = a
            .operations
            .iter()
            .find(|o| o.id == op)
            .cloned()
            .ok_or_else(|| forbidden(format!("{target} declares no operation {op}")))?;
        Ok(Grant {
            target: a,
            op,
            passes_secrets: false,
        })
    }
}
