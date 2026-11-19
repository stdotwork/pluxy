use super::*;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;

/// A new private folder for one test.
fn folder() -> PathBuf {
    let d = std::env::temp_dir().join(crate::test_folder_name("pluxy-handover"));
    std::fs::create_dir_all(&d).unwrap();
    std::fs::set_permissions(&d, std::fs::Permissions::from_mode(0o700)).unwrap();
    d
}

/// A program file in d, as the app would have opened it.
fn program(d: &Path) -> File {
    let p = d.join("agent");
    std::fs::write(&p, "#!/bin/sh\n").unwrap();
    File::open(p).unwrap()
}

/// Whether a and b are the same file.
fn same_file(a: &File, b: &File) -> bool {
    use std::os::unix::fs::MetadataExt;
    let (a, b) = (a.metadata().unwrap(), b.metadata().unwrap());
    (a.dev(), a.ino()) == (b.dev(), b.ino())
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
    let checked = program(&d);
    let given = checked.try_clone().unwrap();
    let served = std::thread::spawn(move || {
        serve(
            &listener,
            &spec().encode().unwrap(),
            &given,
            Duration::from_secs(10),
        )
    });
    let (taken, file) = receive(&socket).unwrap();
    assert_eq!(taken, spec());
    assert!(same_file(&file, &checked), "another file came");
    // SAFETY: file's descriptor is open while it lives.
    let flags = unsafe { libc::fcntl(file.as_raw_fd(), libc::F_GETFD) };
    assert_eq!(
        flags & libc::FD_CLOEXEC,
        libc::FD_CLOEXEC,
        "it stays open across exec"
    );
    assert_eq!(served.join().unwrap(), Ok(()));
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_launch_no_one_takes_expires() {
    let d = folder();
    let listener = UnixListener::bind(d.join("s")).unwrap();
    let started = Instant::now();
    let e = serve(
        &listener,
        &spec().encode().unwrap(),
        &program(&d),
        Duration::from_millis(300),
    )
    .unwrap_err();
    assert!(e.starts_with("launch expired"), "{e}");
    assert!(started.elapsed() < Duration::from_secs(5));
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_launch_over_what_a_hand_over_carries_is_refused_before_it_is_offered() {
    // Each control character is six bytes in the hand-over (`\u0001`).
    let mut big = spec();
    big.argv.push("\u{1}".repeat(MAX as usize / 6 + 1));
    let Err(e) = big.encode() else {
        panic!("a launch over MAX was offered");
    };
    assert!(e.starts_with("the launch is too large"), "{e}");
    // Just under: offered, and taken whole.
    let mut near = spec();
    let room = MAX as usize - near.encode().unwrap().len() - 3;
    near.argv.push("x".repeat(room));
    assert_eq!(near.encode().unwrap().len() as u64, MAX);
    let d = folder();
    let socket = d.join("s");
    let listener = UnixListener::bind(&socket).unwrap();
    let bytes = near.encode().unwrap();
    let given = program(&d);
    let served =
        std::thread::spawn(move || serve(&listener, &bytes, &given, Duration::from_secs(10)));
    assert_eq!(receive(&socket).unwrap().0, near);
    assert_eq!(served.join().unwrap(), Ok(()));
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
        // A launch with no program attached: the window runs only the file the app checked.
        (spec().encode().unwrap(), "came with 0 programs"),
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

/// How many descriptors this process has open on files in d: other tests open their own.
#[cfg(target_os = "linux")]
fn open_in(d: &Path) -> usize {
    std::fs::read_dir("/proc/self/fd")
        .unwrap()
        .filter_map(|e| std::fs::read_link(e.ok()?.path()).ok())
        .filter(|t| t.starts_with(d))
        .count()
}

/// Sends bytes on s with fds attached, in one message.
#[cfg(target_os = "linux")]
fn send_fds(s: &UnixStream, bytes: &[u8], fds: &[i32]) {
    let mut control = vec![0u64; 64];
    // SAFETY: CMSG_LEN and CMSG_SPACE only compute sizes; control has room for 40 descriptors.
    let (len, space) = unsafe {
        (
            libc::CMSG_LEN((fds.len() * 4) as u32),
            libc::CMSG_SPACE((fds.len() * 4) as u32),
        )
    };
    let mut iov = libc::iovec {
        iov_base: bytes.as_ptr() as *mut _,
        iov_len: bytes.len(),
    };
    // SAFETY: an all-zero msghdr is a valid empty one, filled to point at iov and control.
    unsafe {
        let mut msg: libc::msghdr = std::mem::zeroed();
        msg.msg_iov = &mut iov;
        msg.msg_iovlen = 1;
        msg.msg_control = control.as_mut_ptr().cast();
        msg.msg_controllen = space as _;
        let c = libc::CMSG_FIRSTHDR(&msg);
        (*c).cmsg_level = libc::SOL_SOCKET;
        (*c).cmsg_type = libc::SCM_RIGHTS;
        (*c).cmsg_len = len as _;
        let d = libc::CMSG_DATA(c).cast::<i32>();
        for (i, fd) in fds.iter().enumerate() {
            std::ptr::write_unaligned(d.add(i), *fd);
        }
        assert!(libc::sendmsg(s.as_raw_fd(), &msg, 0) >= 0);
    }
}

/// Offers a launch on a new socket in d, with fds attached.
#[cfg(target_os = "linux")]
fn offer_fds(d: &Path, bytes: Vec<u8>, fds: Vec<File>) -> std::thread::JoinHandle<()> {
    let listener = UnixListener::bind(d.join("s")).unwrap();
    std::thread::spawn(move || {
        let (s, _) = listener.accept().unwrap();
        let raw: Vec<i32> = fds.iter().map(|f| f.as_raw_fd()).collect();
        send_fds(&s, &bytes, &raw);
        let _ = s.shutdown(std::net::Shutdown::Write);
    })
}

/// A launch with more than one descriptor attached (2, and 40, more than the control buffer
/// holds) is refused, and none of them stays open.
#[cfg(target_os = "linux")]
#[test]
fn only_one_program_with_the_launch_is_taken_and_no_other_stays_open() {
    for fds in [2, 40] {
        let d = folder();
        let files = (0..fds).map(|_| program(&d)).collect();
        let offered = offer_fds(&d, spec().encode().unwrap(), files);
        let r = receive(&d.join("s")).map(|_| ());
        offered.join().unwrap();
        let left = open_in(&d);
        std::fs::remove_dir_all(d).unwrap();
        assert!(r.is_err(), "{fds} descriptors: taken");
        assert_eq!(left, 0, "{fds} descriptors: one stayed open");
    }
}
