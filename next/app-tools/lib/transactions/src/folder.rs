//! A folder a tool owns inside a selected root (such as a plugin it carries), known by a marker
//! file in it: put in place, replaced and removed through a handle on the root, never by a path
//! again, each change under a journal, so the next start's [`Transactions::reconcile`] puts back a
//! folder a stop left moved aside and removes a new copy a stop left unfinished.
//!
//! [`Folder::pinned`] opens the folder a preview pinned (a pin of its marker, selected inside the
//! root) and [`Folder::empty`] the place for a first copy. [`Transactions::put_folder`] writes a new
//! copy beside the folder, moves the old one aside, checks that what moved is the folder pinned
//! (its identity, and its marker's identity and bytes), renames the new copy in and removes the old
//! one. [`Transactions::remove_folder`] moves the folder aside, checks it the same way, removes its
//! marker (from then on the removal is finished at the next start, never undone) and then the
//! rest. A folder that is not the one pinned goes back under its name and the operation is
//! refused; when it cannot go back, the error names where it is, and the next start puts it back.
//!
//! Every step goes through the handle on the root opened when the folder was: a link to the root
//! pointed elsewhere meanwhile changes nothing, the operation stays in the folder pinned. On Linux
//! and macOS a folder is renamed by its name in the held root, its identity checked just before
//! and what moved checked after; on Windows the folder is opened and checked first and renamed
//! through its own handle. Files are removed by name in the held folder on Linux and macOS; on
//! Windows cap-std removes them by the held folder's path as it is at that moment. Other programs
//! do not share these checks: something put under the folder's name between the last check and a
//! rename is not seen.

use crate::target::{
    changed, dir_id, io, open_dir, open_root, random_hex, sha256, sync_dir, Id, Pin, Selection,
    Target, MAX_FILE,
};
use crate::{check_set, fields, Transactions, KIND};
use cap_std::fs::Dir;
use pluxy_program::Error;
use serde_json::{json, Value};
use std::io::{ErrorKind, Read, Write};
use std::path::{Path, PathBuf};

/// The journal's op of a folder written in place.
const PUT: &str = "put-folder";
/// The journal's op of a folder removed.
const REMOVE: &str = "remove-folder";
const ROLLED_BACK: &str = "rolled_back";
const COMMITTED: &str = "committed";

/// The folder a preview pinned, as its pin holds it: the folder's identity, and its marker's
/// identity and SHA-256.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Owned {
    folder: Id,
    marker: Id,
    sha256: String,
}

impl Owned {
    fn to_json(&self) -> Value {
        json!({"folder": self.folder.to_json(), "marker": self.marker.to_json(), "sha256": self.sha256})
    }

    fn from_json(v: &Value) -> Result<Option<Owned>, Error> {
        if v.is_null() {
            return Ok(None);
        }
        let bad =
            || Error::internal("a folder journal names an old copy this library did not write");
        Ok(Some(Owned {
            folder: Id::from_json(&v["folder"])?.ok_or_else(bad)?,
            marker: Id::from_json(&v["marker"])?.ok_or_else(bad)?,
            sha256: v["sha256"].as_str().ok_or_else(bad)?.to_string(),
        }))
    }
}

/// A folder name in a root folder held open: the one a preview pinned, or the empty place for a
/// first copy.
pub struct Folder {
    dir: Dir,
    root: PathBuf,
    root_id: Id,
    name: String,
    marker: String,
    old: Option<Owned>,
}

/// One part of a path: no separator, no `.` or `..`, and on Windows a name it takes as itself.
fn plain(name: &str) -> Result<(), Error> {
    let ok = !name.is_empty()
        && name != "."
        && name != ".."
        && !name.contains(['/', '\\', '\0'])
        && (cfg!(not(windows)) || pluxy_files::windows_name_problem(name).is_none());
    if ok {
        Ok(())
    } else {
        Err(Error::invalid_request(format!(
            "{name:?} is not one plain name in a folder"
        )))
    }
}

/// What is at name in d, not following a link: whether it is a folder (not a link) and its
/// identity; None when nothing is there.
fn entry(d: &Dir, name: &str) -> Result<Option<(bool, Id)>, Error> {
    match d.symlink_metadata(name) {
        Ok(m) => Ok(Some((
            m.is_dir() && !m.file_type().is_symlink(),
            Id::of(&m)?,
        ))),
        Err(e) if e.kind() == ErrorKind::NotFound => Ok(None),
        Err(e) => Err(io(name, e)),
    }
}

/// The marker file in d: its identity and bytes; None when it is missing, a link or not a
/// regular file.
fn read_marker(d: &Dir, marker: &str) -> Result<Option<(Id, Vec<u8>)>, Error> {
    let m = match d.symlink_metadata(marker) {
        Ok(m) => m,
        Err(e) if e.kind() == ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(io(marker, e)),
    };
    if m.file_type().is_symlink() || !m.is_file() {
        return Ok(None);
    }
    let mut f = d.open(marker).map_err(|e| io(marker, e))?;
    let fm = f.metadata().map_err(|e| io(marker, e))?;
    if Id::of(&fm)? != Id::of(&m)? {
        return Ok(None);
    }
    let mut bytes = Vec::new();
    (&mut f)
        .take(MAX_FILE as u64 + 1)
        .read_to_end(&mut bytes)
        .map_err(|e| io(marker, e))?;
    Ok(Some((Id::of(&m)?, bytes)))
}

impl Folder {
    /// The folder a preview pinned: a pin of its marker file, `<folder>/<marker>` inside a selected
    /// root. The whole pin is checked as a write checks it (the root's link and the folder it
    /// resolves to, the folder, the marker and its bytes), the root is held open, and the folder
    /// under its name in the held root must still be the one pinned.
    pub fn pinned(pin: &Pin) -> Result<Folder, Error> {
        let bad = || {
            Error::invalid_request(
                "a folder's pin selects its marker file, <folder>/<marker>, inside a root folder",
            )
        };
        let Selection::Root { path, .. } = &pin.selection else {
            return Err(bad());
        };
        let Some((name, marker)) = path.split_once('/') else {
            return Err(bad());
        };
        plain(name)?;
        plain(marker)?;
        let (Some(root), Some(root_id), Some(marker_id), Some(sha)) =
            (&pin.root, pin.root_id, pin.file_id, &pin.sha256)
        else {
            return Err(bad());
        };
        drop(Target::open(pin)?);
        let dir = open_dir(root)?;
        if dir_id(&dir, root)? != root_id {
            return Err(changed(&pin.target, "the root folder was replaced"));
        }
        let f = Folder {
            dir,
            root: root.clone(),
            root_id,
            name: name.to_string(),
            marker: marker.to_string(),
            old: Some(Owned {
                folder: pin.parent_id,
                marker: marker_id,
                sha256: sha.clone(),
            }),
        };
        f.check_place()?;
        Ok(f)
    }

    /// The place for a first copy of the folder name in the selected root root (which may be a
    /// link), resolved now and held open: nothing may be there.
    pub fn empty(root: &Path, name: &str, marker: &str) -> Result<Folder, Error> {
        plain(name)?;
        plain(marker)?;
        let (_, croot, dir, root_id) = open_root(root)?;
        let f = Folder {
            dir,
            root: croot,
            root_id,
            name: name.to_string(),
            marker: marker.to_string(),
            old: None,
        };
        f.check_place()?;
        Ok(f)
    }

    /// Where name in the held root is, as its path read when it was opened.
    pub fn path(&self, name: &str) -> PathBuf {
        self.root.join(name)
    }

    /// The folder's name in its root.
    pub fn name(&self) -> &str {
        &self.name
    }

    /// The folder at name in the held root, opened, when it is a folder (not a link) of identity
    /// id.
    fn open_folder(&self, name: &str, id: Id) -> Result<Option<Dir>, Error> {
        match entry(&self.dir, name)? {
            Some((true, now)) if now == id => {}
            _ => return Ok(None),
        }
        let at = self.path(name);
        let d = self
            .dir
            .open_dir(name)
            .map_err(|e| io(&at.display().to_string(), e))?;
        Ok((dir_id(&d, &at)? == id).then_some(d))
    }

    /// Whether name in the held root is the folder pinned: its identity, and its marker's identity
    /// and bytes.
    fn holds_old(&self, name: &str) -> Result<bool, Error> {
        let Some(old) = &self.old else {
            return Ok(false);
        };
        let Some(d) = self.open_folder(name, old.folder)? else {
            return Ok(false);
        };
        Ok(read_marker(&d, &self.marker)?
            .is_some_and(|(id, bytes)| id == old.marker && sha256(&bytes) == old.sha256))
    }

    /// Whether name in the held root is a folder (not a link) whose marker has SHA-256 sha.
    fn holds_copy(&self, name: &str, sha: &str) -> bool {
        let Ok(Some((true, id))) = entry(&self.dir, name) else {
            return false;
        };
        let Ok(Some(d)) = self.open_folder(name, id) else {
            return false;
        };
        read_marker(&d, &self.marker)
            .ok()
            .flatten()
            .is_some_and(|(_, bytes)| sha256(&bytes) == sha)
    }

    /// The identity of what is at name in the held root, None when nothing is (or it cannot be
    /// read).
    fn id_at(&self, name: &str) -> Option<Id> {
        entry(&self.dir, name).ok().flatten().map(|(_, id)| id)
    }

    /// Whether the folder's place is as when it was opened: the folder pinned under its name, or
    /// nothing there for a first copy.
    fn check_place(&self) -> Result<(), Error> {
        let at = self.path(&self.name);
        match &self.old {
            Some(_) if self.holds_old(&self.name)? => Ok(()),
            Some(_) => Err(changed(
                &at,
                "another folder, or another copy, is there now",
            )),
            None => match entry(&self.dir, &self.name)? {
                None => Ok(()),
                Some(_) => Err(Error::invalid_request(format!(
                    "{} is there already",
                    at.display()
                ))),
            },
        }
    }

    /// Renames from to "to" in the held root. With id, what is renamed must be the folder of that
    /// identity: on Windows it is opened and checked, then renamed through its own handle; on
    /// Linux and macOS it is checked just before the rename (the caller checks what moved after).
    fn rename(&self, from: &str, to: &str, id: Option<Id>) -> Result<(), Error> {
        let at = self.path(from);
        let fail = |e| io(&format!("renaming {} to {to}", at.display()), e);
        #[cfg(unix)]
        {
            if let Some(id) = id {
                if self.id_at(from) != Some(id) {
                    return Err(changed(&at, "another folder is there now"));
                }
            }
            self.dir.rename(from, &self.dir, to).map_err(fail)
        }
        #[cfg(windows)]
        {
            use cap_std::fs::OpenOptionsExt;
            use windows_sys::Win32::Storage::FileSystem::{
                DELETE, FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT,
                FILE_READ_ATTRIBUTES, SYNCHRONIZE,
            };
            let mut options = cap_std::fs::OpenOptions::new();
            options
                .access_mode(DELETE | FILE_READ_ATTRIBUTES | SYNCHRONIZE)
                .custom_flags(FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT);
            let f = self.dir.open_with(from, &options).map_err(fail)?;
            if let Some(id) = id {
                let m = f.metadata().map_err(fail)?;
                if Id::of(&m)? != id {
                    return Err(changed(&at, "another folder is there now"));
                }
            }
            pluxy_files::rename_open(
                &f.into_std(),
                &crate::target::borrowed(&self.dir),
                std::ffi::OsStr::new(to),
            )
            .map_err(fail)
        }
    }

    /// Puts what was moved aside back under the folder's name, if nothing took the name meanwhile.
    fn put_back(&self, aside: &str) -> Result<(), Error> {
        match entry(&self.dir, &self.name)? {
            None => self.rename(aside, &self.name, None),
            Some(_) => Err(Error::invalid_request(format!(
                "something else is under {} now",
                self.path(&self.name).display()
            ))),
        }
    }

    /// Removes the folder name in the held root, with all it holds.
    fn remove_tree(&self, name: &str) -> Result<(), Error> {
        let at = self.path(name);
        self.dir
            .remove_dir_all(name)
            .map_err(|e| io(&format!("removing {}", at.display()), e))
    }

    /// Writes files (by `/`-separated path inside the folder) into the new folder new in the held
    /// root: on Linux and macOS folders 0755 and files 0644 (less the umask), on Windows each takes
    /// its folder's access list. The new folder's identity.
    fn write_new(&self, new: &str, files: &[(&str, &[u8])]) -> Result<Id, Error> {
        let at = self.path(new);
        let fail = |e| io(&format!("writing {}", at.display()), e);
        let mut builder = cap_std::fs::DirBuilder::new();
        #[cfg(unix)]
        {
            use cap_std::fs::DirBuilderExt;
            builder.mode(0o755);
        }
        self.dir.create_dir_with(new, &builder).map_err(fail)?;
        let made = || changed(&at, "it was replaced while it was made");
        let id = match entry(&self.dir, new)? {
            Some((true, id)) => id,
            _ => return Err(made()),
        };
        let d = self.open_folder(new, id)?.ok_or_else(made)?;
        builder.recursive(true);
        let mut folders: Vec<PathBuf> = Vec::new();
        for (name, bytes) in files {
            let rel: PathBuf = name.split('/').collect();
            if let Some(parent) = rel.parent().filter(|p| !p.as_os_str().is_empty()) {
                d.create_dir_with(parent, &builder).map_err(fail)?;
                if !folders.iter().any(|f| f == parent) {
                    folders.push(parent.to_path_buf());
                }
            }
            let mut options = cap_std::fs::OpenOptions::new();
            options.write(true).create_new(true);
            #[cfg(unix)]
            {
                use cap_std::fs::OpenOptionsExt;
                options.mode(0o644);
            }
            let mut f = d.open_with(&rel, &options).map_err(fail)?;
            f.write_all(bytes)
                .and_then(|_| f.sync_all())
                .map_err(fail)?;
        }
        for folder in &folders {
            d.open_dir(folder)
                .and_then(|f| sync_dir(&f))
                .map_err(fail)?;
        }
        sync_dir(&d).map_err(fail)?;
        Ok(id)
    }

    /// The journal's intent for op, with the hidden names it uses: aside for the folder moved
    /// aside, and for a put the new copy's name and its marker's SHA-256.
    fn intent(&self, op: &str, set: &str, aside: &str, new: Option<(&str, &str)>) -> Value {
        json!({"kind": KIND, "op": op, "set": set, "folder": {
            "root": self.root,
            "root_id": self.root_id.to_json(),
            "name": self.name,
            "marker": self.marker,
            "old": self.old.as_ref().map(Owned::to_json),
            "aside": aside,
            "new": new.map(|n| n.0),
            "marker_sha256": new.map(|n| n.1),
        }})
    }
}

/// How moving the pinned folder aside failed.
enum Moved {
    /// Nothing moved, or what moved went back under its name: the journal can end.
    Back(Error),
    /// What moved could not go back: the journal stays open for the next start.
    Stuck(Error),
    /// A test stopped the operation there, as a crash would.
    Stopped(Error),
}

/// The points of a folder operation a test can act at (as another program would) or stop at (as
/// a crash would).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Step {
    /// Checked, just before the folder is moved aside (or, for a first copy, the new one renamed
    /// in).
    Checked,
    /// Moved aside, just before what moved is checked.
    Moved,
    /// The new copy written beside the folder, before anything is moved.
    Written,
    /// The new copy renamed in, before the old one is removed.
    Placed,
    /// The marker of the folder removed, before the rest of it.
    Unmarked,
}

#[cfg(any(test, feature = "test-hooks"))]
type Hook = Box<dyn FnMut(Step) -> bool>;

#[cfg(any(test, feature = "test-hooks"))]
thread_local! {
    static HOOK: std::cell::RefCell<Option<Hook>> = const { std::cell::RefCell::new(None) };
}

/// Tests only: this thread's folder operations call hook at each [`Step`]; where it returns true,
/// the operation stops as a crash would, its journal left open. None takes it away.
#[cfg(any(test, feature = "test-hooks"))]
pub fn hook(hook: Option<Hook>) {
    HOOK.with(|h| *h.borrow_mut() = hook);
}

#[cfg(any(test, feature = "test-hooks"))]
fn stop(step: Step) -> bool {
    HOOK.with(|h| h.borrow_mut().as_mut().is_some_and(|f| f(step)))
}

#[cfg(not(any(test, feature = "test-hooks")))]
fn stop(_: Step) -> bool {
    false
}

fn stopped(step: Step) -> Error {
    Error::internal(format!("stopped at {step:?}"))
}

impl Transactions<'_> {
    /// Puts a copy of files (by `/`-separated path, the marker among them) in folder's place: a new
    /// folder written beside it, then, for a pinned folder, the old one moved aside and checked to
    /// be the folder pinned (else put back and refused), the new one renamed in and the old one
    /// removed; for an empty place, the new one renamed in where nothing is. Under a journal of
    /// set, which the next start reconciles after a stop. When the new copy is in place but the old
    /// one cannot be removed, the error says so and where it is, and the next start removes it.
    pub fn put_folder(&self, set: &str, f: &Folder, files: &[(&str, &[u8])]) -> Result<(), Error> {
        check_set(set)?;
        let marker_sha = files
            .iter()
            .find(|(n, _)| *n == f.marker)
            .map(|(_, b)| sha256(b))
            .ok_or_else(|| Error::internal("the new copy has no marker file"))?;
        let hex = random_hex()?;
        let aside = format!(".{}.pluxy-{hex}.old", f.name);
        let new = format!(".{}.pluxy-{hex}.new", f.name);
        f.check_place()?;
        let journal = self.begin_folder(f.intent(PUT, set, &aside, Some((&new, &marker_sha))))?;
        let new_id = match f.write_new(&new, files) {
            Ok(id) => id,
            Err(e) => {
                let _ = f.remove_tree(&new);
                self.end_folder(&journal, ROLLED_BACK);
                return Err(e);
            }
        };
        if stop(Step::Written) {
            return Err(stopped(Step::Written));
        }
        if f.old.is_some() {
            if let Err(m) = self.move_aside(f, &aside) {
                if !matches!(m, Moved::Stopped(_)) {
                    let _ = f.remove_tree(&new);
                }
                return Err(self.after_move(&journal, m));
            }
        } else if stop(Step::Checked) {
            return Err(stopped(Step::Checked));
        }
        let placed = match entry(&f.dir, &f.name) {
            Ok(None) => f.rename(&new, &f.name, Some(new_id)),
            Ok(Some(_)) => Err(changed(
                &f.path(&f.name),
                "something was put under its name meanwhile",
            )),
            Err(e) => Err(e),
        };
        if let Err(e) = placed {
            let _ = f.remove_tree(&new);
            if f.old.is_none() {
                self.end_folder(&journal, ROLLED_BACK);
                return Err(e);
            }
            return Err(match f.put_back(&aside) {
                Ok(()) => {
                    self.end_folder(&journal, ROLLED_BACK);
                    e
                }
                Err(b) => stuck(f, &aside, &e, &b),
            });
        }
        let _ = sync_dir(&f.dir);
        if stop(Step::Placed) {
            return Err(stopped(Step::Placed));
        }
        if let Some(old) = &f.old {
            let gone = match f.id_at(&aside) {
                Some(id) if id == old.folder => f.remove_tree(&aside),
                None => Ok(()),
                Some(_) => Err(changed(&f.path(&aside), "another folder is there now")),
            };
            if let Err(e) = gone {
                // The new copy is in place; the old one waits for the next start, which removes
                // it: the journal stays open.
                return Err(Error::unavailable(format!(
                    "the new {} is in place, but its old copy, moved aside to {}, could not be removed ({}); the tool's next start removes it",
                    f.name,
                    f.path(&aside).display(),
                    e.message
                )));
            }
            let _ = sync_dir(&f.dir);
        }
        self.commit_folder(&journal);
        Ok(())
    }

    /// Removes the pinned folder: moved aside and checked to be the folder pinned (else put back
    /// and refused), its marker removed (from then on the next start finishes a removal a stop
    /// cut short, never puts it back), then the rest. Under a journal of set. When the rest cannot
    /// be removed, the error says so and where it is, and the next start removes it.
    pub fn remove_folder(&self, set: &str, f: &Folder) -> Result<(), Error> {
        check_set(set)?;
        let old = f
            .old
            .as_ref()
            .ok_or_else(|| Error::internal("only a pinned folder is removed"))?;
        let aside = format!(".{}.pluxy-{}.old", f.name, random_hex()?);
        f.check_place()?;
        let journal = self.begin_folder(f.intent(REMOVE, set, &aside, None))?;
        if let Err(m) = self.move_aside(f, &aside) {
            return Err(self.after_move(&journal, m));
        }
        let _ = sync_dir(&f.dir);
        let unmarked = f.open_folder(&aside, old.folder).and_then(|d| match d {
            Some(d) => d.remove_file(&f.marker).map_err(|e| {
                io(
                    &format!("removing {}'s marker", f.path(&aside).display()),
                    e,
                )
            }),
            None => Err(changed(&f.path(&aside), "another folder is there now")),
        });
        if let Err(e) = unmarked {
            return Err(match f.put_back(&aside) {
                Ok(()) => {
                    self.end_folder(&journal, ROLLED_BACK);
                    e
                }
                Err(b) => stuck(f, &aside, &e, &b),
            });
        }
        let _ = sync_dir(&f.dir);
        if stop(Step::Unmarked) {
            return Err(stopped(Step::Unmarked));
        }
        if let Err(e) = f.remove_tree(&aside) {
            return Err(Error::unavailable(format!(
                "{} is no longer in use (its marker is gone), but its files, moved aside to {}, could not all be removed ({}); the tool's next start removes them",
                f.path(&f.name).display(),
                f.path(&aside).display(),
                e.message
            )));
        }
        let _ = sync_dir(&f.dir);
        self.commit_folder(&journal);
        Ok(())
    }

    /// Moves the pinned folder aside, to aside, and checks that what moved is the folder pinned:
    /// its identity, and its marker's identity and bytes. Another folder moved goes back under the
    /// name.
    fn move_aside(&self, f: &Folder, aside: &str) -> Result<(), Moved> {
        let old = f.old.as_ref().expect("a pinned folder");
        if stop(Step::Checked) {
            return Err(Moved::Stopped(stopped(Step::Checked)));
        }
        f.rename(&f.name, aside, Some(old.folder))
            .map_err(Moved::Back)?;
        if stop(Step::Moved) {
            return Err(Moved::Stopped(stopped(Step::Moved)));
        }
        if f.holds_old(aside).unwrap_or(false) {
            return Ok(());
        }
        let at = f.path(&f.name);
        match f.put_back(aside) {
            Ok(()) => Err(Moved::Back(changed(
                &at,
                "another folder, or another copy, was there when it was moved; it is left as it was",
            ))),
            Err(b) => Err(Moved::Stuck(stuck(
                f,
                aside,
                &changed(&at, "another folder, or another copy, was there when it was moved"),
                &b,
            ))),
        }
    }

    /// The error of a failed move aside, the journal ended when nothing stays moved.
    fn after_move(&self, journal: &Value, m: Moved) -> Error {
        match m {
            Moved::Back(e) => {
                self.end_folder(journal, ROLLED_BACK);
                e
            }
            Moved::Stuck(e) | Moved::Stopped(e) => e,
        }
    }

    fn begin_folder(&self, intent: Value) -> Result<Value, Error> {
        if intent.to_string().len() > 16 << 10 {
            return Err(Error::invalid_request(
                "the operation names too much for one journal",
            ));
        }
        Ok(self
            .vault
            .vault("journal.begin", fields(json!({"intent": intent})))?["journal"]
            .clone())
    }

    fn end_folder(&self, journal: &Value, outcome: &str) {
        let _ = self.vault.vault(
            "journal.end",
            fields(json!({"journal": journal, "outcome": outcome})),
        );
    }

    /// Commits the journal. The change took effect even if the vault cannot record it now: the
    /// next start finds it done on disk.
    fn commit_folder(&self, journal: &Value) {
        let _ = self
            .vault
            .vault("journal.commit", fields(json!({"journal": journal})));
    }

    /// Reconciles a folder journal from what is in its root now (the root opened by the path and
    /// identity it had): a copy put in place is committed and the old copy it moved aside removed;
    /// a removal that had removed the marker is committed and finished; anything else is rolled
    /// back, the new copy it wrote removed and what it moved aside put back under its name. What
    /// cannot be put back or removed (something else under the name, the root gone) is left where
    /// it is and named in the report's conflicts; the journal is ended all the same, so the tool
    /// still starts. The outcome, and the paths restored and left.
    pub(crate) fn reconcile_folder(
        &self,
        intent: &Value,
    ) -> Result<(&'static str, Vec<Value>, Vec<Value>), Error> {
        let fo = &intent["folder"];
        let s = |k: &str| {
            fo[k]
                .as_str()
                .map(String::from)
                .ok_or_else(|| Error::internal(format!("a folder journal without {k}")))
        };
        let root = PathBuf::from(s("root")?);
        let root_id = Id::from_json(&fo["root_id"])?
            .ok_or_else(|| Error::internal("a folder journal without root_id"))?;
        let (name, marker, aside) = (s("name")?, s("marker")?, s("aside")?);
        let new = fo["new"].as_str().map(String::from);
        let marker_sha = fo["marker_sha256"].as_str().map(String::from);
        for n in [&name, &marker, &aside].into_iter().chain(new.as_ref()) {
            plain(n)?;
        }
        let old = Owned::from_json(&fo["old"])?;
        let at = |n: &str| json!(root.join(n));
        let opened = open_dir(&root).and_then(|d| {
            if dir_id(&d, &root)? == root_id {
                Ok(d)
            } else {
                Err(changed(&root, "the root folder was replaced"))
            }
        });
        let Ok(dir) = opened else {
            // The root is gone or another folder now: nothing is touched, and what was moved
            // aside, if anything, stays where it is.
            return Ok((ROLLED_BACK, Vec::new(), vec![at(&aside)]));
        };
        let f = Folder {
            dir,
            root: root.clone(),
            root_id,
            name,
            marker,
            old,
        };
        let (mut restored, mut conflicts) = (Vec::new(), Vec::new());
        let back = |restored: &mut Vec<Value>, conflicts: &mut Vec<Value>| {
            if f.id_at(&aside).is_some() {
                match f.put_back(&aside) {
                    Ok(()) => restored.push(at(&f.name)),
                    Err(_) => conflicts.push(at(&aside)),
                }
            }
        };
        let old_id = f.old.as_ref().map(|o| o.folder);
        let outcome = if intent["op"] == PUT {
            let new_gone = new.as_ref().is_none_or(|n| f.id_at(n).is_none());
            let placed = new_gone
                && marker_sha
                    .as_ref()
                    .is_some_and(|sha| f.holds_copy(&f.name, sha))
                && (old_id.is_none() || f.id_at(&f.name) != old_id);
            if placed {
                if old_id.is_some() && f.id_at(&aside) == old_id && f.remove_tree(&aside).is_err() {
                    conflicts.push(at(&aside));
                }
                COMMITTED
            } else {
                if let Some(n) = &new {
                    if f.id_at(n).is_some() && f.remove_tree(n).is_err() {
                        conflicts.push(at(n));
                    }
                }
                back(&mut restored, &mut conflicts);
                ROLLED_BACK
            }
        } else {
            match f.id_at(&aside) {
                Some(id) if Some(id) == old_id => {
                    let unmarked = f
                        .open_folder(&aside, id)
                        .ok()
                        .flatten()
                        .is_some_and(|d| matches!(read_marker(&d, &f.marker), Ok(None)));
                    if unmarked {
                        if f.remove_tree(&aside).is_err() {
                            conflicts.push(at(&aside));
                        }
                        COMMITTED
                    } else {
                        back(&mut restored, &mut conflicts);
                        ROLLED_BACK
                    }
                }
                Some(_) => {
                    back(&mut restored, &mut conflicts);
                    ROLLED_BACK
                }
                None if old_id.is_some() && f.id_at(&f.name) == old_id => ROLLED_BACK,
                None => COMMITTED,
            }
        };
        let _ = sync_dir(&f.dir);
        Ok((outcome, restored, conflicts))
    }
}

/// The error for what was moved aside and could not go back: where it is now.
fn stuck(f: &Folder, aside: &str, e: &Error, back: &Error) -> Error {
    Error::new(
        &e.code,
        format!(
            "{}; what was moved aside could not be put back under {} ({}): it is at {}, and the tool's next start puts it back",
            e.message,
            f.path(&f.name).display(),
            back.message,
            f.path(aside).display()
        ),
    )
}
