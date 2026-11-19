//! The terminal's run of its program (`run`), on real files in a temporary folder.

use super::{run, BEFORE_RUN};
use std::cell::RefCell;
use std::path::{Path, PathBuf};
use std::rc::Rc;

/// A new folder for one test, by its plain resolved path.
fn folder() -> PathBuf {
    let d = std::env::temp_dir().join(crate::test_folder_name("pluxy-launch"));
    std::fs::create_dir_all(&d).unwrap();
    let r = std::fs::canonicalize(&d).unwrap();
    PathBuf::from(crate::winpath::plain(r.to_str().unwrap()).unwrap())
}

/// Once the terminal has checked its program, nothing another program tries can put anything
/// else in its place before it runs: renaming, replacing, writing or deleting the program, or
/// renaming its folder or a folder above. The program that runs is the one checked.
#[test]
fn a_program_cannot_be_swapped_between_its_check_and_its_run() {
    let dir = folder();
    let bin = dir.join("bin");
    std::fs::create_dir(&bin).unwrap();
    // The program is this test program again, which lists no tests and ends with 0; what would be
    // swapped in starts `MZ` but is no program, which Windows would not start.
    let program = bin.join("agent.exe");
    std::fs::copy(std::env::current_exe().unwrap(), &program).unwrap();
    let other = dir.join("other.exe");
    std::fs::write(&other, b"MZ\x90\x00stand-in").unwrap();
    let done: Rc<RefCell<Vec<String>>> = Rc::default();
    let record = Rc::clone(&done);
    let (d, b) = (dir.clone(), bin.clone());
    BEFORE_RUN.with(|h| {
        *h.borrow_mut() = Some(Box::new(move |p: &Path| {
            let tries: [(&str, std::io::Result<()>); 6] = [
                (
                    "renamed the program",
                    std::fs::rename(p, d.join("moved.exe")),
                ),
                ("replaced the program", std::fs::rename(&other, p)),
                (
                    "wrote the program",
                    std::fs::OpenOptions::new().write(true).open(p).map(drop),
                ),
                ("deleted the program", std::fs::remove_file(p)),
                (
                    "renamed its folder",
                    std::fs::rename(&b, d.join("bin-moved")),
                ),
                (
                    "renamed the folder above it",
                    std::fs::rename(&d, d.with_extension("moved")),
                ),
            ];
            for (what, r) in tries {
                if r.is_ok() {
                    record.borrow_mut().push(what.to_string());
                }
            }
        }))
    });
    let ran = run(
        program.to_str().unwrap(),
        &["--list", "--exact", "pluxy-no-test-has-this-name"],
    );
    BEFORE_RUN.with(|h| h.borrow_mut().take());
    assert!(
        done.borrow().is_empty(),
        "after the check, another program {}",
        done.borrow().join(", ")
    );
    assert_eq!(ran, Ok(0), "the program checked ran");
    std::fs::remove_dir_all(&dir).unwrap();
}
