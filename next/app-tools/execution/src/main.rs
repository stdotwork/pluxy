//! pluxy/execution: prepares terminal and direct launches for pluxy's desktop app, which spawns
//! them (README.md). Its operations: `terminals` and `prepare` (read: no process is started),
//! `launch` (launch: the host confirms the exact executable, argv, cwd, program, lifetime and
//! environment, then spawns it when asked; this program holds secret references only, never a
//! secret's value) and `runtime` (read: its package's managed runtimes, through the host).

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
            "runtime" => {
                let op = call.input["op"].as_str().unwrap_or_default();
                call.runtime(op, call.input["runtime"].as_str())
            }
            op => Err(Error::unsupported(format!("no operation {op}"))),
        }
    }
}

fn main() -> std::io::Result<()> {
    run(Execution)
}
