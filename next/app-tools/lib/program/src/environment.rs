//! The environment the host started a program in, `PLUXY_ENVIRONMENT`: unset for this computer,
//! `wsl:<distribution>:<user>` for a WSL distribution and user (the host runs the package's Linux
//! program there through `wsl.exe`). Before it says hello, a program in WSL checks that it runs in
//! that distribution (`WSL_DISTRO_NAME`, which WSL sets) as that user (its effective user ID's name
//! in `/etc/passwd`), so a stale record (a distribution or user made again under the name) or a
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

/// Whether the distribution and user this runs as (`now`: `WSL_DISTRO_NAME`, None outside WSL,
/// and the user's name) are the ones `PLUXY_ENVIRONMENT` names.
pub fn compare(environment: &str, now: (Option<&str>, &str)) -> Result<(), String> {
    let (distro, user) = parse(environment)?;
    if now.0 == Some(distro) && now.1 == user {
        return Ok(());
    }
    Err(format!(
        "pluxy expected WSL {distro}/{user}, this is {}/{}",
        now.0.unwrap_or("(not WSL)"),
        now.1
    ))
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

/// The name of the first account with user ID uid in `/etc/passwd` text.
pub fn user_named(passwd: &str, uid: u32) -> Option<&str> {
    passwd.lines().find_map(|l| {
        let mut f = l.split(':');
        let name = f.next()?;
        let id = f.nth(1)?;
        (id.parse() == Ok(uid) && !name.is_empty()).then_some(name)
    })
}

/// This process's distribution (None outside WSL) and user name, read now.
#[cfg(target_os = "linux")]
pub fn here() -> Result<(Option<String>, String), String> {
    let status = std::fs::read_to_string("/proc/self/status")
        .map_err(|e| format!("/proc/self/status: {e}"))?;
    let uid = effective_uid(&status).ok_or("/proc/self/status names no user ID")?;
    let passwd = std::fs::read_to_string("/etc/passwd").map_err(|e| format!("/etc/passwd: {e}"))?;
    let user = user_named(&passwd, uid)
        .map(String::from)
        .unwrap_or_else(|| format!("(user ID {uid})"));
    let distro = std::env::var("WSL_DISTRO_NAME")
        .ok()
        .filter(|d| !d.is_empty());
    Ok((distro, user))
}

/// Checks that this program runs where environment says, as [`compare`] does with [`here`]. Only
/// a Linux program runs in WSL: anywhere else, environment is refused.
pub fn check(environment: &str) -> Result<(), String> {
    #[cfg(target_os = "linux")]
    {
        let (distro, user) = here()?;
        compare(environment, (distro.as_deref(), &user))
    }
    #[cfg(not(target_os = "linux"))]
    {
        parse(environment)?;
        Err(format!(
            "pluxy started this program for {environment}, which only its Linux build runs in"
        ))
    }
}
