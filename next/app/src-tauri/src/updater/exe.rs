//! Windows: the program set of a folder bundle this user keeps, or of a copy the per-user setup
//! program installed (`set.rs`): the app's program (one self-contained `.exe`: its WebView2
//! loader is linked in and its pages are embedded), the core beside it (`pluxy.exe`) and the
//! core's packages (`bundled\`), pinned by the set's digest. The setup's `uninstall.exe` beside
//! them is not part of the set. A copy an MSI keeps (an .msi patches its bundle type in) is
//! refused, with how to update instead, and so is a copy of an unknown format, on a network drive
//! or one anyone else could change. The setup program is never run: once a version accepts itself
//! after an update or a rollback, it writes its version into the setup's uninstall entry
//! (`uninstall_entry.rs`).
//!
//! The update is a `.tar.gz` of the set, unpacked into a new folder of this user's beside the
//! program (`.pluxy-update-<hex>`, inheriting the program folder's access list) and checked. Then,
//! by the move list `pending.json` records before anything moves, each part is renamed aside
//! (`.pluxy-old-<hex>-<part>`; a running program can be renamed but not replaced or removed) and
//! the new one renamed into its place, each rename written through to the disk: `bundled\`, then
//! `pluxy.exe`, then the program. Nothing runs the core files beside the app (the app runs the
//! copies in its core store), so a part may be moved while the app runs. A part another program
//! holds open (a virus scanner, or a person's own `pluxy.exe` with a file open in `bundled\`) is
//! waited out for `BUSY_FOR`; if it is still held, the parts already moved are put back. Between
//! the program's two renames (two metadata operations of this process) the folder holds no
//! program under its name: a power loss there leaves the aside copy and the kept set, and
//! `docs/app-design.md` ("Putting a kept copy back by hand") says how to put them back. A crash
//! before that is undone at the next start of the earlier version (`set::recover`).
//!
//! What is left aside is removed once the version that replaced it holds the profile, when the
//! earlier version has ended (`cleanup`): the staging folder and each aside part are recorded with
//! their volume serial and file ID before they are left there (`leftovers.rs`), and only a
//! recorded file or folder that is still that one is removed, through its handle, never following a
//! link. While `pending.json` records an install that is not finished, its staging folder and
//! asides stay.
use super::leftovers::{self, Made, Outcome};
use super::{read_json, set, Pending, Pinned};
use std::ffi::OsStr;
use std::fs;
use std::os::windows::ffi::OsStrExt;
use std::os::windows::fs::MetadataExt;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::AppHandle;
use windows_sys::Win32::Foundation::{
    ERROR_ACCESS_DENIED, ERROR_LOCK_VIOLATION, ERROR_SHARING_VIOLATION,
};
use windows_sys::Win32::Storage::FileSystem::{
    FileDispositionInfo, GetDriveTypeW, MoveFileExW, SetFileInformationByHandle, DELETE,
    FILE_ATTRIBUTE_DIRECTORY, FILE_ATTRIBUTE_REPARSE_POINT, FILE_DISPOSITION_INFO,
    FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT, FILE_LIST_DIRECTORY,
    FILE_READ_ATTRIBUTES, FILE_SHARE_DELETE, FILE_SHARE_READ, FILE_SHARE_WRITE,
    MOVEFILE_WRITE_THROUGH,
};

/// The updater's name for this system's artifact: Windows, this architecture, a program set.
#[cfg(target_arch = "x86_64")]
pub(super) const TARGET: &str = "windows-x86_64-set";
#[cfg(target_arch = "aarch64")]
pub(super) const TARGET: &str = "windows-aarch64-set";
/// The PE machine type of this architecture.
#[cfg(target_arch = "x86_64")]
const MACHINE: u16 = 0x8664;
#[cfg(target_arch = "aarch64")]
const MACHINE: u16 = 0xaa64;
/// What a kept copy's name ends in: nothing, it is a folder holding the set.
pub(super) const SUFFIX: &str = "";
/// What the installed copy is called in messages.
pub(super) const WHAT: &str = "program set";
/// What a pin's digest is, in messages.
pub(super) const DIGEST: &str = "set SHA-256";
/// How long a rename or a removal waits out a file another program (a virus scanner, or the
/// earlier version not yet unmapped) has open.
const BUSY_FOR: Duration = Duration::from_secs(5);
/// `GetDriveTypeW`'s answer for a fixed local drive.
const DRIVE_FIXED: u32 = 3;
/// How deep a recorded folder's removal goes: a set's `bundled\` is far shallower.
const DEEPEST: usize = 64;
/// What every open here shares: nothing it opens is kept from anyone else.
const SHARE_ALL: u32 = FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE;

/// How this copy of the app was installed, as far as updating it goes: the program set of a
/// folder bundle, or of a copy the setup program installed, that this user keeps on a local drive
/// (its folder), or why it cannot be updated from here.
pub(super) fn provenance(app: &AppHandle) -> Result<PathBuf, String> {
    use tauri::utils::config::BundleType;
    match tauri::utils::platform::bundle_type() {
        // A folder bundle, or a copy the setup program installed. The update's program is the
        // one built, with no bundle type, so a copy the setup installed reads as a folder bundle
        // once updated; nothing here treats the two differently.
        None | Some(BundleType::Nsis) => {}
        Some(BundleType::Msi) => {
            return Err(
                "This copy of pluxy was installed from an .msi package. Install the new \
                 version with its .msi package, not from here."
                    .into(),
            )
        }
        Some(_) => {
            return Err(
                "This copy of pluxy is not a folder bundle or one its setup program installed, \
                 the only kinds this updater can replace on Windows (its install format is \
                 unknown). Install the new version the way you installed this one."
                    .into(),
            )
        }
    }
    let folder = here(app)
        .ok_or("This program cannot tell where it is on disk; install the new version by hand.")?;
    let exe = program(&folder);
    local_fixed(&exe)?;
    // shortcut: as on Linux, only the program and its folder are checked, and the replace
    // renames by path; a folder further up that others could change could be swapped. Upgrade by
    // checking the ancestors, if updating on shared machines is wanted.
    for (path, is_folder) in [(exe.as_path(), false), (folder.as_path(), true)] {
        crate::win_acl::check(path, is_folder, crate::win_acl::Others::Read).map_err(|e| {
            format!(
                "{} is not a {} of yours that only you can change (an installer or another user \
                 may keep it): {e}. Update it the way it was installed.",
                path.display(),
                if is_folder { "folder" } else { "file" }
            )
        })?;
    }
    super::open_record(&super::dir(app)?, &folder)?;
    Ok(folder)
}

/// Refused unless path is on a fixed local drive (`C:\...`): not a network share or a mapped,
/// removable or virtual drive, where renames are not the local file system's.
pub(super) fn local_fixed(path: &Path) -> Result<(), String> {
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

/// The folder of the program this process runs from, by the path it has now with every link
/// resolved: the installed copy, the set.
pub(super) fn here(_app: &AppHandle) -> Option<PathBuf> {
    own_path()?.parent().map(Path::to_path_buf)
}

/// This process's program by the path it has now with every link resolved, in the names and case
/// the disk holds (the launch's spelling may differ: other case, an 8.3 name).
fn own_path() -> Option<PathBuf> {
    let exe = std::env::current_exe().ok()?;
    let f = fs::File::open(exe).ok()?;
    crate::owned::windows::final_path(&f).ok()
}

/// The app program's name, the set's program part: this process's own, as the disk holds it (in
/// tests, the fixture's), or "" if it cannot be read. Kept from the first call that reads it (as
/// the app starts, and at the latest as an update stages, before any part moves), so it holds
/// while the program is renamed aside.
pub(super) fn program_name() -> String {
    #[cfg(test)]
    return set::tests::EXE.into();
    #[cfg(not(test))]
    {
        static NAME: std::sync::OnceLock<String> = std::sync::OnceLock::new();
        if let Some(n) = NAME.get() {
            return n.clone();
        }
        match own_path().and_then(|p| p.file_name().map(|n| n.to_string_lossy().into_owned())) {
            Some(n) => NAME.get_or_init(|| n).clone(),
            None => String::new(),
        }
    }
}

/// The set digest of the set in the folder at path.
pub(super) fn digest(path: &Path) -> Result<String, String> {
    set::digest(path, &program_name())
}

/// Copies the set in the folder from to the new private folder to.
pub(super) fn copy_new(from: &Path, to: &Path) -> Result<(), String> {
    crate::private_windows::create_private_dir_all(to)
        .map_err(|e| format!("{}: {e}", to.display()))?;
    set::copy(from, to, &program_name())
}

/// Removes a kept set (a folder in the app's own updates folder) or a file there.
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

/// A checked set in its staging folder beside the installed one, ready to move in. The folder
/// is removed if it is not moved in, only while it is still the folder made (`Stage`).
pub(super) struct Staged {
    digest: String,
    staging: Stage,
}

impl Staged {
    pub(super) fn digest(&self) -> &str {
        &self.digest
    }

    /// The move list that puts it in place.
    pub(super) fn moves(&self) -> set::Moves {
        set::Moves::new(&self.staging.name(), &program_name())
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

/// Refused unless the set in staging has its program part a program for this system.
fn check_program(staging: &Path) -> Result<(), String> {
    use std::io::Read;
    let exe = staging.join(program_name());
    let mut head = Vec::new();
    fs::File::open(&exe)
        .and_then(|f| f.take(4096).read_to_end(&mut head))
        .map_err(|e| format!("{}: {e}", exe.display()))?;
    if !is_program(&head) {
        return Err(format!(
            "The update's {} is not a Windows program for this system ({TARGET}).",
            program_name()
        ));
    }
    Ok(())
}

/// Unpacks the update's verified archive into a new staging folder in the installed set's
/// folder and checks it: the set of this program (`set::unpack`), its program one for this
/// system.
pub(super) fn stage(
    installed: &Path,
    bytes: Vec<u8>,
    _version: &str,
    dir: &Path,
) -> Result<Staged, String> {
    let staging = Stage::new(dir, installed)?;
    set::unpack(&bytes, staging.path(), &program_name())?;
    check_program(staging.path())?;
    Ok(Staged {
        digest: set::digest(staging.path(), &program_name())?,
        staging,
    })
}

/// The kept set at kept, copied into a new staging folder in the installed set's folder, if the
/// copy still has its pinned digest and its program is one for this system.
pub(super) fn stage_kept(
    installed: &Path,
    kept: &Path,
    pin: &Pinned,
    dir: &Path,
) -> Result<Staged, String> {
    let staging = Stage::new(dir, installed)?;
    set::copy(kept, staging.path(), &program_name())
        .map_err(|e| format!("the kept copy {}: {e}", kept.display()))?;
    if set::digest(staging.path(), &program_name()).ok().as_deref() != Some(pin.sha256.as_str())
        || check_program(staging.path()).is_err()
    {
        return Err(format!("the kept copy {} changed", kept.display()));
    }
    Ok(Staged {
        digest: pin.sha256.clone(),
        staging,
    })
}

/// Puts staged in place of the installed set, by the move list of the pending record in the
/// updates folder dir, which must be this replace's (see the module). An update's or a rollback's
/// parts already moved are put back if one cannot be moved; a put-back's are left for the next
/// start to finish. The staging folder stays (recorded) while it is needed.
pub(super) fn swap_in(installed: &Path, staged: Staged, dir: &Path) -> Result<(), String> {
    let mut staged = staged;
    let p = read_json::<Pending>(&dir.join("pending.json"))
        .filter(|p| p.installed == installed && p.moves.as_ref() == Some(&staged.moves()))
        .ok_or("the pending record does not list this replace's moves")?;
    let m = staged.moves();
    let undo_from = (p.kind != "put-back").then(|| super::kept_path(dir, &p.from));
    let r = set::apply(&Moving::new(dir), installed, &m, undo_from.as_deref());
    if r.is_ok() || set::unresolved(installed, &m) {
        staged.staging.keep();
    }
    r
}

/// What starts the installed set: its program, by the name this process has.
pub(super) fn program(installed: &Path) -> PathBuf {
    installed.join(program_name())
}

/// The real file operations of a replace (`set::Ops`), recording asides in the updates folder.
pub(super) struct Moving<'a> {
    dir: &'a Path,
}

impl<'a> Moving<'a> {
    pub(super) fn new(dir: &'a Path) -> Moving<'a> {
        Moving { dir }
    }
}

impl set::Ops for Moving<'_> {
    fn rename(&self, from: &Path, to: &Path) -> std::io::Result<()> {
        rename(from, to)
    }

    fn record(&self, part: &Path, aside: &Path) -> Result<(), String> {
        let f = open_any(part, 0).map_err(|e| format!("{}: {e}", part.display()))?;
        if f.metadata()
            .map(|m| m.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0)
            .unwrap_or(true)
        {
            return Err(format!("{} is a link", part.display()));
        }
        record(self.dir, aside, &f).map(drop)
    }

    /// The native test's kill between two parts: with `<dir>/hold-parts`, this process says where
    /// it is and waits there, once, for the session to end it.
    fn between(&self, part: &str) {
        #[cfg(feature = "native-test")]
        if let Some(d) = std::env::var_os("PLUXY_APP_TEST_UPDATE")
            .map(PathBuf::from)
            .filter(|d| d.join("hold-parts").exists())
        {
            let _ = fs::remove_file(d.join("hold-parts"));
            eprintln!(
                "native-test: update_between_parts {}",
                serde_json::json!({"pid": std::process::id(), "part": part})
            );
            let until = Instant::now() + Duration::from_secs(120);
            while Instant::now() < until {
                std::thread::sleep(Duration::from_millis(100));
            }
        }
        #[cfg(not(feature = "native-test"))]
        let _ = part;
    }
}

/// Opens path itself, a file or a folder, never following a link, with access (and its
/// attributes): for its identity, or to list a folder.
fn open_any(path: &Path, access: u32) -> std::io::Result<fs::File> {
    use std::os::windows::fs::OpenOptionsExt;
    fs::OpenOptions::new()
        .access_mode(access | FILE_READ_ATTRIBUTES)
        .share_mode(SHARE_ALL)
        .custom_flags(FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT)
        .open(path)
}

/// A staging folder made in the installed set's folder for one install and recorded in the
/// updates folder. Dropped, as a failed or cancelled install drops it, it is removed as a recorded
/// entry is (`remove_recorded`): only while it is still the folder made. One kept (`keep`) stays,
/// recorded for a later start's cleanup.
struct Stage {
    made: Made,
    kept: bool,
}

impl Stage {
    /// Makes `.pluxy-update-<hex>` in the folder installed (it takes the folder's access list),
    /// reads its identity through one open of it that does not follow a link (it must be an
    /// empty folder), and records it in dir; if it cannot be recorded it is removed again.
    fn new(dir: &Path, installed: &Path) -> Result<Stage, String> {
        let mut nonce = [0u8; 8];
        getrandom::fill(&mut nonce).map_err(|e| e.to_string())?;
        let path = installed.join(format!(".pluxy-update-{}", crate::artifact::hex(&nonce)));
        let shown = |e: std::io::Error| format!("{}: {e}", path.display());
        fs::create_dir(&path).map_err(shown)?;
        let f = open_any(&path, FILE_LIST_DIRECTORY).map_err(shown)?;
        let m = f.metadata().map_err(shown)?;
        let listed = crate::vault::entries(&f);
        if m.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0
            || !m.is_dir()
            || !listed.map_err(shown)?.is_empty()
        {
            return Err(format!("{} is not a new empty folder", path.display()));
        }
        let made = Made {
            path: path.clone(),
            id: file_id(&f).map_err(shown)?,
        };
        drop(f);
        if let Err(e) = leftovers::record(dir, &path, made.id.clone()) {
            #[cfg(test)]
            tests::before_removing(&path);
            remove_recorded(&made);
            return Err(e);
        }
        Ok(Stage { made, kept: false })
    }

    fn path(&self) -> &Path {
        &self.made.path
    }

    fn name(&self) -> String {
        self.made
            .path
            .file_name()
            .map(|n| n.to_string_lossy().into_owned())
            .unwrap_or_default()
    }

    fn keep(&mut self) {
        self.kept = true;
    }
}

impl Drop for Stage {
    fn drop(&mut self) {
        if !self.kept {
            #[cfg(test)]
            tests::before_removing(&self.made.path);
            remove_recorded(&self.made);
        }
    }
}

/// The volume serial number and file ID of the open file or folder f, in hex: what a recorded
/// entry is.
fn file_id(f: &fs::File) -> std::io::Result<String> {
    crate::owned::windows::file_id(f).map(|id| crate::artifact::hex(&id))
}

/// Records path, the open file or folder f's name now or once renamed, in the updates folder dir:
/// its identity as recorded.
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

/// Removes what an earlier install recorded that it left in the installed set's folder (a
/// staging folder, the aside parts of a version that has ended), each only while it is still
/// what was recorded (`remove_recorded`), except what a pending install that is not finished
/// still needs (`set::in_use`). The program's folder is not listed.
pub(super) fn cleanup(dir: &Path) {
    let busy = read_json::<Pending>(&dir.join("pending.json"))
        .map(|p| set::in_use(&p))
        .unwrap_or_default();
    leftovers::clean(dir, |m| {
        if busy.contains(&m.path) {
            Outcome::Kept
        } else {
            remove_recorded(m)
        }
    });
}

/// Removes the recorded file or folder m: opened without following a link and with delete
/// access, it must be a plain file or folder with the recorded volume serial and file ID; a file is
/// then deleted through that handle, never by its name, and a folder emptied through its handle
/// first (`empty`). Something in use is waited out for `BUSY_FOR` (the earlier version's image can
/// stay mapped for a moment after this app took the profile); still in use then, it stays recorded
/// for the next start. A link at its name, or something else, is not it: not there.
fn remove_recorded(m: &Made) -> Outcome {
    let mut not_there = false;
    let r = while_busy(|| {
        // List access too, to empty a folder; on a file that is read access, which only a holder
        // sharing delete but not read refuses (then waited out as a file in use).
        let opened = open_any(&m.path, DELETE | FILE_LIST_DIRECTORY);
        let f = match opened {
            Ok(f) => f,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                not_there = true;
                return Ok(());
            }
            // Another user's folder or link that this user may not open: not what was recorded.
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
        if md.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0
            || !(md.is_file() || md.is_dir())
            || file_id(&f)? != m.id
        {
            not_there = true;
            return Ok(());
        }
        if md.is_dir() {
            empty(&f, 0)?;
            return crate::vault::delete(&f);
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

/// Removes everything in the folder open as dir (listed through it), through it: each entry
/// opened relative to it without following a link (`vault_windows::at`), folders emptied first, a
/// link removed as a link.
fn empty(dir: &fs::File, depth: usize) -> std::io::Result<()> {
    if depth > DEEPEST {
        return Err(std::io::Error::other("too deep"));
    }
    for (name, attributes) in crate::vault::entries(dir)? {
        let folder = attributes & FILE_ATTRIBUTE_DIRECTORY != 0;
        let link = attributes & FILE_ATTRIBUTE_REPARSE_POINT != 0;
        let access = DELETE
            | FILE_READ_ATTRIBUTES
            | if folder && !link {
                FILE_LIST_DIRECTORY
            } else {
                0
            };
        let f = crate::vault::at(dir, &name, access, SHARE_ALL, folder, None)?;
        let md = f.metadata()?;
        if md.is_dir() && md.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT == 0 {
            #[cfg(test)]
            tests::before_emptying(&name);
            empty(&f, depth + 1)?;
        }
        crate::vault::delete(&f)?;
    }
    Ok(())
}

fn wide(p: &Path) -> Vec<u16> {
    p.as_os_str().encode_wide().chain([0]).collect()
}

/// Renames from to to (which must not exist), written through to the disk; a file another
/// program has open is waited out for `BUSY_FOR`.
pub(super) fn rename(from: &Path, to: &Path) -> std::io::Result<()> {
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

#[cfg(test)]
pub(super) mod tests {
    use super::super::set::tests::{payload, write_set, EXE};
    use super::*;
    use std::sync::Mutex;

    /// Run just before a staging folder made in the folder a test set it for is removed (its
    /// record failed, or it was dropped). Tests run in parallel, so each test's swap is an entry
    /// of its own (`hook_staging`), which no other test replaces or clears.
    static SWAP: Mutex<Vec<Swap>> = Mutex::new(Vec::new());

    /// The folder a swap is for, and the swap.
    type Swap = (PathBuf, fn(&Path));

    /// The swap one test set (`hook_staging`): dropping it removes that entry only.
    struct Hooked(PathBuf);

    impl Drop for Hooked {
        fn drop(&mut self) {
            SWAP.lock()
                .unwrap_or_else(|e| e.into_inner())
                .retain(|(folder, _)| *folder != self.0);
        }
    }

    /// Sets f to run for a staging folder in folder, until the value returned is dropped.
    fn hook_staging(folder: &Path, f: fn(&Path)) -> Hooked {
        SWAP.lock().unwrap().push((folder.to_path_buf(), f));
        Hooked(folder.to_path_buf())
    }

    pub(super) fn before_removing(path: &Path) {
        let hook = SWAP
            .lock()
            .unwrap()
            .iter()
            .find(|(folder, _)| Some(folder.as_path()) == path.parent())
            .map(|(_, f)| *f);
        if let Some(f) = hook {
            f(path);
        }
    }

    /// Run by `empty` before it empties a folder of the name set here, with the path set here.
    static BEFORE_EMPTYING: Mutex<Option<BeforeEmptying>> = Mutex::new(None);

    /// The folder's name, the path to act on, and what to do to it.
    type BeforeEmptying = (String, PathBuf, fn(&Path));

    pub(super) fn before_emptying(name: &str) {
        let hook = match &*BEFORE_EMPTYING.lock().unwrap() {
            Some((n, p, f)) if n == name => Some((p.clone(), *f)),
            _ => None,
        };
        if let Some((p, f)) = hook {
            f(&p);
        }
    }

    /// Moves the folder at p aside (to `<p>.ours`) and puts another folder, holding "theirs",
    /// at its name.
    fn swap_folder(p: &Path) {
        fs::rename(p, p.with_extension("ours")).unwrap();
        fs::create_dir(p).unwrap();
        fs::write(p.join("theirs"), b"z").unwrap();
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

    /// An installed set at `<tmp>/pluxy Test` whose parts say v.
    pub(crate) fn fixture(tmp: &Path, v: &str) -> PathBuf {
        let folder = tmp.join("pluxy Test");
        fs::create_dir_all(&folder).unwrap();
        write_set(&folder, v, &program_bytes(v));
        folder
    }

    /// Changes a kept set.
    pub(crate) fn tamper(path: &Path) {
        let index = path.join("bundled").join("index.json");
        let mut b = fs::read(&index).unwrap();
        b.push(0);
        fs::write(index, b).unwrap();
    }

    /// The names in t, sorted.
    fn names(t: &Path) -> Vec<String> {
        let mut v: Vec<String> = fs::read_dir(t)
            .unwrap()
            .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
            .collect();
        v.sort();
        v
    }

    fn set_names() -> Vec<String> {
        let mut v: Vec<String> = set::parts(EXE).iter().map(|s| s.to_string()).collect();
        v.sort();
        v
    }

    /// Stages the update of the set in folder to version v and writes its pending record, as an
    /// install does before it moves anything.
    fn ready(folder: &Path, dir: &Path, v: &str) -> (Staged, Pending) {
        let staged = stage(folder, payload(&program_bytes(v)), v, dir).unwrap();
        // Kept as an install keeps it: a kept set already there with this digest is used again.
        let from = super::super::keep(dir, folder, "0.1.0", &digest(folder).unwrap()).unwrap();
        let to = Pinned {
            version: "0.2.0".into(),
            sha256: staged.digest().into(),
        };
        let p =
            super::super::pending(dir, "update", from, to, folder, Some(staged.moves())).unwrap();
        (staged, p)
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
        let tmp = super::super::tests::temp("exe-stage");
        let folder = fixture(&tmp, "old");
        let dir = tmp.join("updates");
        for (what, b) in [
            ("a library", dll),
            ("another machine", other),
            ("32-bit", pe32),
            ("a driver", driver),
            ("cut short", short),
            ("not MZ", b"\x7fELF".to_vec()),
        ] {
            assert!(!is_program(&b), "{what}");
            let e = stage(&folder, payload(&b), "0.2.0", &dir).err().unwrap();
            assert!(e.contains("not a Windows program"), "{what}: {e}");
        }
        // A payload without the core is refused too, and no staging folder is left.
        let no_core = set::tests::targz(&[
            (EXE.as_bytes(), b'0', 0o755, &program_bytes("new"), ""),
            (b"bundled/", b'5', 0o755, b"", ""),
        ]);
        let e = stage(&folder, no_core, "0.2.0", &dir).err().unwrap();
        assert!(e.contains("holds no pluxy.exe"), "{e}");
        assert_eq!(names(&folder), set_names());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // The new set is in place, each old part aside, and nothing else is left once cleaned up;
    // a running program is renamed aside the same way (the native test shows it on a runner).
    // Only what the replace recorded goes: a file another user renamed to an aside's name stays.
    #[test]
    fn the_replace_moves_each_part_and_cleanup_removes_what_is_left() {
        let tmp = super::super::tests::temp("exe-replace");
        let dir = tmp.join("updates");
        let folder = fixture(&tmp, "old");
        fs::write(folder.join("uninstall.exe"), b"not ours").unwrap();
        fs::write(
            folder.join(".pluxy-old-0123456789abcdef-pluxy.exe"),
            b"not ours either",
        )
        .unwrap();
        let (staged, p) = ready(&folder, &dir, "new");
        swap_in(&folder, staged, &dir).unwrap();
        assert_eq!(digest(&folder).unwrap(), p.to.sha256);
        // Three asides and the empty staging folder, besides the set and the two others.
        assert_eq!(names(&folder).len(), 9, "{:?}", names(&folder));
        // The next version's cleanup, once this one has ended and the record is resolved.
        fs::remove_file(dir.join("pending.json")).unwrap();
        leftovers::forget_mine(&tmp);
        cleanup(&dir);
        let mut want = set_names();
        want.extend([
            ".pluxy-old-0123456789abcdef-pluxy.exe".to_string(),
            "uninstall.exe".to_string(),
        ]);
        want.sort();
        assert_eq!(names(&folder), want, "only what the replace recorded goes");
        assert_eq!(digest(&folder).unwrap(), p.to.sha256);
        fs::remove_dir_all(&tmp).unwrap();
    }

    // While the pending record is not resolved, cleanup leaves its staging folder and asides.
    #[test]
    fn cleanup_leaves_what_an_unfinished_install_needs() {
        let tmp = super::super::tests::temp("exe-unfinished");
        let dir = tmp.join("updates");
        let folder = fixture(&tmp, "old");
        let (staged, _) = ready(&folder, &dir, "new");
        swap_in(&folder, staged, &dir).unwrap();
        let before = names(&folder);
        leftovers::forget_mine(&tmp);
        cleanup(&dir);
        assert_eq!(names(&folder), before);
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A recorded aside file replaced by another file of the same name stays.
    #[test]
    fn cleanup_leaves_a_file_put_in_place_of_a_recorded_one() {
        let tmp = super::super::tests::temp("exe-replaced");
        let dir = tmp.join("updates");
        let folder = fixture(&tmp, "old");
        let (staged, p) = ready(&folder, &dir, "new");
        let aside = folder.join(p.moves.as_ref().unwrap().aside(set::CORE));
        swap_in(&folder, staged, &dir).unwrap();
        fs::remove_file(dir.join("pending.json")).unwrap();
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

    // A recorded aside folder goes with everything in it; a junction in it is removed, not
    // followed; a folder put in place of one being emptied is not entered.
    #[test]
    fn a_recorded_folder_goes_whole_and_a_junction_in_it_is_not_followed() {
        let tmp = super::super::tests::temp("exe-junction");
        let dir = tmp.join("updates");
        let outside = tmp.join("outside");
        fs::create_dir(&outside).unwrap();
        fs::write(outside.join("keep"), b"z").unwrap();
        let folder = fixture(&tmp, "old");
        let (staged, p) = ready(&folder, &dir, "new");
        let m = p.moves.clone().unwrap();
        swap_in(&folder, staged, &dir).unwrap();
        fs::remove_file(dir.join("pending.json")).unwrap();
        let aside = folder.join(m.aside(set::BUNDLED));
        let made = std::process::Command::new("cmd")
            .args(["/c", "mklink", "/J"])
            .arg(aside.join("packages").join("link"))
            .arg(&outside)
            .output()
            .unwrap();
        assert!(made.status.success(), "{made:?}");
        leftovers::forget_mine(&tmp);
        cleanup(&dir);
        assert!(!aside.exists(), "the aside folder is gone");
        assert!(outside.join("keep").exists(), "the junction was followed");
        assert_eq!(names(&folder), set_names());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A file in `bundled\` held open (not shared for delete) keeps that folder from moving: the
    // parts already moved are put back after the wait, and the set is the old one.
    #[test]
    fn a_part_held_open_is_waited_out_then_the_others_put_back() {
        use std::os::windows::fs::OpenOptionsExt;
        let tmp = super::super::tests::temp("exe-busy");
        let dir = tmp.join("updates");
        let folder = fixture(&tmp, "old");
        let old = digest(&folder).unwrap();
        // Held: bundled\ cannot move at all.
        let (staged, _) = ready(&folder, &dir, "new");
        let held = fs::OpenOptions::new()
            .read(true)
            .share_mode(FILE_SHARE_READ)
            .open(folder.join("bundled").join("index.json"))
            .unwrap();
        let started = Instant::now();
        let e = swap_in(&folder, staged, &dir).unwrap_err();
        assert!(started.elapsed() >= BUSY_FOR, "it waited for the file: {e}");
        drop(held);
        assert_eq!(digest(&folder).unwrap(), old);
        assert_eq!(names(&folder), set_names(), "nothing left beside the set");
        fs::remove_file(dir.join("pending.json")).unwrap();
        // Held: the core cannot move once bundled\ has; bundled\ goes back.
        let (staged, p) = ready(&folder, &dir, "new");
        let held = fs::OpenOptions::new()
            .read(true)
            .share_mode(FILE_SHARE_READ)
            .open(folder.join(set::CORE))
            .unwrap();
        assert!(swap_in(&folder, staged, &dir).is_err());
        drop(held);
        assert_eq!(digest(&folder).unwrap(), old);
        assert!(!set::unresolved(&folder, p.moves.as_ref().unwrap()));
        assert_eq!(names(&folder), set_names());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A put-back after a new version did not start that stops part way (the core held past the
    // wait, once bundled\ is back) keeps a record of its own, kind put-back, which the next start
    // finishes.
    #[test]
    fn a_put_back_that_cannot_finish_keeps_its_record_for_the_next_start() {
        use std::os::windows::fs::OpenOptionsExt;
        let tmp = super::super::tests::temp("exe-putback");
        let dir = tmp.join("updates");
        let folder = fixture(&tmp, "old");
        let old = digest(&folder).unwrap();
        let (staged, p) = ready(&folder, &dir, "new");
        swap_in(&folder, staged, &dir).unwrap();
        let held = fs::OpenOptions::new()
            .read(true)
            .share_mode(FILE_SHARE_READ)
            .open(folder.join(set::CORE))
            .unwrap();
        assert!(super::super::restore(&dir, &p).is_err());
        drop(held);
        let r: Pending =
            super::super::read_json(&dir.join("pending.json")).expect("the put-back's record");
        assert_eq!(r.kind, "put-back");
        assert_eq!(r.from, p.to);
        assert_eq!(r.to, p.from);
        assert!(set::unresolved(&folder, r.moves.as_ref().unwrap()));
        assert_ne!(
            digest(&folder).unwrap(),
            old,
            "bundled\\ back, the rest not"
        );
        // The next start (here the version that did not start) finishes it.
        let done = set::recover(
            &Moving::new(&dir),
            &dir,
            &r,
            &folder,
            EXE,
            "0.2.0",
            &folder.join(EXE),
        );
        assert_eq!(done, set::Recovery::PutBack);
        assert_eq!(digest(&folder).unwrap(), old);
        assert!(!dir.join("pending.json").exists());
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A staging folder dropped (a failed or cancelled install) goes only while it is still the
    // folder made: a folder put at its name first stays whole.
    #[test]
    fn a_dropped_stage_removes_only_the_folder_it_made() {
        let tmp = super::super::tests::temp("exe-drop");
        let dir = tmp.join("updates");
        let folder = fixture(&tmp, "old");
        drop(stage(&folder, payload(&program_bytes("new")), "0.2.0", &dir).unwrap());
        assert_eq!(names(&folder), set_names(), "dropped as made: removed");
        let staged = stage(&folder, payload(&program_bytes("new")), "0.2.0", &dir).unwrap();
        let hooked = hook_staging(&folder, swap_folder);
        drop(staged);
        drop(hooked);
        let theirs = names(&folder)
            .into_iter()
            .find(|n| n.starts_with(".pluxy-update-") && !n.ends_with(".ours"))
            .unwrap();
        assert!(
            folder.join(theirs).join("theirs").exists(),
            "not the folder made"
        );
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A staging folder whose record cannot be written is removed again, and the install told; it
    // goes only while it is still the folder made.
    #[test]
    fn a_stage_that_cannot_be_recorded_leaves_nothing() {
        let tmp = super::super::tests::temp("exe-norecord");
        let folder = fixture(&tmp, "old");
        // The updates folder's name holds a file: the record cannot be written.
        fs::write(tmp.join("updates"), b"not a folder").unwrap();
        assert!(stage(
            &folder,
            payload(&program_bytes("new")),
            "0.2.0",
            &tmp.join("updates")
        )
        .is_err());
        assert_eq!(names(&folder), set_names());
        let hooked = hook_staging(&folder, swap_folder);
        let r = stage(
            &folder,
            payload(&program_bytes("new")),
            "0.2.0",
            &tmp.join("updates"),
        );
        drop(hooked);
        assert!(r.is_err());
        assert_eq!(
            names(&folder).len(),
            set_names().len() + 2,
            "{:?}",
            names(&folder)
        );
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A folder in a recorded folder swapped for a junction after it was listed is not followed.
    #[test]
    fn a_folder_swapped_for_a_junction_while_emptying_is_not_followed() {
        let tmp = super::super::tests::temp("exe-swap-junction");
        let outside = tmp.join("outside");
        fs::create_dir(&outside).unwrap();
        fs::write(outside.join("keep"), b"z").unwrap();
        let path = tmp.join(".pluxy-old-0123456789abcdef-bundled");
        fs::create_dir_all(path.join("inner").join("deeper")).unwrap();
        fs::write(path.join("inner").join("deeper").join("f"), b"x").unwrap();
        let m = Made {
            path: path.clone(),
            id: file_id(&open_any(&path, 0).unwrap()).unwrap(),
        };
        fn to_junction(p: &Path) {
            fs::remove_dir_all(p.join("deeper")).unwrap();
            let outside = p.parent().unwrap().parent().unwrap().join("outside");
            let made = std::process::Command::new("cmd")
                .args(["/c", "mklink", "/J"])
                .arg(p.join("deeper"))
                .arg(outside)
                .output()
                .unwrap();
            assert!(made.status.success(), "{made:?}");
        }
        *BEFORE_EMPTYING.lock().unwrap() = Some(("inner".into(), path.join("inner"), to_junction));
        let r = remove_recorded(&m);
        *BEFORE_EMPTYING.lock().unwrap() = None;
        assert_eq!(r, Outcome::Removed);
        assert!(outside.join("keep").exists(), "the junction was followed");
        fs::remove_dir_all(&tmp).unwrap();
    }

    // A folder at a recorded file's name is not that file: settled at once as not there, not
    // waited out as a file in use, and left whole.
    #[test]
    fn a_folder_at_a_recorded_name_is_not_there() {
        let tmp = super::super::tests::temp("exe-folder");
        let path = tmp.join(".pluxy-old-0123456789abcdef-pluxy.exe");
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
        let path = tmp.join(".pluxy-old-0123456789abcdef-pluxy.exe");
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
        let aside = tmp.join(".pluxy-old-0123456789abcdef-pluxy.exe");
        fs::write(&aside, b"recorded").unwrap();
        let m = Made {
            path: aside.clone(),
            id: file_id(&fs::File::open(&aside).unwrap()).unwrap(),
        };
        fs::rename(&aside, tmp.join("moved.exe")).unwrap();
        fs::write(&aside, b"theirs").unwrap();
        let held = fs::OpenOptions::new()
            .read(true)
            .share_mode(FILE_SHARE_READ)
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

    // An aside copy still held open for a moment after this version took the profile (the earlier
    // version's image not yet unmapped) is waited out and removed, not left for the next start.
    #[test]
    fn cleanup_waits_out_a_leftover_held_open_for_a_moment() {
        use std::os::windows::fs::OpenOptionsExt;
        let tmp = super::super::tests::temp("exe-cleanup-busy");
        let dir = tmp.join("updates");
        let folder = fixture(&tmp, "new");
        let aside = folder.join(format!(".pluxy-old-0123456789abcdef-{EXE}"));
        fs::write(&aside, program_bytes("old")).unwrap();
        record(&dir, &aside, &fs::File::open(&aside).unwrap()).unwrap();
        leftovers::forget_mine(&tmp);
        let held = fs::OpenOptions::new()
            .read(true)
            .share_mode(FILE_SHARE_READ)
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
        assert_eq!(names(&folder), set_names());
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
