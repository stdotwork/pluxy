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
//! Linux only for now: its terminals and openers are Linux's, and on any other system every
//! operation is refused (its manifest names no program for one, so the app does not run it there).

#[cfg(target_os = "linux")]
mod recipe;

use pluxy_program::{run, Call, Error, Handler};
use serde_json::Value;

struct Execution;

impl Handler for Execution {
    #[cfg(not(target_os = "linux"))]
    fn call(&self, _call: &Call) -> Result<Value, Error> {
        Err(Error::unsupported(
            "pluxy/execution runs on Linux only for now",
        ))
    }

    #[cfg(target_os = "linux")]
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
            "launch" => call.spawn(),
            "runtime" => runtime(call, &["status", "list"]),
            "control" => runtime(call, &["stop", "restart"]),
            op => Err(Error::unsupported(format!("no operation {op}"))),
        }
    }
}

/// Whether a call with chain came through another package: its chain is not the app's window
/// alone.
#[cfg(target_os = "linux")]
fn imported(chain: &[Value]) -> bool {
    !(chain.len() == 1 && chain[0]["kind"] == "main")
}

/// A runtime request of one of ops for the call's input, refused for any other op: a read call
/// never stops or restarts a runtime, whatever its input says.
#[cfg(target_os = "linux")]
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

fn main() -> std::io::Result<()> {
    run(Execution)
}
