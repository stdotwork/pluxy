//! What the updater makes beside the installed copy, recorded so that only it is removed later:
//! on macOS the staging folder a new bundle is unpacked into (where the old bundle ends after the
//! swap), on Windows the new program written next to the installed one and the old one renamed
//! aside. The folder beside the installed copy is not the app's (another administrator can rename
//! a folder of this user's there), so nothing lists it: each thing is recorded in the app's own
//! data (`updates/leftovers.json`) with what identifies it beyond its name, when it is made (or,
//! for a rename, just before), and the cleanup at startup removes only a recorded entry that is
//! still that thing, never through a link. An entry whose path is gone or holds something else is
//! dropped without deleting anything; one that could not be removed stays for the next start.
use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};
use std::sync::Mutex;

/// The record's name in the updates folder.
const FILE: &str = "leftovers.json";
/// At most this many entries are kept; the oldest go first (a leftover then stays on disk).
const MAX: usize = 64;

/// One thing the updater made: where, and what it is (`dev:ino` of a folder on macOS, the volume
/// serial and file ID of a file on Windows, in hex).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub(super) struct Made {
    pub path: PathBuf,
    pub id: String,
}

/// What removing a recorded entry came to.
#[derive(Debug, PartialEq, Eq)]
pub(super) enum Outcome {
    /// It was that thing, and it is gone.
    Removed,
    /// The path is gone, or holds something else (another folder or file, a link): nothing was
    /// deleted.
    NotThere,
    /// It is that thing but could not be removed now (still in use, say): kept for the next start.
    Kept,
}

/// Held while the record is read and written: an install and the startup cleanup can overlap.
static LOCK: Mutex<()> = Mutex::new(());
/// What this process recorded: its own cleanup leaves it alone (an install of this process may
/// still be using it); the next version's removes it.
static MINE: Mutex<Vec<Made>> = Mutex::new(Vec::new());

fn read(dir: &Path) -> Vec<Made> {
    super::read_json(&dir.join(FILE)).unwrap_or_default()
}

/// Forgets what this process recorded under root, so its cleanup takes it as an earlier run's.
#[cfg(test)]
pub(super) fn forget_mine(root: &Path) {
    MINE.lock().unwrap().retain(|m| !m.path.starts_with(root));
}

/// Records that path, identified by id, was made (or is about to be renamed to) beside the
/// installed copy. Fails, and the caller makes nothing more, if the record cannot be written.
pub(super) fn record(dir: &Path, path: &Path, id: String) -> Result<(), String> {
    let _held = LOCK.lock().unwrap();
    let made = Made {
        path: path.to_path_buf(),
        id,
    };
    let mut all = read(dir);
    all.retain(|m| m.path != made.path);
    all.push(made.clone());
    let over = all.len().saturating_sub(MAX);
    all.drain(..over);
    super::write_json(&dir.join(FILE), &all)?;
    MINE.lock().unwrap().push(made);
    Ok(())
}

/// Removes, with remove, each entry an earlier run recorded, and drops from the record every one
/// that is gone now or was never there. remove runs without the record held.
pub(super) fn clean(dir: &Path, remove: impl Fn(&Made) -> Outcome) {
    let todo: Vec<Made> = {
        let _held = LOCK.lock().unwrap();
        let mine = MINE.lock().unwrap();
        read(dir)
            .into_iter()
            .filter(|m| !mine.contains(m))
            .collect()
    };
    if todo.is_empty() {
        return;
    }
    let done: Vec<Made> = todo
        .into_iter()
        .filter(|m| remove(m) != Outcome::Kept)
        .collect();
    let _held = LOCK.lock().unwrap();
    let mut all = read(dir);
    let before = all.len();
    all.retain(|m| !done.contains(m));
    if all.len() != before {
        let _ = super::write_json(&dir.join(FILE), &all);
    }
}

/// A folder's identity on Unix: device and inode, read without following a link. Refused unless
/// path is a folder of this user's.
#[cfg(unix)]
pub(super) fn folder_id(path: &Path) -> Result<String, String> {
    use std::os::unix::fs::MetadataExt;
    let m = std::fs::symlink_metadata(path).map_err(|e| format!("{}: {e}", path.display()))?;
    // SAFETY: geteuid has no preconditions.
    if !m.is_dir() || m.uid() != unsafe { libc::geteuid() } {
        return Err(format!("{} is not a folder of yours", path.display()));
    }
    Ok(format!("{}:{}", m.dev(), m.ino()))
}

/// Removes the recorded folder m on Unix: opened without following a link, it must still be the
/// recorded folder (device and inode) and this user's; what is in it is removed through that
/// open folder, never by a path from outside it, then the folder itself, by its path once more
/// only if that path still names it.
#[cfg(unix)]
pub(super) fn remove_folder(m: &Made) -> Outcome {
    use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
    use std::os::unix::io::AsRawFd;
    let Ok(f) = std::fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC)
        .open(&m.path)
    else {
        return Outcome::NotThere;
    };
    // SAFETY: geteuid has no preconditions.
    let me = unsafe { libc::geteuid() };
    let id = |md: &std::fs::Metadata| format!("{}:{}", md.dev(), md.ino());
    match f.metadata() {
        Ok(md) if md.is_dir() && md.uid() == me && id(&md) == m.id => {}
        _ => return Outcome::NotThere,
    }
    if empty(f.as_raw_fd(), 0).is_err() {
        return Outcome::Kept;
    }
    drop(f);
    let still = std::fs::symlink_metadata(&m.path).is_ok_and(|md| md.is_dir() && id(&md) == m.id);
    if !still {
        return Outcome::NotThere;
    }
    // Between that look and this removal only an empty folder could take its place.
    match std::fs::remove_dir(&m.path) {
        Ok(()) => Outcome::Removed,
        Err(_) => Outcome::Kept,
    }
}

/// How deep `empty` goes: an update's bundle is far shallower.
#[cfg(unix)]
const DEEPEST: usize = 64;

/// Removes everything in the open folder fd, through it: each entry looked at without following
/// a link, folders opened the same way and emptied first. The folder is made this user's to
/// change first (a bundle's folders may be read-only).
#[cfg(unix)]
fn empty(fd: std::os::unix::io::RawFd, depth: usize) -> std::io::Result<()> {
    use std::ffi::CStr;
    use std::io::Error;
    if depth > DEEPEST {
        return Err(Error::other("too deep"));
    }
    // SAFETY: fd is an open folder; a failure leaves it as it was.
    unsafe { libc::fchmod(fd, 0o700) };
    let names = {
        // SAFETY: dup of an open descriptor; fdopendir takes the copy and closedir closes it.
        let copy = unsafe { libc::dup(fd) };
        if copy < 0 {
            return Err(Error::last_os_error());
        }
        let d = unsafe { libc::fdopendir(copy) };
        if d.is_null() {
            let e = Error::last_os_error();
            unsafe { libc::close(copy) };
            return Err(e);
        }
        let mut names = vec![];
        loop {
            // SAFETY: d is an open directory stream.
            let e = unsafe { libc::readdir(d) };
            if e.is_null() {
                break;
            }
            // SAFETY: d_name is NUL-terminated within the entry readdir returned.
            let name = unsafe { CStr::from_ptr((*e).d_name.as_ptr()) }.to_owned();
            if name.as_bytes() != b"." && name.as_bytes() != b".." {
                names.push(name);
            }
        }
        // SAFETY: d came from fdopendir and is closed once.
        unsafe { libc::closedir(d) };
        names
    };
    for name in names {
        let mut st: libc::stat = unsafe { std::mem::zeroed() };
        // SAFETY: fd is open, name is NUL-terminated, st is written on success.
        if unsafe { libc::fstatat(fd, name.as_ptr(), &mut st, libc::AT_SYMLINK_NOFOLLOW) } != 0 {
            return Err(Error::last_os_error());
        }
        if st.st_mode & libc::S_IFMT == libc::S_IFDIR {
            // SAFETY: as above; the descriptor is closed below.
            let child = unsafe {
                libc::openat(
                    fd,
                    name.as_ptr(),
                    libc::O_RDONLY | libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC,
                )
            };
            if child < 0 {
                return Err(Error::last_os_error());
            }
            let r = empty(child, depth + 1);
            // SAFETY: child is open and closed once.
            unsafe { libc::close(child) };
            r?;
            // SAFETY: as above.
            if unsafe { libc::unlinkat(fd, name.as_ptr(), libc::AT_REMOVEDIR) } != 0 {
                return Err(Error::last_os_error());
            }
        } else if unsafe { libc::unlinkat(fd, name.as_ptr(), 0) } != 0 {
            return Err(Error::last_os_error());
        }
    }
    Ok(())
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;
    use std::fs;

    fn names(dir: &Path) -> Vec<String> {
        let mut v: Vec<String> = fs::read_dir(dir)
            .unwrap()
            .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
            .collect();
        v.sort();
        v
    }

    /// A folder at path with a file, a folder (read-only if read_only) and a link to outside in it.
    fn made(path: &Path, outside: &Path, read_only: bool) {
        fs::create_dir(path).unwrap();
        fs::write(path.join("file"), b"x").unwrap();
        fs::create_dir_all(path.join("a/b")).unwrap();
        fs::write(path.join("a/b/deep"), b"y").unwrap();
        std::os::unix::fs::symlink(outside, path.join("link")).unwrap();
        if read_only {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(path.join("a"), fs::Permissions::from_mode(0o500)).unwrap();
        }
    }

    // Only what this updater recorded goes: a folder of the same kind of name it did not record
    // stays, a recorded folder goes with everything in it (a link in it is removed, not followed),
    // and a recorded path that now holds another folder stays. Every entry is dropped once
    // settled; this process's own entries are left for the next one.
    #[test]
    fn only_recorded_folders_that_are_still_them_are_removed() {
        let tmp = super::super::tests::temp("leftovers");
        let (dir, beside) = (tmp.join("updates"), tmp.join("Applications"));
        fs::create_dir_all(&beside).unwrap();
        let outside = tmp.join("outside");
        fs::create_dir(&outside).unwrap();
        fs::write(outside.join("keep"), b"z").unwrap();

        // Someone else's folder renamed to the staging pattern: never recorded.
        let renamed = beside.join(".pluxy-update-0123456789abcdef");
        made(&renamed, &outside, false);
        // A staging folder this updater made and recorded.
        let ours = beside.join(".pluxy-update-1111111111111111");
        made(&ours, &outside, true);
        record(&dir, &ours, folder_id(&ours).unwrap()).unwrap();
        // A recorded staging folder replaced by another folder of the same name.
        let replaced = beside.join(".pluxy-update-2222222222222222");
        fs::create_dir(&replaced).unwrap();
        record(&dir, &replaced, folder_id(&replaced).unwrap()).unwrap();
        fs::rename(&replaced, tmp.join("moved")).unwrap();
        made(&replaced, &outside, false);
        // A recorded entry that is gone.
        let gone = beside.join(".pluxy-update-3333333333333333");
        fs::create_dir(&gone).unwrap();
        record(&dir, &gone, folder_id(&gone).unwrap()).unwrap();
        fs::remove_dir(&gone).unwrap();

        // This process recorded them: its own cleanup leaves them.
        clean(&dir, remove_folder);
        assert!(ours.exists());
        assert_eq!(read(&dir).len(), 3);

        forget_mine(&tmp);
        clean(&dir, remove_folder);
        assert_eq!(
            names(&beside),
            [
                ".pluxy-update-0123456789abcdef",
                ".pluxy-update-2222222222222222"
            ]
        );
        assert!(renamed.join("a/b/deep").exists());
        assert!(replaced.join("a/b/deep").exists());
        assert!(outside.join("keep").exists(), "the link was not followed");
        assert!(read(&dir).is_empty(), "{:?}", read(&dir));
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A recorded path that is a link to the recorded folder is not followed: the folder stays.
    #[test]
    fn a_link_in_place_of_a_recorded_folder_is_not_followed() {
        let tmp = super::super::tests::temp("leftovers-link");
        let real = tmp.join("real");
        fs::create_dir(&real).unwrap();
        fs::write(real.join("keep"), b"z").unwrap();
        let path = tmp.join(".pluxy-update-4444444444444444");
        let m = Made {
            path: path.clone(),
            id: folder_id(&real).unwrap(),
        };
        std::os::unix::fs::symlink(&real, &path).unwrap();
        assert_eq!(remove_folder(&m), Outcome::NotThere);
        assert!(real.join("keep").exists());
        assert!(
            folder_id(&path).is_err(),
            "a link is not a folder to record"
        );
        fs::remove_dir_all(&tmp).unwrap();
    }

    // What could not be removed stays recorded; the record keeps at most MAX entries.
    #[test]
    fn what_is_kept_stays_recorded_and_the_record_is_bounded() {
        let tmp = super::super::tests::temp("leftovers-kept");
        let dir = tmp.join("updates");
        for i in 0..MAX + 3 {
            let p = tmp.join(format!("x{i}"));
            record(&dir, &p, format!("{i}")).unwrap();
        }
        let all = read(&dir);
        assert_eq!(all.len(), MAX);
        assert_eq!(all[0].path, tmp.join("x3"), "the oldest went first");
        forget_mine(&tmp);
        clean(&dir, |m| {
            if m.id == "5" {
                Outcome::Kept
            } else {
                Outcome::NotThere
            }
        });
        assert_eq!(
            read(&dir),
            [Made {
                path: tmp.join("x5"),
                id: "5".into()
            }]
        );
        fs::remove_dir_all(&tmp).unwrap();
    }
}
