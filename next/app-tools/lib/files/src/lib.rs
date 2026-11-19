//! The file rules the app tools share on Linux, macOS and Windows.
//!
//! - **Private** files and folders are this user's alone: on Linux and macOS owned by this user, a
//!   file 0600 and a folder 0700; on Windows each is owned by this user, by the owner this process gives what it
//!   makes (its user, or for an elevated administrator the Administrators group), or by SYSTEM (the
//!   system account, whose reach matches root's), and carries a protected access list (one that
//!   takes nothing from its folder's) that allows only this user and SYSTEM. A folder's list is inherited by what is made inside it;
//!   what is made that way is not private until it is made so, since its list is not protected.
//!   A private file or folder is made with its list in one step, so it is never open to anyone
//!   else, even for a moment.
//! - [`open_regular`] opens a file for reading without following a link and only if it is a
//!   regular file, checked on the open handle: on Linux and macOS with `O_NOFOLLOW` and
//!   `O_NONBLOCK` (a FIFO is refused, never waited on); on Windows the link itself is opened
//!   (`FILE_FLAG_OPEN_REPARSE_POINT`) and refused, as is anything that is not a file on a disk.
//! - [`Id`] tells files apart: the device and inode on Linux and macOS, the volume serial number
//!   and 128-bit file ID on Windows.
//! - [`mtime_ns`] is a file's modification time in nanoseconds since the Unix epoch, the same
//!   value on every platform.
//! - On macOS and Windows, a [`Lock`] is a private file this process holds while it lives, made
//!   and taken in one step each ([`create_locked`], [`take_lock`]): on macOS with `O_EXLOCK`, removed
//!   when dropped; on Windows opened for no one else to share, and removed by the system when its
//!   handle closes (when the process stops, too).

use std::fs::{self, File, Metadata};
use std::io;
use std::path::Path;
use std::time::UNIX_EPOCH;

/// A file's identity: two handles or paths with the same `Id` are the same file.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Id {
    volume: u64,
    file: u128,
}

/// The modification time in m, in nanoseconds since the Unix epoch (negative before it), saturating
/// at the ends of `i64`.
pub fn mtime_ns(m: &Metadata) -> i64 {
    let Ok(t) = m.modified() else {
        return 0;
    };
    let ns = |d: std::time::Duration| i64::try_from(d.as_nanos()).unwrap_or(i64::MAX);
    match t.duration_since(UNIX_EPOCH) {
        Ok(d) => ns(d),
        Err(e) => ns(e.duration()).saturating_neg(),
    }
}

fn another_owner() -> io::Error {
    io::Error::new(io::ErrorKind::PermissionDenied, "owned by another account")
}

fn not_regular() -> io::Error {
    io::Error::new(io::ErrorKind::InvalidInput, "not a regular file")
}

/// Opens path for reading without following a link or waiting on a FIFO, and only if it is a
/// regular file (checked on the open handle, so a swap after an earlier check is caught).
pub fn open_regular(path: &Path) -> io::Result<File> {
    let file = imp::open_nofollow(path)?;
    if imp::regular(&file)? {
        Ok(file)
    } else {
        Err(not_regular())
    }
}

/// The identity of the file open as file.
pub fn id_of(file: &File) -> io::Result<Id> {
    imp::id_of(file)
}

/// The identity of what path names, without following a link (a link's own identity).
pub fn id_of_path(path: &Path) -> io::Result<Id> {
    imp::id_of_path(path)
}

/// Makes the folder path, private (as `mkdir` with 0700). Its parent must exist; an existing path
/// is an `AlreadyExists` error.
pub fn create_private_dir(path: &Path) -> io::Result<()> {
    imp::create_private_dir(path)
}

/// Makes the folder path and any missing parents; the folders it makes are private on Linux and
/// macOS (0700). On Windows a new folder inherits its parent's access list: use this only under a
/// folder that is private already, or one the system keeps per user (the temporary folder).
pub fn create_private_dir_all(path: &Path) -> io::Result<()> {
    imp::create_private_dir_all(path)
}

/// Creates the file path, which must not exist, for writing, private (as `O_CREAT|O_EXCL` with
/// 0600). On Windows, in a folder whose inherited entries deny OWNER RIGHTS the right to change an
/// access list, this (and [`create_private_dir`]) is refused with `PermissionDenied`: that folder
/// forbids whoever makes something in it to set its list.
pub fn create_new_private(path: &Path) -> io::Result<File> {
    imp::create_new_private(path)
}

/// Makes the existing file or folder path private, through a handle that is path itself, not a
/// link it names: 0600 or 0700 on Linux and macOS; on Windows a protected access list allowing only
/// this user and SYSTEM (a folder's inherited by what is made in it). A link, anything but a file or
/// folder, and one owned by an account `is_private` does not accept are refused, never taken over.
pub fn make_private(path: &Path) -> io::Result<()> {
    imp::make_private(path)
}

/// Whether the file or folder path (not followed if it is a link) is private already: on Linux and
/// macOS this user (the effective user ID) owns it and no group or other permission bit is set; on
/// Windows this user, the owner this process gives what it makes, or SYSTEM owns it, its access list
/// exists and is protected, every allow entry in it is for this user or SYSTEM (inherit-only ones
/// included), deny entries are ignored, and it holds no other kind of entry.
pub fn is_private(path: &Path) -> io::Result<bool> {
    imp::is_private(path)
}

/// Makes the file or folder open as file private, as [`make_private`] does, through that handle
/// (which must have been opened with the right to change its permissions or access list). Meant for
/// what this process has just made itself.
pub fn make_private_open(file: &File) -> io::Result<()> {
    imp::make_private_open(file)
}

/// Windows: the access list of the file open as file, in the system's text form (SDDL), with
/// whether it is protected (`D:P(…)`) or takes up its folder's (`D:AI(…)`, inherited entries
/// marked `ID`).
#[cfg(windows)]
pub fn access_list(file: &File) -> io::Result<String> {
    imp::access_list(file)
}

/// Windows: gives the file open as file the access list sddl (as [`access_list`] writes it):
/// protected if it says so; else it takes up its folder's inheritable entries besides its own.
#[cfg(windows)]
pub fn set_access_list(file: &File, sddl: &str) -> io::Result<()> {
    imp::set_access_list(file, sddl)
}

/// Windows: creates the file name, which must not exist, in the folder open as folder (no path is
/// looked up again), private from the moment it is made (its protected list given at creation, as
/// [`create_new_private`] does), open to read, write, delete and change its list, and shared with
/// no other handle: a temporary file nothing else can open until it is renamed and closed. A name
/// that is a path, empty, `.` or `..` is refused; in a folder that forbids setting access lists
/// (see [`create_new_private`]) the creation is refused with `PermissionDenied`.
#[cfg(windows)]
pub fn create_private_in(folder: &File, name: &std::ffi::OsStr) -> io::Result<File> {
    imp::create_private_in(folder, name)
}

/// Windows: renames the file open as file to name in the folder open as folder, replacing a file
/// there, through the handles (no path is looked up again). The file must have been opened with
/// the right to delete it.
#[cfg(windows)]
pub fn rename_open(file: &File, folder: &File, name: &std::ffi::OsStr) -> io::Result<()> {
    imp::rename_open(file, folder, name)
}

/// Why Windows would not take name, one part of a path, as the file it names, or None when it
/// would: a name ending in a dot or a blank (Windows drops them), one holding a character Windows
/// reserves (`:`, which names a stream of a file, `/ * ? " < > |` or a control character), or a
/// device name (`CON`, `PRN`, `AUX`, `NUL`, `COM1` to `COM9`, `LPT1` to `LPT9`, `CONIN$`,
/// `CONOUT$`, in any case, with or without an extension). A plain string check, the same on every
/// platform.
pub fn windows_name_problem(name: &str) -> Option<&'static str> {
    if name.ends_with(['.', ' ']) {
        return Some("it ends in a dot or a blank, which Windows drops");
    }
    if name
        .chars()
        .any(|c| c.is_control() || matches!(c, ':' | '/' | '*' | '?' | '"' | '<' | '>' | '|'))
    {
        return Some("it holds a character Windows reserves (a colon names a stream of a file)");
    }
    let stem = name
        .split('.')
        .next()
        .unwrap_or("")
        .trim_end()
        .to_uppercase();
    let numbered = |prefix: &str| {
        stem.strip_prefix(prefix).is_some_and(|n| {
            let mut c = n.chars();
            matches!(
                (c.next(), c.next()),
                (Some('1'..='9' | '¹' | '²' | '³'), None)
            )
        })
    };
    if matches!(
        stem.as_str(),
        "CON" | "PRN" | "AUX" | "NUL" | "CONIN$" | "CONOUT$"
    ) || numbered("COM")
        || numbered("LPT")
    {
        return Some("it is a device name");
    }
    None
}

/// Whether anyone but this user may change the file or folder path (not followed if it is a link),
/// on Windows: its owner is not this user, the owner this process gives what it makes, SYSTEM or
/// the Administrators group; or it has no access list (which allows everyone); or an allow entry
/// that applies to it gives a write-type right (write or append data, add or delete what a folder
/// holds, delete, change its access list or owner, generic write or all) to an account other than
/// this user, SYSTEM or Administrators (whose reach matches root's). Entries only inherited by what
/// a folder holds do not count; an allow entry of any other kind counts as one that may write.
/// Inherited lists are accepted as they are (unlike [`is_private`]), so a folder in the user's
/// profile, which allows SYSTEM and Administrators, is one that no one else may change.
#[cfg(windows)]
pub fn others_may_write(path: &Path) -> io::Result<bool> {
    imp::others_may_write(path)
}

/// A lock file this process holds (macOS and Windows): no other process can take it while this
/// lives. Dropping it releases and removes it.
#[cfg(any(windows, target_os = "macos"))]
pub struct Lock {
    _file: File,
    #[cfg(unix)]
    path: std::path::PathBuf,
}

#[cfg(target_os = "macos")]
impl Drop for Lock {
    fn drop(&mut self) {
        // Removed while still held, so no process can take it between the two.
        let _ = fs::remove_file(&self.path);
    }
}

/// Creates the private file path, which must not exist, already held (in one step: no other
/// process can see it unheld). An existing path is an `AlreadyExists` error.
#[cfg(any(windows, target_os = "macos"))]
pub fn create_locked(path: &Path) -> io::Result<Lock> {
    imp::create_locked(path)
}

/// Takes the lock file path when no process holds it: `None` while one does. A link at path is
/// never followed.
#[cfg(any(windows, target_os = "macos"))]
pub fn take_lock(path: &Path) -> io::Result<Option<Lock>> {
    imp::take_lock(path)
}

/// Whether path (a link followed) is a program someone may start by its path: on Linux and macOS
/// a regular file with an execute permission bit; on Windows, which has no such bit and starts a
/// file as a program by its name's extension, a regular file named `….exe` or `….com` (in any
/// case) that is a PE image (starts with `MZ`), the rule the execution program launches by.
pub fn executable(path: &Path) -> bool {
    let Ok(m) = fs::metadata(path) else {
        return false;
    };
    if !m.is_file() {
        return false;
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        m.permissions().mode() & 0o111 != 0
    }
    #[cfg(windows)]
    {
        use std::io::Read;
        let named = path
            .extension()
            .and_then(|e| e.to_str())
            .is_some_and(|e| e.eq_ignore_ascii_case("exe") || e.eq_ignore_ascii_case("com"));
        let mut magic = [0u8; 2];
        named
            && File::open(path).is_ok_and(|mut f| f.read_exact(&mut magic).is_ok())
            && magic == *b"MZ"
    }
}

/// Flushes the folder path, so a rename or removal in it lasts. On Windows this does nothing: the
/// file system logs a folder's entries itself, and a folder cannot be flushed through a handle
/// opened to read it.
pub fn sync_dir(path: &Path) -> io::Result<()> {
    #[cfg(unix)]
    return File::open(path).and_then(|d| d.sync_all());
    #[cfg(windows)]
    {
        let _ = path;
        Ok(())
    }
}

#[cfg(unix)]
mod imp {
    use super::*;
    use std::os::unix::fs::{DirBuilderExt, MetadataExt, OpenOptionsExt, PermissionsExt};

    pub fn open_nofollow(path: &Path) -> io::Result<File> {
        fs::OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
            .open(path)
    }

    pub fn regular(file: &File) -> io::Result<bool> {
        Ok(file.metadata()?.is_file())
    }

    fn id(m: &Metadata) -> Id {
        Id {
            volume: m.dev(),
            file: u128::from(m.ino()),
        }
    }

    pub fn id_of(file: &File) -> io::Result<Id> {
        Ok(id(&file.metadata()?))
    }

    pub fn id_of_path(path: &Path) -> io::Result<Id> {
        Ok(id(&fs::symlink_metadata(path)?))
    }

    pub fn create_private_dir(path: &Path) -> io::Result<()> {
        fs::DirBuilder::new().mode(0o700).create(path)
    }

    pub fn create_private_dir_all(path: &Path) -> io::Result<()> {
        fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(path)
    }

    pub fn create_new_private(path: &Path) -> io::Result<File> {
        fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(path)
    }

    fn a_link(e: io::Error) -> io::Error {
        if e.raw_os_error() == Some(libc::ELOOP) {
            io::Error::new(io::ErrorKind::InvalidInput, "a link")
        } else {
            e
        }
    }

    pub fn make_private(path: &Path) -> io::Result<()> {
        // The mode is changed on the handle (fchmod), so a link swapped in after the open is not
        // followed.
        let f = open_nofollow(path).map_err(a_link)?;
        let m = f.metadata()?;
        // Another user's file is refused, even where this process could change it (as root).
        if m.uid() != unsafe { libc::geteuid() } {
            return Err(another_owner());
        }
        let mode = if m.is_dir() {
            0o700
        } else if m.is_file() {
            0o600
        } else {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "not a file or folder",
            ));
        };
        f.set_permissions(fs::Permissions::from_mode(mode))
    }

    pub fn make_private_open(f: &File) -> io::Result<()> {
        let m = f.metadata()?;
        if m.uid() != unsafe { libc::geteuid() } {
            return Err(another_owner());
        }
        let mode = if m.is_dir() {
            0o700
        } else if m.is_file() {
            0o600
        } else {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "not a file or folder",
            ));
        };
        f.set_permissions(fs::Permissions::from_mode(mode))
    }

    pub fn is_private(path: &Path) -> io::Result<bool> {
        let m = fs::symlink_metadata(path)?;
        Ok(m.uid() == unsafe { libc::geteuid() } && m.permissions().mode() & 0o077 == 0)
    }

    #[cfg(target_os = "macos")]
    pub fn create_locked(path: &Path) -> io::Result<Lock> {
        // O_EXLOCK with O_CREAT|O_EXCL: made and locked in one open.
        let file = fs::OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .mode(0o600)
            .custom_flags(libc::O_EXLOCK | libc::O_NONBLOCK | libc::O_NOFOLLOW)
            .open(path)?;
        Ok(Lock {
            _file: file,
            path: path.to_owned(),
        })
    }

    #[cfg(target_os = "macos")]
    pub fn take_lock(path: &Path) -> io::Result<Option<Lock>> {
        match fs::OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_EXLOCK | libc::O_NONBLOCK | libc::O_NOFOLLOW)
            .open(path)
        {
            Ok(file) => Ok(Some(Lock {
                _file: file,
                path: path.to_owned(),
            })),
            Err(e) if e.raw_os_error() == Some(libc::EWOULDBLOCK) => Ok(None),
            Err(e) => Err(a_link(e)),
        }
    }
}

#[cfg(windows)]
mod imp {
    use super::*;
    use std::os::windows::ffi::OsStrExt;
    use std::os::windows::fs::{MetadataExt, OpenOptionsExt};
    use std::os::windows::io::{AsRawHandle, FromRawHandle};
    use std::ptr::{null, null_mut};
    use windows_sys::Wdk::Foundation::OBJECT_ATTRIBUTES;
    use windows_sys::Wdk::Storage::FileSystem::{
        FileRenameInformation, FileRenameInformationEx, NtCreateFile, NtSetInformationFile,
        FILE_CREATE, FILE_NON_DIRECTORY_FILE, FILE_OPEN_REPARSE_POINT, FILE_RENAME_INFORMATION,
        FILE_RENAME_POSIX_SEMANTICS, FILE_RENAME_REPLACE_IF_EXISTS, FILE_SYNCHRONOUS_IO_NONALERT,
    };
    use windows_sys::Win32::Foundation::{
        CloseHandle, LocalFree, RtlNtStatusToDosError, ERROR_ACCESS_DENIED,
        ERROR_SHARING_VIOLATION, ERROR_SUCCESS, GENERIC_READ, GENERIC_WRITE, HANDLE,
        INVALID_HANDLE_VALUE, NTSTATUS, OBJ_CASE_INSENSITIVE, STATUS_INVALID_INFO_CLASS,
        STATUS_INVALID_PARAMETER, STATUS_NOT_IMPLEMENTED, STATUS_NOT_SUPPORTED, UNICODE_STRING,
    };
    use windows_sys::Win32::Security::Authorization::{
        ConvertSecurityDescriptorToStringSecurityDescriptorW,
        ConvertStringSecurityDescriptorToSecurityDescriptorW, GetSecurityInfo, SetEntriesInAclW,
        SetSecurityInfo, EXPLICIT_ACCESS_W, NO_MULTIPLE_TRUSTEE, SDDL_REVISION_1, SET_ACCESS,
        SE_FILE_OBJECT, TRUSTEE_IS_SID, TRUSTEE_IS_USER, TRUSTEE_IS_WELL_KNOWN_GROUP, TRUSTEE_W,
    };
    use windows_sys::Win32::Security::{
        CreateWellKnownSid, EqualSid, GetAce, GetSecurityDescriptorControl,
        GetSecurityDescriptorDacl, GetTokenInformation, InitializeSecurityDescriptor,
        SetSecurityDescriptorControl, SetSecurityDescriptorDacl, TokenOwner, TokenUser,
        WinBuiltinAdministratorsSid, WinLocalSystemSid, ACCESS_ALLOWED_ACE, ACE_HEADER, ACL,
        DACL_SECURITY_INFORMATION, NO_INHERITANCE, OWNER_SECURITY_INFORMATION,
        PROTECTED_DACL_SECURITY_INFORMATION, PSECURITY_DESCRIPTOR, PSID, SECURITY_ATTRIBUTES,
        SECURITY_DESCRIPTOR, SECURITY_MAX_SID_SIZE, SE_DACL_PROTECTED,
        SUB_CONTAINERS_AND_OBJECTS_INHERIT, TOKEN_INFORMATION_CLASS, TOKEN_QUERY,
        UNPROTECTED_DACL_SECURITY_INFORMATION,
    };
    use windows_sys::Win32::Storage::FileSystem::{
        CreateDirectoryW, CreateFileW, FileIdInfo, GetFileInformationByHandle,
        GetFileInformationByHandleEx, GetFileType, BY_HANDLE_FILE_INFORMATION, CREATE_NEW, DELETE,
        FILE_ALL_ACCESS, FILE_ATTRIBUTE_NORMAL, FILE_ATTRIBUTE_REPARSE_POINT,
        FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_DELETE_ON_CLOSE, FILE_FLAG_OPEN_REPARSE_POINT,
        FILE_ID_INFO, FILE_READ_ATTRIBUTES, FILE_SHARE_DELETE, FILE_SHARE_READ, FILE_SHARE_WRITE,
        FILE_TYPE_DISK, OPEN_EXISTING, READ_CONTROL, SYNCHRONIZE, WRITE_DAC,
    };
    use windows_sys::Win32::System::Threading::{GetCurrentProcess, OpenProcessToken};
    use windows_sys::Win32::System::IO::IO_STATUS_BLOCK;

    /// `ACCESS_ALLOWED_ACE_TYPE` and `ACCESS_DENIED_ACE_TYPE` (winnt.h).
    const ACCESS_ALLOWED: u8 = 0;
    const ACCESS_DENIED: u8 = 1;
    /// `SECURITY_DESCRIPTOR_REVISION` (winnt.h).
    const SD_REVISION: u32 = 1;

    fn handle(f: &File) -> HANDLE {
        f.as_raw_handle() as HANDLE
    }

    fn win32(code: u32) -> io::Result<()> {
        if code == ERROR_SUCCESS {
            Ok(())
        } else {
            Err(io::Error::from_raw_os_error(code as i32))
        }
    }

    /// A Win32 call's BOOL as a result (the error is the thread's last one).
    fn ok(b: windows_sys::core::BOOL) -> io::Result<()> {
        if b == 0 {
            Err(io::Error::last_os_error())
        } else {
            Ok(())
        }
    }

    fn refuse_link(m: &Metadata) -> io::Result<()> {
        if m.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
            Err(io::Error::new(io::ErrorKind::InvalidInput, "a link"))
        } else {
            Ok(())
        }
    }

    /// path as a NUL-terminated wide string.
    fn wide(path: &Path) -> io::Result<Vec<u16>> {
        let w: Vec<u16> = path.as_os_str().encode_wide().collect();
        if w.contains(&0) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "a NUL in the path",
            ));
        }
        Ok(w.into_iter().chain([0]).collect())
    }

    /// CreateFileW's handle as a File.
    fn created(h: HANDLE) -> io::Result<File> {
        if h == INVALID_HANDLE_VALUE {
            Err(io::Error::last_os_error())
        } else {
            Ok(unsafe { File::from_raw_handle(h as _) })
        }
    }

    pub fn open_nofollow(path: &Path) -> io::Result<File> {
        fs::OpenOptions::new()
            .read(true)
            .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
            .open(path)
    }

    pub fn regular(file: &File) -> io::Result<bool> {
        let m = file.metadata()?;
        Ok(m.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT == 0
            && m.is_file()
            && unsafe { GetFileType(handle(file)) } == FILE_TYPE_DISK)
    }

    pub fn id_of(file: &File) -> io::Result<Id> {
        let mut info = FILE_ID_INFO::default();
        let got = unsafe {
            GetFileInformationByHandleEx(
                handle(file),
                FileIdInfo,
                (&mut info as *mut FILE_ID_INFO).cast(),
                std::mem::size_of::<FILE_ID_INFO>() as u32,
            )
        };
        if got != 0 {
            return Ok(Id {
                volume: info.VolumeSerialNumber,
                file: u128::from_le_bytes(info.FileId.Identifier),
            });
        }
        // A file system with no 128-bit IDs (FAT32, exFAT): its 64-bit index, the same for every
        // file on that volume.
        let mut info = BY_HANDLE_FILE_INFORMATION::default();
        ok(unsafe { GetFileInformationByHandle(handle(file), &mut info) })?;
        Ok(Id {
            volume: u64::from(info.dwVolumeSerialNumber),
            file: u128::from(info.nFileIndexHigh) << 32 | u128::from(info.nFileIndexLow),
        })
    }

    /// path itself, a link or not, opened to read its attributes (and its access list).
    fn open_itself(path: &Path, access: u32) -> io::Result<File> {
        fs::OpenOptions::new()
            .access_mode(access)
            .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS)
            .open(path)
    }

    pub fn id_of_path(path: &Path) -> io::Result<Id> {
        id_of(&open_itself(path, FILE_READ_ATTRIBUTES)?)
    }

    pub fn create_private_dir(path: &Path) -> io::Result<()> {
        let name = wide(path)?;
        with_private(true, |sa| {
            ok(unsafe { CreateDirectoryW(name.as_ptr(), sa) })
        })
    }

    pub fn create_private_dir_all(path: &Path) -> io::Result<()> {
        fs::create_dir_all(path)
    }

    pub fn create_new_private(path: &Path) -> io::Result<File> {
        let name = wide(path)?;
        with_private(false, |sa| {
            created(unsafe {
                CreateFileW(
                    name.as_ptr(),
                    GENERIC_WRITE,
                    FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                    sa,
                    CREATE_NEW,
                    FILE_ATTRIBUTE_NORMAL,
                    null_mut(),
                )
            })
        })
    }

    pub fn create_locked(path: &Path) -> io::Result<Lock> {
        let name = wide(path)?;
        // Shared with no one, so no other open succeeds while it is held; removed when its last
        // handle closes, so a process that stops leaves none.
        let file = with_private(false, |sa| {
            created(unsafe {
                CreateFileW(
                    name.as_ptr(),
                    GENERIC_READ | GENERIC_WRITE | DELETE,
                    0,
                    sa,
                    CREATE_NEW,
                    FILE_ATTRIBUTE_NORMAL | FILE_FLAG_DELETE_ON_CLOSE,
                    null_mut(),
                )
            })
        })?;
        Ok(Lock { _file: file })
    }

    pub fn take_lock(path: &Path) -> io::Result<Option<Lock>> {
        let name = wide(path)?;
        let opened = created(unsafe {
            CreateFileW(
                name.as_ptr(),
                GENERIC_READ | DELETE,
                0,
                null(),
                OPEN_EXISTING,
                FILE_FLAG_DELETE_ON_CLOSE | FILE_FLAG_OPEN_REPARSE_POINT,
                null_mut(),
            )
        });
        match opened {
            Ok(file) => Ok(Some(Lock { _file: file })),
            // Held; or being removed by the process that held it (denied while removal is pending).
            Err(e)
                if e.raw_os_error() == Some(ERROR_SHARING_VIOLATION as i32)
                    || e.raw_os_error() == Some(ERROR_ACCESS_DENIED as i32) =>
            {
                Ok(None)
            }
            Err(e) => Err(e),
        }
    }

    pub fn make_private(path: &Path) -> io::Result<()> {
        let f = open_itself(path, FILE_READ_ATTRIBUTES | READ_CONTROL | WRITE_DAC)?;
        refuse_link(&f.metadata()?)?;
        make_private_open(&f)
    }

    pub fn make_private_open(f: &File) -> io::Result<()> {
        let m = f.metadata()?;
        // A link (symbolic link or junction) opened itself, or anything not on a disk, is refused.
        refuse_link(&m)?;
        if !(m.is_dir() || m.is_file()) || unsafe { GetFileType(handle(f)) } != FILE_TYPE_DISK {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "not a file or folder",
            ));
        }
        let user = TokenSid::user()?;
        let default_owner = TokenSid::default_owner()?;
        let system = System::sid()?;
        // Another account's file is refused, never taken over, as on Linux and macOS.
        let (owner, _sd) = owner_of(f)?;
        if !owner_accepted(owner, &user, &default_owner, &system) {
            return Err(another_owner());
        }
        let acl = private_acl(&user, &system, m.is_dir())?;
        win32(unsafe {
            SetSecurityInfo(
                handle(f),
                SE_FILE_OBJECT,
                DACL_SECURITY_INFORMATION | PROTECTED_DACL_SECURITY_INFORMATION,
                null_mut(),
                null_mut(),
                acl.0,
                null(),
            )
        })
    }

    pub fn access_list(f: &File) -> io::Result<String> {
        let mut sd: PSECURITY_DESCRIPTOR = null_mut();
        win32(unsafe {
            GetSecurityInfo(
                handle(f),
                SE_FILE_OBJECT,
                DACL_SECURITY_INFORMATION,
                null_mut(),
                null_mut(),
                null_mut(),
                null_mut(),
                &mut sd,
            )
        })?;
        let sd = Local(sd);
        let mut text: *mut u16 = null_mut();
        let mut len = 0u32;
        ok(unsafe {
            ConvertSecurityDescriptorToStringSecurityDescriptorW(
                sd.0,
                SDDL_REVISION_1,
                DACL_SECURITY_INFORMATION,
                &mut text,
                &mut len,
            )
        })?;
        let text = Local(text);
        let wide = unsafe { std::slice::from_raw_parts(text.0, len as usize) };
        // The text ends at its first NUL: the length the system gives may count more after it.
        let wide = &wide[..wide.iter().position(|&c| c == 0).unwrap_or(wide.len())];
        String::from_utf16(wide)
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "an access list not in UTF-16"))
    }

    pub fn set_access_list(f: &File, sddl: &str) -> io::Result<()> {
        let bad = || io::Error::new(io::ErrorKind::InvalidInput, "not an access list");
        let text: Vec<u16> = sddl.encode_utf16().chain([0]).collect();
        if text[..text.len() - 1].contains(&0) {
            return Err(bad());
        }
        let mut sd: PSECURITY_DESCRIPTOR = null_mut();
        ok(unsafe {
            ConvertStringSecurityDescriptorToSecurityDescriptorW(
                text.as_ptr(),
                SDDL_REVISION_1,
                &mut sd,
                null_mut(),
            )
        })?;
        let sd = Local(sd);
        let (mut present, mut defaulted) = (0, 0);
        let mut dacl: *mut ACL = null_mut();
        ok(unsafe { GetSecurityDescriptorDacl(sd.0, &mut present, &mut dacl, &mut defaulted) })?;
        // No list, or a NULL one (which allows everyone), is not one this gives.
        if present == 0 || dacl.is_null() {
            return Err(bad());
        }
        let mut control = 0u16;
        let mut revision = 0u32;
        ok(unsafe { GetSecurityDescriptorControl(sd.0, &mut control, &mut revision) })?;
        let protection = if control & SE_DACL_PROTECTED != 0 {
            PROTECTED_DACL_SECURITY_INFORMATION
        } else {
            UNPROTECTED_DACL_SECURITY_INFORMATION
        };
        win32(unsafe {
            SetSecurityInfo(
                handle(f),
                SE_FILE_OBJECT,
                DACL_SECURITY_INFORMATION | protection,
                null_mut(),
                null_mut(),
                dacl,
                null(),
            )
        })
    }

    /// An NTSTATUS as a result, its error as the matching Win32 one.
    fn nt(status: NTSTATUS) -> io::Result<()> {
        if status >= 0 {
            Ok(())
        } else {
            Err(io::Error::from_raw_os_error(
                unsafe { RtlNtStatusToDosError(status) } as i32,
            ))
        }
    }

    /// A single name in a folder, as UTF-16: not empty, no separator, no NUL, not `.` or `..`.
    fn single_name(name: &std::ffi::OsStr) -> io::Result<Vec<u16>> {
        let wide: Vec<u16> = name.encode_wide().collect();
        if wide.is_empty()
            || wide.contains(&0)
            || wide.iter().any(|c| *c == b'\\' as u16 || *c == b'/' as u16)
            || name == "."
            || name == ".."
        {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "not a file name",
            ));
        }
        Ok(wide)
    }

    pub fn create_private_in(folder: &File, name: &std::ffi::OsStr) -> io::Result<File> {
        let name = single_name(name)?;
        let len = u16::try_from(name.len() * 2)
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "a name too long"))?;
        let object = UNICODE_STRING {
            Length: len,
            MaximumLength: len,
            Buffer: name.as_ptr() as *mut u16,
        };
        // The native call: it makes the file relative to the folder's handle, with the private
        // descriptor, in one step.
        with_private(false, |sa| {
            let attributes = OBJECT_ATTRIBUTES {
                Length: std::mem::size_of::<OBJECT_ATTRIBUTES>() as u32,
                RootDirectory: handle(folder),
                ObjectName: &object,
                Attributes: OBJ_CASE_INSENSITIVE,
                SecurityDescriptor: unsafe { (*sa).lpSecurityDescriptor } as *const _,
                SecurityQualityOfService: null(),
            };
            let mut h: HANDLE = null_mut();
            let mut status = IO_STATUS_BLOCK::default();
            nt(unsafe {
                NtCreateFile(
                    &mut h,
                    GENERIC_READ | GENERIC_WRITE | DELETE | WRITE_DAC | SYNCHRONIZE,
                    &attributes,
                    &mut status,
                    null(),
                    FILE_ATTRIBUTE_NORMAL,
                    0,
                    FILE_CREATE,
                    FILE_NON_DIRECTORY_FILE
                        | FILE_SYNCHRONOUS_IO_NONALERT
                        | FILE_OPEN_REPARSE_POINT,
                    null(),
                    0,
                )
            })?;
            Ok(unsafe { File::from_raw_handle(h as _) })
        })
    }

    pub fn rename_open(f: &File, folder: &File, name: &std::ffi::OsStr) -> io::Result<()> {
        let name = single_name(name)?;
        // The native call: the Win32 one takes no folder handle (RootDirectory must be null).
        let head = std::mem::offset_of!(FILE_RENAME_INFORMATION, FileName);
        let bytes = (head + name.len() * 2).max(std::mem::size_of::<FILE_RENAME_INFORMATION>());
        // u64s keep the structure's handle field aligned.
        let mut buf = vec![0u64; bytes.div_ceil(8)];
        let info = buf.as_mut_ptr() as *mut FILE_RENAME_INFORMATION;
        let rename = |class, posix: bool| unsafe {
            if posix {
                (*info).Anonymous.Flags =
                    FILE_RENAME_REPLACE_IF_EXISTS | FILE_RENAME_POSIX_SEMANTICS;
            } else {
                (*info).Anonymous.Flags = 0;
                (*info).Anonymous.ReplaceIfExists = true;
            }
            (*info).RootDirectory = handle(folder);
            (*info).FileNameLength = (name.len() * 2) as u32;
            std::ptr::copy_nonoverlapping(
                name.as_ptr(),
                std::ptr::addr_of_mut!((*info).FileName) as *mut u16,
                name.len(),
            );
            let mut status = IO_STATUS_BLOCK::default();
            NtSetInformationFile(
                handle(f),
                &mut status,
                info as *const core::ffi::c_void,
                bytes as u32,
                class,
            )
        };
        // POSIX semantics (the name is replaced even while another program has the old file
        // open) where the file system has them; else the classic rename, which replaces too.
        let status = rename(FileRenameInformationEx, true);
        if [
            STATUS_INVALID_PARAMETER,
            STATUS_INVALID_INFO_CLASS,
            STATUS_NOT_SUPPORTED,
            STATUS_NOT_IMPLEMENTED,
        ]
        .contains(&status)
        {
            return nt(rename(FileRenameInformation, false));
        }
        nt(status)
    }

    /// A SID this process's token names, in the buffer that holds it: its user, or its default
    /// owner (the owner it gives what it makes: its user, or for an elevated administrator the
    /// Administrators group).
    struct TokenSid(Vec<u64>);

    impl TokenSid {
        fn user() -> io::Result<TokenSid> {
            TokenSid::query(TokenUser)
        }

        fn default_owner() -> io::Result<TokenSid> {
            TokenSid::query(TokenOwner)
        }

        fn query(class: TOKEN_INFORMATION_CLASS) -> io::Result<TokenSid> {
            let mut token: HANDLE = null_mut();
            if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) } == 0 {
                return Err(io::Error::last_os_error());
            }
            let mut len = 0u32;
            unsafe { GetTokenInformation(token, class, null_mut(), 0, &mut len) };
            // u64s keep the structure's pointer field aligned.
            let mut buf = vec![0u64; (len as usize).div_ceil(8).max(1)];
            let ok = unsafe {
                GetTokenInformation(token, class, buf.as_mut_ptr().cast(), len, &mut len)
            };
            let err = io::Error::last_os_error();
            unsafe { CloseHandle(token) };
            if ok == 0 {
                return Err(err);
            }
            Ok(TokenSid(buf))
        }

        /// TOKEN_USER and TOKEN_OWNER both start with the SID's pointer.
        fn sid(&self) -> PSID {
            unsafe { *(self.0.as_ptr() as *const PSID) }
        }
    }

    /// Whether owner is one a private file may have: this user, the owner this process gives what
    /// it makes, or SYSTEM.
    fn owner_accepted(
        owner: PSID,
        user: &TokenSid,
        default_owner: &TokenSid,
        system: &System,
    ) -> bool {
        !owner.is_null()
            && unsafe {
                EqualSid(owner, user.sid()) != 0
                    || EqualSid(owner, default_owner.sid()) != 0
                    || EqualSid(owner, system.ptr()) != 0
            }
    }

    /// The SYSTEM account's SID.
    struct System([u64; SECURITY_MAX_SID_SIZE.div_ceil(8) as usize]);

    impl System {
        fn sid() -> io::Result<System> {
            let mut s = System([0; SECURITY_MAX_SID_SIZE.div_ceil(8) as usize]);
            let mut len = SECURITY_MAX_SID_SIZE;
            let ok = unsafe {
                CreateWellKnownSid(
                    WinLocalSystemSid,
                    null_mut(),
                    s.0.as_mut_ptr().cast(),
                    &mut len,
                )
            };
            if ok == 0 {
                return Err(io::Error::last_os_error());
            }
            Ok(s)
        }

        fn ptr(&self) -> PSID {
            self.0.as_ptr() as PSID
        }
    }

    /// Memory the system allocated with LocalAlloc, freed when dropped.
    struct Local<T>(*mut T);

    impl<T> Drop for Local<T> {
        fn drop(&mut self) {
            unsafe { LocalFree(self.0.cast()) };
        }
    }

    /// An access list allowing this user and SYSTEM everything, inherited by what is made in a
    /// folder.
    fn private_acl(user: &TokenSid, system: &System, folder: bool) -> io::Result<Local<ACL>> {
        let inheritance = if folder {
            SUB_CONTAINERS_AND_OBJECTS_INHERIT
        } else {
            NO_INHERITANCE
        };
        let entry = |sid: PSID, kind| EXPLICIT_ACCESS_W {
            grfAccessPermissions: FILE_ALL_ACCESS,
            grfAccessMode: SET_ACCESS,
            grfInheritance: inheritance,
            Trustee: TRUSTEE_W {
                pMultipleTrustee: null_mut(),
                MultipleTrusteeOperation: NO_MULTIPLE_TRUSTEE,
                TrusteeForm: TRUSTEE_IS_SID,
                TrusteeType: kind,
                ptstrName: sid.cast(),
            },
        };
        let entries = [
            entry(user.sid(), TRUSTEE_IS_USER),
            entry(system.ptr(), TRUSTEE_IS_WELL_KNOWN_GROUP),
        ];
        let mut acl: *mut ACL = null_mut();
        win32(unsafe { SetEntriesInAclW(2, entries.as_ptr(), null(), &mut acl) })?;
        Ok(Local(acl))
    }

    /// Runs make with security attributes for a private file or folder: a protected list allowing
    /// only this user and SYSTEM, so it is private from the moment it is made. No owner is named:
    /// it gets the owner this process gives what it makes, as anything it makes does.
    fn with_private<T>(
        folder: bool,
        make: impl FnOnce(*const SECURITY_ATTRIBUTES) -> io::Result<T>,
    ) -> io::Result<T> {
        let user = TokenSid::user()?;
        let system = System::sid()?;
        let acl = private_acl(&user, &system, folder)?;
        let mut sd = SECURITY_DESCRIPTOR::default();
        let psd: PSECURITY_DESCRIPTOR = (&mut sd as *mut SECURITY_DESCRIPTOR).cast();
        ok(unsafe { InitializeSecurityDescriptor(psd, SD_REVISION) })?;
        ok(unsafe { SetSecurityDescriptorDacl(psd, 1, acl.0, 0) })?;
        ok(unsafe { SetSecurityDescriptorControl(psd, SE_DACL_PROTECTED, SE_DACL_PROTECTED) })?;
        let sa = SECURITY_ATTRIBUTES {
            nLength: std::mem::size_of::<SECURITY_ATTRIBUTES>() as u32,
            lpSecurityDescriptor: psd,
            bInheritHandle: 0,
        };
        make(&sa)
    }

    /// The owner of the file open as f, and the descriptor that holds it.
    fn owner_of(f: &File) -> io::Result<(PSID, Local<core::ffi::c_void>)> {
        let mut owner: PSID = null_mut();
        let mut sd: PSECURITY_DESCRIPTOR = null_mut();
        win32(unsafe {
            GetSecurityInfo(
                handle(f),
                SE_FILE_OBJECT,
                OWNER_SECURITY_INFORMATION,
                &mut owner,
                null_mut(),
                null_mut(),
                null_mut(),
                &mut sd,
            )
        })?;
        Ok((owner, Local(sd)))
    }

    /// `INHERIT_ONLY_ACE` (winnt.h): an entry only passed down, not applied to the object itself.
    const INHERIT_ONLY: u8 = 0x08;
    /// The write-type rights: `FILE_WRITE_DATA`, `FILE_APPEND_DATA`, `FILE_DELETE_CHILD`,
    /// `DELETE`, `WRITE_DAC`, `WRITE_OWNER`, `GENERIC_ALL`, `GENERIC_WRITE`.
    const WRITE_RIGHTS: u32 =
        0x2 | 0x4 | 0x40 | 0x1_0000 | 0x4_0000 | 0x8_0000 | 0x1000_0000 | 0x4000_0000;

    /// The Administrators group's SID.
    fn administrators() -> io::Result<System> {
        let mut s = System([0; SECURITY_MAX_SID_SIZE.div_ceil(8) as usize]);
        let mut len = SECURITY_MAX_SID_SIZE;
        ok(unsafe {
            CreateWellKnownSid(
                WinBuiltinAdministratorsSid,
                null_mut(),
                s.0.as_mut_ptr().cast(),
                &mut len,
            )
        })?;
        Ok(s)
    }

    pub fn others_may_write(path: &Path) -> io::Result<bool> {
        let f = open_itself(path, FILE_READ_ATTRIBUTES | READ_CONTROL)?;
        let user = TokenSid::user()?;
        let default_owner = TokenSid::default_owner()?;
        let system = System::sid()?;
        let admins = administrators()?;
        let mut owner: PSID = null_mut();
        let mut dacl: *mut ACL = null_mut();
        let mut sd: PSECURITY_DESCRIPTOR = null_mut();
        win32(unsafe {
            GetSecurityInfo(
                handle(&f),
                SE_FILE_OBJECT,
                OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION,
                &mut owner,
                null_mut(),
                &mut dacl,
                null_mut(),
                &mut sd,
            )
        })?;
        let _sd = Local(sd);
        let trusted = |sid: PSID| unsafe {
            EqualSid(sid, user.sid()) != 0
                || EqualSid(sid, system.ptr()) != 0
                || EqualSid(sid, admins.ptr()) != 0
        };
        if !(owner_accepted(owner, &user, &default_owner, &system)
            || (!owner.is_null() && unsafe { EqualSid(owner, admins.ptr()) } != 0))
            || dacl.is_null()
        {
            return Ok(true);
        }
        for i in 0..u32::from(unsafe { (*dacl).AceCount }) {
            let mut ace: *mut core::ffi::c_void = null_mut();
            if unsafe { GetAce(dacl, i, &mut ace) } == 0 {
                return Ok(true);
            }
            let header = unsafe { &*(ace as *const ACE_HEADER) };
            if header.AceFlags & INHERIT_ONLY != 0 || header.AceType == ACCESS_DENIED {
                continue;
            }
            if header.AceType != ACCESS_ALLOWED {
                return Ok(true);
            }
            let allowed = unsafe { &*(ace as *const ACCESS_ALLOWED_ACE) };
            let sid = unsafe { std::ptr::addr_of!((*(ace as *const ACCESS_ALLOWED_ACE)).SidStart) }
                as PSID;
            if allowed.Mask & WRITE_RIGHTS != 0 && !trusted(sid) {
                return Ok(true);
            }
        }
        Ok(false)
    }

    pub fn is_private(path: &Path) -> io::Result<bool> {
        let f = open_itself(path, FILE_READ_ATTRIBUTES | READ_CONTROL)?;
        let user = TokenSid::user()?;
        let default_owner = TokenSid::default_owner()?;
        let system = System::sid()?;
        let mut owner: PSID = null_mut();
        let mut dacl: *mut ACL = null_mut();
        let mut sd: PSECURITY_DESCRIPTOR = null_mut();
        win32(unsafe {
            GetSecurityInfo(
                handle(&f),
                SE_FILE_OBJECT,
                OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION,
                &mut owner,
                null_mut(),
                &mut dacl,
                null_mut(),
                &mut sd,
            )
        })?;
        let sd = Local(sd);
        let ours = |sid: PSID| unsafe {
            EqualSid(sid, user.sid()) != 0 || EqualSid(sid, system.ptr()) != 0
        };
        // Another owner could change the list; an unprotected list takes up what its folder's
        // passes down later; no list at all allows everyone. The list itself allows only this user
        // and SYSTEM.
        let mut control = 0u16;
        let mut revision = 0u32;
        ok(unsafe { GetSecurityDescriptorControl(sd.0, &mut control, &mut revision) })?;
        if !owner_accepted(owner, &user, &default_owner, &system)
            || control & SE_DACL_PROTECTED == 0
            || dacl.is_null()
        {
            return Ok(false);
        }
        for i in 0..u32::from(unsafe { (*dacl).AceCount }) {
            let mut ace: *mut core::ffi::c_void = null_mut();
            if unsafe { GetAce(dacl, i, &mut ace) } == 0 {
                return Ok(false);
            }
            let header = unsafe { &*(ace as *const ACE_HEADER) };
            // Deny entries take nothing away from privacy; any other kind of allow entry
            // (object, callback, compound) is not one this checks, so it is not private.
            if header.AceType == ACCESS_ALLOWED {
                let sid =
                    unsafe { std::ptr::addr_of!((*(ace as *const ACCESS_ALLOWED_ACE)).SidStart) }
                        as PSID;
                if !ours(sid) {
                    return Ok(false);
                }
            } else if header.AceType != ACCESS_DENIED {
                return Ok(false);
            }
        }
        Ok(true)
    }
}

#[cfg(test)]
mod tests;
