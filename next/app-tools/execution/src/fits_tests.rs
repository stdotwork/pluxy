//! What a launch may carry before the host is asked (`fits`, `program_fits`): on Windows and macOS
//! its standard input and an app launch's fields; on Linux and macOS a terminal's program.

use super::fits;
use serde_json::json;

fn launch() -> serde_json::Value {
    json!({"executable": "/x/execution", "argv": ["terminal"], "cwd": "/"})
}

#[test]
fn a_terminal_launch_takes_no_standard_input() {
    let mut i = launch();
    assert!(fits(&i, true, false).is_ok());
    i["stdin"] = json!("key");
    let e = fits(&i, true, false).unwrap_err();
    assert_eq!(e.code, "invalid_request");
    assert!(e.message.contains("no standard input"), "{}", e.message);
    // Any other launch with input is the host's to judge.
    assert!(fits(&i, false, false).is_ok());
}

#[test]
fn an_app_launch_carries_nothing_but_the_app() {
    let mut bare = launch();
    bare["lifetime"] = json!("detached");
    bare["environment"] = json!({"set": {}, "unset": [], "inherit": [], "secrets": {}});
    assert!(fits(&bare, false, true).is_ok());
    for (k, v) in [
        ("environment", json!({"set": {"A": "1"}})),
        ("environment", json!({"inherit": ["HOME"]})),
        ("environment", json!({"unset": ["PATH"]})),
        ("environment", json!({"secrets": {"KEY": "key"}})),
        ("stdin", json!("key")),
        ("key", json!("ref-1")),
        ("lifetime", json!("managed")),
    ] {
        let mut i = bare.clone();
        i[k] = v.clone();
        let e = fits(&i, false, true).unwrap_err();
        assert_eq!(e.code, "invalid_request", "{k}: {v}");
        assert!(
            e.message.starts_with("an app launch takes no"),
            "{}",
            e.message
        );
        // The same fields on a launch that is not an app's are not refused here.
        assert!(fits(&i, false, false).is_ok(), "{k}: {v}");
    }
}

/// A terminal launch names as its program the argument its terminal runs, which the host checks
/// as that file; any other launch names none.
#[cfg(not(windows))]
#[test]
fn a_terminal_launchs_program_is_the_argument_its_terminal_runs() {
    use super::program_fits;
    let launch = |argv: serde_json::Value| json!({"executable": "/t", "argv": argv, "cwd": "/"});
    let mut runs = launch(json!(["-e", "/bin/agent", "/bin/other"]));
    runs["program"] = json!("/bin/agent");
    assert!(program_fits(&runs, Some(1)).is_ok());
    // Another argument named, or none.
    let mut other = runs.clone();
    other["program"] = json!("/bin/other");
    let mut unnamed = runs.clone();
    unnamed.as_object_mut().unwrap().remove("program");
    for i in [&other, &unnamed] {
        let e = program_fits(i, Some(1)).unwrap_err();
        assert_eq!(e.code, "invalid_request");
        assert!(
            e.message.contains("the argument its terminal runs"),
            "{}",
            e.message
        );
    }
    // A launch that is not a terminal's names no program.
    let e = program_fits(&runs, None).unwrap_err();
    assert!(
        e.message.starts_with("only a terminal launch"),
        "{}",
        e.message
    );
    assert!(program_fits(&unnamed, None).is_ok());
}

/// On Linux a launch of a terminal pluxy knows is a terminal launch: its program is right after
/// that terminal's flag, and a launch with any other start, or no program, is refused before the
/// host is asked.
#[cfg(target_os = "linux")]
#[test]
fn on_linux_a_launch_of_a_known_terminal_names_its_program_after_its_flag() {
    use super::launch_fits;
    let mut i = json!({"executable": "/usr/bin/gnome-terminal", "argv": ["--", "/bin/agent"],
        "cwd": "/", "program": "/bin/agent"});
    assert!(launch_fits(&i).is_ok());
    i["argv"] = json!(["-e", "/bin/agent"]);
    let e = launch_fits(&i).unwrap_err();
    assert!(
        e.message.contains("runs its program after --"),
        "{}",
        e.message
    );
    i["argv"] = json!(["--", "/bin/agent"]);
    i.as_object_mut().unwrap().remove("program");
    let e = launch_fits(&i).unwrap_err();
    assert!(
        e.message.contains("the argument its terminal runs"),
        "{}",
        e.message
    );
    // A program that is no terminal pluxy knows: a direct launch, naming no program.
    let direct = json!({"executable": "/bin/agent", "argv": ["-e", "/bin/x"], "cwd": "/"});
    assert!(launch_fits(&direct).is_ok());
    // A known terminal started with no command, or with its own options only, naming no program:
    // a direct launch of the terminal. Naming a program, it must run it after its exec flag.
    for (exe, argv) in [
        ("/usr/bin/kitty", json!([])),
        ("/usr/bin/xterm", json!(["-fa", "Mono"])),
    ] {
        let mut plain = json!({"executable": exe, "argv": argv, "cwd": "/"});
        assert!(launch_fits(&plain).is_ok(), "{plain}");
        plain["program"] = json!("/bin/agent");
        let e = launch_fits(&plain).unwrap_err();
        assert!(
            e.message.contains("runs its program after -e"),
            "{}",
            e.message
        );
    }
    // Its exec flag first and no program: refused, the command would go unchecked.
    let unnamed = json!({"executable": "/usr/bin/xterm", "argv": ["-e", "/tmp/x"], "cwd": "/"});
    let e = launch_fits(&unnamed).unwrap_err();
    assert!(
        e.message.contains("the argument its terminal runs"),
        "{}",
        e.message
    );
}

/// On macOS a terminal launch is the execution program's terminal mode: its program is the
/// argument right after `--`, and one that names none, or another, is refused before the host is
/// asked.
#[cfg(target_os = "macos")]
#[test]
fn on_macos_a_terminal_launch_names_its_program_after_its_dashes() {
    use super::launch_fits;
    let me = super::recipe::os::this_program().unwrap();
    let mut i = json!({"executable": me, "cwd": "/", "argv": [
        "terminal", "--with", "terminal", "--app", "/T.app", "--", "/bin/agent", "/bin/other"]});
    let e = launch_fits(&i).unwrap_err();
    assert!(
        e.message.contains("the argument its terminal runs"),
        "{}",
        e.message
    );
    i["program"] = json!("/bin/other");
    assert!(launch_fits(&i).is_err(), "another argument passed");
    i["program"] = json!("/bin/agent");
    assert!(launch_fits(&i).is_ok());
}
