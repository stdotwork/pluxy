//! Windows: what the window of a terminal launch runs (`launch.rs`, `terminal`). Windows cannot
//! run an open file, so the program runs by the path the launch names, which `hold` first makes
//! sure names a program no other account could have changed since the app checked it, and that
//! no one can change until it runs:
//!
//! - the program and every folder above it are opened and held with deleting shared with no one
//!   (and the program with writing shared with no one too), so none of them can be renamed,
//!   removed or replaced, nor the program written, while they are held;
//! - with all of them held, the path names what is held: the program's final path is the path
//!   named (so no link, junction or short name is on the way), each folder's final path is its
//!   part of that path, and the path opened again is the same file (volume serial number and file
//!   ID);
//! - the program is a program image (`.exe` or `.com`, starting `MZ`), as the app's own pin of a
//!   program requires (`pin_file` in `next/app/src-tauri/src/owned/windows.rs`), and a `.com`
//!   has no `.com.exe` beside it, which std's `Command` would run instead;
//! - no account but this user, SYSTEM, Administrators and TrustedInstaller may change the program
//!   or its folder in any way (another's file in the folder would be loaded as one of the
//!   program's libraries), nor remove or rename what a folder above holds
//!   (`pluxy_files::others_may_change`, the app tools' Windows rule, read through the held
//!   handles). Adding to a folder above is allowed, as every account may in `C:\`.
//!
//! So a program another account swapped in after the app's check, or could swap in before it
//! runs, never runs: what it swapped in is its own, or it held a right this refuses. Only this
//! user's own processes, and accounts as trusted as Administrators, could change it before the
//! hold, as on Linux and macOS.

use pluxy_files::Change;
use std::fs::{File, OpenOptions};
use std::os::windows::fs::{FileExt, MetadataExt, OpenOptionsExt};
use std::os::windows::io::AsRawHandle;
use std::path::{Path, PathBuf};
use windows_sys::Win32::Foundation::HANDLE;
use windows_sys::Win32::Storage::FileSystem::{
    GetFinalPathNameByHandleW, FILE_ATTRIBUTE_REPARSE_POINT, FILE_FLAG_BACKUP_SEMANTICS,
    FILE_FLAG_OPEN_REPARSE_POINT, FILE_NAME_NORMALIZED, FILE_SHARE_READ, FILE_SHARE_WRITE,
    VOLUME_NAME_DOS,
};

/// The program a terminal runs, held with the folders above it (see the module) until dropped.
pub struct Held {
    pub path: PathBuf,
    _file: File,
    _folders: Vec<File>,
}

/// p in double quotes, as a refusal names a path.
fn quoted(p: &Path) -> String {
    format!("\"{}\"", p.display())
}

/// The program at the full path program, held as the module says, or why it is refused (naming
/// the path it is refused for).
pub fn hold(program: &str) -> Result<Held, String> {
    let path = Path::new(program);
    let refuse = |p: &Path, why: &str| Err(format!("{} {why}", quoted(p)));
    if !path.is_absolute() {
        return refuse(path, "is not a full path");
    }
    let file = OpenOptions::new()
        .read(true)
        .share_mode(FILE_SHARE_READ)
        .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
        .open(path)
        .map_err(|e| format!("{} could not be opened: {e}", quoted(path)))?;
    let m = file
        .metadata()
        .map_err(|e| format!("{}: {e}", quoted(path)))?;
    if m.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
        return refuse(path, "is a link");
    }
    if !m.is_file() {
        return refuse(path, "is not a file");
    }
    image(&file, path)?;
    // Every folder above it, from its own up to the drive's (or share's) root.
    let mut folders = Vec::new();
    for d in path.ancestors().skip(1) {
        let f = OpenOptions::new()
            .read(true)
            .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
            .custom_flags(FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT)
            .open(d)
            .map_err(|e| format!("{} could not be opened: {e}", quoted(d)))?;
        let m = f.metadata().map_err(|e| format!("{}: {e}", quoted(d)))?;
        if m.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
            return refuse(d, "is a link or a junction");
        }
        if !m.is_dir() {
            return refuse(d, "is not a folder");
        }
        folders.push((d, f));
    }
    // All held: the path names exactly what is held.
    if final_path(&file)? != program {
        return refuse(
            path,
            "is not the program's own path: a link, a junction or a short name is on the way, or it was moved",
        );
    }
    for (d, f) in &folders {
        if d.as_os_str() != final_path(f)?.as_str() {
            return refuse(d, "is no longer the folder on the program's path");
        }
    }
    let same = matches!(
        (pluxy_files::id_of(&file), pluxy_files::id_of_path(path)),
        (Ok(a), Ok(b)) if a == b
    );
    if !same {
        return refuse(path, "is no longer the file that was opened");
    }
    // std's `Command` runs `<program>.exe`, when there is one, in place of a program not named
    // `.exe` (a `.com`).
    if !program.to_ascii_lowercase().ends_with(".exe") {
        let twin = PathBuf::from(format!("{program}.exe"));
        // Only a twin known to be absent passes: any other answer, an error included, refuses.
        match twin.symlink_metadata() {
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Ok(_) => return refuse(&twin, "is there, and would run in place of the program"),
            Err(e) => return refuse(&twin, &format!("could not be checked: {e}")),
        }
    }
    let others =
        |f: &File, p: &Path, change: Change| match pluxy_files::others_may_change(f, change) {
            Ok(None) => Ok(()),
            Ok(Some(why)) => Err(format!(
                "{} may be changed by another account: it {why}",
                quoted(p)
            )),
            Err(e) => Err(format!(
                "{}: could not read who may change it ({e})",
                quoted(p)
            )),
        };
    others(&file, path, Change::Any)?;
    for (i, (d, f)) in folders.iter().enumerate() {
        others(f, d, if i == 0 { Change::Any } else { Change::Entries })?;
    }
    Ok(Held {
        path: path.to_path_buf(),
        _file: file,
        _folders: folders.into_iter().map(|(_, f)| f).collect(),
    })
}

/// Refused unless f, at path, is a program image: named `.exe` or `.com`, starting `MZ` (what
/// `CreateProcessW` runs itself, never a script it would hand to `cmd.exe`).
fn image(f: &File, path: &Path) -> Result<(), String> {
    let named = path
        .extension()
        .and_then(|e| e.to_str())
        .is_some_and(|e| e.eq_ignore_ascii_case("exe") || e.eq_ignore_ascii_case("com"));
    let mut b = [0u8; 2];
    let mz = f.seek_read(&mut b, 0).ok() == Some(2) && &b == b"MZ";
    if !(named && mz) {
        return Err(format!(
            "{} is not a program image (an .exe or .com file starting MZ)",
            quoted(path)
        ));
    }
    Ok(())
}

/// The final path of the object open as f, in its plain form (`C:\…`, `\\server\share\…`).
fn final_path(f: &File) -> Result<String, String> {
    let mut buf = vec![0u16; 512];
    loop {
        // SAFETY: f is open; buf holds buf.len() characters.
        let n = unsafe {
            GetFinalPathNameByHandleW(
                f.as_raw_handle() as HANDLE,
                buf.as_mut_ptr(),
                buf.len() as u32,
                FILE_NAME_NORMALIZED | VOLUME_NAME_DOS,
            )
        } as usize;
        if n == 0 {
            return Err(format!(
                "finding the program's path: {}",
                std::io::Error::last_os_error()
            ));
        }
        if n < buf.len() {
            buf.truncate(n);
            break;
        }
        buf.resize(n + 1, 0);
    }
    let full = String::from_utf16(&buf).map_err(|_| "the program's path is not valid Unicode")?;
    crate::winpath::plain(&full)
}
