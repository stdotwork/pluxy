//! pluxy/usage-omp: the usage hub's parser of Oh My Pi's session files (`../README.md`). It reads the
//! session files under `~/.omp/agent/sessions` and `~/.omp/sessions` (the hub's `roots` name others),
//! read-only, and sends the hub only token counts per hour and model: never a prompt, a message, a
//! title, a folder or a key. The format and its rules are in `pluxy-pi-sessions`, which the Pi parser
//! shares.

use pluxy_pi_sessions::scan_call;
use pluxy_program::{run, Call, Error, Handler};
use serde_json::Value;

struct Tool;

impl Handler for Tool {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        match call.op.as_str() {
            "scan" => scan_call(call, "Oh My Pi", ".omp"),
            op => Err(Error::unsupported(format!("no operation {op}"))),
        }
    }
}

fn main() -> std::io::Result<()> {
    run(Tool)
}
