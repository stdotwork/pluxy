use crate::{
    default_selections, make_library, pinned_read, recipe, settings_of, threep_in, MISSING_LIBRARY,
};
use pluxy_program::Error;
use pluxy_transactions::{sha256_of, Selection};
use serde_json::{json, Value};
use std::cell::Cell;
use std::ffi::OsString;
use std::path::{Path, PathBuf};

fn tmp(name: &str) -> std::path::PathBuf {
    let d = std::env::temp_dir().join(format!("claude-desktop-test-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    d
}

/// An environment holding only vars.
fn env(vars: &[(&str, &Path)]) -> impl Fn(&str) -> Option<OsString> {
    let vars: Vec<(String, OsString)> = vars
        .iter()
        .map(|(k, v)| (k.to_string(), v.as_os_str().to_owned()))
        .collect();
    move |k| vars.iter().find(|(n, _)| n == k).map(|(_, v)| v.clone())
}

#[test]
fn a_missing_library_is_named_for_the_folders_operation_whatever_is_missing_above_it() {
    let home = tmp("missing");
    let threep = home.join(".config/Claude-3p");
    // No ~/.config/Claude-3p at all, then the folder without its library: both name the library.
    for _ in 0..2 {
        let e = default_selections("linux", &env(&[("HOME", &home)])).unwrap_err();
        assert!(e.message.contains(MISSING_LIBRARY), "{e}");
        assert!(e.message.contains("configLibrary"), "{e}");
        std::fs::create_dir_all(&threep).unwrap();
    }
    assert!(!threep.join("configLibrary").exists(), "a folder was made");
    // With the library: the three files.
    std::fs::create_dir(threep.join("configLibrary")).unwrap();
    let s = default_selections("linux", &env(&[("HOME", &home)])).unwrap();
    assert_eq!(
        s[2],
        Selection::File(threep.join("configLibrary/_meta.json"))
    );
    assert!(default_selections("linux", &env(&[])).is_err());
    std::fs::remove_dir_all(home).unwrap();
}

#[cfg(unix)]
#[test]
fn something_else_where_the_library_or_its_folder_should_be_is_not_called_missing() {
    // A link to nowhere as the library, or as the third-party folder: the preview names what it
    // finds instead of offering to create a folder over it.
    let home = tmp("dangling");
    let threep = home.join(".config/Claude-3p");
    std::fs::create_dir_all(&threep).unwrap();
    std::os::unix::fs::symlink(home.join("nowhere"), threep.join("configLibrary")).unwrap();
    assert!(default_selections("linux", &env(&[("HOME", &home)])).is_ok());
    std::fs::remove_dir_all(&threep).unwrap();
    std::os::unix::fs::symlink(home.join("nowhere"), &threep).unwrap();
    assert!(default_selections("linux", &env(&[("HOME", &home)])).is_ok());
    std::fs::remove_dir_all(home).unwrap();
}

#[test]
fn files_claude_desktop_would_not_take_are_reported_not_refused() {
    let broken = Some(b"{ // no\n}".to_vec());
    let v = settings_of(&[None, None, broken], None);
    assert_eq!(v["state"], "refused");
    assert!(v["reason"].as_str().unwrap().contains("_meta.json"), "{v}");
    let v = settings_of(&[None, None, None], None);
    assert_eq!(v["state"], "not-configured");
}

/// A preview whose pins say sha256 of pinned, and then the bytes read.
fn read_of(pinned: &[&[u8]; 3], read: &[&[u8]; 3]) -> (Value, [Option<Vec<u8>>; 3]) {
    (
        json!({"targets": pinned.iter().map(|p| json!({"sha256": sha256_of(Some(p))})).collect::<Vec<_>>()}),
        read.map(|r| Some(r.to_vec())),
    )
}

#[test]
fn settings_read_back_are_the_pinned_files_or_the_read_is_refused() {
    let n = Cell::new(0);
    let (_, bytes) = pinned_read(|| {
        n.set(n.get() + 1);
        Ok::<_, Error>(if n.get() == 1 {
            read_of(&[b"{}", b"{}", b"{}"], &[b"{}", b"{}", b"{\"x\": 1}"])
        } else {
            read_of(
                &[b"{}", b"{}", b"{\"x\": 1}"],
                &[b"{}", b"{}", b"{\"x\": 1}"],
            )
        })
    })
    .unwrap();
    assert_eq!(n.get(), 2);
    assert_eq!(bytes[2].as_deref(), Some(&b"{\"x\": 1}"[..]));
    let e =
        pinned_read(|| Ok(read_of(&[b"{}", b"{}", b"{}"], &[b"[]", b"{}", b"{}"]))).unwrap_err();
    assert!(e.message.contains("changed while it was read"), "{e}");
}

#[test]
fn the_third_party_folder_is_where_claude_desktop_keeps_it_on_each_system() {
    // Anthropic's data-storage page: ~/.config/Claude-3p, ~/Library/Application Support/Claude-3p
    // and %LOCALAPPDATA%\Claude-3p.
    let home = tmp("threep-home");
    let local = tmp("threep-local");
    let both = env(&[("HOME", &home), ("LOCALAPPDATA", &local)]);
    assert_eq!(
        threep_in("linux", &both).unwrap(),
        home.join(".config").join("Claude-3p")
    );
    assert_eq!(
        threep_in("macos", &both).unwrap(),
        home.join("Library")
            .join("Application Support")
            .join("Claude-3p")
    );
    assert_eq!(
        threep_in("windows", &both).unwrap(),
        local.join("Claude-3p")
    );
    // Windows reads LOCALAPPDATA, never HOME: none, or one that is not a full path, is refused.
    for e in [
        threep_in("windows", &env(&[("HOME", &home)])).unwrap_err(),
        threep_in("windows", &env(&[("LOCALAPPDATA", Path::new("AppData"))])).unwrap_err(),
    ] {
        assert!(e.message.contains("LOCALAPPDATA"), "{e}");
    }
    assert!(threep_in("macos", &env(&[("LOCALAPPDATA", &local)])).is_err());
    std::fs::remove_dir_all(home).unwrap();
    std::fs::remove_dir_all(local).unwrap();
}

#[test]
fn on_windows_10_the_msix_packages_folder_is_taken_only_when_it_alone_exists() {
    let local = tmp("threep-msix");
    let vars = env(&[("LOCALAPPDATA", &local)]);
    let packaged: PathBuf = local
        .join("Packages")
        .join("Claude_pzs8sxrjxfjjc")
        .join("LocalCache")
        .join("Local")
        .join("Claude-3p");
    // Neither yet: the plain folder, which `folders` creates.
    assert_eq!(
        threep_in("windows", &vars).unwrap(),
        local.join("Claude-3p")
    );
    std::fs::create_dir_all(&packaged).unwrap();
    assert_eq!(threep_in("windows", &vars).unwrap(), packaged);
    // The selections name the package's library, and say it is missing there.
    let e = default_selections("windows", &vars).unwrap_err();
    assert!(
        e.message
            .contains(&packaged.join("configLibrary").display().to_string()),
        "{e}"
    );
    // After an upgrade to Windows 11 the plain folder is Claude Desktop's; the old one stays.
    std::fs::create_dir(local.join("Claude-3p")).unwrap();
    assert_eq!(
        threep_in("windows", &vars).unwrap(),
        local.join("Claude-3p")
    );
    std::fs::remove_dir_all(local).unwrap();
}

#[test]
fn the_recipe_starts_claude_desktop_as_each_system_installs_it() {
    let home = tmp("recipe-home");
    let apps = tmp("recipe-apps");
    let program = |os: &str| recipe(os, &home, &apps).map(|r| r["program"].clone());
    assert_eq!(program("linux").unwrap(), "claude-desktop");
    // macOS: the bundle in the system's applications folder, else the home folder's.
    let system = apps.join("Claude.app");
    let own = home.join("Applications").join("Claude.app");
    assert_eq!(program("macos").unwrap(), json!(system));
    std::fs::create_dir_all(&own).unwrap();
    assert_eq!(program("macos").unwrap(), json!(own));
    std::fs::create_dir_all(&system).unwrap();
    assert_eq!(program("macos").unwrap(), json!(system));
    let r = recipe("macos", &home, &apps).unwrap();
    assert_eq!(
        (r["kind"].clone(), r["args"].clone(), r["cwd"].clone()),
        (json!("direct"), json!([]), json!(home))
    );
    // Windows: nothing documented to start, said so.
    let e = program("windows").unwrap_err();
    assert_eq!(e.code, "unsupported");
    assert!(e.message.contains("Start menu"), "{e}");
    std::fs::remove_dir_all(home).unwrap();
    std::fs::remove_dir_all(apps).unwrap();
}

#[test]
fn the_library_folder_is_made_private_with_the_folders_above_it() {
    let base = tmp("library");
    let library = base.join("Claude-3p").join("configLibrary");
    make_library(&library).unwrap();
    assert!(pluxy_files::is_private(&library).unwrap());
    #[cfg(unix)]
    assert!(pluxy_files::is_private(library.parent().unwrap()).unwrap());
    // Made again meanwhile (by Claude Desktop): taken as it is.
    make_library(&library).unwrap();
    std::fs::remove_dir(&library).unwrap();
    std::fs::write(&library, "").unwrap();
    assert!(make_library(&library).is_err());
    std::fs::remove_dir_all(base).unwrap();
}
