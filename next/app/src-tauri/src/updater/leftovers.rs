//! What the updater makes for an install, recorded so that only it is removed later: on macOS the
//! staging folder a new bundle is unpacked into (where the old bundle ends after the swap), in the
//! app's own data folder or, on another volume, beside the installed bundle; on Windows the new
//! program written next to the installed one and the old one renamed aside. The folder beside the
//! installed copy is not the app's (another administrator can rename a folder of this user's
//! there), so nothing lists it, nor the staging folders: each thing is recorded in the app's own
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
    /// The path is gone, or holds something else (another folder or file, a link): nothing at that
    /// name was deleted.
    NotThere,
    /// It is that thing but could not be removed now (still in use, say, or a file system is
    /// mounted in it): kept for the next start.
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

/// Records that path, identified by id, was made (or is about to be renamed to) for an install.
/// Fails, and the caller makes nothing more, if the record cannot be written.
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

/// A folder made for an install and recorded (`Folder::new`). Dropped, as a failed or
/// cancelled install drops it, it is removed as a recorded entry is (`remove_folder`): only while
/// it is still the folder made, never by its name alone. One kept (`keep`) stays.
#[cfg(unix)]
pub(super) struct Folder {
    made: Made,
    kept: bool,
}

#[cfg(unix)]
impl Folder {
    /// Makes folder path (this user's, mode 0700) and records it in the updates folder dir; if it
    /// cannot be recorded it is removed again, the same way.
    pub(super) fn new(dir: &Path, path: &Path) -> Result<Folder, String> {
        use std::os::unix::fs::DirBuilderExt;
        std::fs::DirBuilder::new()
            .mode(0o700)
            .create(path)
            .map_err(|e| format!("{}: {e}", path.display()))?;
        let id = folder_id(path).inspect_err(|_| {
            // Only an empty folder goes this way.
            let _ = std::fs::remove_dir(path);
        })?;
        let made = Made {
            path: path.to_path_buf(),
            id,
        };
        if let Err(e) = record(dir, path, made.id.clone()) {
            #[cfg(test)]
            tests::swap_point(path, "unrecorded");
            remove_folder(&made);
            return Err(e);
        }
        Ok(Folder { made, kept: false })
    }

    pub(super) fn path(&self) -> &Path {
        &self.made.path
    }

    /// What the folder made is: its device and inode, as recorded.
    pub(super) fn id(&self) -> &str {
        &self.made.id
    }

    /// Leaves the folder when dropped: it stays recorded for a later start's cleanup.
    pub(super) fn keep(&mut self) {
        self.kept = true;
    }
}

#[cfg(unix)]
impl Drop for Folder {
    fn drop(&mut self) {
        if !self.kept {
            remove_folder(&self.made);
        }
    }
}

/// The identity on Unix of folder path, just made (`Folder::new`): device and inode, read through
/// one open of it that does not follow a link. Refused unless what that open reached is a folder
/// of this user's, mode 0700 and empty, all read through the same open folder.
#[cfg(unix)]
pub(super) fn folder_id(path: &Path) -> Result<String, String> {
    use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
    use std::os::unix::io::AsRawFd;
    let shown = |e: std::io::Error| format!("{}: {e}", path.display());
    let f = std::fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC)
        .open(path)
        .map_err(shown)?;
    #[cfg(test)]
    tests::swap_point(path, "opened");
    let m = f.metadata().map_err(shown)?;
    // SAFETY: geteuid has no preconditions.
    if !m.is_dir() || m.uid() != unsafe { libc::geteuid() } || m.mode() & 0o7777 != 0o700 {
        return Err(format!(
            "{} is not a private folder of yours",
            path.display()
        ));
    }
    if !entries(f.as_raw_fd()).map_err(shown)?.is_empty() {
        return Err(format!("{} is not empty", path.display()));
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
    #[cfg(test)]
    tests::swap_point(&m.path, "checked");
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
/// change first (a bundle's folders may be read-only). A folder in it on another device (a file
/// system mounted there) is not entered: refused, by the device looked at before the open and
/// again by the device of what the open reached.
#[cfg(unix)]
fn empty(fd: std::os::unix::io::RawFd, depth: usize) -> std::io::Result<()> {
    use std::io::Error;
    if depth > DEEPEST {
        return Err(Error::other("too deep"));
    }
    let mut own: libc::stat = unsafe { std::mem::zeroed() };
    // SAFETY: fd is open; own is written on success.
    if unsafe { libc::fstat(fd, &mut own) } != 0 {
        return Err(Error::last_os_error());
    }
    // SAFETY: fd is an open folder; a failure leaves it as it was.
    unsafe { libc::fchmod(fd, 0o700) };
    for name in entries(fd)? {
        let mut st: libc::stat = unsafe { std::mem::zeroed() };
        // SAFETY: fd is open, name is NUL-terminated, st is written on success.
        if unsafe { libc::fstatat(fd, name.as_ptr(), &mut st, libc::AT_SYMLINK_NOFOLLOW) } != 0 {
            return Err(Error::last_os_error());
        }
        if st.st_mode & libc::S_IFMT == libc::S_IFDIR {
            if st.st_dev != own.st_dev {
                return Err(Error::other("a file system is mounted in it"));
            }
            #[cfg(test)]
            tests::before_open(&name);
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
            // Mounted on between that look and the open: what the open reached is checked too.
            let mut opened: libc::stat = unsafe { std::mem::zeroed() };
            // SAFETY: child is open; opened is written on success.
            if unsafe { libc::fstat(child, &mut opened) } != 0 || opened.st_dev != own.st_dev {
                // SAFETY: child is open and closed once.
                unsafe { libc::close(child) };
                return Err(Error::other("a file system is mounted in it"));
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

/// The names in the open folder fd but `.` and `..`, read through it.
#[cfg(unix)]
fn entries(fd: std::os::unix::io::RawFd) -> std::io::Result<Vec<std::ffi::CString>> {
    use std::ffi::CStr;
    use std::io::Error;
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
    Ok(names)
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;
    use std::fs;

    /// Run at the point named, for the path a test set it for: "checked" in `remove_folder`
    /// between its identity check and emptying the folder, "opened" in `folder_id` right after
    /// its open, and "unrecorded" in `Folder::new` just before it removes a folder whose record
    /// failed. Tests run in parallel, so each test's swap is an entry of its own (`swap_at`),
    /// which no other test replaces or clears.
    static SWAP: Mutex<Vec<Swap>> = Mutex::new(Vec::new());

    /// The path a swap is for, the point it runs at, and the swap.
    type Swap = (PathBuf, &'static str, fn(&Path));

    /// The swap one test set (`swap_at`): dropping it removes that entry only.
    struct Hooked(PathBuf, &'static str);

    impl Drop for Hooked {
        fn drop(&mut self) {
            SWAP.lock()
                .unwrap_or_else(|e| e.into_inner())
                .retain(|(p, at, _)| !(*p == self.0 && *at == self.1));
        }
    }

    /// Sets f to run at the point at for path, until the value returned is dropped.
    fn swap_at(path: &Path, at: &'static str, f: fn(&Path)) -> Hooked {
        SWAP.lock().unwrap().push((path.to_path_buf(), at, f));
        Hooked(path.to_path_buf(), at)
    }

    pub(super) fn swap_point(path: &Path, at: &str) {
        let hook = SWAP
            .lock()
            .unwrap()
            .iter()
            .find(|(p, point, _)| p == path && *point == at)
            .map(|(_, _, f)| *f);
        if let Some(f) = hook {
            f(path);
        }
    }

    /// Run by `empty` between looking at a folder in the folder it empties and opening it, for the
    /// name set here only: that name, the path it is at, and what to do to it.
    static BEFORE_OPEN: Mutex<Option<BeforeOpen>> = Mutex::new(None);

    type BeforeOpen = (std::ffi::CString, PathBuf, fn(&Path));

    pub(super) fn before_open(name: &std::ffi::CStr) {
        let hook = match &*BEFORE_OPEN.lock().unwrap() {
            Some((n, p, f)) if n.as_c_str() == name => Some((p.clone(), *f)),
            _ => None,
        };
        if let Some((p, f)) = hook {
            f(&p);
        }
    }

    /// Device and inode of what path names, without following a link.
    fn id_of(path: &Path) -> String {
        use std::os::unix::fs::MetadataExt;
        let m = fs::symlink_metadata(path).unwrap();
        format!("{}:{}", m.dev(), m.ino())
    }

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
        record(&dir, &ours, id_of(&ours)).unwrap();
        // A recorded staging folder replaced by another folder of the same name.
        let replaced = beside.join(".pluxy-update-2222222222222222");
        fs::create_dir(&replaced).unwrap();
        record(&dir, &replaced, id_of(&replaced)).unwrap();
        fs::rename(&replaced, tmp.join("moved")).unwrap();
        made(&replaced, &outside, false);
        // A recorded entry that is gone.
        let gone = beside.join(".pluxy-update-3333333333333333");
        fs::create_dir(&gone).unwrap();
        record(&dir, &gone, id_of(&gone)).unwrap();
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
            id: id_of(&real),
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

    // A folder made for an install and dropped (the install failed or was cancelled) goes, with
    // what was put in it, only while it is still the folder made: a folder of the user's put in
    // its place stays whole, and a link put there is not followed. One kept stays.
    #[test]
    fn a_dropped_folder_goes_only_while_it_is_still_the_one_made() {
        let tmp = super::super::tests::temp("leftovers-drop");
        let dir = tmp.join("updates");
        let outside = tmp.join("outside");
        fs::create_dir(&outside).unwrap();
        fs::write(outside.join("keep"), b"z").unwrap();
        let path = tmp.join(".pluxy-update-5555555555555555");

        let f = Folder::new(&dir, &path).unwrap();
        assert_eq!(f.path(), path);
        fs::write(path.join("file"), b"x").unwrap();
        fs::create_dir_all(path.join("a/b")).unwrap();
        drop(f);
        assert!(!path.exists(), "dropped as made: removed");

        let f = Folder::new(&dir, &path).unwrap();
        fs::rename(&path, tmp.join("moved")).unwrap();
        made(&path, &outside, false);
        drop(f);
        assert!(path.join("a/b/deep").exists(), "not the folder made");
        assert!(tmp.join("moved").exists());
        assert!(outside.join("keep").exists());

        let linked = tmp.join(".pluxy-update-6666666666666666");
        let f = Folder::new(&dir, &linked).unwrap();
        fs::rename(&linked, tmp.join("moved2")).unwrap();
        std::os::unix::fs::symlink(&outside, &linked).unwrap();
        drop(f);
        assert!(outside.join("keep").exists(), "the link was not followed");

        let kept = tmp.join(".pluxy-update-7777777777777777");
        let mut f = Folder::new(&dir, &kept).unwrap();
        f.keep();
        drop(f);
        assert!(kept.exists());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A new folder's identity is recorded only if, opened without following a link, it is a
    // folder of this user's, mode 0700 and empty: a link, a folder with something in it, and one
    // others can open are refused.
    #[test]
    fn a_new_folder_is_recorded_only_if_it_is_private_empty_and_no_link() {
        use std::os::unix::fs::{MetadataExt, PermissionsExt};
        let tmp = super::super::tests::temp("leftovers-new");
        let private = |p: &Path| {
            fs::create_dir(p).unwrap();
            fs::set_permissions(p, fs::Permissions::from_mode(0o700)).unwrap();
        };
        let ok = tmp.join("ok");
        private(&ok);
        let m = fs::symlink_metadata(&ok).unwrap();
        assert_eq!(folder_id(&ok).unwrap(), format!("{}:{}", m.dev(), m.ino()));
        let full = tmp.join("full");
        private(&full);
        fs::write(full.join("file"), b"x").unwrap();
        assert!(folder_id(&full).is_err(), "a folder with something in it");
        let open = tmp.join("open");
        private(&open);
        fs::set_permissions(&open, fs::Permissions::from_mode(0o755)).unwrap();
        assert!(folder_id(&open).is_err(), "a folder others can open");
        let link = tmp.join("link");
        std::os::unix::fs::symlink(&ok, &link).unwrap();
        assert!(folder_id(&link).is_err(), "a link");
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A file system mounted on a folder inside a recorded folder is not crossed: the folder is
    // kept, and what is on the mount stays. The mount needs a mount namespace of its own: the
    // test runs itself again under `unshare -rm` where this system allows that (CI's Ubuntu
    // runners do not), else says it did not run.
    #[cfg(target_os = "linux")]
    #[test]
    fn a_file_system_mounted_inside_is_not_crossed() {
        const IN_NS: &str = "PLUXY_TEST_MOUNT_NAMESPACE";
        if std::env::var_os(IN_NS).is_none() {
            let unshare = |args: &[&std::ffi::OsStr]| {
                std::process::Command::new("unshare")
                    .arg("-rm")
                    .args(args)
                    .env(IN_NS, "1")
                    .status()
            };
            if !unshare(&["true".as_ref()]).is_ok_and(|s| s.success()) {
                // Written past the test harness's capture, so that a run's log shows it.
                use std::io::Write;
                let _ = writeln!(
                    std::io::stderr(),
                    "a_file_system_mounted_inside_is_not_crossed not run: this system makes no \
                     mount namespace for this user (unshare -rm)"
                );
                return;
            }
            let exe = std::env::current_exe().unwrap();
            let name = "updater::leftovers::tests::a_file_system_mounted_inside_is_not_crossed";
            let s = unshare(&[
                exe.as_os_str(),
                "--exact".as_ref(),
                name.as_ref(),
                "--test-threads=1".as_ref(),
            ])
            .unwrap();
            assert!(s.success(), "in its own mount namespace: {s}");
            return;
        }
        let tmp = super::super::tests::temp("leftovers-mount");
        let path = tmp.join(".pluxy-update-8888888888888888");
        fs::create_dir(&path).unwrap();
        fs::write(path.join("file"), b"x").unwrap();
        let mounted = path.join("mounted");
        fs::create_dir(&mounted).unwrap();
        let c = |p: &Path| std::ffi::CString::new(p.as_os_str().as_encoded_bytes()).unwrap();
        // SAFETY: NUL-terminated strings that outlive the call; no data for tmpfs.
        let r = unsafe {
            libc::mount(
                c"tmpfs".as_ptr(),
                c(&mounted).as_ptr(),
                c"tmpfs".as_ptr(),
                0,
                std::ptr::null(),
            )
        };
        assert_eq!(r, 0, "{}", std::io::Error::last_os_error());
        fs::write(mounted.join("keep"), b"z").unwrap();
        let m = Made {
            path: path.clone(),
            id: id_of(&path),
        };
        assert_eq!(remove_folder(&m), Outcome::Kept);
        assert!(mounted.join("keep").exists(), "the mount was crossed");
        // SAFETY: as above.
        assert_eq!(unsafe { libc::umount(c(&mounted).as_ptr()) }, 0);

        // Mounted on between the look at the folder and its open: not entered either.
        let path = tmp.join(".pluxy-update-bbbbbbbbbbbbbbbb");
        fs::create_dir(&path).unwrap();
        let later = path.join("mounted-later");
        fs::create_dir(&later).unwrap();
        fn mount(p: &Path) {
            let at = std::ffi::CString::new(p.as_os_str().as_encoded_bytes()).unwrap();
            // SAFETY: NUL-terminated strings that outlive the call; no data for tmpfs.
            let r = unsafe {
                libc::mount(
                    c"tmpfs".as_ptr(),
                    at.as_ptr(),
                    c"tmpfs".as_ptr(),
                    0,
                    std::ptr::null(),
                )
            };
            assert_eq!(r, 0, "{}", std::io::Error::last_os_error());
            fs::write(p.join("keep"), b"z").unwrap();
        }
        *BEFORE_OPEN.lock().unwrap() = Some((c"mounted-later".into(), later.clone(), mount));
        let m = Made {
            path: path.clone(),
            id: id_of(&path),
        };
        let r = remove_folder(&m);
        *BEFORE_OPEN.lock().unwrap() = None;
        assert_eq!(r, Outcome::Kept);
        assert!(
            later.join("keep").exists(),
            "the mount made before the open was crossed"
        );
        // SAFETY: as above.
        assert_eq!(unsafe { libc::umount(c(&later).as_ptr()) }, 0);
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A recorded folder swapped for another between the identity check and the emptying: what is
    // emptied is the folder checked, through the open it was checked on, and the folder now at its
    // name stays whole.
    #[test]
    fn the_folder_emptied_is_the_one_checked() {
        let tmp = super::super::tests::temp("leftovers-swap");
        let outside = tmp.join("outside");
        fs::create_dir(&outside).unwrap();
        let path = tmp.join(".pluxy-update-9999999999999999");
        made(&path, &outside, false);
        let m = Made {
            path: path.clone(),
            id: id_of(&path),
        };
        fn swap(p: &Path) {
            fs::rename(p, p.with_extension("moved")).unwrap();
            fs::create_dir(p).unwrap();
            fs::write(p.join("theirs"), b"z").unwrap();
        }
        let hooked = swap_at(&path, "checked", swap);
        let r = remove_folder(&m);
        drop(hooked);
        assert_eq!(r, Outcome::NotThere);
        assert!(
            path.join("theirs").exists(),
            "the folder put there was emptied"
        );
        assert!(names(&path.with_extension("moved")).is_empty());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A new folder's identity is that of the folder its one open reached: a folder put at its
    // name after that open is not what is recorded.
    #[test]
    fn a_new_folders_identity_is_read_through_its_one_open() {
        use std::os::unix::fs::PermissionsExt;
        let tmp = super::super::tests::temp("leftovers-one-open");
        let path = tmp.join(".pluxy-update-cccccccccccccccc");
        fs::create_dir(&path).unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o700)).unwrap();
        fn swap(p: &Path) {
            fs::rename(p, p.with_extension("moved")).unwrap();
            fs::create_dir(p).unwrap();
            fs::set_permissions(p, fs::Permissions::from_mode(0o700)).unwrap();
        }
        let hooked = swap_at(&path, "opened", swap);
        let id = folder_id(&path);
        drop(hooked);
        assert_eq!(id.unwrap(), id_of(&path.with_extension("moved")));
        fs::remove_dir_all(&tmp).unwrap();
    }

    // Another user's folder at a recorded name is not entered, even with the recorded identity.
    // /usr/bin is root's; not run as root, or where this user can change it.
    #[test]
    fn another_users_folder_is_not_there() {
        let path = Path::new("/usr/bin");
        let c = std::ffi::CString::new("/usr/bin").unwrap();
        // SAFETY: geteuid has no preconditions; c is NUL-terminated.
        if unsafe { libc::geteuid() } == 0 || unsafe { libc::access(c.as_ptr(), libc::W_OK) } == 0 {
            eprintln!("not run: as root, or /usr/bin is this user's to change");
            return;
        }
        let m = Made {
            path: path.to_path_buf(),
            id: id_of(path),
        };
        assert_eq!(remove_folder(&m), Outcome::NotThere);
    }

    // A folder made whose record cannot be written is removed again, and the install told; it
    // goes only while it is still the folder made: a folder put at its name first stays.
    #[test]
    fn a_folder_that_cannot_be_recorded_is_not_left() {
        let tmp = super::super::tests::temp("leftovers-norecord");
        let dir = tmp.join("updates");
        fs::write(&dir, b"not a folder").unwrap();
        let path = tmp.join(".pluxy-update-aaaaaaaaaaaaaaaa");
        assert!(Folder::new(&dir, &path).is_err());
        assert!(!path.exists());
        fn swap(p: &Path) {
            fs::rename(p, p.with_extension("moved")).unwrap();
            fs::create_dir(p).unwrap();
            fs::write(p.join("theirs"), b"z").unwrap();
        }
        let hooked = swap_at(&path, "unrecorded", swap);
        let r = Folder::new(&dir, &path);
        drop(hooked);
        assert!(r.is_err());
        assert!(path.join("theirs").exists(), "not the folder made");
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
