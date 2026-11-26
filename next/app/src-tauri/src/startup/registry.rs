//! The login item on Windows: a string value in the user's own `Run` key
//! (`HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Run`), named with the app's
//! identifier, whose data is `"<path of the app>" --autostart`. Never `HKEY_LOCAL_MACHINE`: the
//! item is the user's, and removing it leaves nothing behind.
//!
//! A value of that name is pluxy's own when its data has exactly that shape. One of another shape
//! or type is someone else's, and is left alone.
//!
//! Task Manager's Startup tab turns an item off without removing it, by a binary value of the same
//! name under `...\Explorer\StartupApproved\Run` whose first byte is odd when off. An item turned
//! off there is off here too, and saving it on clears that mark. Removing the item removes the
//! mark.

use super::Registration;
use std::path::Path;
use std::ptr::{null, null_mut};
use windows_sys::Win32::Foundation::{ERROR_FILE_NOT_FOUND, ERROR_SUCCESS};
use windows_sys::Win32::System::Registry::{
    RegCloseKey, RegCreateKeyExW, RegDeleteValueW, RegOpenKeyExW, RegQueryValueExW, RegSetValueExW,
    HKEY, HKEY_CURRENT_USER, KEY_QUERY_VALUE, KEY_SET_VALUE, REG_BINARY, REG_OPTION_NON_VOLATILE,
    REG_SZ,
};

/// The two keys, below `HKEY_CURRENT_USER`.
pub struct Keys {
    pub run: String,
    pub approved: String,
}

impl Keys {
    /// The user's own: the ones Windows reads at login.
    pub fn user() -> Keys {
        Keys {
            run: r"Software\Microsoft\Windows\CurrentVersion\Run".into(),
            approved: r"Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run"
                .into(),
        }
    }
}

/// What was there before a change: the Run value's text, and the approval's bytes.
pub type Snapshot = (Option<String>, Option<Vec<u8>>);

fn wide(s: &str) -> Vec<u16> {
    s.encode_utf16().chain(std::iter::once(0)).collect()
}

fn check(what: &str, code: u32) -> Result<(), String> {
    if code == ERROR_SUCCESS {
        Ok(())
    } else {
        Err(format!(
            "{what}: {}",
            std::io::Error::from_raw_os_error(code as i32)
        ))
    }
}

/// An open registry key, closed when dropped.
struct Key(HKEY);
impl Drop for Key {
    fn drop(&mut self) {
        // SAFETY: the key is open, and owned by this value.
        unsafe { RegCloseKey(self.0) };
    }
}

/// Opens `subkey` for `access`; None if it does not exist.
fn open(subkey: &str, access: u32) -> Result<Option<Key>, String> {
    let path = wide(subkey);
    let mut key: HKEY = null_mut();
    // SAFETY: path is NUL-terminated; key is written when this succeeds.
    let code = unsafe { RegOpenKeyExW(HKEY_CURRENT_USER, path.as_ptr(), 0, access, &mut key) };
    if code == ERROR_FILE_NOT_FOUND {
        return Ok(None);
    }
    check("opening the registry key", code)?;
    Ok(Some(Key(key)))
}

/// A value's type and bytes; None if the key or the value does not exist.
fn read(subkey: &str, name: &str) -> Result<Option<(u32, Vec<u8>)>, String> {
    let Some(key) = open(subkey, KEY_QUERY_VALUE)? else {
        return Ok(None);
    };
    let name = wide(name);
    let (mut ty, mut len) = (0u32, 0u32);
    // SAFETY: asks for the size only.
    let code =
        unsafe { RegQueryValueExW(key.0, name.as_ptr(), null(), &mut ty, null_mut(), &mut len) };
    if code == ERROR_FILE_NOT_FOUND {
        return Ok(None);
    }
    check("reading the registry value", code)?;
    // A value can grow between the two calls: ask again with room, a few times.
    for _ in 0..4 {
        let mut buf = vec![0u8; len as usize + 2];
        let mut got = buf.len() as u32;
        // SAFETY: buf has got bytes.
        let code = unsafe {
            RegQueryValueExW(
                key.0,
                name.as_ptr(),
                null(),
                &mut ty,
                buf.as_mut_ptr(),
                &mut got,
            )
        };
        if code == ERROR_FILE_NOT_FOUND {
            return Ok(None);
        }
        if code == windows_sys::Win32::Foundation::ERROR_MORE_DATA {
            len = got;
            continue;
        }
        check("reading the registry value", code)?;
        buf.truncate(got as usize);
        return Ok(Some((ty, buf)));
    }
    Err("the registry value keeps changing".into())
}

/// Sets a value, making the key if it is missing.
fn write(subkey: &str, name: &str, ty: u32, data: &[u8]) -> Result<(), String> {
    let path = wide(subkey);
    let mut key: HKEY = null_mut();
    // SAFETY: path is NUL-terminated; key is written when this succeeds.
    let code = unsafe {
        RegCreateKeyExW(
            HKEY_CURRENT_USER,
            path.as_ptr(),
            0,
            null(),
            REG_OPTION_NON_VOLATILE,
            KEY_SET_VALUE,
            null(),
            &mut key,
            null_mut(),
        )
    };
    check("making the registry key", code)?;
    let key = Key(key);
    let name = wide(name);
    // SAFETY: data is as long as it says.
    let code = unsafe {
        RegSetValueExW(
            key.0,
            name.as_ptr(),
            0,
            ty,
            data.as_ptr(),
            data.len() as u32,
        )
    };
    check("setting the registry value", code)
}

/// Removes a value. A missing key or value is fine.
fn delete(subkey: &str, name: &str) -> Result<(), String> {
    let Some(key) = open(subkey, KEY_SET_VALUE)? else {
        return Ok(());
    };
    let name = wide(name);
    // SAFETY: name is NUL-terminated.
    let code = unsafe { RegDeleteValueW(key.0, name.as_ptr()) };
    if code == ERROR_FILE_NOT_FOUND {
        return Ok(());
    }
    check("removing the registry value", code)
}

/// The text of a `REG_SZ` value (its trailing NUL dropped).
fn text(ty: u32, bytes: &[u8]) -> Option<String> {
    let (pairs, rest) = bytes.as_chunks::<2>();
    if ty != REG_SZ || !rest.is_empty() {
        return None;
    }
    let units: Vec<u16> = pairs.iter().map(|c| u16::from_le_bytes(*c)).collect();
    let end = units.iter().position(|&u| u == 0).unwrap_or(units.len());
    String::from_utf16(&units[..end]).ok()
}

/// The text of a `REG_SZ` value below `HKEY_CURRENT_USER`; None if the key or the value does not
/// exist, or the value is not text.
pub(crate) fn text_value(subkey: &str, name: &str) -> Result<Option<String>, String> {
    Ok(read(subkey, name)?.and_then(|(ty, bytes)| text(ty, &bytes)))
}

/// Sets a `REG_SZ` value below `HKEY_CURRENT_USER`, making the key if it is missing.
pub(crate) fn set_text(subkey: &str, name: &str, value: &str) -> Result<(), String> {
    let units: Vec<u8> = wide(value).iter().flat_map(|u| u.to_le_bytes()).collect();
    write(subkey, name, REG_SZ, &units)
}

/// The command the item runs for the app at `path`.
pub fn command(path: &Path) -> Result<String, String> {
    let p = path.to_str().ok_or("the app path is not UTF-8")?;
    if p.contains('"') {
        return Err("autostart executable paths cannot contain a quote".into());
    }
    Ok(format!("\"{p}\" --autostart"))
}

/// Whether a Run value's text is the command pluxy makes (for any app path).
fn ours(value: &str) -> bool {
    let Some(rest) = value.strip_prefix('"') else {
        return false;
    };
    let Some((path, tail)) = rest.rsplit_once('"') else {
        return false;
    };
    tail == " --autostart" && !path.contains('"') && Path::new(path).is_absolute()
}

/// Whether Task Manager turned the item off.
fn turned_off(keys: &Keys, name: &str) -> Result<bool, String> {
    Ok(read(&keys.approved, name)?.is_some_and(|(_, b)| b.first().is_some_and(|b| b & 1 == 1)))
}

pub fn registration(keys: &Keys, name: &str) -> Result<Registration, String> {
    let Some((ty, bytes)) = read(&keys.run, name)? else {
        return Ok(Registration::default());
    };
    let owned = text(ty, &bytes).is_some_and(|t| ours(&t));
    Ok(Registration {
        exists: true,
        owned,
        enabled: owned && !turned_off(keys, name)?,
    })
}

pub fn snapshot(keys: &Keys, name: &str) -> Result<Snapshot, String> {
    let run = read(&keys.run, name)?.and_then(|(ty, b)| text(ty, &b));
    let approved = read(&keys.approved, name)?.map(|(_, b)| b);
    Ok((run, approved))
}

pub fn install(keys: &Keys, name: &str, executable: &Path) -> Result<(), String> {
    let value = command(executable)?;
    let data: Vec<u8> = wide(&value).iter().flat_map(|u| u.to_le_bytes()).collect();
    write(&keys.run, name, REG_SZ, &data)?;
    // A mark Task Manager left would keep the item off.
    delete(&keys.approved, name)
}

pub fn uninstall(keys: &Keys, name: &str) -> Result<(), String> {
    delete(&keys.run, name)?;
    delete(&keys.approved, name)
}

pub fn restore(keys: &Keys, name: &str, previous: Snapshot) -> Result<(), String> {
    let (run, approved) = previous;
    match run {
        Some(t) => {
            let data: Vec<u8> = wide(&t).iter().flat_map(|u| u.to_le_bytes()).collect();
            write(&keys.run, name, REG_SZ, &data)?;
        }
        None => delete(&keys.run, name)?,
    }
    match approved {
        Some(b) => write(&keys.approved, name, REG_BINARY, &b),
        None => delete(&keys.approved, name),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::startup::{Preferences, Store};
    use std::fs;
    use windows_sys::Win32::System::Registry::{RegDeleteKeyW, RegDeleteTreeW, REG_DWORD};

    /// These write the real per-user registry, in a key of their own that they remove: only on a
    /// hosted CI runner, whose user is throwaway.
    fn on_a_runner() -> bool {
        std::env::var("GITHUB_ACTIONS").as_deref() == Ok("true")
            && std::env::var("RUNNER_ENVIRONMENT").as_deref() == Ok("github-hosted")
    }

    const ROOT: &str = r"Software\pluxy-app-test";

    /// A scratch pair of keys, removed with their folder when dropped.
    struct Scratch {
        keys: Keys,
        tree: String,
        dir: std::path::PathBuf,
    }
    impl Scratch {
        fn new() -> Option<Scratch> {
            if !on_a_runner() {
                eprintln!("skipped: not a hosted runner, and these write the real registry");
                return None;
            }
            let id = crate::managed::random_key()[..12].to_string();
            let tree = format!(r"{ROOT}\{id}");
            let dir = std::env::temp_dir().join(format!("pluxy-startup-{id}"));
            fs::create_dir_all(&dir).unwrap();
            fs::write(dir.join("pluxy app.exe"), b"fixture").unwrap();
            Some(Scratch {
                keys: Keys {
                    run: format!(r"{tree}\Run"),
                    approved: format!(r"{tree}\Approved"),
                },
                tree,
                dir,
            })
        }
        fn store(&self) -> Store {
            let mut s = Store::new(
                self.dir.join("config"),
                self.dir.clone(),
                "dev.pluxy.desktop".into(),
            );
            s.keys = Keys {
                run: self.keys.run.clone(),
                approved: self.keys.approved.clone(),
            };
            s
        }
        fn exe(&self) -> std::path::PathBuf {
            self.dir.join("pluxy app.exe")
        }
    }
    impl Drop for Scratch {
        fn drop(&mut self) {
            let tree = wide(&self.tree);
            let root = wide(ROOT);
            // SAFETY: both are NUL-terminated.
            unsafe {
                RegDeleteTreeW(HKEY_CURRENT_USER, tree.as_ptr());
                RegDeleteKeyW(HKEY_CURRENT_USER, root.as_ptr());
            }
            let _ = fs::remove_dir_all(&self.dir);
        }
    }

    const NAME: &str = "dev.pluxy.desktop";

    #[test]
    fn the_command_is_the_quoted_path_and_the_argument() {
        let c = command(Path::new(r"C:\Program Files\pluxy\pluxy-app.exe")).unwrap();
        assert_eq!(c, r#""C:\Program Files\pluxy\pluxy-app.exe" --autostart"#);
        assert!(ours(&c));
        assert!(command(Path::new("C:\\a\"b.exe")).is_err());
        for foreign in [
            r"C:\x\app.exe --autostart",
            r#""C:\x\app.exe""#,
            r#""C:\x\app.exe" --other"#,
            r#""rel\app.exe" --autostart"#,
            r#""C:\x\app.exe" --autostart extra"#,
            "",
        ] {
            assert!(!ours(foreign), "{foreign}");
        }
    }

    #[test]
    fn the_item_is_written_read_and_removed() {
        let Some(t) = Scratch::new() else { return };
        let s = t.store();
        assert!(!s.registration().unwrap().exists);
        s.save(
            Preferences {
                autostart_enabled: true,
                ..Preferences::default()
            },
            || Ok(t.exe()),
        )
        .unwrap();
        let (ty, bytes) = read(&t.keys.run, NAME).unwrap().unwrap();
        assert_eq!(ty, REG_SZ);
        assert_eq!(
            text(ty, &bytes).unwrap(),
            format!("\"{}\" --autostart", t.exe().display())
        );
        let r = s.registration().unwrap();
        assert!(r.exists && r.owned && r.enabled);
        s.save(Preferences::default(), || {
            panic!("removing does not resolve the executable")
        })
        .unwrap();
        assert!(read(&t.keys.run, NAME).unwrap().is_none());
        assert!(!s.registration().unwrap().exists);
    }

    #[test]
    fn an_item_turned_off_in_task_manager_is_off_and_saving_it_on_clears_the_mark() {
        let Some(t) = Scratch::new() else { return };
        let s = t.store();
        let on = Preferences {
            autostart_enabled: true,
            ..Preferences::default()
        };
        s.save(on, || Ok(t.exe())).unwrap();
        assert!(s.registration().unwrap().enabled);
        let off: [u8; 12] = [3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0];
        write(&t.keys.approved, NAME, REG_BINARY, &off).unwrap();
        let r = s.registration().unwrap();
        assert!(r.exists && r.owned && !r.enabled);
        assert!(!s.view(on).unwrap().preferences.autostart_enabled);
        // The even first byte is on, as Windows writes it.
        write(
            &t.keys.approved,
            NAME,
            REG_BINARY,
            &[2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0],
        )
        .unwrap();
        assert!(s.registration().unwrap().enabled);
        write(&t.keys.approved, NAME, REG_BINARY, &off).unwrap();
        s.save(on, || Ok(t.exe())).unwrap();
        assert!(s.registration().unwrap().enabled);
        assert!(read(&t.keys.approved, NAME).unwrap().is_none());
        // Removing removes the mark too (the one Windows writes when the item is on).
        write(
            &t.keys.approved,
            NAME,
            REG_BINARY,
            &[2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0],
        )
        .unwrap();
        s.save(Preferences::default(), || Ok(t.exe())).unwrap();
        assert!(read(&t.keys.approved, NAME).unwrap().is_none());
        assert!(read(&t.keys.run, NAME).unwrap().is_none());
    }

    #[test]
    fn another_programs_value_is_left_alone() {
        let Some(t) = Scratch::new() else { return };
        let s = t.store();
        let on = Preferences {
            autostart_enabled: true,
            ..Preferences::default()
        };
        let theirs = wide(r"C:\other\program.exe /background");
        let data: Vec<u8> = theirs.iter().flat_map(|u| u.to_le_bytes()).collect();
        write(&t.keys.run, NAME, REG_SZ, &data).unwrap();
        let r = s.registration().unwrap();
        assert!(r.exists && !r.owned && !r.enabled);
        assert!(s.view(on).unwrap().registration_conflict);
        assert!(s.save(on, || Ok(t.exe())).is_err());
        s.save(Preferences::default(), || {
            panic!("disabling a foreign item must not resolve the executable")
        })
        .unwrap();
        let (_, kept) = read(&t.keys.run, NAME).unwrap().unwrap();
        assert_eq!(kept, data, "the other program's value changed");
        // A value of another type is another program's too.
        write(&t.keys.run, NAME, REG_DWORD, &[1, 0, 0, 0]).unwrap();
        let r = s.registration().unwrap();
        assert!(r.exists && !r.owned);
        assert!(s.save(on, || Ok(t.exe())).is_err());
        assert_eq!(read(&t.keys.run, NAME).unwrap().unwrap().0, REG_DWORD);
    }

    #[test]
    fn a_change_that_fails_once_made_is_undone() {
        let Some(t) = Scratch::new() else { return };
        let s = t.store();
        let on = Preferences {
            autostart_enabled: true,
            ..Preferences::default()
        };
        s.fault.store(true, std::sync::atomic::Ordering::SeqCst);
        assert!(s.save(on, || Ok(t.exe())).is_err());
        assert!(
            !s.registration().unwrap().exists,
            "the failed save left the item"
        );
        // The positive control on the same route: without the failure it is made.
        s.fault.store(false, std::sync::atomic::Ordering::SeqCst);
        s.save(on, || Ok(t.exe())).unwrap();
        let made = snapshot(&t.keys, NAME).unwrap();
        s.fault.store(true, std::sync::atomic::Ordering::SeqCst);
        assert!(s
            .save(Preferences::default(), || panic!(
                "removing resolves nothing"
            ))
            .is_err());
        assert_eq!(snapshot(&t.keys, NAME).unwrap(), made);
        assert!(s.registration().unwrap().enabled);
        // Reading the item back fails after it was changed: it is put back as well.
        s.fault.store(false, std::sync::atomic::Ordering::SeqCst);
        s.read_fault
            .store(true, std::sync::atomic::Ordering::SeqCst);
        assert!(s
            .save(Preferences::default(), || panic!(
                "removing resolves nothing"
            ))
            .is_err());
        assert_eq!(snapshot(&t.keys, NAME).unwrap(), made);
        s.read_fault
            .store(false, std::sync::atomic::Ordering::SeqCst);
    }

    #[test]
    fn a_failed_save_puts_the_item_back() {
        let Some(t) = Scratch::new() else { return };
        let s = t.store();
        s.save(Preferences::default(), || Ok(t.exe())).unwrap();
        // A folder where the settings file goes makes the writer's rename fail.
        fs::remove_file(s.file()).unwrap();
        fs::create_dir(s.file()).unwrap();
        let on = Preferences {
            autostart_enabled: true,
            ..Preferences::default()
        };
        assert!(s.save(on, || Ok(t.exe())).is_err());
        assert!(
            !s.registration().unwrap().exists,
            "the failed save left the item"
        );
        // And the other way: an item that was there is back after a failed removal.
        fs::remove_dir(s.file()).unwrap();
        s.save(on, || Ok(t.exe())).unwrap();
        let before = snapshot(&t.keys, NAME).unwrap();
        fs::remove_file(s.file()).unwrap();
        fs::create_dir(s.file()).unwrap();
        assert!(s.save(Preferences::default(), || Ok(t.exe())).is_err());
        assert_eq!(snapshot(&t.keys, NAME).unwrap(), before);
        assert!(s.registration().unwrap().enabled);
    }
}
