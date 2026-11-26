//! pluxy/fixture-writer: the transaction library's test fixture, never a curated tool. It writes
//! top-level values into JSON config files through `pluxy-transactions`, as a real writer would:
//! `preview` (read) pins the selected files or files in selected roots; `apply`, `clear` and
//! `restore` (write: each call asks the person) write them; `status` (read) shows the applied sets
//! and the latest recovery reports. `crash_after` makes `apply` exit after writing that many files,
//! for the native test's crash recovery.

use pluxy_program::{run, Call, Error, Handler};
use pluxy_transactions::{data_dir, Json, Pin, Selection, Transactions};
use serde_json::{json, Value};

struct Writer;

/// The input's `targets`, each read with f.
fn targets<T>(input: &Value, f: fn(&Value) -> Result<T, Error>) -> Result<Vec<T>, Error> {
    input["targets"]
        .as_array()
        .ok_or_else(|| Error::invalid_request("targets is a list"))?
        .iter()
        .map(f)
        .collect()
}

/// before as a JSON object (a missing file is an empty one) with values set at its top level.
fn with_values(
    before: Option<&[u8]>,
    values: &serde_json::Map<String, Value>,
) -> Result<Option<Vec<u8>>, Error> {
    let mut v = before
        .map(Json::parse)
        .transpose()?
        .unwrap_or_else(|| json!({}));
    let o = v
        .as_object_mut()
        .ok_or_else(|| Error::invalid_request("the file is not a JSON object"))?;
    for (k, val) in values {
        o.insert(k.clone(), val.clone());
    }
    Json::render(&v).map(Some)
}

impl Handler for Writer {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        let tx = Transactions::new(call, data_dir()?);
        let input = &call.input;
        let set = input["set"].as_str().unwrap_or_default();
        match call.op.as_str() {
            "preview" => tx.preview(set, &targets(input, Selection::from_json)?),
            "apply" => {
                let values = input["values"]
                    .as_object()
                    .ok_or_else(|| Error::invalid_request("values is an object"))?;
                let crash_after = input["crash_after"].as_u64();
                tx.apply(
                    set,
                    &targets(input, Pin::from_json)?,
                    &|_, _, before| with_values(before, values),
                    &Json,
                    &mut |i| {
                        if Some(i as u64 + 1) == crash_after {
                            std::process::exit(70);
                        }
                    },
                )
            }
            "clear" => tx.clear(set, &targets(input, Pin::from_json)?, &Json, &mut |_| {}),
            "restore" => tx.restore(set, &targets(input, Pin::from_json)?, &mut |_| {}),
            "status" => tx.status(),
            op => Err(Error::unsupported(format!("no operation {op}"))),
        }
    }

    fn reconcile(&self, call: &Call, _unknown: &[Value], journals: &[Value]) -> Result<(), Error> {
        Transactions::new(call, data_dir()?)
            .reconcile(journals)
            .map(|_| ())
    }
}

fn main() {
    if let Err(e) = run(Writer) {
        eprintln!("fixture-writer: {e}");
        std::process::exit(1);
    }
}
