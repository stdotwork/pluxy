//! Private files and folders on Windows, as `next/app-tools/lib/files` makes them (its Windows
//! half, copied: the app tools are a workspace of their own, never linked into the app). Keep the
//! two identical.
//!
//! A private file or folder is owned by this user, by the owner this process gives what it makes
//! (its user, or for an elevated administrator the Administrators group), or by SYSTEM, and
//! carries a protected access list (one that takes nothing from its folder's) that allows only
//! this user and SYSTEM. A folder's list is inherited by what is made inside it; what is made that
//! way is not private until it is made so, since its list is not protected. A private file or
//! folder is made with its list in one step, so it is never open to anyone else, even for a
//! moment.
use std::fs::{self, File, Metadata};
use std::io;
use std::os::windows::ffi::OsStrExt;
use std::os::windows::fs::{MetadataExt, OpenOptionsExt};
use std::os::windows::io::{AsRawHandle, FromRawHandle};
use std::path::Path;
use std::ptr::{null, null_mut};
use windows_sys::Win32::Foundation::{
    CloseHandle, LocalFree, ERROR_SUCCESS, GENERIC_WRITE, HANDLE, INVALID_HANDLE_VALUE,
};
use windows_sys::Win32::Security::Authorization::{
    GetSecurityInfo, SetEntriesInAclW, SetSecurityInfo, EXPLICIT_ACCESS_W, NO_MULTIPLE_TRUSTEE,
    SET_ACCESS, SE_FILE_OBJECT, TRUSTEE_IS_SID, TRUSTEE_IS_USER, TRUSTEE_IS_WELL_KNOWN_GROUP,
    TRUSTEE_W,
};
use windows_sys::Win32::Security::{
    CreateWellKnownSid, EqualSid, GetTokenInformation, InitializeSecurityDescriptor,
    SetSecurityDescriptorControl, SetSecurityDescriptorDacl, TokenOwner, TokenUser,
    WinLocalSystemSid, ACL, DACL_SECURITY_INFORMATION, NO_INHERITANCE, OWNER_SECURITY_INFORMATION,
    PROTECTED_DACL_SECURITY_INFORMATION, PSECURITY_DESCRIPTOR, PSID, SECURITY_ATTRIBUTES,
    SECURITY_DESCRIPTOR, SECURITY_MAX_SID_SIZE, SE_DACL_PROTECTED,
    SUB_CONTAINERS_AND_OBJECTS_INHERIT, TOKEN_INFORMATION_CLASS, TOKEN_QUERY,
};
use windows_sys::Win32::Storage::FileSystem::{
    CreateDirectoryW, CreateFileW, CREATE_NEW, FILE_ALL_ACCESS, FILE_ATTRIBUTE_NORMAL,
    FILE_ATTRIBUTE_REPARSE_POINT, FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT,
    FILE_READ_ATTRIBUTES, FILE_SHARE_DELETE, FILE_SHARE_READ, FILE_SHARE_WRITE, READ_CONTROL,
    WRITE_DAC,
};
use windows_sys::Win32::System::Threading::{GetCurrentProcess, OpenProcessToken};

/// `SECURITY_DESCRIPTOR_REVISION` (winnt.h).
const SD_REVISION: u32 = 1;

fn another_owner() -> io::Error {
    io::Error::new(io::ErrorKind::PermissionDenied, "owned by another account")
}

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

/// path itself, a link or not, opened to read its attributes (and its access list).
fn open_itself(path: &Path, access: u32) -> io::Result<File> {
    fs::OpenOptions::new()
        .access_mode(access)
        .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS)
        .open(path)
}

/// Makes the folder path, private (as `mkdir` with 0700). Its parent must exist; an existing path
/// is an `AlreadyExists` error.
pub(crate) fn create_private_dir(path: &Path) -> io::Result<()> {
    let name = wide(path)?;
    with_private(true, |sa| {
        ok(unsafe { CreateDirectoryW(name.as_ptr(), sa) })
    })
}

/// Creates the file path, which must not exist, for writing, private (as `O_CREAT|O_EXCL` with
/// 0600). In a folder whose inherited entries deny OWNER RIGHTS the right to change an access
/// list, this (and [`create_private_dir`]) is refused with `PermissionDenied`: that folder forbids
/// whoever makes something in it to set its list.
pub(crate) fn create_new_private(path: &Path) -> io::Result<File> {
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

/// Makes the existing file or folder path private, through a handle that is path itself, not a
/// link it names: a protected access list allowing only this user and SYSTEM (a folder's
/// inherited by what is made in it). A link, and one owned by an account a private file may not
/// have, are refused, never taken over.
pub(crate) fn make_private(path: &Path) -> io::Result<()> {
    let f = open_itself(path, FILE_READ_ATTRIBUTES | READ_CONTROL | WRITE_DAC)?;
    let m = f.metadata()?;
    refuse_link(&m)?;
    let user = TokenSid::user()?;
    let default_owner = TokenSid::default_owner()?;
    let system = System::sid()?;
    // Another account's file is refused, never taken over, as on Linux and macOS.
    let (owner, _sd) = owner_of(&f)?;
    if !owner_accepted(owner, &user, &default_owner, &system) {
        return Err(another_owner());
    }
    let acl = private_acl(&user, &system, m.is_dir())?;
    win32(unsafe {
        SetSecurityInfo(
            handle(&f),
            SE_FILE_OBJECT,
            DACL_SECURITY_INFORMATION | PROTECTED_DACL_SECURITY_INFORMATION,
            null_mut(),
            null_mut(),
            acl.0,
            null(),
        )
    })
}

/// A SID this process's token names, in the buffer that holds it: its user, or its default owner
/// (the owner it gives what it makes: its user, or for an elevated administrator the
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
        let ok =
            unsafe { GetTokenInformation(token, class, buf.as_mut_ptr().cast(), len, &mut len) };
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

/// Whether owner is one a private file may have: this user, the owner this process gives what it
/// makes, or SYSTEM.
fn owner_accepted(owner: PSID, user: &TokenSid, default_owner: &TokenSid, system: &System) -> bool {
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
/// only this user and SYSTEM, so it is private from the moment it is made. No owner is named: it
/// gets the owner this process gives what it makes, as anything it makes does.
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

/// Whether the file or folder path (not followed if it is a link) is private already: this user,
/// the owner this process gives what it makes, or SYSTEM owns it, its access list exists and is
/// protected, every allow entry in it is for this user or SYSTEM (inherit-only ones included),
/// deny entries are ignored, and it holds no other kind of entry. For the tests: the app checks
/// an open file with `confirmation_windows.rs`.
#[cfg(test)]
pub(crate) fn is_private(path: &Path) -> io::Result<bool> {
    use windows_sys::Win32::Security::{
        GetAce, GetSecurityDescriptorControl, ACCESS_ALLOWED_ACE, ACE_HEADER,
    };
    /// `ACCESS_ALLOWED_ACE_TYPE` and `ACCESS_DENIED_ACE_TYPE` (winnt.h).
    const ACCESS_ALLOWED: u8 = 0;
    const ACCESS_DENIED: u8 = 1;
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
    let ours =
        |sid: PSID| unsafe { EqualSid(sid, user.sid()) != 0 || EqualSid(sid, system.ptr()) != 0 };
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
        if header.AceType == ACCESS_ALLOWED {
            let sid = unsafe { std::ptr::addr_of!((*(ace as *const ACCESS_ALLOWED_ACE)).SidStart) }
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

#[cfg(test)]
mod tests {
    use super::*;

    fn temp() -> std::path::PathBuf {
        let d = std::env::temp_dir().join(format!("pluxy-files-{}", crate::managed::random_key()));
        fs::create_dir(&d).unwrap();
        d
    }

    #[test]
    fn made_private_from_the_start() {
        let dir = temp();
        // The control: what the temporary folder passes down is not protected.
        let plain = dir.join("plain");
        fs::write(&plain, b"").unwrap();
        assert!(!is_private(&plain).unwrap());
        let file = dir.join("file");
        drop(create_new_private(&file).unwrap());
        assert!(is_private(&file).unwrap());
        assert_eq!(
            create_new_private(&file).unwrap_err().kind(),
            io::ErrorKind::AlreadyExists
        );
        let folder = dir.join("folder");
        create_private_dir(&folder).unwrap();
        assert!(is_private(&folder).unwrap());
        assert_eq!(
            create_private_dir(&folder).unwrap_err().kind(),
            io::ErrorKind::AlreadyExists
        );
        make_private(&plain).unwrap();
        assert!(is_private(&plain).unwrap());
        let target = dir.join("target");
        fs::write(&target, b"").unwrap();
        let link = dir.join("link");
        match std::os::windows::fs::symlink_file(&target, &link) {
            Ok(()) => {
                assert_eq!(
                    make_private(&link).unwrap_err().kind(),
                    io::ErrorKind::InvalidInput
                );
                assert!(!is_private(&target).unwrap());
            }
            // Creating a symbolic link needs a privilege (or developer mode) this account may lack.
            Err(e) => eprintln!("SKIP symbolic link: {e}"),
        }
        fs::remove_dir_all(dir).unwrap();
    }
}
