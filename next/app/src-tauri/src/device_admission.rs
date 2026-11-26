//! Signed native-tier admission for this device's catalog (`../app-host/README.md`). A native
//! tool installs only from a curated registry (one a trust root names by its index), with a
//! descriptor signed by that root's first-party key and verified (`artifact.rs`) before its
//! archive is unpacked. It installs disabled. Enabling it takes a native confirmation that says
//! it has full access to your user account; the confirmed admission is bound to the active
//! release's digest, its source, its signer and key. A new release, a rollback, disabling or
//! removing it ends the admission.
//!
//! `Catalog::admitted` is the record the program host takes: it checks the admission, the root
//! and signature again, and every file of the installed package, each time. Nothing here starts
//! a process.

use super::*;
use crate::artifact::{self, PublisherKeys, Tier, VerifiedDescriptor};

/// Native-tier trust, as admission needs it (`trust::TrustStore`).
pub trait Roots: PublisherKeys {
    /// Whether any native-tier root is configured.
    fn configured(&self) -> bool;
    /// Whether a root names source, a registry's index, as a curated source.
    fn curated(&self, source: &str) -> bool;
    /// Whether a root names publisher: then only a curated source may use it.
    fn reserved(&self, publisher: &str) -> bool;
}

impl Roots for crate::trust::TrustStore {
    fn configured(&self) -> bool {
        self.require_native().is_ok()
    }
    fn curated(&self, source: &str) -> bool {
        self.curated_source(source)
    }
    fn reserved(&self, publisher: &str) -> bool {
        self.root_publisher(publisher)
    }
}

/// A release's publisher signature: one type for a curated root's and for one Access's
/// approved signers use (`access.rs`).
pub use crate::access::Signed;

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct OperationDecl {
    pub id: String,
    pub description: String,
    pub input_schema: Value,
    pub output_schema: Value,
    /// `read`, `write`, `launch` or `disclose` (`imports::EFFECTS`); absent in a manifest: `write`.
    #[serde(default = "imports::write_effect")]
    pub effect: String,
    /// Input fields that carry a secret, and how: `value` (the host delivers the secret to the
    /// tool) or `reference` (the tool gets only a reference to it).
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub secrets: BTreeMap<String, String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct StateDecl {
    pub version: i64,
    pub readable_versions: Vec<i64>,
}

/// What the person confirmed in the native confirmation: this release, from this source,
/// signed with this key, for this host contract.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Admission {
    pub sha256: String,
    pub installed_at: u64,
    pub source: String,
    pub publisher: String,
    pub key_id: String,
    /// The root's public key at confirmation, in lower-case hex.
    pub public_key: String,
    pub host_contract: i64,
    pub confirmed_at: u64,
}

impl Admission {
    /// The admission of a, confirmed now.
    pub(super) fn of(a: &Admitted) -> Admission {
        Admission {
            sha256: a.sha256.clone(),
            installed_at: a.installed_at,
            source: a.signed.source.clone(),
            publisher: a.signed.publisher.clone(),
            key_id: a.signed.key_id.clone(),
            public_key: a.public_key().into(),
            host_contract: a.host_contract,
            confirmed_at: super::now(),
        }
    }

    fn names(&self, r: &Release) -> bool {
        self.sha256 == r.sha256
            && self.installed_at == r.installed_at
            && r.signed.as_ref().is_some_and(|s| {
                s.source == self.source && s.publisher == self.publisher && s.key_id == self.key_id
            })
    }
}

impl Package {
    /// The active release's curated signature, when it verifies against roots now (the root
    /// still has its key), from the source it was signed for, and names that release.
    pub(super) fn curated_signature(&self, id: &str, roots: &dyn Roots) -> Result<&Signed, String> {
        self.curated_signature_of(id, &self.active, roots)
    }

    /// `curated_signature`, for release r of this package (one being swapped in).
    pub(super) fn curated_signature_of<'a>(
        &self,
        id: &str,
        r: &'a Release,
        roots: &dyn Roots,
    ) -> Result<&'a Signed, String> {
        self.curated_signature_for(id, r, roots, &platform())
    }

    /// `curated_signature_of`, for release r's archive for platform (a Linux build for WSL).
    pub(super) fn curated_signature_for<'a>(
        &self,
        id: &str,
        r: &'a Release,
        roots: &dyn Roots,
        platform: &str,
    ) -> Result<&'a Signed, String> {
        let s = r
            .signed
            .as_ref()
            .ok_or_else(|| format!("{id} {} has no curated signature", r.version))?;
        if self.source.kind != "registry" || self.source.index.as_deref() != Some(s.source.as_str())
        {
            return Err(format!(
                "{id} is not installed from the curated source it was signed for"
            ));
        }
        let key = roots
            .publisher_key(&s.source, &s.key_id, &s.publisher)
            .map_err(|e| format!("{id}: {e}"))?;
        if hex(key.as_bytes()) != s.public_key {
            return Err(format!(
                "{id}: its root's key for {} is not the key it was signed with",
                s.key_id
            ));
        }
        let sig = artifact::decode_hex::<64>(&s.signature)?;
        let v = artifact::verify_descriptor(&key, s.descriptor.as_bytes(), &sig, platform)
            .map_err(|e| format!("{id}: {e}"))?;
        let d = v.descriptor();
        if d.id != id
            || d.version != r.version
            || d.tier != tier(&r.tier)
            || d.size != r.size
            || d.sha256 != r.sha256
        {
            return Err(format!(
                "{id}: its signed descriptor does not name the installed release"
            ));
        }
        Ok(s)
    }

    /// A native tool the person confirmed for its active release, while it stays enabled.
    pub fn authorized(&self) -> bool {
        self.enabled
            && self
                .admission
                .as_ref()
                .is_some_and(|a| a.names(&self.active))
    }
}

/// An admitted native tool, checked: the only record the program host may start a program
/// from (`Catalog::admitted`, called again before each start), and what the native confirmation
/// enabling it shows (`native_candidate_unlocked`). It holds no signer logic for the host to redo.
/// Only `device` builds or changes one; everyone else reads it through the methods below.
#[derive(Clone, Debug, PartialEq)]
pub struct Admitted {
    pub(super) id: String,
    pub(super) version: String,
    pub(super) sha256: String,
    pub(super) size: u64,
    pub(super) installed_at: u64,
    pub(super) source: Source,
    pub(super) signed: Signed,
    pub(super) dir: PathBuf,
    pub(super) program: PathBuf,
    pub(super) program_sha256: String,
    pub(super) operations: Vec<OperationDecl>,
    pub(super) state: StateDecl,
    pub(super) app_version: i64,
    pub(super) host_contract: i64,
    pub(super) imports: Vec<ImportDecl>,
    pub(super) exports: Option<ExportDecl>,
    pub(super) produces: Vec<ProduceDecl>,
    pub(super) consumes: Vec<ConsumeDecl>,
}

impl Admitted {
    /// Package ID, `publisher/name`.
    pub fn id(&self) -> &str {
        &self.id
    }
    /// The active release's version.
    pub fn version(&self) -> &str {
        &self.version
    }
    /// SHA-256 of the release archive, in lower-case hex; also its store folder's name.
    pub fn sha256(&self) -> &str {
        &self.sha256
    }
    /// The archive's byte count.
    pub fn size(&self) -> u64 {
        self.size
    }
    /// When this release was installed (Unix seconds): its generation.
    pub fn installed_at(&self) -> u64 {
        self.installed_at
    }
    /// The registry it was installed from.
    pub fn source(&self) -> &Source {
        &self.source
    }
    /// The curated signature: publisher, key ID, source and the exact signed bytes.
    pub fn signed(&self) -> &Signed {
        &self.signed
    }
    /// The root's public key, in lower-case hex (also `signed().public_key`).
    pub fn public_key(&self) -> &str {
        &self.signed.public_key
    }
    /// The release's immutable store folder.
    pub fn dir(&self) -> &Path {
        &self.dir
    }
    /// The program for this computer, inside dir.
    pub fn program(&self) -> &Path {
        &self.program
    }
    /// The SHA-256 the program had at install, in lower-case hex: check the opened file against
    /// it before running it, since the path can change after this record was checked.
    pub fn program_sha256(&self) -> &str {
        &self.program_sha256
    }
    /// The operations the package declares.
    pub fn operations(&self) -> &[OperationDecl] {
        &self.operations
    }
    /// The state versions it writes and reads.
    pub fn state(&self) -> &StateDecl {
        &self.state
    }
    /// The app contribution's format version (`app.version`).
    pub fn app_version(&self) -> i64 {
        self.app_version
    }
    /// The app host contract it was admitted for.
    pub fn host_contract(&self) -> i64 {
        self.host_contract
    }
    /// The other packages' operations it may call through the host.
    pub fn imports(&self) -> &[ImportDecl] {
        &self.imports
    }
    /// The operations other packages may import, and their contract.
    pub fn exports(&self) -> Option<&ExportDecl> {
        self.exports.as_ref()
    }
    /// It, as native confirmations name a package: ID, source, digest, signer and key,
    /// contracts and generation.
    #[cfg(native_stack)]
    pub fn package(&self) -> crate::confirmation::Package {
        crate::confirmation::Package {
            id: self.id.clone(),
            source: format!("registry {} ({})", self.source.name, self.signed.source),
            digest: self.sha256.clone(),
            signer: format!(
                "{} key {} ({})",
                self.signed.publisher, self.signed.key_id, self.signed.public_key
            ),
            contract: format!(
                "app host {}; app {}; state {}, reads {:?}{}",
                self.host_contract,
                self.app_version,
                self.state.version,
                self.state.readable_versions,
                self.exports
                    .as_ref()
                    .map(|e| format!("; exports contract {}", e.contract))
                    .unwrap_or_default()
            ),
            generation: self.installed_at,
        }
    }
    /// The native confirmation enabling it shows: the main window as the requester; the tool,
    /// as the executor, with its ID, source, digest, signer and key, contracts, operations and
    /// program; and that it has full access to your user account.
    #[cfg(native_stack)]
    pub fn confirmation(&self) -> crate::confirmation::Action {
        let package = self.package();
        let operations: Vec<String> = self
            .operations
            .iter()
            .map(|o| {
                format!(
                    "{} ({}, {})",
                    o.id,
                    o.description,
                    imports::effect_label(&o.effect)
                )
            })
            .collect();
        crate::confirmation::Action {
            what: format!(
                "Enable native tool {} {}. It has full access to your user account. Operations: {}.{}",
                self.id,
                self.version,
                operations.join(", "),
                imports::listing(&self.imports)
            ) + &jobs::listing(&self.produces, &self.consumes),
            // The person asked in the app's main window; the tool is what would run.
            chain: vec![main_window()],
            executor: package,
            native_tool: true,
            files: vec![self.program.clone()],
            launch: None,
            environment: None,
        }
    }
}

/// The main window, as native confirmations name the requester of what the person asked for
/// there.
#[cfg(native_stack)]
pub fn main_window() -> crate::confirmation::Package {
    crate::confirmation::Package {
        id: "main window".into(),
        source: format!("pluxy app {}", env!("CARGO_PKG_VERSION")),
        digest: String::new(),
        signer: String::new(),
        contract: format!("app host {HOST_CONTRACT}"),
        generation: 0,
    }
}

/// A registry entry's signature, verified against a curated root.
pub struct Signing {
    pub verified: VerifiedDescriptor,
    pub signed: Signed,
}

/// A signature a registry entry lists: all four fields (`Artifact`).
pub(super) struct Listed<'a> {
    pub key_id: &'a str,
    pub public_key: &'a str,
    pub descriptor: &'a str,
    pub signature: &'a str,
}

/// The key source's root names for id's publisher and l's key ID, when the index names that
/// same key.
fn root_key(
    roots: &dyn Roots,
    source: &str,
    l: &Listed,
    id: &str,
) -> Result<ed25519_dalek::VerifyingKey, String> {
    let publisher = id.split('/').next().unwrap_or_default();
    let key = roots
        .publisher_key(source, l.key_id, publisher)
        .map_err(|e| format!("{id}: {e}"))?;
    if hex(key.as_bytes()) != l.public_key {
        return Err(format!(
            "{id}: the index's public_key is not the key its root names for {}",
            l.key_id
        ));
    }
    Ok(key)
}

/// Whether descriptor d names id's release version.
fn names(d: &artifact::Descriptor, id: &str, version: &str) -> Result<(), String> {
    if d.id != id || d.version != version {
        return Err(format!(
            "{id} {version}: its signed descriptor names {} {}",
            d.id, d.version
        ));
    }
    Ok(())
}

/// Checks the signature l over its descriptor with source's root key, and that the descriptor
/// names id's release version, from the index alone (before anything is
/// fetched).
pub(super) fn check_listed(
    roots: &dyn Roots,
    source: &str,
    l: &Listed,
    platform: &str,
    (id, version): (&str, &str),
) -> Result<(), String> {
    let key = root_key(roots, source, l, id)?;
    let sig = artifact::decode_hex::<64>(l.signature)
        .map_err(|_| format!("{id} {version}: its signature is not 128 lower-case hex digits"))?;
    let v = artifact::verify_descriptor(&key, l.descriptor.as_bytes(), &sig, platform)
        .map_err(|e| format!("{id} {version}: {e}"))?;
    names(v.descriptor(), id, version)
}

/// The descriptor tier a release of tier names.
fn tier(tier: &str) -> Tier {
    if tier == "native" {
        Tier::Native
    } else {
        Tier::Web
    }
}

/// Verifies a curated registry's signature l over its descriptor with source's root key for
/// id's publisher, then bytes against the signed size and digest, before anything is unpacked.
/// Returns the checked bytes.
pub(super) fn verify(
    roots: &dyn Roots,
    source: &str,
    l: &Listed,
    platform: &str,
    (id, version): (&str, &str),
    bytes: Vec<u8>,
) -> Result<(Signing, Vec<u8>), String> {
    let key = root_key(roots, source, l, id)?;
    let sig = artifact::decode_hex::<64>(l.signature)
        .map_err(|_| format!("{id} {version}: its signature is not 128 lower-case hex digits"))?;
    let checked = artifact::verify_archive(
        &key,
        l.descriptor.as_bytes(),
        &sig,
        platform,
        std::io::Cursor::new(bytes),
    )
    .map_err(|e| format!("{id} {version}: {e}"))?;
    names(checked.descriptor(), id, version)?;
    let (verified, archive) = checked.into_parts();
    Ok((
        Signing {
            verified,
            signed: Signed {
                publisher: id.split('/').next().unwrap_or_default().into(),
                key_id: l.key_id.into(),
                public_key: l.public_key.into(),
                source: source.into(),
                descriptor: l.descriptor.into(),
                signature: l.signature.into(),
            },
        },
        archive.into_inner(),
    ))
}

/// Fills in a signed native release staged in staged: its program for platform (this
/// computer's, or a Linux build's for WSL), which must be an executable file in the package, its
/// declarations and the digest of every file. executables: the files the archive's modes let run.
pub(super) fn native_release(
    r: &mut Release,
    app: &App,
    staged: &Path,
    signed: Signed,
    platform: &str,
    executables: &BTreeSet<String>,
) -> Result<(), String> {
    let program = app
        .programs
        .as_ref()
        .and_then(|p| p.get(platform))
        .ok_or_else(|| format!("{} has no program for {platform}", r.version))?;
    if !store_path(program) {
        return Err(format!(
            "program {program:?} must be a file inside the package"
        ));
    }
    r.tree = hash_files(staged, MAX_ENTRIES, MAX_UNPACKED, "the package")?;
    let meta = fs::symlink_metadata(staged.join(program));
    // A Linux build for WSL runs in Linux: its program is a file the archive's mode lets run,
    // whatever this computer's file system keeps.
    let foreign = platform != super::platform();
    let linux = platform.starts_with("linux/")
        && meta.as_ref().is_ok_and(|m| m.is_file())
        && executables.contains(program.as_str());
    #[cfg(unix)]
    let executable = !foreign
        && meta.is_ok_and(|m| {
            use std::os::unix::fs::PermissionsExt;
            m.is_file() && m.permissions().mode() & 0o111 != 0
        });
    // Windows starts programs only (`.exe` or `.com`, starting `MZ`), never a script by its name.
    #[cfg(windows)]
    let executable = !foreign && meta.is_ok_and(|m| m.is_file()) && {
        let path = staged.join(program);
        let f = fs::File::open(&path).map_err(|e| format!("program {program}: {e}"))?;
        crate::owned::windows::image(&f, &path)?;
        true
    };
    #[cfg(not(any(unix, windows)))]
    let executable = !foreign && meta.is_ok_and(|m| m.is_file());
    let executable = if foreign { linux } else { executable };
    if !r.tree.contains_key(program) || !executable {
        return Err(format!(
            "program {program} is not an executable file in the package"
        ));
    }
    r.tier = "native".into();
    r.signed = Some(signed);
    r.program = Some(program.clone());
    r.operations = app
        .operations
        .iter()
        .flatten()
        .map(|o| OperationDecl {
            id: o.id.clone(),
            description: o.description.clone(),
            input_schema: o.input_schema.clone(),
            output_schema: o.output_schema.clone(),
            effect: o.effect.clone().unwrap_or_else(imports::write_effect),
            secrets: o.secrets.clone().unwrap_or_default(),
        })
        .collect();
    r.exports = app.exports.as_ref().map(|e| ExportDecl {
        contract: e.contract.unwrap_or_default(),
        operations: e.operations.clone(),
    });
    r.state = app.state.as_ref().map(|s| StateDecl {
        version: s.version,
        readable_versions: s.readable_versions.clone(),
    });
    r.environments = app.environments.clone().unwrap_or_default();
    Ok(())
}

/// Checks that dir holds exactly the files of want, each with its digest.
pub(super) fn verify_tree(dir: &Path, want: &BTreeMap<String, String>) -> Result<(), String> {
    let got = hash_files(dir, MAX_ENTRIES, MAX_UNPACKED, "the installed package")?;
    if got != *want {
        return Err(format!(
            "the files in {} changed since they were installed",
            dir.display()
        ));
    }
    Ok(())
}

impl Catalog {
    /// Package id as an admitted native tool would be run: a native release with a curated
    /// signature that verifies against its root now, from the source it was signed for, for
    /// this host contract; and the digests its files must have, which the caller checks without
    /// the catalog's lock (`checked_unlocked`).
    fn native_record(&self, id: &str, roots: &dyn Roots) -> Result<(Admitted, Tree), String> {
        let p = self
            .packages
            .get(id)
            .ok_or_else(|| format!("{id} is not installed on this device"))?;
        self.release_record(id, p, &p.active, roots)
    }

    /// `native_record`, for release r of package p (the active release, or one being swapped
    /// in).
    pub(super) fn release_record(
        &self,
        id: &str,
        p: &Package,
        r: &Release,
        roots: &dyn Roots,
    ) -> Result<(Admitted, Tree), String> {
        self.release_record_for(id, p, r, roots, &platform())
    }

    /// `release_record`, for release r's archive for platform (a Linux build for WSL).
    fn release_record_for(
        &self,
        id: &str,
        p: &Package,
        r: &Release,
        roots: &dyn Roots,
        platform: &str,
    ) -> Result<(Admitted, Tree), String> {
        if r.tier != "native" {
            return Err(format!("{id} is not a native tool"));
        }
        let s = p.curated_signature_for(id, r, roots, platform)?;
        if !r.host_versions.contains(&HOST_CONTRACT) {
            return Err(format!(
                "{id} {} needs app host contract {:?}; this app speaks host contract {HOST_CONTRACT}",
                r.version, r.host_versions
            ));
        }
        let state = r
            .state
            .clone()
            .ok_or_else(|| format!("{id} {} declares no state versions", r.version))?;
        let (program, program_sha256) = r
            .program
            .as_ref()
            .and_then(|x| r.tree.get(x).map(|h| (x, h.clone())))
            .ok_or_else(|| {
                if platform == super::platform() {
                    format!("{id} {} has no program for this computer", r.version)
                } else {
                    format!("{id} {} has no program for {platform}", r.version)
                }
            })?;
        let dir = self.dirs.store().join(&r.sha256);
        let a = Admitted {
            id: id.into(),
            version: r.version.clone(),
            sha256: r.sha256.clone(),
            size: r.size,
            installed_at: r.installed_at,
            source: p.source.clone(),
            signed: s.clone(),
            program: dir.join(program),
            program_sha256,
            dir,
            operations: r.operations.clone(),
            state,
            app_version: r.app_version,
            host_contract: HOST_CONTRACT,
            imports: r.imports.clone(),
            exports: r.exports.clone(),
            produces: r.produces.clone(),
            consumes: r.consumes.clone(),
        };
        Ok((a, r.tree.clone()))
    }

    /// The native tool id as the native confirmation enabling it shows it, and its files'
    /// digests (`native_candidate_unlocked`).
    pub(super) fn native_candidate_record(
        &self,
        id: &str,
        roots: &dyn Roots,
    ) -> Result<(Admitted, Tree), String> {
        if self.safe_mode {
            return Err("the app runs in safe mode: no native tool is enabled".into());
        }
        if self.packages.get(id).is_some_and(|p| p.enabled) {
            return Err(format!("{id} is enabled already"));
        }
        let (a, tree) = self.native_record(id, roots)?;
        // Its required imports are enabled tools exporting them, and they lead back to it nowhere.
        self.dependencies(id, &a.imports, roots)?;
        Ok((a, tree))
    }

    /// Enables the native tool a, after its native confirmation, when it is still the release,
    /// source and signer the person confirmed. Its files were checked just now without the
    /// catalog's lock (`native_candidate_unlocked`): the record must still be the one checked.
    pub fn admit_checked(&mut self, a: &Admitted, roots: &dyn Roots) -> Result<(), String> {
        if self.safe_mode {
            return Err("the app runs in safe mode: no native tool is enabled".into());
        }
        let (now, _) = self.native_record(&a.id, roots)?;
        if now != *a {
            return Err(format!(
                "{} changed while it was being confirmed; enable it again",
                a.id
            ));
        }
        // Its imports again: a target disabled while it was being confirmed refuses it.
        self.dependencies(&a.id, &a.imports, roots)?;
        let p = self
            .packages
            .get_mut(&a.id)
            .ok_or_else(|| format!("{} is not installed on this device", a.id))?;
        let was = (p.enabled, p.admission.take());
        p.enabled = true;
        p.admission = Some(Admission::of(a));
        if let Err(e) = self.save() {
            if let Some(p) = self.packages.get_mut(&a.id) {
                (p.enabled, p.admission) = was;
            }
            return Err(e);
        }
        Ok(())
    }

    /// The admitted native tool id, for the program host: enabled, confirmed for its active
    /// release, signer and key, and checked again now (root, signature, files).
    pub fn admitted(&self, id: &str, roots: &dyn Roots) -> Result<Admitted, String> {
        let (a, tree) = self.admitted_record(id, roots)?;
        verify_tree(&a.dir, &tree)?;
        Ok(a)
    }

    /// `admitted` but for the installed files.
    pub(super) fn admitted_record(
        &self,
        id: &str,
        roots: &dyn Roots,
    ) -> Result<(Admitted, Tree), String> {
        if self.safe_mode {
            return Err("the app runs in safe mode: no native tool is admitted".into());
        }
        let p = self
            .packages
            .get(id)
            .ok_or_else(|| format!("{id} is not installed on this device"))?;
        if !p.enabled {
            return Err(format!("{id} is disabled"));
        }
        let admission = p
            .admission
            .as_ref()
            .filter(|a| a.names(&p.active))
            .ok_or_else(|| {
                format!(
                    "{id} {} has no native confirmation for its active release",
                    p.active.version
                )
            })?;
        let (a, tree) = self.native_record(id, roots)?;
        if admission.public_key != a.public_key() || admission.host_contract != a.host_contract {
            return Err(format!(
                "{id}'s signing key or host contract changed since it was confirmed; enable it again"
            ));
        }
        Ok((a, tree))
    }
}

/// Each installed file's digest, by its path in the package.
type Tree = BTreeMap<String, String>;

/// A check of a native tool's installed files that does not hold the catalog's lock while it
/// hashes them: read gives the record and the digests under the lock; the files are hashed
/// without it; then read must give the same record again (the same release and generation,
/// source, signer and program). A large package then never holds up the catalog.
pub(super) fn checked_unlocked(
    catalog: &std::sync::Mutex<Catalog>,
    read: impl Fn(&Catalog) -> Result<(Admitted, Tree), String>,
) -> Result<Admitted, String> {
    let (a, tree) = read(&catalog.lock().unwrap())?;
    verify_tree(&a.dir, &tree)?;
    let (again, _) = read(&catalog.lock().unwrap())?;
    if again != a {
        return Err(format!(
            "{} changed while its files were checked; try again",
            a.id
        ));
    }
    Ok(a)
}

/// `Catalog::admitted`, hashing the files without the catalog's lock (`checked_unlocked`).
pub fn admitted_unlocked(
    catalog: &std::sync::Mutex<Catalog>,
    id: &str,
    roots: &dyn Roots,
) -> Result<Admitted, String> {
    checked_unlocked(catalog, |c| c.admitted_record(id, roots))
}

/// The native tool id as the native confirmation enabling it shows it, checked, hashing its
/// files without the catalog's lock (`checked_unlocked`); enable it with
/// `Catalog::admit_checked`.
pub fn native_candidate_unlocked(
    catalog: &std::sync::Mutex<Catalog>,
    id: &str,
    roots: &dyn Roots,
) -> Result<Admitted, String> {
    checked_unlocked(catalog, |c| c.native_candidate_record(id, roots))
}

// ---- A native tool's Linux build, to run it in WSL ----

/// The platforms a WSL distribution runs, whose Linux build a native tool may have.
const WSL_PLATFORMS: &[&str] = &["linux/amd64", "linux/arm64"];

/// What staging native tool id's Linux build for platform starts from: its active release,
/// source and signer as the catalog has them now (`Catalog::linux_target`).
#[derive(Clone, Debug, PartialEq)]
pub struct LinuxTarget {
    id: String,
    platform: String,
    source: Source,
    active: Release,
    signed: Signed,
}

/// A native tool's Linux build, checked and staged, waiting for the person's native
/// confirmation and `Catalog::commit_linux`. Dropped, it leaves nothing.
#[derive(Debug)]
pub struct LinuxCandidate {
    target: LinuxTarget,
    pub(super) candidate: Candidate,
}

impl LinuxCandidate {
    /// The package ID.
    pub fn id(&self) -> &str {
        &self.target.id
    }
    /// `linux/amd64` or `linux/arm64`.
    pub fn platform(&self) -> &str {
        &self.target.platform
    }
    /// The release's version, the active release's.
    pub fn version(&self) -> &str {
        &self.candidate.release.version
    }
    /// SHA-256 of the Linux archive, in lower-case hex.
    pub fn sha256(&self) -> &str {
        &self.candidate.release.sha256
    }

    /// The native confirmation installing it shows: the main window as the requester; the
    /// package, with its Linux archive's digest, as the executor; its version and platform.
    #[cfg(native_stack)]
    pub fn confirmation(&self) -> crate::confirmation::Action {
        let r = &self.candidate.release;
        let s = &self.target.signed;
        crate::confirmation::Action {
            what: format!(
                "Install the Linux build of {} {} ({}, SHA-256 {}) to run it in WSL",
                self.target.id, r.version, self.target.platform, r.sha256
            ),
            chain: vec![main_window()],
            executor: crate::confirmation::Package {
                id: self.target.id.clone(),
                source: format!("registry {} ({})", self.target.source.name, s.source),
                digest: r.sha256.clone(),
                signer: format!("{} key {} ({})", s.publisher, s.key_id, s.public_key),
                contract: format!("app host {HOST_CONTRACT}; app {}", r.app_version),
                generation: r.installed_at,
            },
            // What it installs runs with full access to the account it runs as.
            native_tool: true,
            files: vec![],
            launch: None,
            environment: None,
        }
    }
}

/// Whether two releases of one package declare the same: operations, state, imports, exports,
/// jobs, environments and app contribution version. A Linux build declaring more than the
/// release the person enabled would get what they never confirmed.
fn same_declarations(a: &Release, b: &Release) -> bool {
    (
        a.app_version,
        &a.operations,
        &a.state,
        &a.imports,
        &a.exports,
        &a.produces,
        &a.consumes,
        &a.environments,
    ) == (
        b.app_version,
        &b.operations,
        &b.state,
        &b.imports,
        &b.exports,
        &b.produces,
        &b.consumes,
        &b.environments,
    )
}

/// Whether s and t name the same signer: publisher, key ID and source.
fn same_signer(s: &Signed, t: &Signed) -> bool {
    (&s.publisher, &s.key_id, &s.source) == (&t.publisher, &t.key_id, &t.source)
}

/// Fetches native tool t.id's Linux build for t.platform from the registry it was installed
/// from, the active release's version, and checks it before anything is recorded: its curated
/// signature for that platform, by the active release's signer; its size and digest; its
/// entries; its manifest (the same ID and version, a native tool for this host contract running
/// in WSL, declaring what the active release declares, with an executable program for the
/// platform); and the digest of every file. Hold no catalog lock while it runs.
pub fn stage_linux(
    dirs: &Dirs,
    roots: &dyn Roots,
    t: &LinuxTarget,
) -> Result<LinuxCandidate, String> {
    let (id, platform, version) = (&t.id, &t.platform, &t.active.version);
    let name = &t.source.name;
    let r = registries(dirs)?
        .into_iter()
        .find(|r| &r.name == name && t.source.index.as_deref() == Some(r.url.as_str()))
        .ok_or_else(|| {
            format!("registry {name} no longer has the index {id} was installed from")
        })?;
    let (index, base) = read_index(&r)?;
    let a = index
        .packages
        .get(id.as_str())
        .and_then(|p| p.releases.iter().find(|x| &x.version == version))
        .and_then(|x| x.artifacts.get(platform.as_str()))
        .ok_or_else(|| format!("registry {name} lists no {id} {version} for {platform}"))?;
    let l = to_verify(roots, &r, id, version, a)?.ok_or_else(|| {
        format!("{id} {version} for {platform}: registry {name} lists no curated signature for it")
    })?;
    let bytes = fetch_artifact(&base, a)?;
    let (signing, bytes) = verify(roots, &r.url, &l, platform, (id, version), bytes)?;
    if !same_signer(&signing.signed, &t.signed) {
        return Err(format!(
            "{id} {version} for {platform} is signed by {} key {}, not by the signer of the installed release",
            signing.signed.publisher, signing.signed.key_id
        ));
    }
    let c = stage_signed(
        dirs,
        &bytes,
        t.source.clone(),
        Some(&Expect {
            id: id.clone(),
            version: version.clone(),
            sha256: a.sha256.to_ascii_lowercase(),
            size: a.size,
        }),
        Some(signing),
        roots,
        platform,
    )?;
    let rel = &c.release;
    if rel.tier != "native" {
        return Err(format!(
            "{id} {version} for {platform} is not a native tool"
        ));
    }
    if !rel.environments.iter().any(|e| e == "wsl") {
        return Err(format!("{id} {version} for {platform} does not run in WSL"));
    }
    if !same_declarations(rel, &t.active) {
        return Err(format!(
            "{id} {version} for {platform} declares other operations, state, imports, exports, jobs or environments than the installed release"
        ));
    }
    Ok(LinuxCandidate {
        target: t.clone(),
        candidate: c,
    })
}

impl Catalog {
    /// Whether native tool id's active release declares that it runs in WSL.
    pub fn runs_in_wsl(&self, id: &str) -> bool {
        self.packages
            .get(id)
            .is_some_and(|p| p.active.environments.iter().any(|e| e == "wsl"))
    }

    /// What staging native tool id's Linux build for platform starts from (`stage_linux`): an
    /// enabled tool the person confirmed for its active release, which runs in WSL.
    pub fn linux_target(
        &self,
        id: &str,
        platform: &str,
        roots: &dyn Roots,
    ) -> Result<LinuxTarget, String> {
        if !WSL_PLATFORMS.contains(&platform) {
            return Err(format!(
                "{platform} is not a platform WSL runs (linux/amd64 or linux/arm64)"
            ));
        }
        if self.safe_mode {
            return Err("the app runs in safe mode: no native tool is admitted".into());
        }
        let p = self
            .packages
            .get(id)
            .ok_or_else(|| format!("{id} is not installed on this device"))?;
        if !self.runs_in_wsl(id) {
            return Err(format!("{id} does not run in WSL"));
        }
        let (a, _) = self.admitted_record(id, roots)?;
        Ok(LinuxTarget {
            id: id.into(),
            platform: platform.into(),
            source: p.source.clone(),
            active: p.active.clone(),
            signed: a.signed,
        })
    }

    /// Records Linux build c of its package, after its native confirmation, while the package
    /// is still the release, source and signer it was staged for and its staged files are
    /// unchanged. A build for the same platform it replaces goes.
    pub fn commit_linux(&mut self, c: LinuxCandidate, roots: &dyn Roots) -> Result<(), String> {
        let result = self.commit_linux_inner(&c, roots);
        // The staged folder is either in the store now or no longer needed.
        if let Err(e) = remove_tree(&c.candidate.staged) {
            self.cleanup.push(format!("could not delete {e}"));
        }
        if result.is_err() {
            self.collect();
        }
        result
    }

    fn commit_linux_inner(&mut self, c: &LinuxCandidate, roots: &dyn Roots) -> Result<(), String> {
        let (id, platform) = (c.id(), c.platform());
        if self.linux_target(id, platform, roots)? != c.target {
            return Err(format!(
                "{id} changed while its Linux build was being confirmed; install it again"
            ));
        }
        verify_tree(&c.candidate.staged, &c.candidate.release.tree)?;
        self.place(&c.candidate)?;
        let p = self.packages.get_mut(id).unwrap();
        let was = p.linux.insert(platform.into(), c.candidate.release.clone());
        if let Err(e) = self.save() {
            let p = self.packages.get_mut(id).unwrap();
            match was {
                Some(r) => p.linux.insert(platform.into(), r),
                None => p.linux.remove(platform),
            };
            return Err(e);
        }
        self.collect();
        Ok(())
    }

    /// `admitted_record`, for native tool id's Linux build for platform: the tool runs in WSL,
    /// is enabled and confirmed for its active release, and has a Linux build of that version
    /// and signer whose curated signature verifies for platform now.
    pub(super) fn admitted_linux_record(
        &self,
        id: &str,
        platform: &str,
        roots: &dyn Roots,
    ) -> Result<(Admitted, Tree), String> {
        if self.safe_mode {
            return Err("the app runs in safe mode: no native tool is admitted".into());
        }
        let p = self
            .packages
            .get(id)
            .ok_or_else(|| format!("{id} is not installed on this device"))?;
        if !self.runs_in_wsl(id) {
            return Err(format!("{id} does not run in WSL"));
        }
        let (active, _) = self.admitted_record(id, roots)?;
        let r = p
            .linux
            .get(platform)
            .filter(|r| r.version == p.active.version)
            .ok_or_else(|| {
                format!("{id}'s Linux build for {platform} is not installed: install it for WSL")
            })?;
        let (a, tree) = self.release_record_for(id, p, r, roots, platform)?;
        if !same_signer(&a.signed, &active.signed) {
            return Err(format!(
                "{id}'s Linux build for {platform} is not signed by the signer of its active release: install it for WSL again"
            ));
        }
        Ok((a, tree))
    }
}

/// Native tool id's Linux build for platform, as the program host runs it in WSL: checked as
/// `Catalog::admitted_linux_record` says, and every file of it hashed again without the
/// catalog's lock (`checked_unlocked`).
pub fn admitted_linux_unlocked(
    catalog: &std::sync::Mutex<Catalog>,
    id: &str,
    platform: &str,
    roots: &dyn Roots,
) -> Result<Admitted, String> {
    checked_unlocked(catalog, |c| c.admitted_linux_record(id, platform, roots))
}
