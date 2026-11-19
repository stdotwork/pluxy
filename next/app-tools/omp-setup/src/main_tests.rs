use crate::{config_of, pinned_read, Selection};
use pluxy_program::Error;
use pluxy_transactions::sha256_of;
use serde_json::{json, Value};
use std::cell::Cell;

fn tmp() -> std::path::PathBuf {
    let d = std::env::temp_dir().join(format!(
        "omp-setup-main-test-{}-{:?}",
        std::process::id(),
        std::thread::current().id()
    ));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    d
}

/// A preview whose pins say sha256 of pinned, and then the bytes read.
fn read_of(pinned: [&[u8]; 2], read: [&[u8]; 2]) -> (Value, [Option<Vec<u8>>; 2]) {
    (
        json!({"targets": [{"sha256": sha256_of(Some(pinned[0]))}, {"sha256": sha256_of(Some(pinned[1]))}]}),
        [Some(read[0].to_vec()), Some(read[1].to_vec())],
    )
}

#[test]
fn the_values_read_back_are_the_pinned_files_or_the_read_is_refused() {
    // The settings file changed between the pin and the read once: read again, and it holds.
    let n = Cell::new(0);
    let (_, bytes) = pinned_read(|| {
        n.set(n.get() + 1);
        Ok::<_, Error>(if n.get() == 1 {
            read_of([b"a: 1\n", b"b: 1\n"], [b"a: 1\n", b"b: 2\n"])
        } else {
            read_of([b"a: 1\n", b"b: 2\n"], [b"a: 1\n", b"b: 2\n"])
        })
    })
    .unwrap();
    assert_eq!(
        (n.get(), bytes[1].clone().unwrap()),
        (2, b"b: 2\n".to_vec())
    );
    // Changed again: refused.
    let e =
        pinned_read(|| Ok(read_of([b"a: 1\n", b"b: 1\n"], [b"a: 2\n", b"b: 1\n"]))).unwrap_err();
    assert!(e.message.contains("changed while it was read"), "{e}");
    // No files on either side are the same files.
    let none = json!({"targets": [{"sha256": null}, {"sha256": null}]});
    let (_, b) = pinned_read(|| Ok((none.clone(), [None, None]))).unwrap();
    assert_eq!(b, [None, None]);
}

#[test]
fn files_oh_my_pi_would_not_take_are_reported_refused_not_failed() {
    let dir = tmp();
    let sels = [
        Selection::File(dir.join("models.yml")),
        Selection::File(dir.join("config.yml")),
    ];
    let v = config_of(&sels, Some(b"providers: [a]\n"), None, None);
    assert_eq!(v["state"], "refused");
    assert!(v["reason"].as_str().unwrap().contains("providers"), "{v}");
    let v = config_of(&sels, None, Some(b"a:\n\tb: 1\n"), None);
    assert_eq!(v["state"], "refused");
    assert!(v["reason"].as_str().unwrap().contains("YAML"), "{v}");
    // A legacy models.json Oh My Pi has not moved yet: refused, with the reason.
    std::fs::write(dir.join("models.json"), "{}").unwrap();
    let v = config_of(&sels, None, None, None);
    assert_eq!(v["state"], "refused");
    assert!(v["reason"].as_str().unwrap().contains("models.json"), "{v}");
    std::fs::remove_file(dir.join("models.json")).unwrap();
    assert_eq!(
        config_of(&sels, None, None, None)["state"],
        "not-configured"
    );
    std::fs::remove_dir_all(dir).unwrap();
}

/// Writes a program file at p, executable where this system has the bit.
fn program_file(p: &std::path::Path) {
    std::fs::create_dir_all(p.parent().unwrap()).unwrap();
    std::fs::write(p, "MZ").unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(p, std::fs::Permissions::from_mode(0o755)).unwrap();
    }
}

#[test]
fn the_recipe_starts_the_first_installed_omp_this_system_runs_else_the_one_on_path() {
    let d = tmp();
    let (home, local) = (d.join("home"), d.join("local"));
    let program = |os: &str, local: Option<&std::path::Path>| {
        crate::recipe(os, &home, local)["program"].clone()
    };
    for os in ["linux", "macos", "windows"] {
        assert_eq!(program(os, Some(&local)), "omp", "{os}");
    }
    // omp.sh/install's ~/.local/bin/omp, and install.ps1's %LOCALAPPDATA%\omp\omp.exe.
    let (script, script_exe) = (
        home.join(".local/bin/omp"),
        local.join("omp").join("omp.exe"),
    );
    program_file(&script);
    program_file(&script_exe);
    // A file without an extension is no program on Windows.
    let unix = |p: &std::path::Path| if cfg!(unix) { json!(p) } else { json!("omp") };
    assert_eq!(program("linux", None), unix(&script));
    assert_eq!(program("macos", None), unix(&script));
    assert_eq!(program("windows", Some(&local)), json!(script_exe));
    // No LOCALAPPDATA, or one that is not a full path: that copy is not looked for.
    assert_eq!(program("windows", None), "omp");
    assert_eq!(
        program("windows", Some(std::path::Path::new("local"))),
        "omp"
    );
    // bun's global copy comes first: ~/.bun/bin/omp, omp.exe on Windows.
    let (bun, bun_exe) = (
        home.join(".bun/bin/omp"),
        home.join(".bun").join("bin").join("omp.exe"),
    );
    program_file(&bun);
    program_file(&bun_exe);
    assert_eq!(program("linux", None), unix(&bun));
    assert_eq!(program("macos", None), unix(&bun));
    assert_eq!(program("windows", Some(&local)), json!(bun_exe));
    std::fs::remove_dir_all(d).unwrap();
}
