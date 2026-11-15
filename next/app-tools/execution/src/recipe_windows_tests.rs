//! Windows' launch recipes, on real files in a temporary folder.

use super::*;
use std::path::{Path, PathBuf};

/// A new folder for one test, by its plain resolved path.
fn folder() -> PathBuf {
    let d = std::env::temp_dir().join(format!(
        "pluxy-execution-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&d).unwrap();
    let r = std::fs::canonicalize(&d).unwrap();
    PathBuf::from(crate::winpath::plain(r.to_str().unwrap()).unwrap())
}

/// Writes a stand-in PE image (it starts with `MZ`; nothing runs it here).
fn pe(p: &Path) {
    std::fs::write(p, b"MZ\x90\x00stand-in").unwrap();
}

fn s(p: &Path) -> String {
    p.to_str().unwrap().to_string()
}

fn system32(name: &str) -> PathBuf {
    PathBuf::from(std::env::var_os("SystemRoot").unwrap())
        .join("System32")
        .join(name)
}

fn me() -> String {
    os::this_program().unwrap()
}

#[test]
fn the_terminals_are_the_console_and_the_shells_it_can_stay_at() {
    let t = terminals(&json!({"search_path": []}));
    assert_eq!(
        t["terminals"],
        json!([
            {"id": "automatic", "label": "Automatic"},
            {"id": "powershell", "label": "PowerShell",
                "path": system32(r"WindowsPowerShell\v1.0\powershell.exe")},
            {"id": "cmd", "label": "Command Prompt", "path": system32("cmd.exe")},
            {"id": "console", "label": "Console window"},
        ])
    );
}

#[test]
fn a_terminal_launch_runs_the_program_in_the_execution_programs_console() {
    let d = folder();
    pe(&d.join("agent.exe"));
    let agent = s(&d.join("agent.exe"));
    let args = json!(["a b", "\"quoted\" & calc", "", r"C:\dir\"]);
    let mut i = json!({"kind": "terminal", "program": "agent", "args": args, "cwd": d,
        "search_path": [d]});
    let mut want = vec![
        json!("terminal"),
        json!("--with"),
        json!("powershell"),
        json!("--"),
    ];
    want.push(json!(agent));
    want.extend(args.as_array().unwrap().iter().cloned());
    assert_eq!(
        prepare(&i).unwrap(),
        json!({"executable": me(), "argv": want, "cwd": s(&d), "program": agent})
    );
    i["terminal"] = json!("console");
    assert_eq!(prepare(&i).unwrap()["argv"][2], "console");
    // Windows Terminal is the person's default terminal, not a kind: wt.exe reads its command
    // line its own way.
    for t in ["wt", "gnome-terminal", "iterm2"] {
        i["terminal"] = json!(t);
        let e = prepare(&i).unwrap_err();
        assert_eq!(e.code, "invalid_request", "{t}: {}", e.message);
    }
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn programs_are_pe_images_found_in_windows_order() {
    let d = folder();
    pe(&d.join("agent.com"));
    pe(&d.join("agent.exe"));
    // Named .exe but not a PE image: not a program.
    std::fs::write(d.join("tool.exe"), b"#!/bin/sh\n").unwrap();
    let direct = |program: &str| {
        prepare(
            &json!({"kind": "direct", "program": program, "args": [], "cwd": d,
            "search_path": [d]}),
        )
    };
    assert_eq!(
        direct("agent").unwrap()["executable"],
        json!(s(&d.join("agent.com")))
    );
    assert_eq!(
        direct("agent.exe").unwrap()["executable"],
        json!(s(&d.join("agent.exe")))
    );
    assert_eq!(direct("tool").unwrap_err().code, "not_found");
    assert_eq!(
        direct(&s(&d.join("tool.exe"))).unwrap_err().code,
        "not_found"
    );
    for bad in [r"bin\agent", "C:agent", "bin/agent"] {
        assert_eq!(direct(bad).unwrap_err().code, "invalid_request", "{bad}");
    }
    std::fs::remove_dir_all(d).unwrap();
}

/// pnpm's shim for a Node script, and Yarn's for a native program (shim_tests.rs has them all).
const NODE_SHIM: &str = "@SETLOCAL\r\n@IF EXIST \"%~dp0\\node.exe\" (\r\n  \"%~dp0\\node.exe\"  \"%~dp0\\node_modules\\pkg\\cli.js\" %*\r\n) ELSE (\r\n  @SET PATHEXT=%PATHEXT:;.JS;=;%\r\n  node  \"%~dp0\\node_modules\\pkg\\cli.js\" %*\r\n)\r\n";
const NATIVE_SHIM: &str = "@\"%~dp0\\node_modules\\pkg\\bin\\tool.exe\"   %*\r\n";

#[test]
fn a_command_shim_runs_what_it_names_and_any_other_batch_file_is_refused() {
    let d = folder();
    let pkg = d.join(r"node_modules\pkg");
    std::fs::create_dir_all(pkg.join("bin")).unwrap();
    std::fs::write(pkg.join("cli.js"), b"console.log(1)\n").unwrap();
    pe(&pkg.join(r"bin\tool.exe"));
    std::fs::write(d.join("codex.cmd"), NODE_SHIM).unwrap();
    std::fs::write(d.join("tool.cmd"), NATIVE_SHIM).unwrap();
    std::fs::write(d.join("run.cmd"), "@echo off\r\nnode %~dp0\\x.js %*\r\n").unwrap();
    std::fs::write(d.join("up.cmd"), "@\"%~dp0\\..\\tool.exe\"   %*\r\n").unwrap();
    let node = folder();
    pe(&node.join("node.exe"));
    let direct = |program: &str| {
        prepare(
            &json!({"kind": "direct", "program": program, "args": ["--x"], "cwd": d,
            "search_path": [d, node]}),
        )
    };
    // Node from the search path, with the script first.
    assert_eq!(
        direct("codex").unwrap(),
        json!({"executable": s(&node.join("node.exe")),
            "argv": [s(&pkg.join("cli.js")), "--x"], "cwd": s(&d)})
    );
    // node.exe beside the shim comes first, as the shim's own test.
    pe(&d.join("node.exe"));
    assert_eq!(
        direct("codex").unwrap()["executable"],
        json!(s(&d.join("node.exe")))
    );
    assert_eq!(
        direct("tool").unwrap(),
        json!({"executable": s(&pkg.join(r"bin\tool.exe")), "argv": ["--x"], "cwd": s(&d)})
    );
    // A terminal launch names what the shim runs as its program.
    let t = prepare(
        &json!({"kind": "terminal", "program": "tool", "args": [], "cwd": d,
        "search_path": [d]}),
    )
    .unwrap();
    assert_eq!(t["program"], json!(s(&pkg.join(r"bin\tool.exe"))));
    for other in ["run", "up"] {
        let e = direct(other).unwrap_err();
        assert_eq!(e.code, "invalid_request", "{other}: {}", e.message);
        assert!(
            e.message.ends_with(crate::shim::NOT_A_SHIM),
            "{}",
            e.message
        );
    }
    for f in [d, node] {
        std::fs::remove_dir_all(f).unwrap();
    }
}

#[test]
fn a_program_that_reads_its_command_line_its_own_way_takes_no_arguments() {
    let d = folder();
    let cmd = s(&system32("cmd.exe"));
    let direct = |args: Value| {
        prepare(
            &json!({"kind": "direct", "program": cmd, "args": args, "cwd": d,
            "search_path": []}),
        )
    };
    let e = direct(json!(["/c", "calc"])).unwrap_err();
    assert_eq!(e.code, "invalid_request");
    assert!(e.message.contains("its own way"), "{}", e.message);
    assert!(direct(json!([])).is_ok());
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_verbatim_path_shows_plainly_and_is_refused_when_its_plain_form_is_another_file() {
    let d = folder();
    pe(&d.join("agent.exe"));
    let direct = |program: &str| {
        prepare(
            &json!({"kind": "direct", "program": program, "args": [], "cwd": d,
            "search_path": []}),
        )
    };
    let verbatim = format!(r"\\?\{}", s(&d));
    assert_eq!(
        direct(&format!(r"{verbatim}\agent.exe")).unwrap()["executable"],
        json!(s(&d.join("agent.exe")))
    );
    // `dir.` exists only in the verbatim form: the plain `dir.\agent.exe` is `dir\agent.exe`.
    std::fs::create_dir(format!(r"{verbatim}\dir.")).unwrap();
    pe(Path::new(&format!(r"{verbatim}\dir.\agent.exe")));
    std::fs::create_dir(d.join("dir")).unwrap();
    pe(&d.join(r"dir\agent.exe"));
    let e = direct(&format!(r"{verbatim}\dir.\agent.exe")).unwrap_err();
    assert_eq!(e.code, "invalid_request");
    assert!(e.message.contains("would change"), "{}", e.message);
    std::fs::remove_dir_all(format!(r"{verbatim}\dir.")).unwrap();
    std::fs::remove_dir_all(d).unwrap();
}

/// A folder to open files in, with a session file; and the folder above it.
fn files() -> (PathBuf, PathBuf, PathBuf) {
    let root = folder();
    let within = root.join("codex");
    std::fs::create_dir_all(within.join("sessions")).unwrap();
    let file = within.join(r"sessions\rollout-a.jsonl");
    std::fs::write(&file, b"{}\n").unwrap();
    (root, within, file)
}

#[test]
fn an_open_launch_runs_the_execution_programs_open_mode_on_the_resolved_file() {
    let (root, within, file) = files();
    let named = within.join(r"sessions\..\sessions\rollout-a.jsonl");
    let i = json!({"kind": "open", "file": named, "within": within});
    assert_eq!(
        prepare(&i).unwrap(),
        json!({"executable": me(), "argv": ["open", "--", s(&file)],
            "cwd": s(&within.join("sessions")), "file": s(&file), "within": s(&within)})
    );
    std::fs::write(within.join(r"sessions\run.exe"), b"MZ").unwrap();
    std::fs::write(within.join(r"sessions\notes.html"), b"<p>").unwrap();
    for name in ["run.exe", "notes.html"] {
        let i = json!({"kind": "open", "file": within.join("sessions").join(name),
            "within": within});
        let e = prepare(&i).unwrap_err();
        assert!(e.message.contains(".txt, .json"), "{name}: {}", e.message);
    }
    // A drive's root is not a within folder.
    let drive = &s(&root)[..3];
    let e = prepare(&json!({"kind": "open", "file": file, "within": drive})).unwrap_err();
    assert!(e.message.contains("cannot be /"), "{}", e.message);
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn the_opener_refuses_network_paths_streams_long_paths_and_types_no_app_opens() {
    for (p, why) in [
        (r"\\server\share\a.txt".to_string(), "network path"),
        (r"C:\dir\a:stream.txt".to_string(), ""),
        (format!(r"C:\{}.txt", "a".repeat(260)), "260"),
    ] {
        let e = os::opener(Path::new(&p)).unwrap_err();
        assert_eq!(e.code, "invalid_request", "{p}");
        assert!(e.message.contains(why), "{p}: {}", e.message);
    }
    // Windows opens text files out of the box; an unknown type has no app (and ShellExecuteExW
    // with no dialog would report success and open nothing).
    assert!(os::opens("txt"));
    assert!(!os::opens("pluxy-no-such-type"));
}

#[test]
fn a_packaged_app_starts_through_the_execution_programs_app_mode() {
    let d = folder();
    let calc = "Microsoft.WindowsCalculator_8wekyb3d8bbwe!App";
    let mut i = json!({"kind": "direct", "program": format!(r"shell:AppsFolder\{calc}"),
        "args": [], "cwd": d, "search_path": []});
    assert_eq!(
        prepare(&i).unwrap(),
        json!({"executable": me(), "argv": ["app", "--", calc], "cwd": s(&d)})
    );
    i["args"] = json!(["--x"]);
    assert_eq!(prepare(&i).unwrap_err().code, "invalid_request");
    i["args"] = json!([]);
    i["program"] = json!(r"shell:AppsFolder\notepad");
    assert_eq!(prepare(&i).unwrap_err().code, "invalid_request");
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn an_app_id_is_a_packaged_apps() {
    for ok in [
        "Microsoft.WindowsCalculator_8wekyb3d8bbwe!App",
        "Example.Tool-2_0123456789abc!App.Main",
        "Microsoft.WindowsTerminal_8wekyb3d8bbwe!App",
    ] {
        assert!(os::aumid_ok(ok), "{ok}");
    }
    for bad in [
        "notepad",
        "Microsoft.WindowsCalculator!App",
        "Microsoft.WindowsCalculator_8wekyb3d8bbwe!",
        "Microsoft.WindowsCalculator_8WEKYB3D8BBWE!App",
        "Microsoft.WindowsCalculator_8wekyb3d8bbw!App",
        "Micro soft_8wekyb3d8bbwe!App",
        r"x_8wekyb3d8bbwe!App\..\..",
        "x_8wekyb3d8bbwe!App&calc",
    ] {
        assert!(!os::aumid_ok(bad), "{bad}");
    }
}
