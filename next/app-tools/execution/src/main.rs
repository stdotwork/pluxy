//! pluxy/execution: prepares terminal and direct launches for pluxy's desktop app, which spawns
//! them (README.md). Its operations: `terminals` and `prepare` (read: no process is started),
//! `launch` (launch: the host confirms the exact executable, argv, cwd, program, lifetime and
//! environment, then spawns it when asked; this program holds secret references only, never a
//! secret's value), `runtime` (read: its package's managed runtimes, through the host) and
//! `control` (write: stops or restarts one of them). Only `launch` is exported.

mod recipe;

use pluxy_program::{run, Call, Error, Handler};
use serde_json::Value;

struct Execution;

impl Handler for Execution {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        match call.op.as_str() {
            "terminals" => Ok(recipe::terminals(&call.input)),
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

fn main() -> std::io::Result<()> {
    run(Execution)
}
