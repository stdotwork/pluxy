//! Windows: whether a file or folder is this user's own and no one else's, from its owner and
//! access list, the way unix code reads an owner uid and mode bits. SYSTEM and the
//! Administrators group may be in the list: they can take any file on the machine anyway.
use std::fs::OpenOptions;
use std::os::windows::fs::{MetadataExt, OpenOptionsExt};
use std::os::windows::io::AsRawHandle;
use std::path::Path;
use std::ptr::null_mut;
use windows_sys::Win32::Foundation::{CloseHandle, LocalFree, HANDLE};
use windows_sys::Win32::Security::Authorization::{GetSecurityInfo, SE_FILE_OBJECT};
use windows_sys::Win32::Security::{
    AclSizeInformation, EqualSid, GetAce, GetAclInformation, GetTokenInformation, IsWellKnownSid,
    TokenOwner, TokenUser, WinBuiltinAdministratorsSid, WinLocalSystemSid, ACCESS_ALLOWED_ACE,
    ACE_HEADER, ACL, ACL_SIZE_INFORMATION, DACL_SECURITY_INFORMATION, INHERIT_ONLY_ACE,
    OWNER_SECURITY_INFORMATION, PSECURITY_DESCRIPTOR, PSID, TOKEN_OWNER, TOKEN_QUERY, TOKEN_USER,
};
use windows_sys::Win32::Storage::FileSystem::{
    FILE_ATTRIBUTE_REPARSE_POINT, FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT,
    READ_CONTROL,
};
use windows_sys::Win32::System::SystemServices::{ACCESS_ALLOWED_ACE_TYPE, ACCESS_DENIED_ACE_TYPE};
use windows_sys::Win32::System::Threading::{GetCurrentProcess, OpenProcessToken};

/// Who else the access list may let in.
#[derive(Clone, Copy, PartialEq, Eq)]
pub enum Others {
    /// No one: as a unix mode with no group or other bits (`& 0o077 == 0`).
    None,
    /// Readers only: as a unix mode no one else can write (`& 0o022 == 0`).
    Read,
}

/// Rights that change a file or folder, its contents, its attributes, its access list or its
/// owner, or remove it: the ones `Others::Read` keeps from everyone but this user.
const CHANGE: u32 = 0x0002 // FILE_WRITE_DATA, FILE_ADD_FILE
    | 0x0004 // FILE_APPEND_DATA, FILE_ADD_SUBDIRECTORY
    | 0x0010 // FILE_WRITE_EA
    | 0x0040 // FILE_DELETE_CHILD
    | 0x0100 // FILE_WRITE_ATTRIBUTES
    | 0x0001_0000 // DELETE
    | 0x0004_0000 // WRITE_DAC
    | 0x0008_0000 // WRITE_OWNER
    | 0x1000_0000 // GENERIC_ALL
    | 0x4000_0000; // GENERIC_WRITE

/// An open handle, closed when dropped.
struct Token(HANDLE);
impl Drop for Token {
    fn drop(&mut self) {
        // SAFETY: the handle is open and owned by this value.
        unsafe { CloseHandle(self.0) };
    }
}

/// A token information class's data, in memory aligned for it.
fn token_info(token: &Token, class: i32) -> Result<Vec<u64>, String> {
    let mut len = 0u32;
    // SAFETY: asks for the size only; the call fails with the size in len.
    unsafe { GetTokenInformation(token.0, class, null_mut(), 0, &mut len) };
    if len == 0 {
        return Err(format!("token: {}", std::io::Error::last_os_error()));
    }
    let mut buf = vec![0u64; (len as usize).div_ceil(8)];
    // SAFETY: buf holds at least len bytes.
    if unsafe { GetTokenInformation(token.0, class, buf.as_mut_ptr().cast(), len, &mut len) } == 0 {
        return Err(format!("token: {}", std::io::Error::last_os_error()));
    }
    Ok(buf)
}

/// Checks that path is a folder (folder set) or a plain file, not a link or another reparse
/// point; owned by this user, or by the token's default owner (the owner this user's new files
/// get: the Administrators group for an elevated administrator, as on a hosted CI runner); with
/// an access list that lets no one else in beyond others allows. Allow entries for SYSTEM and the
/// Administrators group are accepted: they are the Windows form of root, which a unix mode check
/// cannot exclude either, and folders such as a runner's temporary folder carry them by
/// inheritance. Entries present on the object are judged whether inherited or not; inherit-only
/// entries, which apply only to what is made inside, are not counted. As on unix, this is the
/// object's state now: its parents are not checked.
pub fn check(path: &Path, folder: bool, others: Others) -> Result<(), String> {
    let what = path.display();
    let file = OpenOptions::new()
        .access_mode(READ_CONTROL)
        .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS)
        .open(path)
        .map_err(|e| format!("{what}: {e}"))?;
    let meta = file.metadata().map_err(|e| format!("{what}: {e}"))?;
    if meta.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0
        || meta.is_dir() != folder
        || (!folder && !meta.is_file())
    {
        return Err(format!(
            "{what} is not a {}",
            if folder { "folder" } else { "plain file" }
        ));
    }
    let mut token: HANDLE = null_mut();
    // SAFETY: the pseudo handle of this process needs no closing; token is written on success.
    if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) } == 0 {
        return Err(format!("token: {}", std::io::Error::last_os_error()));
    }
    let token = Token(token);
    let user = token_info(&token, TokenUser)?;
    let default_owner = token_info(&token, TokenOwner)?;
    // SAFETY: filled in by GetTokenInformation for these classes, and aligned for them.
    let (me, my_owner) = unsafe {
        (
            (*user.as_ptr().cast::<TOKEN_USER>()).User.Sid,
            (*default_owner.as_ptr().cast::<TOKEN_OWNER>()).Owner,
        )
    };
    let mut owner: PSID = null_mut();
    let mut dacl: *mut ACL = null_mut();
    let mut sd: PSECURITY_DESCRIPTOR = null_mut();
    // SAFETY: the handle is open with READ_CONTROL; the outputs point into sd, freed below.
    let status = unsafe {
        GetSecurityInfo(
            file.as_raw_handle() as HANDLE,
            SE_FILE_OBJECT,
            OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION,
            &mut owner,
            null_mut(),
            &mut dacl,
            null_mut(),
            &mut sd,
        )
    };
    if status != 0 {
        return Err(format!(
            "{what}: {}",
            std::io::Error::from_raw_os_error(status as i32)
        ));
    }
    // SAFETY: owner and dacl point into sd, which lives until the LocalFree after this.
    let result = unsafe { judge(owner, dacl, me, my_owner, others) };
    // SAFETY: sd was allocated by GetSecurityInfo.
    unsafe { LocalFree(sd) };
    result.map_err(|why| format!("{what}: {why}"))
}

/// Judges an owner and access list by `check`'s rule: me is this user's SID, my_owner the token's
/// default owner; both count as this user, and SYSTEM and Administrators allow entries are
/// accepted (see `check`). SAFETY: every pointer must be valid (dacl may be null).
unsafe fn judge(
    owner: PSID,
    dacl: *mut ACL,
    me: PSID,
    my_owner: PSID,
    others: Others,
) -> Result<(), String> {
    let mine = |sid: PSID| EqualSid(sid, me) != 0 || EqualSid(sid, my_owner) != 0;
    if owner.is_null() || !mine(owner) {
        return Err("not owned by this user".into());
    }
    if dacl.is_null() {
        return Err("no access list, so everyone may change it".into());
    }
    let mut size: ACL_SIZE_INFORMATION = std::mem::zeroed();
    if GetAclInformation(
        dacl,
        (&mut size as *mut ACL_SIZE_INFORMATION).cast(),
        std::mem::size_of::<ACL_SIZE_INFORMATION>() as u32,
        AclSizeInformation,
    ) == 0
    {
        return Err(format!("access list: {}", std::io::Error::last_os_error()));
    }
    for i in 0..size.AceCount {
        let mut ace: *mut core::ffi::c_void = null_mut();
        if GetAce(dacl, i, &mut ace) == 0 {
            return Err(format!("access list: {}", std::io::Error::last_os_error()));
        }
        let header = &*ace.cast::<ACE_HEADER>();
        if u32::from(header.AceFlags) & INHERIT_ONLY_ACE != 0
            || u32::from(header.AceType) == ACCESS_DENIED_ACE_TYPE
        {
            continue;
        }
        if u32::from(header.AceType) != ACCESS_ALLOWED_ACE_TYPE {
            return Err(format!(
                "its access list has an entry of a kind not checked here ({})",
                header.AceType
            ));
        }
        let entry = &*ace.cast::<ACCESS_ALLOWED_ACE>();
        let sid: PSID = (&entry.SidStart as *const u32).cast_mut().cast();
        if mine(sid)
            || IsWellKnownSid(sid, WinLocalSystemSid) != 0
            || IsWellKnownSid(sid, WinBuiltinAdministratorsSid) != 0
        {
            continue;
        }
        if others == Others::None || entry.Mask & CHANGE != 0 {
            return Err("its access list lets another user in".into());
        }
    }
    Ok(())
}
