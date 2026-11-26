//! The environment the host started a program in, `PLUXY_ENVIRONMENT`: unset for this computer,
//! `wsl:<distribution>:<user>` for a WSL distribution and user (the host runs the package's Linux
//! program there through `wsl.exe`), with `PLUXY_ENVIRONMENT_ACCOUNT`, `<user ID>:<home>`, the
//! user's number and home as the host's record has them. Before it says hello, a program in WSL
//! checks that it runs in that distribution (`WSL_DISTRO_NAME`, which WSL sets) as that user (its
//! effective user ID's name in `/etc/passwd`), with that number and that home (the same entry's),
//! so a stale record (a distribution or user made again under the name, or a home moved) or a
//! routing mistake stops it before it does anything.

/// The distribution and user `PLUXY_ENVIRONMENT` names.
pub fn parse(environment: &str) -> Result<(&str, &str), String> {
    let mut parts = environment.splitn(3, ':');
    match (parts.next(), parts.next(), parts.next()) {
        // A distribution name holds no `:` and a user name none either (the host checks both).
        (Some("wsl"), Some(d), Some(u)) if !d.is_empty() && !u.is_empty() && !u.contains(':') => {
            Ok((d, u))
        }
        _ => Err(format!(
            "pluxy's app started this program in an environment it does not know: {environment}"
        )),
    }
}

/// The user ID and home `PLUXY_ENVIRONMENT_ACCOUNT` names: `<user ID>:<home>`, the home absolute.
pub fn parse_account(account: &str) -> Result<(u32, &str), String> {
    match account.split_once(':') {
        Some((uid, home))
            if !uid.is_empty()
                && uid.bytes().all(|b| b.is_ascii_digit())
                && home.starts_with('/') =>
        {
            if let Ok(uid) = uid.parse() {
                return Ok((uid, home));
            }
        }
        _ => {}
    }
    Err(format!(
        "pluxy's app started this program for an account it does not know: {account}"
    ))
}

/// Where this program runs: its distribution (None outside WSL), and its effective user's name,
/// number and home.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Here {
    pub distro: Option<String>,
    pub user: String,
    pub uid: u32,
    pub home: String,
}

/// Whether this runs (`now`) in the distribution, as the user, with the user ID and home that
/// environment and account name.
pub fn compare(environment: &str, account: &str, now: &Here) -> Result<(), String> {
    let (distro, user) = parse(environment)?;
    let (uid, home) = parse_account(account)?;
    if now.distro.as_deref() != Some(distro) || now.user != user {
        return Err(format!(
            "pluxy expected WSL {distro}/{user}, this is {}/{}",
            now.distro.as_deref().unwrap_or("(not WSL)"),
            now.user
        ));
    }
    if now.uid != uid || now.home != home {
        return Err(format!(
            "pluxy expected {user} with user ID {uid} and home {home}, this is user ID {} with \
             home {}",
            now.uid, now.home
        ));
    }
    Ok(())
}

/// The effective user ID in `/proc/self/status` text (its `Uid:` line: real, effective, ...).
pub fn effective_uid(status: &str) -> Option<u32> {
    status
        .lines()
        .find_map(|l| l.strip_prefix("Uid:"))?
        .split_whitespace()
        .nth(1)?
        .parse()
        .ok()
}

/// The name and home of the first account with user ID uid in `/etc/passwd` text.
pub fn account_of(passwd: &str, uid: u32) -> Option<(&str, &str)> {
    passwd.lines().find_map(|l| {
        let f: Vec<&str> = l.split(':').collect();
        let (name, id, home) = (*f.first()?, *f.get(2)?, *f.get(5)?);
        (id.parse() == Ok(uid) && !name.is_empty()).then_some((name, home))
    })
}

/// Where this process runs, read now.
#[cfg(target_os = "linux")]
pub fn here() -> Result<Here, String> {
    let status = std::fs::read_to_string("/proc/self/status")
        .map_err(|e| format!("/proc/self/status: {e}"))?;
    let uid = effective_uid(&status).ok_or("/proc/self/status names no user ID")?;
    let passwd = std::fs::read_to_string("/etc/passwd").map_err(|e| format!("/etc/passwd: {e}"))?;
    let (user, home) = match account_of(&passwd, uid) {
        Some((user, home)) => (user.to_string(), home.to_string()),
        None => (format!("(user ID {uid})"), "(none)".to_string()),
    };
    let distro = std::env::var("WSL_DISTRO_NAME")
        .ok()
        .filter(|d| !d.is_empty());
    Ok(Here {
        distro,
        user,
        uid,
        home,
    })
}

/// Checks that this program runs where environment and account say, as [`compare`] does with
/// [`here`]. Only a Linux program runs in WSL: anywhere else, environment is refused.
pub fn check(environment: &str, account: &str) -> Result<(), String> {
    #[cfg(target_os = "linux")]
    {
        compare(environment, account, &here()?)
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = account;
        parse(environment)?;
        Err(format!(
            "pluxy started this program for {environment}, which only its Linux build runs in"
        ))
    }
}
