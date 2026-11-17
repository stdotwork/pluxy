//! The native test's hooks are off in a normal build: the console window waits for Enter and the
//! shells after the program are interactive, with no command. CI's tests build without the
//! `native-test` feature, so this runs there; with the feature it checks the hooks instead.

use super::{shell_flags, waits};

#[cfg(not(feature = "native-test"))]
#[test]
fn a_normal_build_waits_and_starts_interactive_shells() {
    assert!(waits());
    assert_eq!(shell_flags("cmd"), ["/D"]);
    assert_eq!(shell_flags("powershell"), ["-NoLogo", "-NoProfile"]);
}

#[cfg(feature = "native-test")]
#[test]
fn the_native_test_build_runs_fixed_after_exit_commands() {
    assert!(!waits());
    assert_eq!(shell_flags("cmd"), ["/D", "/C", "cd > pluxy-after-exit"]);
    assert!(shell_flags("powershell").contains(&"-NonInteractive"));
}
