//! pluxy/execution: prepares terminal, direct and open launches for pluxy's desktop app, which
//! spawns them (README.md). Its operations: `terminals` and `prepare` (read: no process is started),
//! `launch` (launch: the host confirms the exact executable, argv, cwd, program, lifetime and
//! environment, then spawns it when asked; this program holds secret references only, never a
//! secret's value; `managed` applies to direct launches only: the host refuses a managed
//! terminal launch, since many terminals hand their session to a server process and exit),
//! `runtime` (read: its package's managed runtimes, through the host) and
//! `control` (write: stops or restarts one of them). Only `prepare` and `launch` are exported:
//! another tool prepares and launches, such as opening one of its files in the person's own app.
//!
//! On Windows and macOS this program is also the terminal and opener of the launches it prepares:
//! run by the app with arguments (as a confirmed launch, never as a tool, which gets none), it
//! runs a launch mode (`launch_windows.rs`, `launch_macos.rs`) instead of serving operations.

#[cfg(target_os = "macos")]
mod checked;
#[cfg(all(unix, any(target_os = "macos", test)))]
mod handover;
#[cfg(target_os = "macos")]
#[path = "launch_macos.rs"]
mod launch;
#[cfg(windows)]
#[path = "launch_windows.rs"]
mod launch;
mod recipe;
#[cfg_attr(not(windows), allow(dead_code))]
mod shim;
#[cfg_attr(not(windows), allow(dead_code))]
mod winpath;

use pluxy_program::{run, Call, Error, Handler};
use serde_json::Value;

struct Execution;

impl Handler for Execution {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        match call.op.as_str() {
            "terminals" => Ok(recipe::terminals(&call.input)),
            // Another package (a tool's import, or a page's) prepares only open launches: a
            // terminal or direct preparation answers whether programs and folders exist.
            "prepare" if imported(&call.chain) && call.input["kind"] != "open" => Err(Error::new(
                "permission",
                "another package prepares only open launches",
            )),
            "prepare" => recipe::prepare(&call.input),
            // The host spawns exactly the launch it confirmed for this call; nothing of it is
            // named here.
            "launch" => {
                launch_fits(&call.input)?;
                call.spawn()
            }
            "runtime" => runtime(call, &["status", "list"]),
            "control" => runtime(call, &["stop", "restart"]),
            op => Err(Error::unsupported(format!("no operation {op}"))),
        }
    }
}

/// What launch input is on this system: whether it is a terminal launch made by this program, and
/// whether it is an app launch (Windows' `app` mode, macOS' `/usr/bin/open -a <bundle>`). Linux
/// has neither: its terminals are the person's own programs.
#[cfg(not(target_os = "linux"))]
fn shape(input: &Value) -> (bool, bool) {
    let argv = input["argv"]
        .as_array()
        .map(Vec::as_slice)
        .unwrap_or_default();
    let executable = input["executable"].as_str().unwrap_or_default();
    let ours = || recipe::os::this_program().is_ok_and(|p| p == executable);
    let mode = argv.first().and_then(Value::as_str);
    let terminal = mode == Some("terminal") && ours();
    #[cfg(windows)]
    let app = mode == Some("app") && ours();
    #[cfg(target_os = "macos")]
    let app = recipe::os::is_app_launch(executable, argv);
    (terminal, app)
}

#[cfg(target_os = "linux")]
fn shape(_: &Value) -> (bool, bool) {
    (false, false)
}

/// Refused before the host is asked, on Windows and macOS: a terminal launch with standard input
/// (the window's input is the keyboard), and an app launch with anything but the app (no
/// environment, input, secret or managed lifetime: the system starts the app with its own).
fn launch_fits(input: &Value) -> Result<(), Error> {
    let (terminal, app) = shape(input);
    fits(input, terminal, app)?;
    #[cfg(not(windows))]
    program_fits(input, program_at(input, terminal)?)?;
    Ok(())
}

/// Where a terminal launch's program must be in its argv, the argument its terminal runs, or None
/// for a launch that is not a terminal's: on Linux right after the exec flag of a terminal of
/// `recipe::TERMINALS`, when argv starts with that flag (a launch of such a terminal whose argv
/// does not, and that names no program, runs no command of the launch's: a direct launch of the
/// terminal); on macOS right after `--`. The host checks the program a launch names as the file
/// its terminal runs.
#[cfg(target_os = "linux")]
fn program_at(input: &Value, _: bool) -> Result<Option<usize>, Error> {
    let executable = std::path::Path::new(input["executable"].as_str().unwrap_or_default());
    let Some(t) = recipe::TERMINALS
        .iter()
        .find(|t| executable.file_name().is_some_and(|n| n == t.id))
    else {
        return Ok(None);
    };
    let argv = input["argv"]
        .as_array()
        .map(Vec::as_slice)
        .unwrap_or_default();
    if argv.len() < t.flags.len() || argv.iter().zip(t.flags).any(|(a, f)| a != f) {
        if input.get("program").is_none() {
            return Ok(None);
        }
        return Err(Error::invalid_request(format!(
            "a launch of {} runs its program after {}",
            t.id,
            t.flags.join(" ")
        )));
    }
    Ok(Some(t.flags.len()))
}

#[cfg(target_os = "macos")]
fn program_at(input: &Value, terminal: bool) -> Result<Option<usize>, Error> {
    if !terminal {
        return Ok(None);
    }
    let argv = input["argv"]
        .as_array()
        .map(Vec::as_slice)
        .unwrap_or_default();
    match argv.iter().position(|a| a == "--") {
        Some(i) => Ok(Some(i + 1)),
        None => Err(Error::invalid_request(
            "a terminal launch runs its program after --",
        )),
    }
}

/// Refused unless a terminal launch (at: where its program must be) names as its program the
/// argument at at, and a launch that is not a terminal's (None) names no program.
#[cfg(not(windows))]
fn program_fits(input: &Value, at: Option<usize>) -> Result<(), Error> {
    let program = input.get("program");
    match at {
        Some(i) => {
            let named = program.and_then(Value::as_str);
            if named.is_none() || input["argv"].get(i).and_then(Value::as_str) != named {
                return Err(Error::invalid_request(
                    "a terminal launch names as its program the argument its terminal runs",
                ));
            }
            Ok(())
        }
        None if program.is_some() => Err(Error::invalid_request(
            "only a terminal launch names a program: the one its terminal runs",
        )),
        None => Ok(()),
    }
}

fn fits(input: &Value, terminal: bool, app: bool) -> Result<(), Error> {
    if terminal && input.get("stdin").is_some() {
        return Err(Error::invalid_request(
            "a terminal launch takes no standard input: its input is the keyboard",
        ));
    }
    // An environment that sets, unsets, inherits and gives nothing.
    let empty = |v: &Value| {
        v.as_object().is_some_and(|o| o.is_empty()) || v.as_array().is_some_and(|a| a.is_empty())
    };
    let no_environment = input["environment"]
        .as_object()
        .is_none_or(|e| e.values().all(empty));
    let bare = no_environment
        && input.get("stdin").is_none()
        && input.get("key").is_none()
        && input["lifetime"] != "managed";
    if app && !bare {
        return Err(Error::invalid_request(
            "an app launch takes no environment, standard input, secret or managed lifetime: the \
             system starts the app with its own",
        ));
    }
    Ok(())
}

/// Whether a call with chain came through another package: its chain is not the app's window
/// alone.
fn imported(chain: &[Value]) -> bool {
    !(chain.len() == 1 && chain[0]["kind"] == "main")
}

/// A runtime request of one of ops for the call's input, refused for any other op: a read call
/// never stops or restarts a runtime, whatever its input says.
fn runtime(call: &Call, ops: &[&str]) -> Result<Value, Error> {
    let op = call.input["op"].as_str().unwrap_or_default();
    if !ops.contains(&op) {
        return Err(Error::invalid_request(format!(
            "{} takes op {}, not {op:?}",
            call.op,
            ops.join(" or ")
        )));
    }
    call.runtime(op, call.input["runtime"].as_str())
}

#[cfg(test)]
#[path = "manifest_tests.rs"]
mod manifest_tests;

#[cfg(test)]
#[path = "platform_tests.rs"]
mod platform_tests;

#[cfg(test)]
#[path = "fits_tests.rs"]
mod fits_tests;

fn main() -> std::io::Result<()> {
    // A launch mode: the app runs this program as a confirmed launch with arguments; as a tool it
    // gets none.
    #[cfg(not(target_os = "linux"))]
    {
        let args: Vec<String> = match std::env::args_os()
            .skip(1)
            .map(|a| a.into_string())
            .collect()
        {
            Ok(a) => a,
            Err(_) => {
                eprintln!("pluxy: an argument is not UTF-8");
                std::process::exit(2);
            }
        };
        if !args.is_empty() {
            std::process::exit(launch::main(&args));
        }
    }
    run(Execution)
}
