use pluxy_program::{run, Call, Error, Handler};
use pluxy_usage::{produce, Parser, Sink, Tokens};
use serde_json::Value;
use std::collections::HashSet;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

struct Fixture;

impl Parser for Fixture {
    fn label(&self) -> &str {
        "Fixture"
    }

    fn default_roots(&self, home: &Path) -> Vec<PathBuf> {
        vec![home.join(".usage-fixture")]
    }

    fn wanted(&self, path: &Path) -> bool {
        path.extension().is_some_and(|e| e == "jsonl")
    }

    fn parse(&self, bytes: &[u8], sink: &mut Sink) -> Result<(), Error> {
        let mut ids = HashSet::new();
        for line in bytes.split(|b| *b == b'\n') {
            let Ok(v) = serde_json::from_slice::<Value>(line) else {
                continue;
            };
            if v["part"] == true {
                sink.end_part()?;
                continue;
            }
            if let Some(ms) = v["pause"].as_u64() {
                sink.end_part()?;
                let until = Instant::now() + Duration::from_millis(ms);
                while Instant::now() < until {
                    if sink.cancelled() {
                        return Err(Error::cancelled());
                    }
                    std::thread::sleep(Duration::from_millis(20));
                }
                continue;
            }
            let (Some(ts), Some(model)) = (v["ts"].as_i64(), v["model"].as_str()) else {
                continue;
            };
            if let Some(id) = v["id"].as_str() {
                if !ids.insert(id.to_string()) {
                    continue;
                }
            }
            let n = |k: &str| v[k].as_u64().unwrap_or(0);
            let mut t = Tokens {
                input: n("input"),
                output: n("output"),
                cache_read: n("cache_read"),
                cache_write: n("cache_write"),
                reasoning: n("reasoning"),
                total: 0,
                samples: 1,
            };
            t.total = v["total"].as_u64().unwrap_or(
                t.input
                    .saturating_add(t.output)
                    .saturating_add(t.cache_read)
                    .saturating_add(t.cache_write),
            );
            sink.row(ts, model, &t)?;
        }
        Ok(())
    }
}

struct Tool;

impl Handler for Tool {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        match call.op.as_str() {
            "scan" => produce(call, &Fixture),
            "relay" => {
                let i = &call.input;
                let answer = call.call(
                    i["package"].as_str().unwrap_or_default(),
                    i["op"].as_str().unwrap_or_default(),
                    i["input"].clone(),
                );
                Ok(match answer {
                    Ok(output) => serde_json::json!({"output": output}),
                    Err(e) => serde_json::json!({"error": {"code": e.code, "message": e.message}}),
                })
            }
            op => Err(Error::unsupported(format!("no operation {op}"))),
        }
    }
}

fn main() -> std::io::Result<()> {
    run(Tool)
}
