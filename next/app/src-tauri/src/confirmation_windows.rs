//! The confirmations' file identities and their settings file's privacy on Windows: what
//! `std::os::unix` and `libc` give `confirmation.rs` on Linux and macOS.
//!
//! A file's identity is its volume serial number and 64-bit file index (`ino`), as
//! `GetFileInformationByHandle` reports them; its metadata adds size, last-write and change times
//! and attributes. The settings folder and file are private when this user, the owner this process
//! gives what it makes (its user, or for an elevated administrator the Administrators group) or
//! SYSTEM owns them, and their access list is protected (takes nothing from their folder's) and
//! allows no one but this user and SYSTEM: the same rule as `next/app-tools/lib/files`.
use std::fs::{File, OpenOptions};
use std::io;
use std::os::windows::fs::OpenOptionsExt;
use std::os::windows::io::AsRawHandle;
use std::path::Path;
use windows::core::{HSTRING, PWSTR};
use windows::Win32::Foundation::{LocalFree, ERROR_SUCCESS, HANDLE, HLOCAL};
use windows::Win32::Security::Authorization::{
    GetSecurityInfo, SetEntriesInAclW, SetNamedSecurityInfoW, EXPLICIT_ACCESS_W, SET_ACCESS,
    SE_FILE_OBJECT, TRUSTEE_IS_SID, TRUSTEE_IS_UNKNOWN, TRUSTEE_W,
};
use windows::Win32::Security::{
    CreateWellKnownSid, EqualSid, GetAce, GetSecurityDescriptorControl, GetTokenInformation,
    TokenOwner, TokenUser, WinLocalSystemSid, ACE_FLAGS, ACE_HEADER, ACL,
    DACL_SECURITY_INFORMATION, NO_INHERITANCE, OWNER_SECURITY_INFORMATION,
    PROTECTED_DACL_SECURITY_INFORMATION, PSECURITY_DESCRIPTOR, PSID, SE_DACL_PROTECTED,
    SUB_CONTAINERS_AND_OBJECTS_INHERIT, TOKEN_INFORMATION_CLASS, TOKEN_OWNER, TOKEN_QUERY,
    TOKEN_USER, WELL_KNOWN_SID_TYPE,
};
use windows::Win32::Storage::FileSystem::{
    FileBasicInfo, GetFileInformationByHandle, GetFileInformationByHandleEx,
    BY_HANDLE_FILE_INFORMATION, FILE_ATTRIBUTE_DIRECTORY, FILE_ATTRIBUTE_REPARSE_POINT,
    FILE_BASIC_INFO, FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT,
};
use windows::Win32::System::Threading::{GetCurrentProcess, OpenProcessToken};

/// What `Metadata` holds on Windows: volume, file index, size, last-write time, change time and
/// attributes (in the fields Unix fills with dev, ino, size, mtime, ctime and mode).
pub(super) struct Info {
    pub dev: u64,
    pub ino: u64,
    pub size: u64,
    pub mtime: i64,
    pub ctime: i64,
    pub attributes: u32,
}

fn handle(f: &File) -> HANDLE {
    HANDLE(f.as_raw_handle())
}

pub(super) fn info(f: &File) -> Result<Info, String> {
    let mut by = BY_HANDLE_FILE_INFORMATION::default();
    unsafe { GetFileInformationByHandle(handle(f), &mut by) }.map_err(|e| e.to_string())?;
    let mut basic = FILE_BASIC_INFO::default();
    unsafe {
        GetFileInformationByHandleEx(
            handle(f),
            FileBasicInfo,
            &mut basic as *mut FILE_BASIC_INFO as *mut core::ffi::c_void,
            std::mem::size_of::<FILE_BASIC_INFO>() as u32,
        )
    }
    .map_err(|e| e.to_string())?;
    Ok(Info {
        dev: u64::from(by.dwVolumeSerialNumber),
        ino: (u64::from(by.nFileIndexHigh) << 32) | u64::from(by.nFileIndexLow),
        size: (u64::from(by.nFileSizeHigh) << 32) | u64::from(by.nFileSizeLow),
        mtime: basic.LastWriteTime,
        ctime: basic.ChangeTime,
        attributes: by.dwFileAttributes,
    })
}

/// Opens path (a file or a folder) for reading its identity, following links as Unix's `open`
/// does; the share mode lets others go on writing, renaming and deleting it, as on Unix.
pub(super) fn open_any(path: &Path) -> io::Result<File> {
    OpenOptions::new()
        .read(true)
        .custom_flags(FILE_FLAG_BACKUP_SEMANTICS.0)
        .open(path)
}

/// Opens path without following a final link (the `O_NOFOLLOW` of Unix), for the settings file.
pub(super) fn open_no_follow(path: &Path) -> io::Result<File> {
    let f = OpenOptions::new()
        .read(true)
        .custom_flags(FILE_FLAG_BACKUP_SEMANTICS.0 | FILE_FLAG_OPEN_REPARSE_POINT.0)
        .open(path)?;
    let a = info(&f).map_err(io::Error::other)?.attributes;
    if a & FILE_ATTRIBUTE_REPARSE_POINT.0 != 0 {
        return Err(io::Error::other(
            "links and other reparse points are refused",
        ));
    }
    Ok(f)
}

/// Whether a regular file is executable here: Windows has no execute bit, so its type is its
/// extension, one CreateProcess runs directly (`exe`, `com`) or through cmd (`bat`, `cmd`).
pub(super) fn executable(path: &Path) -> bool {
    path.extension().and_then(|e| e.to_str()).is_some_and(|e| {
        ["exe", "com", "bat", "cmd"]
            .iter()
            .any(|x| e.eq_ignore_ascii_case(x))
    })
}

pub(super) fn is_dir(attributes: u32) -> bool {
    attributes & FILE_ATTRIBUTE_DIRECTORY.0 != 0
}

/// A SID held in its own buffer.
struct Sid(Vec<u8>);
impl Sid {
    fn psid(&self) -> PSID {
        PSID(self.0.as_ptr() as *mut core::ffi::c_void)
    }
    fn well_known(kind: WELL_KNOWN_SID_TYPE) -> Result<Sid, String> {
        let mut len = 68u32; // SECURITY_MAX_SID_SIZE
        let mut b = vec![0u8; len as usize];
        unsafe { CreateWellKnownSid(kind, None, Some(PSID(b.as_mut_ptr() as _)), &mut len) }
            .map_err(|e| e.to_string())?;
        Ok(Sid(b))
    }
    /// This process's user.
    fn user() -> Result<Sid, String> {
        Self::of_token(TokenUser, |b| unsafe {
            (*(b as *const TOKEN_USER)).User.Sid
        })
    }
    /// The owner this process gives what it makes: its user, or for an elevated administrator
    /// the Administrators group.
    fn default_owner() -> Result<Sid, String> {
        Self::of_token(TokenOwner, |b| unsafe {
            (*(b as *const TOKEN_OWNER)).Owner
        })
    }
    /// A SID from this process's token information of class, which sid finds in the buffer.
    fn of_token(class: TOKEN_INFORMATION_CLASS, sid: fn(*const u8) -> PSID) -> Result<Sid, String> {
        let mut token = HANDLE::default();
        unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) }
            .map_err(|e| e.to_string())?;
        let token = Owned(token);
        let mut len = 0u32;
        let _ = unsafe { GetTokenInformation(token.0, class, None, 0, &mut len) };
        // Aligned for the structure at its start.
        let mut b = vec![0u64; (len as usize).div_ceil(8)];
        unsafe { GetTokenInformation(token.0, class, Some(b.as_mut_ptr() as _), len, &mut len) }
            .map_err(|e| e.to_string())?;
        let sid = sid(b.as_ptr() as *const u8);
        let n = unsafe { windows::Win32::Security::GetLengthSid(sid) } as usize;
        Ok(Sid(unsafe {
            std::slice::from_raw_parts(sid.0 as *const u8, n)
        }
        .to_vec()))
    }
}

struct Owned(HANDLE);
impl Drop for Owned {
    fn drop(&mut self) {
        let _ = unsafe { windows::Win32::Foundation::CloseHandle(self.0) };
    }
}

/// A security descriptor the system allocated, freed with LocalFree.
struct Descriptor(PSECURITY_DESCRIPTOR);
impl Drop for Descriptor {
    fn drop(&mut self) {
        unsafe {
            LocalFree(Some(HLOCAL(self.0 .0)));
        }
    }
}

const ACCESS_ALLOWED: u8 = 0;
const ACCESS_DENIED: u8 = 1;

/// The access-control entry layout of `ACCESS_ALLOWED_ACE` (header, mask, then the SID).
#[repr(C)]
struct AllowedAce {
    header: ACE_HEADER,
    mask: u32,
    sid_start: u32,
}

/// Refused unless f is private: owned by this user, by the owner this process gives what it makes
/// or by SYSTEM, with a protected access list whose allow entries are only this user's and
/// SYSTEM's (what the mode check refuses on Unix: another owner, or group or world bits).
pub(super) fn private(f: &File, what: &str) -> Result<(), String> {
    let mut owner = PSID::default();
    let mut dacl: *mut ACL = std::ptr::null_mut();
    let mut sd = PSECURITY_DESCRIPTOR::default();
    let r = unsafe {
        GetSecurityInfo(
            handle(f),
            SE_FILE_OBJECT,
            OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION,
            Some(&mut owner),
            None,
            Some(&mut dacl),
            None,
            Some(&mut sd),
        )
    };
    if r != ERROR_SUCCESS {
        return Err(format!("{what}: could not read its permissions ({})", r.0));
    }
    let _sd = Descriptor(sd);
    let user = Sid::user()?;
    let system = Sid::well_known(WinLocalSystemSid)?;
    let owners = [&user, &Sid::default_owner()?, &system];
    if owner.is_invalid()
        || !owners
            .iter()
            .any(|s| unsafe { EqualSid(owner, s.psid()) }.is_ok())
    {
        return Err(format!("{what} is not owned by this user"));
    }
    // An unprotected list takes up what its folder's passes down later; no list allows everyone.
    let mut control = 0u16;
    let mut revision = 0u32;
    unsafe { GetSecurityDescriptorControl(sd, &mut control, &mut revision) }
        .map_err(|e| e.to_string())?;
    if control & SE_DACL_PROTECTED.0 == 0 {
        return Err(format!("{what} takes its access list from its folder"));
    }
    if dacl.is_null() {
        return Err(format!("{what} has no access list, so anyone may write it"));
    }
    let count = unsafe { (*dacl).AceCount };
    for i in 0..u32::from(count) {
        let mut ace: *mut core::ffi::c_void = std::ptr::null_mut();
        unsafe { GetAce(dacl, i, &mut ace) }.map_err(|e| e.to_string())?;
        let header = unsafe { &*(ace as *const ACE_HEADER) };
        // Deny entries take nothing away from privacy; an allow entry of another kind (object,
        // callback, compound) is not one this reads, so it is refused.
        match header.AceType {
            ACCESS_DENIED => continue,
            ACCESS_ALLOWED => {}
            _ => {
                return Err(format!(
                    "{what} has an access entry of a kind this app does not read"
                ))
            }
        }
        let ace = unsafe { &*(ace as *const AllowedAce) };
        let sid = PSID(&ace.sid_start as *const u32 as *mut core::ffi::c_void);
        if ![&user, &system]
            .iter()
            .any(|a| unsafe { EqualSid(sid, a.psid()) }.is_ok())
        {
            return Err(format!("{what} may be opened by another user or group"));
        }
    }
    Ok(())
}

/// Makes the file or folder path private: a protected access list of its own allowing this user
/// and SYSTEM full control, a folder's inherited by what is made inside it (the 0700 or 0600 of
/// Unix). What a folder passes down is not protected: each file is made private itself.
pub(super) fn protect(path: &Path, folder: bool) -> Result<(), String> {
    let inheritance: ACE_FLAGS = if folder {
        SUB_CONTAINERS_AND_OBJECTS_INHERIT
    } else {
        NO_INHERITANCE
    };
    let sids = [Sid::user()?, Sid::well_known(WinLocalSystemSid)?];
    let entries: Vec<EXPLICIT_ACCESS_W> = sids
        .iter()
        .map(|s| EXPLICIT_ACCESS_W {
            grfAccessPermissions: 0x1000_0000, // GENERIC_ALL
            grfAccessMode: SET_ACCESS,
            grfInheritance: inheritance,
            Trustee: TRUSTEE_W {
                TrusteeForm: TRUSTEE_IS_SID,
                TrusteeType: TRUSTEE_IS_UNKNOWN,
                ptstrName: PWSTR(s.psid().0 as *mut u16),
                ..Default::default()
            },
        })
        .collect();
    let mut acl: *mut ACL = std::ptr::null_mut();
    let r = unsafe { SetEntriesInAclW(Some(&entries), None, &mut acl) };
    if r != ERROR_SUCCESS {
        return Err(format!(
            "could not make the settings' access list ({})",
            r.0
        ));
    }
    let r = unsafe {
        SetNamedSecurityInfoW(
            &HSTRING::from(path.as_os_str()),
            SE_FILE_OBJECT,
            DACL_SECURITY_INFORMATION | PROTECTED_DACL_SECURITY_INFORMATION,
            None,
            None,
            Some(acl),
            None,
        )
    };
    unsafe {
        LocalFree(Some(HLOCAL(acl as *mut core::ffi::c_void)));
    }
    if r != ERROR_SUCCESS {
        return Err(format!("could not make the settings private ({})", r.0));
    }
    Ok(())
}

/// Adds an entry letting Everyone write path: for the tests of refusing such a file or folder.
#[cfg(test)]
pub(super) fn let_everyone_write(path: &Path) {
    let everyone = Sid::well_known(windows::Win32::Security::WinWorldSid).unwrap();
    let mut old: *mut ACL = std::ptr::null_mut();
    let mut sd = PSECURITY_DESCRIPTOR::default();
    let f = open_any(path).unwrap();
    let r = unsafe {
        GetSecurityInfo(
            handle(&f),
            SE_FILE_OBJECT,
            DACL_SECURITY_INFORMATION,
            None,
            None,
            Some(&mut old),
            None,
            Some(&mut sd),
        )
    };
    assert_eq!(r, ERROR_SUCCESS);
    let _sd = Descriptor(sd);
    let entry = EXPLICIT_ACCESS_W {
        grfAccessPermissions: 0x4000_0000, // GENERIC_WRITE
        grfAccessMode: windows::Win32::Security::Authorization::GRANT_ACCESS,
        grfInheritance: NO_INHERITANCE,
        Trustee: TRUSTEE_W {
            TrusteeForm: TRUSTEE_IS_SID,
            TrusteeType: TRUSTEE_IS_UNKNOWN,
            ptstrName: PWSTR(everyone.psid().0 as *mut u16),
            ..Default::default()
        },
    };
    let mut acl: *mut ACL = std::ptr::null_mut();
    assert_eq!(
        unsafe { SetEntriesInAclW(Some(&[entry]), Some(old), &mut acl) },
        ERROR_SUCCESS
    );
    drop(f);
    let r = unsafe {
        SetNamedSecurityInfoW(
            &HSTRING::from(path.as_os_str()),
            SE_FILE_OBJECT,
            DACL_SECURITY_INFORMATION,
            None,
            None,
            Some(acl),
            None,
        )
    };
    unsafe {
        LocalFree(Some(HLOCAL(acl as *mut core::ffi::c_void)));
    }
    assert_eq!(r, ERROR_SUCCESS);
}
