//! What runs inside a distribution, and reading what it prints: the fixed scripts of `users` and
//! `check` (constants, never built from input; values reach them only as `sh -c` arguments), the
//! users a person may pick, and a user's home, processor and mounted Windows drives.

use crate::list::user_ok;
use pluxy_program::Error;
use serde_json::{json, Value};

/// `users`, as the distribution's default user: who that is, the processor, then the accounts
/// (`getent` where there is one, else `/etc/passwd`). No login shell and no rc file is read.
pub const USERS: &str =
    "printf '%s\\n' \"$(id -un)\" \"$(uname -m)\"; getent passwd 2>/dev/null || cat /etc/passwd";

/// `check`, as the selected user: their name and number, home, the processor, the distribution
/// WSL says this is, then the mounts.
pub const CHECK: &str = "printf '%s\\n' \"$(id -un)\" \"$(id -u)\" \"$HOME\" \"$(uname -m)\" \
     \"$WSL_DISTRO_NAME\"; cat /proc/self/mountinfo";

/// The lowest and highest user numbers of a person's account (Debian's and Ubuntu's ranges).
const PEOPLE: std::ops::RangeInclusive<u64> = 1000..=59999;

/// pluxy's name for a processor `uname -m` reports: the Linux builds pluxy has.
pub fn arch(machine: &str) -> Result<&'static str, Error> {
    match machine {
        "x86_64" => Ok("amd64"),
        "aarch64" | "arm64" => Ok("arm64"),
        m => Err(Error::unsupported(format!(
            "pluxy has no Linux build for {m}"
        ))),
    }
}

/// `users` from what [`USERS`] printed: root (shown last) and each person's account (a number of
/// 1000 to 59999, a login shell and an absolute home); which one is the default; the processor.
pub fn users(out: &str) -> Result<Value, Error> {
    let mut lines = out.lines();
    let (Some(default), Some(machine)) = (lines.next(), lines.next()) else {
        return Err(Error::unavailable(
            "the distribution did not say who its users are",
        ));
    };
    let arch = arch(machine.trim())?;
    let mut people = Vec::new();
    let mut roots = Vec::new();
    for line in lines {
        let f: Vec<&str> = line.split(':').collect();
        let [name, _, uid, _, _, home, shell] = f[..] else {
            continue;
        };
        let Ok(uid) = uid.parse::<u64>() else {
            continue;
        };
        let login = !shell.ends_with("/nologin") && !shell.ends_with("/false") && !shell.is_empty();
        let listed = uid == 0 || (PEOPLE.contains(&uid) && login && home.starts_with('/'));
        if !listed
            || !user_ok(name)
            || people
                .iter()
                .chain(&roots)
                .any(|u: &Value| u["name"] == name)
        {
            continue;
        }
        let u = json!({"name": name, "uid": uid, "home": home, "default": name == default,
            "root": uid == 0});
        if uid == 0 {
            roots.push(u);
        } else {
            people.push(u);
        }
    }
    people.sort_by(|a, b| a["name"].as_str().cmp(&b["name"].as_str()));
    people.extend(roots);
    Ok(json!({"users": people, "default": default, "arch": arch}))
}

/// A mount from one line of `/proc/self/mountinfo`: where, its type and its options.
#[derive(Debug, PartialEq, Eq)]
pub struct Mount {
    pub point: String,
    pub fstype: String,
    pub options: String,
}

/// A mount point as mountinfo writes it: space, tab, newline and backslash as octal escapes.
fn unescape(s: &str) -> String {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        let oct = |c: u8| (b'0'..=b'7').contains(&c);
        let v = (b[i] == b'\\' && i + 3 < b.len() && b[i + 1..i + 4].iter().all(|c| oct(*c)))
            .then(|| {
                b[i + 1..i + 4]
                    .iter()
                    .fold(0u32, |v, c| v * 8 + u32::from(c - b'0'))
            })
            .and_then(|v| u8::try_from(v).ok());
        if let Some(v) = v {
            out.push(v);
            i += 4;
        } else {
            out.push(b[i]);
            i += 1;
        }
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// The mounts in `/proc/self/mountinfo` text; lines it cannot read are left out.
pub fn mounts(text: &str) -> Vec<Mount> {
    text.lines()
        .filter_map(|line| {
            let f: Vec<&str> = line.split(' ').collect();
            let dash = f.iter().position(|x| *x == "-")?;
            if dash < 6 || f.len() < dash + 2 {
                return None;
            }
            Some(Mount {
                point: unescape(f[4]),
                fstype: f[dash + 1].to_string(),
                options: f[5].to_string(),
            })
        })
        .collect()
}

/// A Windows drive WSL mounted: a mount of a type WSL mounts drives with whose last part is one
/// drive letter (`/mnt/c`).
pub fn drive(m: &Mount) -> Option<char> {
    let windows = matches!(m.fstype.as_str(), "drvfs" | "9p" | "virtiofs");
    let (_, last) = m.point.rsplit_once('/')?;
    let mut c = last.chars();
    match (c.next(), c.next()) {
        (Some(l), None) if windows && l.is_ascii_alphabetic() => Some(l.to_ascii_lowercase()),
        _ => None,
    }
}

/// The Windows drives mounted (`{"letter", "path", "exec"}`, `exec` false on a `noexec` mount)
/// and the folder WSL mounts them in (the automount root, with a trailing `/`; that of drive C
/// when there are several, else the first; null with none).
pub fn drives(mountinfo: &str) -> (Value, Value) {
    // Of mounts stacked on one folder, the last is the one seen there (as pluxy-transactions
    // reads them).
    let all = mounts(mountinfo);
    let mut list = Vec::new();
    let mut root = Value::Null;
    for (i, m) in all.iter().enumerate() {
        if all[i + 1..].iter().any(|later| later.point == m.point) {
            continue;
        }
        let Some(letter) = drive(m) else { continue };
        let parent = &m.point[..m.point.len() - 1];
        if root.is_null() || letter == 'c' {
            root = json!(parent);
        }
        let exec = !m.options.split(',').any(|o| o == "noexec");
        list.push(json!({"letter": letter.to_string(), "path": m.point, "exec": exec}));
    }
    (Value::Array(list), root)
}

/// `check` from what [`CHECK`] printed, run as user in distro: refused when it ran as someone
/// else, in another distribution, or with no absolute home.
pub fn check(out: &str, distro: &str, user: &str) -> Result<Value, Error> {
    let mut lines = out.lines();
    let mut next = || lines.next().unwrap_or_default();
    let (name, uid, home, machine, wsl_distro) = (next(), next(), next(), next(), next());
    if name != user || wsl_distro != distro {
        return Err(Error::unavailable(format!(
            "pluxy expected WSL {distro}/{user}, this is {wsl_distro}/{name}"
        )));
    }
    if !home.starts_with('/') {
        return Err(Error::unavailable(format!(
            "{user} has no home folder in {distro}"
        )));
    }
    let uid: u64 = uid
        .parse()
        .map_err(|_| Error::unavailable(format!("{user}'s user number is unreadable")))?;
    let rest: Vec<&str> = lines.collect();
    let (drives, automount) = drives(&rest.join("\n"));
    Ok(
        json!({"distro": distro, "user": user, "uid": uid, "home": home, "arch": arch(machine)?,
        "automount": automount, "drives": drives}),
    )
}

/// The error for a run in distro (as user, when one was named) that did not succeed: WSL's own
/// failure (exit -1, its text) or the script's (any other code).
pub fn failure(code: i32, text: &str, distro: &str, user: Option<&str>) -> Error {
    if code == -1 && (text.contains("WSL_E_DISTRO_NOT_FOUND") || text.contains("no distribution")) {
        return Error::not_found(format!("there is no WSL distribution named {distro}"));
    }
    if code == -1 {
        if let Some(u) = user {
            // WSL 2 says "User not found."; WSL 1 gives a connection error instead.
            let missing = if text.contains("User not found") {
                ""
            } else {
                " (or it could not be started)"
            };
            return Error::not_found(format!(
                "the user {u} was not found in WSL {distro}{missing}: {text}"
            ));
        }
        return Error::unavailable(format!("WSL could not start {distro}: {text}"));
    }
    Error::unavailable(format!("reading WSL {distro} failed (exit {code}): {text}"))
}

#[cfg(test)]
#[path = "distro_tests.rs"]
mod tests;
