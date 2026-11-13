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

/// The curated signature a release was installed with: the same shape as the signatures
/// Access keeps for approved signers (P8e3).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Signed {
    pub publisher: String,
    /// The key's ID in its root.
    pub key_id: String,
    /// The Ed25519 public key the index named, which is the root's, in lower-case hex.
    pub public_key: String,
    /// The curated registry's index the root names: the trust domain.
    pub source: String,
    /// The exact signed descriptor bytes, UTF-8 JSON as the index gave them.
    pub descriptor: String,
    /// The detached Ed25519 signature, in lower-case hex.
    pub signature: String,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct OperationDecl {
    pub id: String,
    pub description: String,
    pub input_schema: Value,
    pub output_schema: Value,
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
        let v = artifact::verify_descriptor(&key, s.descriptor.as_bytes(), &sig, &platform())
            .map_err(|e| format!("{id}: {e}"))?;
        let d = v.descriptor();
        if d.id != id
            || d.version != r.version
            || d.tier != Tier::Native
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
/// enabling it shows (`Catalog::native_candidate`). It holds no signer logic for the host to redo.
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
    /// The native confirmation enabling it shows: the main window as the requester; the tool,
    /// as the executor, with its ID, source, digest, signer and key, contracts, operations and
    /// program; and that it has full access to your user account.
    #[cfg(target_os = "linux")]
    pub fn confirmation(&self) -> crate::confirmation::Action {
        let package = crate::confirmation::Package {
            id: self.id.clone(),
            source: format!("registry {} ({})", self.source.name, self.signed.source),
            digest: self.sha256.clone(),
            signer: format!(
                "{} key {} ({})",
                self.signed.publisher, self.signed.key_id, self.signed.public_key
            ),
            contract: format!(
                "app host {}; app {}; state {}, reads {:?}",
                self.host_contract,
                self.app_version,
                self.state.version,
                self.state.readable_versions
            ),
            generation: self.installed_at,
        };
        let operations: Vec<String> = self
            .operations
            .iter()
            .map(|o| format!("{} ({})", o.id, o.description))
            .collect();
        crate::confirmation::Action {
            what: format!(
                "Enable native tool {} {}. It has full access to your user account. Operations: {}",
                self.id,
                self.version,
                operations.join(", ")
            ),
            // The person asked in the app's main window; the tool is what would run.
            chain: vec![crate::confirmation::Package {
                id: "main window".into(),
                source: format!("pluxy app {}", env!("CARGO_PKG_VERSION")),
                digest: String::new(),
                signer: String::new(),
                contract: format!("app host {HOST_CONTRACT}"),
                generation: 0,
            }],
            executor: package,
            native_tool: true,
            files: vec![self.program.clone()],
            launch: None,
        }
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
/// names id's release version as a native tool, from the index alone (before anything is
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
    names(v.descriptor(), id, version)?;
    // As staging refuses it once the archive is unpacked.
    if v.descriptor().tier != Tier::Native {
        return Err(format!(
            "{id} {version}: curated web packages are not admitted by this app yet"
        ));
    }
    Ok(())
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

/// Fills in a signed native release staged in staged: its program for this computer, which
/// must be an executable file in the package, its declarations and the digest of every file.
pub(super) fn native_release(
    r: &mut Release,
    app: &App,
    staged: &Path,
    signed: Signed,
) -> Result<(), String> {
    let platform = platform();
    let program = app
        .programs
        .as_ref()
        .and_then(|p| p.get(&platform))
        .ok_or_else(|| format!("{} has no program for {platform}", r.version))?;
    if !store_path(program) {
        return Err(format!(
            "program {program:?} must be a file inside the package"
        ));
    }
    r.tree = hash_files(staged, MAX_ENTRIES, MAX_UNPACKED, "the package")?;
    let meta = fs::symlink_metadata(staged.join(program));
    #[cfg(unix)]
    let executable = meta.is_ok_and(|m| {
        use std::os::unix::fs::PermissionsExt;
        m.is_file() && m.permissions().mode() & 0o111 != 0
    });
    #[cfg(not(unix))]
    let executable = meta.is_ok_and(|m| m.is_file());
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
        })
        .collect();
    r.state = app.state.as_ref().map(|s| StateDecl {
        version: s.version,
        readable_versions: s.readable_versions.clone(),
    });
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
    /// this host contract, with every file unchanged.
    fn check_native(&self, id: &str, roots: &dyn Roots) -> Result<Admitted, String> {
        let (a, tree) = self.native_record(id, roots)?;
        verify_tree(&a.dir, &tree)?;
        Ok(a)
    }

    /// `check_native` but for the installed files: the record and the digests its files must
    /// have, to be checked without the catalog's lock (`checked_unlocked`).
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
        if r.tier != "native" {
            return Err(format!("{id} is not a native tool"));
        }
        let s = p.curated_signature_of(id, r, roots)?;
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
            .ok_or_else(|| format!("{id} {} has no program for this computer", r.version))?;
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
        };
        Ok((a, r.tree.clone()))
    }

    /// The native tool id as the native confirmation enabling it shows it, checked.
    pub fn native_candidate(&self, id: &str, roots: &dyn Roots) -> Result<Admitted, String> {
        let (a, tree) = self.native_candidate_record(id, roots)?;
        verify_tree(&a.dir, &tree)?;
        Ok(a)
    }

    /// `native_candidate` but for the installed files.
    fn native_candidate_record(
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
        self.native_record(id, roots)
    }

    /// Enables the native tool a, after its native confirmation, when it is still the release,
    /// source and signer the person confirmed and still checks out.
    pub fn admit(&mut self, a: &Admitted, roots: &dyn Roots) -> Result<(), String> {
        let now = self.check_native(&a.id, roots);
        self.admit_as(a, now)
    }

    /// `admit`, for a candidate whose files `native_candidate_unlocked` checked just now without
    /// the catalog's lock: the record must still be the one checked.
    pub fn admit_checked(&mut self, a: &Admitted, roots: &dyn Roots) -> Result<(), String> {
        let now = self.native_record(&a.id, roots).map(|(now, _)| now);
        self.admit_as(a, now)
    }

    fn admit_as(&mut self, a: &Admitted, now: Result<Admitted, String>) -> Result<(), String> {
        if self.safe_mode {
            return Err("the app runs in safe mode: no native tool is enabled".into());
        }
        if now? != *a {
            return Err(format!(
                "{} changed while it was being confirmed; enable it again",
                a.id
            ));
        }
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

/// `Catalog::native_candidate`, hashing the files without the catalog's lock
/// (`checked_unlocked`); enable it with `Catalog::admit_checked`.
pub fn native_candidate_unlocked(
    catalog: &std::sync::Mutex<Catalog>,
    id: &str,
    roots: &dyn Roots,
) -> Result<Admitted, String> {
    checked_unlocked(catalog, |c| c.native_candidate_record(id, roots))
}
