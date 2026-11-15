use super::*;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;

/// A new private folder for one test.
fn folder() -> PathBuf {
    let d = std::env::temp_dir().join(format!(
        "pluxy-handover-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&d).unwrap();
    std::fs::set_permissions(&d, std::fs::Permissions::from_mode(0o700)).unwrap();
    d
}

fn spec() -> Spec {
    Spec {
        argv: vec![
            "/opt/agent/bin/agent".into(),
            "--prompt".into(),
            "it's \"quoted\" $HOME `x` \n; rm -rf /".into(),
        ],
        cwd: "/home/me/proj ect".into(),
        env: vec![
            ("PATH".into(), "/usr/bin:/bin".into()),
            ("AGENT_MODE".into(), "a b".into()),
        ],
    }
}

#[test]
fn the_launch_reaches_the_window_as_it_was_given() {
    let d = folder();
    let socket = d.join("s");
    let listener = UnixListener::bind(&socket).unwrap();
    let served = std::thread::spawn(move || serve(&listener, &spec(), Duration::from_secs(10)));
    assert_eq!(receive(&socket).unwrap(), spec());
    assert_eq!(served.join().unwrap(), Ok(()));
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_launch_no_one_takes_expires() {
    let d = folder();
    let listener = UnixListener::bind(d.join("s")).unwrap();
    let started = Instant::now();
    let e = serve(&listener, &spec(), Duration::from_millis(300)).unwrap_err();
    assert!(e.starts_with("launch expired"), "{e}");
    assert!(started.elapsed() < Duration::from_secs(5));
    std::fs::remove_dir_all(d).unwrap();
}

/// Serves bytes once on a new socket in d, as a hand-over would.
fn offer(d: &Path, bytes: Vec<u8>) -> std::thread::JoinHandle<()> {
    let listener = UnixListener::bind(d.join("s")).unwrap();
    std::thread::spawn(move || {
        let (mut s, _) = listener.accept().unwrap();
        // The reader may stop early (a launch too large): its end closing is not this test's.
        let _ = s.write_all(&bytes);
    })
}

#[test]
fn only_a_launch_pluxy_makes_is_taken() {
    for (bytes, why) in [
        (b"not json".to_vec(), "expected"),
        (
            br#"{"argv": [], "cwd": "/", "env": []}"#.to_vec(),
            "not one pluxy makes",
        ),
        (
            br#"{"argv": ["a"], "cwd": "/"}"#.to_vec(),
            "not one pluxy makes",
        ),
        (
            br#"{"argv": ["a"], "cwd": "/", "env": [["K"]]}"#.to_vec(),
            "not one pluxy makes",
        ),
        (vec![b' '; MAX as usize + 1], "too large"),
    ] {
        let d = folder();
        let offered = offer(&d, bytes);
        let e = receive(&d.join("s")).unwrap_err();
        assert!(e.contains(why), "{e}");
        offered.join().unwrap();
        std::fs::remove_dir_all(d).unwrap();
    }
    // No one offering: the launch has expired, or was taken.
    let d = folder();
    let e = receive(&d.join("s")).unwrap_err();
    assert!(e.starts_with("launch expired or already taken"), "{e}");
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn the_command_file_runs_the_helper_on_the_socket_and_removes_itself() {
    let d = folder().join("it's a folder");
    std::fs::create_dir(&d).unwrap();
    let file = d.join("launch.command");
    let socket = d.join("s '$x'");
    std::fs::write(&file, script(&file, "/bin/echo", &socket)).unwrap();
    // As Terminal runs it: a shell runs the file.
    let out = std::process::Command::new("/bin/sh")
        .arg(&file)
        .output()
        .unwrap();
    assert!(out.status.success(), "{out:?}");
    assert_eq!(
        String::from_utf8(out.stdout).unwrap(),
        format!("attach {}\n", socket.display())
    );
    assert!(!file.exists());
    std::fs::remove_dir_all(d.parent().unwrap()).unwrap();
}

fn pairs(v: &[(&str, &str)]) -> Vec<(String, String)> {
    v.iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect()
}

#[test]
fn the_window_gives_the_launch_its_terminal_names_and_nothing_else() {
    let own = pairs(&[
        ("PATH", "/opt/homebrew/bin:/usr/bin"),
        ("TERM", "xterm-256color"),
        ("TERM_PROGRAM", "Apple_Terminal"),
        ("HOME", "/Users/someone-else"),
        ("SHLVL", "1"),
    ]);
    assert_eq!(
        environment(&spec(), &own),
        pairs(&[
            ("AGENT_MODE", "a b"),
            ("PATH", "/opt/homebrew/bin:/usr/bin"),
            ("TERM", "xterm-256color"),
            ("TERM_PROGRAM", "Apple_Terminal"),
        ])
    );
    // A name the window lacks keeps the launch's value.
    assert_eq!(
        environment(&spec(), &pairs(&[("TERM", "dumb")])),
        pairs(&[
            ("PATH", "/usr/bin:/bin"),
            ("AGENT_MODE", "a b"),
            ("TERM", "dumb"),
        ])
    );
}

#[test]
fn the_peer_of_a_connection_is_this_user() {
    let (a, _b) = UnixStream::pair().unwrap();
    // SAFETY: geteuid has no preconditions.
    assert_eq!(peer_uid(&a).unwrap(), unsafe { libc::geteuid() });
}
