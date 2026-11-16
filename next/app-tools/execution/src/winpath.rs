//! Windows paths as a person reads them: the plain form `C:\…` (or `\\server\share\…`) of a
//! resolved path, which Windows reports as `\\?\C:\…`. The plain form is used only when it names
//! the same file: Windows rewrites a plain path whose names end in a dot or a blank, or are device
//! names (`NUL`, `COM1`, …), and the `\\?\` form is the only one for those. Such a path is refused,
//! so the confirmation never shows one path while another is used.

/// The plain form of a resolved path p (as `std::fs::canonicalize` gives it), or why it has none.
pub fn plain(p: &str) -> Result<String, String> {
    let s = if let Some(rest) = p.strip_prefix(r"\\?\UNC\") {
        format!(r"\\{rest}")
    } else if let Some(rest) = p.strip_prefix(r"\\?\") {
        rest.to_string()
    } else {
        p.to_string()
    };
    let names = if let Some(unc) = s.strip_prefix(r"\\") {
        let mut parts = unc.splitn(3, '\\');
        let (server, share) = (parts.next().unwrap_or(""), parts.next().unwrap_or(""));
        if server.is_empty() || share.is_empty() || server == "?" || server == "." {
            return Err(format!("{p} is not a path pluxy can show plainly"));
        }
        parts.next().unwrap_or("")
    } else {
        let b = s.as_bytes();
        if b.len() < 3 || !b[0].is_ascii_alphabetic() || b[1] != b':' || b[2] != b'\\' {
            return Err(format!("{p} is not a full path"));
        }
        &s[3..]
    };
    for name in names.split('\\').filter(|n| !n.is_empty()) {
        if name.ends_with(['.', ' ']) || name.contains(['/', ':', '*', '?', '"', '<', '>', '|']) {
            return Err(format!(
                "{p} has a name Windows would change in its plain form ({name})"
            ));
        }
        let stem = name.split('.').next().unwrap_or("").trim_end();
        let device = matches!(
            stem.to_ascii_uppercase().as_str(),
            "CON" | "PRN" | "AUX" | "NUL" | "CONIN$" | "CONOUT$"
        ) || (stem.len() == 4
            && ["COM", "LPT"].contains(&stem[..3].to_ascii_uppercase().as_str())
            && stem.as_bytes()[3].is_ascii_digit()
            && stem.as_bytes()[3] != b'0');
        if device {
            return Err(format!("{p} names a device name ({name})"));
        }
    }
    Ok(s)
}

#[cfg(test)]
mod tests {
    use super::plain;

    #[test]
    fn a_resolved_path_shows_in_its_plain_form() {
        for (resolved, shown) in [
            (
                r"\\?\C:\Users\me\.codex\sessions",
                r"C:\Users\me\.codex\sessions",
            ),
            (r"C:\Users\me\proj", r"C:\Users\me\proj"),
            (r"\\?\UNC\server\share\dir", r"\\server\share\dir"),
            (r"\\?\D:\", r"D:\"),
            (
                r"\\?\C:\a b\c.d\com10\nulx.txt",
                r"C:\a b\c.d\com10\nulx.txt",
            ),
        ] {
            assert_eq!(plain(resolved).as_deref(), Ok(shown));
        }
    }

    #[test]
    fn a_path_whose_plain_form_names_another_file_is_refused() {
        for p in [
            r"\\?\C:\dir.\x",
            r"\\?\C:\dir \x",
            r"\\?\C:\x\NUL",
            r"\\?\C:\x\nul.txt",
            r"\\?\C:\x\COM1.log",
            r"\\?\C:\x\lpt9",
            r"\\?\C:\x\a:stream",
            r"\\?\GLOBALROOT\Device\x",
            r"\\?\UNC\server",
            r"\\.\pipe\x",
            r"relative\x",
            "/unix/path",
        ] {
            assert!(plain(p).is_err(), "{p}");
        }
    }
}
