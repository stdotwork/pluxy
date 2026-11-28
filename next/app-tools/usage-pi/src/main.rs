use pluxy_pi_sessions::scan_call;
use pluxy_program::{run, Call, Error, Handler};
use serde_json::Value;

struct Tool;

impl Handler for Tool {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        match call.op.as_str() {
            "scan" => scan_call(call, "Pi", ".pi"),
            op => Err(Error::unsupported(format!("no operation {op}"))),
        }
    }
}

fn main() -> std::io::Result<()> {
    run(Tool)
}
