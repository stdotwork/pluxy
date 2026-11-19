//! Windows: the program of a folder bundle, one self-contained `.exe` this user keeps (its
//! WebView2 loader is linked in and its pages are embedded, so it needs nothing beside it). It is
//! one file, pinned by its SHA-256, like an AppImage. A copy an installer keeps (an MSI or NSIS
//! setup patches its bundle type) is refused, with how to update instead, and so is a copy on a
//! network drive or one anyone else could change.
//!
//! A running program can be renamed but not replaced or removed, so the new program is written
//! next to it, the running one is renamed aside (`.pluxy-old-<hex>.exe`) and the new one renamed
//! into its place, each rename written through to the disk; if the second rename fails the first
//! is undone. Between the two renames (two metadata operations of this process) the folder holds
//! no program under its name: a power loss there leaves the aside copy and the kept copy, and the
//! README says how to put one back. What is left aside is removed once the version that replaced
//! it holds the profile, when the earlier version has ended (`cleanup`): the new file and the
//! aside copy are recorded with their volume serial and file ID before they are left there
//! (`leftovers.rs`), and only a recorded file that is still that file is removed.
use super::leftovers::{self, Made, Outcome};
use super::{sha256, Pinned};
use std::ffi::OsStr;
use std::fs;
use std::os::windows::ffi::OsStrExt;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::AppHandle;
use windows_sys::Win32::Foundation::{
    ERROR_ACCESS_DENIED, ERROR_LOCK_VIOLATION, ERROR_SHARING_VIOLATION,
};
use windows_sys::Win32::Storage::FileSystem::{
    FileDispositionInfo, GetDriveTypeW, MoveFileExW, SetFileInformationByHandle, DELETE,
    FILE_ATTRIBUTE_REPARSE_POINT, FILE_DISPOSITION_INFO, FILE_FLAG_OPEN_REPARSE_POINT,
    FILE_GENERIC_WRITE, FILE_READ_ATTRIBUTES, FILE_SHARE_DELETE, FILE_SHARE_READ, FILE_SHARE_WRITE,
    MOVEFILE_WRITE_THROUGH,
};

/// The updater's name for this system's artifact: Windows, this architecture, a program.
#[cfg(target_arch = "x86_64")]
pub(super) const TARGET: &str = "windows-x86_64-exe";
#[cfg(target_arch = "aarch64")]
pub(super) const TARGET: &str = "windows-aarch64-exe";
/// The PE machine type of this architecture.
#[cfg(target_arch = "x86_64")]
const MACHINE: u16 = 0x8664;
#[cfg(target_arch = "aarch64")]
const MACHINE: u16 = 0xaa64;
/// What a kept copy's name ends in.
pub(super) const SUFFIX: &str = ".exe";
/// What the installed copy is called in messages.
pub(super) const WHAT: &str = "program";
/// What a pin's digest is, in messages.
pub(super) const DIGEST: &str = "SHA-256";
/// How long a rename or a removal waits out a file another program (a virus scanner, or the
/// earlier version not yet unmapped) has open.
const BUSY_FOR: Duration = Duration::from_secs(5);
/// `GetDriveTypeW`'s answer for a fixed local drive.
const DRIVE_FIXED: u32 = 3;

/// How this copy of the app was installed, as far as updating it goes: the program of a folder
/// bundle this user keeps on a local drive, or why it cannot be updated from here.
pub(super) fn provenance(app: &AppHandle) -> Result<PathBuf, String> {
    use tauri::utils::config::BundleType;
    match tauri::utils::platform::bundle_type() {
        None => {}
        Some(BundleType::Msi) => {
            return Err(
                "This copy of pluxy was installed from an .msi package. Install the new \
                 version with its .msi package, not from here."
                    .into(),
            )
        }
        Some(BundleType::Nsis) => {
            return Err(
                "This copy of pluxy was installed by its setup program. Install the new \
                 version with its setup program, not from here."
                    .into(),
            )
        }
        Some(_) => {
            return Err(
                "This copy of pluxy is not a folder bundle, the only kind this updater \
                 can replace on Windows (its install format is unknown). Install the new version \
                 the way you installed this one."
                    .into(),
            )
        }
    }
    let exe = here(app)
        .ok_or("This program cannot tell where it is on disk; install the new version by hand.")?;
    local_fixed(&exe)?;
    let folder = exe.parent().ok_or("The program has no folder.")?;
    // shortcut: as on Linux, only the program and its folder are checked, and the replace
    // renames by path; a folder further up that others could change could be swapped. Upgrade by
    // checking the ancestors, if updating on shared machines is wanted.
    for (path, is_folder) in [(exe.as_path(), false), (folder, true)] {
        crate::win_acl::check(path, is_folder, crate::win_acl::Others::Read).map_err(|e| {
            format!(
                "{} is not a {} of yours that only you can change (an installer or another user \
                 may keep it): {e}. Update it the way it was installed.",
                path.display(),
                if is_folder { "folder" } else { "file" }
            )
        })?;
    }
    Ok(exe)
}

/// Refused unless path is on a fixed local drive (`C:\...`): not a network share or a mapped,
/// removable or virtual drive, where renames are not the local file system's.
fn local_fixed(path: &Path) -> Result<(), String> {
    let s = path.as_os_str().to_string_lossy();
    let b = s.as_bytes();
    let drive = b.len() >= 3 && b[0].is_ascii_alphabetic() && b[1] == b':' && b[2] == b'\\';
    let fixed = drive && {
        let root: Vec<u16> = OsStr::new(&s[..3]).encode_wide().chain([0]).collect();
        // SAFETY: root is a NUL-terminated wide string.
        unsafe { GetDriveTypeW(root.as_ptr()) == DRIVE_FIXED }
    };
    if !fixed {
        return Err(format!(
            "{} is not on a local drive of this computer; install the new version by hand.",
            path.display()
        ));
    }
    Ok(())
}

/// The program this process runs from, by the path it has now with every link resolved.
pub(super) fn here(_app: &AppHandle) -> Option<PathBuf> {
    let exe = std::env::current_exe().ok()?;
    let f = fs::File::open(exe).ok()?;
    crate::owned::windows::final_path(&f).ok()
}

/// The SHA-256 of the program at path.
pub(super) fn digest(path: &Path) -> Result<String, String> {
    fs::read(path)
        .map(|b| sha256(&b))
        .map_err(|e| format!("{}: {e}", path.display()))
}

/// Copies the program from to the new private file to.
pub(super) fn copy_new(from: &Path, to: &Path) -> Result<(), String> {
    let bytes = fs::read(from).map_err(|e| format!("{}: {e}", from.display()))?;
    crate::managed::write_private(to, &bytes).map_err(|e| format!("{}: {e}", to.display()))
}

pub(super) fn remove(path: &Path) {
    let _ = fs::remove_file(path);
}

/// A checked program, ready to replace the installed one.
pub(super) struct Staged {
    digest: String,
    bytes: Vec<u8>,
}

impl Staged {
    pub(super) fn digest(&self) -> &str {
        &self.digest
    }
}

/// Whether bytes are a 64-bit Windows program (not a library) for this architecture, with the
/// desktop or the console subsystem.
fn is_program(b: &[u8]) -> bool {
    let u16_at = |i: usize| b.get(i..i + 2).map(|s| u16::from_le_bytes([s[0], s[1]]));
    let u32_at = |i: usize| {
        b.get(i..i + 4)
            .map(|s| u32::from_le_bytes([s[0], s[1], s[2], s[3]]))
    };
    if b.get(..2) != Some(b"MZ") {
        return false;
    }
    let Some(pe) = u32_at(0x3c).map(|e| e as usize).filter(|e| *e >= 0x40) else {
        return false;
    };
    let optional = pe + 24;
    b.get(pe..pe + 4) == Some(b"PE\0\0")
        && u16_at(pe + 4) == Some(MACHINE)
        // IMAGE_FILE_EXECUTABLE_IMAGE set, IMAGE_FILE_DLL not.
        && u16_at(pe + 22).is_some_and(|c| c & 0x0002 != 0 && c & 0x2000 == 0)
        // PE32+.
        && u16_at(optional) == Some(0x20b)
        // IMAGE_SUBSYSTEM_WINDOWS_GUI or _CUI.
        && matches!(u16_at(optional + 68), Some(2 | 3))
}

/// The update's verified bytes, if they are a program for this system.
pub(super) fn stage(
    _installed: &Path,
    bytes: Vec<u8>,
    _version: &str,
    _dir: &Path,
) -> Result<Staged, String> {
    if !is_program(&bytes) {
        return Err(format!(
            "The update is not a Windows program for this system ({TARGET})."
        ));
    }
    Ok(Staged {
        digest: sha256(&bytes),
        bytes,
    })
}

/// The kept copy at kept, if it still has its pinned digest and is a program.
pub(super) fn stage_kept(
    _installed: &Path,
    kept: &Path,
    pin: &Pinned,
    _dir: &Path,
) -> Result<Staged, String> {
    let bytes = fs::read(kept).map_err(|e| format!("the kept copy {}: {e}", kept.display()))?;
    if sha256(&bytes) != pin.sha256 || !is_program(&bytes) {
        return Err(format!("the kept copy {} changed", kept.display()));
    }
    Ok(Staged {
        digest: pin.sha256.clone(),
        bytes,
    })
}

/// Puts staged in place of the installed program (see the module), recording what it leaves
/// beside it in the updates folder dir.
pub(super) fn swap_in(installed: &Path, staged: Staged, dir: &Path) -> Result<(), String> {
    replace(installed, &staged.bytes, dir)
}

/// What starts the installed copy: the program itself.
pub(super) fn program(installed: &Path) -> PathBuf {
    installed.to_path_buf()
}

/// The volume serial number and file ID of the open file f, in hex: what a recorded file is.
fn file_id(f: &fs::File) -> std::io::Result<String> {
    crate::owned::windows::file_id(f).map(|id| crate::artifact::hex(&id))
}

/// Records path, the open file f's name now or once renamed, in the updates folder dir: its
/// identity as recorded.
fn record(dir: &Path, path: &Path, f: &fs::File) -> Result<String, String> {
    let id = file_id(f).map_err(|e| format!("{}: {e}", path.display()))?;
    leftovers::record(dir, path, id.clone())?;
    Ok(id)
}

/// Deletes the file open as f (with delete access) through its handle, never by its name: it
/// goes when the last handle to it closes.
fn delete_through(f: &fs::File) -> std::io::Result<()> {
    use std::os::windows::io::AsRawHandle;
    let delete = FILE_DISPOSITION_INFO { DeleteFile: true };
    // SAFETY: f is open with DELETE; delete is the structure the class names.
    if unsafe {
        SetFileInformationByHandle(
            f.as_raw_handle() as _,
            FileDispositionInfo,
            (&delete as *const FILE_DISPOSITION_INFO).cast(),
            std::mem::size_of::<FILE_DISPOSITION_INFO>() as u32,
        )
    } == 0
    {
        return Err(std::io::Error::last_os_error());
    }
    Ok(())
}

/// Removes what an earlier replace recorded that it left next to the installed program (the
/// aside copy of a version that has ended, or a new file a failed replace did not take back),
/// each only while it is still the file recorded (`remove_recorded`). The program's folder is not
/// listed.
pub(super) fn cleanup(dir: &Path) {
    leftovers::clean(dir, remove_recorded);
}

/// Removes the recorded file m: opened without following a link and with delete access, it must
/// be a plain file with the recorded volume serial and file ID; it is then deleted through that
/// handle, never by its name. A file in use is waited out for `BUSY_FOR` (the earlier version's
/// image can stay mapped for a moment after this app took the profile); one still in use then
/// stays recorded for the next start. A folder or link at its name, which such an open refuses
/// as access denied, is not the file: not there.
fn remove_recorded(m: &Made) -> Outcome {
    use std::os::windows::fs::{MetadataExt, OpenOptionsExt};
    let mut not_there = false;
    let r = while_busy(|| {
        let opened = fs::OpenOptions::new()
            .access_mode(DELETE | FILE_READ_ATTRIBUTES)
            .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE)
            .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
            .open(&m.path);
        let f = match opened {
            Ok(f) => f,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                not_there = true;
                return Ok(());
            }
            // Opened without FILE_FLAG_BACKUP_SEMANTICS, a folder is refused as access denied,
            // which would read as busy: looked at again without opening it.
            Err(e) if e.raw_os_error().map(|c| c as u32) == Some(ERROR_ACCESS_DENIED) => {
                if fs::symlink_metadata(&m.path).is_ok_and(|md| {
                    md.is_dir() || md.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0
                }) {
                    not_there = true;
                    return Ok(());
                }
                return Err(e);
            }
            Err(e) => return Err(e),
        };
        let md = f.metadata()?;
        if !md.is_file()
            || md.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0
            || file_id(&f)? != m.id
        {
            not_there = true;
            return Ok(());
        }
        // The file goes when f closes, at the end of this call.
        delete_through(&f)
    });
    match r {
        Ok(()) if not_there => Outcome::NotThere,
        Ok(()) => Outcome::Removed,
        Err(_) => Outcome::Kept,
    }
}

fn wide(p: &Path) -> Vec<u16> {
    p.as_os_str().encode_wide().chain([0]).collect()
}

/// Renames from to to (which must not exist), written through to the disk; a file another
/// program has open is waited out for `BUSY_FOR`.
fn rename(from: &Path, to: &Path) -> std::io::Result<()> {
    let (f, t) = (wide(from), wide(to));
    while_busy(|| {
        // SAFETY: both are NUL-terminated wide strings.
        if unsafe { MoveFileExW(f.as_ptr(), t.as_ptr(), MOVEFILE_WRITE_THROUGH) } != 0 {
            Ok(())
        } else {
            Err(std::io::Error::last_os_error())
        }
    })
}

/// Runs op again while it fails because another program has the file open (a sharing, access
/// or lock violation), for at most `BUSY_FOR`.
fn while_busy(mut op: impl FnMut() -> std::io::Result<()>) -> std::io::Result<()> {
    let until = Instant::now() + BUSY_FOR;
    loop {
        let Err(e) = op() else {
            return Ok(());
        };
        let busy = matches!(
            e.raw_os_error().map(|c| c as u32),
            Some(ERROR_SHARING_VIOLATION | ERROR_ACCESS_DENIED | ERROR_LOCK_VIOLATION)
        );
        if !busy || Instant::now() > until {
            return Err(e);
        }
        std::thread::sleep(Duration::from_millis(100));
    }
}

/// Replaces the program with bytes (see the module): a new file next to it, recorded in the
/// updates folder dir, written and flushed; the program recorded under its aside name, then
/// renamed aside; the new file renamed into its place, or the program renamed back. A replace
/// that fails deletes the new file only as the file it made: through the handle it made it with
/// while that is open, then only while its name still holds the file recorded
/// (`remove_recorded`).
fn replace(installed: &Path, bytes: &[u8], dir: &Path) -> Result<(), String> {
    use std::io::Write;
    use std::os::windows::fs::OpenOptionsExt;
    let folder = installed.parent().ok_or("The program has no folder.")?;
    let mut nonce = [0u8; 8];
    getrandom::fill(&mut nonce).map_err(|e| e.to_string())?;
    let hex = crate::artifact::hex(&nonce);
    let new = folder.join(format!(".pluxy-update-{hex}"));
    let aside = folder.join(format!(".pluxy-old-{hex}.exe"));
    let shown = |e: std::io::Error| format!("{}: {e}", installed.display());
    let mut f = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .access_mode(FILE_GENERIC_WRITE | DELETE | FILE_READ_ATTRIBUTES)
        .open(&new)
        .map_err(shown)?;
    let id = match record(dir, &new, &f) {
        Ok(id) => id,
        Err(e) => {
            #[cfg(test)]
            tests::before_removing_new(&new);
            let _ = while_busy(|| delete_through(&f));
            return Err(e);
        }
    };
    if let Err(e) = f.write_all(bytes).and_then(|()| f.sync_all()) {
        #[cfg(test)]
        tests::before_removing_new(&new);
        let _ = while_busy(|| delete_through(&f));
        return Err(shown(e));
    }
    drop(f);
    let made = Made {
        path: new.clone(),
        id,
    };
    // The program as it is now is what the aside name will hold: recorded before the rename, so
    // what is left aside is always recorded.
    let program = fs::File::open(installed)
        .map_err(shown)
        .and_then(|p| record(dir, &aside, &p).map(drop));
    if let Err(e) = program {
        #[cfg(test)]
        tests::before_removing_new(&new);
        remove_recorded(&made);
        return Err(e);
    }
    if let Err(e) = rename(installed, &aside) {
        #[cfg(test)]
        tests::before_removing_new(&new);
        remove_recorded(&made);
        return Err(shown(e));
    }
    if let Err(e) = rename(&new, installed) {
        let back = rename(&aside, installed);
        #[cfg(test)]
        tests::before_removing_new(&new);
        remove_recorded(&made);
        return Err(match back {
            Ok(()) => shown(e),
            Err(b) => format!(
                "{}: {e}; and the program could not be put back from {}: {b}",
                installed.display(),
                aside.display()
            ),
        });
    }
    Ok(())
}

#[cfg(test)]
pub(super) mod tests {
    use super::*;
    use std::sync::Mutex;

    /// Run by `replace` just before it deletes its new file after a failure, for the folder a
    /// test set it for. Tests run in parallel, so each test's swap is an entry of its own
    /// (`swap_new_in`), which no other test replaces or clears.
    static SWAP_NEW: Mutex<Vec<SwapNew>> = Mutex::new(Vec::new());

    /// The folder a swap is for, and the swap.
    type SwapNew = (PathBuf, fn(&Path));

    /// The swap one test set (`swap_new_in`): dropping it removes that entry only.
    struct Hooked(PathBuf);

    impl Drop for Hooked {
        fn drop(&mut self) {
            SWAP_NEW
                .lock()
                .unwrap_or_else(|e| e.into_inner())
                .retain(|(folder, _)| *folder != self.0);
        }
    }

    /// Sets f to run for a new file in folder, until the value returned is dropped.
    fn swap_new_in(folder: &Path, f: fn(&Path)) -> Hooked {
        SWAP_NEW.lock().unwrap().push((folder.to_path_buf(), f));
        Hooked(folder.to_path_buf())
    }

    pub(super) fn before_removing_new(new: &Path) {
        let hook = SWAP_NEW
            .lock()
            .unwrap()
            .iter()
            .find(|(folder, _)| Some(folder.as_path()) == new.parent())
            .map(|(_, f)| *f);
        if let Some(f) = hook {
            f(new);
        }
    }

    /// Moves the file at p aside (to `<p>.ours`) and puts another file, "theirs", at its name.
    fn swap_new(p: &Path) {
        fs::rename(p, p.with_extension("ours")).unwrap();
        fs::write(p, b"theirs").unwrap();
    }

    /// The file named like a new program in t that is not the one moved aside.
    fn theirs(t: &Path) -> Vec<u8> {
        let n = names(t)
            .into_iter()
            .find(|n| n.starts_with(".pluxy-update-") && !n.ends_with(".ours"))
            .unwrap();
        fs::read(t.join(n)).unwrap()
    }

    /// A minimal program header for this architecture, then v.
    fn program_bytes(v: &str) -> Vec<u8> {
        let mut b = vec![0u8; 0x200];
        b[..2].copy_from_slice(b"MZ");
        b[0x3c..0x40].copy_from_slice(&0x80u32.to_le_bytes());
        b[0x80..0x84].copy_from_slice(b"PE\0\0");
        b[0x84..0x86].copy_from_slice(&MACHINE.to_le_bytes());
        b[0x96..0x98].copy_from_slice(&0x0022u16.to_le_bytes());
        b[0x98..0x9a].copy_from_slice(&0x20bu16.to_le_bytes());
        b[0x98 + 68..0x98 + 70].copy_from_slice(&2u16.to_le_bytes());
        b.extend_from_slice(v.as_bytes());
        b
    }

    /// An installed program at `<tmp>/pluxy.exe` whose contents say v.
    pub(crate) fn fixture(tmp: &Path, v: &str) -> PathBuf {
        let exe = tmp.join("pluxy.exe");
        fs::write(&exe, program_bytes(v)).unwrap();
        exe
    }

    /// Changes a kept copy.
    pub(crate) fn tamper(path: &Path) {
        let mut b = fs::read(path).unwrap();
        b.push(0);
        fs::write(path, b).unwrap();
    }

    #[test]
    fn only_a_program_for_this_system_is_staged() {
        assert!(is_program(&program_bytes("x")));
        let mut dll = program_bytes("x");
        dll[0x96..0x98].copy_from_slice(&0x2022u16.to_le_bytes());
        let mut other = program_bytes("x");
        other[0x84..0x86].copy_from_slice(&0x014cu16.to_le_bytes());
        let mut pe32 = program_bytes("x");
        pe32[0x98..0x9a].copy_from_slice(&0x10bu16.to_le_bytes());
        let mut driver = program_bytes("x");
        driver[0x98 + 68..0x98 + 70].copy_from_slice(&1u16.to_le_bytes());
        let mut short = program_bytes("");
        short.truncate(0x90);
        for (what, b) in [
            ("a library", dll),
            ("another machine", other),
            ("32-bit", pe32),
            ("a driver", driver),
            ("cut short", short),
            ("not MZ", b"\x7fELF".to_vec()),
        ] {
            assert!(!is_program(&b), "{what}");
            assert!(
                stage(Path::new("x"), b, "0.2.0", Path::new("u")).is_err(),
                "{what}"
            );
        }
    }

    /// The names in t, sorted, without the updates folder.
    fn names(t: &Path) -> Vec<String> {
        let mut v: Vec<String> = fs::read_dir(t)
            .unwrap()
            .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
            .filter(|n| n != "updates")
            .collect();
        v.sort();
        v
    }

    // The new program is in place, the old one aside, and nothing else is left once cleaned up;
    // a running program is renamed aside the same way (the native test shows it on a runner).
    // Only what the replace recorded goes: a file another user renamed to the aside pattern stays.
    #[test]
    fn the_replace_renames_aside_and_cleanup_removes_what_is_left() {
        let tmp = super::super::tests::temp("exe-replace");
        let dir = tmp.join("updates");
        let exe = fixture(&tmp, "old");
        fs::write(tmp.join("other.txt"), b"not ours").unwrap();
        fs::write(
            tmp.join(".pluxy-old-0123456789abcdef.exe"),
            b"not ours either",
        )
        .unwrap();
        replace(&exe, &program_bytes("new"), &dir).unwrap();
        assert_eq!(fs::read(&exe).unwrap(), program_bytes("new"));
        let left = names(&tmp);
        assert_eq!(left.len(), 4, "{left:?}");
        // The next version's cleanup, once this one has ended.
        leftovers::forget_mine(&tmp);
        cleanup(&dir);
        assert_eq!(
            names(&tmp),
            [".pluxy-old-0123456789abcdef.exe", "other.txt", "pluxy.exe"],
            "only what the replace recorded goes"
        );
        assert_eq!(fs::read(&exe).unwrap(), program_bytes("new"));
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A recorded aside copy replaced by another file of the same name stays.
    #[test]
    fn cleanup_leaves_a_file_put_in_place_of_a_recorded_one() {
        let tmp = super::super::tests::temp("exe-replaced");
        let dir = tmp.join("updates");
        let exe = fixture(&tmp, "old");
        replace(&exe, &program_bytes("new"), &dir).unwrap();
        let aside = names(&tmp)
            .into_iter()
            .find(|n| n.starts_with(".pluxy-old-"))
            .map(|n| tmp.join(n))
            .unwrap();
        fs::rename(&aside, tmp.join("moved.exe")).unwrap();
        fs::write(&aside, b"theirs").unwrap();
        leftovers::forget_mine(&tmp);
        cleanup(&dir);
        assert_eq!(
            fs::read(&aside).unwrap(),
            b"theirs",
            "not the file recorded"
        );
        assert!(tmp.join("moved.exe").exists());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A program held open without sharing delete cannot be renamed aside: nothing changes and no
    // new file is left behind.
    #[test]
    fn a_program_that_cannot_be_moved_is_left_as_it_was() {
        use std::os::windows::fs::OpenOptionsExt;
        let tmp = super::super::tests::temp("exe-busy");
        let exe = fixture(&tmp, "old");
        let held = fs::OpenOptions::new()
            .read(true)
            .share_mode(windows_sys::Win32::Storage::FileSystem::FILE_SHARE_READ)
            .open(&exe)
            .unwrap();
        let started = Instant::now();
        let e = replace(&exe, &program_bytes("new"), &tmp.join("updates")).unwrap_err();
        assert!(started.elapsed() >= BUSY_FOR, "it waited for the file: {e}");
        drop(held);
        assert_eq!(fs::read(&exe).unwrap(), program_bytes("old"));
        assert_eq!(names(&tmp), ["pluxy.exe"]);
        // Its aside name was recorded before the rename was tried.
        let record = fs::read_to_string(tmp.join("updates").join("leftovers.json")).unwrap();
        assert!(record.contains(".pluxy-old-"), "{record}");
        // The same with the new file's name taken over just before it is removed: the file now at
        // that name is not the one recorded, and stays.
        let held = fs::OpenOptions::new()
            .read(true)
            .share_mode(windows_sys::Win32::Storage::FileSystem::FILE_SHARE_READ)
            .open(&exe)
            .unwrap();
        let hooked = swap_new_in(&tmp, swap_new);
        let r = replace(&exe, &program_bytes("new"), &tmp.join("updates"));
        drop(hooked);
        drop(held);
        assert!(r.is_err());
        assert_eq!(theirs(&tmp), b"theirs");
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A folder at a recorded file's name is not that file: settled at once as not there, not
    // waited out as a file in use, and left whole.
    #[test]
    fn a_folder_at_a_recorded_name_is_not_there() {
        let tmp = super::super::tests::temp("exe-folder");
        let path = tmp.join("pluxy.exe.old-0123456789abcdef");
        fs::write(&path, b"x").unwrap();
        let id = file_id(&fs::File::open(&path).unwrap()).unwrap();
        fs::remove_file(&path).unwrap();
        fs::create_dir(&path).unwrap();
        fs::write(path.join("keep"), b"z").unwrap();
        let start = Instant::now();
        let m = Made {
            path: path.clone(),
            id,
        };
        assert_eq!(remove_recorded(&m), Outcome::NotThere);
        assert!(start.elapsed() < BUSY_FOR, "waited {:?}", start.elapsed());
        assert!(path.join("keep").exists());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A link at a recorded name, even to the recorded file, is not that file: the link is not
    // followed and the file stays. Not run where this user cannot make a link.
    #[test]
    fn a_link_at_a_recorded_name_is_not_followed() {
        let tmp = super::super::tests::temp("exe-link");
        let target = tmp.join("target.exe");
        fs::write(&target, b"x").unwrap();
        let path = tmp.join(".pluxy-old-0123456789abcdef.exe");
        if let Err(e) = std::os::windows::fs::symlink_file(&target, &path) {
            eprintln!("not run: this user cannot make a link: {e}");
            fs::remove_dir_all(&tmp).unwrap();
            return;
        }
        let m = Made {
            path: path.clone(),
            id: file_id(&fs::File::open(&target).unwrap()).unwrap(),
        };
        assert_eq!(remove_recorded(&m), Outcome::NotThere);
        assert!(target.exists());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A file put in place of a recorded one and held open is waited out like the recorded file,
    // then checked: it is not the file recorded, and stays.
    #[test]
    fn a_file_put_in_place_and_held_is_checked_once_let_go() {
        use std::os::windows::fs::OpenOptionsExt;
        let tmp = super::super::tests::temp("exe-held-other");
        let aside = tmp.join(".pluxy-old-0123456789abcdef.exe");
        fs::write(&aside, b"recorded").unwrap();
        let m = Made {
            path: aside.clone(),
            id: file_id(&fs::File::open(&aside).unwrap()).unwrap(),
        };
        fs::rename(&aside, tmp.join("moved.exe")).unwrap();
        fs::write(&aside, b"theirs").unwrap();
        let held = fs::OpenOptions::new()
            .read(true)
            .share_mode(windows_sys::Win32::Storage::FileSystem::FILE_SHARE_READ)
            .open(&aside)
            .unwrap();
        let let_go = std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(500));
            drop(held);
        });
        let r = remove_recorded(&m);
        let_go.join().unwrap();
        assert_eq!(r, Outcome::NotThere);
        assert_eq!(fs::read(&aside).unwrap(), b"theirs");
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A replace whose new file cannot be recorded changes nothing and leaves nothing beside the
    // program: the new file is deleted through the handle it was made with.
    #[test]
    fn a_replace_that_cannot_record_leaves_nothing() {
        let tmp = super::super::tests::temp("exe-norecord");
        let exe = fixture(&tmp, "old");
        // The updates folder's name holds a file: the record cannot be written.
        fs::write(tmp.join("updates"), b"not a folder").unwrap();
        assert!(replace(&exe, &program_bytes("new"), &tmp.join("updates")).is_err());
        assert_eq!(fs::read(&exe).unwrap(), program_bytes("old"));
        assert_eq!(names(&tmp), ["pluxy.exe"]);
        // The same with the new file's name taken over just before it is deleted: what goes is
        // the file made, through its handle, not the file now at its name.
        let hooked = swap_new_in(&tmp, swap_new);
        let r = replace(&exe, &program_bytes("new"), &tmp.join("updates"));
        drop(hooked);
        assert!(r.is_err());
        assert_eq!(theirs(&tmp), b"theirs");
        assert_eq!(names(&tmp).len(), 2, "{:?}", names(&tmp));
        fs::remove_dir_all(&tmp).unwrap();
    }

    // An aside copy still held open for a moment after this version took the profile (the earlier
    // version's image not yet unmapped) is waited out and removed, not left for the next start.
    #[test]
    fn cleanup_waits_out_a_leftover_held_open_for_a_moment() {
        use std::os::windows::fs::OpenOptionsExt;
        let tmp = super::super::tests::temp("exe-cleanup-busy");
        let dir = tmp.join("updates");
        let exe = fixture(&tmp, "new");
        let aside = tmp.join(".pluxy-old-0123456789abcdef.exe");
        fs::write(&aside, program_bytes("old")).unwrap();
        record(&dir, &aside, &fs::File::open(&aside).unwrap()).unwrap();
        leftovers::forget_mine(&tmp);
        let held = fs::OpenOptions::new()
            .read(true)
            .share_mode(windows_sys::Win32::Storage::FileSystem::FILE_SHARE_READ)
            .open(&aside)
            .unwrap();
        let let_go = std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(1000));
            drop(held);
        });
        let started = Instant::now();
        cleanup(&dir);
        let_go.join().unwrap();
        assert!(!aside.exists(), "the aside copy is gone");
        // The hold did keep the first removal from going through.
        assert!(
            started.elapsed() >= Duration::from_millis(900),
            "it waited for the file"
        );
        assert_eq!(fs::read(&exe).unwrap(), program_bytes("new"));
        fs::remove_dir_all(&tmp).unwrap();
    }

    #[test]
    fn a_network_or_relative_path_is_not_a_local_drive() {
        for p in [
            r"\\server\share\pluxy.exe",
            r"pluxy.exe",
            r"\\?\UNC\server\share\x.exe",
        ] {
            assert!(local_fixed(Path::new(p)).is_err(), "{p}");
        }
        let windir = std::env::var("SystemRoot").unwrap_or_else(|_| r"C:\Windows".into());
        local_fixed(&Path::new(&windir).join("notepad.exe")).unwrap();
    }
}
