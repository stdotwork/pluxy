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
//! it holds the profile, when the earlier version has ended (`cleanup`).
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
use windows_sys::Win32::Storage::FileSystem::{GetDriveTypeW, MoveFileExW, MOVEFILE_WRITE_THROUGH};

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
pub(super) fn stage(_installed: &Path, bytes: Vec<u8>, _version: &str) -> Result<Staged, String> {
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
pub(super) fn stage_kept(_installed: &Path, kept: &Path, pin: &Pinned) -> Result<Staged, String> {
    let bytes = fs::read(kept).map_err(|e| format!("the kept copy {}: {e}", kept.display()))?;
    if sha256(&bytes) != pin.sha256 || !is_program(&bytes) {
        return Err(format!("the kept copy {} changed", kept.display()));
    }
    Ok(Staged {
        digest: pin.sha256.clone(),
        bytes,
    })
}

/// Puts staged in place of the installed program (see the module).
pub(super) fn swap_in(installed: &Path, staged: Staged) -> Result<(), String> {
    replace(installed, &staged.bytes)
}

/// What starts the installed copy: the program itself.
pub(super) fn program(installed: &Path) -> PathBuf {
    installed.to_path_buf()
}

/// Whether name is one this module gives a file next to the program: `.pluxy-update-<hex>` or
/// `.pluxy-old-<hex>.exe`, with 16 lowercase hex digits.
fn ours(name: &str) -> bool {
    let hex = |h: &str| h.len() == 16 && h.bytes().all(|c| matches!(c, b'0'..=b'9' | b'a'..=b'f'));
    name.strip_prefix(".pluxy-update-").is_some_and(hex)
        || name
            .strip_prefix(".pluxy-old-")
            .and_then(|r| r.strip_suffix(".exe"))
            .is_some_and(hex)
}

/// Removes what an earlier replace left next to the installed program: the aside copy of a
/// version that has ended, or a new file a failed replace did not take back. A file in use is
/// waited out for `BUSY_FOR` (the earlier version's image can stay mapped for a moment after this
/// app took the profile); one still in use then stays for the next start.
pub(super) fn cleanup(installed: &Path) {
    use std::os::windows::fs::MetadataExt;
    let Some(folder) = installed.parent() else {
        return;
    };
    let Ok(entries) = fs::read_dir(folder) else {
        return;
    };
    for e in entries.flatten() {
        let plain = e.metadata().is_ok_and(|m| {
            m.is_file()
                && m.file_attributes()
                    & windows_sys::Win32::Storage::FileSystem::FILE_ATTRIBUTE_REPARSE_POINT
                    == 0
        });
        if plain && e.file_name().to_str().is_some_and(ours) {
            let path = e.path();
            let _ = while_busy(|| fs::remove_file(&path));
        }
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

/// Replaces the program with bytes (see the module): a new file next to it, written and flushed;
/// the program renamed aside; the new file renamed into its place, or the program renamed back.
fn replace(installed: &Path, bytes: &[u8]) -> Result<(), String> {
    use std::io::Write;
    let folder = installed.parent().ok_or("The program has no folder.")?;
    let mut nonce = [0u8; 8];
    getrandom::fill(&mut nonce).map_err(|e| e.to_string())?;
    let hex = crate::artifact::hex(&nonce);
    let new = folder.join(format!(".pluxy-update-{hex}"));
    let aside = folder.join(format!(".pluxy-old-{hex}.exe"));
    let shown = |e: std::io::Error| format!("{}: {e}", installed.display());
    let written = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(&new)
        .and_then(|mut f| {
            f.write_all(bytes)?;
            f.sync_all()
        });
    if let Err(e) = written {
        let _ = fs::remove_file(&new);
        return Err(shown(e));
    }
    if let Err(e) = rename(installed, &aside) {
        let _ = fs::remove_file(&new);
        return Err(shown(e));
    }
    if let Err(e) = rename(&new, installed) {
        let back = rename(&aside, installed);
        let _ = fs::remove_file(&new);
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
            assert!(stage(Path::new("x"), b, "0.2.0").is_err(), "{what}");
        }
    }

    // The new program is in place, the old one aside, and nothing else is left once cleaned up;
    // a running program is renamed aside the same way (the native test shows it on a runner).
    #[test]
    fn the_replace_renames_aside_and_cleanup_removes_what_is_left() {
        let tmp = super::super::tests::temp("exe-replace");
        let exe = fixture(&tmp, "old");
        fs::write(tmp.join("other.txt"), b"not ours").unwrap();
        fs::write(tmp.join(".pluxy-old-zz.exe"), b"not ours either").unwrap();
        replace(&exe, &program_bytes("new")).unwrap();
        assert_eq!(fs::read(&exe).unwrap(), program_bytes("new"));
        let names = |t: &Path| {
            let mut v: Vec<String> = fs::read_dir(t)
                .unwrap()
                .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
                .collect();
            v.sort();
            v
        };
        let left = names(&tmp);
        assert_eq!(left.len(), 4, "{left:?}");
        assert!(left.iter().any(|n| ours(n) && n.starts_with(".pluxy-old-")));
        cleanup(&exe);
        assert_eq!(
            names(&tmp),
            [".pluxy-old-zz.exe", "other.txt", "pluxy.exe"],
            "only this module's own names go"
        );
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
        let e = replace(&exe, &program_bytes("new")).unwrap_err();
        assert!(started.elapsed() >= BUSY_FOR, "it waited for the file: {e}");
        drop(held);
        assert_eq!(fs::read(&exe).unwrap(), program_bytes("old"));
        assert_eq!(fs::read_dir(&tmp).unwrap().count(), 1);
        fs::remove_dir_all(&tmp).unwrap();
    }

    // An aside copy still held open for a moment after this version took the profile (the earlier
    // version's image not yet unmapped) is waited out and removed, not left for the next start.
    #[test]
    fn cleanup_waits_out_a_leftover_held_open_for_a_moment() {
        use std::os::windows::fs::OpenOptionsExt;
        let tmp = super::super::tests::temp("exe-cleanup-busy");
        let exe = fixture(&tmp, "new");
        let aside = tmp.join(".pluxy-old-0123456789abcdef.exe");
        fs::write(&aside, program_bytes("old")).unwrap();
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
        cleanup(&exe);
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
