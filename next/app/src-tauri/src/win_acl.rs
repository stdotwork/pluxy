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
    let same = |a: PSID, b: PSID| !a.is_null() && !b.is_null() && EqualSid(a, b) != 0;
    let mine = |sid: PSID| same(sid, me) || same(sid, my_owner);
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

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::windows::ffi::OsStrExt;
    use windows_sys::Win32::Security::Authorization::{
        ConvertStringSecurityDescriptorToSecurityDescriptorW, ConvertStringSidToSidW,
        SDDL_REVISION_1,
    };
    use windows_sys::Win32::Security::{GetSecurityDescriptorDacl, GetSecurityDescriptorOwner};

    /// This user, in the descriptors below; another user; both made up.
    const ME: &str = "S-1-5-21-1000-2000-3000-1001";
    const OTHER: &str = "S-1-5-21-1000-2000-3000-1002";

    fn wide(s: &str) -> Vec<u16> {
        std::ffi::OsStr::new(s)
            .encode_wide()
            .chain(std::iter::once(0))
            .collect()
    }

    /// Memory LocalAlloc'd by the conversions, freed when dropped.
    struct Local(*mut core::ffi::c_void);
    impl Drop for Local {
        fn drop(&mut self) {
            // SAFETY: allocated by the conversion that made it.
            unsafe { LocalFree(self.0) };
        }
    }

    fn sid(text: &str) -> Local {
        let mut sid: PSID = null_mut();
        // SAFETY: text is NUL-terminated; sid is written on success.
        assert_ne!(
            unsafe { ConvertStringSidToSidW(wide(text).as_ptr(), &mut sid) },
            0
        );
        Local(sid)
    }

    /// judge on the owner and access list of the descriptor sddl (`ME` and `OTHER` in it are
    /// replaced), for this user ME and the default owner my_owner.
    fn judged(sddl: &str, my_owner: Option<&str>, others: Others) -> Result<(), String> {
        let sddl = sddl.replace("ME", ME).replace("OTHER", OTHER);
        let mut sd: PSECURITY_DESCRIPTOR = null_mut();
        // SAFETY: the string is NUL-terminated; sd is written on success.
        let made = unsafe {
            ConvertStringSecurityDescriptorToSecurityDescriptorW(
                wide(&sddl).as_ptr(),
                SDDL_REVISION_1,
                &mut sd,
                null_mut(),
            )
        };
        assert_ne!(made, 0, "{sddl}: {}", std::io::Error::last_os_error());
        let sd = Local(sd);
        let (mut owner, mut dacl): (PSID, *mut ACL) = (null_mut(), null_mut());
        let (mut defaulted, mut present) = (0, 0);
        // SAFETY: sd is a valid descriptor; the outputs point into it.
        unsafe {
            assert_ne!(
                GetSecurityDescriptorOwner(sd.0, &mut owner, &mut defaulted),
                0
            );
            assert_ne!(
                GetSecurityDescriptorDacl(sd.0, &mut present, &mut dacl, &mut defaulted),
                0
            );
        }
        let me = sid(ME);
        let default_owner = my_owner.map(sid);
        // SAFETY: every pointer is valid while sd, me and default_owner live.
        unsafe {
            judge(
                owner,
                dacl,
                me.0,
                default_owner.as_ref().map_or(null_mut(), |s| s.0),
                others,
            )
        }
    }

    // A token whose default owner cannot be read as a SID is judged on its user alone.
    #[test]
    fn a_missing_default_owner_counts_as_no_one() {
        assert!(judged("O:MED:P(A;;FA;;;ME)", None, Others::None).is_ok());
        let e = judged("O:BAD:P(A;;FA;;;ME)", None, Others::None).unwrap_err();
        assert!(e.contains("not owned"), "{e}");
    }

    // Each of judge's branches, with a passing descriptor beside each refusal.
    #[test]
    fn judge_takes_only_this_users_own() {
        let mut wrong = Vec::new();
        let mut expect =
            |case: &str, got: Result<(), String>, want: Option<&str>| match (&got, want) {
                (Ok(()), None) => {}
                (Err(e), Some(w)) if e.contains(w) => {}
                _ => wrong.push(format!("{case}: expected {want:?}, got {got:?}")),
            };
        let private = "O:MED:P(A;;FA;;;ME)(A;;FA;;;SY)(A;;FA;;;BA)";
        expect("private", judged(private, Some(ME), Others::None), None);
        expect(
            "owned by another user",
            judged("O:OTHERD:P(A;;FA;;;ME)", Some(ME), Others::None),
            Some("not owned"),
        );
        expect(
            "owned by the default owner",
            judged("O:BAD:P(A;;FA;;;ME)", Some("S-1-5-32-544"), Others::None),
            None,
        );
        expect(
            "owned by Administrators, not the default owner",
            judged("O:BAD:P(A;;FA;;;ME)", Some(ME), Others::None),
            Some("not owned"),
        );
        expect(
            "no access list",
            judged("O:MED:NO_ACCESS_CONTROL", Some(ME), Others::None),
            Some("no access list"),
        );
        expect(
            "a deny entry for another user",
            judged(&format!("{private}(D;;FA;;;OTHER)"), Some(ME), Others::None),
            None,
        );
        expect(
            "an inherit-only entry for another user",
            judged(
                &format!("{private}(A;OICIIO;FA;;;OTHER)"),
                Some(ME),
                Others::None,
            ),
            None,
        );
        expect(
            "an object entry",
            judged(
                &format!("{private}(OA;;CC;bf967aba-0de6-11d0-a285-00aa003049e2;;ME)"),
                Some(ME),
                Others::None,
            ),
            Some("not checked here"),
        );
        let read = format!("{private}(A;;FR;;;OTHER)");
        expect(
            "another user reads, readers allowed",
            judged(&read, Some(ME), Others::Read),
            None,
        );
        expect(
            "another user reads, no one allowed",
            judged(&read, Some(ME), Others::None),
            Some("lets another user in"),
        );
        for (case, entry) in [
            ("another user writes", "(A;;FW;;;OTHER)"),
            ("Everyone may change it", "(A;;0x10000;;;WD)"),
            ("another user may change its access list", "(A;;WD;;;OTHER)"),
            ("another user has generic all", "(A;;GA;;;OTHER)"),
        ] {
            expect(
                case,
                judged(&format!("{private}{entry}"), Some(ME), Others::Read),
                Some("lets another user in"),
            );
        }
        assert!(wrong.is_empty(), "{wrong:#?}");
    }

    /// A new folder under the temporary folder whose access list is only this user's, SYSTEM's
    /// and Administrators', inherited by what is made in it.
    fn private_folder(name: &str) -> std::path::PathBuf {
        let mut random = [0; 8];
        getrandom::fill(&mut random).unwrap();
        let dir = std::env::temp_dir().join(format!(
            "pluxy-acl-{name}-{}",
            crate::artifact::hex(&random)
        ));
        std::fs::create_dir(&dir).unwrap();
        let user = std::process::Command::new("whoami").output().unwrap();
        let user = String::from_utf8_lossy(&user.stdout).trim().to_string();
        let set = std::process::Command::new("icacls")
            .arg(&dir)
            .args(["/inheritance:r", "/grant:r"])
            .arg(format!("{user}:(OI)(CI)F"))
            .args(["*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F"])
            .output()
            .unwrap();
        assert!(
            set.status.success(),
            "icacls: {}",
            String::from_utf8_lossy(&set.stdout)
        );
        dir
    }

    // check on real files: the folder and a file in it pass; the wrong kind, a junction and an
    // entry letting Everyone change them are refused.
    #[test]
    fn check_reads_real_files() {
        let dir = private_folder("check");
        let file = dir.join("file");
        std::fs::write(&file, b"x").unwrap();
        check(&dir, true, Others::None).unwrap();
        check(&file, false, Others::Read).unwrap();
        let e = check(&dir, false, Others::None).unwrap_err();
        assert!(e.contains("not a plain file"), "{e}");
        let e = check(&file, true, Others::None).unwrap_err();
        assert!(e.contains("not a folder"), "{e}");
        let link = dir.join("link");
        let made = std::process::Command::new("cmd")
            .arg("/c")
            .arg("mklink")
            .arg("/J")
            .arg(&link)
            .arg(&dir)
            .output()
            .unwrap();
        assert!(
            made.status.success(),
            "mklink: {}",
            String::from_utf8_lossy(&made.stdout)
        );
        let e = check(&link, true, Others::None).unwrap_err();
        assert!(e.contains("not a folder"), "{e}");
        std::fs::remove_dir(&link).unwrap();
        let opened = std::process::Command::new("icacls")
            .arg(&file)
            .arg("/grant")
            .arg("*S-1-1-0:M")
            .output()
            .unwrap();
        assert!(opened.status.success());
        let e = check(&file, false, Others::Read).unwrap_err();
        assert!(e.contains("lets another user in"), "{e}");
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
