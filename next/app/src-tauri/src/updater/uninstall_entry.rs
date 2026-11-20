//! Windows: the version Settings > Apps shows for a copy the setup program installed. The setup
//! writes an uninstall entry for the user (`HKEY_CURRENT_USER\Software\Microsoft\Windows\
//! CurrentVersion\Uninstall\<product name>`) with the version it installed (`DisplayVersion`) and
//! the folder it installed into (`InstallLocation`, in quotes). The app updates and rolls back its
//! program set itself, so once a version accepts itself after an update or a rollback it writes
//! its own version there: only into an entry that already exists and names the program's own
//! folder, never making one, so a folder bundle or another install of the same product is left
//! alone.
#![cfg_attr(not(windows), allow(dead_code))]

/// The uninstall entry's key for the product, below `HKEY_CURRENT_USER`.
pub(super) fn key(product: &str) -> String {
    format!(r"Software\Microsoft\Windows\CurrentVersion\Uninstall\{product}")
}

/// Whether location, an uninstall entry's `InstallLocation`, names the folder at folder: the same
/// path once the quotes around it and a `\\?\` prefix (on either) are removed, compared without
/// case, as Windows compares names.
pub(super) fn names_folder(location: &str, folder: &str) -> bool {
    let unquoted = match location.strip_prefix('"') {
        Some(rest) => match rest.strip_suffix('"') {
            Some(inner) => inner,
            None => return false,
        },
        None => location,
    };
    let plain = |p: &str| p.strip_prefix(r"\\?\").unwrap_or(p).to_lowercase();
    let (location, folder) = (plain(unquoted), plain(folder));
    !folder.is_empty() && !location.contains('"') && location == folder
}

/// Writes version as the entry's `DisplayVersion` when the product's uninstall entry exists and
/// its `InstallLocation` names folder. Whether it wrote.
#[cfg(windows)]
pub(super) fn record(
    product: &str,
    folder: &std::path::Path,
    version: &str,
) -> Result<bool, String> {
    let key = key(product);
    let Some(folder) = folder.to_str() else {
        return Ok(false);
    };
    let Some(location) = crate::startup::text_value(&key, "InstallLocation")? else {
        return Ok(false);
    };
    if !names_folder(&location, folder) {
        return Ok(false);
    }
    crate::startup::set_text(&key, "DisplayVersion", version)?;
    Ok(true)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_entry_of_the_programs_own_folder_is_recognised() {
        let folder = r"C:\Users\someone\AppData\Local\pluxy Test";
        // As the setup writes it: in quotes.
        assert!(names_folder(
            r#""C:\Users\someone\AppData\Local\pluxy Test""#,
            folder
        ));
        // Without quotes, in another case, with a \\?\ prefix on either side.
        assert!(names_folder(
            r"c:\users\SOMEONE\appdata\local\PLUXY TEST",
            folder
        ));
        assert!(names_folder(
            r#""\\?\C:\Users\someone\AppData\Local\pluxy Test""#,
            folder
        ));
        assert!(names_folder(
            r#""C:\Users\someone\AppData\Local\pluxy Test""#,
            r"\\?\C:\Users\someone\AppData\Local\pluxy Test"
        ));
    }

    #[test]
    fn another_folders_entry_is_left_alone() {
        let folder = r"C:\Users\someone\AppData\Local\pluxy Test";
        for other in [
            r#""C:\Users\someone\AppData\Local\pluxy""#,
            r#""C:\Users\someone\AppData\Local\pluxy Test\sub""#,
            r#""C:\Users\other\AppData\Local\pluxy Test""#,
            r#""D:\pluxy Test""#,
            // One quote only, or quotes inside: not the setup's shape.
            r#""C:\Users\someone\AppData\Local\pluxy Test"#,
            r#"C:\Users\someone\AppData\Local\pluxy Test""#,
            r#""C:\Users\someone\AppData\Local\"pluxy Test""#,
            "",
            r#""""#,
        ] {
            assert!(!names_folder(other, folder), "{other} named {folder}");
        }
        // An empty folder names nothing, even an empty entry.
        assert!(!names_folder("", ""));
        assert!(!names_folder(r#""""#, ""));
    }

    #[test]
    fn the_key_is_the_products_own() {
        assert_eq!(
            key("pluxy Test"),
            r"Software\Microsoft\Windows\CurrentVersion\Uninstall\pluxy Test"
        );
    }
}
