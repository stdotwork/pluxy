//! macOS: the `.app` bundle this user keeps, run as `<name>.app/Contents/MacOS/<program>`, pinned
//! by its tree digest (`bundle::tree_digest`). The update is a `.app.tar.gz` (Tauri's macOS update
//! layout), unpacked under strict rules (`bundle::unpack`) into a new staging folder of this
//! user's, checked (identifier, program name, announced version, a program for this Mac), then
//! swapped with the installed bundle in one step (`renameatx_np` with `RENAME_SWAP`, relative to
//! the two folders' open descriptors): the bundle is the old one or the new one, never missing.
//!
//! The staging folder is private (`updates/staging/<hex>` in the app's data folder) whenever that
//! is on the bundle's volume, after checking that every folder from the home folder down to it is
//! this user's alone, by its mode and its access list (`private_path`, `acl`); the staging folder
//! made there takes no access list entry from its folder (`leftovers::Folder`). Then nothing in
//! the bundle's folder changes but the bundle's own entry, at the swap. Only on another volume,
//! where a swap cannot reach, is it made next to the bundle (`.pluxy-update-<hex>`), in the folder
//! the bundle's checks (mode and access list, as the bundle's own) found this user's alone. The old bundle ends in the staging folder, which is removed once the new version
//! runs and the old one has ended: the folder is recorded when it is made (`leftovers.rs`), and
//! only a recorded folder that is still that folder is removed; the bundle's folder is never
//! listed. A volume that cannot swap (not APFS) is refused, not updated another way.
//!
//! Nothing here asks for an administrator, changes the quarantine attribute or goes through
//! LaunchServices: a bundle this user cannot change is refused.
//!
//! Also built for the tests on Linux, which swap with `renameat2`'s `RENAME_EXCHANGE`.
#![cfg_attr(not(target_os = "macos"), allow(dead_code))]
use super::acl;
use super::bundle;
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
    yours_alone(&bundle)?;
    yours_alone(folder)?;
    swaps(folder)?;
    Ok(bundle)
}

/// Refused unless the folder at path is this user's, not a link, and no one else can change it:
/// no group or other write (`check_owned`) and, read through the folder opened without following
/// a link, no access list entry that lets another account change it or what is in it (`acl`).
fn yours_alone(path: &Path) -> Result<(), String> {
    check_owned(path, true)?;
    let shown = |e: io::Error| format!("{}: {e}", path.display());
    let f = open_folder(path).map_err(shown)?;
    let entries = acl::entries(&f).map_err(shown)?;
    match acl::others(&entries, &acl::yours()?) {
        Some(why) => Err(format!(
            "{} is not a folder of yours that only you can change: {why}; update it the way it \
             was installed.",
            path.display()
        )),
        None => Ok(()),
    }
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
/// volume, `.pluxy-update-<hex>` next to the bundle. The folders from the home folder down to
/// `<dir>/staging` that exist already are checked before anything is made in them.
fn staging(installed: &Path, dir: &Path) -> Result<leftovers::Folder, String> {
    let folder = installed.parent().ok_or("The app bundle has no folder.")?;
    let mut nonce = [0u8; 8];
    getrandom::fill(&mut nonce).map_err(|e| e.to_string())?;
    let hex = crate::artifact::hex(&nonce);
    // SAFETY: geteuid has no preconditions.
    let me = unsafe { libc::geteuid() };
    let yours = acl::yours()?;
    let home = home()?;
    let private = dir.join("staging");
    private_path(&home, &private, me, &yours, look, Missing::Made)?;
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
    // Refused, the folder just made goes with made.
    private_path(&home, made.path(), me, &yours, look, Missing::Refused)?;
    Ok(made)
}

/// Whether the folders a and b are on one volume (one device): only then can one swap reach both.
/// A link at either is refused, not followed.
fn same_volume(a: &Path, b: &Path) -> Result<bool, String> {
    use std::os::unix::fs::MetadataExt;
    let dev = |p: &Path| match fs::symlink_metadata(p) {
        Ok(m) if m.is_dir() => Ok(m.dev()),
        Ok(_) => Err(format!("{} is not a folder", p.display())),
        Err(e) => Err(format!("{}: {e}", p.display())),
    };
    let (a, b) = (dev(a)?, dev(b)?);
    #[cfg(test)]
    if tests::another_volume() {
        return Ok(false);
    }
    Ok(a == b)
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

/// In tests, the folder the test set (`tests::HOME`).
#[cfg(test)]
fn home() -> Result<PathBuf, String> {
    tests::home().ok_or_else(|| "the test set no home folder (app::tests::HOME)".into())
}

/// What `private_path` reads of a path without following a link: whether it is a folder, its
/// owner, its mode and, for a folder, its access list's entries.
#[derive(Clone, Debug)]
struct Look {
    folder: bool,
    uid: u32,
    mode: u32,
    entries: Vec<acl::Entry>,
}

/// A folder is read through one open of it that follows no link: its owner, mode and entries
/// all come from that open folder.
fn look(path: &Path) -> io::Result<Look> {
    use std::os::unix::fs::MetadataExt;
    let m = fs::symlink_metadata(path)?;
    if !m.is_dir() {
        return Ok(Look {
            folder: false,
            uid: m.uid(),
            mode: m.mode(),
            entries: vec![],
        });
    }
    let f = open_folder(path)?;
    let m = f.metadata()?;
    Ok(Look {
        folder: m.is_dir(),
        uid: m.uid(),
        mode: m.mode(),
        entries: acl::entries(&f)?,
    })
}

/// What `private_path` makes of a folder on the path that does not exist.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Missing {
    /// The rest is about to be made (private, by this user): the folders above it are all there is
    /// to check.
    Made,
    /// Refused, as any other failed look.
    Refused,
}

/// Refused unless path is in the home folder home and every folder from home down to path,
/// both included, is a folder (not a link) owned by me that no one else can write to: no group or
/// other write, and no access list entry that lets an account other than those whose UUIDs are in
/// yours change it or what is in it (`acl`; deny entries, such as the stock `everyone deny
/// delete` on the home folder, are fine). No other account can then rename or replace any of
/// them, so what the updater
/// unpacks and checks there stays what it checked. look reads each, by a path built from home's
/// and path's parts alone: no trailing `/` that would make a look follow a link, and no `.` or
/// `..`. With `Missing::Made`, the walk ends at the first folder that does not exist.
fn private_path(
    home: &Path,
    path: &Path,
    me: u32,
    yours: &[[u8; 16]],
    look: impl Fn(&Path) -> io::Result<Look>,
    missing: Missing,
) -> Result<(), String> {
    let elsewhere = || {
        format!(
            "{} is not in your home folder ({}), so the update has nowhere private to go.",
            path.display(),
            home.display()
        )
    };
    // Each path rebuilt from its parts: only the root and plain names.
    let plain = |p: &Path| -> Option<Vec<std::ffi::OsString>> {
        let mut c = p.components();
        if c.next() != Some(Component::RootDir) {
            return None;
        }
        c.map(|c| match c {
            Component::Normal(n) => Some(n.to_os_string()),
            _ => None,
        })
        .collect()
    };
    let (Some(home_parts), Some(parts)) = (plain(home), plain(path)) else {
        return Err(elsewhere());
    };
    if parts.len() < home_parts.len() || parts[..home_parts.len()] != home_parts[..] {
        return Err(elsewhere());
    }
    let mut at = PathBuf::from("/");
    let mut folders = vec![];
    for (i, name) in parts.iter().enumerate() {
        at.push(name);
        if i + 1 >= home_parts.len() {
            folders.push(at.clone());
        }
    }
    for f in folders {
        let l = match look(&f) {
            Err(e) if missing == Missing::Made && e.kind() == io::ErrorKind::NotFound => break,
            l => l.map_err(|e| format!("{}: {e}", f.display()))?,
        };
        let others = acl::others(&l.entries, yours);
        if !l.folder || l.uid != me || l.mode & 0o022 != 0 || others.is_some() {
            return Err(format!(
                "{} is not a folder of yours that only you can change{}, so another account could \
                 move what the update unpacks under it. Make it yours alone (no group or other \
                 write, and no access list entry that lets another account change it) and \
                 update then.",
                f.display(),
                others.map(|why| format!(" ({why})")).unwrap_or_default()
            ));
        }
    }
    Ok(())
}

/// Unpacks the update's verified archive into a staging folder (`staging`) and checks it: the same
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
/// without following a link. Each is checked through the descriptor the swap goes through: the
/// bundle's folder must still be a folder this user owns that no one else can write to (by its
/// mode and its access list, `acl`), and the staging folder still the one made
/// (`leftovers::Folder`), this user's and 0700.
pub(super) fn swap_in(installed: &Path, mut staged: Staged, _updates: &Path) -> Result<(), String> {
    use std::os::unix::fs::MetadataExt;
    let shown = |p: &Path, e: io::Error| format!("{}: {e}", p.display());
    let (folder, name) = named(installed)?;
    let (staging, from_name) = named(&staged.bundle)?;
    if staging != staged.staging.path() {
        return Err(format!(
            "{} is not in its staging folder",
            staged.bundle.display()
        ));
    }
    // SAFETY: geteuid has no preconditions.
    let me = unsafe { libc::geteuid() };
    let to = open_folder(folder).map_err(|e| shown(folder, e))?;
    let m = to.metadata().map_err(|e| shown(folder, e))?;
    let entries = acl::entries(&to).map_err(|e| shown(folder, e))?;
    let others = acl::others(&entries, &acl::yours()?);
    if !m.is_dir() || m.uid() != me || m.mode() & 0o022 != 0 || others.is_some() {
        return Err(format!(
            "{} is not a folder of yours that only you can change{}; the app is not updated there.",
            folder.display(),
            others.map(|why| format!(" ({why})")).unwrap_or_default()
        ));
    }
    // The staging folder: next to the bundle, opened through the bundle's folder just checked;
    // in private staging, by its path, whose folders `staging` checked. Either way it must still
    // be the folder made: the same device and inode, this user's, 0700.
    let (staging_folder, staging_name) = named(staging)?;
    let from = if staging_folder == folder {
        open_in(&to, &staging_name)
    } else {
        open_folder(staging)
    }
    .map_err(|e| shown(staging, e))?;
    let m = from.metadata().map_err(|e| shown(staging, e))?;
    if !m.is_dir()
        || m.uid() != me
        || m.mode() & 0o7777 != 0o700
        || format!("{}:{}", m.dev(), m.ino()) != staged.staging.id()
    {
        return Err(format!(
            "{} is no longer the staging folder this update made; nothing was swapped.",
            staging.display()
        ));
    }
    #[cfg(test)]
    {
        tests::seen("swap", &staged.bundle);
        tests::seen("swap", installed);
        tests::before_swap(installed);
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

/// The folder name in the folder open as dir, opened to act relative to it, without following a
/// link.
fn open_in(dir: &fs::File, name: &CString) -> io::Result<fs::File> {
    use std::os::unix::io::{AsRawFd, FromRawFd};
    // SAFETY: dir is an open folder and name NUL-terminated; a descriptor given back is owned
    // below.
    let fd = unsafe {
        libc::openat(
            dir.as_raw_fd(),
            name.as_ptr(),
            libc::O_RDONLY | libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC,
        )
    };
    if fd < 0 {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: fd was just opened and nothing else owns it.
    Ok(unsafe { fs::File::from_raw_fd(fd) })
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
        /// Run by `swap_in` on this thread after its checks, just before the swap, with the
        /// installed bundle's path.
        static BEFORE_SWAP: Cell<Option<fn(&Path)>> = const { Cell::new(None) };
    }

    pub(super) fn before_swap(installed: &Path) {
        if let Some(f) = BEFORE_SWAP.with(Cell::get) {
            f(installed);
        }
    }

    pub(super) fn seen(op: &'static str, path: &Path) {
        SEEN.with(|s| s.borrow_mut().push((op, path.to_path_buf())));
    }

    pub(super) fn home() -> Option<PathBuf> {
        HOME.with(|h| h.borrow().clone())
    }

    /// Makes tmp, a test's own folder, this user's alone and the home folder on this thread, for
    /// the updater's tests that stage a bundle (`mod.rs`).
    pub(crate) fn home_at(tmp: &Path) {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(tmp, fs::Permissions::from_mode(0o700)).unwrap();
        HOME.with(|h| *h.borrow_mut() = Some(tmp.to_path_buf()));
    }

    pub(super) fn another_volume() -> bool {
        ANOTHER.with(Cell::get)
    }

    /// A folder of this user's alone for a test, which is also its home folder on this thread.
    fn temp(name: &str) -> PathBuf {
        let tmp = super::super::tests::temp(name);
        home_at(&tmp);
        SEEN.with(|s| s.borrow_mut().clear());
        ANOTHER.with(|a| a.set(false));
        BEFORE_SWAP.with(|b| b.set(None));
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
        // A folder of the staging pattern this updater never recorded, as another administrator
        // could rename one of the user's to: it stays as it is.
        let decoy = folder.join(".pluxy-update-0123456789abcdef");
        fs::create_dir(&decoy).unwrap();
        fs::write(decoy.join("theirs"), b"x").unwrap();
        let name = |l: &[(String, String)]| l.iter().map(|(n, _)| n.clone()).collect::<Vec<_>>();
        let before = listing(&folder);
        let names = [".pluxy-update-0123456789abcdef", "pluxy Test.app"];
        assert_eq!(name(&before), names);
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
        assert_eq!(name(&updated), names);
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
        let after = listing(&folder);
        assert_eq!(name(&after), names);
        assert_eq!(after[0], before[0], "the decoy is the same folder");
        assert_eq!(fs::read(decoy.join("theirs")).unwrap(), b"x");

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

    // The swap goes through the two folders it checked, not their paths: the bundle's folder
    // moved away after the checks, and another put at its path, the swap still happens in the
    // folder checked, and the folder now at that path is untouched.
    #[test]
    fn the_swap_goes_through_the_folders_it_checked_not_their_paths() {
        let tmp = temp("app-swap-handles");
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
        let s = stage_kept(&installed, &kept, &pin, &dir).unwrap();
        fn replace(installed: &Path) {
            let folder = installed.parent().unwrap();
            fs::rename(folder, folder.with_file_name("Moved")).unwrap();
            fixture(folder, "theirs");
        }
        BEFORE_SWAP.with(|b| b.set(Some(replace)));
        swap_in(&installed, s, &dir).unwrap();
        let theirs = digest(&installed).unwrap();
        let moved = tmp.join("Moved/pluxy Test.app");
        assert_eq!(
            digest(&moved).unwrap(),
            old,
            "swapped in the folder checked"
        );
        assert_ne!(theirs, old, "the folder put at its path was not swapped");
        assert_eq!(
            fs::read(installed.join("Contents/MacOS/pluxy-app-test")).unwrap()[32..],
            *b"theirs"
        );
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A staging folder that is no longer the one made (another folder put at its path, private
    // or next to the bundle) is not swapped in, even when it holds a bundle.
    #[test]
    fn a_staging_folder_put_in_place_of_the_one_made_is_not_swapped_in() {
        use std::os::unix::fs::PermissionsExt;
        for another in [false, true] {
            let tmp = temp(if another {
                "app-staging-replaced-beside"
            } else {
                "app-staging-replaced"
            });
            ANOTHER.with(|a| a.set(another));
            let dir = tmp.join("updates");
            let installed = fixture(&tmp, "old");
            let old = digest(&installed).unwrap();
            let kept = tmp.join("kept.app");
            copy_new(&installed, &kept).unwrap();
            let pin = Pinned {
                version: "0.1.0".into(),
                sha256: old.clone(),
            };
            let s = stage_kept(&installed, &kept, &pin, &dir).unwrap();
            let staging = s.staging.path().to_path_buf();
            fs::rename(&staging, tmp.join("aside")).unwrap();
            fs::create_dir(&staging).unwrap();
            fs::set_permissions(&staging, fs::Permissions::from_mode(0o700)).unwrap();
            fixture(&staging, "theirs");
            let e = swap_in(&installed, s, &dir).unwrap_err();
            assert!(e.contains("no longer the staging folder"), "{another}: {e}");
            assert_eq!(
                digest(&installed).unwrap(),
                old,
                "{another}: nothing swapped"
            );
            fs::remove_dir_all(&tmp).unwrap();
        }
    }

    // Nothing is made where a link stands on the staging path: a home folder given with a
    // trailing `/` over a link, a link at the updates folder or at its staging folder, or a link
    // as the bundle's folder are refused before anything is made or recorded through them.
    #[test]
    fn staging_makes_nothing_through_a_link() {
        let tmp = temp("app-links");
        let installed = fixture(&tmp.join("Applications"), "old");
        let kept = tmp.join("kept.app");
        copy_new(&installed, &kept).unwrap();
        let pin = Pinned {
            version: "0.1.0".into(),
            sha256: digest(&installed).unwrap(),
        };
        let elsewhere = tmp.join("elsewhere");
        fs::create_dir(&elsewhere).unwrap();
        let link = |at: &Path| std::os::unix::fs::symlink(&elsewhere, at).unwrap();
        let empty = |p: &Path| fs::read_dir(p).unwrap().next().is_none();

        // HOME=<link>/ where the link leads to a private folder.
        let home = tmp.join("home");
        link(&home);
        HOME.with(|h| *h.borrow_mut() = Some(PathBuf::from(format!("{}/", home.display()))));
        let e = stage_kept(&installed, &kept, &pin, &home.join("updates"))
            .err()
            .unwrap();
        assert!(
            e.contains(&format!("{} is not a folder", home.display())),
            "{e}"
        );
        assert!(empty(&elsewhere), "made through the home link");
        fs::remove_file(&home).unwrap();
        home_at(&tmp);

        // A link at the updates folder, then at its staging folder.
        let dir = tmp.join("updates");
        link(&dir);
        let e = stage_kept(&installed, &kept, &pin, &dir).err().unwrap();
        assert!(
            e.contains(&format!("{} is not a folder", dir.display())),
            "{e}"
        );
        assert!(empty(&elsewhere), "made through the updates link");
        fs::remove_file(&dir).unwrap();
        fs::create_dir(&dir).unwrap();
        link(&dir.join("staging"));
        let e = stage_kept(&installed, &kept, &pin, &dir).err().unwrap();
        assert!(e.contains("staging is not a folder"), "{e}");
        assert!(empty(&elsewhere), "made through the staging link");
        assert!(
            !dir.join("leftovers.json").exists(),
            "recorded through the staging link"
        );
        fs::remove_file(dir.join("staging")).unwrap();

        // The bundle reached through a link as its folder.
        let linked = tmp.join("Linked");
        std::os::unix::fs::symlink(tmp.join("Applications"), &linked).unwrap();
        let e = stage_kept(&linked.join("pluxy Test.app"), &kept, &pin, &dir)
            .err()
            .unwrap();
        assert!(
            e.contains(&format!("{} is not a folder", linked.display())),
            "{e}"
        );
        assert!(private(&dir).is_empty());
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
            entries: vec![],
        };
        let check = |bad: Option<(&Path, Option<Look>)>| {
            private_path(
                home,
                path,
                501,
                &YOURS,
                |p| match &bad {
                    Some((at, l)) if *at == p => l
                        .clone()
                        .ok_or_else(|| io::Error::from(io::ErrorKind::PermissionDenied)),
                    _ => Ok(ok.clone()),
                },
                Missing::Refused,
            )
        };
        check(None).unwrap();
        // Group read and other execute, as the home folder has: fine.
        private_path(
            home,
            path,
            501,
            &YOURS,
            |_| {
                Ok(Look {
                    mode: 0o40751,
                    ..ok.clone()
                })
            },
            Missing::Refused,
        )
        .unwrap();
        let folders: Vec<&Path> = path
            .ancestors()
            .take_while(|a| a.starts_with(home))
            .collect();
        assert_eq!(folders.len(), 7);
        let wrong = [
            Some(Look {
                mode: 0o40770,
                ..ok.clone()
            }),
            Some(Look {
                mode: 0o40702,
                ..ok.clone()
            }),
            Some(Look {
                mode: 0o41777,
                ..ok.clone()
            }),
            Some(Look {
                uid: 0,
                ..ok.clone()
            }),
            Some(Look {
                uid: 502,
                ..ok.clone()
            }),
            Some(Look {
                folder: false,
                ..ok.clone()
            }),
            None,
        ];
        for at in &folders {
            for l in &wrong {
                let e = check(Some((at, l.clone()))).unwrap_err();
                assert!(e.contains(&at.display().to_string()), "{at:?} {l:?}: {e}");
            }
        }
        for outside in [
            "/Users/other/Library/x",
            "/Users/me/../other/x",
            "/Users/meow/x",
            "/tmp/x",
        ] {
            let e = private_path(
                home,
                Path::new(outside),
                501,
                &YOURS,
                |_| Ok(ok.clone()),
                Missing::Refused,
            )
            .unwrap_err();
            assert!(e.contains("not in your home folder"), "{outside}: {e}");
        }
        // Each folder is looked at by a path of plain names: a home given with a trailing `/`
        // (which would make a look follow a link there) or with `.` in it is looked at without.
        for given in ["/Users/me/", "/Users/./me", "/Users/me/."] {
            let looked = RefCell::new(vec![]);
            private_path(
                Path::new(given),
                path,
                501,
                &YOURS,
                |p| {
                    looked.borrow_mut().push(p.as_os_str().to_owned());
                    Ok(ok.clone())
                },
                Missing::Refused,
            )
            .unwrap();
            let looked = looked.into_inner();
            assert_eq!(looked.len(), 7, "{given}");
            assert_eq!(looked[0], "/Users/me", "{given}");
        }
        // With the rest about to be made, the walk ends at the first folder that is not there;
        // the folders above it still count. Otherwise a missing folder is refused.
        let missing = |at: &'static str, above: Look| {
            let ok = ok.clone();
            move |p: &Path| {
                if p.starts_with(at) {
                    Err(io::Error::from(io::ErrorKind::NotFound))
                } else if p == Path::new("/Users/me/Library") {
                    Ok(above.clone())
                } else {
                    Ok(ok.clone())
                }
            }
        };
        let at = "/Users/me/Library/Application Support/id";
        private_path(
            home,
            path,
            501,
            &YOURS,
            missing(at, ok.clone()),
            Missing::Made,
        )
        .unwrap();
        let e = private_path(
            home,
            path,
            501,
            &YOURS,
            missing(at, ok.clone()),
            Missing::Refused,
        )
        .unwrap_err();
        assert!(e.contains(at), "{e}");
        let open = Look {
            mode: 0o40770,
            ..ok.clone()
        };
        let e =
            private_path(home, path, 501, &YOURS, missing(at, open), Missing::Made).unwrap_err();
        assert!(
            e.contains("/Users/me/Library is not a folder of yours"),
            "{e}"
        );
    }

    /// This user and root, as `acl::tests` names them.
    const YOURS: [[u8; 16]; 2] = [acl::tests::ME, acl::tests::ROOT];

    // The staging path check judges each folder's access list by what it lets another account
    // do: on any folder from the home folder down, an allow entry that lets another account
    // write, or one that only what is made inside inherits, is refused; a deny entry (the stock
    // `everyone deny delete`), a read-only allow entry and this user's own entries are fine.
    #[test]
    fn a_staging_path_is_judged_by_what_its_access_lists_let_others_do() {
        use acl::tests::{entry, ADD_FILE, DELETE, EVERYONE, LIST, ME, STAFF};
        let home = Path::new("/Users/me");
        let path = Path::new("/Users/me/Library/Application Support/id/updates/staging/ab");
        let with = |at: &Path, entries: Vec<acl::Entry>| {
            private_path(
                home,
                path,
                501,
                &YOURS,
                |p| {
                    Ok(Look {
                        folder: true,
                        uid: 501,
                        mode: 0o40700,
                        entries: if p == at { entries.clone() } else { vec![] },
                    })
                },
                Missing::Refused,
            )
        };
        let deny = entry(false, EVERYONE, DELETE, false);
        let folders: Vec<&Path> = path
            .ancestors()
            .take_while(|a| a.starts_with(home))
            .collect();
        assert_eq!(folders.len(), 7);
        for at in folders {
            with(at, vec![deny]).unwrap();
            with(
                at,
                vec![
                    deny,
                    entry(true, EVERYONE, LIST, true),
                    entry(true, ME, ADD_FILE | DELETE, false),
                ],
            )
            .unwrap();
            for (e, what) in [
                (
                    entry(true, STAFF, ADD_FILE, false),
                    "change it or what is in it",
                ),
                (
                    entry(true, STAFF, ADD_FILE, true),
                    "change what is put in it",
                ),
            ] {
                let err = with(at, vec![deny, e]).unwrap_err();
                assert!(
                    err.contains(&format!("{} is not a folder of yours", at.display()))
                        && err.contains("entry 1 of its access list")
                        && err.contains(what),
                    "{at:?} {e:?}: {err}"
                );
            }
        }
    }

    /// Gives path no access list entry (`chmod -N`), then each of entries (`chmod +a`).
    #[cfg(target_os = "macos")]
    fn set_entries(path: &Path, entries: &[&str]) {
        let out = std::process::Command::new("/bin/chmod")
            .arg("-N")
            .arg(path)
            .output()
            .unwrap();
        assert!(out.status.success(), "chmod -N {}: {out:?}", path.display());
        for e in entries {
            crate::private_macos::add_entry(path, e);
        }
    }

    // With real access lists: staging goes through folders whose lists hold only the stock deny
    // entry or read-only allow entries, and the staging folder made takes no entry from its
    // folder, an inheritable one included; an allow entry that lets another account write, or a
    // write entry only what is made inside inherits, on a folder from the home folder down
    // refuses it, and the folder made for it goes.
    #[cfg(target_os = "macos")]
    #[test]
    fn staging_is_judged_by_its_folders_real_access_lists() {
        let tmp = temp("app-acl-staging");
        let dir = tmp.join("updates");
        let staging = dir.join("staging");
        let installed = fixture(&tmp, "old");
        let kept = tmp.join("kept.app");
        copy_new(&installed, &kept).unwrap();
        let pin = Pinned {
            version: "0.1.0".into(),
            sha256: digest(&installed).unwrap(),
        };
        drop(stage_kept(&installed, &kept, &pin, &dir).unwrap());
        let stock = |at: &Path| {
            set_entries(at, &["everyone deny delete"]);
        };
        for at in [&tmp, &dir, &staging] {
            stock(at);
        }
        set_entries(
            &staging,
            &[
                "everyone deny delete",
                "everyone allow list,search,readattr,file_inherit,directory_inherit",
            ],
        );
        let s = stage_kept(&installed, &kept, &pin, &dir).unwrap();
        let made = fs::File::open(s.staging.path()).unwrap();
        assert!(
            !crate::private_macos::has_entries(&made).unwrap(),
            "the staging folder made took an entry from its folder"
        );
        drop(s);
        stock(&staging);
        for (at, e) in [
            (&tmp, "everyone allow delete_child"),
            (&dir, "everyone allow add_file"),
            (&dir, "group:staff allow add_subdirectory"),
            (
                &staging,
                "everyone allow add_file,delete_child,file_inherit,directory_inherit,only_inherit",
            ),
        ] {
            set_entries(at, &["everyone deny delete", e]);
            let err = stage_kept(&installed, &kept, &pin, &dir).err().unwrap();
            stock(at);
            assert!(
                err.contains(&format!("{} is not a folder of yours", at.display()))
                    && err.contains("entry 1 of its access list"),
                "{e}: {err}"
            );
            assert!(private(&dir).is_empty(), "{e}: the folder made for it went");
        }
        for at in [&tmp, &dir, &staging] {
            set_entries(at, &[]);
        }
        fs::remove_dir_all(&tmp).unwrap();
    }

    // With real access lists: the bundle and its folder (`yours_alone`, as `provenance` checks
    // them) pass with the stock deny entry and are refused with an allow entry that lets another
    // account write, or a write entry only what is made inside inherits; the swap reads the
    // bundle's folder's list again through the folder it swaps in, and swaps nothing once it lets
    // another account write.
    #[cfg(target_os = "macos")]
    #[test]
    fn the_bundle_and_its_folder_are_judged_by_their_real_access_lists() {
        let tmp = temp("app-acl-bundle");
        let dir = tmp.join("updates");
        let folder = tmp.join("Applications");
        let installed = fixture(&folder, "old");
        let old = digest(&installed).unwrap();
        for at in [&installed, &folder] {
            set_entries(at, &["everyone deny delete"]);
            yours_alone(at).unwrap();
            for e in [
                "everyone allow add_file",
                "group:staff allow writesecurity",
                "everyone allow add_file,file_inherit,directory_inherit,only_inherit",
            ] {
                set_entries(at, &["everyone deny delete", e]);
                let err = yours_alone(at).unwrap_err();
                assert!(
                    err.contains(&format!("{} is not a folder of yours", at.display()))
                        && err.contains("entry 1 of its access list"),
                    "{e}: {err}"
                );
            }
            set_entries(at, &[]);
        }
        let kept = tmp.join("kept.app");
        copy_new(&installed, &kept).unwrap();
        let pin = Pinned {
            version: "0.1.0".into(),
            sha256: old,
        };
        fixture(&folder, "new");
        let new = digest(&installed).unwrap();
        let s = stage_kept(&installed, &kept, &pin, &dir).unwrap();
        set_entries(
            &folder,
            &["everyone deny delete", "everyone allow add_file"],
        );
        let e = swap_in(&installed, s, &dir).unwrap_err();
        set_entries(&folder, &[]);
        assert!(
            e.contains("is not a folder of yours") && e.contains("entry 1 of its access list"),
            "{e}"
        );
        assert_eq!(digest(&installed).unwrap(), new, "nothing was swapped");
        assert!(private(&dir).is_empty(), "the staging folder went");
        fs::remove_dir_all(&tmp).unwrap();
    }

    // This machine's own home folder, `~/Library` and `~/Library/Application Support`, as they
    // are (on the macOS runners each carries `group:everyone deny delete`), pass the staging path
    // check. Their lists are printed (`ls -led`; shown with --nocapture or on failure).
    #[cfg(target_os = "macos")]
    #[test]
    fn this_macs_own_home_folders_pass_the_staging_path_check() {
        let mut buf = vec![0 as libc::c_char; 16 << 10];
        // SAFETY: getpwuid_r writes only into pw and buf; found points into them or is null; the
        // home folder's name is copied out before they are dropped.
        let home = unsafe {
            let mut pw: libc::passwd = std::mem::zeroed();
            let mut found: *mut libc::passwd = std::ptr::null_mut();
            let r = libc::getpwuid_r(
                libc::geteuid(),
                &mut pw,
                buf.as_mut_ptr(),
                buf.len(),
                &mut found,
            );
            assert!(r == 0 && !found.is_null(), "no account");
            PathBuf::from(std::ffi::OsStr::from_bytes(
                std::ffi::CStr::from_ptr(pw.pw_dir).to_bytes(),
            ))
        };
        let support = home.join("Library/Application Support");
        let ls = std::process::Command::new("/bin/ls")
            .arg("-led")
            .args([&home, &home.join("Library"), &support])
            .output()
            .unwrap();
        println!("{}", String::from_utf8_lossy(&ls.stdout));
        // SAFETY: geteuid has no preconditions.
        let me = unsafe { libc::geteuid() };
        private_path(
            &home,
            &support,
            me,
            &acl::yours().unwrap(),
            look,
            Missing::Refused,
        )
        .unwrap();
    }
}
