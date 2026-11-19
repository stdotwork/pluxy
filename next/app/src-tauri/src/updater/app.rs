//! macOS: the `.app` bundle this user keeps, run as `<name>.app/Contents/MacOS/<program>`, pinned
//! by its tree digest (`bundle::tree_digest`). The update is a `.app.tar.gz` (Tauri's macOS update
//! layout), unpacked under strict rules (`bundle::unpack`) into a new folder of this user's next
//! to the bundle (`.pluxy-update-<hex>`, on the same volume), checked (identifier, program name,
//! announced version, a program for this Mac), then swapped with the installed bundle in one step
//! (`renamex_np` with `RENAME_SWAP`): the bundle is the old one or the new one, never missing. The
//! old one ends in that folder, which is removed once the new version runs and the old one has
//! ended: the folder is recorded when it is made (`leftovers.rs`), and only a recorded folder that
//! is still that folder is removed. A volume that cannot swap (not APFS) is refused, not updated
//! another way.
//!
//! Nothing here asks for an administrator, changes the quarantine attribute or goes through
//! LaunchServices: a bundle this user cannot change is refused.
use super::bundle;
use super::leftovers;
use super::{check_owned, Pinned};
use std::ffi::CString;
use std::fs;
use std::os::unix::ffi::OsStrExt;
use std::path::{Path, PathBuf};
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
    // shortcut: as on Linux, only the bundle and its folder are checked, and the swap goes by
    // path; a folder further up that others could change could be swapped. Upgrade by checking
    // the ancestors, if updating on shared machines is wanted.
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

/// A checked bundle in its staging folder next to the installed one, ready to swap in. The
/// folder is removed if it is not swapped in, only while it is still the folder made
/// (`leftovers::Folder`).
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

/// A new staging folder of this user's next to the installed bundle, recorded in the updates
/// folder dir (`leftovers::Folder`).
fn staging(installed: &Path, dir: &Path) -> Result<leftovers::Folder, String> {
    let folder = installed.parent().ok_or("The app bundle has no folder.")?;
    let mut nonce = [0u8; 8];
    getrandom::fill(&mut nonce).map_err(|e| e.to_string())?;
    let path = folder.join(format!(".pluxy-update-{}", crate::artifact::hex(&nonce)));
    leftovers::Folder::new(dir, &path)
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

/// The kept bundle at kept, copied next to the installed one, if the copy still has its pinned
/// digest and is this app's.
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
    bundle::copy_tree(kept, &s.bundle)
        .map_err(|e| format!("the kept copy {}: {e}", kept.display()))?;
    if bundle::tree_digest(&s.bundle).ok().as_deref() != Some(pin.sha256.as_str())
        || bundle::check(&s.bundle, &want, None, CPU).is_err()
    {
        return Err(format!("the kept copy {} changed", kept.display()));
    }
    Ok(s)
}

/// Swaps staged with the installed bundle in one step.
pub(super) fn swap_in(installed: &Path, mut staged: Staged, _updates: &Path) -> Result<(), String> {
    let from = CString::new(staged.bundle.as_os_str().as_bytes()).map_err(|e| e.to_string())?;
    let to = CString::new(installed.as_os_str().as_bytes()).map_err(|e| e.to_string())?;
    // SAFETY: both are NUL-terminated paths.
    if unsafe { libc::renamex_np(from.as_ptr(), to.as_ptr(), libc::RENAME_SWAP) } != 0 {
        return Err(format!(
            "{}: {}",
            installed.display(),
            std::io::Error::last_os_error()
        ));
    }
    // The old bundle is in the staging folder now: it stays until the old version has ended.
    staged.staging.keep();
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
/// recorded, never through a link (`leftovers::remove_folder`). The bundle's folder is not listed.
pub(super) fn cleanup(dir: &Path) {
    leftovers::clean(dir, leftovers::remove_folder);
}

/// The process the version put back after a failed update starts after: this one.
pub(super) fn restart_after() -> i32 {
    std::process::id() as i32
}

#[cfg(test)]
pub(super) mod tests {
    use super::*;

    /// An installed bundle `<tmp>/pluxy Test.app` whose program says v.
    pub(crate) fn fixture(tmp: &Path, v: &str) -> PathBuf {
        let b = bundle::tests::make(tmp, "0.1.0", v);
        // The fixture's program is for this Mac.
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

    /// The staging folders beside the bundle in tmp, by name.
    fn staging_folders(tmp: &Path) -> Vec<PathBuf> {
        let mut v: Vec<PathBuf> = fs::read_dir(tmp)
            .unwrap()
            .flatten()
            .filter(|e| {
                e.file_name()
                    .to_string_lossy()
                    .starts_with(".pluxy-update-")
            })
            .map(|e| e.path())
            .collect();
        v.sort();
        v
    }

    // A staged bundle dropped without being swapped in (a failed or cancelled install) removes its
    // staging folder only while that is still the folder made: a folder put at its name stays.
    #[test]
    fn a_dropped_stage_removes_only_the_folder_it_made() {
        let tmp = super::super::tests::temp("app-drop");
        let dir = tmp.join("updates");
        let installed = fixture(&tmp, "old");
        let kept = tmp.join("kept.app");
        copy_new(&installed, &kept).unwrap();
        let pin = Pinned {
            version: "0.1.0".into(),
            sha256: digest(&installed).unwrap(),
        };
        drop(stage_kept(&installed, &kept, &pin, &dir).unwrap());
        assert!(staging_folders(&tmp).is_empty(), "dropped as made: removed");
        let s = stage_kept(&installed, &kept, &pin, &dir).unwrap();
        let staged = staging_folders(&tmp).remove(0);
        fs::rename(&staged, tmp.join("moved")).unwrap();
        fs::create_dir(&staged).unwrap();
        fs::write(staged.join("theirs"), b"z").unwrap();
        drop(s);
        assert!(staged.join("theirs").exists(), "not the folder made");
        fs::remove_dir_all(&tmp).unwrap();
    }

    // The swap leaves the new bundle in place and the old one in the staging folder, which
    // cleanup removes while the bundle in place stays. A kept copy that changed is refused and
    // its staging folder removed.
    #[test]
    fn the_swap_is_one_step_and_cleanup_removes_only_staging() {
        let tmp = super::super::tests::temp("app-swap");
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
        let staged = staging_folders(&tmp);
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
        assert!(staging_folders(&tmp).is_empty());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // Only a staging folder an install recorded is removed: a folder another user renamed to the
    // staging pattern beside the bundle stays, and so does a folder of the same name put in
    // place of a recorded one.
    #[test]
    fn cleanup_leaves_folders_it_did_not_record_beside_the_bundle() {
        let tmp = super::super::tests::temp("app-cleanup");
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
        let recorded = staging_folders(&tmp)
            .into_iter()
            .find(|p| *p != renamed)
            .unwrap();
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
}
