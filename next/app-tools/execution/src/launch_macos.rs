//! The execution program's launch modes on macOS, run by the app as a confirmed launch:
//!
//! - `terminal --with terminal --app <Terminal.app> -- <program> <args>…`: hands the launch (the
//!   program and arguments, the working folder and environment it was started with) to a new
//!   Terminal window: it makes a private folder in the per-user temporary folder with a socket and
//!   a `.command` file, has Terminal open the file (`/usr/bin/open -a`), and gives the launch to the
//!   first connection from this user on the socket, then removes the folder.
//! - `attach <socket>`: what the `.command` runs inside the window: takes the launch and runs the
//!   program in place of itself, with the launch's environment and the terminal's own names
//!   (`handover::TERMINAL_NAMES`).

use crate::handover::{self, Spec};
use std::io::Write;
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::os::unix::net::UnixListener;
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{Duration, Instant};

/// How long `open` may take to hand the file to Terminal: it does not return while macOS holds a
/// file back (a quarantined one, or a permission prompt).
const OPEN_WAIT: Duration = Duration::from_secs(20);
/// How long Terminal's window may take to take the launch.
const ATTACH_WAIT: Duration = Duration::from_secs(60);

pub fn main(args: &[String]) -> i32 {
    let args: Vec<&str> = args.iter().map(String::as_str).collect();
    let r = match args.as_slice() {
        ["terminal", "--with", "terminal", "--app", app, "--", program, rest @ ..] => {
            terminal(app, program, rest)
        }
        ["attach", socket] => attach(Path::new(socket)),
        _ => Err("execution: not a launch this program makes".into()),
    };
    match r {
        Ok(()) => 0,
        Err(e) => {
            eprintln!("pluxy: {e}");
            1
        }
    }
}

/// The per-user temporary folder, when it is this user's alone.
fn temporary() -> Result<PathBuf, String> {
    let t = PathBuf::from(std::env::var_os("TMPDIR").ok_or("no TMPDIR")?);
    let m = std::fs::metadata(&t).map_err(|e| format!("TMPDIR {}: {e}", t.display()))?;
    // SAFETY: geteuid has no preconditions.
    if !t.is_absolute()
        || !m.is_dir()
        || m.uid() != unsafe { libc::geteuid() }
        || m.mode() & 0o022 != 0
    {
        return Err(format!(
            "TMPDIR {} is not a folder that is yours alone",
            t.display()
        ));
    }
    Ok(t)
}

/// 16 random hex digits.
fn random() -> Result<String, String> {
    use std::io::Read;
    let mut b = [0u8; 8];
    std::fs::File::open("/dev/urandom")
        .and_then(|mut f| f.read_exact(&mut b))
        .map_err(|e| e.to_string())?;
    Ok(b.iter().map(|x| format!("{x:02x}")).collect())
}

fn terminal(app: &str, program: &str, args: &[&str]) -> Result<(), String> {
    if !crate::recipe::os::terminal_bundle(app) {
        return Err(format!("{app} is not Terminal"));
    }
    let env = std::env::vars_os()
        .map(|(k, v)| match (k.into_string(), v.into_string()) {
            (Ok(k), Ok(v)) => Ok((k, v)),
            _ => Err("the launch's environment is not UTF-8".to_string()),
        })
        .collect::<Result<Vec<_>, _>>()?;
    let cwd = std::env::current_dir()
        .map_err(|e| e.to_string())?
        .into_os_string()
        .into_string()
        .map_err(|_| "the working folder is not UTF-8".to_string())?;
    let mut argv = vec![program.to_string()];
    argv.extend(args.iter().map(|a| a.to_string()));
    let spec = Spec { argv, cwd, env }.encode()?;
    let helper = std::env::current_exe()
        .and_then(std::fs::canonicalize)
        .map_err(|e| e.to_string())?;
    let helper = helper
        .to_str()
        .ok_or("the execution program's path is not UTF-8")?;

    let dir = temporary()?.join(format!("pluxy-launch-{}", random()?));
    // Terminal types the file's path into its shell: keep it to letters, digits and `/._-`.
    let plain = dir.to_str().is_some_and(|d| {
        d.chars()
            .all(|c| c.is_ascii_alphanumeric() || "/._-".contains(c))
    });
    if !plain {
        return Err(format!(
            "the temporary folder {} has a name Terminal could misread",
            dir.display()
        ));
    }
    pluxy_files::create_private_dir(&dir).map_err(|e| format!("{}: {e}", dir.display()))?;
    let r = hand_over(&dir, app, helper, &spec);
    let _ = std::fs::remove_dir_all(&dir);
    r
}

fn hand_over(dir: &Path, app: &str, helper: &str, spec: &[u8]) -> Result<(), String> {
    let socket = dir.join("s");
    if socket.as_os_str().len() > 103 {
        return Err("the temporary folder's path is too long for a socket".into());
    }
    let listener = UnixListener::bind(&socket).map_err(|e| e.to_string())?;
    let file = dir.join("launch.command");
    std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o700)
        .open(&file)
        .and_then(|mut f| f.write_all(handover::script(&file, helper, &socket).as_bytes()))
        .map_err(|e| format!("{}: {e}", file.display()))?;
    unquarantine(&file);
    let mut open = Command::new("/usr/bin/open")
        .arg("-a")
        .arg(app)
        .arg(&file)
        .spawn()
        .map_err(|e| format!("/usr/bin/open: {e}"))?;
    let end = Instant::now() + OPEN_WAIT;
    loop {
        match open.try_wait().map_err(|e| e.to_string())? {
            Some(s) if s.success() => break,
            Some(s) => return Err(format!("Terminal did not open the launch (open: {s})")),
            // Not waited on further, and not stopped: it ends when macOS lets it.
            None if Instant::now() >= end => {
                return Err(
                    "launch blocked: macOS did not open Terminal (a quarantine or permission \
                     prompt?)"
                        .into(),
                )
            }
            None => std::thread::sleep(Duration::from_millis(50)),
        }
    }
    handover::serve(&listener, spec, ATTACH_WAIT)
}

/// Removes a quarantine attribute from file, if it has one: macOS would hold a quarantined
/// `.command` back. The execution program does not quarantine what it writes; this is a guard.
fn unquarantine(file: &Path) {
    use std::os::unix::ffi::OsStrExt;
    if let Ok(c) = std::ffi::CString::new(file.as_os_str().as_bytes()) {
        // SAFETY: both strings are NUL-terminated.
        unsafe {
            libc::removexattr(
                c.as_ptr(),
                c"com.apple.quarantine".as_ptr(),
                libc::XATTR_NOFOLLOW,
            )
        };
    }
}

fn attach(socket: &Path) -> Result<(), String> {
    let spec = handover::receive(socket)?;
    let own: Vec<(String, String)> = std::env::vars_os()
        .filter_map(|(k, v)| Some((k.into_string().ok()?, v.into_string().ok()?)))
        .collect();
    let env = handover::environment(&spec, &own);
    let e = Command::new(&spec.argv[0])
        .args(&spec.argv[1..])
        .env_clear()
        .envs(env)
        .current_dir(&spec.cwd)
        .exec();
    Err(format!("could not start {}: {e}", spec.argv[0]))
}
