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

/// A release's publisher signature, as the catalog keeps it (`device.rs`).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Signed {
    pub publisher: String,
    pub key_id: String,
    /// The Ed25519 public key the index named, in lower-case hex.
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
