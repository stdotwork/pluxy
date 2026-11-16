//! The Unicode format characters (general category Cf), which native reviews show escaped: on
//! Windows and macOS, where GLib's character data is not there. A Linux test checks every code
//! point against GLib's, so the three platforms escape the same characters.

/// Each range of Cf code points, first and last, in order (Unicode 16).
const CF: &[(u32, u32)] = &[
    (0x00AD, 0x00AD),
    (0x0600, 0x0605),
    (0x061C, 0x061C),
    (0x06DD, 0x06DD),
    (0x070F, 0x070F),
    (0x0890, 0x0891),
    (0x08E2, 0x08E2),
    (0x180E, 0x180E),
    (0x200B, 0x200F),
    (0x202A, 0x202E),
    (0x2060, 0x2064),
    (0x2066, 0x206F),
    (0xFEFF, 0xFEFF),
    (0xFFF9, 0xFFFB),
    (0x110BD, 0x110BD),
    (0x110CD, 0x110CD),
    (0x13430, 0x1343F),
    (0x1BCA0, 0x1BCA3),
    (0x1D173, 0x1D17A),
    (0xE0001, 0xE0001),
    (0xE0020, 0xE007F),
];

/// Whether ch is a format character.
#[cfg_attr(target_os = "linux", allow(dead_code))]
pub(crate) fn is_format(ch: char) -> bool {
    let c = ch as u32;
    CF.binary_search_by(|&(first, last)| {
        if last < c {
            std::cmp::Ordering::Less
        } else if first > c {
            std::cmp::Ordering::Greater
        } else {
            std::cmp::Ordering::Equal
        }
    })
    .is_ok()
}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    use gtk::glib::Unichar;

    #[test]
    fn the_table_is_glibs_format_category() {
        for c in (0..=0x10FFFFu32).filter_map(char::from_u32) {
            assert_eq!(
                super::is_format(c),
                c.unicode_type() == gtk::glib::UnicodeType::Format,
                "U+{:04X}",
                c as u32
            );
        }
    }
}
