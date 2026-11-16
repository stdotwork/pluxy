//! Reading `wsl.exe` output, and the names this program accepts: distributions from
//! `wsl.exe --list --quiet` (decoded, checked, Docker's own hidden) and user names.

/// Docker Desktop's own distributions: no user homes, nothing to set up in them.
pub const HIDDEN: [&str; 2] = ["docker-desktop", "docker-desktop-data"];

/// The longest distribution name accepted, in characters.
pub const MAX_DISTRO: usize = 64;

/// The longest user name accepted, in bytes.
pub const MAX_USER: usize = 32;

/// `wsl.exe`'s own text: UTF-16LE when it starts with `FF FE` or holds a NUL (what `wsl.exe` writes
/// to a pipe unless `WSL_UTF8` is honoured), else UTF-8; byte-order marks dropped.
pub fn decode(bytes: &[u8]) -> String {
    if bytes.starts_with(&[0xFF, 0xFE]) || bytes.contains(&0) {
        let body = bytes.strip_prefix(&[0xFF, 0xFE]).unwrap_or(bytes);
        let words: Vec<u16> = body
            .as_chunks::<2>()
            .0
            .iter()
            .map(|p| u16::from_le_bytes(*p))
            .collect();
        String::from_utf16_lossy(&words)
            .trim_start_matches('\u{feff}')
            .to_string()
    } else {
        let body = bytes.strip_prefix(&[0xEF, 0xBB, 0xBF]).unwrap_or(bytes);
        String::from_utf8_lossy(body)
            .trim_start_matches('\u{feff}')
            .to_string()
    }
}

/// A distribution name this program passes to `wsl.exe -d`: what the old app accepted as a path
/// part (not empty, `.` or `..`; no `\ / : * ? " < > |`, control character or trailing dot or
/// space), at most 64 characters, and not starting with `-`, so it is never read as an option.
pub fn distro_ok(name: &str) -> bool {
    !name.is_empty()
        && name != "."
        && name != ".."
        && !name.ends_with(['.', ' '])
        && !name.starts_with('-')
        && !name.contains(['\\', '/', ':', '*', '?', '"', '<', '>', '|'])
        && !name.chars().any(char::is_control)
        && name.chars().count() <= MAX_DISTRO
}

/// A POSIX portable user name: `[a-z_][a-z0-9_-]*[$]?`, at most 32 bytes.
pub fn user_ok(name: &str) -> bool {
    let b = name.as_bytes();
    let body = b.strip_suffix(b"$").unwrap_or(b);
    !body.is_empty()
        && b.len() <= MAX_USER
        && (body[0].is_ascii_lowercase() || body[0] == b'_')
        && body
            .iter()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || *c == b'_' || *c == b'-')
}

/// The distribution names in `wsl.exe --list --quiet` output, in its order: each line trimmed,
/// names this program refuses and Docker's own left out, each once.
pub fn names(output: &[u8]) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for line in decode(output).lines() {
        let name = line.trim_matches(|c: char| c.is_whitespace() || c == '\u{feff}');
        if distro_ok(name) && !HIDDEN.contains(&name) && !out.iter().any(|n| n == name) {
            out.push(name.to_string());
        }
    }
    out
}

#[cfg(test)]
#[path = "list_tests.rs"]
mod tests;
