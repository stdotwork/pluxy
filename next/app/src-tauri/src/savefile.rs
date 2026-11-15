//! The host's save-file service: a file is saved only where the person chooses, in a native save
//! dialog, never at a path a page or a tool names. Linux only, as the program host.
//!
//! The dialog is GTK's `FileChooserNative` in save mode, as the Access chooser (the desktop portal's
//! with `GTK_USE_PORTAL=1` or inside a sandbox, else GTK's own), titled with the package that asks
//! and suggesting the caller's file name (`file_name`: a name, never a path, with control and format
//! characters replaced). One shows at a time. GTK's own overwrite question guards an existing file:
//! the dialog answers with an existing file only once the person chose to replace it. Cancelling, or
//! the call it serves ending, writes nothing.
//!
//! What the dialog answered is looked at as it answers (`Chosen`): its folder, and whether the name
//! is a regular file (and which) or nothing. A link, a folder or anything else at the name is
//! refused: the dialog showed a name, and writing through a link would write a file it did not show.
//! `write` then saves atomically beside it: a new file `.pluxy-save-<random>` in that folder, opened
//! relative to the folder it opened (no link followed), mode 0600 (a replaced file keeps its
//! permission bits), the bytes, fsync; then, if the folder and the name are still what the dialog
//! answered, a rename over the name (for a new file, one that refuses to replace anything), and an
//! fsync of the folder. Anything else (the name became a link, another file or a folder, a file
//! appeared where there was none) is refused and the temporary file removed. The check and the
//! rename are two steps: a change between them is not caught, as with any writer that does not lock.
//!
//! Nothing here logs: not the bytes, not the chosen path.

use std::ffi::{CString, OsStr, OsString};
use std::io;
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::MetadataExt;
use std::path::{Path, PathBuf};

/// The most bytes one save holds.
pub const MAX_BYTES: usize = 64 << 20;
/// The longest suggested name, in bytes: a file name's limit on Linux file systems.
const MAX_NAME: usize = 255;

/// The name a dialog suggests for caller's name: refused when it names a path (a `/` or `\`, or a
/// NUL); otherwise control and format characters (such as U+202E) become `_`, leading dots and
/// spaces are dropped (no hidden file, no `..`), and it is cut to 255 bytes. An empty name is
/// `file`.
pub fn file_name(name: &str) -> Result<String, String> {
    if name.contains(['/', '\\', '\0']) {
        return Err("name is a file name, not a path: it may not hold '/' or '\\'".into());
    }
    let replaced: String = name
        .chars()
        .map(|c| if forbidden(c) { '_' } else { c })
        .collect();
    let mut out = replaced
        .trim_start_matches(['.', ' '])
        .trim_end()
        .to_string();
    while out.len() > MAX_NAME {
        out.pop();
    }
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

/// The file a save dialog answered with, as it was when it answered.
#[derive(Debug)]
pub struct Chosen {
    dir: PathBuf,
    name: OsString,
    /// The folder's device and inode.
    dir_id: (u64, u64),
    /// The regular file at the name (device, inode and permission bits), or None for no file.
    existing: Option<(u64, u64, u32)>,
}

/// Looks at path, which a save dialog just answered with: refused unless it is a name in a folder,
/// with a regular file or nothing at it.
pub fn chosen(path: &Path) -> Result<Chosen, String> {
    let (Some(dir), Some(name)) = (path.parent(), path.file_name()) else {
        return Err("the dialog answered with no file name".into());
    };
    if !path.is_absolute() {
        return Err("the dialog answered with a relative path".into());
    }
    let d = std::fs::metadata(dir).map_err(|e| format!("the chosen folder: {e}"))?;
    if !d.is_dir() {
        return Err("the chosen folder is not a folder".into());
    }
    let existing = match std::fs::symlink_metadata(path) {
        Ok(m) if m.file_type().is_symlink() => {
            return Err(
                "the chosen file is a link: choose the file it points to, or another name".into(),
            )
        }
        Ok(m) if m.is_file() => Some((m.dev(), m.ino(), m.mode() & 0o777)),
        Ok(_) => return Err("the chosen name is not a file".into()),
        Err(e) if e.kind() == io::ErrorKind::NotFound => None,
        Err(e) => return Err(format!("the chosen file: {e}")),
    };
    Ok(Chosen {
        dir: dir.to_path_buf(),
        name: name.to_os_string(),
        dir_id: (d.dev(), d.ino()),
        existing,
    })
}

/// Why `write` saved nothing.
#[derive(Debug, PartialEq, Eq)]
pub enum Refusal {
    /// The folder or the name is no longer what the dialog answered.
    Changed(String),
    /// Writing failed.
    Failed(String),
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

fn rename_at(dir: &OwnedFd, from: &CString, to: &CString, flags: libc::c_uint) -> libc::c_int {
    unsafe {
        libc::renameat2(
            dir.as_raw_fd(),
            from.as_ptr(),
            dir.as_raw_fd(),
            to.as_ptr(),
            flags,
        )
    }
}

/// Saves bytes as chosen says, atomically (see the module's notes).
pub fn write(c: &Chosen, bytes: &[u8]) -> Result<(), Refusal> {
    let dir_path = CString::new(c.dir.as_os_str().as_bytes())
        .map_err(|_| Refusal::Failed("the folder's path holds a NUL".into()))?;
    let fd = unsafe {
        libc::open(
            dir_path.as_ptr(),
            libc::O_RDONLY | libc::O_DIRECTORY | libc::O_CLOEXEC,
        )
    };
    if fd < 0 {
        return Err(last("opening the chosen folder"));
    }
    let dir = unsafe { OwnedFd::from_raw_fd(fd) };
    let mut st: libc::stat = unsafe { std::mem::zeroed() };
    if unsafe { libc::fstat(dir.as_raw_fd(), &mut st) } != 0 {
        return Err(last("the chosen folder"));
    }
    if identity(&st) != c.dir_id {
        return Err(Refusal::Changed(
            "the chosen folder was replaced after you chose it: nothing was saved".into(),
        ));
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
    let mode = c.existing.map_or(0o600, |(_, _, m)| m);
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
        // The umask may have narrowed it; a replaced file's bits are set exactly.
        if unsafe { libc::fchmod(file.as_raw_fd(), mode as libc::mode_t) } != 0 {
            return Err(last("setting the file's mode"));
        }
        (&file)
            .write_all(bytes)
            .map_err(|e| Refusal::Failed(format!("writing the file: {e}")))?;
        file.sync_all()
            .map_err(|e| Refusal::Failed(format!("writing the file: {e}")))?;
        hold();
        // The name as the dialog answered: nothing, or the same regular file.
        let changed = || {
            Refusal::Changed(
                "the chosen file changed after you chose it (it is now a link, another file or gone): nothing was saved".into(),
            )
        };
        match (stat_at(&dir, &name), c.existing) {
            (Err(e), None) if e.kind() == io::ErrorKind::NotFound => {}
            (Ok(s), Some((dev, ino, _)))
                if s.st_mode & libc::S_IFMT == libc::S_IFREG && identity(&s) == (dev, ino) => {}
            (Err(e), _) if e.kind() != io::ErrorKind::NotFound => {
                return Err(Refusal::Failed(format!("the chosen file: {e}")))
            }
            _ => return Err(changed()),
        }
        let renamed = match c.existing {
            // A new file replaces nothing, even what appeared since the check.
            None => {
                let r = rename_at(&dir, &tmp, &name, libc::RENAME_NOREPLACE);
                if r != 0 && io::Error::last_os_error().raw_os_error() == Some(libc::EINVAL) {
                    // A file system without RENAME_NOREPLACE: the check above stands.
                    rename_at(&dir, &tmp, &name, 0)
                } else {
                    r
                }
            }
            Some(_) => rename_at(&dir, &tmp, &name, 0),
        };
        if renamed != 0 {
            let e = io::Error::last_os_error();
            if e.raw_os_error() == Some(libc::EEXIST) {
                return Err(changed());
            }
            return Err(Refusal::Failed(format!("saving the file: {e}")));
        }
        Ok(())
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
#[cfg(feature = "native-test")]
fn hold() {
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

#[cfg(not(feature = "native-test"))]
fn hold() {}

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
    use std::os::unix::fs::PermissionsExt;

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
}
