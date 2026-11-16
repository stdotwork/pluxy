//! Running Windows' own `wsl.exe` (Windows only): from System32 as Windows reports it, never found
//! on a PATH; with no window, standard input empty, a cleared environment (`SystemRoot`, `WINDIR`
//! and `WSL_UTF8=1` only), its output read while it runs and bounded, a deadline after which it is
//! ended through its own handle, and the call's cancel honoured. A launch WSL 1 sometimes refuses
//! with `0xd0000225` before anything runs is tried again. Also the registry's WSL entries, which
//! give the default distribution and each one's WSL version, for display only.

use pluxy_program::Error;
use std::io::Read;
use std::os::windows::process::CommandExt;
use std::path::PathBuf;
use std::process::{Command, Stdio};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

const CREATE_NO_WINDOW: u32 = 0x0800_0000;

/// The error WSL 1 sometimes gives a launch right after it starts; nothing ran.
const LAUNCH_ERROR: &str = "0xd0000225";

/// How many times a launch is tried when it fails with [`LAUNCH_ERROR`].
const TRIES: u32 = 3;

/// What `wsl.exe` did: its exit code (-1 for WSL's own errors) and its output.
pub struct Output {
    pub code: i32,
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
}

impl Output {
    /// Its output as text, for messages: both streams, decoded, trimmed.
    pub fn text(&self) -> String {
        let mut t = crate::list::decode(&self.stdout).trim().to_string();
        let e = crate::list::decode(&self.stderr);
        if !e.trim().is_empty() {
            if !t.is_empty() {
                t.push(' ');
            }
            t.push_str(e.trim());
        }
        t.chars().take(2000).collect()
    }
}

/// Windows' System32 folder, as Windows reports it (`GetSystemDirectoryW`), never from the
/// environment.
fn system32() -> Option<PathBuf> {
    use windows_sys::Win32::System::SystemInformation::GetSystemDirectoryW;
    let mut buf = [0u16; 261];
    // SAFETY: buf holds buf.len() characters.
    let n = unsafe { GetSystemDirectoryW(buf.as_mut_ptr(), buf.len() as u32) } as usize;
    if n == 0 || n >= buf.len() {
        return None;
    }
    let d = PathBuf::from(String::from_utf16(&buf[..n]).ok()?);
    (d.is_absolute() && d.is_dir()).then_some(d)
}

/// `wsl.exe` in System32, None when WSL is not installed.
pub fn wsl_exe() -> Option<PathBuf> {
    system32()
        .map(|d| d.join("wsl.exe"))
        .filter(|p| p.is_file())
}

#[cfg(test)]
#[path = "exec_tests.rs"]
mod tests;

/// Keeps this program's own standard handles (the host's pipes) from being inherited by what it
/// starts: a `wsl.exe` left running must never hold the host's pipes open.
pub fn keep_own_handles() {
    use windows_sys::Win32::Foundation::{SetHandleInformation, HANDLE_FLAG_INHERIT};
    use windows_sys::Win32::System::Console::{
        GetStdHandle, STD_ERROR_HANDLE, STD_INPUT_HANDLE, STD_OUTPUT_HANDLE,
    };
    for which in [STD_INPUT_HANDLE, STD_OUTPUT_HANDLE, STD_ERROR_HANDLE] {
        // SAFETY: GetStdHandle takes a constant; SetHandleInformation fails harmlessly on a null
        // or invalid handle.
        unsafe {
            let h = GetStdHandle(which);
            if !h.is_null() {
                SetHandleInformation(h, HANDLE_FLAG_INHERIT, 0);
            }
        }
    }
}

/// What a reader kept of its stream, and whether there was more than it keeps.
type Kept = Arc<Mutex<(Vec<u8>, bool)>>;

/// Reads r to its end into a buffer that keeps at most limit bytes, noting when there was more.
fn reader(mut r: impl Read + Send + 'static, limit: usize) -> (Kept, std::thread::JoinHandle<()>) {
    let buf = Arc::new(Mutex::new((Vec::new(), false)));
    let b = Arc::clone(&buf);
    let h = std::thread::spawn(move || {
        let mut chunk = [0u8; 8192];
        loop {
            match r.read(&mut chunk) {
                Ok(0) | Err(_) => break,
                Ok(n) => {
                    let mut g = b.lock().unwrap();
                    let room = limit.saturating_sub(g.0.len());
                    g.0.extend_from_slice(&chunk[..n.min(room)]);
                    if n > room {
                        g.1 = true;
                    }
                }
            }
        }
    });
    (buf, h)
}

/// Runs `wsl.exe` with args once: its output (each stream at most limit bytes), or an error once
/// the deadline passes, the output grows past limit or the call is cancelled (`wsl.exe` is ended
/// then, through its own handle).
fn once(
    exe: &PathBuf,
    args: &[String],
    limit: usize,
    deadline: Instant,
    cancelled: &dyn Fn() -> bool,
) -> Result<Output, Error> {
    let windows = exe
        .parent()
        .and_then(|d| d.parent())
        .ok_or_else(|| Error::unavailable("Windows' folder is unknown"))?;
    let mut child = Command::new(exe)
        .args(args)
        // System32, not wherever the app started this tool: `--exec` without `--cd` starts the
        // Linux side there.
        .current_dir(exe.parent().unwrap_or(windows))
        .env_clear()
        .env("SystemRoot", windows)
        .env("WINDIR", windows)
        .env("WSL_UTF8", "1")
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .creation_flags(CREATE_NO_WINDOW)
        .spawn()
        .map_err(|e| Error::unavailable(format!("could not start wsl.exe: {e}")))?;
    let (out, out_h) = reader(child.stdout.take().unwrap(), limit);
    let (err, err_h) = reader(child.stderr.take().unwrap(), limit);
    let over = |b: &Kept| b.lock().unwrap().1;
    let stop = |child: &mut std::process::Child, e: Error| {
        let _ = child.kill();
        let _ = child.wait();
        Err(e)
    };
    let status = loop {
        match child.try_wait() {
            Ok(Some(s)) => break s,
            Ok(None) => {}
            Err(e) => return stop(&mut child, Error::unavailable(format!("wsl.exe: {e}"))),
        }
        if cancelled() {
            return stop(&mut child, Error::cancelled());
        }
        if over(&out) || over(&err) {
            return stop(
                &mut child,
                Error::unavailable("wsl.exe wrote more than expected"),
            );
        }
        if Instant::now() >= deadline {
            return stop(
                &mut child,
                Error::unavailable("WSL did not answer in time; try again"),
            );
        }
        std::thread::sleep(Duration::from_millis(20));
    };
    // The pipes close when wsl.exe and whatever it handed them to have ended; a reader still
    // waiting a moment later is left to end with them.
    let until = Instant::now() + Duration::from_secs(2);
    while !(out_h.is_finished() && err_h.is_finished()) && Instant::now() < until {
        std::thread::sleep(Duration::from_millis(10));
    }
    if over(&out) || over(&err) {
        return Err(Error::unavailable("wsl.exe wrote more than expected"));
    }
    let take = |b: &Kept| std::mem::take(&mut b.lock().unwrap().0);
    Ok(Output {
        code: status.code().unwrap_or(-1),
        stdout: take(&out),
        stderr: take(&err),
    })
}

/// Runs `wsl.exe` with args, within timeout: as [`once`], tried again (up to three times, two
/// seconds apart, while the time allows) when WSL refuses the launch with `0xd0000225`.
pub fn wsl(
    args: &[String],
    limit: usize,
    timeout: Duration,
    cancelled: &dyn Fn() -> bool,
) -> Result<Output, Error> {
    let exe = wsl_exe().ok_or_else(|| Error::unavailable("WSL is not installed"))?;
    let deadline = Instant::now() + timeout;
    let mut tried = 1;
    loop {
        let out = once(&exe, args, limit, deadline, cancelled)?;
        let refused = out.code == -1 && out.text().contains(LAUNCH_ERROR);
        let later = Instant::now() + Duration::from_secs(3);
        if !refused || tried >= TRIES || later >= deadline {
            return Ok(out);
        }
        tried += 1;
        let wait = Instant::now() + Duration::from_secs(2);
        while Instant::now() < wait {
            if cancelled() {
                return Err(Error::cancelled());
            }
            std::thread::sleep(Duration::from_millis(50));
        }
    }
}

/// The distributions the registry knows (`HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss`):
/// each one's name and WSL version (from `Flags`, see `list::wsl_version`), and the default's name.
/// Values as WSL's own source names them (`DistributionName`, `Flags`, `DefaultDistribution`), read
/// for display only; anything missing or unreadable is left out.
pub fn registry() -> (Vec<(String, Option<u32>)>, Option<String>) {
    use windows_sys::Win32::System::Registry::{
        RegCloseKey, RegEnumKeyExW, RegGetValueW, RegOpenKeyExW, HKEY, HKEY_CURRENT_USER, KEY_READ,
        RRF_RT_REG_DWORD, RRF_RT_REG_SZ,
    };
    let wide = |s: &str| s.encode_utf16().chain([0]).collect::<Vec<u16>>();
    let string = |key: HKEY, sub: &[u16], value: &str| -> Option<String> {
        let mut buf = [0u16; 512];
        let mut len = (buf.len() * 2) as u32;
        // SAFETY: buf holds len bytes; the names are NUL-terminated.
        let r = unsafe {
            RegGetValueW(
                key,
                sub.as_ptr(),
                wide(value).as_ptr(),
                RRF_RT_REG_SZ,
                std::ptr::null_mut(),
                buf.as_mut_ptr().cast(),
                &mut len,
            )
        };
        let n = (len as usize / 2).min(buf.len());
        (r == 0).then(|| {
            String::from_utf16_lossy(&buf[..n])
                .trim_end_matches('\0')
                .to_string()
        })
    };
    let dword = |key: HKEY, sub: &[u16], value: &str| -> Option<u32> {
        let mut v = 0u32;
        let mut len = 4u32;
        // SAFETY: v holds 4 bytes; the names are NUL-terminated.
        let r = unsafe {
            RegGetValueW(
                key,
                sub.as_ptr(),
                wide(value).as_ptr(),
                RRF_RT_REG_DWORD,
                std::ptr::null_mut(),
                (&mut v as *mut u32).cast(),
                &mut len,
            )
        };
        (r == 0).then_some(v)
    };
    let mut key: HKEY = std::ptr::null_mut();
    let path = wide(r"Software\Microsoft\Windows\CurrentVersion\Lxss");
    // SAFETY: path is NUL-terminated; key receives the opened key, closed below.
    if unsafe { RegOpenKeyExW(HKEY_CURRENT_USER, path.as_ptr(), 0, KEY_READ, &mut key) } != 0 {
        return (Vec::new(), None);
    }
    let here = wide("");
    let default_guid = string(key, &here, "DefaultDistribution");
    let mut found = Vec::new();
    let mut default = None;
    for i in 0..256u32 {
        let mut name = [0u16; 256];
        let mut len = name.len() as u32;
        // SAFETY: name holds len characters.
        let r = unsafe {
            RegEnumKeyExW(
                key,
                i,
                name.as_mut_ptr(),
                &mut len,
                std::ptr::null(),
                std::ptr::null_mut(),
                std::ptr::null_mut(),
                std::ptr::null_mut(),
            )
        };
        if r != 0 {
            break;
        }
        let guid = String::from_utf16_lossy(&name[..len as usize]);
        let sub = wide(&guid);
        let Some(distro) = string(key, &sub, "DistributionName") else {
            continue;
        };
        if default_guid.as_deref() == Some(guid.as_str()) {
            default = Some(distro.clone());
        }
        found.push((
            distro,
            crate::list::wsl_version(|name| dword(key, &sub, name)),
        ));
    }
    // SAFETY: key was opened above.
    unsafe { RegCloseKey(key) };
    (found, default)
}
