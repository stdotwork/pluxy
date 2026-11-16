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
    // What WSL prints when there is no distribution: a sentence, which is not a name (it has a
    // trailing period).
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
