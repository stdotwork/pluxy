use super::*;

fn utf16(s: &str, bom: bool) -> Vec<u8> {
    let mut b = if bom { vec![0xFF, 0xFE] } else { Vec::new() };
    b.extend(s.encode_utf16().flat_map(u16::to_le_bytes));
    b
}

#[test]
fn utf16_output_with_or_without_a_byte_order_mark_is_read() {
    let text = "Ubuntu\r\nDebian\r\n";
    assert_eq!(names(&utf16(text, true)), ["Ubuntu", "Debian"]);
    assert_eq!(names(&utf16(text, false)), ["Ubuntu", "Debian"]);
    // Unicode beyond ASCII survives UTF-16.
    assert_eq!(names(&utf16("Übuntu-é\r\n", false)), ["Übuntu-é"]);
}

#[test]
fn utf8_output_with_or_without_a_byte_order_mark_is_read() {
    assert_eq!(names(b"Ubuntu\nOther\n"), ["Ubuntu", "Other"]);
    assert_eq!(
        names(b"\xEF\xBB\xBFUbuntu\r\n  Other  \r\n\r\n"),
        ["Ubuntu", "Other"]
    );
    assert_eq!(names("Übuntu\n".as_bytes()), ["Übuntu"]);
}

#[test]
fn dockers_own_distributions_are_hidden() {
    let out = utf16("docker-desktop\r\nUbuntu\r\ndocker-desktop-data\r\n", true);
    assert_eq!(names(&out), ["Ubuntu"]);
}

#[test]
fn names_that_are_not_safe_are_left_out_and_each_name_is_listed_once() {
    let long = "x".repeat(65);
    let ok64 = "y".repeat(64);
    let text =
        format!("a/b\n..\n.\nC:\\x\nend.\n-u\nwith\u{7}bell\n{long}\n{ok64}\nUbuntu\nUbuntu\nq?\n");
    assert_eq!(names(text.as_bytes()), [ok64.as_str(), "Ubuntu"]);
}

#[test]
fn nothing_installed_gives_no_names() {
    assert!(names(b"").is_empty());
    // What WSL prints when there is no distribution. The English sentence happens to fail the name
    // rules (a trailing period), a translated one may not: `listing` decides by WSL's error code
    // and never reads a failed listing's output as names (below).
    let msg = utf16(
        "Windows Subsystem for Linux has no installed distributions.\r\n",
        false,
    );
    assert!(names(&msg).is_empty());
}

#[test]
fn user_names_are_posix_portable_names_of_at_most_32_bytes() {
    for ok in ["bob", "_svc", "a-b_c9", "machine$", &"a".repeat(32)] {
        assert!(user_ok(ok), "{ok}");
    }
    for bad in [
        "",
        "Bob",
        "9bob",
        "-u",
        "bo b",
        "bob:",
        "b$ob",
        "$",
        "bób",
        "a/b",
        &"a".repeat(33),
    ] {
        assert!(!user_ok(bad), "{bad}");
    }
}

#[test]
fn distribution_names_never_start_like_an_option() {
    assert!(distro_ok("Ubuntu-24.04"));
    assert!(!distro_ok("-d"));
    assert!(!distro_ok("--exec"));
}

#[test]
fn no_distribution_is_known_by_wsls_error_code_in_any_language() {
    // What wsl.exe --list says with no distribution, in Japanese and in English: a sentence (one
    // that passes the name rules) and WSL's own error code, which is never translated.
    for msg in [
        "Linux 用 Windows サブシステムには、ディストリビューションがインストールされていません。\r\n\
         Error code: Wsl/WSL_E_DEFAULT_DISTRO_NOT_FOUND\r\n",
        "Windows Subsystem for Linux has no installed distributions\r\n\
         Error code: Wsl/WSL_E_DEFAULT_DISTRO_NOT_FOUND\r\n",
    ] {
        assert_eq!(listing(-1, &utf16(msg, false), b"").unwrap(), None, "{msg}");
    }
    // Any other failure is an error, whatever its text looks like; the output of a failed
    // listing is never read as names.
    let e = listing(
        -1,
        &utf16("Ubuntu\r\nError code: Wsl/Service/E_UNEXPECTED\r\n", false),
        b"",
    )
    .unwrap_err();
    assert_eq!(e.code, "unavailable");
    assert!(e.message.contains("E_UNEXPECTED"), "{e}");
    assert_eq!(listing(1, b"", b"").unwrap_err().code, "unavailable");
    // A listing that worked.
    assert_eq!(
        listing(0, &utf16("Ubuntu\r\n", true), b"").unwrap(),
        Some(vec!["Ubuntu".to_string()])
    );
    // No distribution: windows-latest's wsl.exe exits 0 with no name.
    assert_eq!(listing(0, b"", b"").unwrap(), None);
    // Only names pluxy hides or refuses: none it can use either.
    assert_eq!(
        listing(0, &utf16("docker-desktop\r\n", true), b"").unwrap(),
        None
    );
}

#[test]
fn nuls_around_a_name_are_trimmed() {
    assert_eq!(
        names(&utf16("Ubuntu\0\r\n\0Other\0\0\r\n", true)),
        ["Ubuntu", "Other"]
    );
    // A NUL inside a name is still refused.
    assert!(names(&utf16("Ub\0untu\r\n", true)).is_empty());
}

#[test]
fn wsl_not_enabled_is_not_installed() {
    // System32's wsl.exe on a Windows where the WSL feature is off: exit -1 and this code.
    let msg = "The Windows Subsystem for Linux is not installed. You can install by running \
               'wsl.exe --install'.\r\nError code: Wsl/WSL_E_WSL_OPTIONAL_COMPONENT_REQUIRED\r\n";
    assert_eq!(listing(-1, &utf16(msg, false), b"").unwrap(), None);
    assert_eq!(listing(-1, b"", msg.as_bytes()).unwrap(), None);
    // The same code with another exit is not WSL's own answer.
    assert_eq!(
        listing(1, &utf16(msg, false), b"").unwrap_err().code,
        "unavailable"
    );
}

/// The values WSL keeps for a distribution under `HKCU\...\Lxss\{guid}`, as WSL itself writes
/// them: `Version` is the registration's format (2 for every new one, whatever its WSL version),
/// `Flags` holds WSL 2 as 0x8 (interop 0x1, Windows PATH 0x2 and drive mounting 0x4 by default).
fn lxss(version: u32, flags: Option<u32>) -> impl Fn(&str) -> Option<u32> {
    move |name| match name {
        "Version" => Some(version),
        "Flags" => flags,
        "DefaultUid" => Some(1000),
        _ => None,
    }
}

#[test]
fn the_wsl_version_is_the_vm_mode_flag_not_the_registrations_format() {
    // Seen on windows-latest: both distributions WSL 1 (wsl.exe --list --verbose), format 2.
    assert_eq!(wsl_version(lxss(2, Some(0x7))), Some(1));
    assert_eq!(wsl_version(lxss(2, Some(0xF))), Some(2));
    // An old registration's format does not change the answer either.
    assert_eq!(wsl_version(lxss(1, Some(0xF))), Some(2));
    assert_eq!(wsl_version(lxss(0, Some(0x7))), Some(1));
    // No flags: not known.
    assert_eq!(wsl_version(lxss(2, None)), None);
}
