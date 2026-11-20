//! What the native test sees of the real processes, threads and pipes, on each platform: the
//! processes running a program, whether a process runs, this app's thread names, and the
//! processes holding a pipe. Linux reads `/proc`; macOS asks libproc; Windows takes a Tool Help
//! snapshot and asks each process or thread. These only look: nothing here signals, stops or
//! changes a process.
//!
//! A process that has exited but not been reaped (a zombie, or on Windows a process whose handle
//! is still held) does not run: it is in no `processes` set and not `alive`.

use std::collections::BTreeSet;
use std::path::Path;

#[cfg(target_os = "linux")]
mod imp {
    use super::super::admission::require;
    use std::collections::BTreeSet;
    use std::path::Path;

    /// The processes running program, by /proc: the tool and the child it started.
    pub(super) fn processes(program: &Path) -> BTreeSet<u32> {
        std::fs::read_dir("/proc")
            .map(|d| {
                d.filter_map(|e| e.ok())
                    .filter_map(|e| e.file_name().to_str()?.parse::<u32>().ok())
                    .filter(|pid| {
                        std::fs::read_link(format!("/proc/{pid}/exe"))
                            .is_ok_and(|exe| exe == program)
                    })
                    .collect()
            })
            .unwrap_or_default()
    }

    /// Whether pid runs and is not a zombie.
    pub(super) fn alive(pid: u64) -> bool {
        std::fs::read_to_string(format!("/proc/{pid}/stat"))
            .ok()
            .and_then(|s| {
                s.rsplit_once(") ")
                    .and_then(|(_, rest)| rest.chars().next())
            })
            .is_some_and(|state| state != 'Z')
    }

    /// The names of this app's threads.
    pub(super) fn threads() -> Vec<String> {
        std::fs::read_dir("/proc/self/task")
            .map(|d| {
                d.filter_map(|e| e.ok())
                    .filter_map(|e| std::fs::read_to_string(e.path().join("comm")).ok())
                    .map(|s| s.trim().to_string())
                    .collect()
            })
            .unwrap_or_default()
    }

    /// The processes holding the pipe that pid has open as fd.
    pub(super) fn pipe_holders(pid: u64, fd: u32) -> Result<(String, BTreeSet<u64>), String> {
        let pipe = std::fs::read_link(format!("/proc/{pid}/fd/{fd}"))
            .map_err(|e| format!("fd {fd} of {pid}: {e}"))?
            .to_string_lossy()
            .into_owned();
        require(
            pipe.starts_with("pipe:"),
            format!("fd {fd} of the tool is {pipe}"),
        )?;
        let mut holders = BTreeSet::new();
        for e in std::fs::read_dir("/proc")
            .map_err(|e| e.to_string())?
            .flatten()
        {
            let Some(p) = e.file_name().to_str().and_then(|s| s.parse::<u64>().ok()) else {
                continue;
            };
            let Ok(fds) = std::fs::read_dir(e.path().join("fd")) else {
                continue;
            };
            if fds
                .flatten()
                .any(|f| std::fs::read_link(f.path()).is_ok_and(|l| l.to_string_lossy() == pipe))
            {
                holders.insert(p);
            }
        }
        Ok((pipe, holders))
    }
}

#[cfg(target_os = "macos")]
mod imp {
    use super::super::admission::require;
    use std::collections::BTreeSet;
    use std::ffi::CStr;
    use std::mem::{size_of, zeroed};
    use std::path::{Path, PathBuf};

    /// `<sys/proc_info.h>`: a thread list, and a pipe descriptor's information.
    const PROC_PIDLISTTHREADS: libc::c_int = 6;
    const PROC_PIDFDPIPEINFO: libc::c_int = 6;
    /// `<sys/proc.h>`: a zombie.
    const SZOMB: u32 = 5;

    /// `struct proc_fileinfo`.
    #[repr(C)]
    struct FileInfo {
        fi_openflags: u32,
        fi_status: u32,
        fi_offset: libc::off_t,
        fi_type: i32,
        fi_guardflags: u32,
    }

    /// `struct pipe_fdinfo`: its `proc_fileinfo` and `pipe_info`.
    #[repr(C)]
    struct PipeFdInfo {
        pfi: FileInfo,
        pipe_stat: libc::vinfo_stat,
        pipe_handle: u64,
        pipe_peerhandle: u64,
        pipe_status: libc::c_int,
        rfu_1: libc::c_int,
    }

    /// Every process ID, this user's or not.
    fn pids() -> Vec<libc::pid_t> {
        // SAFETY: a null buffer of size 0 asks only for the count.
        let n = unsafe { libc::proc_listallpids(std::ptr::null_mut(), 0) };
        if n <= 0 {
            return Vec::new();
        }
        // Room for processes started since.
        let mut v: Vec<libc::pid_t> = vec![0; n as usize + 256];
        let bytes = (v.len() * size_of::<libc::pid_t>()) as libc::c_int;
        // SAFETY: v holds bytes bytes.
        let got = unsafe { libc::proc_listallpids(v.as_mut_ptr().cast(), bytes) };
        v.truncate(got.max(0) as usize);
        v.retain(|p| *p > 0);
        v
    }

    /// The program file pid runs, while it runs (a zombie has none).
    fn path(pid: libc::pid_t) -> Option<PathBuf> {
        let mut buf = vec![0u8; libc::PROC_PIDPATHINFO_MAXSIZE as usize];
        // SAFETY: buf holds the length given.
        let n = unsafe { libc::proc_pidpath(pid, buf.as_mut_ptr().cast(), buf.len() as u32) };
        if n <= 0 {
            return None;
        }
        buf.truncate(n as usize);
        Some(PathBuf::from(String::from_utf8(buf).ok()?))
    }

    pub(super) fn processes(program: &Path) -> BTreeSet<u32> {
        let Ok(program) = std::fs::canonicalize(program) else {
            return BTreeSet::new();
        };
        pids()
            .into_iter()
            .filter(|pid| path(*pid).is_some_and(|p| p == program))
            .map(|pid| pid as u32)
            .collect()
    }

    pub(super) fn alive(pid: u64) -> bool {
        let Ok(pid) = libc::pid_t::try_from(pid) else {
            return false;
        };
        // SAFETY: an all-zero proc_bsdinfo is a valid value to fill.
        let mut info: libc::proc_bsdinfo = unsafe { zeroed() };
        let size = size_of::<libc::proc_bsdinfo>() as libc::c_int;
        // SAFETY: info is size bytes.
        let n = unsafe {
            libc::proc_pidinfo(
                pid,
                libc::PROC_PIDTBSDINFO,
                0,
                (&mut info as *mut libc::proc_bsdinfo).cast(),
                size,
            )
        };
        n == size && info.pbi_status != SZOMB
    }

    pub(super) fn threads() -> Vec<String> {
        // SAFETY: getpid has no preconditions.
        let me = unsafe { libc::getpid() };
        let mut ids: Vec<u64> = vec![0; 4096];
        let bytes = (ids.len() * size_of::<u64>()) as libc::c_int;
        // SAFETY: ids holds bytes bytes.
        let n = unsafe {
            libc::proc_pidinfo(me, PROC_PIDLISTTHREADS, 0, ids.as_mut_ptr().cast(), bytes)
        };
        ids.truncate((n.max(0) as usize) / size_of::<u64>());
        ids.into_iter()
            .filter_map(|id| {
                // SAFETY: an all-zero proc_threadinfo is a valid value to fill.
                let mut t: libc::proc_threadinfo = unsafe { zeroed() };
                let size = size_of::<libc::proc_threadinfo>() as libc::c_int;
                // SAFETY: t is size bytes.
                let got = unsafe {
                    libc::proc_pidinfo(
                        me,
                        libc::PROC_PIDTHREADINFO,
                        id,
                        (&mut t as *mut libc::proc_threadinfo).cast(),
                        size,
                    )
                };
                // The name's buffer, up to its first NUL (none when the name fills it).
                (got == size).then(|| {
                    let bytes: Vec<u8> = t.pth_name.iter().map(|c| *c as u8).collect();
                    match CStr::from_bytes_until_nul(&bytes) {
                        Ok(c) => c.to_string_lossy().into_owned(),
                        Err(_) => String::from_utf8_lossy(&bytes).into_owned(),
                    }
                })
            })
            .collect()
    }

    /// The pipe ends pid has open (the kernel's handles of each end), by descriptor.
    fn pipe_ends(pid: libc::pid_t) -> Vec<(i32, u64, u64)> {
        // SAFETY: an all-zero proc_fdinfo is a valid value to fill.
        let mut fds: Vec<libc::proc_fdinfo> = vec![unsafe { zeroed() }; 4096];
        let bytes = (fds.len() * size_of::<libc::proc_fdinfo>()) as libc::c_int;
        // SAFETY: fds holds bytes bytes.
        let n = unsafe {
            libc::proc_pidinfo(
                pid,
                libc::PROC_PIDLISTFDS,
                0,
                fds.as_mut_ptr().cast(),
                bytes,
            )
        };
        fds.truncate((n.max(0) as usize) / size_of::<libc::proc_fdinfo>());
        fds.into_iter()
            .filter(|f| f.proc_fdtype == libc::PROX_FDTYPE_PIPE as u32)
            .filter_map(|f| {
                // SAFETY: an all-zero PipeFdInfo (plain integers) is a valid value to fill.
                let mut info: PipeFdInfo = unsafe { zeroed() };
                let size = size_of::<PipeFdInfo>() as libc::c_int;
                // SAFETY: info is size bytes, laid out as `struct pipe_fdinfo`.
                let got = unsafe {
                    libc::proc_pidfdinfo(
                        pid,
                        f.proc_fd,
                        PROC_PIDFDPIPEINFO,
                        (&mut info as *mut PipeFdInfo).cast(),
                        size,
                    )
                };
                (got == size).then_some((f.proc_fd, info.pipe_handle, info.pipe_peerhandle))
            })
            .collect()
    }

    /// The processes holding either end of the pipe that pid has open as fd. Only this user's
    /// processes can be asked: no other user's process holds the app's or the tool's pipes.
    pub(super) fn pipe_holders(pid: u64, fd: u32) -> Result<(String, BTreeSet<u64>), String> {
        let tool = libc::pid_t::try_from(pid).map_err(|e| e.to_string())?;
        let end = pipe_ends(tool)
            .into_iter()
            .find(|(f, _, _)| *f as u32 == fd);
        require(end.is_some(), format!("fd {fd} of the tool is not a pipe"))?;
        let (_, handle, peer) = end.unwrap_or_default();
        require(
            handle != 0 && peer != 0,
            format!("fd {fd} of the tool: pipe {handle:#x}, peer {peer:#x}"),
        )?;
        let holders = pids()
            .into_iter()
            .filter(|p| {
                pipe_ends(*p)
                    .iter()
                    .any(|(_, h, _)| *h == handle || *h == peer)
            })
            .map(|p| p as u64)
            .collect();
        Ok((format!("pipe:{handle:#x}"), holders))
    }
}

#[cfg(windows)]
mod imp {
    use std::collections::BTreeSet;
    use std::ffi::OsString;
    use std::mem::{size_of, zeroed};
    use std::os::windows::ffi::OsStringExt;
    use std::path::{Path, PathBuf};
    use windows_sys::Win32::Foundation::{
        CloseHandle, LocalFree, HANDLE, INVALID_HANDLE_VALUE, WAIT_TIMEOUT,
    };
    use windows_sys::Win32::System::Diagnostics::ToolHelp::{
        CreateToolhelp32Snapshot, Process32FirstW, Process32NextW, Thread32First, Thread32Next,
        PROCESSENTRY32W, TH32CS_SNAPPROCESS, TH32CS_SNAPTHREAD, THREADENTRY32,
    };
    use windows_sys::Win32::System::Threading::{
        GetCurrentProcessId, GetThreadDescription, OpenProcess, OpenThread,
        QueryFullProcessImageNameW, WaitForSingleObject, PROCESS_NAME_WIN32,
        PROCESS_QUERY_LIMITED_INFORMATION, PROCESS_SYNCHRONIZE, THREAD_QUERY_LIMITED_INFORMATION,
    };

    /// A handle closed when dropped.
    struct Owned(HANDLE);
    impl Drop for Owned {
        fn drop(&mut self) {
            // SAFETY: the handle is this value's own, closed once.
            unsafe { CloseHandle(self.0) };
        }
    }

    fn snapshot(what: u32) -> Option<Owned> {
        // SAFETY: no pointers; the handle it returns is checked.
        let h = unsafe { CreateToolhelp32Snapshot(what, 0) };
        (h != INVALID_HANDLE_VALUE && !h.is_null()).then_some(Owned(h))
    }

    fn pids() -> Vec<u32> {
        let Some(s) = snapshot(TH32CS_SNAPPROCESS) else {
            return Vec::new();
        };
        // SAFETY: an all-zero PROCESSENTRY32W is a valid value to fill; its size is set below.
        let mut e: PROCESSENTRY32W = unsafe { zeroed() };
        e.dwSize = size_of::<PROCESSENTRY32W>() as u32;
        let mut out = Vec::new();
        // SAFETY: s is a live snapshot; e is a sized entry.
        let mut more = unsafe { Process32FirstW(s.0, &mut e) } != 0;
        while more {
            if e.th32ProcessID != 0 {
                out.push(e.th32ProcessID);
            }
            // SAFETY: as above.
            more = unsafe { Process32NextW(s.0, &mut e) } != 0;
        }
        out
    }

    /// pid, opened to query it, while it runs (not once it has exited, though held): its handle is
    /// not yet signalled. Its exit code would not do: a process may exit with 259, `STILL_ACTIVE`.
    // shortcut: no run has a process exit with 259, so nothing shows a process that did counted
    // as ended; upgrade when a fixture can exit with 259.
    fn running(pid: u32) -> Option<Owned> {
        // SAFETY: no pointers; the handle it returns is checked.
        let h = unsafe {
            OpenProcess(
                PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_SYNCHRONIZE,
                0,
                pid,
            )
        };
        if h.is_null() {
            return None;
        }
        let h = Owned(h);
        // SAFETY: h is a live process handle with SYNCHRONIZE; 0 ms only polls it.
        (unsafe { WaitForSingleObject(h.0, 0) } == WAIT_TIMEOUT).then_some(h)
    }

    fn image(h: &Owned) -> Option<PathBuf> {
        let mut buf = vec![0u16; 32768];
        let mut len = buf.len() as u32;
        // SAFETY: buf holds len characters.
        if unsafe {
            QueryFullProcessImageNameW(h.0, PROCESS_NAME_WIN32, buf.as_mut_ptr(), &mut len)
        } == 0
        {
            return None;
        }
        buf.truncate(len as usize);
        // The final path, as the program's own is compared.
        std::fs::canonicalize(PathBuf::from(OsString::from_wide(&buf))).ok()
    }

    pub(super) fn processes(program: &Path) -> BTreeSet<u32> {
        let Ok(program) = std::fs::canonicalize(program) else {
            return BTreeSet::new();
        };
        pids()
            .into_iter()
            .filter(|pid| {
                running(*pid)
                    .and_then(|h| image(&h))
                    .is_some_and(|p| p == program)
            })
            .collect()
    }

    pub(super) fn alive(pid: u64) -> bool {
        u32::try_from(pid).is_ok_and(|pid| running(pid).is_some())
    }

    pub(super) fn threads() -> Vec<String> {
        let Some(s) = snapshot(TH32CS_SNAPTHREAD) else {
            return Vec::new();
        };
        // SAFETY: no preconditions.
        let me = unsafe { GetCurrentProcessId() };
        // SAFETY: an all-zero THREADENTRY32 is a valid value to fill; its size is set below.
        let mut e: THREADENTRY32 = unsafe { zeroed() };
        e.dwSize = size_of::<THREADENTRY32>() as u32;
        let mut out = Vec::new();
        // SAFETY: s is a live snapshot; e is a sized entry.
        let mut more = unsafe { Thread32First(s.0, &mut e) } != 0;
        while more {
            if e.th32OwnerProcessID == me {
                // SAFETY: no pointers; the handle it returns is checked.
                let t = unsafe { OpenThread(THREAD_QUERY_LIMITED_INFORMATION, 0, e.th32ThreadID) };
                if !t.is_null() {
                    let t = Owned(t);
                    let mut name: *mut u16 = std::ptr::null_mut();
                    // SAFETY: t is a live thread handle; name receives a string the system
                    // allocated, NUL-terminated, which LocalFree releases once read.
                    if unsafe { GetThreadDescription(t.0, &mut name) } >= 0 && !name.is_null() {
                        // SAFETY: name is NUL-terminated, so every index up to and including the
                        // NUL is inside its allocation, and the count stops at the NUL.
                        let len = (0..).take_while(|i| unsafe { *name.add(*i) } != 0).count();
                        // SAFETY: the len characters before the NUL are initialised, aligned
                        // u16s that nothing writes while wide is read; name is freed only after.
                        let wide = unsafe { std::slice::from_raw_parts(name, len) };
                        out.push(String::from_utf16_lossy(wide));
                        // SAFETY: name came from GetThreadDescription and is freed once; wide,
                        // which borrows it, is no longer used.
                        unsafe { LocalFree(name.cast()) };
                    }
                }
            }
            // SAFETY: as above.
            more = unsafe { Thread32Next(s.0, &mut e) } != 0;
        }
        out
    }
}

/// The processes running program: the tool and the child it started.
pub(super) fn processes(program: &Path) -> BTreeSet<u32> {
    imp::processes(program)
}

/// Whether pid runs (and has not exited).
pub(super) fn alive(pid: u64) -> bool {
    imp::alive(pid)
}

/// The names of this app's threads.
pub(super) fn threads() -> Vec<String> {
    imp::threads()
}

/// The pipe that pid has open as fd, named, and the processes holding it (on macOS, either of
/// its ends). Windows has no such listing for an unprivileged process; there the tool reports
/// which of its standard handles are pipes instead.
#[cfg(unix)]
pub(super) fn pipe_holders(pid: u64, fd: u32) -> Result<(String, BTreeSet<u64>), String> {
    imp::pipe_holders(pid, fd)
}
