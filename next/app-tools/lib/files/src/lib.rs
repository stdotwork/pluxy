//! The file rules the app tools share on Linux, macOS and Windows.
//!
//! - **Private** files and folders are this user's alone: on Linux and macOS a file is 0600 and a
//!   folder 0700; on Windows each carries a protected access list that allows only this user and
//!   SYSTEM (the system account, whose reach matches root's), and a folder's list is inherited by
//!   what is made inside it.
//! - [`open_regular`] opens a file for reading without following a link and only if it is a
//!   regular file, checked on the open handle: on Linux and macOS with `O_NOFOLLOW` and
//!   `O_NONBLOCK` (a FIFO is refused, never waited on); on Windows the link itself is opened
//!   (`FILE_FLAG_OPEN_REPARSE_POINT`) and refused, as is anything that is not a file on a disk.
//! - [`Id`] tells files apart: the device and inode on Linux and macOS, the volume serial number
//!   and 128-bit file ID on Windows.
//! - [`mtime_ns`] is a file's modification time in nanoseconds since the Unix epoch, the same
//!   value on every platform.

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
/// 0600).
pub fn create_new_private(path: &Path) -> io::Result<File> {
    imp::create_new_private(path)
}

/// Makes the existing file or folder path private: 0600 or 0700 on Linux and macOS; on Windows
/// a protected access list allowing only this user and SYSTEM (a folder's inherited by what is made
/// in it). path itself must not be a link.
pub fn make_private(path: &Path) -> io::Result<()> {
    imp::make_private(path)
}

/// Whether the file or folder path (not followed if it is a link) is private already: on Linux and
/// macOS no group or other permission bit is set; on Windows its access list allows no one but this
/// user and SYSTEM.
pub fn is_private(path: &Path) -> io::Result<bool> {
    imp::is_private(path)
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

    pub fn make_private(path: &Path) -> io::Result<()> {
        let m = fs::symlink_metadata(path)?;
        if m.file_type().is_symlink() {
            return Err(io::Error::new(io::ErrorKind::InvalidInput, "a link"));
        }
        let mode = if m.is_dir() { 0o700 } else { 0o600 };
        fs::set_permissions(path, fs::Permissions::from_mode(mode))
    }

    pub fn is_private(path: &Path) -> io::Result<bool> {
        Ok(fs::symlink_metadata(path)?.permissions().mode() & 0o077 == 0)
    }
}

#[cfg(windows)]
mod imp {
    use super::*;
    use std::os::windows::fs::{MetadataExt, OpenOptionsExt};
    use std::os::windows::io::AsRawHandle;
    use std::ptr::{null, null_mut};
    use windows_sys::Win32::Foundation::{CloseHandle, LocalFree, ERROR_SUCCESS, HANDLE};
    use windows_sys::Win32::Security::Authorization::{
        GetSecurityInfo, SetEntriesInAclW, SetSecurityInfo, EXPLICIT_ACCESS_W, NO_MULTIPLE_TRUSTEE,
        SET_ACCESS, SE_FILE_OBJECT, TRUSTEE_IS_SID, TRUSTEE_IS_USER, TRUSTEE_IS_WELL_KNOWN_GROUP,
        TRUSTEE_W,
    };
    use windows_sys::Win32::Security::{
        CreateWellKnownSid, EqualSid, GetAce, GetTokenInformation, TokenUser, WinLocalSystemSid,
        ACCESS_ALLOWED_ACE, ACE_HEADER, ACL, DACL_SECURITY_INFORMATION, NO_INHERITANCE,
        PROTECTED_DACL_SECURITY_INFORMATION, PSECURITY_DESCRIPTOR, PSID, SECURITY_MAX_SID_SIZE,
        SUB_CONTAINERS_AND_OBJECTS_INHERIT, TOKEN_QUERY, TOKEN_USER,
    };
    use windows_sys::Win32::Storage::FileSystem::{
        FileIdInfo, GetFileInformationByHandleEx, GetFileType, FILE_ALL_ACCESS,
        FILE_ATTRIBUTE_REPARSE_POINT, FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT,
        FILE_GENERIC_WRITE, FILE_ID_INFO, FILE_READ_ATTRIBUTES, FILE_TYPE_DISK, READ_CONTROL,
        WRITE_DAC,
    };
    use windows_sys::Win32::System::Threading::{GetCurrentProcess, OpenProcessToken};

    /// `ACCESS_ALLOWED_ACE_TYPE` and `ACCESS_DENIED_ACE_TYPE` (winnt.h).
    const ACCESS_ALLOWED: u8 = 0;
    const ACCESS_DENIED: u8 = 1;

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

    fn refuse_link(m: &Metadata) -> io::Result<()> {
        if m.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
            Err(io::Error::new(io::ErrorKind::InvalidInput, "a link"))
        } else {
            Ok(())
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
        let ok = unsafe {
            GetFileInformationByHandleEx(
                handle(file),
                FileIdInfo,
                (&mut info as *mut FILE_ID_INFO).cast(),
                std::mem::size_of::<FILE_ID_INFO>() as u32,
            )
        };
        if ok == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(Id {
            volume: info.VolumeSerialNumber,
            file: u128::from_le_bytes(info.FileId.Identifier),
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
        fs::create_dir(path)?;
        make_private(path)
    }

    pub fn create_private_dir_all(path: &Path) -> io::Result<()> {
        fs::create_dir_all(path)
    }

    pub fn create_new_private(path: &Path) -> io::Result<File> {
        let file = fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .access_mode(FILE_GENERIC_WRITE | WRITE_DAC | READ_CONTROL)
            .open(path)?;
        set_private(&file, false)?;
        Ok(file)
    }

    pub fn make_private(path: &Path) -> io::Result<()> {
        let f = open_itself(path, FILE_READ_ATTRIBUTES | READ_CONTROL | WRITE_DAC)?;
        let m = f.metadata()?;
        refuse_link(&m)?;
        set_private(&f, m.is_dir())
    }

    /// The user this process runs as: its token's user SID, in a buffer that holds it.
    struct User(Vec<u64>);

    impl User {
        fn current() -> io::Result<User> {
            let mut token: HANDLE = null_mut();
            if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) } == 0 {
                return Err(io::Error::last_os_error());
            }
            let mut len = 0u32;
            unsafe { GetTokenInformation(token, TokenUser, null_mut(), 0, &mut len) };
            // u64s keep TOKEN_USER's pointer field aligned.
            let mut buf = vec![0u64; (len as usize).div_ceil(8).max(1)];
            let ok = unsafe {
                GetTokenInformation(token, TokenUser, buf.as_mut_ptr().cast(), len, &mut len)
            };
            let err = io::Error::last_os_error();
            unsafe { CloseHandle(token) };
            if ok == 0 {
                return Err(err);
            }
            Ok(User(buf))
        }

        fn sid(&self) -> PSID {
            unsafe { (*(self.0.as_ptr() as *const TOKEN_USER)).User.Sid }
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

        fn ptr(&mut self) -> PSID {
            self.0.as_mut_ptr().cast()
        }
    }

    fn set_private(f: &File, folder: bool) -> io::Result<()> {
        let user = User::current()?;
        let mut system = System::sid()?;
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
        let set = win32(unsafe {
            SetSecurityInfo(
                handle(f),
                SE_FILE_OBJECT,
                DACL_SECURITY_INFORMATION | PROTECTED_DACL_SECURITY_INFORMATION,
                null_mut(),
                null_mut(),
                acl,
                null(),
            )
        });
        unsafe { LocalFree(acl.cast()) };
        set
    }

    pub fn is_private(path: &Path) -> io::Result<bool> {
        let f = open_itself(path, FILE_READ_ATTRIBUTES | READ_CONTROL)?;
        let user = User::current()?;
        let mut system = System::sid()?;
        let mut dacl: *mut ACL = null_mut();
        let mut sd: PSECURITY_DESCRIPTOR = null_mut();
        win32(unsafe {
            GetSecurityInfo(
                handle(&f),
                SE_FILE_OBJECT,
                DACL_SECURITY_INFORMATION,
                null_mut(),
                null_mut(),
                &mut dacl,
                null_mut(),
                &mut sd,
            )
        })?;
        // No access list at all allows everyone.
        let mut private = !dacl.is_null();
        if private {
            for i in 0..u32::from(unsafe { (*dacl).AceCount }) {
                let mut ace: *mut core::ffi::c_void = null_mut();
                if unsafe { GetAce(dacl, i, &mut ace) } == 0 {
                    private = false;
                    break;
                }
                let header = unsafe { &*(ace as *const ACE_HEADER) };
                // Deny entries take nothing away from privacy; any other kind of allow entry
                // (object, callback, compound) is not one this checks, so it is not private.
                if header.AceType == ACCESS_ALLOWED {
                    let sid = unsafe {
                        std::ptr::addr_of!((*(ace as *const ACCESS_ALLOWED_ACE)).SidStart)
                    } as PSID;
                    if unsafe { EqualSid(sid, user.sid()) == 0 && EqualSid(sid, system.ptr()) == 0 }
                    {
                        private = false;
                        break;
                    }
                } else if header.AceType != ACCESS_DENIED {
                    private = false;
                    break;
                }
            }
        }
        unsafe { LocalFree(sd.cast()) };
        Ok(private)
    }
}

#[cfg(test)]
mod tests;
