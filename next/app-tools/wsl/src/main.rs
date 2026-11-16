//! pluxy/wsl: the WSL distributions on this Windows computer and their users, for pluxy's desktop
//! app, which sets clients up and starts them inside the one the person picks. Its operations, all
//! reads (README.md): `distros` (from `wsl.exe --list --quiet` and the registry; no distribution is
//! started), `users` (starts the distribution and reads its accounts as its default user), `check`
//! (as the selected user: their home, the processor and the Windows drives mounted) and `prepare`
//! (finds a program and folder inside the distribution as the user, and answers the exact
//! `wsl.exe` launch, which it never starts). It writes nothing anywhere and runs only Windows'
//! own `wsl.exe`, each time with one fixed script; names reach the scripts only as arguments.
//! On other systems every operation is refused.

#[cfg_attr(not(windows), allow(dead_code))]
mod distro;
#[cfg(windows)]
mod exec;
#[cfg_attr(not(windows), allow(dead_code))]
mod list;
#[cfg_attr(not(windows), allow(dead_code))]
mod prepare;

use pluxy_program::{run, Call, Error, Handler};
use serde_json::Value;

struct Wsl;

/// The input's distribution: a name this program accepts, not one of Docker's own.
fn distro_of(input: &Value) -> Result<&str, Error> {
    let d = input["distro"].as_str().unwrap_or_default();
    if !list::distro_ok(d) {
        return Err(Error::invalid_request(format!(
            "{d:?} is not a WSL distribution name pluxy accepts"
        )));
    }
    if list::HIDDEN.contains(&d) {
        return Err(Error::invalid_request(format!(
            "{d} is Docker's own distribution"
        )));
    }
    Ok(d)
}

/// The input's user: a POSIX portable user name.
fn user_of(input: &Value) -> Result<&str, Error> {
    let u = input["user"].as_str().unwrap_or_default();
    if !list::user_ok(u) {
        return Err(Error::invalid_request(format!(
            "{u:?} is not a user name pluxy accepts"
        )));
    }
    Ok(u)
}

impl Handler for Wsl {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        let input = &call.input;
        match call.op.as_str() {
            "distros" => os::distros(call),
            "users" => os::users(call, distro_of(input)?),
            "check" => os::check(call, distro_of(input)?, user_of(input)?),
            "prepare" => {
                let (d, u) = (distro_of(input)?, user_of(input)?);
                os::prepare(call, d, u, &prepare::Request::from_input(input)?)
            }
            op => Err(Error::unsupported(format!("no operation {op}"))),
        }
    }
}

#[cfg(windows)]
mod os {
    use super::{distro, exec, list, prepare};
    use pluxy_program::{Call, Error};
    use serde_json::{json, Value};
    use std::time::Duration;

    /// `wsl.exe --list` gets this long: the first call may start the WSL service.
    const LIST_TIME: Duration = Duration::from_secs(10);
    /// A run inside a distribution gets this long: a stopped WSL 2 distribution starts first.
    const RUN_TIME: Duration = Duration::from_secs(60);

    fn args(a: &[&str]) -> Vec<String> {
        a.iter().map(|s| s.to_string()).collect()
    }

    /// A distribution's WSL version, from the registry (display only).
    fn version(distro: &str) -> Value {
        exec::registry()
            .0
            .into_iter()
            .find(|(n, _)| n == distro)
            .and_then(|(_, v)| v)
            .map_or(Value::Null, |v| json!(v))
    }

    /// Runs script inside distro (as user, when named): what it printed, or why it failed.
    fn inside(
        call: &Call,
        distro: &str,
        user: Option<&str>,
        script: &[&str],
        limit: usize,
    ) -> Result<Vec<u8>, Error> {
        let mut a = args(&["-d", distro]);
        if let Some(u) = user {
            a.extend(args(&["-u", u]));
        }
        a.extend(args(&["--exec", "/bin/sh", "-c"]));
        a.extend(args(script));
        let out = exec::wsl(&a, limit, RUN_TIME, &|| call.cancelled())?;
        if out.code != 0 {
            return Err(distro::failure(out.code, &out.text(), distro, user));
        }
        Ok(out.stdout)
    }

    pub fn distros(call: &Call) -> Result<Value, Error> {
        if exec::wsl_exe().is_none() {
            return Ok(json!({"distros": [], "installed": false}));
        }
        let out = exec::wsl(&args(&["--list", "--quiet"]), 64 << 10, LIST_TIME, &|| {
            call.cancelled()
        })?;
        let Some(names) = list::listing(out.code, &out.stdout, &out.stderr)? else {
            return Ok(json!({"distros": [], "installed": false}));
        };
        let (known, default) = exec::registry();
        let distros: Vec<Value> = names
            .iter()
            .map(|n| {
                let v = known.iter().find(|(k, _)| k == n).and_then(|(_, v)| *v);
                json!({"name": n, "default": default.as_deref() == Some(n.as_str()), "version": v})
            })
            .collect();
        Ok(json!({"distros": distros, "installed": true}))
    }

    pub fn users(call: &Call, d: &str) -> Result<Value, Error> {
        let out = inside(call, d, None, &[distro::USERS], 256 << 10)?;
        let mut v = distro::users(&String::from_utf8_lossy(&out))?;
        v["wsl"] = version(d);
        Ok(v)
    }

    pub fn check(call: &Call, d: &str, u: &str) -> Result<Value, Error> {
        let out = inside(call, d, Some(u), &[distro::CHECK], 256 << 10)?;
        let mut v = distro::check(&String::from_utf8_lossy(&out), d, u)?;
        v["wsl"] = version(d);
        Ok(v)
    }

    pub fn prepare(call: &Call, d: &str, u: &str, r: &prepare::Request) -> Result<Value, Error> {
        let exe = exec::wsl_exe().ok_or_else(|| Error::unavailable("WSL is not installed"))?;
        let exe = exe
            .to_str()
            .ok_or_else(|| Error::unavailable("wsl.exe's path is not Unicode"))?
            .to_string();
        // wsl.exe starts in the person's home folder on Windows, which the app gives as HOME.
        let home = std::env::var("HOME")
            .ok()
            .filter(|h| std::path::Path::new(h).is_absolute() && std::path::Path::new(h).is_dir())
            .ok_or_else(|| Error::unavailable("the app gave no home folder (HOME)"))?;
        let a = r.resolve_args(d, u);
        let out = exec::wsl(&a, 64 << 10, RUN_TIME, &|| call.cancelled())?;
        if out.code != 0 {
            return Err(distro::failure(out.code, &out.text(), d, Some(u)));
        }
        r.launch(&out.stdout, d, u, &exe, &home)
    }
}

#[cfg(not(windows))]
mod os {
    use super::prepare::Request;
    use pluxy_program::{Call, Error};
    use serde_json::Value;

    fn refused() -> Result<Value, Error> {
        Err(Error::unsupported("WSL is on Windows only"))
    }

    pub fn distros(_: &Call) -> Result<Value, Error> {
        refused()
    }
    pub fn users(_: &Call, _: &str) -> Result<Value, Error> {
        refused()
    }
    pub fn check(_: &Call, _: &str, _: &str) -> Result<Value, Error> {
        refused()
    }
    pub fn prepare(_: &Call, _: &str, _: &str, _: &Request) -> Result<Value, Error> {
        refused()
    }
}

#[cfg(test)]
#[path = "main_tests.rs"]
mod main_tests;

fn main() -> std::io::Result<()> {
    #[cfg(windows)]
    exec::keep_own_handles();
    run(Wsl)
}
