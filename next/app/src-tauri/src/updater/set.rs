//! The program set of a Windows folder bundle (`exe.rs`), as plain file work: built on Windows
//! and, for its tests, on every system. The set is the folder's own files: the app's program,
//! the core beside it (`pluxy.exe`) and the core's packages (`bundled\`); anything else in the
//! folder (an installer's `uninstall.exe`, what an install leaves) is not part of it.
//!
//! - It is pinned by its digest (`digest`), computed from the files themselves.
//! - An update is a `.tar.gz` of the set, unpacked under strict rules (`unpack`).
//! - It is replaced part by part, core parts first and the program last (`apply`): each part is
//!   renamed aside (`.pluxy-old-<hex>-<part>`) and the new one renamed in from the staging folder
//!   (`.pluxy-update-<hex>`), as `pending.json`'s move list (`Moves`) says. A replace that stops
//!   part way is undone at the next start of the version it came from (`undo`), and a put-back of
//!   the kept set that stops part way is finished (`complete`), each part checked against the kept
//!   set first (`recover`).
//!
//! The renames go through `Ops`, so the tests can stop them at any point, as a crash would.
#![cfg_attr(not(windows), allow(dead_code))]
use super::{sha256, Pending, MAX_BYTES, MAX_ENTRIES};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::fs;
use std::io::{self, Read, Write};
use std::path::{Component, Path};

/// The core program beside the app's.
pub(super) const CORE: &str = "pluxy.exe";
/// The core's packages.
pub(super) const BUNDLED: &str = "bundled";
/// What a staging folder's name starts with; 16 hex digits follow.
const STAGING: &str = ".pluxy-update-";
/// What an aside's name starts with: `<hex>-<part>` follows.
const ASIDE: &str = ".pluxy-old-";

/// The set's parts for the app program named exe, in the order a replace moves them.
pub(super) fn parts(exe: &str) -> [&str; 3] {
    [BUNDLED, CORE, exe]
}

/// The lines part (at path, named name in the set) adds to a digest: its own and, for a folder,
/// everything below it. A part has its fixed kind: `bundled` a folder, the others plain files.
fn part_lines(path: &Path, name: &str, out: &mut Vec<(Vec<u8>, String)>) -> Result<(), String> {
    let m = plain(path)?;
    if (name == BUNDLED) != m.is_dir() {
        return Err(format!(
            "{} is not a {}",
            path.display(),
            if name == BUNDLED { "folder" } else { "file" }
        ));
    }
    lines(path, name, out)
}

/// What path is, without following a link; refused unless a plain folder or file (on Windows
/// not a reparse point of any kind either).
fn plain(path: &Path) -> Result<fs::Metadata, String> {
    let m = fs::symlink_metadata(path).map_err(|e| format!("{}: {e}", path.display()))?;
    #[cfg(windows)]
    let reparse = {
        use std::os::windows::fs::MetadataExt;
        m.file_attributes() & 0x400 != 0 // FILE_ATTRIBUTE_REPARSE_POINT
    };
    #[cfg(not(windows))]
    let reparse = false;
    if reparse || !(m.is_dir() || m.is_file()) {
        return Err(format!(
            "{} is a link or another kind of file, which a program set may not hold",
            path.display()
        ));
    }
    Ok(m)
}

fn lines(path: &Path, rel: &str, out: &mut Vec<(Vec<u8>, String)>) -> Result<(), String> {
    let m = plain(path)?;
    if m.is_dir() {
        out.push((rel.as_bytes().to_vec(), format!("d {rel}\n")));
        for e in fs::read_dir(path).map_err(|e| format!("{}: {e}", path.display()))? {
            let e = e.map_err(|e| e.to_string())?;
            let name = e.file_name();
            let name = name
                .to_str()
                .ok_or_else(|| format!("{} has a name that is not UTF-8", e.path().display()))?;
            lines(&e.path(), &format!("{rel}/{name}"), out)?;
        }
    } else {
        let mut f = fs::File::open(path).map_err(|e| format!("{}: {e}", path.display()))?;
        let mut h = <sha2::Sha256 as sha2::Digest>::new();
        let mut buf = vec![0u8; 1 << 16];
        let mut size = 0u64;
        loop {
            let n = f
                .read(&mut buf)
                .map_err(|e| format!("{}: {e}", path.display()))?;
            if n == 0 {
                break;
            }
            size += n as u64;
            sha2::Digest::update(&mut h, &buf[..n]);
        }
        let digest = crate::artifact::hex(&sha2::Digest::finalize(h));
        out.push((
            rel.as_bytes().to_vec(),
            format!("f {size} {digest} {rel}\n"),
        ));
    }
    Ok(())
}

fn hashed(head: &str, mut lines: Vec<(Vec<u8>, String)>) -> String {
    lines.sort_by(|a, b| a.0.cmp(&b.0));
    let mut text = String::from(head);
    for (_, line) in lines {
        text.push_str(&line);
    }
    sha256(text.as_bytes())
}

/// The `pluxy-set-v1` digest of the set in folder whose program is exe: the SHA-256 (hex) of
/// `pluxy-set-v1\n` and a line per entry of the set, sorted by its path (`/`-separated, from the
/// folder, compared as bytes): a folder `d <path>\n`, a file `f <size> <SHA-256> <path>\n`. Only
/// the set's parts count, and each must be there with its kind; a link or any other kind of file
/// in them has no digest.
pub(super) fn digest(folder: &Path, exe: &str) -> Result<String, String> {
    let mut out = Vec::new();
    for part in parts(exe) {
        part_lines(&folder.join(part), part, &mut out)?;
    }
    Ok(hashed("pluxy-set-v1\n", out))
}

/// The `pluxy-part-v1` digest of the part at path as the set's part name: the same lines as the
/// set digest's for that part, after `pluxy-part-v1\n`.
pub(super) fn part_digest(path: &Path, name: &str) -> Result<String, String> {
    let mut out = Vec::new();
    part_lines(path, name, &mut out)?;
    Ok(hashed("pluxy-part-v1\n", out))
}

/// Copies the set whose program is exe from the folder from into the folder to, which exists
/// and holds none of its parts: plain folders and files only, made new.
pub(super) fn copy(from: &Path, to: &Path, exe: &str) -> Result<(), String> {
    fn tree(from: &Path, to: &Path) -> Result<(), String> {
        let m = plain(from)?;
        if m.is_dir() {
            fs::create_dir(to).map_err(|e| format!("{}: {e}", to.display()))?;
            for e in fs::read_dir(from).map_err(|e| format!("{}: {e}", from.display()))? {
                let e = e.map_err(|e| e.to_string())?;
                tree(&e.path(), &to.join(e.file_name()))?;
            }
            return Ok(());
        }
        let mut r = fs::File::open(from).map_err(|e| format!("{}: {e}", from.display()))?;
        let mut w = fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(to)
            .map_err(|e| format!("{}: {e}", to.display()))?;
        io::copy(&mut r, &mut w).map_err(|e| format!("{}: {e}", to.display()))?;
        w.sync_all().map_err(|e| format!("{}: {e}", to.display()))
    }
    for part in parts(exe) {
        let m = plain(&from.join(part))?;
        if (part == BUNDLED) != m.is_dir() {
            return Err(format!(
                "{} is not a part of a set",
                from.join(part).display()
            ));
        }
        tree(&from.join(part), &to.join(part))?;
    }
    Ok(())
}

/// The key Windows compares names by, ignoring case: each character as its one-character upper
/// case, if it has one. Two names with the same key are one name there. As the core store
/// compares them (`cores.rs` on the A1 branch; one copy goes once both are on main).
fn windows_name_key(name: &str) -> String {
    name.chars()
        .map(|c| {
            let mut upper = c.to_uppercase();
            match (upper.next(), upper.next()) {
                (Some(u), None) => u,
                _ => c,
            }
        })
        .collect()
}

/// Whether name has the form of a short (8.3) name Windows makes for a longer one: a stem of at
/// most 8 characters ending in `~` and digits, and an extension of at most 3. On a volume that
/// makes such names, it may be another entry's second name.
fn short_alias(name: &str) -> bool {
    let (stem, ext) = name.rsplit_once('.').unwrap_or((name, ""));
    stem.chars().count() <= 8
        && ext.chars().count() <= 3
        && stem.rsplit_once('~').is_some_and(|(head, n)| {
            !head.is_empty() && !n.is_empty() && n.bytes().all(|b| b.is_ascii_digit())
        })
}

/// Unpacks the update's `.tar.gz` gz into the empty folder into, refusing (with into left to its
/// caller to remove) anything but the set of the program exe: at its top the program and `pluxy.exe`
/// as plain files and `bundled` as a folder, each there, and below `bundled` folders and plain files
/// only, each once; relative paths without `..`; names Windows holds as they are (no device name, no
/// stream, no trailing dot or space, none of `\:<>"|?*` or control characters, none in the form
/// of a short name such as `PACKAG~1`) and no two that differ only in case; no setuid, setgid or
/// sticky bit; at most `MAX_ENTRIES` entries and
/// `MAX_BYTES` bytes.
pub(super) fn unpack(gz: &[u8], into: &Path, exe: &str) -> Result<(), String> {
    let refuse = |what: String| Err(format!("The update's archive is refused: {what}."));
    let unreadable = |e: io::Error| format!("The update's archive cannot be read: {e}");
    let mut archive = tar::Archive::new(flate2::read::GzDecoder::new(gz));
    let entries = archive
        .entries()
        .map_err(|e| format!("The update is not a .tar.gz archive: {e}"))?;
    let (mut count, mut total) = (0usize, 0u64);
    // Each path and every path on the way to it, by its Windows key: the first spelling seen.
    let mut spelled: HashMap<String, String> = HashMap::new();
    for entry in entries {
        let mut entry = entry.map_err(unreadable)?;
        count += 1;
        if count > MAX_ENTRIES {
            return refuse(format!("it holds more than {MAX_ENTRIES} entries"));
        }
        // A backslash is a separator on Windows and a name's character elsewhere: refused, so the
        // archive means the same everywhere.
        if entry.path_bytes().contains(&b'\\') {
            return refuse(format!(
                "{} has a backslash in it",
                String::from_utf8_lossy(&entry.path_bytes())
            ));
        }
        let path = entry.path().map_err(unreadable)?.into_owned();
        let mut names: Vec<String> = Vec::new();
        for c in path.components() {
            match c {
                Component::Normal(s) => match s.to_str() {
                    Some(s) if short_alias(s) => {
                        return refuse(format!(
                            "{s:?} has the form of a Windows short (8.3) name, which may be \
                             another entry's"
                        ))
                    }
                    Some(s) if !crate::vault::windows_refuses(s) => names.push(s.to_string()),
                    Some(s) => {
                        return refuse(format!("{s:?} is not a name Windows holds as it is"))
                    }
                    None => return refuse(format!("{} is not a UTF-8 name", path.display())),
                },
                Component::CurDir => {}
                _ => return refuse(format!("{} is not a plain relative path", path.display())),
            }
        }
        let kind = entry.header().entry_type();
        if names.is_empty() && kind.is_dir() {
            continue;
        }
        let shown = names.join("/");
        let top = names.first().map(String::as_str).unwrap_or_default();
        let at_top = names.len() == 1;
        let allowed = match top {
            BUNDLED => kind.is_dir() || !at_top,
            t if t == CORE || t == exe => at_top && kind.is_file(),
            _ => false,
        };
        if !allowed {
            return refuse(format!(
                "{shown} is not part of a program set ({exe}, {CORE} and the folder {BUNDLED})"
            ));
        }
        let mut prefix = String::new();
        for n in &names {
            if !prefix.is_empty() {
                prefix.push('/');
            }
            prefix.push_str(n);
            let first = spelled
                .entry(windows_name_key(&prefix))
                .or_insert_with(|| prefix.clone());
            if *first != prefix {
                return refuse(format!(
                    "{shown} differs only in case from {first}, the same name on Windows"
                ));
            }
        }
        let mode = entry.header().mode().map_err(unreadable)?;
        if mode & 0o7000 != 0 {
            return refuse(format!("{shown} has a setuid, setgid or sticky bit"));
        }
        let dest = names.iter().fold(into.to_path_buf(), |p, n| p.join(n));
        if kind.is_dir() {
            match fs::symlink_metadata(&dest) {
                Ok(m) if m.is_dir() => {}
                Ok(_) => return refuse(format!("{shown} is there twice")),
                Err(_) => {
                    fs::create_dir_all(&dest).map_err(|e| format!("{}: {e}", dest.display()))?
                }
            }
        } else if kind.is_file() {
            let size = entry.header().size().map_err(unreadable)?;
            total = total.saturating_add(size);
            if total > MAX_BYTES {
                return refuse(format!("it unpacks to more than {MAX_BYTES} bytes"));
            }
            if let Some(parent) = dest.parent() {
                fs::create_dir_all(parent).map_err(|e| format!("{}: {e}", parent.display()))?;
            }
            let mut w = fs::OpenOptions::new()
                .write(true)
                .create_new(true)
                .open(&dest)
                .map_err(|e| {
                    format!("The update's archive is refused: {shown} ({e}; is it there twice?).")
                })?;
            let n = io::copy(&mut (&mut entry).take(size), &mut w)
                .map_err(|e| format!("{}: {e}", dest.display()))?;
            if n != size {
                return refuse(format!("{shown} is cut short"));
            }
            w.flush().map_err(|e| e.to_string())?;
            w.sync_all().map_err(|e| e.to_string())?;
        } else {
            return refuse(format!(
                "{shown} is a link, a device or another kind of entry, not a folder or a plain file"
            ));
        }
    }
    for part in parts(exe) {
        if fs::symlink_metadata(into.join(part)).is_err() {
            return refuse(format!("it holds no {part}"));
        }
    }
    Ok(())
}

/// The move list of one replace of the set, in `pending.json`: the staging folder in the
/// program's folder that the new parts come from, and the parts, in the order they are moved.
/// A part's old copy is renamed aside to `.pluxy-old-<hex>-<part>`, hex being the staging
/// folder's.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Moves {
    pub(super) staging: String,
    pub(super) parts: Vec<String>,
}

impl Moves {
    /// The move list for the staging folder named staging (`.pluxy-update-<hex>`) and the
    /// program exe.
    pub(super) fn new(staging: &str, exe: &str) -> Moves {
        Moves {
            staging: staging.into(),
            parts: parts(exe).iter().map(|p| p.to_string()).collect(),
        }
    }

    /// Refused unless the staging folder is named `.pluxy-update-<16 hex digits>` and the parts
    /// are exactly the set's of the program exe, in their order: a list naming anything else is
    /// never acted on.
    pub(super) fn check(&self, exe: &str) -> Result<(), String> {
        let hex = self.staging.strip_prefix(STAGING).unwrap_or_default();
        let staging_ok = hex.len() == 16
            && hex
                .bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b));
        if !staging_ok {
            return Err(format!(
                "its staging folder {:?} is not one an update makes",
                self.staging
            ));
        }
        if self.parts != parts(exe) {
            return Err(format!(
                "its parts {:?} are not this program set's ({BUNDLED}, {CORE}, {exe})",
                self.parts
            ));
        }
        Ok(())
    }

    /// The name part is renamed aside to.
    pub(super) fn aside(&self, part: &str) -> String {
        format!(
            "{ASIDE}{}-{part}",
            self.staging.strip_prefix(STAGING).unwrap_or_default()
        )
    }
}

/// The file operations a replace makes, so a test can stop them where a crash would.
pub(super) trait Ops {
    /// Renames from to to, which must not exist.
    fn rename(&self, from: &Path, to: &Path) -> io::Result<()>;
    /// Records, in the updates folder, that what is at part now will be at aside: before it is
    /// renamed there, so whatever is left aside is recorded (`leftovers.rs`).
    fn record(&self, part: &Path, aside: &Path) -> Result<(), String>;
    /// Called once each part but the last is in place.
    fn between(&self, _part: &str) {}
}

fn there(p: &Path) -> bool {
    fs::symlink_metadata(p).is_ok()
}

fn renamed(ops: &dyn Ops, from: &Path, to: &Path) -> Result<(), String> {
    ops.rename(from, to)
        .map_err(|e| format!("renaming {} to {}: {e}", from.display(), to.display()))
}

/// Moves part's new copy in from the staging folder: the old one, if it is there, recorded and
/// renamed aside first.
fn move_in(ops: &dyn Ops, folder: &Path, m: &Moves, part: &str) -> Result<(), String> {
    let (at, aside) = (folder.join(part), folder.join(m.aside(part)));
    if there(&at) {
        if there(&aside) {
            return Err(format!(
                "{} and {} are both there",
                at.display(),
                aside.display()
            ));
        }
        ops.record(&at, &aside)?;
        renamed(ops, &at, &aside)?;
    }
    renamed(ops, &folder.join(&m.staging).join(part), &at)
}

/// Replaces the set in folder with the one in the staging folder, part by part in the move
/// list's order (the program last). If a part cannot be moved, with undo_from (the kept set of
/// the version replaced) the parts already moved are put back (`undo`); without it they are left
/// for the next start (a put-back, which that start finishes).
pub(super) fn apply(
    ops: &dyn Ops,
    folder: &Path,
    m: &Moves,
    undo_from: Option<&Path>,
) -> Result<(), String> {
    for (i, part) in m.parts.iter().enumerate() {
        if let Err(e) = move_in(ops, folder, m, part) {
            let Some(kept) = undo_from else {
                return Err(e);
            };
            return Err(match undo(ops, folder, m, kept) {
                Ok(()) => e,
                Err(u) => format!("{e}; and what was already moved could not all be put back: {u}"),
            });
        }
        if i + 1 < m.parts.len() {
            ops.between(part);
        }
    }
    Ok(())
}

/// Puts each part that was renamed aside back, in the reverse order, once its aside copy is
/// checked against the same part of the kept set at kept: the new copy at its name, if any, goes
/// back into the staging folder first. Stops at the first part it cannot put back.
pub(super) fn undo(ops: &dyn Ops, folder: &Path, m: &Moves, kept: &Path) -> Result<(), String> {
    for part in m.parts.iter().rev() {
        let (at, aside) = (folder.join(part), folder.join(m.aside(part)));
        if !there(&aside) {
            continue;
        }
        if part_digest(&aside, part)? != part_digest(&kept.join(part), part)? {
            return Err(format!(
                "{} is not the kept copy's {part}; it was not put back",
                aside.display()
            ));
        }
        if there(&at) {
            renamed(ops, &at, &folder.join(&m.staging).join(part))?;
        }
        renamed(ops, &aside, &at)?;
    }
    Ok(())
}

/// Finishes moving the kept set at kept in, from the staging folder, part by part in the move
/// list's order: a part still in the staging folder is checked against the kept set's and moved in;
/// one already moved is checked where it is.
pub(super) fn complete(ops: &dyn Ops, folder: &Path, m: &Moves, kept: &Path) -> Result<(), String> {
    for part in &m.parts {
        let want = part_digest(&kept.join(part), part)?;
        let new = folder.join(&m.staging).join(part);
        if there(&new) {
            if part_digest(&new, part)? != want {
                return Err(format!("{} is not the kept copy's {part}", new.display()));
            }
            move_in(ops, folder, m, part)?;
        } else if part_digest(&folder.join(part), part)? != want {
            return Err(format!(
                "{} is not the kept copy's {part}",
                folder.join(part).display()
            ));
        }
    }
    Ok(())
}

/// Whether any part of the move list is still aside in folder: until it is gone, the record of
/// that replace is needed.
pub(super) fn unresolved(folder: &Path, m: &Moves) -> bool {
    m.parts.iter().any(|p| there(&folder.join(m.aside(p))))
}

/// What a start found in `pending.json`.
#[derive(Debug, PartialEq, Eq)]
pub(super) enum Recovery {
    /// Nothing for this start: another install's record (left alone), or the new version's to
    /// accept.
    Nothing,
    /// A replace that stopped part way was undone, or a put-back finished; the record is gone.
    Undone,
    PutBack,
    /// A record of this install's that this start cannot finish: left as it is, with why.
    Left(String),
}

/// At the start of the program exe in folder here, running version running from the program
/// file at program, before its start digest is taken and before any cleanup: finishes what the
/// pending install p (in the updates folder dir) left part way, by its move list.
///
/// - Another install's record is left alone.
/// - An update or a rollback is undone when the version running is the one it came from, and the
///   running program is that version's (its kept set's program): the new version was never put
///   in place, so the earlier one undoes what was moved.
/// - A put-back is finished toward the kept set by either of its versions.
///
/// Every part put in place is checked against the kept set first; one that does not match stops
/// it, and the record stays.
pub(super) fn recover(
    ops: &dyn Ops,
    dir: &Path,
    p: &Pending,
    here: &Path,
    exe: &str,
    running: &str,
    program: &Path,
) -> Recovery {
    if p.installed != here {
        return Recovery::Nothing;
    }
    let Some(m) = &p.moves else {
        return Recovery::Left("it has no move list".into());
    };
    if let Err(e) = m.check(exe) {
        return Recovery::Left(e);
    }
    let kept_set = |pin: &super::Pinned| {
        let kept = super::kept_path(dir, pin);
        match digest(&kept, exe) {
            Ok(d) if d == pin.sha256 => Ok(kept),
            _ => Err(format!(
                "the kept copy {} does not have its pinned digest",
                kept.display()
            )),
        }
    };
    let outcome = match p.kind.as_str() {
        "update" | "rollback" => {
            if running != p.from.version {
                return Recovery::Nothing;
            }
            let kept = match kept_set(&p.from) {
                Ok(k) => k,
                Err(e) => return Recovery::Left(e),
            };
            let same =
                part_digest(program, exe).and_then(|d| Ok(d == part_digest(&kept.join(exe), exe)?));
            if same != Ok(true) {
                return Recovery::Left(format!(
                    "this program is not the one version {} was kept as",
                    p.from.version
                ));
            }
            undo(ops, here, m, &kept).map(|()| Recovery::Undone)
        }
        "put-back" => {
            if running != p.from.version && running != p.to.version {
                return Recovery::Nothing;
            }
            kept_set(&p.to)
                .and_then(|kept| complete(ops, here, m, &kept))
                .map(|()| Recovery::PutBack)
        }
        k => Err(format!("its kind {k:?} is not one this version knows")),
    };
    match outcome {
        Ok(r) => {
            let _ = fs::remove_file(dir.join("pending.json"));
            r
        }
        Err(e) => Recovery::Left(e),
    }
}

/// The paths cleanup must leave while the pending install p is not resolved: its staging folder
/// (the undo puts new parts back in it; a put-back moves parts in from it) and its asides.
pub(super) fn in_use(p: &Pending) -> Vec<std::path::PathBuf> {
    let Some(m) = &p.moves else {
        return vec![];
    };
    let mut v = vec![p.installed.join(&m.staging)];
    v.extend(m.parts.iter().map(|part| p.installed.join(m.aside(part))));
    v
}

#[cfg(test)]
pub(super) mod tests {
    use super::super::{tests::temp, Pinned};
    use super::*;
    use std::cell::Cell;
    use std::path::PathBuf;

    pub(crate) const EXE: &str = "pluxy-app-test.exe";

    /// Writes the set of version v into folder (which may already hold one): the program and
    /// the core with contents that say v, and `bundled` with a file and a folder in it.
    pub(crate) fn write_set(folder: &Path, v: &str, program: &[u8]) {
        for part in parts(EXE) {
            let p = folder.join(part);
            match fs::symlink_metadata(&p) {
                Ok(m) if m.is_dir() => fs::remove_dir_all(&p).unwrap(),
                Ok(_) => fs::remove_file(&p).unwrap(),
                Err(_) => {}
            }
        }
        fs::create_dir_all(folder.join("bundled/packages")).unwrap();
        fs::write(folder.join(EXE), program).unwrap();
        fs::write(folder.join(CORE), format!("core {v}")).unwrap();
        fs::write(folder.join("bundled/index.json"), format!("index {v}")).unwrap();
        fs::write(folder.join(format!("bundled/packages/p-{v}.tar.gz")), v).unwrap();
    }

    fn set(folder: &Path, v: &str) {
        write_set(folder, v, format!("program {v}").as_bytes());
    }

    /// An archive entry: path bytes, kind byte, mode, contents, link target.
    type Entry<'a> = (&'a [u8], u8, u32, &'a [u8], &'a str);

    pub(crate) fn targz(entries: &[Entry]) -> Vec<u8> {
        let mut out = Vec::new();
        {
            let gz = flate2::write::GzEncoder::new(&mut out, flate2::Compression::fast());
            let mut b = tar::Builder::new(gz);
            for (path, kind, mode, data, link) in entries {
                let mut h = tar::Header::new_ustar();
                h.as_old_mut().name[..path.len()].copy_from_slice(path);
                h.set_entry_type(tar::EntryType::new(*kind));
                h.set_mode(*mode);
                h.set_size(data.len() as u64);
                if !link.is_empty() {
                    h.set_link_name(link).unwrap();
                }
                h.set_cksum();
                b.append(&h, *data).unwrap();
            }
            b.into_inner().unwrap().finish().unwrap();
        }
        out
    }

    /// The archive of a good set whose program is program.
    pub(crate) fn payload(program: &[u8]) -> Vec<u8> {
        targz(&[
            (EXE.as_bytes(), b'0', 0o755, program, ""),
            (b"pluxy.exe", b'0', 0o755, b"core 0.2.0", ""),
            (b"bundled/", b'5', 0o755, b"", ""),
            (b"bundled/index.json", b'0', 0o644, b"index 0.2.0", ""),
        ])
    }

    // The digest is the one the definition gives (the native test computes it the same way): the
    // set's parts only, nothing else in the folder.
    #[test]
    fn the_set_digest_is_its_definition() {
        let tmp = temp("set-digest");
        set(&tmp, "0.1.0");
        let file = |rel: &str| {
            let b = fs::read(tmp.join(rel)).unwrap();
            format!("f {} {} {rel}\n", b.len(), sha256(&b))
        };
        let text = format!(
            "pluxy-set-v1\nd bundled\n{}d bundled/packages\n{}{}{}",
            file("bundled/index.json"),
            file("bundled/packages/p-0.1.0.tar.gz"),
            file(EXE),
            file(CORE),
        );
        let d = digest(&tmp, EXE).unwrap();
        assert_eq!(d, sha256(text.as_bytes()));
        // Something else in the folder is not part of it.
        fs::write(tmp.join("uninstall.exe"), b"x").unwrap();
        fs::create_dir(tmp.join(".pluxy-update-0123456789abcdef")).unwrap();
        assert_eq!(digest(&tmp, EXE).unwrap(), d);
        // A part's digest has its own lines only.
        let part = format!("pluxy-part-v1\n{}", file(CORE));
        assert_eq!(
            part_digest(&tmp.join(CORE), CORE).unwrap(),
            sha256(part.as_bytes())
        );
        // A change of any part, a missing part, or a part of the other kind changes it or has none.
        fs::write(tmp.join("bundled/new"), b"").unwrap();
        assert_ne!(digest(&tmp, EXE).unwrap(), d);
        fs::remove_file(tmp.join(CORE)).unwrap();
        assert!(digest(&tmp, EXE).is_err());
        fs::create_dir(tmp.join(CORE)).unwrap();
        assert!(digest(&tmp, EXE).is_err());
        fs::remove_dir_all(&tmp).unwrap();
    }

    #[cfg(unix)]
    #[test]
    fn a_link_in_a_set_has_no_digest() {
        let tmp = temp("set-link");
        set(&tmp, "0.1.0");
        std::os::unix::fs::symlink("/etc/passwd", tmp.join("bundled/l")).unwrap();
        assert!(digest(&tmp, EXE).is_err());
        fs::create_dir(tmp.join("x")).unwrap();
        let e = copy(&tmp, &tmp.join("x"), EXE).unwrap_err();
        assert!(e.contains("a link"), "{e}");
        fs::remove_dir_all(&tmp).unwrap();
    }

    // On Windows any reparse point is refused for what it is, also one that is not a link (a
    // tag of a program's own, here with no program to handle it): its attribute, not its kind.
    #[cfg(windows)]
    #[test]
    fn a_reparse_point_in_a_set_has_no_digest() {
        use std::os::windows::fs::OpenOptionsExt;
        use std::os::windows::io::AsRawHandle;
        use windows_sys::Win32::Storage::FileSystem::FILE_FLAG_OPEN_REPARSE_POINT;
        use windows_sys::Win32::System::IO::DeviceIoControl;
        const FSCTL_SET_REPARSE_POINT: u32 = 0x0009_00a4;
        const FSCTL_DELETE_REPARSE_POINT: u32 = 0x0009_00ac;
        let tmp = temp("set-reparse");
        set(&tmp, "0.1.0");
        let file = tmp.join("bundled/index.json");
        // A REPARSE_GUID_DATA_BUFFER without data: tag 0x1234 (not Microsoft's, not a name
        // surrogate), its length, a reserved field, then a GUID.
        let mut buf = [0u8; 24];
        buf[..4].copy_from_slice(&0x1234u32.to_le_bytes());
        buf[8..].copy_from_slice(b"pluxy-reparse-te");
        let control = |code: u32| {
            let f = fs::OpenOptions::new()
                .write(true)
                .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
                .open(&file)
                .unwrap();
            let mut returned = 0u32;
            // SAFETY: f is an open handle; buf is the input buffer of the length given; no
            // output buffer.
            let ok = unsafe {
                DeviceIoControl(
                    f.as_raw_handle(),
                    code,
                    buf.as_ptr().cast(),
                    buf.len() as u32,
                    std::ptr::null_mut(),
                    0,
                    &mut returned,
                    std::ptr::null_mut(),
                )
            };
            assert_ne!(ok, 0, "{code:#x}: {}", io::Error::last_os_error());
        };
        control(FSCTL_SET_REPARSE_POINT);
        let e = digest(&tmp, EXE).unwrap_err();
        assert!(e.contains("a link or another kind of file"), "{e}");
        control(FSCTL_DELETE_REPARSE_POINT);
        fs::remove_dir_all(&tmp).unwrap();
    }

    #[test]
    fn a_copied_set_has_the_same_digest() {
        let tmp = temp("set-copy");
        set(&tmp.join("a"), "0.1.0");
        fs::create_dir(tmp.join("b")).unwrap();
        copy(&tmp.join("a"), &tmp.join("b"), EXE).unwrap();
        assert_eq!(
            digest(&tmp.join("a"), EXE).unwrap(),
            digest(&tmp.join("b"), EXE).unwrap()
        );
        assert!(
            copy(&tmp.join("a"), &tmp.join("b"), EXE).is_err(),
            "nothing is copied over"
        );
        fs::remove_dir_all(&tmp).unwrap();
    }

    #[test]
    fn an_archive_of_the_set_unpacks() {
        let tmp = temp("set-unpack");
        unpack(&payload(b"new program"), &tmp, EXE).unwrap();
        assert_eq!(fs::read(tmp.join(EXE)).unwrap(), b"new program");
        assert_eq!(
            fs::read(tmp.join("bundled/index.json")).unwrap(),
            b"index 0.2.0"
        );
        digest(&tmp, EXE).unwrap();
        fs::remove_dir_all(&tmp).unwrap();
    }

    #[test]
    fn an_archive_breaking_a_rule_is_refused() {
        let program: Entry<'static> = (EXE.as_bytes(), b'0', 0o755, b"p", "");
        let core: Entry<'static> = (b"pluxy.exe", b'0', 0o755, b"c", "");
        let bundled: Entry<'static> = (b"bundled/", b'5', 0o755, b"", "");
        let set = [program, core, bundled];
        let with = |more: &[Entry<'static>]| -> Vec<Entry<'static>> {
            set.iter().chain(more).cloned().collect()
        };
        let cases: Vec<(&str, Vec<Entry<'static>>, &str)> = vec![
            ("no core", vec![program, bundled], "holds no pluxy.exe"),
            (
                "no program",
                vec![core, bundled],
                "holds no pluxy-app-test.exe",
            ),
            ("no bundled", vec![program, core], "holds no bundled"),
            (
                "another program",
                with(&[(b"other.exe", b'0', 0o755, b"o", "")]),
                "not part of a program set",
            ),
            (
                "uninstall.exe",
                with(&[(b"uninstall.exe", b'0', 0o755, b"o", "")]),
                "not part of a program set",
            ),
            (
                "a top folder",
                vec![(b"pluxy Test/pluxy.exe", b'0', 0o755, b"c", "")],
                "not part of a program set",
            ),
            (
                "the core as a folder",
                vec![program, (b"pluxy.exe/", b'5', 0o755, b"", ""), bundled],
                "not part of a program set",
            ),
            (
                "bundled as a file",
                vec![program, core, (b"bundled", b'0', 0o644, b"x", "")],
                "not part of a program set",
            ),
            (
                "a link",
                with(&[(b"bundled/l", b'2', 0o777, b"", "/etc/passwd")]),
                "a link",
            ),
            (
                "a hard link",
                with(&[(b"bundled/h", b'1', 0o644, b"", "pluxy.exe")]),
                "a link",
            ),
            (
                "a device",
                with(&[(b"bundled/d", b'3', 0o644, b"", "")]),
                "a link",
            ),
            (
                "dot-dot",
                with(&[(b"bundled/../../evil", b'0', 0o644, b"e", "")]),
                "plain relative path",
            ),
            (
                "absolute",
                vec![(b"/tmp/evil", b'0', 0o644, b"e", "")],
                "plain relative path",
            ),
            (
                "setuid",
                with(&[(b"bundled/s", b'0', 0o4755, b"e", "")]),
                "setuid",
            ),
            (
                "setgid",
                with(&[(b"bundled/s", b'0', 0o2755, b"e", "")]),
                "setgid",
            ),
            (
                "sticky",
                with(&[(b"bundled/s/", b'5', 0o1755, b"", "")]),
                "sticky",
            ),
            (
                "an 8.3 name",
                with(&[(b"bundled/PACKAG~1/x", b'0', 0o644, b"e", "")]),
                "short (8.3) name",
            ),
            (
                "a device name",
                with(&[(b"bundled/CON.txt", b'0', 0o644, b"e", "")]),
                "not a name Windows holds",
            ),
            (
                "a stream",
                with(&[(b"bundled/a:b", b'0', 0o644, b"e", "")]),
                "not a name Windows holds",
            ),
            (
                "a trailing dot",
                with(&[(b"bundled/a.", b'0', 0o644, b"e", "")]),
                "not a name Windows holds",
            ),
            (
                "a backslash",
                with(&[(b"bundled/a\\b", b'0', 0o644, b"e", "")]),
                "backslash",
            ),
            (
                "case twins",
                with(&[
                    (b"bundled/a.txt", b'0', 0o644, b"1", ""),
                    (b"bundled/A.txt", b'0', 0o644, b"2", ""),
                ]),
                "differs only in case",
            ),
            (
                "case twins on the way",
                with(&[
                    (b"bundled/x/a", b'0', 0o644, b"1", ""),
                    (b"bundled/X/b", b'0', 0o644, b"2", ""),
                ]),
                "differs only in case",
            ),
            (
                "sigma twins",
                with(&[
                    ("bundled/\u{3c3}".as_bytes(), b'0', 0o644, b"1", ""),
                    ("bundled/\u{3c2}".as_bytes(), b'0', 0o644, b"2", ""),
                ]),
                "differs only in case",
            ),
            ("twice", with(&[core]), "there twice"),
            ("empty", vec![], "holds no"),
        ];
        for (what, entries, want) in cases {
            let tmp = temp(&format!("set-refuse-{}", what.replace(' ', "-")));
            let e = unpack(&targz(&entries), &tmp, EXE).unwrap_err();
            assert!(e.contains(want), "{what}: {e}");
            assert!(!Path::new("/tmp/evil").exists(), "{what}");
            assert!(!tmp.parent().unwrap().join("evil").exists(), "{what}");
            fs::remove_dir_all(&tmp).unwrap();
        }
        let tmp = temp("set-refuse-many");
        let many: Vec<Entry> = (0..MAX_ENTRIES).map(|_| bundled).chain(set).collect();
        assert!(unpack(&targz(&many), &tmp, EXE)
            .unwrap_err()
            .contains("more than"));
        assert!(unpack(b"not gzip", &tmp, EXE).is_err());
        fs::remove_dir_all(&tmp).unwrap();
    }

    #[test]
    fn a_move_list_naming_anything_but_the_set_is_refused() {
        let ok = Moves::new(".pluxy-update-0123456789abcdef", EXE);
        ok.check(EXE).unwrap();
        assert_eq!(ok.aside(CORE), ".pluxy-old-0123456789abcdef-pluxy.exe");
        let bad = [
            Moves {
                parts: vec![BUNDLED.into(), CORE.into(), "uninstall.exe".into()],
                ..ok.clone()
            },
            Moves {
                parts: vec![BUNDLED.into(), CORE.into(), "..\\..\\x".into()],
                ..ok.clone()
            },
            Moves {
                parts: vec![EXE.into(), CORE.into(), BUNDLED.into()],
                ..ok.clone()
            },
            Moves {
                parts: vec![BUNDLED.into(), CORE.into()],
                ..ok.clone()
            },
            Moves {
                staging: "..".into(),
                ..ok.clone()
            },
            Moves {
                staging: ".pluxy-update-0123456789ABCDEF".into(),
                ..ok.clone()
            },
            Moves {
                staging: ".pluxy-update-0123456789abcdef\\..".into(),
                ..ok.clone()
            },
        ];
        for m in bad {
            assert!(m.check(EXE).is_err(), "{m:?}");
        }
        assert!(ok.check("other.exe").is_err(), "another program's set");
    }

    /// File operations that stop after `left` renames, as a crash would: from then on every
    /// operation fails and nothing more changes on disk. fail_at makes that one rename fail
    /// (a file in use) without stopping the rest.
    pub(crate) struct Stop {
        left: Cell<Option<usize>>,
        fail_at: Option<usize>,
        done: Cell<usize>,
        pub(crate) recorded: std::cell::RefCell<Vec<PathBuf>>,
    }

    impl Stop {
        pub(crate) fn after(n: usize) -> Stop {
            Stop {
                left: Cell::new(Some(n)),
                fail_at: None,
                done: Cell::new(0),
                recorded: Default::default(),
            }
        }
        pub(crate) fn never() -> Stop {
            Stop {
                left: Cell::new(None),
                fail_at: None,
                done: Cell::new(0),
                recorded: Default::default(),
            }
        }
        fn failing(n: usize) -> Stop {
            Stop {
                fail_at: Some(n),
                ..Stop::never()
            }
        }
    }

    impl Ops for Stop {
        fn rename(&self, from: &Path, to: &Path) -> io::Result<()> {
            if self.left.get() == Some(0) {
                return Err(io::Error::other("stopped"));
            }
            let n = self.done.get();
            self.done.set(n + 1);
            if self.fail_at == Some(n) {
                return Err(io::Error::other("in use"));
            }
            self.left.set(self.left.get().map(|l| l - 1));
            real_rename(from, to)
        }
        fn record(&self, _part: &Path, aside: &Path) -> Result<(), String> {
            if self.left.get() == Some(0) {
                return Err("stopped".into());
            }
            self.recorded.borrow_mut().push(aside.to_path_buf());
            Ok(())
        }
    }

    /// The system's own rename where it may not replace: on Windows the updater's.
    fn real_rename(from: &Path, to: &Path) -> io::Result<()> {
        #[cfg(windows)]
        return super::super::exe::rename(from, to);
        #[cfg(not(windows))]
        {
            if there(to) {
                return Err(io::ErrorKind::AlreadyExists.into());
            }
            fs::rename(from, to)
        }
    }

    /// An install of the set of version "old" in `<tmp>/app`, kept in `<tmp>/updates`, with the set
    /// of "new" staged and a pending record of kind for it; returns the folder, the updates folder
    /// and the record.
    fn staged(tmp: &Path, kind: &str) -> (PathBuf, PathBuf, Pending) {
        let (folder, dir) = (tmp.join("app"), tmp.join("updates"));
        set(&folder, "old");
        let from = Pinned {
            version: "0.1.0".into(),
            sha256: digest(&folder, EXE).unwrap(),
        };
        let kept = super::super::kept_path(&dir, &from);
        fs::create_dir_all(&kept).unwrap();
        copy(&folder, &kept, EXE).unwrap();
        let m = Moves::new(".pluxy-update-00112233aabbccdd", EXE);
        set(&folder.join(&m.staging), "new");
        let to = Pinned {
            version: "0.2.0".into(),
            sha256: digest(&folder.join(&m.staging), EXE).unwrap(),
        };
        let p = Pending {
            format: 1,
            kind: kind.into(),
            from,
            to,
            installed: folder.clone(),
            nonce: "n".into(),
            moves: Some(m),
        };
        super::super::write_json(&dir.join("pending.json"), &p).unwrap();
        (folder, dir, p)
    }

    /// The new version in place after an update (its asides beside it) and the kept set of the
    /// earlier one staged again in the staging folder `.pluxy-update-<hex>` for a put-back, whose
    /// record is written; returns the folder, the updates folder, the update's record and the
    /// put-back's.
    fn putting_back(tmp: &Path, hex: &str) -> (PathBuf, PathBuf, Pending, Pending) {
        let (folder, dir, update) = staged(tmp, "update");
        apply(
            &Stop::never(),
            &folder,
            update.moves.as_ref().unwrap(),
            None,
        )
        .unwrap();
        let m = Moves::new(&format!("{STAGING}{hex}"), EXE);
        let kept = super::super::kept_path(&dir, &update.from);
        fs::create_dir(folder.join(&m.staging)).unwrap();
        copy(&kept, &folder.join(&m.staging), EXE).unwrap();
        let p = Pending {
            kind: "put-back".into(),
            from: update.to.clone(),
            to: update.from.clone(),
            moves: Some(m),
            ..update.clone()
        };
        super::super::write_json(&dir.join("pending.json"), &p).unwrap();
        (folder, dir, update, p)
    }

    fn names(folder: &Path) -> Vec<String> {
        let mut v: Vec<String> = fs::read_dir(folder)
            .unwrap()
            .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
            .collect();
        v.sort();
        v
    }

    // The five renames of steps 4 and 5 move every part, core parts first and the program last,
    // each old copy recorded before it is renamed aside.
    #[test]
    fn a_replace_moves_each_part_aside_and_the_new_one_in() {
        let tmp = temp("set-apply");
        let (folder, _, p) = staged(&tmp, "update");
        let m = p.moves.as_ref().unwrap();
        let ops = Stop::never();
        apply(&ops, &folder, m, None).unwrap();
        assert_eq!(digest(&folder, EXE).unwrap(), p.to.sha256);
        let asides: Vec<PathBuf> = parts(EXE).iter().map(|x| folder.join(m.aside(x))).collect();
        assert_eq!(*ops.recorded.borrow(), asides);
        assert!(names(&folder.join(&m.staging)).is_empty());
        assert!(unresolved(&folder, m));
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A crash at any point of step 4 (and before the program's first rename) leaves the earlier
    // version's program in place; its next start undoes what was moved, checking each aside copy
    // against the kept set, and drops the record: the earlier set is whole again.
    #[test]
    fn a_crash_part_way_is_undone_at_the_next_start() {
        // Renames before the program's: two per core part. A stop after 5 is between the
        // program's own two (no program under its name: the README's case).
        for n in 0..=4 {
            let tmp = temp(&format!("set-crash-{n}"));
            let (folder, dir, p) = staged(&tmp, "update");
            let m = p.moves.as_ref().unwrap();
            let e = apply(
                &Stop::after(n),
                &folder,
                m,
                Some(&super::super::kept_path(&dir, &p.from)),
            );
            assert!(e.is_err(), "{n}");
            let r = recover(
                &Stop::never(),
                &dir,
                &p,
                &folder,
                EXE,
                "0.1.0",
                &folder.join(EXE),
            );
            assert_eq!(r, Recovery::Undone, "{n}");
            assert_eq!(digest(&folder, EXE).unwrap(), p.from.sha256, "{n}");
            assert!(!dir.join("pending.json").exists(), "{n}");
            assert!(!unresolved(&folder, m), "{n}");
            // What was moved in went back into the staging folder, for cleanup to remove.
            assert_eq!(
                digest(&folder.join(&m.staging), EXE).unwrap(),
                p.to.sha256,
                "{n}"
            );
            fs::remove_dir_all(&tmp).unwrap();
        }
    }

    // A crash between undoing two parts at a start is undone at the start after.
    #[test]
    fn a_crash_while_undoing_is_undone_at_the_start_after() {
        for n in 0..=3 {
            let tmp = temp(&format!("set-crash-undo-{n}"));
            let (folder, dir, p) = staged(&tmp, "update");
            let m = p.moves.as_ref().unwrap();
            let _ = apply(&Stop::after(4), &folder, m, None);
            let r = recover(
                &Stop::after(n),
                &dir,
                &p,
                &folder,
                EXE,
                "0.1.0",
                &folder.join(EXE),
            );
            assert!(matches!(r, Recovery::Left(_)), "{n}: {r:?}");
            assert!(dir.join("pending.json").exists(), "{n}");
            let r = recover(
                &Stop::never(),
                &dir,
                &p,
                &folder,
                EXE,
                "0.1.0",
                &folder.join(EXE),
            );
            assert_eq!(r, Recovery::Undone, "{n}");
            assert_eq!(digest(&folder, EXE).unwrap(), p.from.sha256, "{n}");
            fs::remove_dir_all(&tmp).unwrap();
        }
    }

    // A part that cannot be moved (in use past the wait) undoes the parts already moved, there
    // and then; the record is then not needed.
    #[test]
    fn a_part_that_cannot_be_moved_undoes_the_others() {
        for n in 0..=5 {
            let tmp = temp(&format!("set-busy-{n}"));
            let (folder, dir, p) = staged(&tmp, "update");
            let m = p.moves.as_ref().unwrap();
            let kept = super::super::kept_path(&dir, &p.from);
            assert!(
                apply(&Stop::failing(n), &folder, m, Some(&kept)).is_err(),
                "{n}"
            );
            assert_eq!(digest(&folder, EXE).unwrap(), p.from.sha256, "{n}");
            assert!(!unresolved(&folder, m), "{n}");
            fs::remove_dir_all(&tmp).unwrap();
        }
    }

    // The undo runs only for the version the install came from, started from that version's own
    // program: not for the new version (its acceptance is #372's), and not for another program
    // of the same version number.
    #[test]
    fn only_the_earlier_versions_own_program_undoes() {
        let tmp = temp("set-crash-who");
        let (folder, dir, p) = staged(&tmp, "update");
        let m = p.moves.as_ref().unwrap();
        let _ = apply(&Stop::after(2), &folder, m, None);
        let mixed = digest(&folder, EXE).unwrap();
        let r = recover(
            &Stop::never(),
            &dir,
            &p,
            &folder,
            EXE,
            "0.2.0",
            &folder.join(EXE),
        );
        assert_eq!(r, Recovery::Nothing);
        let other = tmp.join("other.exe");
        fs::write(&other, b"program 0.1.0, another build").unwrap();
        let r = recover(&Stop::never(), &dir, &p, &folder, EXE, "0.1.0", &other);
        assert!(matches!(r, Recovery::Left(_)), "{r:?}");
        assert_eq!(digest(&folder, EXE).unwrap(), mixed, "nothing moved");
        assert!(dir.join("pending.json").exists());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // An aside copy that is no longer the kept set's part is not put back: the undo stops there
    // and the record stays.
    #[test]
    fn a_changed_aside_is_not_put_back() {
        let tmp = temp("set-crash-changed");
        let (folder, dir, p) = staged(&tmp, "update");
        let m = p.moves.as_ref().unwrap();
        let _ = apply(&Stop::after(4), &folder, m, None);
        let aside = folder.join(m.aside(CORE));
        fs::write(&aside, b"someone else's").unwrap();
        let r = recover(
            &Stop::never(),
            &dir,
            &p,
            &folder,
            EXE,
            "0.1.0",
            &folder.join(EXE),
        );
        assert!(
            matches!(&r, Recovery::Left(e) if e.contains("not the kept copy")),
            "{r:?}"
        );
        assert_eq!(fs::read(&aside).unwrap(), b"someone else's");
        assert_eq!(fs::read(folder.join(CORE)).unwrap(), b"core new");
        assert!(dir.join("pending.json").exists());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // Another install's record (two installs of one identifier share the updates folder) is left
    // alone, its parts aside too; so is one naming a part outside the set.
    #[test]
    fn another_installs_record_or_a_bad_one_is_left_alone() {
        let tmp = temp("set-other");
        let (folder, dir, p) = staged(&tmp, "update");
        let m = p.moves.clone().unwrap();
        let _ = apply(&Stop::after(2), &folder, &m, None);
        let mixed = digest(&folder, EXE).unwrap();
        let elsewhere = tmp.join("elsewhere");
        fs::create_dir(&elsewhere).unwrap();
        let r = recover(
            &Stop::never(),
            &dir,
            &p,
            &elsewhere,
            EXE,
            "0.1.0",
            &folder.join(EXE),
        );
        assert_eq!(r, Recovery::Nothing);
        for bad in [
            Moves {
                parts: vec![BUNDLED.into(), CORE.into(), "uninstall.exe".into()],
                ..m.clone()
            },
            Moves {
                staging: "..".into(),
                ..m.clone()
            },
        ] {
            let q = Pending {
                moves: Some(bad),
                ..p.clone()
            };
            let r = recover(
                &Stop::never(),
                &dir,
                &q,
                &folder,
                EXE,
                "0.1.0",
                &folder.join(EXE),
            );
            assert!(matches!(r, Recovery::Left(_)), "{r:?}");
        }
        let q = Pending {
            kind: "something".into(),
            ..p.clone()
        };
        assert!(matches!(
            recover(
                &Stop::never(),
                &dir,
                &q,
                &folder,
                EXE,
                "0.1.0",
                &folder.join(EXE)
            ),
            Recovery::Left(_)
        ));
        assert_eq!(digest(&folder, EXE).unwrap(), mixed);
        assert!(unresolved(&folder, &m));
        assert!(dir.join("pending.json").exists());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A put-back of the kept set after a new version did not start that stops part way is
    // finished at the next start, by whichever version's program is in place, each part checked
    // against the kept set.
    #[test]
    fn a_put_back_that_stops_part_way_is_finished() {
        for n in 0..=6 {
            let tmp = temp(&format!("set-putback-{n}"));
            // The new version is in place, the earlier one kept; the kept set is staged again.
            let (folder, dir, update, p) = putting_back(&tmp, "44556677eeff0011");
            let m = p.moves.clone().unwrap();
            let r = apply(&Stop::after(n), &folder, &m, None);
            assert_eq!(r.is_ok(), n == 6, "{n}");
            if n == 5 {
                // Between the program's two renames: nothing can start (the README's case).
                assert!(!folder.join(EXE).exists());
                fs::remove_dir_all(&tmp).unwrap();
                continue;
            }
            let running = if n < 5 { "0.2.0" } else { "0.1.0" };
            let r = recover(
                &Stop::never(),
                &dir,
                &p,
                &folder,
                EXE,
                running,
                &folder.join(EXE),
            );
            assert_eq!(r, Recovery::PutBack, "{n}");
            assert_eq!(digest(&folder, EXE).unwrap(), update.from.sha256, "{n}");
            assert!(!dir.join("pending.json").exists(), "{n}");
            fs::remove_dir_all(&tmp).unwrap();
        }
    }

    // A put-back finishes only with the kept set's parts: one in the staging folder that is not
    // the kept set's is not moved in, and the record stays.
    #[test]
    fn a_put_back_part_that_is_not_the_kept_ones_is_not_moved_in() {
        let tmp = temp("set-putback-changed");
        let (folder, dir, _, p) = putting_back(&tmp, "8899aabbccddeeff");
        let m = p.moves.clone().unwrap();
        fs::write(folder.join(&m.staging).join(CORE), b"someone else's").unwrap();
        let r = recover(
            &Stop::never(),
            &dir,
            &p,
            &folder,
            EXE,
            "0.2.0",
            &folder.join(EXE),
        );
        assert!(
            matches!(&r, Recovery::Left(e) if e.contains("not the kept copy")),
            "{r:?}"
        );
        assert_eq!(fs::read(folder.join(CORE)).unwrap(), b"core new");
        assert!(dir.join("pending.json").exists());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // What cleanup leaves while a record is unresolved: its staging folder and asides.
    #[test]
    fn an_unresolved_record_keeps_its_staging_and_asides() {
        let tmp = temp("set-in-use");
        let (folder, _, p) = staged(&tmp, "update");
        let m = p.moves.as_ref().unwrap();
        let v = in_use(&p);
        assert_eq!(v[0], folder.join(&m.staging));
        for part in parts(EXE) {
            assert!(v.contains(&folder.join(m.aside(part))), "{part}");
        }
        fs::remove_dir_all(&tmp).unwrap();
    }
    // The form of a short name Windows may give a longer one, and names that only look near it.
    #[test]
    fn only_a_name_in_the_form_of_a_short_name_is_one() {
        for name in ["PACKAG~1", "packa~12.tar", "A~1", "AB~123.GZ", "x~0"] {
            assert!(short_alias(name), "{name}");
        }
        for name in [
            "packages-long",
            "~1",
            "notes~",
            "a~b1",
            "longer-name~1",
            "PACKAG~1.long",
            "v~1.2.3-long",
            "index.json",
            "packag~12.tar",
        ] {
            assert!(!short_alias(name), "{name}");
        }
    }

    // A put-back finishes only over parts that are the kept set's: one already moved in that has
    // changed since stops it there, and the parts after it are not moved.
    #[test]
    fn a_put_back_part_already_in_place_that_changed_stops_it() {
        let tmp = temp("set-putback-in-place");
        let (folder, dir, _, p) = putting_back(&tmp, "0011223344556677");
        let m = p.moves.clone().unwrap();
        // bundled\ is moved in, then the put-back stops; then a file in it changes.
        assert!(apply(&Stop::after(2), &folder, &m, None).is_err());
        fs::write(folder.join("bundled/index.json"), b"changed").unwrap();
        let r = recover(
            &Stop::never(),
            &dir,
            &p,
            &folder,
            EXE,
            "0.2.0",
            &folder.join(EXE),
        );
        assert!(
            matches!(&r, Recovery::Left(e) if e.contains("not the kept copy's bundled")),
            "{r:?}"
        );
        assert_eq!(fs::read(folder.join(CORE)).unwrap(), b"core new");
        assert!(dir.join("pending.json").exists());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A put-back is finished only by one of its two versions: another one leaves it.
    #[test]
    fn a_put_back_is_left_to_its_own_versions() {
        let tmp = temp("set-putback-other");
        let (folder, dir, _, p) = putting_back(&tmp, "8877665544332211");
        let m = p.moves.clone().unwrap();
        assert!(apply(&Stop::after(2), &folder, &m, None).is_err());
        let mixed = digest(&folder, EXE).unwrap();
        let r = recover(
            &Stop::never(),
            &dir,
            &p,
            &folder,
            EXE,
            "0.3.0",
            &folder.join(EXE),
        );
        assert_eq!(r, Recovery::Nothing);
        assert_eq!(digest(&folder, EXE).unwrap(), mixed, "nothing moved");
        assert!(dir.join("pending.json").exists());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // The kept set an undo puts back from must still have its pin: one changed since (here a part
    // the undo would not compare) leaves the record as it is.
    #[test]
    fn a_kept_set_that_changed_undoes_nothing() {
        let tmp = temp("set-kept-changed");
        let (folder, dir, p) = staged(&tmp, "update");
        let m = p.moves.as_ref().unwrap();
        let _ = apply(&Stop::after(2), &folder, m, None);
        let mixed = digest(&folder, EXE).unwrap();
        let kept = super::super::kept_path(&dir, &p.from);
        fs::write(kept.join(CORE), b"core changed").unwrap();
        let r = recover(
            &Stop::never(),
            &dir,
            &p,
            &folder,
            EXE,
            "0.1.0",
            &folder.join(EXE),
        );
        assert!(
            matches!(&r, Recovery::Left(e) if e.contains("pinned digest")),
            "{r:?}"
        );
        assert_eq!(digest(&folder, EXE).unwrap(), mixed, "nothing moved");
        assert!(dir.join("pending.json").exists());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A new update or rollback is refused while the record names another install, or this one
    // with parts aside (or with no move list to put them back by); a resolved one of this
    // install's, or none, lets it go on.
    #[test]
    fn an_unfinished_record_refuses_a_new_one() {
        let tmp = temp("set-open-record");
        let (folder, dir, p) = staged(&tmp, "update");
        let m = p.moves.clone().unwrap();
        let open = || super::super::open_record(&dir, &folder);
        // The record `staged` wrote: this install's, nothing aside.
        open().unwrap();
        let elsewhere = Pending {
            installed: tmp.join("elsewhere"),
            ..p.clone()
        };
        super::super::write_json(&dir.join("pending.json"), &elsewhere).unwrap();
        assert!(open().unwrap_err().contains("Another copy of pluxy"));
        let no_list = Pending {
            moves: None,
            ..p.clone()
        };
        super::super::write_json(&dir.join("pending.json"), &no_list).unwrap();
        assert!(open().unwrap_err().contains("left parts"));
        super::super::write_json(&dir.join("pending.json"), &p).unwrap();
        let _ = apply(&Stop::after(2), &folder, &m, None);
        assert!(open().unwrap_err().contains("left parts"));
        fs::remove_file(dir.join("pending.json")).unwrap();
        open().unwrap();
        fs::remove_dir_all(&tmp).unwrap();
    }

    // After a replace that failed, the record stays only while parts it moved are aside.
    #[test]
    fn a_failed_replace_keeps_its_record_only_while_parts_are_aside() {
        let tmp = temp("set-failed");
        let (folder, dir, p) = staged(&tmp, "update");
        let m = p.moves.clone().unwrap();
        let record = dir.join("pending.json");
        super::super::failed(&dir, &p);
        assert!(!record.exists(), "nothing aside");
        super::super::write_json(&record, &p).unwrap();
        let _ = apply(&Stop::after(2), &folder, &m, None);
        super::super::failed(&dir, &p);
        assert!(record.exists(), "a part aside");
        let no_list = Pending {
            moves: None,
            ..p.clone()
        };
        super::super::failed(&dir, &no_list);
        assert!(!record.exists(), "no move list (Linux, macOS)");
        fs::remove_dir_all(&tmp).unwrap();
    }

    // What a start records after its recovery, and when it installs nothing until a restart.
    #[test]
    fn a_start_says_what_its_recovery_left() {
        let tmp = temp("set-recovered");
        let (folder, _, update) = staged(&tmp, "update");
        let say = |r: &Recovery, p: &Pending, running: &str| {
            super::super::recovered(r, p, running, &folder)
        };
        assert_eq!(say(&Recovery::Nothing, &update, "0.1.0"), (None, None));
        assert_eq!(say(&Recovery::Undone, &update, "0.1.0"), (None, None));
        let (last, held) = say(&Recovery::Left("a part changed".into()), &update, "0.1.0");
        let last = last.unwrap();
        assert!(
            last.contains("a part changed") && last.contains("still aside"),
            "{last}"
        );
        assert!(held.unwrap().contains("a part changed"));
        let put_back = Pending {
            kind: "put-back".into(),
            from: update.to.clone(),
            to: update.from.clone(),
            ..update.clone()
        };
        // Finished by the version put back: it runs on; by the one that did not start: that one
        // installs nothing until the app restarts into the version put back.
        let (last, held) = say(&Recovery::PutBack, &put_back, "0.1.0");
        assert!(last.unwrap().contains("0.1.0 was put back"));
        assert_eq!(held, None);
        let (last, held) = say(&Recovery::PutBack, &put_back, "0.2.0");
        assert!(last.is_some());
        assert!(held.unwrap().contains("restart the app"));
        fs::remove_dir_all(&tmp).unwrap();
    }
}
