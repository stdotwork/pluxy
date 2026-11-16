//! The Windows side of `instance.rs`: the profile's lock file and the named pipe on which the
//! first app hears later launches.
//!
//! The lock is a plain file in the instance folder, locked with `LockFileEx` (what `File::try_lock`
//! does): the system drops it when the holder ends, however it ends, and it holds across logon
//! sessions of one user, which a named mutex in the session's namespace would not. The folder and
//! the file are opened without following a link (a junction or symbolic link is refused).
//!
//! The pipe is `\\.\pipe\pluxy-app-<digest of the instance folder>`. Its first instance is made
//! with `FILE_FLAG_FIRST_PIPE_INSTANCE`, so a pipe someone else made under the name is refused
//! and not served. Remote clients are refused, and the pipe's access list is protected (it takes
//! none from its parent) and grants access to this user's SID alone: no other user, not even an
//! administrator without that right, can open it. A later launch opens it with
//! `SECURITY_IDENTIFICATION` (the server learns who it is, and cannot act as it) and checks, before
//! it sends anything, that the process serving the pipe runs as its own user.
//!
//! The server is one thread with overlapped operations, as the Unix one is with `poll`: at most
//! [`MAX_CONNECTIONS`] connections at once, each read for at most [`READ_BOUND`] and at most
//! [`MAX_MESSAGE`] bytes, so one that says nothing does not hold up the others; stopping sets an
//! event and waits for nothing.

pub mod fsx {
    //! The instance folder, opened without following a link.
    use crate::instance::{ADDRESS, LOCK};
    use std::fs::{File, OpenOptions};
    use std::io::{self, Read, Write};
    use std::os::windows::fs::{MetadataExt, OpenOptionsExt};
    use std::path::{Path, PathBuf};
    use windows_sys::Win32::Storage::FileSystem::{
        FILE_ATTRIBUTE_REPARSE_POINT, FILE_FLAG_OPEN_REPARSE_POINT,
    };

    /// The instance folder.
    pub struct Dir(PathBuf);

    fn is_link(m: &std::fs::Metadata) -> bool {
        m.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0
    }

    /// Makes `dir` (and any missing parent), and checks it is a folder and not a link. The folder
    /// is inside the user's profile, whose access list already keeps other users out.
    pub fn open_dir(dir: &Path) -> io::Result<Dir> {
        std::fs::create_dir_all(dir)?;
        let m = std::fs::symlink_metadata(dir)?;
        if !m.is_dir() || is_link(&m) {
            return Err(io::Error::other("not a folder"));
        }
        Ok(Dir(dir.to_path_buf()))
    }

    /// Opens `name` in `dir` as itself, not as what a link there points to, and checks it is a
    /// plain file.
    fn open_in(dir: &Dir, name: &str, write: bool, create: bool) -> io::Result<File> {
        let mut o = OpenOptions::new();
        o.read(true)
            .write(write)
            .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT);
        if create {
            o.create(true).truncate(false);
        }
        let f = o.open(dir.0.join(name))?;
        let m = f.metadata()?;
        if !m.is_file() || is_link(&m) {
            return Err(io::Error::other(format!("{name} is not a plain file")));
        }
        Ok(f)
    }

    pub fn open_lock(dir: &Dir) -> io::Result<File> {
        open_in(dir, LOCK, true, true)
    }

    /// The pipe the first app wrote down: None if it said it has none, an error if it has not
    /// written one (yet).
    pub fn read_address(dir: &Dir) -> io::Result<Option<PathBuf>> {
        let mut f = open_in(dir, ADDRESS, false, false)?;
        let mut b = Vec::new();
        (&mut f).take(4096).read_to_end(&mut b)?;
        if b.is_empty() {
            return Ok(None);
        }
        let s = String::from_utf8(b)
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "the address is not text"))?;
        Ok(Some(PathBuf::from(s)))
    }

    /// Writes the address in one step: a later launch reads all of it or none.
    pub fn write_address(dir: &Dir, pipe: &[u8]) -> io::Result<()> {
        write_file(dir, ADDRESS, pipe)
    }

    /// Writes name in dir in one step: a reader reads all of it or none.
    pub fn write_file(dir: &Dir, name: &str, bytes: &[u8]) -> io::Result<()> {
        let tmp = format!("{name}.new");
        remove(dir, &tmp)?;
        let new = dir.0.join(&tmp);
        let mut f = OpenOptions::new().write(true).create_new(true).open(&new)?;
        f.write_all(bytes)?;
        drop(f);
        std::fs::rename(&new, dir.0.join(name))
    }

    /// Up to 64 KiB of name in dir, opened as itself (not through a link); None if it is not there.
    pub fn read_file(dir: &Dir, name: &str) -> io::Result<Option<Vec<u8>>> {
        let mut f = match open_in(dir, name, false, false) {
            Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(None),
            r => r?,
        };
        let mut b = Vec::new();
        (&mut f).take(64 << 10).read_to_end(&mut b)?;
        Ok(Some(b))
    }

    /// Removes `name` from `dir`. Gone already is fine.
    pub fn remove(dir: &Dir, name: &str) -> io::Result<()> {
        match std::fs::remove_file(dir.0.join(name)) {
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(()),
            r => r,
        }
    }
}

pub mod ipc {
    use crate::instance::{MAX_CONNECTIONS, MAX_MESSAGE, MESSAGE, OK, READ_BOUND};
    use std::ffi::OsStr;
    use std::io;
    use std::os::windows::ffi::OsStrExt;
    use std::path::{Path, PathBuf};
    use std::ptr::{null, null_mut};
    use std::sync::Arc;
    use std::time::{Duration, Instant};
    use windows_sys::Win32::Foundation::{
        CloseHandle, GetLastError, LocalFree, ERROR_BROKEN_PIPE, ERROR_IO_PENDING, ERROR_PIPE_BUSY,
        ERROR_PIPE_CONNECTED, ERROR_SEM_TIMEOUT, GENERIC_READ, GENERIC_WRITE, HANDLE,
        INVALID_HANDLE_VALUE, WAIT_FAILED, WAIT_OBJECT_0, WAIT_TIMEOUT,
    };
    use windows_sys::Win32::Security::Authorization::{
        ConvertSidToStringSidW, ConvertStringSecurityDescriptorToSecurityDescriptorW,
        SDDL_REVISION_1,
    };
    use windows_sys::Win32::Security::{
        EqualSid, GetTokenInformation, TokenUser, PSECURITY_DESCRIPTOR, PSID, SECURITY_ATTRIBUTES,
        TOKEN_QUERY, TOKEN_USER,
    };
    use windows_sys::Win32::Storage::FileSystem::{
        CreateFileW, ReadFile, WriteFile, FILE_FLAG_FIRST_PIPE_INSTANCE, FILE_FLAG_OVERLAPPED,
        OPEN_EXISTING, PIPE_ACCESS_DUPLEX, SECURITY_IDENTIFICATION, SECURITY_SQOS_PRESENT,
    };
    use windows_sys::Win32::System::Pipes::{
        ConnectNamedPipe, CreateNamedPipeW, GetNamedPipeServerProcessId, WaitNamedPipeW,
        PIPE_READMODE_BYTE, PIPE_REJECT_REMOTE_CLIENTS, PIPE_TYPE_BYTE, PIPE_UNLIMITED_INSTANCES,
        PIPE_WAIT,
    };
    use windows_sys::Win32::System::Threading::{
        CreateEventW, GetCurrentProcess, OpenProcess, OpenProcessToken, ResetEvent, SetEvent,
        WaitForMultipleObjects, WaitForSingleObject, PROCESS_QUERY_LIMITED_INFORMATION,
    };
    use windows_sys::Win32::System::IO::{CancelIoEx, GetOverlappedResult, OVERLAPPED};

    /// What a pipe's name starts with: nothing else is connected to.
    const PIPE_PREFIX: &str = r"\\.\pipe\pluxy-app-";

    /// An open handle, closed when dropped.
    struct Handle(HANDLE);
    // SAFETY: a kernel handle may be used and closed from any thread.
    unsafe impl Send for Handle {}
    unsafe impl Sync for Handle {}
    impl Drop for Handle {
        fn drop(&mut self) {
            if !self.0.is_null() && self.0 != INVALID_HANDLE_VALUE {
                // SAFETY: the handle is open, and owned by this value.
                unsafe { CloseHandle(self.0) };
            }
        }
    }

    fn wide(s: &OsStr) -> Vec<u16> {
        s.encode_wide().chain(std::iter::once(0)).collect()
    }

    fn os_error(code: u32) -> io::Error {
        io::Error::from_raw_os_error(code as i32)
    }

    /// Whole milliseconds, rounded up, for a wait.
    fn millis(d: Duration) -> u32 {
        d.as_millis()
            .saturating_add(1)
            .min(u128::from(u32::MAX - 1)) as u32
    }

    /// TimedOut once `deadline` has passed.
    fn before(deadline: Instant) -> io::Result<()> {
        if Instant::now() >= deadline {
            return Err(io::ErrorKind::TimedOut.into());
        }
        Ok(())
    }

    fn event() -> io::Result<Handle> {
        // SAFETY: no pointers are passed; a manual-reset event, not signalled.
        let h = unsafe { CreateEventW(null(), 1, 0, null()) };
        if h.is_null() {
            return Err(io::Error::last_os_error());
        }
        Ok(Handle(h))
    }

    // ---- who is who ----------------------------------------------------------------------

    fn open_token(process: HANDLE) -> io::Result<Handle> {
        let mut token: HANDLE = null_mut();
        // SAFETY: token is written when this succeeds.
        if unsafe { OpenProcessToken(process, TOKEN_QUERY, &mut token) } == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(Handle(token))
    }

    /// The `TOKEN_USER` of a token, in memory aligned for it.
    fn token_user(token: &Handle) -> io::Result<Vec<u64>> {
        let mut len = 0u32;
        // SAFETY: asks for the size only; the call fails with the size in len.
        unsafe { GetTokenInformation(token.0, TokenUser, null_mut(), 0, &mut len) };
        if len == 0 {
            return Err(io::Error::last_os_error());
        }
        let mut buf = vec![0u64; (len as usize).div_ceil(8)];
        // SAFETY: buf holds at least len bytes.
        if unsafe {
            GetTokenInformation(token.0, TokenUser, buf.as_mut_ptr().cast(), len, &mut len)
        } == 0
        {
            return Err(io::Error::last_os_error());
        }
        Ok(buf)
    }

    fn sid_of(user: &[u64]) -> PSID {
        // SAFETY: user was filled in by GetTokenInformation(TokenUser), and is aligned for it.
        unsafe { (*user.as_ptr().cast::<TOKEN_USER>()).User.Sid }
    }

    /// This user's SID, as text (`S-1-5-21-..`).
    pub(super) fn own_sid() -> io::Result<String> {
        // SAFETY: the pseudo handle of this process needs no closing.
        let token = open_token(unsafe { GetCurrentProcess() })?;
        let user = token_user(&token)?;
        let mut text: *mut u16 = null_mut();
        // SAFETY: the SID is valid while user lives; text is written when this succeeds.
        if unsafe { ConvertSidToStringSidW(sid_of(&user), &mut text) } == 0 {
            return Err(io::Error::last_os_error());
        }
        let mut n = 0;
        // SAFETY: text is a NUL-terminated string the call allocated, freed below.
        let s = unsafe {
            while *text.add(n) != 0 {
                n += 1;
            }
            let s = String::from_utf16_lossy(std::slice::from_raw_parts(text, n));
            LocalFree(text.cast());
            s
        };
        Ok(s)
    }

    /// The access list the pipe is made with: this user alone, nothing inherited, and a Medium
    /// integrity label that bars lower ones (a sandboxed program of the same user) from reading
    /// or writing it.
    struct Security {
        sd: PSECURITY_DESCRIPTOR,
        attrs: SECURITY_ATTRIBUTES,
    }
    // SAFETY: the descriptor is plain memory, read only once made.
    unsafe impl Send for Security {}
    impl Security {
        fn this_user() -> io::Result<Security> {
            let sddl = wide(OsStr::new(&format!(
                "D:P(A;;GA;;;{})S:(ML;;NRNW;;;ME)",
                own_sid()?
            )));
            let mut sd: PSECURITY_DESCRIPTOR = null_mut();
            // SAFETY: sd is written, and freed by Drop, when this succeeds.
            if unsafe {
                ConvertStringSecurityDescriptorToSecurityDescriptorW(
                    sddl.as_ptr(),
                    SDDL_REVISION_1,
                    &mut sd,
                    null_mut(),
                )
            } == 0
            {
                return Err(io::Error::last_os_error());
            }
            Ok(Security {
                sd,
                attrs: SECURITY_ATTRIBUTES {
                    nLength: std::mem::size_of::<SECURITY_ATTRIBUTES>() as u32,
                    lpSecurityDescriptor: sd,
                    bInheritHandle: 0,
                },
            })
        }
    }
    impl Drop for Security {
        fn drop(&mut self) {
            // SAFETY: sd was allocated by the conversion, and nothing uses it after this.
            unsafe { LocalFree(self.sd) };
        }
    }

    /// A pipe instance of this name: the first of the name when `first`, which fails if the name
    /// is taken.
    fn create_instance(name: &[u16], sec: &Security, first: bool) -> io::Result<Handle> {
        let mut open = PIPE_ACCESS_DUPLEX | FILE_FLAG_OVERLAPPED;
        if first {
            open |= FILE_FLAG_FIRST_PIPE_INSTANCE;
        }
        // SAFETY: name is NUL-terminated; sec's descriptor lives as long as sec.
        let h = unsafe {
            CreateNamedPipeW(
                name.as_ptr(),
                open,
                PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT | PIPE_REJECT_REMOTE_CLIENTS,
                PIPE_UNLIMITED_INSTANCES,
                512,
                512,
                0,
                &sec.attrs,
            )
        };
        if h == INVALID_HANDLE_VALUE {
            return Err(io::Error::last_os_error());
        }
        Ok(Handle(h))
    }

    // ---- one overlapped operation --------------------------------------------------------

    enum Step {
        /// It ended at once, having moved this many bytes.
        Done(usize),
        /// It runs; its event is signalled when it ends.
        Pending,
    }

    /// An overlapped operation on a pipe: the event that says it ended and the structure it names.
    /// Dropping it cancels the operation and waits for the cancel, so what it names is free again.
    struct Op {
        pipe: HANDLE,
        ev: Handle,
        ov: Box<OVERLAPPED>,
        pending: bool,
    }
    // SAFETY: the raw handle is used by the thread that owns the Op.
    unsafe impl Send for Op {}

    impl Op {
        fn new(pipe: HANDLE) -> io::Result<Op> {
            Ok(Op {
                pipe,
                ev: event()?,
                // SAFETY: an all-zero OVERLAPPED is how one starts.
                ov: Box::new(unsafe { std::mem::zeroed() }),
                pending: false,
            })
        }

        fn arm(&mut self) {
            // SAFETY: the event is open; the previous operation ended (or was cancelled).
            unsafe { ResetEvent(self.ev.0) };
            // SAFETY: as above.
            *self.ov = unsafe { std::mem::zeroed() };
            self.ov.hEvent = self.ev.0;
        }

        fn started(&mut self, ok: i32, moved: u32, already: Option<u32>) -> io::Result<Step> {
            if ok != 0 {
                return Ok(Step::Done(moved as usize));
            }
            // SAFETY: reads the thread's last error.
            let e = unsafe { GetLastError() };
            if e == ERROR_IO_PENDING {
                self.pending = true;
                Ok(Step::Pending)
            } else if Some(e) == already {
                Ok(Step::Done(0))
            } else {
                Err(os_error(e))
            }
        }

        fn read(&mut self, buf: &mut [u8]) -> io::Result<Step> {
            self.arm();
            let mut moved = 0;
            // SAFETY: buf and self.ov stay where they are until the operation ends: the Op waits
            // for it in cancel (called on drop) and the callers keep buf alive until then.
            let ok = unsafe {
                ReadFile(
                    self.pipe,
                    buf.as_mut_ptr(),
                    buf.len() as u32,
                    &mut moved,
                    &mut *self.ov,
                )
            };
            self.started(ok, moved, None)
        }

        fn write(&mut self, buf: &[u8]) -> io::Result<Step> {
            self.arm();
            let mut moved = 0;
            // SAFETY: as in read.
            let ok = unsafe {
                WriteFile(
                    self.pipe,
                    buf.as_ptr(),
                    buf.len() as u32,
                    &mut moved,
                    &mut *self.ov,
                )
            };
            self.started(ok, moved, None)
        }

        /// Waits for a client; Done at once if one is connected already.
        fn connect(&mut self) -> io::Result<Step> {
            self.arm();
            // SAFETY: as in read.
            let ok = unsafe { ConnectNamedPipe(self.pipe, &mut *self.ov) };
            self.started(ok, 0, Some(ERROR_PIPE_CONNECTED))
        }

        /// Whether the operation has ended.
        fn ended(&self) -> bool {
            // SAFETY: the event is open.
            unsafe { WaitForSingleObject(self.ev.0, 0) == WAIT_OBJECT_0 }
        }

        /// The result of an operation that has ended.
        fn finish(&mut self) -> io::Result<usize> {
            let mut moved = 0;
            // SAFETY: the operation was started on this pipe with this structure.
            let ok = unsafe { GetOverlappedResult(self.pipe, &*self.ov, &mut moved, 0) };
            self.pending = false;
            if ok == 0 {
                return Err(io::Error::last_os_error());
            }
            Ok(moved as usize)
        }

        /// Waits for the operation until `deadline`; cancels it, and says TimedOut, if it has
        /// not ended by then.
        fn wait(&mut self, deadline: Instant) -> io::Result<usize> {
            let left = deadline.saturating_duration_since(Instant::now());
            // SAFETY: the event is open.
            match unsafe { WaitForSingleObject(self.ev.0, millis(left)) } {
                WAIT_OBJECT_0 => self.finish(),
                WAIT_TIMEOUT => {
                    self.cancel();
                    Err(io::ErrorKind::TimedOut.into())
                }
                _ => {
                    let e = io::Error::last_os_error();
                    self.cancel();
                    Err(e)
                }
            }
        }

        fn cancel(&mut self) {
            if self.pending {
                let mut moved = 0;
                // SAFETY: the operation is running on this pipe with this structure; after the
                // cancel, waiting for its result is waiting for it to be over.
                unsafe {
                    CancelIoEx(self.pipe, &*self.ov);
                    GetOverlappedResult(self.pipe, &*self.ov, &mut moved, 1);
                }
                self.pending = false;
            }
        }
    }
    impl Drop for Op {
        fn drop(&mut self) {
            self.cancel();
        }
    }

    // ---- the first app: serving the pipe --------------------------------------------------

    /// A pipe made and waiting to be served: its first instance, which holds the name.
    pub struct Listener {
        first: Handle,
        name: Vec<u16>,
        sec: Security,
    }

    /// A pipe being served, and the way to stop serving it.
    pub struct Listening {
        pub socket: PathBuf,
        stop: Arc<Handle>,
    }

    impl Listening {
        /// Stops the serving thread (an event: nothing to wait on).
        pub fn stop(&self) {
            // SAFETY: the event is open while this value or the thread holds it.
            unsafe { SetEvent(self.stop.0) };
        }
    }

    /// Makes `pipe` (a name in `\\.\pipe`), as the first instance of it, for this user only.
    /// `dir` is the pipes' folder, which nothing is made in.
    pub fn listen(_dir: &Path, pipe: &Path) -> io::Result<Listener> {
        let sec = Security::this_user()?;
        let name = wide(pipe.as_os_str());
        let first = create_instance(&name, &sec, true)?;
        Ok(Listener { first, name, sec })
    }

    /// A connection being read: one message of at most [`MAX_MESSAGE`] bytes within [`READ_BOUND`].
    struct Conn {
        /// Before `pipe`: dropped first, cancelling its read while the pipe is open.
        op: Op,
        pipe: Handle,
        buf: [u8; MAX_MESSAGE],
        n: usize,
        deadline: Instant,
        started: bool,
    }

    enum Got {
        More,
        Done,
        Message,
    }

    impl Conn {
        fn new(pipe: Handle) -> io::Result<Conn> {
            Ok(Conn {
                op: Op::new(pipe.0)?,
                pipe,
                buf: [0; MAX_MESSAGE],
                n: 0,
                deadline: Instant::now() + READ_BOUND,
                started: false,
            })
        }

        /// Takes what the last read got (if one ended) and reads on: whether to wait for more,
        /// drop the connection, or act on the message.
        fn pump(&mut self) -> Got {
            let mut ended = self.started && self.op.ended();
            self.started = true;
            loop {
                let got = if ended {
                    ended = false;
                    self.op.finish()
                } else if self.op.pending {
                    return Got::More;
                } else {
                    match self.op.read(&mut self.buf[self.n..]) {
                        Ok(Step::Done(k)) => Ok(k),
                        Ok(Step::Pending) => return Got::More,
                        Err(e) => Err(e),
                    }
                };
                match got {
                    Ok(0) | Err(_) => return Got::Done,
                    Ok(k) => self.n += k,
                }
                let got = &self.buf[..self.n];
                if got.contains(&b'\n') || self.n == MAX_MESSAGE {
                    return if got == MESSAGE {
                        Got::Message
                    } else {
                        Got::Done
                    };
                }
            }
        }
    }

    /// A pipe instance waiting for a client.
    struct Connecting {
        /// Before `pipe`, as in [`Conn`].
        op: Op,
        pipe: Handle,
    }

    enum Started {
        Connected(Handle),
        Waiting(Connecting),
    }

    fn start_connecting(pipe: Handle) -> io::Result<Started> {
        let mut op = Op::new(pipe.0)?;
        match op.connect()? {
            Step::Done(_) => Ok(Started::Connected(pipe)),
            Step::Pending => Ok(Started::Waiting(Connecting { op, pipe })),
        }
    }

    /// Makes pipe instances until one waits for a client (a client that is connected already is
    /// taken into `conns` on the way), so that one is always listening while the others are
    /// served. False if one could not be made.
    // The connections are boxed on purpose: a read in flight names each one's buffer.
    #[allow(clippy::vec_box)]
    fn listening(
        spare: &mut Option<Handle>,
        name: &[u16],
        sec: &Security,
        conns: &mut Vec<Box<Conn>>,
        connecting: &mut Option<Connecting>,
    ) -> bool {
        while connecting.is_none() && conns.len() < MAX_CONNECTIONS {
            let pipe = match spare.take() {
                Some(p) => Ok(p),
                None => create_instance(name, sec, false),
            };
            match pipe.and_then(start_connecting) {
                Ok(Started::Connected(p)) => match Conn::new(p) {
                    Ok(c) => conns.push(Box::new(c)),
                    Err(_) => return false,
                },
                Ok(Started::Waiting(c)) => *connecting = Some(c),
                Err(_) => return false,
            }
        }
        true
    }

    impl Listener {
        /// Serves the pipe on a thread of its own until [`Listening::stop`].
        pub fn serve(
            self,
            pipe: PathBuf,
            on_activate: impl Fn() -> bool + Send + 'static,
        ) -> io::Result<Listening> {
            let stop = Arc::new(event()?);
            let theirs = stop.clone();
            std::thread::Builder::new()
                .name("instance-pipe".into())
                .spawn(move || self.run(&theirs, on_activate))?;
            Ok(Listening { socket: pipe, stop })
        }

        fn run(self, stop: &Handle, on_activate: impl Fn() -> bool) {
            let Listener { first, name, sec } = self;
            let mut spare = Some(first);
            // Boxed: a read in flight names the connection's buffer, which must stay where it is
            // while the vector grows, shifts and swaps its entries.
            let mut conns: Vec<Box<Conn>> = Vec::new();
            let mut connecting: Option<Connecting> = None;
            loop {
                let now = Instant::now();
                conns.retain(|c| c.deadline > now);
                // A pipe instance waits for a client before anything else, and again as soon as
                // it has taken one, before that one is read and the window is raised: with none,
                // a launch finds every instance busy, or no pipe, or another program could make
                // the name.
                let mut failed = !listening(&mut spare, &name, &sec, &mut conns, &mut connecting);
                if connecting.as_ref().is_some_and(|c| c.op.ended()) {
                    let c = connecting.take().expect("checked above");
                    let Connecting { mut op, pipe } = c;
                    if op.finish().is_ok() {
                        // Only this user's connections are read at all: the pipe's access list
                        // keeps every other one out.
                        match Conn::new(pipe) {
                            Ok(c) => conns.push(Box::new(c)),
                            Err(_) => failed = true,
                        }
                    } else {
                        failed = true;
                    }
                    failed |= !listening(&mut spare, &name, &sec, &mut conns, &mut connecting);
                }
                let mut i = conns.len();
                while i > 0 {
                    i -= 1;
                    match conns[i].pump() {
                        Got::More => {}
                        Got::Done => {
                            conns.swap_remove(i);
                        }
                        Got::Message => {
                            let c = conns.swap_remove(i);
                            let ok = on_activate();
                            let _ = reply(&c.pipe, if ok { OK } else { b"no\n" });
                        }
                    }
                }
                // Served some, and room for more: a pipe instance is made again at the top.
                if !failed && connecting.is_none() && conns.len() < MAX_CONNECTIONS {
                    // SAFETY: the event is open while the thread runs.
                    if unsafe { WaitForSingleObject(stop.0, 0) } == WAIT_OBJECT_0 {
                        return;
                    }
                    continue;
                }
                let mut handles = vec![stop.0];
                if let Some(c) = &connecting {
                    handles.push(c.op.ev.0);
                }
                handles.extend(conns.iter().map(|c| c.op.ev.0));
                let timeout = conns
                    .iter()
                    .map(|c| millis(c.deadline.saturating_duration_since(now)))
                    .min()
                    .unwrap_or(u32::MAX);
                // A pipe that could not be made is tried again soon, not in a spin.
                let timeout = if failed { timeout.min(100) } else { timeout };
                // SAFETY: handles are open events, at most 34 of them.
                let r = unsafe {
                    WaitForMultipleObjects(handles.len() as u32, handles.as_ptr(), 0, timeout)
                };
                if r == WAIT_OBJECT_0 {
                    return;
                }
                if r == WAIT_FAILED {
                    std::thread::sleep(Duration::from_millis(50));
                }
            }
        }
    }

    /// Writes the answer to a client within [`READ_BOUND`].
    fn reply(pipe: &Handle, what: &[u8]) -> io::Result<()> {
        let mut op = Op::new(pipe.0)?;
        match op.write(what)? {
            Step::Done(_) => Ok(()),
            Step::Pending => op.wait(Instant::now() + READ_BOUND).map(|_| ()),
        }
    }

    // ---- a later launch ------------------------------------------------------------------

    /// Opens the pipe within `deadline`. A pipe with no instance listening is waited for; one that
    /// does not exist is an error at once (NotFound), for the caller to try again.
    fn open(pipe: &Path, deadline: Instant) -> io::Result<Handle> {
        let name = wide(pipe.as_os_str());
        loop {
            before(deadline)?;
            // SAFETY: name is NUL-terminated.
            let h = unsafe {
                CreateFileW(
                    name.as_ptr(),
                    GENERIC_READ | GENERIC_WRITE,
                    0,
                    null(),
                    OPEN_EXISTING,
                    FILE_FLAG_OVERLAPPED | SECURITY_SQOS_PRESENT | SECURITY_IDENTIFICATION,
                    null_mut(),
                )
            };
            if h != INVALID_HANDLE_VALUE {
                return Ok(Handle(h));
            }
            // SAFETY: reads the thread's last error.
            let e = unsafe { GetLastError() };
            if e != ERROR_PIPE_BUSY {
                return Err(os_error(e));
            }
            let left = deadline.saturating_duration_since(Instant::now());
            let wait = millis(left.min(Duration::from_millis(500)));
            // SAFETY: name is NUL-terminated.
            if unsafe { WaitNamedPipeW(name.as_ptr(), wait) } == 0 {
                // SAFETY: reads the thread's last error.
                let e = unsafe { GetLastError() };
                if e != ERROR_SEM_TIMEOUT {
                    return Err(os_error(e));
                }
            }
        }
    }

    /// Refuses a pipe whose server is not a process of this user's.
    fn check_server(pipe: &Handle) -> io::Result<()> {
        let mut pid = 0u32;
        // SAFETY: pid is written when this succeeds.
        if unsafe { GetNamedPipeServerProcessId(pipe.0, &mut pid) } == 0 {
            return Err(io::Error::last_os_error());
        }
        // SAFETY: no pointers.
        let process = unsafe { OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, pid) };
        if process.is_null() {
            return Err(io::Error::last_os_error());
        }
        let process = Handle(process);
        let theirs = token_user(&open_token(process.0)?)?;
        // SAFETY: the pseudo handle of this process needs no closing.
        let mine = token_user(&open_token(unsafe { GetCurrentProcess() })?)?;
        // SAFETY: both SIDs are valid while theirs and mine live.
        if unsafe { EqualSid(sid_of(&theirs), sid_of(&mine)) } == 0 {
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                "the pipe is another user's",
            ));
        }
        Ok(())
    }

    /// Whether `pipe` is a name this app makes: nothing else is connected to.
    fn ours(pipe: &Path) -> bool {
        pipe.to_str()
            .and_then(|s| s.strip_prefix(PIPE_PREFIX))
            .is_some_and(|rest| !rest.is_empty() && rest.bytes().all(|b| b.is_ascii_hexdigit()))
    }

    /// Asks the app on the pipe to raise its window; Ok once it has answered `ok`, all by
    /// `deadline`: nothing is started after it (connecting, each write and read), and an answer
    /// read after it is not taken.
    pub fn activate(pipe: &Path, deadline: Instant) -> io::Result<()> {
        before(deadline)?;
        if !ours(pipe) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "the address is not one of this app's pipes",
            ));
        }
        let h = open(pipe, deadline)?;
        // Only an app of this user's is told anything.
        check_server(&h)?;
        let mut op = Op::new(h.0)?;
        let mut sent = 0;
        while sent < MESSAGE.len() {
            before(deadline)?;
            sent += match op.write(&MESSAGE[sent..])? {
                Step::Done(k) => k,
                Step::Pending => op.wait(deadline)?,
            };
        }
        let mut reply = [0u8; 3];
        let mut got = 0;
        while got < reply.len() {
            before(deadline)?;
            let k = match op.read(&mut reply[got..]) {
                Ok(Step::Done(k)) => k,
                Ok(Step::Pending) => op.wait(deadline)?,
                Err(e) if e.raw_os_error() == Some(ERROR_BROKEN_PIPE as i32) => 0,
                Err(e) => return Err(e),
            };
            if k == 0 {
                return Err(io::ErrorKind::UnexpectedEof.into());
            }
            got += k;
        }
        if reply == OK {
            before(deadline)
        } else {
            Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "its window could not be shown",
            ))
        }
    }

    #[cfg(test)]
    mod tests {
        use super::*;
        use crate::instance::{socket_dirs, start, start_by, Instance, Start, ADDRESS};
        use std::fs;
        use std::io::{Read, Write};
        use std::sync::atomic::{AtomicUsize, Ordering};
        use windows_sys::Win32::Security::Authorization::{
            ConvertSecurityDescriptorToStringSecurityDescriptorW, GetSecurityInfo, SE_KERNEL_OBJECT,
        };
        use windows_sys::Win32::Security::{
            GetAce, GetSecurityDescriptorSacl, GetSidIdentifierAuthority, GetSidSubAuthority,
            GetSidSubAuthorityCount, IsValidSid, ACL, DACL_SECURITY_INFORMATION,
            LABEL_SECURITY_INFORMATION, SECURITY_MANDATORY_LABEL_AUTHORITY,
            SYSTEM_MANDATORY_LABEL_ACE,
        };
        use windows_sys::Win32::System::SystemServices::{
            SECURITY_MANDATORY_MEDIUM_RID, SYSTEM_MANDATORY_LABEL_ACE_TYPE,
            SYSTEM_MANDATORY_LABEL_NO_READ_UP, SYSTEM_MANDATORY_LABEL_NO_WRITE_UP,
        };

        struct Temp(PathBuf);
        impl Drop for Temp {
            fn drop(&mut self) {
                let _ = fs::remove_dir_all(&self.0);
            }
        }
        fn temp() -> Temp {
            let d = std::env::temp_dir().join(format!(
                "pluxy-app-inst-{}",
                &crate::managed::random_key()[..12]
            ));
            fs::create_dir_all(&d).unwrap();
            Temp(d)
        }

        fn first(dir: &Path) -> Instance {
            match start(dir, Duration::from_secs(1)).unwrap() {
                Start::First(i) => i,
                Start::Raised => panic!("expected to be the first"),
            }
        }

        /// An instance that counts the activations it gets.
        fn counting(dir: &Path) -> (Instance, Arc<AtomicUsize>) {
            let i = first(dir);
            let n = Arc::new(AtomicUsize::new(0));
            let c = n.clone();
            i.listen(&socket_dirs(), move || {
                c.fetch_add(1, Ordering::SeqCst);
                true
            })
            .unwrap();
            (i, n)
        }

        fn pipe_of(i: &Instance) -> PathBuf {
            i.socket().unwrap().to_path_buf()
        }

        /// Runs `f` on a thread of its own; its result, or None if it did not end within `bound`.
        fn within<T: Send + 'static>(
            bound: Duration,
            f: impl FnOnce() -> T + Send + 'static,
        ) -> Option<T> {
            let (tx, rx) = std::sync::mpsc::channel();
            std::thread::spawn(move || {
                let _ = tx.send(f());
            });
            rx.recv_timeout(bound).ok()
        }

        /// A security descriptor's access list as SDDL text.
        fn sddl_of(sd: PSECURITY_DESCRIPTOR, info: u32) -> String {
            let mut text: *mut u16 = null_mut();
            // SAFETY: sd is a valid descriptor; text is written, and freed below, on success.
            let ok = unsafe {
                ConvertSecurityDescriptorToStringSecurityDescriptorW(
                    sd,
                    SDDL_REVISION_1,
                    info,
                    &mut text,
                    null_mut(),
                )
            };
            assert_ne!(ok, 0, "{}", io::Error::last_os_error());
            // SAFETY: text is a NUL-terminated string the call allocated.
            unsafe {
                let mut n = 0;
                while *text.add(n) != 0 {
                    n += 1;
                }
                let s = String::from_utf16_lossy(std::slice::from_raw_parts(text, n));
                LocalFree(text.cast());
                s
            }
        }

        /// What the system holds of an open pipe's security (`info`), as SDDL text.
        fn security_of(pipe: HANDLE, info: u32) -> String {
            let mut sd: PSECURITY_DESCRIPTOR = null_mut();
            // SAFETY: sd is written, and freed below, when this succeeds.
            let r = unsafe {
                GetSecurityInfo(
                    pipe,
                    SE_KERNEL_OBJECT,
                    info,
                    null_mut(),
                    null_mut(),
                    null_mut(),
                    null_mut(),
                    &mut sd,
                )
            };
            assert_eq!(r, 0, "GetSecurityInfo failed with {r}");
            let s = sddl_of(sd, info);
            // SAFETY: sd was allocated by GetSecurityInfo.
            unsafe { LocalFree(sd) };
            s
        }

        /// The integrity label an open pipe carries, read through the API: its entry count, then
        /// the first entry's type, mask, SID authority and sub-authorities.
        fn label_of(pipe: HANDLE) -> (u16, u8, u32, [u8; 6], Vec<u32>) {
            let mut sd: PSECURITY_DESCRIPTOR = null_mut();
            // SAFETY: sd is written, and freed below, when this succeeds.
            let r = unsafe {
                GetSecurityInfo(
                    pipe,
                    SE_KERNEL_OBJECT,
                    LABEL_SECURITY_INFORMATION,
                    null_mut(),
                    null_mut(),
                    null_mut(),
                    null_mut(),
                    &mut sd,
                )
            };
            assert_eq!(r, 0, "GetSecurityInfo failed with {r}");
            let (mut present, mut defaulted) = (0, 0);
            let mut acl: *mut ACL = null_mut();
            // SAFETY: sd is a valid descriptor; the outputs are written.
            let ok =
                unsafe { GetSecurityDescriptorSacl(sd, &mut present, &mut acl, &mut defaulted) };
            assert!(
                ok != 0 && present != 0 && !acl.is_null(),
                "the pipe has no label"
            );
            // SAFETY: acl points into sd, which lives until the LocalFree below; an entry's
            // pointer and its SID stay inside the list.
            let found = unsafe {
                let count = (*acl).AceCount;
                let mut ace: *mut core::ffi::c_void = null_mut();
                assert_ne!(
                    GetAce(acl, 0, &mut ace),
                    0,
                    "{}",
                    io::Error::last_os_error()
                );
                let ace = ace.cast::<SYSTEM_MANDATORY_LABEL_ACE>();
                let sid: PSID = std::ptr::addr_of_mut!((*ace).SidStart).cast();
                assert_ne!(IsValidSid(sid), 0, "the label's SID is not valid");
                let authority = (*GetSidIdentifierAuthority(sid)).Value;
                let subs = (0..u32::from(*GetSidSubAuthorityCount(sid)))
                    .map(|n| *GetSidSubAuthority(sid, n))
                    .collect();
                (count, (*ace).Header.AceType, (*ace).Mask, authority, subs)
            };
            // SAFETY: sd was allocated by GetSecurityInfo.
            unsafe { LocalFree(sd) };
            found
        }

        /// A connection to the pipe as any program could make it, with no message sent. Like a
        /// real client, it waits when every instance is busy: the server makes the next one
        /// just after it takes a client, so a burst of connections can arrive in between.
        fn raw(pipe: &Path) -> fs::File {
            let deadline = Instant::now() + Duration::from_secs(10);
            loop {
                match fs::OpenOptions::new().read(true).write(true).open(pipe) {
                    Ok(f) => return f,
                    Err(e)
                        if e.raw_os_error() == Some(ERROR_PIPE_BUSY as i32)
                            && Instant::now() < deadline =>
                    {
                        std::thread::sleep(Duration::from_millis(5));
                    }
                    Err(e) => panic!("{}: {e}", pipe.display()),
                }
            }
        }

        #[test]
        fn the_second_launch_is_refused_the_lock_and_raises_the_first() {
            let t = temp();
            let dir = t.0.join("instance");
            let (_first, raised) = counting(&dir);
            for want in [1, 2] {
                assert!(matches!(
                    start(&dir, Duration::from_secs(5)).unwrap(),
                    Start::Raised
                ));
                assert_eq!(raised.load(Ordering::SeqCst), want);
            }
        }

        #[test]
        fn another_profile_is_its_own_instance() {
            let t = temp();
            let (a, na) = counting(&t.0.join("a"));
            let (b, nb) = counting(&t.0.join("b"));
            assert_ne!(pipe_of(&a), pipe_of(&b));
            assert!(matches!(
                start(&t.0.join("a"), Duration::from_secs(5)).unwrap(),
                Start::Raised
            ));
            assert_eq!(
                (na.load(Ordering::SeqCst), nb.load(Ordering::SeqCst)),
                (1, 0)
            );
        }

        #[test]
        fn a_holder_that_does_not_answer_is_an_error_not_a_second_instance() {
            let t = temp();
            let dir = t.0.join("instance");
            let _held = first(&dir); // holds the lock, never listens
            let e = start(&dir, Duration::from_millis(400)).err().unwrap();
            assert!(e.contains("did not answer"), "{e}");
        }

        #[test]
        fn the_lock_and_the_pipe_are_free_again_when_the_holder_is_dropped() {
            let t = temp();
            let dir = t.0.join("instance");
            let (i, _) = counting(&dir);
            let pipe = pipe_of(&i);
            assert!(dir.join(ADDRESS).exists());
            drop(i);
            assert!(
                !dir.join(ADDRESS).exists(),
                "the address goes with its owner"
            );
            // The same name is made again as the first of its name: nothing of the old pipe is
            // left, once the serving thread has seen the stop.
            let again = first(&dir);
            let mut made = false;
            for _ in 0..50 {
                if again.listen(&socket_dirs(), || true).is_ok() {
                    made = true;
                    break;
                }
                std::thread::sleep(Duration::from_millis(100));
            }
            assert!(made, "the pipe {} stayed after its owner", pipe.display());
        }

        /// The pipe's access list is this user's alone, and nothing is inherited.
        #[test]
        fn the_pipe_is_for_this_user_only() {
            let t = temp();
            let (i, _) = counting(&t.0.join("instance"));
            let name = wide(pipe_of(&i).as_os_str());
            // SAFETY: name is NUL-terminated.
            let h = unsafe {
                CreateFileW(
                    name.as_ptr(),
                    GENERIC_READ,
                    0,
                    null(),
                    OPEN_EXISTING,
                    SECURITY_SQOS_PRESENT | SECURITY_IDENTIFICATION,
                    null_mut(),
                )
            };
            assert_ne!(h, INVALID_HANDLE_VALUE, "{}", io::Error::last_os_error());
            let h = Handle(h);
            let sddl = security_of(h.0, DACL_SECURITY_INFORMATION);
            // The user's own SID, as SDDL writes it (the built-in Administrator is "LA"): the
            // same text as an access list made for this user by the app's own code.
            let reference = sddl_of(Security::this_user().unwrap().sd, DACL_SECURITY_INFORMATION);
            let trustee = |s: &str| {
                s.trim_end_matches(')')
                    .rsplit(';')
                    .next()
                    .unwrap()
                    .to_owned()
            };
            assert!(sddl.starts_with("D:P("), "the access list inherits: {sddl}");
            assert_eq!(sddl.matches('(').count(), 1, "more than one entry: {sddl}");
            assert!(sddl.starts_with("D:P(A;"), "{sddl}");
            assert_eq!(
                trustee(&sddl),
                trustee(&reference),
                "{sddl} is not this user's alone ({reference})"
            );
            // The pipe carries its integrity label: one entry, a mandatory label that bars lower
            // levels from reading and writing, at the Medium level. Read through the API, not
            // from the SDDL text, which the system writes with flags and an order of its own.
            let (count, kind, mask, authority, subs) = label_of(h.0);
            assert_eq!(count, 1, "more than one entry in the label list");
            assert_eq!(
                u32::from(kind),
                SYSTEM_MANDATORY_LABEL_ACE_TYPE,
                "not a label entry"
            );
            assert_eq!(
                mask,
                SYSTEM_MANDATORY_LABEL_NO_WRITE_UP | SYSTEM_MANDATORY_LABEL_NO_READ_UP,
                "the label does not bar lower levels from reading and writing"
            );
            assert_eq!(authority, SECURITY_MANDATORY_LABEL_AUTHORITY.Value);
            assert_eq!(
                subs,
                [SECURITY_MANDATORY_MEDIUM_RID as u32],
                "not the Medium level"
            );
            // The positive control on the same route: the user's own launch gets through.
            drop(h);
            assert!(matches!(
                start(&t.0.join("instance"), Duration::from_secs(5)).unwrap(),
                Start::Raised
            ));
        }

        /// Someone else's pipe under the name is not served, and not connected to: the first app
        /// makes its pipe as the first instance of its name, and fails if it is not.
        #[test]
        fn a_pipe_that_was_there_first_is_not_taken() {
            let t = temp();
            let dir = t.0.join("instance");
            let i = first(&dir);
            let name = PathBuf::from(r"\\.\pipe").join(crate::instance::socket_name(&i.key));
            let squat = Security::this_user().unwrap();
            let _held = create_instance(&wide(name.as_os_str()), &squat, true).unwrap();
            let e = i.listen(&socket_dirs(), || true).err().unwrap();
            assert!(
                i.socket().is_none(),
                "served a pipe it did not make first: {e}"
            );
            // A later launch is told at once that the window cannot be raised.
            let t0 = Instant::now();
            let e = start(&dir, Duration::from_secs(10)).err().unwrap();
            assert!(e.contains("cannot be raised"), "{e}");
            assert!(t0.elapsed() < Duration::from_secs(2), "{:?}", t0.elapsed());
        }

        /// What the first app does with bytes that are not the message: nothing, and it goes on
        /// serving.
        #[test]
        fn anything_but_the_message_is_ignored_and_bounded() {
            let t = temp();
            let dir = t.0.join("instance");
            let (i, n) = counting(&dir);
            let pipe = pipe_of(&i);
            let ignored = |bytes: Vec<u8>| {
                let p = pipe.clone();
                let out = within(Duration::from_secs(10), move || {
                    let mut f = raw(&p);
                    let _ = f.write_all(&bytes);
                    let mut out = Vec::new();
                    let _ = f.read_to_end(&mut out);
                    out
                })
                .expect("the first app held a bad connection open");
                assert!(out.is_empty(), "answered with {out:?}");
            };
            ignored(b"pluxy-activate 2\n".to_vec());
            ignored(b"pluxy-activate 1\nextra\n".to_vec());
            ignored(b"\n".to_vec());
            ignored(vec![b'x'; 500]); // more than it reads, with no newline
            let mut long = MESSAGE.to_vec();
            long.extend_from_slice(&[b'y'; 200]);
            ignored(long);
            assert_eq!(n.load(Ordering::SeqCst), 0);
            // Half a message, never finished: dropped after the read bound, not waited on for ever.
            let t0 = Instant::now();
            let p = pipe.clone();
            within(Duration::from_secs(10), move || {
                let mut f = raw(&p);
                f.write_all(&MESSAGE[..8]).unwrap();
                let mut out = Vec::new();
                let _ = f.read_to_end(&mut out);
                assert!(out.is_empty());
            })
            .expect("a half message was waited on for ever");
            assert!(t0.elapsed() < READ_BOUND + Duration::from_secs(2));
            assert_eq!(n.load(Ordering::SeqCst), 0);
            // And the real message still works.
            assert!(matches!(
                start(&dir, Duration::from_secs(5)).unwrap(),
                Start::Raised
            ));
            assert_eq!(n.load(Ordering::SeqCst), 1);
        }

        /// Clients that connect and say nothing do not keep a real launch waiting on them.
        #[test]
        fn silent_clients_do_not_hold_up_a_launch() {
            let t = temp();
            let dir = t.0.join("instance");
            let (i, n) = counting(&dir);
            let pipe = pipe_of(&i);
            let _silent: Vec<_> = (0..11).map(|_| raw(&pipe)).collect();
            let r = start(&dir, Duration::from_millis(1500));
            assert!(matches!(r, Ok(Start::Raised)), "{:?}", r.err());
            assert_eq!(n.load(Ordering::SeqCst), 1);
        }

        /// Reads in flight name each connection's buffer: many connections, some part way through
        /// the message, come and go (the list of them grows past its capacity, entries are
        /// swapped out, and expired ones are removed) while the others finish, and each message
        /// that is finished is answered and nothing else activates.
        #[test]
        fn connections_that_come_and_go_keep_their_reads() {
            let t = temp();
            let dir = t.0.join("instance");
            let (i, n) = counting(&dir);
            let pipe = pipe_of(&i);
            // Three have said part of the message, three part of something else; fourteen say
            // nothing, which takes the list past its capacity of 16 on the way.
            let begun: Vec<fs::File> = (0..3)
                .map(|_| {
                    let mut f = raw(&pipe);
                    f.write_all(&MESSAGE[..8]).unwrap();
                    f
                })
                .collect();
            let junk: Vec<fs::File> = (0..3)
                .map(|_| {
                    let mut f = raw(&pipe);
                    f.write_all(b"xxxxxxxx").unwrap();
                    f
                })
                .collect();
            let silent: Vec<fs::File> = (0..14).map(|_| raw(&pipe)).collect();
            // Some of the silent ones leave, so entries are swapped out under the others.
            let (leaving, _staying): (Vec<_>, Vec<_>) = silent
                .into_iter()
                .enumerate()
                .partition(|(k, _)| k % 2 == 0);
            drop(leaving);
            for mut f in junk {
                f.write_all(b"yyyy\n").unwrap();
                let mut out = Vec::new();
                let _ = f.read_to_end(&mut out);
                assert!(out.is_empty(), "junk was answered with {out:?}");
            }
            assert_eq!(
                n.load(Ordering::SeqCst),
                0,
                "something but a message activated"
            );
            for mut f in begun {
                f.write_all(&MESSAGE[8..]).unwrap();
                let mut answer = [0u8; 3];
                f.read_exact(&mut answer).unwrap();
                assert_eq!(&answer, OK);
            }
            assert_eq!(n.load(Ordering::SeqCst), 3);
            // The ones that said nothing are dropped after the read bound, and a launch still
            // raises the first app.
            std::thread::sleep(READ_BOUND + Duration::from_millis(500));
            assert!(matches!(
                start(&dir, Duration::from_secs(5)).unwrap(),
                Start::Raised
            ));
            assert_eq!(n.load(Ordering::SeqCst), 4);
        }

        /// While the window is being raised for one launch (which can take long), a pipe
        /// instance is listening: another launch is connected at once, not found a pipe with
        /// every instance busy, and is answered when the first is done.
        #[test]
        fn a_pipe_instance_listens_while_a_window_is_being_raised() {
            let t = temp();
            let dir = t.0.join("instance");
            let i = first(&dir);
            let entered = Arc::new(AtomicUsize::new(0));
            let release = Arc::new(AtomicUsize::new(0));
            let (e, r) = (entered.clone(), release.clone());
            i.listen(&socket_dirs(), move || {
                e.fetch_add(1, Ordering::SeqCst);
                while r.load(Ordering::SeqCst) == 0 {
                    std::thread::sleep(Duration::from_millis(10));
                }
                true
            })
            .unwrap();
            let pipe = pipe_of(&i);
            let d = dir.clone();
            let a = std::thread::spawn(move || start(&d, Duration::from_secs(30)).unwrap());
            let deadline = Instant::now() + Duration::from_secs(10);
            while entered.load(Ordering::SeqCst) == 0 {
                assert!(
                    Instant::now() < deadline,
                    "the first launch never reached the app"
                );
                std::thread::sleep(Duration::from_millis(10));
            }
            // One try, no waiting: an instance is listening.
            let mut b = fs::OpenOptions::new()
                .read(true)
                .write(true)
                .open(&pipe)
                .expect("no pipe instance listens while the window is being raised");
            b.write_all(MESSAGE).unwrap();
            release.store(1, Ordering::SeqCst);
            assert!(matches!(a.join().unwrap(), Start::Raised));
            let mut answer = [0u8; 3];
            b.read_exact(&mut answer).unwrap();
            assert_eq!(&answer, OK);
            assert_eq!(entered.load(Ordering::SeqCst), 2);
        }

        #[test]
        fn a_launch_is_told_when_the_window_could_not_be_shown() {
            let t = temp();
            let dir = t.0.join("instance");
            let i = first(&dir);
            i.listen(&socket_dirs(), || false).unwrap();
            let e = start(&dir, Duration::from_secs(2)).err().unwrap();
            assert!(e.contains("window could not be shown"), "{e}");
        }

        /// When the first app has nowhere to listen, a later launch says so at once instead of
        /// waiting out its bound.
        #[test]
        fn a_first_app_that_cannot_listen_is_reported_at_once() {
            let t = temp();
            let dir = t.0.join("instance");
            let i = first(&dir);
            assert!(i
                .listen(&[PathBuf::from("y".repeat(120))], || true)
                .is_err());
            let t0 = Instant::now();
            let e = start(&dir, Duration::from_secs(10)).err().unwrap();
            assert!(e.contains("cannot be raised"), "{e}");
            assert!(t0.elapsed() < Duration::from_secs(2), "{:?}", t0.elapsed());
        }

        /// A server that takes the message and never answers is waited on until the deadline, and
        /// not after it.
        #[test]
        fn a_launch_that_gets_no_answer_ends() {
            let t = temp();
            let dir = t.0.join("instance");
            let held = first(&dir);
            let name = PathBuf::from(r"\\.\pipe").join(crate::instance::socket_name(&held.key));
            let sec = Security::this_user().unwrap();
            let _mute = create_instance(&wide(name.as_os_str()), &sec, true).unwrap();
            fs::write(dir.join(ADDRESS), name.as_os_str().as_encoded_bytes()).unwrap();
            let wait = Duration::from_millis(300);
            let d = dir.clone();
            let (e, took) = within(Duration::from_secs(20), move || {
                let t0 = Instant::now();
                (start(&d, wait).err(), t0.elapsed())
            })
            .expect("a launch did not end");
            let e = e.expect("a launch ran as a second instance");
            assert!(e.contains("did not answer"), "{e}");
            assert!(took >= wait, "it gave up after {took:?}, before {wait:?}");
            assert!(
                took < wait + Duration::from_secs(2),
                "it took {took:?} for a {wait:?} deadline"
            );
            // Past its deadline a launch asks nothing.
            let e = start_by(&dir, Instant::now()).err().unwrap();
            assert!(e.contains("did not answer"), "{e}");
        }

        /// An address that is not one of this app's pipes is not connected to.
        #[test]
        fn an_address_that_is_not_a_pluxy_pipe_is_refused() {
            let t = temp();
            let dir = t.0.join("instance");
            let _held = first(&dir);
            for other in [r"\\.\pipe\other", r"\\server\pipe\pluxy-app-00", r"C:\x"] {
                fs::write(dir.join(ADDRESS), other).unwrap();
                let e = start(&dir, Duration::from_secs(2)).err().unwrap();
                assert!(e.contains("not one of this app's pipes"), "{other}: {e}");
            }
        }

        /// A junction in place of the folder is not followed.
        #[test]
        fn a_link_in_place_of_the_folder_is_refused() {
            let t = temp();
            let real = t.0.join("real");
            fs::create_dir_all(&real).unwrap();
            let link = t.0.join("link");
            let made = std::process::Command::new("cmd")
                .args(["/C", "mklink", "/J"])
                .arg(&link)
                .arg(&real)
                .output()
                .unwrap();
            assert!(made.status.success(), "mklink: {made:?}");
            let e = start(&link, Duration::from_millis(100)).err().unwrap();
            assert!(e.contains("not a folder"), "{e}");
            // The positive control on the same route: the real folder is used.
            let _i = first(&real);
        }

        /// A lock that is a link is not followed: through it, two launches could lock two
        /// different files and both run.
        #[test]
        fn a_lock_that_is_a_link_is_refused() {
            let t = temp();
            let dir = t.0.join("instance");
            fs::create_dir_all(&dir).unwrap();
            let target = t.0.join("elsewhere");
            fs::write(&target, b"").unwrap();
            if std::os::windows::fs::symlink_file(&target, dir.join(crate::instance::LOCK)).is_err()
            {
                // Making a symbolic link needs a privilege some accounts lack (a runner has it).
                assert!(
                    std::env::var_os("GITHUB_ACTIONS").is_none(),
                    "a runner could not make a symbolic link"
                );
                return;
            }
            assert!(start(&dir, Duration::from_millis(100)).is_err());
            // The positive control on the same route: a plain lock file is used.
            let other = t.0.join("other");
            fs::create_dir_all(&other).unwrap();
            fs::write(other.join(crate::instance::LOCK), b"").unwrap();
            let _i = first(&other);
        }
    }
}
