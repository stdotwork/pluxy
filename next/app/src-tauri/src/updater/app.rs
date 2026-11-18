//! macOS: the `.app` bundle this user keeps, run as `<name>.app/Contents/MacOS/<program>`, pinned
//! by its tree digest (`bundle::tree_digest`). The update is a `.app.tar.gz` (Tauri's macOS update
//! layout), unpacked under strict rules (`bundle::unpack`) into a new staging folder of this
//! user's, checked (identifier, program name, announced version, a program for this Mac), then
//! swapped with the installed bundle in one step (`renameatx_np` with `RENAME_SWAP`, relative to
//! the two folders' open descriptors): the bundle is the old one or the new one, never missing.
//!
//! The staging folder is private (`updates/staging/<hex>` in the app's data folder) whenever that
//! is on the bundle's volume, after checking that every folder from the home folder down to it is
//! this user's alone (`private_path`): then nothing in the bundle's folder changes but the
//! bundle's own entry, at the swap. Only on another volume, where a swap cannot reach, is it made
//! next to the bundle (`.pluxy-update-<hex>`), in the folder the bundle's checks found this
//! user's alone. The old bundle ends in the staging folder, which is removed once the new version
//! runs and the old one has ended: the folder is recorded when it is made (`leftovers.rs`), and
//! only a recorded folder that is still that folder is removed; the bundle's folder is never
//! listed. A volume that cannot swap (not APFS) is refused, not updated another way.
//!
//! Nothing here asks for an administrator, changes the quarantine attribute or goes through
//! LaunchServices: a bundle this user cannot change is refused.
//!
//! Also built for the tests on Linux, which swap with `renameat2`'s `RENAME_EXCHANGE`.
#![cfg_attr(not(target_os = "macos"), allow(dead_code))]
use super::bundle;
#[cfg(target_os = "macos")]
use super::check_owned;
use super::leftovers;
use super::Pinned;
use std::ffi::CString;
use std::fs;
use std::io;
use std::os::unix::ffi::OsStrExt;
use std::path::{Component, Path, PathBuf};
use tauri::AppHandle;

/// The updater's name for this system's artifact: macOS, this architecture, an app bundle.
#[cfg(target_arch = "aarch64")]
pub(super) const TARGET: &str = "darwin-aarch64-app";
#[cfg(target_arch = "x86_64")]
pub(super) const TARGET: &str = "darwin-x86_64-app";
/// The Mach-O CPU type of this architecture.
#[cfg(target_arch = "aarch64")]
const CPU: u32 = 0x0100_000c;
#[cfg(target_arch = "x86_64")]
const CPU: u32 = 0x0100_0007;
/// What a kept copy's name ends in: it is a bundle too.
pub(super) const SUFFIX: &str = ".app";
/// What the installed copy is called in messages.
pub(super) const WHAT: &str = "app bundle";
/// What a pin's digest is, in messages.
pub(super) const DIGEST: &str = "tree SHA-256";

/// How this copy of the app was installed, as far as updating it goes: the app bundle this user
/// keeps on a volume that can swap it, or why it cannot be updated from here.
#[cfg(target_os = "macos")]
pub(super) fn provenance(app: &AppHandle) -> Result<PathBuf, String> {
    use tauri::utils::config::BundleType;
    // Unpatched, the bundle type is App on macOS: it says nothing about where the program is,
    // which `here` checks.
    if tauri::utils::platform::bundle_type() != Some(BundleType::App) {
        return Err(
            "This copy of pluxy was not installed as an app bundle, the only kind this \
             updater can replace on macOS. Install the new version the way you installed this one."
                .into(),
        );
    }
    let bundle = here(app).ok_or(
        "This program is not in its app bundle (<name>.app/Contents/MacOS/<program>, with this \
         app's identifier); install the new version by hand.",
    )?;
    if translocated(&bundle) {
        return Err(format!(
            "{} runs from a read-only copy macOS made of it (App Translocation), not from where \
             it was put. Move the app into a folder of your own, such as Applications in your \
             home folder (~/Applications), with Finder, open it from there, and update then.",
            bundle.display()
        ));
    }
    // The updater plugin cannot start for a program started by a path with a link in it (Tauri
    // refuses that path on macOS), so such a start is refused here, before an update is offered.
    let exe = std::env::current_exe().map_err(|e| e.to_string())?;
    if let Some(link) = bundle::linked(&exe) {
        return Err(format!(
            "This app was started by a path that goes through a link ({}), and the updater cannot \
             start from there. Start the app from its real location ({}), for example from \
             Finder, and update then.",
            link.display(),
            bundle.display()
        ));
    }
    let folder = bundle.parent().ok_or("The app bundle has no folder.")?;
    // shortcut: as on Linux, only the bundle and its folder are checked, and the swap opens that
    // folder by its path; a folder further up that others could change could be swapped. Upgrade
    // by checking the ancestors, if updating on shared machines is wanted.
    check_owned(&bundle, true)?;
    check_owned(folder, true)?;
    swaps(folder)?;
    Ok(bundle)
}

/// Whether path is under a translocation mount (`/private/var/folders/../AppTranslocation/..`).
fn translocated(path: &Path) -> bool {
    path.components()
        .any(|c| c.as_os_str() == std::ffi::OsStr::new("AppTranslocation"))
}

/// Refused unless folder is on a volume that swaps two entries in one step (`RENAME_SWAP`):
/// APFS, which every supported macOS starts from.
#[cfg(target_os = "macos")]
fn swaps(folder: &Path) -> Result<(), String> {
    let c = CString::new(folder.as_os_str().as_bytes()).map_err(|e| e.to_string())?;
    let mut s: libc::statfs = unsafe { std::mem::zeroed() };
    // SAFETY: c is NUL-terminated; s is written on success.
    if unsafe { libc::statfs(c.as_ptr(), &mut s) } != 0 {
        return Err(format!(
            "{}: {}",
            folder.display(),
            std::io::Error::last_os_error()
        ));
    }
    let name: Vec<u8> = s
        .f_fstypename
        .iter()
        .take_while(|b| **b != 0)
        .map(|b| *b as u8)
        .collect();
    swap_volume(&String::from_utf8_lossy(&name), folder)
}

fn swap_volume(kind: &str, folder: &Path) -> Result<(), String> {
    if kind != "apfs" {
        return Err(format!(
            "{} is on a {kind} volume, which cannot swap the app for its new version in one step; \
             move the app to an APFS volume (such as the startup disk) or install the new version \
             by hand.",
            folder.display()
        ));
    }
    Ok(())
}

/// The app bundle this process runs from: the program's path (links resolved) must be
/// `<name>.app/Contents/MacOS/<program>`, and the bundle's `Info.plist` must name this app's
/// identifier and that program. Otherwise None.
pub(super) fn here(app: &AppHandle) -> Option<PathBuf> {
    let exe = std::env::current_exe().ok()?.canonicalize().ok()?;
    let name = exe.file_name()?.to_str()?.to_string();
    let macos = exe.parent()?;
    let contents = macos.parent()?;
    let bundle = contents.parent()?;
    let shaped = macos.file_name()? == "MacOS"
        && contents.file_name()? == "Contents"
        && bundle.extension()? == "app";
    let info = bundle::info(bundle).ok()?;
    (shaped && info.identifier == app.config().identifier && info.executable == name)
        .then(|| bundle.to_path_buf())
}

/// The bundle at path's tree digest.
pub(super) fn digest(path: &Path) -> Result<String, String> {
    bundle::tree_digest(path)
}

/// Copies the bundle from to the new folder to.
pub(super) fn copy_new(from: &Path, to: &Path) -> Result<(), String> {
    bundle::copy_tree(from, to)
}

pub(super) fn remove(path: &Path) {
    match fs::symlink_metadata(path) {
        Ok(m) if m.is_dir() => {
            let _ = fs::remove_dir_all(path);
        }
        Ok(_) => {
            let _ = fs::remove_file(path);
        }
        Err(_) => {}
    }
}

/// A checked bundle in its staging folder, ready to swap in. The folder is removed if it is not
/// swapped in, only while it is still the folder made (`leftovers::Folder`).
pub(super) struct Staged {
    digest: String,
    staging: leftovers::Folder,
    bundle: PathBuf,
}

impl Staged {
    pub(super) fn digest(&self) -> &str {
        &self.digest
    }
}

/// A new staging folder of this user's, recorded in the updates folder dir (`leftovers::Folder`):
/// `<dir>/staging/<hex>` when that is on the installed bundle's volume and every folder from the
/// home folder down to it is this user's alone (`private_path`, refused otherwise); on another
/// volume, `.pluxy-update-<hex>` next to the bundle.
fn staging(installed: &Path, dir: &Path) -> Result<leftovers::Folder, String> {
    let folder = installed.parent().ok_or("The app bundle has no folder.")?;
    let mut nonce = [0u8; 8];
    getrandom::fill(&mut nonce).map_err(|e| e.to_string())?;
    let hex = crate::artifact::hex(&nonce);
    let private = dir.join("staging");
    super::private_dir(&private)?;
    if !same_volume(folder, &private)? {
        let path = folder.join(format!(".pluxy-update-{hex}"));
        #[cfg(test)]
        tests::seen("make", &path);
        return leftovers::Folder::new(dir, &path);
    }
    let path = private.join(hex);
    #[cfg(test)]
    tests::seen("make", &path);
    let made = leftovers::Folder::new(dir, &path)?;
    // SAFETY: geteuid has no preconditions.
    let me = unsafe { libc::geteuid() };
    // Refused, the folder just made goes with made.
    private_path(&home()?, made.path(), me, look)?;
    Ok(made)
}

/// Whether the folders a and b are on one volume (one device): only then can one swap reach both.
fn same_volume(a: &Path, b: &Path) -> Result<bool, String> {
    use std::os::unix::fs::MetadataExt;
    #[cfg(test)]
    if tests::another_volume() {
        return Ok(false);
    }
    let dev = |p: &Path| {
        fs::metadata(p)
            .map(|m| m.dev())
            .map_err(|e| format!("{}: {e}", p.display()))
    };
    Ok(dev(a)? == dev(b)?)
}

/// The home folder the app's data folder is in: `HOME`, which Tauri's data folder is made from
/// too.
#[cfg(not(test))]
fn home() -> Result<PathBuf, String> {
    std::env::var_os("HOME")
        .map(PathBuf::from)
        .filter(|h| h.is_absolute())
        .ok_or_else(|| {
            "HOME is not set to a folder, so the update has nowhere private to go.".into()
        })
}

/// In tests, the folder a test set (`tests::Home`), else the system's temporary folder.
#[cfg(test)]
fn home() -> Result<PathBuf, String> {
    Ok(tests::home())
}

/// What `private_path` reads of a path without following a link: whether it is a folder, its
/// owner and its mode.
#[derive(Clone, Copy, Debug)]
struct Look {
    folder: bool,
    uid: u32,
    mode: u32,
}

fn look(path: &Path) -> io::Result<Look> {
    use std::os::unix::fs::MetadataExt;
    let m = fs::symlink_metadata(path)?;
    Ok(Look {
        folder: m.is_dir(),
        uid: m.uid(),
        mode: m.mode(),
    })
}

/// Refused unless path is in the home folder home and every folder from home down to path,
/// both included, is a folder (not a link) owned by me that no one else can write to (no group or
/// other write). No other account can then rename or replace any of them, so what the updater
/// unpacks and checks there stays what it checked. look reads each.
fn private_path(
    home: &Path,
    path: &Path,
    me: u32,
    look: impl Fn(&Path) -> io::Result<Look>,
) -> Result<(), String> {
    let elsewhere = || {
        format!(
            "{} is not in your home folder ({}), so the update has nowhere private to go.",
            path.display(),
            home.display()
        )
    };
    let rest = path.strip_prefix(home).map_err(|_| elsewhere())?;
    let mut at = home.to_path_buf();
    let mut folders = vec![at.clone()];
    for c in rest.components() {
        let Component::Normal(name) = c else {
            return Err(elsewhere());
        };
        at.push(name);
        folders.push(at.clone());
    }
    for f in folders {
        let l = look(&f).map_err(|e| format!("{}: {e}", f.display()))?;
        if !l.folder || l.uid != me || l.mode & 0o022 != 0 {
            return Err(format!(
                "{} is not a folder of yours that only you can change, so another account could \
                 move what the update unpacks under it. Make it yours alone (no group or other \
                 write) and update then.",
                f.display()
            ));
        }
    }
    Ok(())
}

/// Unpacks the update's verified archive next to the installed bundle and checks it: the same
/// identifier and program as the installed bundle, the announced version, a program for this Mac.
pub(super) fn stage(
    installed: &Path,
    bytes: Vec<u8>,
    version: &str,
    updates: &Path,
) -> Result<Staged, String> {
    let want = bundle::info(installed)?;
    let staging = staging(installed, updates)?;
    let dir = staging.path().to_path_buf();
    #[cfg(test)]
    tests::seen("unpack", &dir);
    let mut s = Staged {
        digest: String::new(),
        bundle: PathBuf::new(),
        staging,
    };
    s.bundle = bundle::unpack(&bytes, &dir)?;
    bundle::check(&s.bundle, &want, Some(version), CPU)?;
    s.digest = bundle::tree_digest(&s.bundle)?;
    Ok(s)
}

/// The kept bundle at kept, copied into a staging folder (`staging`), if the copy still has its
/// pinned digest and is this app's.
pub(super) fn stage_kept(
    installed: &Path,
    kept: &Path,
    pin: &Pinned,
    updates: &Path,
) -> Result<Staged, String> {
    let want = bundle::info(installed)?;
    let staging = staging(installed, updates)?;
    let name = installed.file_name().ok_or("The app bundle has no name.")?;
    let s = Staged {
        digest: pin.sha256.clone(),
        bundle: staging.path().join(name),
        staging,
    };
    #[cfg(test)]
    tests::seen("copy", &s.bundle);
    bundle::copy_tree(kept, &s.bundle)
        .map_err(|e| format!("the kept copy {}: {e}", kept.display()))?;
    if bundle::tree_digest(&s.bundle).ok().as_deref() != Some(pin.sha256.as_str())
        || bundle::check(&s.bundle, &want, None, CPU).is_err()
    {
        return Err(format!("the kept copy {} changed", kept.display()));
    }
    Ok(s)
}

/// Swaps staged with the installed bundle in one step, by name in each one's folder, both opened
/// without following a link. The bundle's folder must still be a folder this user owns that no
/// one else can write to, read through the descriptor the swap goes through.
pub(super) fn swap_in(installed: &Path, mut staged: Staged, _updates: &Path) -> Result<(), String> {
    use std::os::unix::fs::MetadataExt;
    let shown = |p: &Path, e: io::Error| format!("{}: {e}", p.display());
    let (folder, name) = named(installed)?;
    let (from, from_name) = named(&staged.bundle)?;
    let open = |p: &Path| open_folder(p).map_err(|e| shown(p, e));
    let to = open(folder)?;
    let m = to.metadata().map_err(|e| shown(folder, e))?;
    // SAFETY: geteuid has no preconditions.
    if !m.is_dir() || m.uid() != unsafe { libc::geteuid() } || m.mode() & 0o022 != 0 {
        return Err(format!(
            "{} is not a folder of yours that only you can change; the app is not updated there.",
            folder.display()
        ));
    }
    let from = open(from)?;
    #[cfg(test)]
    {
        tests::seen("swap", &staged.bundle);
        tests::seen("swap", installed);
    }
    swap_at(&from, &from_name, &to, &name).map_err(|e| shown(installed, e))?;
    // The old bundle is in the staging folder now: it stays until the old version has ended.
    staged.staging.keep();
    Ok(())
}

/// The folder path is in, and its name there.
fn named(p: &Path) -> Result<(&Path, CString), String> {
    let folder = p
        .parent()
        .ok_or_else(|| format!("{} has no folder", p.display()))?;
    let name = p
        .file_name()
        .ok_or_else(|| format!("{} has no name", p.display()))?;
    Ok((
        folder,
        CString::new(name.as_bytes()).map_err(|e| e.to_string())?,
    ))
}

/// The folder at path, opened to act relative to it, without following a link.
fn open_folder(path: &Path) -> io::Result<fs::File> {
    use std::os::unix::fs::OpenOptionsExt;
    fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC)
        .open(path)
}

/// Swaps the entry from_name in the folder open as from with to_name in the folder open as to, in
/// one step.
#[cfg(target_os = "macos")]
fn swap_at(
    from: &fs::File,
    from_name: &CString,
    to: &fs::File,
    to_name: &CString,
) -> io::Result<()> {
    use std::os::unix::io::AsRawFd;
    // SAFETY: both descriptors are open folders and both names NUL-terminated.
    let r = unsafe {
        libc::renameatx_np(
            from.as_raw_fd(),
            from_name.as_ptr(),
            to.as_raw_fd(),
            to_name.as_ptr(),
            libc::RENAME_SWAP,
        )
    };
    if r != 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(())
}

/// The same on Linux, for the tests: `renameat2` with `RENAME_EXCHANGE`.
#[cfg(target_os = "linux")]
fn swap_at(
    from: &fs::File,
    from_name: &CString,
    to: &fs::File,
    to_name: &CString,
) -> io::Result<()> {
    use std::os::unix::io::AsRawFd;
    // SAFETY: both descriptors are open folders and both names NUL-terminated.
    let r = unsafe {
        libc::renameat2(
            from.as_raw_fd(),
            from_name.as_ptr(),
            to.as_raw_fd(),
            to_name.as_ptr(),
            libc::RENAME_EXCHANGE,
        )
    };
    if r != 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(())
}

/// What starts the installed bundle: its program, by the name this process has (`here` checked
/// the bundle names it).
pub(super) fn program(installed: &Path) -> PathBuf {
    let name = std::env::current_exe()
        .ok()
        .and_then(|e| e.file_name().map(PathBuf::from))
        .unwrap_or_default();
    installed.join("Contents").join("MacOS").join(name)
}

/// Removes the staging folders an earlier install recorded in the updates folder dir (an old
/// version swapped out, or what a failed install left), each only while it is still the folder
/// recorded, never through a link (`leftovers::remove_folder`): in private staging, and next to
/// the bundle only where an install on another volume (or a version before private staging) made
/// one there. No folder is listed.
pub(super) fn cleanup(dir: &Path) {
    leftovers::clean(dir, |m| {
        #[cfg(test)]
        tests::seen("remove", &m.path);
        leftovers::remove_folder(m)
    });
}

/// The process the version put back after a failed update starts after: this one.
pub(super) fn restart_after() -> i32 {
    std::process::id() as i32
}

#[cfg(test)]
pub(super) mod tests {
    use super::*;
    use std::cell::{Cell, RefCell};

    thread_local! {
        /// What the updater did to folder entries on this test's thread, in order: the operation
        /// and the entry it made, filled, swapped or removed.
        static SEEN: RefCell<Vec<(&'static str, PathBuf)>> = const { RefCell::new(Vec::new()) };
        /// The home folder `private_path` starts from on this thread (`temp`).
        static HOME: RefCell<Option<PathBuf>> = const { RefCell::new(None) };
        /// Set: on this thread the bundle's folder is on another volume than the updates folder.
        static ANOTHER: Cell<bool> = const { Cell::new(false) };
    }

    pub(super) fn seen(op: &'static str, path: &Path) {
        SEEN.with(|s| s.borrow_mut().push((op, path.to_path_buf())));
    }

    pub(super) fn home() -> PathBuf {
        HOME.with(|h| h.borrow().clone())
            .unwrap_or_else(std::env::temp_dir)
    }

    pub(super) fn another_volume() -> bool {
        ANOTHER.with(Cell::get)
    }

    /// A folder of this user's alone for a test, which is also its home folder on this thread.
    fn temp(name: &str) -> PathBuf {
        use std::os::unix::fs::PermissionsExt;
        let tmp = super::super::tests::temp(name);
        fs::set_permissions(&tmp, fs::Permissions::from_mode(0o700)).unwrap();
        HOME.with(|h| *h.borrow_mut() = Some(tmp.clone()));
        SEEN.with(|s| s.borrow_mut().clear());
        ANOTHER.with(|a| a.set(false));
        tmp
    }

    /// An installed bundle `<tmp>/pluxy Test.app` whose program says v.
    pub(crate) fn fixture(tmp: &Path, v: &str) -> PathBuf {
        made(tmp, "0.1.0", v)
    }

    /// A bundle `<dir>/pluxy Test.app` of version, whose program, for this Mac, says v.
    fn made(dir: &Path, version: &str, v: &str) -> PathBuf {
        let b = bundle::tests::make(dir, version, v);
        let exe = b.join("Contents/MacOS/pluxy-app-test");
        let mut bytes = fs::read(&exe).unwrap();
        bytes[4..8].copy_from_slice(&CPU.to_le_bytes());
        fs::write(&exe, bytes).unwrap();
        b
    }

    /// Changes a kept bundle.
    pub(crate) fn tamper(path: &Path) {
        let icon = path.join("Contents/Resources/icon.icns");
        let mut b = fs::read(&icon).unwrap();
        b.push(0);
        fs::write(icon, b).unwrap();
    }

    #[test]
    fn a_translocated_or_non_apfs_bundle_is_refused() {
        assert!(translocated(Path::new(
            "/private/var/folders/ab/T/AppTranslocation/1234/d/pluxy.app"
        )));
        assert!(!translocated(Path::new("/Applications/pluxy.app")));
        for kind in ["hfs", "msdos", "exfat", "smbfs", "nfs"] {
            let e = swap_volume(kind, Path::new("/Volumes/x")).unwrap_err();
            assert!(e.contains("APFS"), "{kind}: {e}");
        }
        swap_volume("apfs", Path::new("/Applications")).unwrap();
    }

    /// The entries of folder whose names start with prefix, sorted; none if it is missing.
    fn entries(folder: &Path, prefix: &str) -> Vec<PathBuf> {
        let Ok(rd) = fs::read_dir(folder) else {
            return vec![];
        };
        let mut v: Vec<PathBuf> = rd
            .flatten()
            .filter(|e| e.file_name().to_string_lossy().starts_with(prefix))
            .map(|e| e.path())
            .collect();
        v.sort();
        v
    }

    /// The staging folders beside the bundle in tmp.
    fn beside(tmp: &Path) -> Vec<PathBuf> {
        entries(tmp, ".pluxy-update-")
    }

    /// The private staging folders in the updates folder dir.
    fn private(dir: &Path) -> Vec<PathBuf> {
        entries(&dir.join("staging"), "")
    }

    // A staged bundle dropped without being swapped in (a failed or cancelled install) removes its
    // staging folder only while that is still the folder made: a folder put at its name stays.
    #[test]
    fn a_dropped_stage_removes_only_the_folder_it_made() {
        let tmp = temp("app-drop");
        let dir = tmp.join("updates");
        let installed = fixture(&tmp, "old");
        let kept = tmp.join("kept.app");
        copy_new(&installed, &kept).unwrap();
        let pin = Pinned {
            version: "0.1.0".into(),
            sha256: digest(&installed).unwrap(),
        };
        drop(stage_kept(&installed, &kept, &pin, &dir).unwrap());
        assert!(private(&dir).is_empty(), "dropped as made: removed");
        let s = stage_kept(&installed, &kept, &pin, &dir).unwrap();
        let staged = private(&dir).remove(0);
        fs::rename(&staged, tmp.join("moved")).unwrap();
        fs::create_dir(&staged).unwrap();
        fs::write(staged.join("theirs"), b"z").unwrap();
        drop(s);
        assert!(staged.join("theirs").exists(), "not the folder made");
        assert!(beside(&tmp).is_empty());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // The swap goes between two folders: the new bundle in the bundle's folder and the old one in
    // the private staging folder, which cleanup removes while the bundle in place stays. A kept
    // copy that changed is refused and its staging folder removed.
    #[test]
    fn the_swap_goes_between_two_folders_and_cleanup_removes_only_staging() {
        let tmp = temp("app-swap");
        let dir = tmp.join("updates");
        let installed = fixture(&tmp, "old");
        let old = digest(&installed).unwrap();
        let kept = tmp.join("kept.app");
        copy_new(&installed, &kept).unwrap();
        let pin = Pinned {
            version: "0.1.0".into(),
            sha256: old.clone(),
        };
        fixture(&tmp, "new");
        let new = digest(&installed).unwrap();
        let s = stage_kept(&installed, &kept, &pin, &dir).unwrap();
        swap_in(&installed, s, &dir).unwrap();
        assert_eq!(digest(&installed).unwrap(), old);
        assert!(beside(&tmp).is_empty(), "nothing staged beside the bundle");
        let staged = private(&dir);
        assert_eq!(staged.len(), 1);
        let swapped_out = staged[0].join("pluxy Test.app");
        assert_eq!(digest(&swapped_out).unwrap(), new);
        // The next version's cleanup, once this one has ended.
        leftovers::forget_mine(&tmp);
        cleanup(&dir);
        assert!(!staged[0].exists());
        assert_eq!(digest(&installed).unwrap(), old);
        // A kept copy that changed is not staged, and its staging folder goes.
        tamper(&kept);
        assert!(stage_kept(&installed, &kept, &pin, &dir).is_err());
        assert!(private(&dir).is_empty());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // On another volume the staging folder is made beside the bundle. Only a staging folder an
    // install recorded is removed: a folder another user renamed to the staging pattern beside
    // the bundle stays, and so does a folder of the same name put in place of a recorded one.
    #[test]
    fn on_another_volume_cleanup_leaves_folders_it_did_not_record_beside_the_bundle() {
        let tmp = temp("app-cleanup");
        ANOTHER.with(|a| a.set(true));
        let dir = tmp.join("updates");
        let installed = fixture(&tmp, "old");
        let renamed = tmp.join(".pluxy-update-0123456789abcdef");
        fs::create_dir(&renamed).unwrap();
        fs::write(renamed.join("theirs"), b"x").unwrap();
        let kept = tmp.join("kept.app");
        copy_new(&installed, &kept).unwrap();
        let pin = Pinned {
            version: "0.1.0".into(),
            sha256: digest(&installed).unwrap(),
        };
        let s = stage_kept(&installed, &kept, &pin, &dir).unwrap();
        swap_in(&installed, s, &dir).unwrap();
        assert!(private(&dir).is_empty(), "staged beside the bundle");
        let recorded = beside(&tmp).into_iter().find(|p| *p != renamed).unwrap();
        fs::rename(&recorded, tmp.join("moved")).unwrap();
        fs::create_dir(&recorded).unwrap();
        fs::write(recorded.join("theirs"), b"y").unwrap();
        leftovers::forget_mine(&tmp);
        cleanup(&dir);
        assert!(renamed.join("theirs").exists(), "never recorded");
        assert!(recorded.join("theirs").exists(), "not the folder recorded");
        assert!(installed.exists());
        fs::remove_dir_all(&tmp).unwrap();
    }

    /// The update archive of a bundle of version whose program says v (`.app.tar.gz`).
    fn archive(tmp: &Path, version: &str, v: &str) -> Vec<u8> {
        let src = tmp.join(format!("src-{version}"));
        let b = made(&src, version, v);
        let gz = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::fast());
        let mut t = tar::Builder::new(gz);
        t.append_dir_all("pluxy Test.app", &b).unwrap();
        let bytes = t.into_inner().unwrap().finish().unwrap();
        fs::remove_dir_all(src).unwrap();
        bytes
    }

    /// The names in folder with what each is (device and inode), sorted.
    fn listing(folder: &Path) -> Vec<(String, String)> {
        use std::os::unix::fs::MetadataExt;
        let mut v: Vec<(String, String)> = fs::read_dir(folder)
            .unwrap()
            .flatten()
            .map(|e| {
                let m = fs::symlink_metadata(e.path()).unwrap();
                (
                    e.file_name().to_string_lossy().into_owned(),
                    format!("{}:{}", m.dev(), m.ino()),
                )
            })
            .collect();
        v.sort();
        v
    }

    // A whole update and a rollback, each followed by the next version's cleanup, change nothing
    // in the bundle's folder but its one entry, and only by the swap: the staging folders are made,
    // filled and removed in private staging, never next to the bundle, and the folder is never
    // listed.
    #[test]
    fn an_update_and_a_rollback_change_only_the_bundles_own_entry() {
        let tmp = temp("app-only-swap");
        let dir = tmp.join("updates");
        let folder = tmp.join("Applications");
        let installed = fixture(&folder, "old");
        let name = |l: &[(String, String)]| l.iter().map(|(n, _)| n.clone()).collect::<Vec<_>>();
        let before = listing(&folder);
        assert_eq!(name(&before), ["pluxy Test.app"]);
        let old = digest(&installed).unwrap();
        let bytes = archive(&tmp, "0.2.0", "new");
        SEEN.with(|s| s.borrow_mut().clear());

        // A copy of the running version kept, as `keep` makes it (`keep` itself goes by this
        // system's format, which on Linux is not this one).
        let keep = |version: &str, sha256: &str| {
            let pin = Pinned {
                version: version.into(),
                sha256: sha256.into(),
            };
            let kept = super::super::kept_path(&dir, &pin);
            super::super::private_dir(kept.parent().unwrap()).unwrap();
            copy_new(&installed, &kept).unwrap();
            assert_eq!(digest(&kept).unwrap(), sha256);
            (pin, kept)
        };

        // Update to 0.2.0, as `install` does it.
        let staged = stage(&installed, bytes, "0.2.0", &dir).unwrap();
        let new = staged.digest().to_string();
        let (from, kept) = keep("0.1.0", &old);
        swap_in(&installed, staged, &dir).unwrap();
        assert_eq!(digest(&installed).unwrap(), new);
        let updated = listing(&folder);
        assert_eq!(name(&updated), ["pluxy Test.app"]);
        assert_ne!(updated, before, "the bundle's entry is the new bundle");
        // 0.2.0 started: its cleanup removes the old bundle from private staging.
        leftovers::forget_mine(&tmp);
        cleanup(&dir);
        assert!(private(&dir).is_empty());

        // Roll back to 0.1.0, as `roll_back` does it.
        let staged = stage_kept(&installed, &kept, &from, &dir).unwrap();
        keep("0.2.0", &new);
        swap_in(&installed, staged, &dir).unwrap();
        assert_eq!(digest(&installed).unwrap(), old);
        leftovers::forget_mine(&tmp);
        cleanup(&dir);
        assert!(private(&dir).is_empty());
        assert_eq!(name(&listing(&folder)), ["pluxy Test.app"]);

        let seen = SEEN.with(|s| std::mem::take(&mut *s.borrow_mut()));
        let in_folder: Vec<_> = seen
            .iter()
            .filter(|(_, p)| p.starts_with(&folder))
            .cloned()
            .collect();
        assert_eq!(
            in_folder,
            [("swap", installed.clone()), ("swap", installed.clone())],
            "operations on the bundle's folder: {seen:?}"
        );
        for (op, p) in &seen {
            assert!(
                p.starts_with(&folder) || p.starts_with(dir.join("staging")),
                "{op} {} is neither the bundle nor in private staging",
                p.display()
            );
        }
        for op in ["make", "unpack", "copy", "remove"] {
            assert!(seen.iter().any(|(o, _)| *o == op), "no {op} seen: {seen:?}");
        }
        fs::remove_dir_all(&tmp).unwrap();
    }

    // Nothing is staged under a folder another account could change: the home folder or one
    // below it down to the staging folder that others can write to is refused, and the folder
    // made for it goes.
    #[test]
    fn staging_is_refused_under_a_folder_others_can_change() {
        use std::os::unix::fs::PermissionsExt;
        let tmp = temp("app-private");
        let dir = tmp.join("updates");
        let installed = fixture(&tmp, "old");
        let kept = tmp.join("kept.app");
        copy_new(&installed, &kept).unwrap();
        let pin = Pinned {
            version: "0.1.0".into(),
            sha256: digest(&installed).unwrap(),
        };
        drop(stage_kept(&installed, &kept, &pin, &dir).unwrap());
        for (open, mode) in [(dir.clone(), 0o770), (tmp.clone(), 0o707)] {
            fs::set_permissions(&open, fs::Permissions::from_mode(mode)).unwrap();
            let e = stage_kept(&installed, &kept, &pin, &dir).err().unwrap();
            fs::set_permissions(&open, fs::Permissions::from_mode(0o700)).unwrap();
            assert!(
                e.contains(&format!("{} is not a folder of yours", open.display())),
                "{e}"
            );
            assert!(private(&dir).is_empty(), "the folder made for it went");
        }
        fs::remove_dir_all(&tmp).unwrap();
    }

    // The swap goes through the bundle's folder only while that is still a folder of this user's
    // that no one else can write to, as read through the descriptor the swap uses: otherwise
    // nothing is swapped.
    #[test]
    fn the_swap_is_refused_once_the_bundles_folder_is_not_yours_alone() {
        use std::os::unix::fs::PermissionsExt;
        let tmp = temp("app-swap-folder");
        let dir = tmp.join("updates");
        let folder = tmp.join("Applications");
        let installed = fixture(&folder, "old");
        let old = digest(&installed).unwrap();
        let kept = tmp.join("kept.app");
        copy_new(&installed, &kept).unwrap();
        let pin = Pinned {
            version: "0.1.0".into(),
            sha256: old.clone(),
        };
        fixture(&folder, "new");
        let new = digest(&installed).unwrap();
        let s = stage_kept(&installed, &kept, &pin, &dir).unwrap();
        fs::set_permissions(&folder, fs::Permissions::from_mode(0o770)).unwrap();
        let e = swap_in(&installed, s, &dir).unwrap_err();
        assert!(e.contains("is not a folder of yours"), "{e}");
        assert_eq!(digest(&installed).unwrap(), new, "nothing was swapped");
        assert!(private(&dir).is_empty(), "the staging folder went");
        fs::set_permissions(&folder, fs::Permissions::from_mode(0o700)).unwrap();
        fs::remove_dir_all(&tmp).unwrap();
    }

    // The staging path check, with the values it reads given: every folder from the home folder
    // down to the staging folder must be a folder, this user's, with no group or other write;
    // the staging folder must be in the home folder.
    #[test]
    fn a_staging_path_is_private_only_if_every_folder_from_home_is_yours_alone() {
        let home = Path::new("/Users/me");
        let path = Path::new("/Users/me/Library/Application Support/id/updates/staging/ab");
        let ok = Look {
            folder: true,
            uid: 501,
            mode: 0o40700,
        };
        let check = |bad: Option<(&Path, Option<Look>)>| {
            private_path(home, path, 501, |p| match bad {
                Some((at, l)) if at == p => {
                    l.ok_or_else(|| io::Error::from(io::ErrorKind::PermissionDenied))
                }
                _ => Ok(ok),
            })
        };
        check(None).unwrap();
        // Group read and other execute, as the home folder has: fine.
        private_path(home, path, 501, |_| {
            Ok(Look {
                mode: 0o40751,
                ..ok
            })
        })
        .unwrap();
        let folders: Vec<&Path> = path
            .ancestors()
            .take_while(|a| a.starts_with(home))
            .collect();
        assert_eq!(folders.len(), 7);
        let wrong = [
            Some(Look {
                mode: 0o40770,
                ..ok
            }),
            Some(Look {
                mode: 0o40702,
                ..ok
            }),
            Some(Look {
                mode: 0o41777,
                ..ok
            }),
            Some(Look { uid: 0, ..ok }),
            Some(Look { uid: 502, ..ok }),
            Some(Look {
                folder: false,
                ..ok
            }),
            None,
        ];
        for at in &folders {
            for l in wrong {
                let e = check(Some((at, l))).unwrap_err();
                assert!(e.contains(&at.display().to_string()), "{at:?} {l:?}: {e}");
            }
        }
        for outside in [
            "/Users/other/Library/x",
            "/Users/me/../other/x",
            "/Users/meow/x",
            "/tmp/x",
        ] {
            let e = private_path(home, Path::new(outside), 501, |_| Ok(ok)).unwrap_err();
            assert!(e.contains("not in your home folder"), "{outside}: {e}");
        }
    }
}
