//! Access: the broker that lets a web package on this device read files and folders the person
//! chose for it (`../README.md`, "Access"). This module holds the approved signers and the
//! grants, checks each read against them, and does the reads. The window's commands, the native
//! file chooser and confirmation, and the page channel are in `lib.rs`; nothing here talks to a
//! webview or GTK.
//!
//! A grant binds one declared resource of one package to what the person chose: the package's
//! source, release digest and signer, the chosen path, the target it resolved to (a config
//! manager's link is followed once, at review), the target's identity, and, for a link, the
//! link's own identity and text. Each read checks all of these again. A changed link or target
//! ends the grant until the person grants it again. Folder reads go through a `cap_std` folder
//! handle opened on the granted folder, so no path, `..` or link leaves it. A file the document
//! portal offers is granted for this session only. Revoking bumps a revision every read checks
//! before it answers.
//!
//! This is cooperative: same-user processes can still change files between a check and a read,
//! and hard links inside a granted folder disclose their files.

use serde::{Deserialize, Serialize};

#[cfg(unix)]
#[path = "access_broker.rs"]
mod broker;
#[cfg(unix)]
pub use broker::*;

/// The operations this app can grant, by resource kind.
pub fn supported(kind: &str, op: &str) -> bool {
    matches!((kind, op), ("file", "read") | ("subtree", "list" | "read"))
}

/// A resource a release declares.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Declared {
    pub id: String,
    /// `file` or `subtree`.
    pub kind: String,
    pub operations: Vec<String>,
}

/// A release's publisher signature, as the catalog keeps it (`device.rs`): the one shape for a
/// curated root's signature of a native tool (`device_admission.rs`) and for a signature no
/// root vouches for, which Access uses once the person approved its signer. Which of the two it
/// is comes from the current roots, never from this record.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Signed {
    pub publisher: String,
    /// The key's ID: in its root, for a curated signature.
    pub key_id: String,
    /// The Ed25519 public key the index named, in lower-case hex: the root's, for a curated
    /// signature.
    pub public_key: String,
    /// The registry's index: the trust domain.
    pub source: String,
    /// The exact signed descriptor bytes, UTF-8 JSON as the index gave them.
    pub descriptor: String,
    /// The detached signature, in lower-case hex.
    pub signature: String,
}

/// What the broker needs to know about a package, from this device's catalog.
#[derive(Clone, Debug)]
pub struct Facts {
    pub id: String,
    pub enabled: bool,
    /// The registry index it came from; None for a local file.
    pub source: Option<String>,
    /// The active release's archive digest.
    pub sha256: String,
    pub signed: Option<Signed>,
    pub declared: Vec<Declared>,
}

/// The native confirmation of an Access action on package f: what it does and the files it
/// names. The person asked in the app's main window, as for a native tool
/// (`device_admission.rs`); the package is what the action is for.
#[cfg(target_os = "linux")]
pub fn confirmation(
    f: &Facts,
    what: String,
    files: Vec<std::path::PathBuf>,
) -> crate::confirmation::Action {
    let package = crate::confirmation::Package {
        id: f.id.clone(),
        source: f.source.clone().unwrap_or_else(|| "a local file".into()),
        digest: f.sha256.clone(),
        signer: f
            .signed
            .as_ref()
            .map(|s| {
                format!(
                    "{} key {} (Ed25519 public key {})",
                    s.publisher, s.key_id, s.public_key
                )
            })
            .unwrap_or_else(|| "unsigned".into()),
        contract: format!("app host {}, web tier", crate::device::HOST_CONTRACT),
        generation: 0,
    };
    crate::confirmation::Action {
        what,
        chain: vec![crate::device::main_window()],
        executor: package,
        native_tool: false,
        files,
        launch: None,
    }
}
