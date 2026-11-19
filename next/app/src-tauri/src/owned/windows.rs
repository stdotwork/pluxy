//! Windows: processes the app starts with `CreateProcessW` itself, suspended, so nothing of them
//! runs before the host has put them where they belong.
//!
//! - **What runs.** Windows cannot execute an open file. The host opens the confirmed file again
//!   by the path it has now, with every link resolved (`pin_file`), so that no process can write,
//!   rename or delete it while that handle is open, checks that it is the same file (volume and
//!   file ID, size and last write time) and a program image (`MZ`, `.exe` or `.com`: never a script, which
//!   `CreateProcessW` would hand to `cmd.exe`), and starts that path while it holds the handle.
//!   The folder it starts in is held the same way (`pin_dir`). These pins close once the
//!   process runs, when its image is mapped and Windows itself keeps the file from changing;
//!   `Command::keep` keeps a pin for as long as the process is held instead, for a file the
//!   process opens itself later (`wsl.exe --exec` running a Linux program from `/mnt/c`, pinned
//!   with `pin_other`).
//! - **What it gets.** Only its standard handles (`PROC_THREAD_ATTRIBUTE_HANDLE_LIST`), a cleared
//!   environment with exactly what the host gives, and a process group of its own.
//! - **Tools, managed runtimes and the app's own core** run in a job of their own (unnamed, so no
//!   other process can open it) that ends everything in it when its last handle closes: when the
//!   host ends it, and when the app itself ends, however it ends. Nothing in it can leave it. A
//!   process given a secret dies on an unhandled exception without an error report.
//! - **A detached launch** is started outside every job of the app's, or refused when the job the
//!   app runs in does not allow that (a terminal, IDE or service that ends its processes would end
//!   it too), and the host keeps no handle of it once it runs.
//! - **Stopping.** A managed runtime is asked first with CTRL_BREAK on its own console; its job
//!   is then terminated. A tool's stdin closes first (the contract); then its job is terminated.
//!   Only the job and process handles the host holds are ever used: never a process ID from
//!   elsewhere.

use super::Leader;
use std::ffi::{c_void, OsStr, OsString};
use std::fs::{File, OpenOptions};
use std::io::{self, Read};
use std::os::windows::ffi::OsStrExt;
use std::os::windows::fs::{FileExt, MetadataExt, OpenOptionsExt};
use std::os::windows::io::{AsRawHandle, FromRawHandle, OwnedHandle};
use std::os::windows::process::ExitStatusExt;
use std::path::{Path, PathBuf};
use std::process::ExitStatus;
use std::ptr;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use windows_sys::Win32::Foundation::{
    DuplicateHandle, SetHandleInformation, DUPLICATE_SAME_ACCESS, ERROR_ACCESS_DENIED, HANDLE,
    HANDLE_FLAG_INHERIT, INVALID_HANDLE_VALUE, WAIT_OBJECT_0,
};
use windows_sys::Win32::Storage::FileSystem::{
    FileBasicInfo, FileIdInfo, GetFileInformationByHandleEx, GetFinalPathNameByHandleW,
    FILE_BASIC_INFO, FILE_FLAG_BACKUP_SEMANTICS, FILE_ID_INFO, FILE_NAME_NORMALIZED,
    FILE_SHARE_READ, FILE_SHARE_WRITE, VOLUME_NAME_DOS,
};
use windows_sys::Win32::System::Console::{
    AttachConsole, FreeConsole, GenerateConsoleCtrlEvent, GetStdHandle, CTRL_BREAK_EVENT,
    STD_ERROR_HANDLE, STD_INPUT_HANDLE, STD_OUTPUT_HANDLE,
};
use windows_sys::Win32::System::JobObjects::{
    AssignProcessToJobObject, CreateJobObjectW, IsProcessInJob, JobObjectExtendedLimitInformation,
    QueryInformationJobObject, SetInformationJobObject, TerminateJobObject,
    JOBOBJECT_EXTENDED_LIMIT_INFORMATION, JOB_OBJECT_LIMIT_BREAKAWAY_OK,
    JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION, JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
    JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK,
};
use windows_sys::Win32::System::Pipes::CreatePipe;
use windows_sys::Win32::System::Threading::{
    CreateProcessW, DeleteProcThreadAttributeList, GetCurrentProcess, GetExitCodeProcess,
    InitializeProcThreadAttributeList, ResumeThread, TerminateProcess, UpdateProcThreadAttribute,
    WaitForSingleObject, CREATE_BREAKAWAY_FROM_JOB, CREATE_DEFAULT_ERROR_MODE, CREATE_NEW_CONSOLE,
    CREATE_NEW_PROCESS_GROUP, CREATE_NO_WINDOW, CREATE_SUSPENDED, CREATE_UNICODE_ENVIRONMENT,
    EXTENDED_STARTUPINFO_PRESENT, INFINITE, LPPROC_THREAD_ATTRIBUTE_LIST, PROCESS_INFORMATION,
    PROC_THREAD_ATTRIBUTE_HANDLE_LIST, STARTF_USESTDHANDLES, STARTUPINFOEXW, STARTUPINFOW,
};

pub(crate) type ChildStdin = File;
pub(crate) type ChildStdout = File;
pub(crate) type ChildStderr = File;

/// Why a detached launch is refused when the app runs in a job it may not leave.
pub(crate) const NO_BREAKAWAY: &str = "the app runs inside a job that does not let a detached launch outlive it (it was started by a terminal, an editor or a service that ends the processes it started): start the app another way, or use a managed launch";

/// Held while a handle a child is to get is inheritable, so no other spawn in the app passes it
/// on. std's own spawns pass on every inheritable handle: the app's run through `std_spawn`.
static SPAWN: Mutex<()> = Mutex::new(());

/// Held while the app attaches to a runtime's console to send it CTRL_BREAK.
static CONSOLE: Mutex<()> = Mutex::new(());

/// Spawns cmd with no handle of a child the host is starting inheritable meanwhile.
pub(crate) fn std_spawn(cmd: &mut std::process::Command) -> io::Result<std::process::Child> {
    let _held = SPAWN.lock().unwrap_or_else(|e| e.into_inner());
    cmd.spawn()
}

/// How a process is started, and what holds it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Kind {
    /// A program host's tool: its pipes, a hidden console, a job of its own.
    Tool,
    /// A managed runtime, or the app's own core: a hidden console of its own (for CTRL_BREAK), a
    /// job of its own.
    Managed,
    /// A detached launch: outside every job of the app's, held by nothing once it runs. Without
    /// a piped stdin it gets a console of its own and no standard handle of the app's (a terminal
    /// program opens its window, a desktop program ignores it); with one (a credential bridge)
    /// it runs headless.
    Detached,
    /// The app's next run, started by an update (`Successor`): in no job of the app's (it stays
    /// in whatever job the app itself runs in), held by its process handle until it accepts
    /// itself, then let go to outlive the app.
    Successor,
}

/// What one of a process's standard handles is.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Io {
    Null,
    Piped,
    /// The app's own standard handle of that number, when it has one (else as `Null`).
    Inherit,
}

/// A file opened again by the path it has now, which no process can write, rename or delete while
/// this is held.
pub(crate) struct Pinned {
    pub(crate) path: PathBuf,
    _file: File,
}

/// The path f has now, with every link and junction resolved, as a plain drive or UNC path (the
/// form `CreateProcessW` and a working folder take).
pub(crate) fn final_path(f: &File) -> Result<PathBuf, String> {
    let mut buf = vec![0u16; 512];
    loop {
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
                "finding the opened file's path: {}",
                io::Error::last_os_error()
            ));
        }
        if n < buf.len() {
            buf.truncate(n);
            break;
        }
        buf.resize(n + 1, 0);
    }
    let full = String::from_utf16(&buf).map_err(|_| "the path is not valid Unicode".to_string())?;
    let plain = if let Some(rest) = full.strip_prefix(r"\\?\UNC\") {
        format!(r"\\{rest}")
    } else if let Some(rest) = full.strip_prefix(r"\\?\") {
        rest.to_string()
    } else {
        full.clone()
    };
    // The plain form is read again by Windows' path rules, which drop a trailing dot or space of
    // a name and limit its length: refuse a path those rules would read otherwise.
    if plain.len() >= 248
        || plain
            .split('\\')
            .skip(1)
            .any(|c| c.ends_with('.') || c.ends_with(' '))
    {
        return Err(format!(
            "{full}: a path this long, or with a name ending in a dot or a space, cannot be started from"
        ));
    }
    Ok(PathBuf::from(plain))
}

/// Volume serial number and file ID.
fn file_id(f: &File) -> io::Result<[u8; 24]> {
    let mut info: FILE_ID_INFO = unsafe { std::mem::zeroed() };
    if unsafe {
        GetFileInformationByHandleEx(
            f.as_raw_handle() as HANDLE,
            FileIdInfo,
            &mut info as *mut _ as *mut c_void,
            std::mem::size_of::<FILE_ID_INFO>() as u32,
        )
    } == 0
    {
        return Err(io::Error::last_os_error());
    }
    let mut out = [0u8; 24];
    out[..8].copy_from_slice(&info.VolumeSerialNumber.to_le_bytes());
    out[8..].copy_from_slice(&info.FileId.Identifier);
    Ok(out)
}

/// Size and last write time: what writing the file changes, which no one can do while the pin is
/// open (it shares reading only). Change time and attributes are left out: they move with changes
/// to the file's names and metadata, such as another hard link to it made or removed, which leave
/// its bytes as they were, so two reads of the same file a moment apart could differ.
fn state(f: &File) -> io::Result<(u64, i64)> {
    let mut info: FILE_BASIC_INFO = unsafe { std::mem::zeroed() };
    if unsafe {
        GetFileInformationByHandleEx(
            f.as_raw_handle() as HANDLE,
            FileBasicInfo,
            &mut info as *mut _ as *mut c_void,
            std::mem::size_of::<FILE_BASIC_INFO>() as u32,
        )
    } == 0
    {
        return Err(io::Error::last_os_error());
    }
    let size = f.metadata()?.file_size();
    Ok((size, info.LastWriteTime))
}

/// Opens path so that no process can write, rename or delete it while the file is open (only
/// reading is shared): a program the host has checked stays what it checked.
pub(crate) fn open_pinned(path: &Path) -> io::Result<File> {
    OpenOptions::new()
        .read(true)
        .share_mode(FILE_SHARE_READ)
        .open(path)
}

/// Refused unless f is a program image: named `.exe` or `.com`, starting `MZ`.
pub(crate) fn image(f: &File, path: &Path) -> Result<(), String> {
    let named = path
        .extension()
        .and_then(OsStr::to_str)
        .is_some_and(|e| e.eq_ignore_ascii_case("exe") || e.eq_ignore_ascii_case("com"));
    let mut b = [0u8; 2];
    let mz = f.seek_read(&mut b, 0).ok() == Some(2) && &b == b"MZ";
    if !(named && mz) {
        return Err(format!(
            "{} is not a program (.exe): on Windows the app starts programs only, never a script by its name; name its interpreter as the program",
            path.display()
        ));
    }
    Ok(())
}

/// held, the program a confirmation opened, pinned by the path it has now (see the module).
pub(crate) fn pin_file(held: &File) -> Result<Pinned, String> {
    let p = pin_other(held)?;
    image(&p._file, &p.path)?;
    Ok(p)
}

/// held, a file a confirmation opened that the process runs or reads by its path itself, not a
/// program image (such as a Linux program `wsl.exe` runs), pinned as `pin_file` pins a program.
pub(crate) fn pin_other(held: &File) -> Result<Pinned, String> {
    let path = final_path(held)?;
    let f = open_pinned(&path).map_err(|e| format!("{}: {e}", path.display()))?;
    // Both read, and equal: a file whose identity or state cannot be read is refused.
    let same = matches!((file_id(&f), file_id(held)), (Ok(a), Ok(b)) if a == b)
        && matches!((state(&f), state(held)), (Ok(a), Ok(b)) if a == b);
    if !same {
        return Err(format!(
            "{} is no longer the file that was opened",
            path.display()
        ));
    }
    Ok(Pinned { path, _file: f })
}

/// held, a folder a confirmation opened, pinned by the path it has now: it cannot be renamed or
/// removed while this is held.
pub(crate) fn pin_dir(held: &File) -> Result<Pinned, String> {
    let path = final_path(held)?;
    let f = OpenOptions::new()
        .read(true)
        .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
        .custom_flags(FILE_FLAG_BACKUP_SEMANTICS)
        .open(&path)
        .map_err(|e| format!("{}: {e}", path.display()))?;
    if file_id(&f).ok().is_none() || file_id(&f).ok() != file_id(held).ok() {
        return Err(format!(
            "{} is no longer the folder that was opened",
            path.display()
        ));
    }
    Ok(Pinned { path, _file: f })
}

/// A process to start (see the module). Built like `std::process::Command`.
pub(crate) struct Command {
    program: Pinned,
    cwd: Option<Pinned>,
    cwd_path: Option<PathBuf>,
    name: OsString,
    args: Vec<OsString>,
    env: Vec<(OsString, OsString)>,
    stdin: Io,
    stdout: Io,
    stderr: Io,
    kind: Kind,
    secrets: bool,
    keep_program: bool,
    kept: Vec<Pinned>,
}

impl Command {
    /// Runs program, named as its path until `arg0`; no environment until `env`.
    pub(crate) fn new(program: Pinned, kind: Kind) -> Command {
        Command {
            name: program.path.clone().into_os_string(),
            program,
            cwd: None,
            cwd_path: None,
            args: Vec::new(),
            env: Vec::new(),
            stdin: Io::Null,
            stdout: Io::Null,
            stderr: Io::Null,
            kind,
            secrets: false,
            keep_program: false,
            kept: Vec::new(),
        }
    }

    pub(crate) fn arg0(&mut self, name: impl AsRef<OsStr>) -> &mut Self {
        self.name = name.as_ref().to_os_string();
        self
    }

    pub(crate) fn args<I, S>(&mut self, args: I) -> &mut Self
    where
        I: IntoIterator<Item = S>,
        S: AsRef<OsStr>,
    {
        self.args
            .extend(args.into_iter().map(|a| a.as_ref().to_os_string()));
        self
    }

    pub(crate) fn env(&mut self, k: impl AsRef<OsStr>, v: impl AsRef<OsStr>) -> &mut Self {
        self.env
            .push((k.as_ref().to_os_string(), v.as_ref().to_os_string()));
        self
    }

    pub(crate) fn envs<I, K, V>(&mut self, vars: I) -> &mut Self
    where
        I: IntoIterator<Item = (K, V)>,
        K: AsRef<OsStr>,
        V: AsRef<OsStr>,
    {
        for (k, v) in vars {
            self.env(k, v);
        }
        self
    }

    /// Starts in the pinned folder dir.
    pub(crate) fn cwd(&mut self, dir: Pinned) -> &mut Self {
        self.cwd = Some(dir);
        self
    }

    /// Starts in folder dir, by its path (a tool's own package folder).
    pub(crate) fn current_dir(&mut self, dir: impl AsRef<Path>) -> &mut Self {
        self.cwd_path = Some(dir.as_ref().to_path_buf());
        self
    }

    pub(crate) fn stdin(&mut self, io: Io) -> &mut Self {
        self.stdin = io;
        self
    }

    pub(crate) fn stdout(&mut self, io: Io) -> &mut Self {
        self.stdout = io;
        self
    }

    pub(crate) fn stderr(&mut self, io: Io) -> &mut Self {
        self.stderr = io;
        self
    }

    /// It is given a secret: no error report of it (see the module).
    pub(crate) fn secrets(&mut self, secrets: bool) -> &mut Self {
        self.secrets = secrets;
        self
    }

    /// Keeps the program's pin (and its folder's) for as long as the returned child is held, not
    /// only until it runs. Tools and managed processes only.
    // No caller outside the tests yet.
    #[cfg_attr(not(test), allow(dead_code))]
    pub(crate) fn keep_program(&mut self) -> &mut Self {
        self.keep_program = true;
        self
    }

    /// Keeps pin, another file the process uses by its path (`pin_other`), from being written,
    /// renamed or removed for as long as the returned child is held. Tools and managed processes
    /// only.
    // No caller outside the tests yet.
    #[cfg_attr(not(test), allow(dead_code))]
    pub(crate) fn keep(&mut self, pin: Pinned) -> &mut Self {
        self.kept.push(pin);
        self
    }

    /// Starts a tool or a managed process, in a job of its own, held by the returned child.
    pub(crate) fn spawn(&mut self) -> io::Result<Child> {
        if matches!(self.kind, Kind::Detached | Kind::Successor) {
            return Err(io::Error::other(
                "only a tool or a managed process is held in a job",
            ));
        }
        let c = self.create(0)?;
        let job = match job(self.secrets).and_then(|j| {
            if unsafe {
                AssignProcessToJobObject(
                    j.as_raw_handle() as HANDLE,
                    c.process.as_raw_handle() as HANDLE,
                )
            } == 0
            {
                Err(io::Error::last_os_error())
            } else {
                Ok(j)
            }
        }) {
            Ok(j) => j,
            Err(e) => {
                c.abandon();
                return Err(e);
            }
        };
        let c = c.resume()?;
        // A duplicate handle is the same open file, with the same sharing it allows.
        let mut kept = Vec::new();
        if self.keep_program {
            kept.push(self.program._file.try_clone()?);
            if let Some(d) = &self.cwd {
                kept.push(d._file.try_clone()?);
            }
        }
        for p in std::mem::take(&mut self.kept) {
            kept.push(p._file);
        }
        Ok(Child {
            _kept: kept,
            process: c.process,
            job,
            pid: c.pid,
            stdin: c.stdin,
            stdout: c.stdout,
            stderr: c.stderr,
            status: None,
        })
    }

    /// Starts a detached launch outside every job of the app's, or refuses it before it runs
    /// (`NO_BREAKAWAY`); keeps nothing of it, but its stdin's pipe when piped.
    pub(crate) fn spawn_detached(&mut self) -> io::Result<Option<File>> {
        if self.kind != Kind::Detached {
            return Err(io::Error::other("only a detached launch is let go"));
        }
        if self.keep_program || !self.kept.is_empty() {
            return Err(io::Error::other(
                "a detached launch is not held, so nothing could keep its files pinned",
            ));
        }
        let flags = breakaway().map_err(|e| io::Error::new(io::ErrorKind::PermissionDenied, e))?;
        let c = match self.create(flags) {
            Err(e) if e.raw_os_error() == Some(ERROR_ACCESS_DENIED as i32) && flags != 0 => {
                return Err(io::Error::new(
                    io::ErrorKind::PermissionDenied,
                    NO_BREAKAWAY,
                ))
            }
            r => r?,
        };
        let mut in_job = 0;
        if unsafe {
            IsProcessInJob(
                c.process.as_raw_handle() as HANDLE,
                ptr::null_mut(),
                &mut in_job,
            )
        } == 0
            || in_job != 0
        {
            c.abandon();
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                NO_BREAKAWAY,
            ));
        }
        let Created { stdin, .. } = c.resume()?;
        Ok(stdin)
    }

    /// Creates the process, suspended.
    fn create(&mut self, extra: u32) -> io::Result<Created> {
        let program = wide(self.program.path.as_os_str())?;
        let mut cmdline = command_line(&self.name, &self.args)?;
        let env = environment_block(&self.env)?;
        let cwd = match (&self.cwd, &self.cwd_path) {
            (Some(p), _) => Some(wide(p.path.as_os_str())?),
            (None, Some(p)) => Some(wide(p.as_os_str())?),
            (None, None) => None,
        };
        // A detached launch without a piped stdin gets a console of its own and none of the app's
        // handles at all.
        let own_console = self.kind == Kind::Detached && self.stdin == Io::Null;
        let mut parent: [Option<File>; 3] = [None, None, None];
        let mut child: Vec<File> = Vec::new();
        let mut std_handles: [HANDLE; 3] = [ptr::null_mut(); 3];
        if !own_console {
            let mut null: Option<File> = None;
            for (i, io) in [self.stdin, self.stdout, self.stderr]
                .into_iter()
                .enumerate()
            {
                match io {
                    Io::Null => {
                        if null.is_none() {
                            null = Some(OpenOptions::new().read(true).write(true).open("NUL")?);
                        }
                        std_handles[i] = null.as_ref().unwrap().as_raw_handle() as HANDLE;
                    }
                    Io::Inherit => {
                        // A copy of the app's own handle, which the lines below make
                        // inheritable for this one child; the app's own stays as it is.
                        let own = unsafe {
                            GetStdHandle([STD_INPUT_HANDLE, STD_OUTPUT_HANDLE, STD_ERROR_HANDLE][i])
                        };
                        let mut copy: HANDLE = ptr::null_mut();
                        let copied = !own.is_null()
                            && own != INVALID_HANDLE_VALUE
                            && unsafe {
                                DuplicateHandle(
                                    GetCurrentProcess(),
                                    own,
                                    GetCurrentProcess(),
                                    &mut copy,
                                    0,
                                    0,
                                    DUPLICATE_SAME_ACCESS,
                                )
                            } != 0;
                        if copied {
                            let f = unsafe { File::from_raw_handle(copy as _) };
                            std_handles[i] = f.as_raw_handle() as HANDLE;
                            child.push(f);
                        } else {
                            if null.is_none() {
                                null = Some(OpenOptions::new().read(true).write(true).open("NUL")?);
                            }
                            std_handles[i] = null.as_ref().unwrap().as_raw_handle() as HANDLE;
                        }
                    }
                    Io::Piped => {
                        let (r, w) = pipe(if i == 0 { 64 << 10 } else { 0 })?;
                        let (ours, theirs) = if i == 0 { (w, r) } else { (r, w) };
                        std_handles[i] = theirs.as_raw_handle() as HANDLE;
                        parent[i] = Some(ours);
                        child.push(theirs);
                    }
                }
            }
            if let Some(n) = null {
                child.push(n);
            }
        }
        let mut flags =
            CREATE_SUSPENDED | CREATE_UNICODE_ENVIRONMENT | CREATE_NEW_PROCESS_GROUP | extra;
        flags |= if own_console {
            CREATE_NEW_CONSOLE
        } else {
            CREATE_NO_WINDOW
        };
        if !self.secrets {
            flags |= CREATE_DEFAULT_ERROR_MODE;
        }
        let mut list_buf: Vec<usize> = Vec::new();
        let mut si: STARTUPINFOEXW = unsafe { std::mem::zeroed() };
        let handles: Vec<HANDLE> = child.iter().map(|f| f.as_raw_handle() as HANDLE).collect();
        let mut pi: PROCESS_INFORMATION = unsafe { std::mem::zeroed() };
        let created = {
            let _held = SPAWN.lock().unwrap_or_else(|e| e.into_inner());
            // The child's ends, made inheritable below, belong to this block from here: declared
            // after the lock's guard, they close before it is let go on every way out of it, an
            // error's too, so no other spawn can pass them on.
            let child = child;
            for h in &handles {
                if unsafe { SetHandleInformation(*h, HANDLE_FLAG_INHERIT, HANDLE_FLAG_INHERIT) }
                    == 0
                {
                    return Err(io::Error::last_os_error());
                }
            }
            let mut list: LPPROC_THREAD_ATTRIBUTE_LIST = ptr::null_mut();
            if !handles.is_empty() {
                let mut size = 0usize;
                unsafe { InitializeProcThreadAttributeList(ptr::null_mut(), 1, 0, &mut size) };
                list_buf = vec![0usize; size.div_ceil(std::mem::size_of::<usize>())];
                list = list_buf.as_mut_ptr() as LPPROC_THREAD_ATTRIBUTE_LIST;
                if unsafe { InitializeProcThreadAttributeList(list, 1, 0, &mut size) } == 0 {
                    return Err(io::Error::last_os_error());
                }
                if unsafe {
                    UpdateProcThreadAttribute(
                        list,
                        0,
                        PROC_THREAD_ATTRIBUTE_HANDLE_LIST as usize,
                        handles.as_ptr() as *const c_void,
                        handles.len() * std::mem::size_of::<HANDLE>(),
                        ptr::null_mut(),
                        ptr::null(),
                    )
                } == 0
                {
                    let e = io::Error::last_os_error();
                    unsafe { DeleteProcThreadAttributeList(list) };
                    return Err(e);
                }
                si.lpAttributeList = list;
                si.StartupInfo.cb = std::mem::size_of::<STARTUPINFOEXW>() as u32;
                si.StartupInfo.dwFlags = STARTF_USESTDHANDLES;
                si.StartupInfo.hStdInput = std_handles[0];
                si.StartupInfo.hStdOutput = std_handles[1];
                si.StartupInfo.hStdError = std_handles[2];
                flags |= EXTENDED_STARTUPINFO_PRESENT;
            } else {
                si.StartupInfo.cb = std::mem::size_of::<STARTUPINFOW>() as u32;
            }
            let ok = unsafe {
                CreateProcessW(
                    program.as_ptr(),
                    cmdline.as_mut_ptr(),
                    ptr::null(),
                    ptr::null(),
                    i32::from(!handles.is_empty()),
                    flags,
                    env.as_ptr() as *const c_void,
                    cwd.as_ref().map_or(ptr::null(), |c| c.as_ptr()),
                    &si.StartupInfo,
                    &mut pi,
                )
            };
            let err = io::Error::last_os_error();
            if !list.is_null() {
                unsafe { DeleteProcThreadAttributeList(list) };
            }
            // The child's ends close here, still under the lock.
            drop(child);
            if ok == 0 {
                Err(err)
            } else {
                Ok(())
            }
        };
        drop(list_buf);
        created?;
        let [stdin, stdout, stderr] = parent;
        Ok(Created {
            process: unsafe { OwnedHandle::from_raw_handle(pi.hProcess as _) },
            thread: unsafe { OwnedHandle::from_raw_handle(pi.hThread as _) },
            pid: pi.dwProcessId,
            stdin,
            stdout,
            stderr,
        })
    }
}

/// A process just created, suspended: none of its code has run.
struct Created {
    process: OwnedHandle,
    thread: OwnedHandle,
    pid: u32,
    stdin: Option<File>,
    stdout: Option<File>,
    stderr: Option<File>,
}

impl Created {
    /// Ends it before it ran: the only use of `TerminateProcess`, on the handle of a process the
    /// host created this moment.
    fn abandon(self) {
        unsafe {
            TerminateProcess(self.process.as_raw_handle() as HANDLE, 1);
            WaitForSingleObject(self.process.as_raw_handle() as HANDLE, INFINITE);
        }
    }

    /// Lets it run.
    fn resume(self) -> io::Result<Created> {
        if unsafe { ResumeThread(self.thread.as_raw_handle() as HANDLE) } == u32::MAX {
            let e = io::Error::last_os_error();
            self.abandon();
            return Err(e);
        }
        Ok(self)
    }
}

/// An anonymous pipe, neither end inheritable; size is the buffer asked for (0: the default).
fn pipe(size: u32) -> io::Result<(File, File)> {
    let (mut r, mut w): (HANDLE, HANDLE) = (ptr::null_mut(), ptr::null_mut());
    if unsafe { CreatePipe(&mut r, &mut w, ptr::null(), size) } == 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(unsafe { (File::from_raw_handle(r as _), File::from_raw_handle(w as _)) })
}

/// A new job, unnamed, that ends everything in it when its last handle closes; with secrets, a
/// process in it that crashes dies without an error report.
fn job(secrets: bool) -> io::Result<OwnedHandle> {
    let h = unsafe { CreateJobObjectW(ptr::null(), ptr::null()) };
    if h.is_null() {
        return Err(io::Error::last_os_error());
    }
    let job = unsafe { OwnedHandle::from_raw_handle(h as _) };
    let mut info: JOBOBJECT_EXTENDED_LIMIT_INFORMATION = unsafe { std::mem::zeroed() };
    info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
        | if secrets {
            JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION
        } else {
            0
        };
    if unsafe {
        SetInformationJobObject(
            h,
            JobObjectExtendedLimitInformation,
            &info as *const _ as *const c_void,
            std::mem::size_of::<JOBOBJECT_EXTENDED_LIMIT_INFORMATION>() as u32,
        )
    } == 0
    {
        return Err(io::Error::last_os_error());
    }
    Ok(job)
}

/// The creation flag a detached launch needs to leave the job the app runs in (none when it runs
/// in none, or its job lets children leave silently), or why it cannot.
pub(crate) fn breakaway() -> Result<u32, String> {
    let mut in_job = 0;
    if unsafe { IsProcessInJob(GetCurrentProcess(), ptr::null_mut(), &mut in_job) } == 0 {
        return Err(format!(
            "checking the app's job: {}",
            io::Error::last_os_error()
        ));
    }
    if in_job == 0 {
        return Ok(0);
    }
    let mut info: JOBOBJECT_EXTENDED_LIMIT_INFORMATION = unsafe { std::mem::zeroed() };
    if unsafe {
        QueryInformationJobObject(
            ptr::null_mut(),
            JobObjectExtendedLimitInformation,
            &mut info as *mut _ as *mut c_void,
            std::mem::size_of::<JOBOBJECT_EXTENDED_LIMIT_INFORMATION>() as u32,
            ptr::null_mut(),
        )
    } == 0
    {
        return Err(format!(
            "reading the app's job: {}",
            io::Error::last_os_error()
        ));
    }
    let f = info.BasicLimitInformation.LimitFlags;
    if f & JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK != 0 {
        Ok(0)
    } else if f & JOB_OBJECT_LIMIT_BREAKAWAY_OK != 0 {
        Ok(CREATE_BREAKAWAY_FROM_JOB)
    } else {
        Err(NO_BREAKAWAY.into())
    }
}

/// s as a NUL-terminated UTF-16 string; refused if it holds a NUL.
fn wide(s: &OsStr) -> io::Result<Vec<u16>> {
    let mut v: Vec<u16> = s.encode_wide().collect();
    if v.contains(&0) {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "a NUL in a path or argument",
        ));
    }
    v.push(0);
    Ok(v)
}

/// The command line `CommandLineToArgvW` (and the C runtimes) read back as name, then args.
pub(crate) fn command_line(name: &OsStr, args: &[OsString]) -> io::Result<Vec<u16>> {
    let mut out: Vec<u16> = Vec::new();
    let n: Vec<u16> = name.encode_wide().collect();
    // The program's name is read up to the next quote, with no escapes: it may hold none.
    if n.contains(&(b'"' as u16)) || n.contains(&0) {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "a program name with a quote or a NUL",
        ));
    }
    out.push(b'"' as u16);
    out.extend(&n);
    out.push(b'"' as u16);
    for a in args {
        out.push(b' ' as u16);
        let a: Vec<u16> = a.encode_wide().collect();
        if a.contains(&0) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "an argument with a NUL",
            ));
        }
        let quote = a.is_empty() || a.iter().any(|c| *c == b' ' as u16 || *c == b'\t' as u16);
        if quote {
            out.push(b'"' as u16);
        }
        let mut backslashes = 0;
        for c in a {
            if c == b'\\' as u16 {
                backslashes += 1;
            } else {
                if c == b'"' as u16 {
                    out.extend(std::iter::repeat_n(b'\\' as u16, backslashes + 1));
                }
                backslashes = 0;
            }
            out.push(c);
        }
        if quote {
            out.extend(std::iter::repeat_n(b'\\' as u16, backslashes));
            out.push(b'"' as u16);
        }
    }
    out.push(0);
    Ok(out)
}

/// The environment block: `NAME=value` entries sorted by name ignoring case (as Windows keeps
/// them), each name once, ending in an empty entry.
pub(crate) fn environment_block(vars: &[(OsString, OsString)]) -> io::Result<Vec<u16>> {
    let mut v: Vec<(Vec<u16>, Vec<u16>)> = Vec::new();
    for (k, val) in vars {
        let k: Vec<u16> = k.encode_wide().collect();
        let val: Vec<u16> = val.encode_wide().collect();
        if k.is_empty() || k[1..].contains(&(b'=' as u16)) || k.contains(&0) || val.contains(&0) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "an environment variable name or value it cannot hold",
            ));
        }
        let upper = |s: &[u16]| -> Vec<u16> {
            String::from_utf16_lossy(s)
                .to_uppercase()
                .encode_utf16()
                .collect()
        };
        match v.iter_mut().find(|(n, _)| upper(n) == upper(&k)) {
            Some(e) => e.1 = val,
            None => v.push((k, val)),
        }
    }
    v.sort_by_cached_key(|(n, _)| String::from_utf16_lossy(n).to_uppercase());
    let mut out = Vec::new();
    for (k, val) in v {
        out.extend(k);
        out.push(b'=' as u16);
        out.extend(val);
        out.push(0);
    }
    if out.is_empty() {
        out.push(0);
    }
    out.push(0);
    Ok(out)
}

/// A process the host started and holds: its process handle (its process ID is never another
/// process's while this is held) and its job.
pub(crate) struct Child {
    /// Pins held as long as it is (`Command::keep`, `Command::keep_program`).
    _kept: Vec<File>,
    process: OwnedHandle,
    job: OwnedHandle,
    pid: u32,
    pub(crate) stdin: Option<File>,
    pub(crate) stdout: Option<File>,
    pub(crate) stderr: Option<File>,
    status: Option<ExitStatus>,
}

impl Child {
    pub(crate) fn id(&self) -> u32 {
        self.pid
    }

    fn exit_code(&mut self) -> io::Result<ExitStatus> {
        let mut code = 0u32;
        if unsafe { GetExitCodeProcess(self.process.as_raw_handle() as HANDLE, &mut code) } == 0 {
            return Err(io::Error::last_os_error());
        }
        let s = ExitStatus::from_raw(code);
        self.status = Some(s);
        Ok(s)
    }

    pub(crate) fn try_wait(&mut self) -> io::Result<Option<ExitStatus>> {
        if let Some(s) = self.status {
            return Ok(Some(s));
        }
        if unsafe { WaitForSingleObject(self.process.as_raw_handle() as HANDLE, 0) }
            != WAIT_OBJECT_0
        {
            return Ok(None);
        }
        self.exit_code().map(Some)
    }

    pub(crate) fn wait(&mut self) -> io::Result<ExitStatus> {
        if let Some(s) = self.status {
            return Ok(s);
        }
        if unsafe { WaitForSingleObject(self.process.as_raw_handle() as HANDLE, INFINITE) }
            != WAIT_OBJECT_0
        {
            return Err(io::Error::last_os_error());
        }
        self.exit_code()
    }

    /// Ends it and everything in its job.
    pub(crate) fn kill(&mut self) -> io::Result<()> {
        if unsafe { TerminateJobObject(self.job.as_raw_handle() as HANDLE, 1) } == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(())
    }
}

/// The state of child, without forgetting its exit.
pub(crate) fn leader(child: &Child) -> Leader {
    if child.status.is_some()
        || unsafe { WaitForSingleObject(child.process.as_raw_handle() as HANDLE, 0) }
            == WAIT_OBJECT_0
    {
        Leader::Exited
    } else {
        Leader::Running
    }
}

/// Asks child's process group to stop with CTRL_BREAK on its own console, while it runs: true
/// once sent; false when it cannot get one (a desktop program has no console, and an app that has
/// a console of its own cannot attach to another), when the caller ends it at once.
pub(crate) fn term_group(child: &Child) -> bool {
    if leader(child) != Leader::Running {
        return true;
    }
    let _held = CONSOLE.lock().unwrap_or_else(|e| e.into_inner());
    if unsafe { AttachConsole(child.pid) } == 0 {
        return false;
    }
    // The group is the one CREATE_NEW_PROCESS_GROUP made, on the console only it uses.
    let sent = unsafe { GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT, child.pid) } != 0;
    unsafe { FreeConsole() };
    sent
}

/// Asks child to stop, as `term_group`: on Windows a process the host starts always leads a
/// process group of its own, which a CTRL_BREAK reaches as a whole.
pub(crate) fn term_process(child: &Child) -> bool {
    term_group(child)
}

/// Ends child and everything in its job.
pub(crate) fn kill_group(child: &mut Child) {
    let _ = child.kill();
}

/// Reads from r until it has data or the end, or closed is set: then None. Every process that
/// could hold the pipe is in the tool's job, which ends with it, so the end comes once it has
/// been reaped.
pub(crate) fn read_some(closed: &AtomicBool, r: &mut File, buf: &mut [u8]) -> Option<usize> {
    if closed.load(Ordering::SeqCst) {
        return None;
    }
    match r.read(buf) {
        Ok(0) | Err(_) => None,
        Ok(n) => Some(n),
    }
}

/// The app's next run, started by an update or a rollback (`updater/`): a process the app starts
/// itself and holds by its process handle (its process ID is never another's while held), in no
/// job of the app's, so it outlives the app once let go. Until then the app may end it, and only
/// it: by that handle. Its own tools and core are in jobs of its own, which end with it.
pub(crate) struct Successor {
    process: OwnedHandle,
    status: Option<ExitStatus>,
}

impl Successor {
    /// Starts program (an `.exe`, opened and pinned until it runs) with args, the app's own
    /// standard output and error, and exactly the environment env.
    pub(crate) fn start(
        program: &Path,
        args: &[OsString],
        env: &[(OsString, OsString)],
    ) -> io::Result<Successor> {
        let held = open_pinned(program)?;
        let pin = pin_file(&held).map_err(io::Error::other)?;
        let mut cmd = Command::new(pin, Kind::Successor);
        cmd.args(args)
            .envs(env.iter().map(|(k, v)| (k, v)))
            .stdin(Io::Null)
            .stdout(Io::Inherit)
            .stderr(Io::Inherit);
        let c = cmd.create(0)?.resume()?;
        Ok(Successor {
            process: c.process,
            status: None,
        })
    }

    pub(crate) fn try_wait(&mut self) -> io::Result<Option<ExitStatus>> {
        if let Some(s) = self.status {
            return Ok(Some(s));
        }
        let h = self.process.as_raw_handle() as HANDLE;
        if unsafe { WaitForSingleObject(h, 0) } != WAIT_OBJECT_0 {
            return Ok(None);
        }
        let mut code = 0u32;
        if unsafe { GetExitCodeProcess(h, &mut code) } == 0 {
            return Err(io::Error::last_os_error());
        }
        let s = ExitStatus::from_raw(code);
        self.status = Some(s);
        Ok(Some(s))
    }

    /// Ends it, by the handle held, and waits until it has ended.
    pub(crate) fn end(mut self) {
        if matches!(self.try_wait(), Ok(None)) {
            let h = self.process.as_raw_handle() as HANDLE;
            unsafe {
                TerminateProcess(h, 1);
                WaitForSingleObject(h, INFINITE);
            }
        }
    }

    /// Lets it go: its handle closes, it runs on, and nothing here can reach it again.
    pub(crate) fn release(self) {
        drop(self.process);
    }
}
