//! Linux: the AppImage this user keeps. It is one file, pinned by its SHA-256, replaced in one
//! rename. A .deb, an .rpm or an unknown format is refused, with how to update instead.
use super::{check_owned, sha256, Pinned};
use std::ffi::OsString;
use std::fs;
use std::path::{Path, PathBuf};
use tauri::{AppHandle, Manager};

/// The updater's name for this system's artifact: Linux, x86_64, AppImage.
pub(super) const TARGET: &str = "linux-x86_64-appimage";
/// What a kept copy's name ends in.
pub(super) const SUFFIX: &str = ".AppImage";
/// What the installed copy is called in messages.
pub(super) const WHAT: &str = "AppImage";
/// What a pin's digest is, in messages.
pub(super) const DIGEST: &str = "SHA-256";

/// Whether bytes start as a type-2 AppImage: an ELF file with `AI\x02` at offset 8.
pub fn is_appimage(bytes: &[u8]) -> bool {
    bytes.len() >= 11 && bytes[..4] == *b"\x7fELF" && bytes[8..11] == *b"AI\x02"
}

/// How this copy of the app was installed, as far as updating it goes: the AppImage this user
/// keeps, or why it cannot be updated from here.
pub(super) fn provenance(app: &AppHandle) -> Result<PathBuf, String> {
    use tauri::utils::config::BundleType;
    match tauri::utils::platform::bundle_type() {
        Some(BundleType::AppImage) => {}
        Some(BundleType::Deb) => {
            return Err("This copy of pluxy was installed from a .deb package. Update it with your \
                 package manager (for example `sudo apt install ./<new package>.deb`), not from here."
                .into())
        }
        Some(BundleType::Rpm) => {
            return Err("This copy of pluxy was installed from an .rpm package. Update it with your \
                 package manager (for example `sudo dnf upgrade` or `sudo rpm -U <new package>.rpm`), \
                 not from here."
                .into())
        }
        _ => {
            return Err("This copy of pluxy is not an AppImage, the only kind this updater can \
                 replace (its install format is unknown). Install the new version the way you \
                 installed this one."
                .into())
        }
    }
    let appimage = env_appimage(app)
        .map(PathBuf::from)
        .filter(|p| p.is_absolute())
        .ok_or("This AppImage does not say where it is (APPIMAGE is not set). Install the new version by hand.")?;
    check_owned(&appimage, false)?;
    let parent = appimage.parent().ok_or("The AppImage has no folder.")?;
    // shortcut: only the AppImage and its folder are checked, and `replace` renames by path; a
    // group- or world-writable, non-sticky folder further up could be swapped by another user of
    // a shared machine. Upgrade by checking the ancestors and renaming through an open folder
    // handle, if updating on shared machines is wanted.
    check_owned(parent, true)?;
    let mut head = [0u8; 16];
    {
        use std::io::Read;
        fs::File::open(&appimage)
            .and_then(|mut f| f.read_exact(&mut head))
            .map_err(|e| format!("{}: {e}", appimage.display()))?;
    }
    if !is_appimage(&head) {
        return Err(format!(
            "{} is not a type-2 AppImage; install the new version by hand.",
            appimage.display()
        ));
    }
    // The program that runs is the one inside that AppImage.
    let inside = env_appdir(app)
        .map(PathBuf::from)
        .and_then(|d| d.canonicalize().ok())
        .zip(
            std::env::current_exe()
                .ok()
                .and_then(|e| e.canonicalize().ok()),
        )
        .is_some_and(|(d, exe)| exe.starts_with(d));
    if !inside {
        return Err(
            "This program is not running from its AppImage; install the new version by hand."
                .into(),
        );
    }
    Ok(appimage)
}

/// The AppImage this process runs from, if it does, as Tauri's `Env` knows it.
fn env_appimage(app: &AppHandle) -> Option<OsString> {
    app.env().appimage
}

/// The folder the AppImage is mounted at.
fn env_appdir(app: &AppHandle) -> Option<OsString> {
    app.env().appdir
}

/// The installed copy this process says it runs from: the AppImage, or None.
pub(super) fn here(app: &AppHandle) -> Option<PathBuf> {
    env_appimage(app).map(PathBuf::from)
}

/// The SHA-256 of the AppImage at path.
pub(super) fn digest(path: &Path) -> Result<String, String> {
    fs::read(path)
        .map(|b| sha256(&b))
        .map_err(|e| format!("{}: {e}", path.display()))
}

/// Copies the AppImage from to the new private file to.
pub(super) fn copy_new(from: &Path, to: &Path) -> Result<(), String> {
    let bytes = fs::read(from).map_err(|e| format!("{}: {e}", from.display()))?;
    crate::managed::write_private(to, &bytes).map_err(|e| format!("{}: {e}", to.display()))
}

pub(super) fn remove(path: &Path) {
    let _ = fs::remove_file(path);
}

/// A checked AppImage, ready to replace the installed one.
pub(super) struct Staged {
    digest: String,
    bytes: Vec<u8>,
}

impl Staged {
    pub(super) fn digest(&self) -> &str {
        &self.digest
    }
}

/// The update's verified bytes, if they are an AppImage.
pub(super) fn stage(
    _installed: &Path,
    bytes: Vec<u8>,
    _version: &str,
    _dir: &Path,
) -> Result<Staged, String> {
    if !is_appimage(&bytes) {
        return Err("The update is not a type-2 AppImage.".into());
    }
    Ok(Staged {
        digest: sha256(&bytes),
        bytes,
    })
}

/// The kept copy at kept, if it still has its pinned digest and is an AppImage.
pub(super) fn stage_kept(
    _installed: &Path,
    kept: &Path,
    pin: &Pinned,
    _dir: &Path,
) -> Result<Staged, String> {
    let bytes = fs::read(kept).map_err(|e| format!("the kept copy {}: {e}", kept.display()))?;
    if sha256(&bytes) != pin.sha256 || !is_appimage(&bytes) {
        return Err(format!("the kept copy {} changed", kept.display()));
    }
    Ok(Staged {
        digest: pin.sha256.clone(),
        bytes,
    })
}

/// Puts staged in place of the installed AppImage (`replace`).
pub(super) fn swap_in(installed: &Path, staged: Staged, _dir: &Path) -> Result<(), String> {
    replace(installed, &staged.bytes)
}

/// What starts the installed copy: the AppImage itself.
pub(super) fn program(installed: &Path) -> PathBuf {
    installed.to_path_buf()
}

/// The process the version put back after a failed update starts after: this one. The earlier
/// version's AppImage has the same contents as this one's: run without FUSE
/// (APPIMAGE_EXTRACT_AND_RUN) it would unpack into the folder this one's runtime, its parent,
/// deletes once this process ends. So then it starts once that runtime has ended.
pub(super) fn restart_after() -> i32 {
    let extracted = std::env::var_os("APPDIR")
        .map(PathBuf::from)
        .and_then(|d| {
            d.file_name()
                .map(|n| n.to_string_lossy().starts_with("appimage_extracted_"))
        })
        .unwrap_or(false);
    if extracted {
        // SAFETY: getppid has no preconditions.
        unsafe { libc::getppid() }
    } else {
        std::process::id() as i32
    }
}

/// Replaces the AppImage with bytes in one step: a new file in the same folder, written and
/// synced, with the AppImage's permissions, renamed over it, then the folder synced.
fn replace(appimage: &Path, bytes: &[u8]) -> Result<(), String> {
    use std::io::Write;
    use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
    let parent = appimage.parent().ok_or("The AppImage has no folder.")?;
    let mode = fs::metadata(appimage).map_or(0o755, |m| m.permissions().mode() & 0o755);
    let mut nonce = [0u8; 8];
    getrandom::fill(&mut nonce).map_err(|e| e.to_string())?;
    let tmp = parent.join(format!(".pluxy-update-{}", crate::artifact::hex(&nonce)));
    let written = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o700)
        .open(&tmp)
        .and_then(|mut f| {
            f.write_all(bytes)?;
            f.set_permissions(fs::Permissions::from_mode(mode | 0o700))?;
            f.sync_all()
        })
        .and_then(|_| fs::rename(&tmp, appimage));
    if let Err(e) = written {
        let _ = fs::remove_file(&tmp);
        return Err(format!("{}: {e}", appimage.display()));
    }
    // Best effort once renamed: the new AppImage is in place, so a failed folder sync must not
    // report the install as failed (the caller would drop its pending record and the new version
    // would run untracked).
    let _ = fs::File::open(parent).and_then(|d| d.sync_all());
    Ok(())
}

#[cfg(test)]
pub(super) mod tests {
    use super::*;

    /// An installed AppImage at `<tmp>/app.AppImage` whose contents say v.
    pub(crate) fn fixture(tmp: &Path, v: &str) -> PathBuf {
        let image = tmp.join("app.AppImage");
        let mut b = b"\x7fELF\x02\x01\x01\x00AI\x02\x00\x00\x00\x00\x00".to_vec();
        b.extend_from_slice(v.as_bytes());
        fs::write(&image, b).unwrap();
        image
    }

    /// Changes a kept copy.
    pub(crate) fn tamper(path: &Path) {
        let mut b = fs::read(path).unwrap();
        b.push(0);
        fs::write(path, b).unwrap();
    }

    #[test]
    fn only_a_type_2_appimage_header_counts() {
        assert!(is_appimage(
            b"\x7fELF\x02\x01\x01\x00AI\x02\x00\x00\x00\x00\x00"
        ));
        assert!(!is_appimage(
            b"\x7fELF\x02\x01\x01\x00AI\x01\x00\x00\x00\x00\x00"
        ));
        assert!(!is_appimage(
            b"\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00"
        ));
        assert!(!is_appimage(b"\x1f\x8b\x08\x00"));
    }

    // The replace keeps the AppImage's permissions and leaves no temporary file.
    #[test]
    fn replace_is_one_rename_with_the_old_permissions() {
        use std::os::unix::fs::PermissionsExt;
        let tmp = super::super::tests::temp("replace");
        let image = tmp.join("app.AppImage");
        fs::write(&image, b"old").unwrap();
        fs::set_permissions(&image, fs::Permissions::from_mode(0o750)).unwrap();
        replace(&image, b"new").unwrap();
        assert_eq!(fs::read(&image).unwrap(), b"new");
        assert_eq!(
            fs::metadata(&image).unwrap().permissions().mode() & 0o777,
            0o750
        );
        assert_eq!(fs::read_dir(&tmp).unwrap().count(), 1);
        fs::remove_dir_all(&tmp).unwrap();
    }
}
