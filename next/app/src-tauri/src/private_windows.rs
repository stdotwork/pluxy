//! Private files and folders on Windows: what the 0600/0700 modes and the owner check give the
//! app on Linux and macOS. The rule is the one the app tools' `lib/files` uses (next/app-tools),
//! so the two can be compared:
//!
//! - the owner is this user, the token's default owner (the owner this process gives what it
//!   makes: its user, or for an elevated administrator the Administrators group) or SYSTEM; any
//!   other owner is refused, never taken over;
//! - the access list exists and is protected (it takes nothing from its folder's);
//! - every allow entry is for this user or SYSTEM, inherit-only ones included; deny entries are
//!   ignored; any other kind of entry is refused;
//! - what the app makes private gets that list as it is made, with no owner named.
//!
//! Every check and change is made through a handle to the file or folder itself, never by a path
//! a link could redirect.

use std::fs::File;
use std::io;
use std::os::windows::io::AsRawHandle;
use std::ptr::{null, null_mut};
use windows_sys::Win32::Foundation::{CloseHandle, LocalFree, ERROR_SUCCESS, HANDLE};
use windows_sys::Win32::Security::Authorization::{
    GetSecurityInfo, SetEntriesInAclW, SetSecurityInfo, EXPLICIT_ACCESS_W, NO_MULTIPLE_TRUSTEE,
    SET_ACCESS, SE_FILE_OBJECT, TRUSTEE_IS_SID, TRUSTEE_IS_USER, TRUSTEE_IS_WELL_KNOWN_GROUP,
    TRUSTEE_W,
};
use windows_sys::Win32::Security::{
    CreateWellKnownSid, EqualSid, GetAce, GetSecurityDescriptorControl, GetTokenInformation,
    InitializeSecurityDescriptor, SetSecurityDescriptorControl, SetSecurityDescriptorDacl,
    TokenOwner, TokenUser, WinLocalSystemSid, ACCESS_ALLOWED_ACE, ACE_HEADER, ACL,
    DACL_SECURITY_INFORMATION, NO_INHERITANCE, OWNER_SECURITY_INFORMATION,
    PROTECTED_DACL_SECURITY_INFORMATION, PSECURITY_DESCRIPTOR, PSID, SECURITY_DESCRIPTOR,
    SECURITY_MAX_SID_SIZE, SE_DACL_PROTECTED, SUB_CONTAINERS_AND_OBJECTS_INHERIT,
    TOKEN_INFORMATION_CLASS, TOKEN_QUERY,
};
use windows_sys::Win32::Storage::FileSystem::FILE_ALL_ACCESS;
use windows_sys::Win32::System::Threading::{GetCurrentProcess, OpenProcessToken};

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

pub(crate) fn another_owner() -> io::Error {
    io::Error::new(io::ErrorKind::PermissionDenied, "owned by another account")
}

/// A SID this process's token names, in the buffer that holds it: its user, or its default owner.
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
        // SAFETY: the pseudo-handle of this process and a valid out pointer.
        if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) } == 0 {
            return Err(io::Error::last_os_error());
        }
        let mut len = 0u32;
        // SAFETY: a size query (no buffer); it fails with the size needed in len.
        unsafe { GetTokenInformation(token, class, null_mut(), 0, &mut len) };
        // u64s keep the structure's pointer field aligned.
        let mut buf = vec![0u64; (len as usize).div_ceil(8).max(1)];
        // SAFETY: buf holds at least len bytes.
        let got =
            unsafe { GetTokenInformation(token, class, buf.as_mut_ptr().cast(), len, &mut len) };
        let err = io::Error::last_os_error();
        // SAFETY: token is the handle OpenProcessToken gave.
        unsafe { CloseHandle(token) };
        if got == 0 {
            return Err(err);
        }
        Ok(TokenSid(buf))
    }

    /// TOKEN_USER and TOKEN_OWNER both start with the SID's pointer, into the same buffer.
    fn sid(&self) -> PSID {
        // SAFETY: the buffer holds a TOKEN_USER or TOKEN_OWNER, whose first field is the PSID.
        unsafe { *(self.0.as_ptr() as *const PSID) }
    }
}

/// The SYSTEM account's SID.
struct System([u64; SECURITY_MAX_SID_SIZE.div_ceil(8) as usize]);

impl System {
    fn sid() -> io::Result<System> {
        let mut s = System([0; SECURITY_MAX_SID_SIZE.div_ceil(8) as usize]);
        let mut len = SECURITY_MAX_SID_SIZE;
        // SAFETY: the buffer is SECURITY_MAX_SID_SIZE bytes, as len says.
        ok(unsafe {
            CreateWellKnownSid(
                WinLocalSystemSid,
                null_mut(),
                s.0.as_mut_ptr().cast(),
                &mut len,
            )
        })?;
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
        // SAFETY: the pointer came from the system's LocalAlloc (or is null, which LocalFree takes).
        unsafe { LocalFree(self.0.cast()) };
    }
}

/// Whether owner is one a private file may have: this user, the owner this process gives what it
/// makes, or SYSTEM.
fn owner_accepted(owner: PSID, user: &TokenSid, default_owner: &TokenSid, system: &System) -> bool {
    !owner.is_null()
        // SAFETY: every SID here is valid while its holder lives.
        && unsafe {
            EqualSid(owner, user.sid()) != 0
                || EqualSid(owner, default_owner.sid()) != 0
                || EqualSid(owner, system.ptr()) != 0
        }
}

/// An access list allowing this user and SYSTEM everything, inherited by what is made in a folder.
fn private_acl(user: &TokenSid, system: &System, folder: bool) -> io::Result<Local<ACL>> {
    acl_of(user, system, folder, FILE_ALL_ACCESS)
}

/// An access list allowing this user and SYSTEM access, inherited by what is made in a folder.
fn acl_of(user: &TokenSid, system: &System, folder: bool, access: u32) -> io::Result<Local<ACL>> {
    let inheritance = if folder {
        SUB_CONTAINERS_AND_OBJECTS_INHERIT
    } else {
        NO_INHERITANCE
    };
    let entry = |sid: PSID, kind| EXPLICIT_ACCESS_W {
        grfAccessPermissions: access,
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
    // SAFETY: two valid entries; the new list is returned in acl (LocalAlloc'd).
    win32(unsafe { SetEntriesInAclW(2, entries.as_ptr(), null(), &mut acl) })?;
    Ok(Local(acl))
}

/// A security descriptor for a private file or folder as it is made: a protected list allowing
/// only this user and SYSTEM, and no owner named (the object gets the owner this process gives
/// what it makes). It must outlive the call that makes the object.
pub(crate) struct Private {
    sd: Box<SECURITY_DESCRIPTOR>,
    _acl: Local<ACL>,
}

impl Private {
    pub(crate) fn new(folder: bool) -> io::Result<Private> {
        let acl = private_acl(&TokenSid::user()?, &System::sid()?, folder)?;
        let mut sd = Box::new(SECURITY_DESCRIPTOR::default());
        let psd: PSECURITY_DESCRIPTOR = (&mut *sd as *mut SECURITY_DESCRIPTOR).cast();
        // SAFETY: psd points at the boxed descriptor, which lives as long as self; acl too.
        ok(unsafe { InitializeSecurityDescriptor(psd, SD_REVISION) })?;
        ok(unsafe { SetSecurityDescriptorDacl(psd, 1, acl.0, 0) })?;
        ok(unsafe { SetSecurityDescriptorControl(psd, SE_DACL_PROTECTED, SE_DACL_PROTECTED) })?;
        Ok(Private { sd, _acl: acl })
    }

    /// The descriptor, for `SECURITY_ATTRIBUTES` or `OBJECT_ATTRIBUTES`.
    pub(crate) fn descriptor(&self) -> PSECURITY_DESCRIPTOR {
        (&*self.sd as *const SECURITY_DESCRIPTOR).cast_mut().cast()
    }
}

/// Whether the owner of the object open as f is one a private object may have (opened with
/// `READ_CONTROL`).
pub(crate) fn owned(f: &File) -> io::Result<bool> {
    let mut owner: PSID = null_mut();
    let mut sd: PSECURITY_DESCRIPTOR = null_mut();
    // SAFETY: f is open; the descriptor comes back LocalAlloc'd, owner points into it.
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
    let _sd = Local(sd);
    Ok(owner_accepted(
        owner,
        &TokenSid::user()?,
        &TokenSid::default_owner()?,
        &System::sid()?,
    ))
}

/// Whether the object open as f (with `READ_CONTROL`) is private by the rule above.
pub(crate) fn is_private(f: &File) -> io::Result<bool> {
    private_without(f, 0)
}

/// Rights that write to a file or folder, add to or remove from it, or change its attributes,
/// its access list or its owner: what `make_read_only` gives no one.
const WRITES: u32 = 0x0002 // FILE_WRITE_DATA, FILE_ADD_FILE
    | 0x0004 // FILE_APPEND_DATA, FILE_ADD_SUBDIRECTORY
    | 0x0010 // FILE_WRITE_EA
    | 0x0040 // FILE_DELETE_CHILD
    | 0x0100 // FILE_WRITE_ATTRIBUTES
    | 0x0001_0000 // DELETE
    | 0x0004_0000 // WRITE_DAC
    | 0x0008_0000 // WRITE_OWNER
    | 0x1000_0000 // GENERIC_ALL
    | 0x4000_0000; // GENERIC_WRITE

/// Whether the object open as f (with `READ_CONTROL`) is as `make_read_only` leaves it: private
/// by the rule above, and no allow entry grants a right that writes to it (`WRITES`). Its owner
/// may still change the list, as on Unix the owner of a 0400 file may chmod it.
pub(crate) fn is_read_only(f: &File) -> io::Result<bool> {
    private_without(f, WRITES)
}

/// Whether the object open as f is private, with no allow entry granting any of forbidden.
fn private_without(f: &File, forbidden: u32) -> io::Result<bool> {
    let user = TokenSid::user()?;
    let default_owner = TokenSid::default_owner()?;
    let system = System::sid()?;
    let mut owner: PSID = null_mut();
    let mut dacl: *mut ACL = null_mut();
    let mut sd: PSECURITY_DESCRIPTOR = null_mut();
    // SAFETY: f is open; owner and dacl point into the LocalAlloc'd descriptor.
    win32(unsafe {
        GetSecurityInfo(
            handle(f),
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
    let ours = |sid: PSID| {
        // SAFETY: both SIDs are valid here.
        unsafe { EqualSid(sid, user.sid()) != 0 || EqualSid(sid, system.ptr()) != 0 }
    };
    // Another owner could change the list; an unprotected list takes up what its folder's passes
    // down later; no list at all allows everyone.
    let mut control = 0u16;
    let mut revision = 0u32;
    // SAFETY: sd is the descriptor GetSecurityInfo gave.
    ok(unsafe { GetSecurityDescriptorControl(sd.0, &mut control, &mut revision) })?;
    if !owner_accepted(owner, &user, &default_owner, &system)
        || control & SE_DACL_PROTECTED == 0
        || dacl.is_null()
    {
        return Ok(false);
    }
    // SAFETY: dacl is a valid list inside sd.
    for i in 0..u32::from(unsafe { (*dacl).AceCount }) {
        let mut ace: *mut core::ffi::c_void = null_mut();
        // SAFETY: i is below the list's entry count.
        if unsafe { GetAce(dacl, i, &mut ace) } == 0 {
            return Ok(false);
        }
        // SAFETY: every entry starts with its header.
        let header = unsafe { &*(ace as *const ACE_HEADER) };
        // Deny entries take nothing away from privacy; any other kind of entry (object,
        // callback, compound) is not one this checks, so the object is not private.
        if header.AceType == ACCESS_ALLOWED {
            // SAFETY: an allow entry is an ACCESS_ALLOWED_ACE, its SID starting at SidStart.
            let sid = unsafe { std::ptr::addr_of!((*(ace as *const ACCESS_ALLOWED_ACE)).SidStart) }
                as PSID;
            // SAFETY: as above.
            let mask = unsafe { (*(ace as *const ACCESS_ALLOWED_ACE)).Mask };
            if !ours(sid) || mask & forbidden != 0 {
                return Ok(false);
            }
        } else if header.AceType != ACCESS_DENIED {
            return Ok(false);
        }
    }
    Ok(true)
}

/// Makes the object open as f (with `READ_CONTROL | WRITE_DAC`) private: a protected list
/// allowing only this user and SYSTEM, a folder's inherited by what is made in it. One owned by
/// another account is refused, never taken over.
pub(crate) fn make_private(f: &File, folder: bool) -> io::Result<()> {
    if !owned(f)? {
        return Err(another_owner());
    }
    let acl = private_acl(&TokenSid::user()?, &System::sid()?, folder)?;
    // SAFETY: f is open with WRITE_DAC; acl is a valid list.
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

/// Makes the object open as f (with `READ_CONTROL | WRITE_DAC`) read-only, the 0444/0500 of an
/// installed package's files and folders on Linux and macOS: a protected list allowing only this
/// user and SYSTEM to read and run it (a folder: list and traverse it), with no right to write,
/// delete or add to it. The owner may still change the list (Windows lets an owner always), as a
/// Unix owner may chmod: `make_private` undoes it. One owned by another account is refused.
pub(crate) fn make_read_only(f: &File, folder: bool) -> io::Result<()> {
    use windows_sys::Win32::Storage::FileSystem::{FILE_GENERIC_EXECUTE, FILE_GENERIC_READ};
    if !owned(f)? {
        return Err(another_owner());
    }
    let acl = acl_of(
        &TokenSid::user()?,
        &System::sid()?,
        folder,
        FILE_GENERIC_READ | FILE_GENERIC_EXECUTE,
    )?;
    // SAFETY: f is open with WRITE_DAC; acl is a valid list.
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

/// Opens path itself (never a link it names: a link is refused) for reading and changing its
/// access list; a folder when folder is set, else a file.
pub(crate) fn open_for_access_list(path: &std::path::Path, folder: bool) -> io::Result<File> {
    use std::os::windows::fs::{MetadataExt, OpenOptionsExt};
    use windows_sys::Win32::Storage::FileSystem::{
        FILE_ATTRIBUTE_REPARSE_POINT, FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT,
        FILE_READ_ATTRIBUTES, READ_CONTROL, WRITE_DAC,
    };
    let f = std::fs::OpenOptions::new()
        .access_mode(FILE_READ_ATTRIBUTES | READ_CONTROL | WRITE_DAC)
        .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS)
        .open(path)?;
    let m = f.metadata()?;
    if m.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 || m.is_dir() != folder {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            if folder {
                "a link or not a folder"
            } else {
                "a link or not a file"
            },
        ));
    }
    Ok(f)
}

/// Makes the folder path and every missing folder above it, each private as it is made (the
/// protected list comes with the descriptor it is made with, so it is never open to others in
/// between), then checks path as `make_private_dir` does: the `mkdir -p -m 0700` of `device.rs`'s
/// `private_dir`. Folders that were there already, above path, are left as they are.
pub(crate) fn create_private_dir_all(path: &std::path::Path) -> io::Result<()> {
    use windows_sys::Win32::Security::SECURITY_ATTRIBUTES;
    use windows_sys::Win32::Storage::FileSystem::CreateDirectoryW;
    let mut missing = Vec::new();
    let mut p = path;
    loop {
        match std::fs::symlink_metadata(p) {
            Ok(_) => break,
            Err(e) if e.kind() == io::ErrorKind::NotFound => {
                missing.push(p);
                match p.parent() {
                    Some(q) if !q.as_os_str().is_empty() => p = q,
                    _ => break,
                }
            }
            Err(e) => return Err(e),
        }
    }
    let private = Private::new(true)?;
    let sa = SECURITY_ATTRIBUTES {
        nLength: std::mem::size_of::<SECURITY_ATTRIBUTES>() as u32,
        lpSecurityDescriptor: private.descriptor(),
        bInheritHandle: 0,
    };
    for dir in missing.iter().rev() {
        #[cfg(test)]
        BEFORE_CREATE.with(|h| {
            if let Some(h) = h.borrow_mut().as_mut() {
                h(dir)
            }
        });
        let name = verbatim(dir)?;
        // SAFETY: name is NUL-terminated; sa and its descriptor live through the call.
        if unsafe { CreateDirectoryW(name.as_ptr(), &sa) } == 0 {
            let e = io::Error::last_os_error();
            if e.kind() != io::ErrorKind::AlreadyExists {
                return Err(e);
            }
            // Made by someone else since the walk: checked before anything is made in it, as
            // the leaf is (a link, a file or another owner's folder is refused, ours made
            // private).
            if *dir != path {
                make_private_dir(dir)?;
            }
        }
    }
    make_private_dir(path)
}

/// path as a NUL-terminated wide string for the calls that make files and folders: absolute, in
/// its `\\?\` form (a share as `\\?\UNC\`), which takes any length, as std's own opens do for
/// a long path. A plain path longer than MAX_PATH fails there with ERROR_PATH_NOT_FOUND.
fn verbatim(path: &std::path::Path) -> io::Result<Vec<u16>> {
    use std::os::windows::ffi::OsStrExt;
    let w: Vec<u16> = std::path::absolute(path)?
        .as_os_str()
        .encode_wide()
        .collect();
    if w.contains(&0) {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "a NUL in the path",
        ));
    }
    let backslash = u16::from(b'\\');
    let prefix: Vec<u16> = if w.starts_with(&[backslash, backslash]) {
        // `\\?\` and `\\.\` already; a share (`\\server\share`) as `\\?\UNC\server\share`.
        if w.get(2)
            .is_some_and(|&c| c == u16::from(b'?') || c == u16::from(b'.'))
        {
            Vec::new()
        } else {
            return Ok(r"\\?\UNC"
                .encode_utf16()
                .chain(w[1..].iter().copied())
                .chain([0])
                .collect());
        }
    } else {
        r"\\?\".encode_utf16().collect()
    };
    Ok(prefix.into_iter().chain(w).chain([0]).collect())
}

/// Creates the file path, which must not exist, for writing, private as it is made (the
/// `O_CREAT|O_EXCL` with 0600 of Linux and macOS): an existing path, a link too, is an
/// `AlreadyExists` error.
pub(crate) fn create_new_private(path: &std::path::Path) -> io::Result<File> {
    use std::os::windows::io::FromRawHandle;
    use windows_sys::Win32::Foundation::{GENERIC_WRITE, INVALID_HANDLE_VALUE};
    use windows_sys::Win32::Security::SECURITY_ATTRIBUTES;
    use windows_sys::Win32::Storage::FileSystem::{
        CreateFileW, CREATE_NEW, FILE_ATTRIBUTE_NORMAL, FILE_SHARE_DELETE, FILE_SHARE_READ,
        FILE_SHARE_WRITE,
    };
    let name = verbatim(path)?;
    let private = Private::new(false)?;
    let sa = SECURITY_ATTRIBUTES {
        nLength: std::mem::size_of::<SECURITY_ATTRIBUTES>() as u32,
        lpSecurityDescriptor: private.descriptor(),
        bInheritHandle: 0,
    };
    // SAFETY: name is NUL-terminated; sa and its descriptor live through the call.
    let h = unsafe {
        CreateFileW(
            name.as_ptr(),
            GENERIC_WRITE,
            FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
            &sa,
            CREATE_NEW,
            FILE_ATTRIBUTE_NORMAL,
            null_mut(),
        )
    };
    if h == INVALID_HANDLE_VALUE {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: h is the handle CreateFileW opened, owned by nothing else.
    Ok(unsafe { File::from_raw_handle(h as _) })
}

/// For the tests: called with each folder `create_private_dir_all` is about to make, to make it
/// first, as another program could between the walk and the making.
#[cfg(test)]
pub(crate) type BeforeCreate = Box<dyn FnMut(&std::path::Path)>;

#[cfg(test)]
thread_local! {
    pub(crate) static BEFORE_CREATE: std::cell::RefCell<Option<BeforeCreate>> =
        const { std::cell::RefCell::new(None) };
}

/// Opens the folder path itself (never a link it names: a link is refused) for checking and
/// changing its access list, and makes it private as `make_private` does when it is not: the
/// `chmod 0700` of `device.rs`'s `private_dir`.
pub(crate) fn make_private_dir(path: &std::path::Path) -> io::Result<()> {
    let f = open_for_access_list(path, true)?;
    if is_private(&f)? {
        return Ok(());
    }
    make_private(&f, true)
}

#[cfg(test)]
#[path = "private_windows_tests.rs"]
pub(crate) mod tests;
