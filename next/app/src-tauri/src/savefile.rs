//! The host's save-file service: a file is saved only where the person chooses, in a native save
//! dialog, never at a path a page or a tool names. Linux only: the dialog is GTK's.
//!
//! The dialog is GTK's `FileChooserNative` in save mode, as the Access chooser (the desktop portal's
//! with `GTK_USE_PORTAL=1` or inside a sandbox, else GTK's own), titled with the package that asks
//! and suggesting the caller's file name (`file_name`: a name, never a path, with control and format
//! characters replaced). One shows at a time. GTK's own overwrite question guards an existing file:
//! the dialog answers with an existing file only once the person chose to replace it. Cancelling, or
//! the call it serves ending, writes nothing.
//!
//! What the dialog answered is looked at as it answers (`chosen`): its folder is opened once, and
//! that opened folder is the one written to. The folder must be one only this user (or root)
//! controls: owned by this user or root, and not writable by group or others unless sticky (as
//! `/tmp`). Otherwise another user could put a file at the chosen name after GTK's question, and the
//! host cannot tell which question GTK asked. The name must hold nothing or a regular file of this
//! user's (which one: device, inode, size and times). A link, a folder or anything else at the name
//! is refused: the dialog showed a name, and writing through a link would write a file it did not
//! show.
//!
//! `write` then saves atomically beside it: a new file `.pluxy-save-<random>` in that folder, opened
//! relative to it (no link followed), mode 0600, the bytes, fsync; then, if the folder is still the
//! one at the chosen path and the name is still what the dialog answered, the replaced file's
//! permission bits, and a rename over the name, and an fsync of the folder. A new file replaces
//! nothing: `RENAME_NOREPLACE`, or where a file system lacks it a hard link that fails on an
//! existing name; with neither, it is refused. Anything else (the name became a link, another or a
//! changed file or a folder, a file appeared where there was none) is refused and the temporary file
//! removed. For an overwrite, the check and the rename are two steps: a change between them is not
//! caught, as with any writer that does not lock.
//!
//! Nothing here logs: not the bytes, not the chosen path.

use std::ffi::{CString, OsStr, OsString};
use std::io;
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
use std::os::unix::ffi::OsStrExt;
use std::path::{Path, PathBuf};

/// The most bytes one save holds.
pub const MAX_BYTES: usize = 64 << 20;
/// The longest suggested name, in bytes: a file name's limit on Linux file systems.
const MAX_NAME: usize = 255;

/// The name a dialog suggests for caller's name: refused when it names a path (a `/` or `\`, or a
/// NUL); otherwise control and format characters (such as U+202E) become `_`, leading dots and
/// white space and trailing white space are dropped (no hidden file, no `..`), and it is cut to 255
/// bytes (and its end trimmed again). An empty name is `file`.
pub fn file_name(name: &str) -> Result<String, String> {
    if name.contains(['/', '\\', '\0']) {
        return Err("name is a file name, not a path: it may not hold '/' or '\\'".into());
    }
    let replaced: String = name
        .chars()
        .map(|c| if forbidden(c) { '_' } else { c })
        .collect();
    let mut out = replaced
        .trim_start_matches(|c: char| c == '.' || c.is_whitespace())
        .trim_end()
        .to_string();
    while out.len() > MAX_NAME {
        out.pop();
    }
    out.truncate(out.trim_end().len());
    if out.is_empty() {
        out = "file".into();
    }
    Ok(out)
}

fn forbidden(c: char) -> bool {
    use gtk::glib::Unichar;
    c.is_control()
        || matches!(c, '\u{2028}' | '\u{2029}')
        || c.unicode_type() == gtk::glib::UnicodeType::Format
}

/// Why a folder (its mode and owner) may not take a save by user euid, if it may not: only a folder
/// no other user can change, owned by euid or root, not writable by group or others unless sticky.
fn folder_refusal(mode: u32, uid: u32, euid: u32) -> Option<&'static str> {
    if uid != euid && uid != 0 {
        return Some("the chosen folder belongs to another user: choose a folder of your own");
    }
    if mode & 0o022 != 0 && mode & libc::S_ISVTX == 0 {
        return Some(
            "other users can change the chosen folder: choose a folder only you can change",
        );
    }
    None
}

/// Why a file of user uid at the chosen name may not be replaced by user euid, if it may not.
fn file_refusal(uid: u32, euid: u32) -> Option<&'static str> {
    (uid != euid).then_some("the chosen file belongs to another user: choose another name")
}

/// The file a save dialog answered with, as it was when it answered: its folder, opened.
#[derive(Debug)]
pub struct Chosen {
    /// The chosen path's folder, as it was opened, and its path, to see it is still there.
    dir: OwnedFd,
    dir_path: PathBuf,
    name: OsString,
    /// The regular file at the name (`Stamp`, and its permission bits), or None for no file.
    existing: Option<(Stamp, u32)>,
}

/// A file's device, inode, size, modification and change times (seconds and nanoseconds): which
/// file it is, and that it is unchanged. The inode alone is not enough: a file removed and made
/// again may get the same inode number.
type Stamp = (u64, u64, u64, i64, i64, i64, i64);

fn euid() -> u32 {
    unsafe { libc::geteuid() }
}

fn open_dir(path: &Path) -> io::Result<OwnedFd> {
    let p = CString::new(path.as_os_str().as_bytes())
        .map_err(|_| io::Error::from(io::ErrorKind::InvalidInput))?;
    let fd = unsafe {
        libc::open(
            p.as_ptr(),
            libc::O_RDONLY | libc::O_DIRECTORY | libc::O_CLOEXEC,
        )
    };
    if fd < 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(unsafe { OwnedFd::from_raw_fd(fd) })
}

fn fstat(fd: &OwnedFd) -> io::Result<libc::stat> {
    let mut st: libc::stat = unsafe { std::mem::zeroed() };
    if unsafe { libc::fstat(fd.as_raw_fd(), &mut st) } != 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(st)
}

/// Looks at path, which a save dialog just answered with: refused unless it is a name in a folder
/// only this user controls, with a regular file of this user's or nothing at it.
pub fn chosen(path: &Path) -> Result<Chosen, String> {
    let (Some(dir_path), Some(name)) = (path.parent(), path.file_name()) else {
        return Err("the dialog answered with no file name".into());
    };
    if !path.is_absolute() {
        return Err("the dialog answered with a relative path".into());
    }
    let dir = open_dir(dir_path).map_err(|e| format!("the chosen folder: {e}"))?;
    let d = fstat(&dir).map_err(|e| format!("the chosen folder: {e}"))?;
    if let Some(why) = folder_refusal(d.st_mode, d.st_uid, euid()) {
        return Err(why.into());
    }
    let c_name = CString::new(name.as_bytes()).map_err(|_| "a name holds a NUL".to_string())?;
    let existing = match stat_at(&dir, &c_name) {
        Ok(s) if s.st_mode & libc::S_IFMT == libc::S_IFLNK => {
            return Err(
                "the chosen file is a link: choose the file it points to, or another name".into(),
            )
        }
        Ok(s) if s.st_mode & libc::S_IFMT == libc::S_IFREG => {
            if let Some(why) = file_refusal(s.st_uid, euid()) {
                return Err(why.into());
            }
            Some((stamp(&s), s.st_mode & 0o777))
        }
        Ok(_) => return Err("the chosen name is not a file".into()),
        Err(e) if e.kind() == io::ErrorKind::NotFound => None,
        Err(e) => return Err(format!("the chosen file: {e}")),
    };
    Ok(Chosen {
        dir,
        dir_path: dir_path.to_path_buf(),
        name: name.to_os_string(),
        existing,
    })
}

/// Why `write` saved nothing.
#[derive(Debug, PartialEq, Eq)]
pub enum Refusal {
    /// The folder or the name is no longer what the dialog answered, or the caller may no longer
    /// save.
    Changed(String),
    /// Writing failed.
    Failed(String),
}

/// What a save came to.
#[derive(Debug, PartialEq, Eq)]
pub enum Outcome {
    Saved,
    Cancelled,
}

/// After the dialog: nothing when the person cancelled (chosen None) or cancelled() says the call
/// ended; refused when current() says the caller may no longer save (its release was disabled,
/// removed or replaced meanwhile); otherwise bytes are written as chosen says.
pub fn finish(
    chosen: Option<Chosen>,
    bytes: &[u8],
    cancelled: &dyn Fn() -> bool,
    current: &dyn Fn() -> Result<(), String>,
) -> Result<Outcome, Refusal> {
    let Some(chosen) = chosen else {
        return Ok(Outcome::Cancelled);
    };
    if cancelled() {
        return Ok(Outcome::Cancelled);
    }
    current().map_err(Refusal::Changed)?;
    write(&chosen, bytes).map(|_| Outcome::Saved)
}

fn cstr(s: &OsStr) -> Result<CString, Refusal> {
    CString::new(s.as_bytes()).map_err(|_| Refusal::Failed("a name holds a NUL".into()))
}

fn last(what: &str) -> Refusal {
    Refusal::Failed(format!("{what}: {}", io::Error::last_os_error()))
}

fn stat_at(dir: &OwnedFd, name: &CString) -> io::Result<libc::stat> {
    let mut st: libc::stat = unsafe { std::mem::zeroed() };
    let r = unsafe {
        libc::fstatat(
            dir.as_raw_fd(),
            name.as_ptr(),
            &mut st,
            libc::AT_SYMLINK_NOFOLLOW,
        )
    };
    if r == 0 {
        Ok(st)
    } else {
        Err(io::Error::last_os_error())
    }
}

/// A stat's device and inode (their types differ between targets).
#[allow(clippy::unnecessary_cast)]
fn identity(st: &libc::stat) -> (u64, u64) {
    (st.st_dev as u64, st.st_ino as u64)
}

/// A stat's `Stamp`.
#[allow(clippy::unnecessary_cast)]
fn stamp(st: &libc::stat) -> Stamp {
    let (dev, ino) = identity(st);
    (
        dev,
        ino,
        st.st_size as u64,
        st.st_mtime as i64,
        st.st_mtime_nsec as i64,
        st.st_ctime as i64,
        st.st_ctime_nsec as i64,
    )
}

fn result(r: libc::c_int) -> io::Result<()> {
    if r == 0 {
        Ok(())
    } else {
        Err(io::Error::last_os_error())
    }
}

/// renameat2 from to to in dir with flags.
fn rename_at(dir: &OwnedFd, from: &CString, to: &CString, flags: libc::c_uint) -> io::Result<()> {
    result(unsafe {
        libc::renameat2(
            dir.as_raw_fd(),
            from.as_ptr(),
            dir.as_raw_fd(),
            to.as_ptr(),
            flags,
        )
    })
}

fn rename_noreplace(dir: &OwnedFd, from: &CString, to: &CString) -> io::Result<()> {
    rename_at(dir, from, to, libc::RENAME_NOREPLACE)
}

/// A hard link to from at to in dir: fails on an existing to.
fn link_at(dir: &OwnedFd, from: &CString, to: &CString) -> io::Result<()> {
    result(unsafe {
        libc::linkat(
            dir.as_raw_fd(),
            from.as_ptr(),
            dir.as_raw_fd(),
            to.as_ptr(),
            0,
        )
    })
}

type Place = dyn Fn(&OwnedFd, &CString, &CString) -> io::Result<()>;

/// Puts the new file tmp at name in dir without ever replacing what is there: noreplace (a rename
/// that refuses an existing name), or where the file system lacks it (EINVAL), link (a hard link,
/// which fails on an existing name) and then removing tmp. With neither, refused.
fn place_new(
    dir: &OwnedFd,
    tmp: &CString,
    name: &CString,
    noreplace: &Place,
    link: &Place,
) -> Result<(), Refusal> {
    let appeared = || {
        Refusal::Changed(
            "a file appeared at the chosen name after you chose it: nothing was saved".into(),
        )
    };
    match noreplace(dir, tmp, name) {
        Ok(()) => return Ok(()),
        Err(e) if e.raw_os_error() == Some(libc::EEXIST) => return Err(appeared()),
        Err(e) if e.raw_os_error() != Some(libc::EINVAL) => {
            return Err(Refusal::Failed(format!("saving the file: {e}")))
        }
        Err(_) => {}
    }
    match link(dir, tmp, name) {
        Ok(()) => {
            unsafe { libc::unlinkat(dir.as_raw_fd(), tmp.as_ptr(), 0) };
            Ok(())
        }
        Err(e) if e.raw_os_error() == Some(libc::EEXIST) => Err(appeared()),
        Err(e) => Err(Refusal::Failed(format!(
            "this folder's file system can save a new file only by risking replacing another ({e}): choose another folder"
        ))),
    }
}

/// Saves bytes as chosen says, atomically (see the module's notes).
pub fn write(c: &Chosen, bytes: &[u8]) -> Result<(), Refusal> {
    write_with(c, bytes, &rename_noreplace, &link_at)
}

fn write_with(c: &Chosen, bytes: &[u8], noreplace: &Place, link: &Place) -> Result<(), Refusal> {
    let dir = &c.dir;
    // The folder opened is still the one at the chosen path, and still only this user's.
    let opened = fstat(dir).map_err(|e| Refusal::Failed(format!("the chosen folder: {e}")))?;
    let at_path = open_dir(&c.dir_path)
        .and_then(|d| fstat(&d))
        .map(|s| identity(&s));
    if at_path.ok() != Some(identity(&opened)) {
        return Err(Refusal::Changed(
            "the chosen folder was moved or replaced after you chose it: nothing was saved".into(),
        ));
    }
    if let Some(why) = folder_refusal(opened.st_mode, opened.st_uid, euid()) {
        return Err(Refusal::Changed(why.into()));
    }
    let mut random = [0u8; 8];
    getrandom::fill(&mut random).map_err(|e| Refusal::Failed(e.to_string()))?;
    let tmp_name = format!(
        ".pluxy-save-{}",
        random
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>()
    );
    let tmp = cstr(OsStr::new(&tmp_name))?;
    let name = cstr(&c.name)?;
    let f = unsafe {
        libc::openat(
            dir.as_raw_fd(),
            tmp.as_ptr(),
            libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL | libc::O_NOFOLLOW | libc::O_CLOEXEC,
            0o600 as libc::c_uint,
        )
    };
    if f < 0 {
        return Err(last("creating the file"));
    }
    let file = std::fs::File::from(unsafe { OwnedFd::from_raw_fd(f) });
    let written = (|| {
        use std::io::Write;
        // The umask may have narrowed 0600; it is set exactly.
        if unsafe { libc::fchmod(file.as_raw_fd(), 0o600) } != 0 {
            return Err(last("setting the file's mode"));
        }
        (&file)
            .write_all(bytes)
            .map_err(|e| Refusal::Failed(format!("writing the file: {e}")))?;
        file.sync_all()
            .map_err(|e| Refusal::Failed(format!("writing the file: {e}")))?;
        hold(dir, &tmp);
        // The name as the dialog answered: nothing, or the same regular file.
        let changed = || {
            Refusal::Changed(
                "the chosen file changed after you chose it (it is now a link, another file or gone): nothing was saved".into(),
            )
        };
        match (stat_at(dir, &name), c.existing) {
            (Err(e), None) if e.kind() == io::ErrorKind::NotFound => {}
            (Ok(s), Some((was, _)))
                if s.st_mode & libc::S_IFMT == libc::S_IFREG && stamp(&s) == was => {}
            (Err(e), _) if e.kind() != io::ErrorKind::NotFound => {
                return Err(Refusal::Failed(format!("the chosen file: {e}")))
            }
            _ => return Err(changed()),
        }
        match c.existing {
            // A new file replaces nothing, even what appeared since the check.
            None => place_new(dir, &tmp, &name, noreplace, link),
            Some((_, mode)) => {
                // Only now, once the name checked out, the replaced file's permission bits.
                if unsafe { libc::fchmod(file.as_raw_fd(), mode as libc::mode_t) } != 0 {
                    return Err(last("setting the file's mode"));
                }
                rename_at(dir, &tmp, &name, 0)
                    .map_err(|e| Refusal::Failed(format!("saving the file: {e}")))
            }
        }
    })();
    if written.is_err() {
        unsafe { libc::unlinkat(dir.as_raw_fd(), tmp.as_ptr(), 0) };
        return written;
    }
    // Saved; the folder's entry is made durable as far as the file system allows.
    unsafe { libc::fsync(dir.as_raw_fd()) };
    Ok(())
}

/// For the native test: holds the save before its last check while `<PLUXY_APP_TEST_SAVE>/hold`
/// exists, saying so with `hold.held` beside it.
#[cfg(all(feature = "native-test", not(test)))]
fn hold(_dir: &OwnedFd, _tmp: &CString) {
    let Ok(dir) = std::env::var("PLUXY_APP_TEST_SAVE") else {
        return;
    };
    let hold = Path::new(&dir).join("hold");
    if !hold.exists() {
        return;
    }
    let _ = std::fs::write(hold.with_extension("held"), "");
    let until = std::time::Instant::now() + std::time::Duration::from_secs(60);
    while hold.exists() && std::time::Instant::now() < until {
        std::thread::sleep(std::time::Duration::from_millis(50));
    }
}

#[cfg(all(not(feature = "native-test"), not(test)))]
fn hold(_dir: &OwnedFd, _tmp: &CString) {}

/// The unit tests' look at the temporary file (its folder and name) before the last check.
#[cfg(test)]
type Hook = Box<dyn FnMut(&OwnedFd, &CString)>;

#[cfg(test)]
thread_local! {
    static HOOK: std::cell::RefCell<Option<Hook>> =
        const { std::cell::RefCell::new(None) };
}

#[cfg(test)]
fn hold(dir: &OwnedFd, tmp: &CString) {
    HOOK.with(|h| {
        if let Some(f) = h.borrow_mut().as_mut() {
            f(dir, tmp)
        }
    });
}

/// Whether a save dialog is showing: one at a time.
static SHOWING: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);

thread_local! {
    /// The save dialog showing, on GTK's thread: kept until it answers, or closed from there.
    static DIALOG: std::cell::RefCell<Option<gtk::FileChooserNative>> =
        const { std::cell::RefCell::new(None) };
}

/// Shows the native save dialog titled title, suggesting name (already `file_name`'s), and waits
/// for the person's answer, as `Chosen`; None when they cancelled, or once cancelled() says so (the
/// dialog then closes). Refused while another save dialog shows.
pub fn ask(
    app: &tauri::AppHandle,
    title: String,
    name: String,
    cancelled: &dyn Fn() -> bool,
) -> Result<Option<Chosen>, String> {
    use std::sync::atomic::Ordering;
    use std::sync::mpsc::RecvTimeoutError;
    if SHOWING.swap(true, Ordering::SeqCst) {
        return Err("another save dialog is showing: answer it first".into());
    }
    struct Showing;
    impl Drop for Showing {
        fn drop(&mut self) {
            SHOWING.store(false, Ordering::SeqCst);
        }
    }
    let _showing = Showing;
    let (tx, rx) = std::sync::mpsc::channel::<Option<Result<Chosen, String>>>();
    app.run_on_main_thread(move || {
        use gtk::prelude::*;
        let d = gtk::FileChooserNative::new(
            Some(&title),
            None::<&gtk::Window>,
            gtk::FileChooserAction::Save,
            Some("_Save"),
            Some("_Cancel"),
        );
        d.set_modal(true);
        d.set_local_only(true);
        d.set_do_overwrite_confirmation(true);
        d.set_current_name(&name);
        d.connect_response(move |d, r| {
            if DIALOG.with(|k| k.borrow_mut().take()).is_none() {
                return;
            }
            // Looked at now, as the dialog answers (after its overwrite question).
            let answer = (r == gtk::ResponseType::Accept)
                .then(|| d.filename())
                .flatten()
                .map(|p| chosen(&p));
            let _ = tx.send(answer);
        });
        DIALOG.with(|k| *k.borrow_mut() = Some(d.clone()));
        d.show();
    })
    .map_err(|e| e.to_string())?;
    loop {
        match rx.recv_timeout(std::time::Duration::from_millis(100)) {
            Ok(None) => return Ok(None),
            Ok(Some(c)) => return c.map(Some),
            Err(RecvTimeoutError::Disconnected) => return Err("the save dialog closed".into()),
            Err(RecvTimeoutError::Timeout) if cancelled() => {
                let _ = app.run_on_main_thread(|| {
                    use gtk::prelude::*;
                    if let Some(d) = DIALOG.with(|k| k.borrow_mut().take()) {
                        d.hide();
                    }
                });
                return Ok(None);
            }
            Err(RecvTimeoutError::Timeout) => {}
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::{MetadataExt, PermissionsExt};

    struct Tmp(PathBuf);
    impl Drop for Tmp {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }
    fn tmp() -> Tmp {
        let mut r = [0u8; 8];
        getrandom::fill(&mut r).unwrap();
        let p = std::env::temp_dir().join(format!(
            "pluxy-savefile-{}",
            r.iter().map(|b| format!("{b:02x}")).collect::<String>()
        ));
        std::fs::create_dir(&p).unwrap();
        Tmp(p)
    }
    fn names(dir: &Path) -> Vec<String> {
        let mut v: Vec<String> = std::fs::read_dir(dir)
            .unwrap()
            .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
            .collect();
        v.sort();
        v
    }
    fn mode(p: &Path) -> u32 {
        std::fs::symlink_metadata(p).unwrap().permissions().mode() & 0o7777
    }

    #[test]
    fn a_name_is_never_a_path_and_loses_control_and_format_characters() {
        assert!(file_name("a/b").is_err());
        assert!(file_name("a\\b").is_err());
        assert!(file_name("a\0b").is_err());
        assert!(file_name("../x").is_err());
        assert_eq!(
            file_name(".ses\u{202e}sion\u{7}.jsonl").unwrap(),
            "ses_sion_.jsonl"
        );
        assert_eq!(file_name("a\u{200b}b\u{2028}c\nd").unwrap(), "a_b_c_d");
        assert_eq!(file_name("..").unwrap(), "file");
        assert_eq!(file_name("").unwrap(), "file");
        assert_eq!(file_name("  ..hidden ").unwrap(), "hidden");
        assert_eq!(file_name("usage-é.csv").unwrap(), "usage-é.csv");
        // Unicode white space at both ends, and the end trimmed again after the cut.
        assert_eq!(
            file_name("\u{3000}\u{a0}name.csv\u{3000}").unwrap(),
            "name.csv"
        );
        let cut = file_name(&format!("{} b", "a".repeat(254))).unwrap();
        assert_eq!(cut, "a".repeat(254));
        let cut = file_name(&"é".repeat(200)).unwrap();
        assert!(cut.len() <= 255 && cut.len() >= 254 && cut.chars().all(|c| c == 'é'));
    }

    #[test]
    fn a_new_file_holds_exactly_the_bytes_with_mode_0600_and_nothing_else_is_left() {
        let t = tmp();
        let path = t.0.join("new.bin");
        let c = chosen(&path).unwrap();
        let bytes: Vec<u8> = (0..=255u8).cycle().take(300_000).collect();
        write(&c, &bytes).unwrap();
        assert_eq!(std::fs::read(&path).unwrap(), bytes);
        assert_eq!(mode(&path), 0o600);
        assert_eq!(names(&t.0), ["new.bin"]);
    }

    #[test]
    fn an_existing_file_is_replaced_by_a_new_one_with_its_permissions() {
        let t = tmp();
        let path = t.0.join("old.txt");
        std::fs::write(&path, b"old").unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o640)).unwrap();
        let inode = std::fs::metadata(&path).unwrap().ino();
        let c = chosen(&path).unwrap();
        write(&c, b"new").unwrap();
        assert_eq!(std::fs::read(&path).unwrap(), b"new");
        assert_eq!(mode(&path), 0o640);
        assert_ne!(std::fs::metadata(&path).unwrap().ino(), inode);
        assert_eq!(names(&t.0), ["old.txt"]);
    }

    #[test]
    fn a_link_or_a_folder_at_the_chosen_name_is_refused() {
        let t = tmp();
        let target = t.0.join("target");
        std::fs::write(&target, b"target").unwrap();
        let link = t.0.join("link");
        std::os::unix::fs::symlink(&target, &link).unwrap();
        assert!(chosen(&link).unwrap_err().contains("link"));
        std::fs::create_dir(t.0.join("folder")).unwrap();
        assert!(chosen(&t.0.join("folder")).is_err());
        assert!(chosen(Path::new("relative")).is_err());
        assert_eq!(std::fs::read(&target).unwrap(), b"target");
    }

    #[test]
    fn a_link_swapped_in_for_a_new_name_is_refused_and_nothing_is_left() {
        let t = tmp();
        let victim = t.0.join("victim");
        std::fs::write(&victim, b"victim").unwrap();
        let path = t.0.join("new");
        let c = chosen(&path).unwrap();
        std::os::unix::fs::symlink(&victim, &path).unwrap();
        assert!(matches!(write(&c, b"bytes"), Err(Refusal::Changed(_))));
        assert_eq!(std::fs::read_link(&path).unwrap(), victim);
        assert_eq!(std::fs::read(&victim).unwrap(), b"victim");
        assert_eq!(names(&t.0), ["new", "victim"]);
    }

    #[test]
    fn a_file_that_appeared_at_a_new_name_is_kept() {
        let t = tmp();
        let path = t.0.join("new");
        let c = chosen(&path).unwrap();
        std::fs::write(&path, b"someone else's").unwrap();
        assert!(matches!(write(&c, b"bytes"), Err(Refusal::Changed(_))));
        assert_eq!(std::fs::read(&path).unwrap(), b"someone else's");
        assert_eq!(names(&t.0), ["new"]);
    }

    #[test]
    fn a_replaced_or_relinked_existing_file_is_refused() {
        let t = tmp();
        let victim = t.0.join("victim");
        std::fs::write(&victim, b"victim").unwrap();
        let path = t.0.join("old");
        std::fs::write(&path, b"old").unwrap();
        let c = chosen(&path).unwrap();
        std::fs::remove_file(&path).unwrap();
        std::os::unix::fs::symlink(&victim, &path).unwrap();
        assert!(matches!(write(&c, b"bytes"), Err(Refusal::Changed(_))));
        assert_eq!(std::fs::read(&victim).unwrap(), b"victim");
        // Another file at the name.
        std::fs::remove_file(&path).unwrap();
        std::fs::write(&path, b"other").unwrap();
        assert!(matches!(write(&c, b"bytes"), Err(Refusal::Changed(_))));
        assert_eq!(std::fs::read(&path).unwrap(), b"other");
        // The same file, written to in place since.
        let c = chosen(&path).unwrap();
        std::fs::OpenOptions::new()
            .append(true)
            .open(&path)
            .and_then(|mut f| std::io::Write::write_all(&mut f, b" and more"))
            .unwrap();
        assert!(matches!(write(&c, b"bytes"), Err(Refusal::Changed(_))));
        assert_eq!(std::fs::read(&path).unwrap(), b"other and more");
        // Gone.
        std::fs::remove_file(&path).unwrap();
        assert!(matches!(write(&c, b"bytes"), Err(Refusal::Changed(_))));
        assert_eq!(names(&t.0), ["victim"]);
    }

    #[test]
    fn a_replaced_folder_is_refused() {
        let t = tmp();
        let dir = t.0.join("dir");
        std::fs::create_dir(&dir).unwrap();
        let c = chosen(&dir.join("new")).unwrap();
        std::fs::rename(&dir, t.0.join("moved")).unwrap();
        std::fs::create_dir(&dir).unwrap();
        assert!(matches!(write(&c, b"bytes"), Err(Refusal::Changed(_))));
        assert!(names(&dir).is_empty() && names(&t.0.join("moved")).is_empty());
    }

    #[test]
    fn only_a_folder_no_other_user_can_change_takes_a_save() {
        // Owned by this user or root; not writable by group or others unless sticky.
        assert_eq!(folder_refusal(0o40755, 1000, 1000), None);
        assert_eq!(folder_refusal(0o40700, 0, 1000), None);
        assert_eq!(folder_refusal(0o41777, 0, 1000), None);
        assert!(folder_refusal(0o40755, 1001, 1000).is_some());
        assert!(folder_refusal(0o40775, 1000, 1000).is_some());
        assert!(folder_refusal(0o40757, 1000, 1000).is_some());
        assert!(folder_refusal(0o40777, 0, 1000).is_some());
        assert!(folder_refusal(0o41775, 1001, 1000).is_some());
        // A file of another user's is never replaced.
        assert_eq!(file_refusal(1000, 1000), None);
        assert!(file_refusal(0, 1000).is_some());
        assert!(file_refusal(1001, 1000).is_some());
        // On disk: a group-writable folder is refused, a sticky one is not.
        let t = tmp();
        let open = t.0.join("open");
        std::fs::create_dir(&open).unwrap();
        std::fs::set_permissions(&open, std::fs::Permissions::from_mode(0o775)).unwrap();
        assert!(chosen(&open.join("new"))
            .unwrap_err()
            .contains("other users"));
        std::fs::set_permissions(&open, std::fs::Permissions::from_mode(0o1775)).unwrap();
        let c = chosen(&open.join("new")).unwrap();
        // And a folder opened to other users after the choice is refused at the write.
        std::fs::set_permissions(&open, std::fs::Permissions::from_mode(0o777)).unwrap();
        assert!(matches!(write(&c, b"bytes"), Err(Refusal::Changed(_))));
        assert!(names(&open).is_empty());
    }

    fn einval(_: &OwnedFd, _: &CString, _: &CString) -> io::Result<()> {
        Err(io::Error::from_raw_os_error(libc::EINVAL))
    }

    fn eperm(_: &OwnedFd, _: &CString, _: &CString) -> io::Result<()> {
        Err(io::Error::from_raw_os_error(libc::EPERM))
    }

    #[test]
    fn without_rename_noreplace_a_new_file_is_linked_in_and_never_replaces_one() {
        let t = tmp();
        let path = t.0.join("new");
        // Saved through the hard link, with nothing else left.
        let c = chosen(&path).unwrap();
        write_with(&c, b"bytes", &einval, &link_at).unwrap();
        assert_eq!(std::fs::read(&path).unwrap(), b"bytes");
        assert_eq!(mode(&path), 0o600);
        assert_eq!(names(&t.0), ["new"]);
        // No hard links either: refused, nothing left.
        let third = t.0.join("third");
        let c = chosen(&third).unwrap();
        assert!(matches!(
            write_with(&c, b"bytes", &einval, &eperm),
            Err(Refusal::Failed(_))
        ));
        assert_eq!(names(&t.0), ["new"]);
    }

    #[test]
    fn a_new_file_never_falls_back_to_a_rename_that_replaces() {
        let t = tmp();
        let dir = open_dir(&t.0).unwrap();
        std::fs::write(t.0.join("tmp"), b"new").unwrap();
        std::fs::write(t.0.join("name"), b"there").unwrap();
        let (tmpc, namec) = (CString::new("tmp").unwrap(), CString::new("name").unwrap());
        assert!(matches!(
            place_new(&dir, &tmpc, &namec, &einval, &link_at),
            Err(Refusal::Changed(_))
        ));
        assert_eq!(std::fs::read(t.0.join("name")).unwrap(), b"there");
    }

    #[test]
    fn a_replaced_files_permission_bits_come_only_after_the_last_check() {
        let t = tmp();
        let path = t.0.join("old");
        std::fs::write(&path, b"old").unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o644)).unwrap();
        let c = chosen(&path).unwrap();
        let seen = std::rc::Rc::new(std::cell::Cell::new(0));
        let s = std::rc::Rc::clone(&seen);
        HOOK.with(|h| {
            *h.borrow_mut() = Some(Box::new(move |d, tmp| {
                s.set(stat_at(d, tmp).unwrap().st_mode & 0o7777);
            }))
        });
        let r = write(&c, b"new");
        HOOK.with(|h| *h.borrow_mut() = None);
        r.unwrap();
        assert_eq!(seen.get(), 0o600);
        assert_eq!(mode(&path), 0o644);
    }

    #[test]
    fn nothing_is_written_once_the_caller_may_no_longer_save() {
        let t = tmp();
        let path = t.0.join("new");
        let ok = || Ok(());
        let gone = || Err("pluxy/exporter was disabled".to_string());
        let (no, yes) = (|| false, || true);
        assert_eq!(finish(None, b"x", &no, &ok), Ok(Outcome::Cancelled));
        assert_eq!(
            finish(Some(chosen(&path).unwrap()), b"x", &yes, &ok),
            Ok(Outcome::Cancelled)
        );
        assert!(matches!(
            finish(Some(chosen(&path).unwrap()), b"x", &no, &gone),
            Err(Refusal::Changed(m)) if m.contains("disabled")
        ));
        assert!(names(&t.0).is_empty());
        assert_eq!(
            finish(Some(chosen(&path).unwrap()), b"x", &no, &ok),
            Ok(Outcome::Saved)
        );
        assert_eq!(std::fs::read(&path).unwrap(), b"x");
    }
}
