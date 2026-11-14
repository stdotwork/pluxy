//! Scanning, locating and reading against real folders, and `produce` against a host played over
//! real pipes.

use super::*;
use pluxy_program::{read_frame, serve, write_frame, Description, Handler, Identity, MAX_INBOUND};
use std::io::{PipeReader, PipeWriter};
use std::sync::Arc;

/// A new empty folder of its own under the system's temporary folder.
fn folder(name: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!("pluxy-usage-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    d
}

/// Writes bytes to path (making its folders) with the modification time secs.
fn file(path: &Path, bytes: &str, secs: i64) {
    std::fs::create_dir_all(path.parent().unwrap()).unwrap();
    std::fs::write(path, bytes).unwrap();
    let t = std::time::UNIX_EPOCH + std::time::Duration::from_secs(secs as u64);
    std::fs::File::options()
        .write(true)
        .open(path)
        .unwrap()
        .set_modified(t)
        .unwrap();
}

fn log(p: &Path) -> bool {
    p.extension().is_some_and(|e| e == "log")
}

#[test]
fn a_scan_lists_wanted_files_newest_first_skipping_links_and_deep_files() {
    let d = folder("scan");
    let root = d.join("root");
    file(&root.join("a.log"), "a", 1_000);
    file(&root.join("x/b.log"), "bb", 3_000);
    file(&root.join("x/c.txt"), "c", 4_000);
    let deep: PathBuf = (0..MAX_DEPTH).fold(root.clone(), |p, i| p.join(format!("d{i}")));
    file(&deep.join("ok.log"), "ok", 2_000);
    file(&deep.join("too/deep.log"), "no", 5_000);
    file(&d.join("outside/o.log"), "o", 6_000);
    std::os::unix::fs::symlink(d.join("outside/o.log"), root.join("link.log")).unwrap();
    std::os::unix::fs::symlink(d.join("outside"), root.join("linked")).unwrap();
    let (found, truncated) = scan(std::slice::from_ref(&root), &log, &|| false).unwrap();
    let got: Vec<(PathBuf, u64, i64)> = found
        .into_iter()
        .map(|f| (f.path, f.size, f.mtime_ns))
        .collect();
    assert_eq!(
        got,
        vec![
            (root.join("x/b.log"), 2, 3_000_000_000_000),
            (deep.join("ok.log"), 2, 2_000_000_000_000),
            (root.join("a.log"), 1, 1_000_000_000_000),
        ]
    );
    assert!(!truncated);
    // A missing root lists nothing.
    assert!(scan(&[d.join("missing")], &log, &|| false)
        .unwrap()
        .0
        .is_empty());
    assert_eq!(scan(&[root], &log, &|| true).unwrap_err().code, "cancelled");
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn locate_finds_only_what_a_scan_would() {
    let d = folder("locate");
    let root = d.join("root");
    file(&root.join("x/b.log"), "bb", 3_000);
    file(&root.join("x/c.txt"), "c", 3_000);
    file(&d.join("outside/o.log"), "o", 3_000);
    std::os::unix::fs::symlink(d.join("outside"), root.join("linked")).unwrap();
    let roots = [root.clone()];
    let s = |p: &Path| p.to_str().unwrap().to_string();
    assert_eq!(
        locate(&roots, &s(&root.join("x/b.log")), &log),
        Some(root.join("x/b.log"))
    );
    for refused in [
        s(&root.join("x/c.txt")),
        s(&root.join("linked/o.log")),
        s(&d.join("outside/o.log")),
        format!("{}/x/../x/b.log", s(&root)),
        "x/b.log".to_string(),
        s(&root.join("x")),
        s(&root.join("missing.log")),
    ] {
        assert_eq!(locate(&roots, &refused, &log), None, "{refused}");
    }
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn read_file_reads_a_regular_file_up_to_the_limit() {
    let d = folder("read");
    file(&d.join("a.log"), "hello", 7);
    let (bytes, size, mtime) = read_file(&d.join("a.log")).unwrap();
    assert_eq!(
        (bytes.as_slice(), size, mtime),
        (&b"hello"[..], 5, 7_000_000_000)
    );
    let big = std::fs::File::create(d.join("big.log")).unwrap();
    big.set_len(MAX_DOCUMENT + 1).unwrap();
    assert!(read_file(&d.join("big.log"))
        .unwrap_err()
        .contains("larger"));
    std::os::unix::fs::symlink(d.join("a.log"), d.join("link.log")).unwrap();
    assert!(read_file(&d.join("link.log")).is_err());
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn rows_and_tokens_are_checked() {
    let row = json!({"hour": 7200, "model": "m", "input": 1, "output": 2, "cache_read": 3,
        "cache_write": 4, "reasoning": 5, "total": 10, "samples": 2});
    let r = Row::from_json(&row).unwrap();
    assert_eq!(
        (r.hour, r.model.as_str(), r.tokens.values()),
        (7200, "m", [1, 2, 3, 4, 5, 10, 2])
    );
    assert_eq!(r.to_json(), row);
    for (field, bad) in [
        ("hour", json!(7201)),
        ("hour", json!(-3600)),
        ("hour", json!(MAX_HOUR + 3600)),
        ("model", json!("m".repeat(MAX_MODEL + 1))),
        ("model", json!(null)),
        ("input", json!(-1)),
        ("total", json!(MAX_COUNT + 1)),
        ("samples", json!(1.5)),
        ("reasoning", json!(null)),
    ] {
        let mut v = row.clone();
        v[field] = bad.clone();
        assert!(Row::from_json(&v).is_err(), "{field} {bad}");
    }
    let mut t = Tokens::from_values([MAX_COUNT, 1, 0, 0, 0, 0, 0]);
    t.add(&Tokens::from_values([1, 1, 0, 0, 0, 0, 0]));
    assert_eq!(t.values(), [MAX_COUNT, 2, 0, 0, 0, 0, 0]);
}

/// A parser of `.log` files whose lines are `<unix seconds> <model> <input> <output>`; a line
/// `cut` ends a part, `fail` fails the file.
struct Lines;

impl Parser for Lines {
    fn label(&self) -> &str {
        "Lines"
    }
    fn default_roots(&self, home: &Path) -> Vec<PathBuf> {
        vec![home.join(".lines")]
    }
    fn wanted(&self, path: &Path) -> bool {
        log(path)
    }
    fn parse(&self, bytes: &[u8], sink: &mut Sink) -> Result<(), Error> {
        for line in std::str::from_utf8(bytes).unwrap_or_default().lines() {
            let f: Vec<&str> = line.split(' ').collect();
            match f.as_slice() {
                ["cut"] => sink.end_part()?,
                ["fail"] => return Err(Error::invalid_request("broken line")),
                [t, m, i, o] => {
                    let (i, o): (u64, u64) = (i.parse().unwrap(), o.parse().unwrap());
                    let tokens = Tokens {
                        input: i,
                        output: o,
                        total: i + o,
                        samples: 1,
                        ..Tokens::default()
                    };
                    sink.row(t.parse().unwrap(), m, &tokens)?;
                }
                _ => {}
            }
        }
        Ok(())
    }
}

/// A streamed parser of `.log` files: a row of the lines it was given (hour 3600, model `lines`,
/// every line one sample, its input the line's length) and one of the lines it skipped for being
/// too long (model `skipped`, input the count).
struct Streamed;

impl Parser for Streamed {
    fn label(&self) -> &str {
        "Streamed"
    }
    fn default_roots(&self, home: &Path) -> Vec<PathBuf> {
        vec![home.join(".streamed")]
    }
    fn wanted(&self, path: &Path) -> bool {
        log(path)
    }
    fn streamed(&self) -> bool {
        true
    }
    fn parse_lines(&self, lines: &mut LineReader, sink: &mut Sink) -> Result<(), Error> {
        let mut seen = Tokens::default();
        while let Some(line) = lines.next_line()? {
            seen.add(&Tokens {
                input: line.len() as u64,
                total: line.len() as u64,
                samples: 1,
                ..Tokens::default()
            });
        }
        sink.row(3600, "lines", &seen)?;
        let skipped = lines.skipped();
        let skipped = Tokens {
            input: skipped,
            total: skipped,
            samples: skipped,
            ..Tokens::default()
        };
        sink.row(3600, "skipped", &skipped)
    }
}

/// A parser of files whose lines are parts listed apart: `<name>.parts` holds lines
/// `<part> <mtime_ns> <tokens>`, each an entry `<path below the root>#<part>` of its own revision
/// (its line's length and time), read by `read_entry`; a part named `long` gets an ID longer than
/// `MAX_PATH`, and a file whose first line is `fail` cannot list its parts.
struct Parts;

impl Parts {
    fn lines(path: &Path) -> Vec<(String, i64, u64, u64)> {
        std::fs::read_to_string(path)
            .unwrap_or_default()
            .lines()
            .filter_map(|l| {
                let f: Vec<&str> = l.split(' ').collect();
                Some((
                    f.first()?.to_string(),
                    f.get(1)?.parse().ok()?,
                    f.get(2)?.parse().ok()?,
                    l.len() as u64,
                ))
            })
            .collect()
    }
}

impl Parser for Parts {
    fn label(&self) -> &str {
        "Parts"
    }
    fn default_roots(&self, home: &Path) -> Vec<PathBuf> {
        vec![home.join(".parts")]
    }
    fn wanted(&self, path: &Path) -> bool {
        path.extension().is_some_and(|e| e == "parts")
    }
    fn file_id_under(&self, root: &Path, path: &Path) -> Option<String> {
        path.strip_prefix(root).ok()?.to_str().map(String::from)
    }
    fn entries(&self, root: &Path, found: &Found) -> Listing {
        if std::fs::read_to_string(&found.path).is_ok_and(|t| t.starts_with("fail")) {
            return Listing::Failed;
        }
        let id = self.file_id_under(root, &found.path).unwrap();
        Listing::Parts(
            Parts::lines(&found.path)
                .into_iter()
                .map(|(part, mtime_ns, _, size)| Entry {
                    id: if part == "long" {
                        format!("{id}#{}", "x".repeat(MAX_PATH))
                    } else {
                        format!("{id}#{part}")
                    },
                    size,
                    mtime_ns,
                })
                .collect(),
        )
    }
    fn read_entry(
        &self,
        roots: &[PathBuf],
        id: &str,
        sink: &mut Sink,
    ) -> Option<Result<(), Error>> {
        let (file, part) = id.split_once('#')?;
        let mut read = || {
            let path = roots
                .iter()
                .find_map(|r| self.locate(roots, r.join(file).to_str()?))
                .ok_or_else(|| Error::invalid_request("no such file"))?;
            let (_, mtime_ns, tokens, size) = Parts::lines(&path)
                .into_iter()
                .find(|l| l.0 == part)
                .ok_or_else(|| Error::invalid_request("no such part"))?;
            sink.revise(size, mtime_ns)?;
            let t = Tokens {
                input: tokens,
                total: tokens,
                samples: 1,
                ..Tokens::default()
            };
            sink.row(3600, "m", &t)?;
            sink.end_part()?;
            // Too late once a part was sent.
            assert_eq!(sink.revise(1, 1).unwrap_err().code, "internal");
            Ok(())
        };
        Some(read())
    }
}

struct Producer;
impl Handler for Producer {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        if call.input["streamed"] == true {
            return produce(call, &Streamed);
        }
        if call.input["parts"] == true {
            return produce(call, &Parts);
        }
        produce(call, &Lines)
    }
}

/// A host for Producer: sends one job call with input and acknowledges every batch; gives the
/// batches and the terminal frame.
fn job(input: Value) -> (Vec<Value>, Value) {
    let (in_r, mut in_w): (PipeReader, PipeWriter) = std::io::pipe().unwrap();
    let (mut out_r, out_w) = std::io::pipe().unwrap();
    let description = Description::from_manifest(
        br#"{"app": {"operations": [{"id": "scan", "description": "Scan"}],
            "state": {"version": 0, "readable_versions": [0]}}}"#,
    )
    .unwrap();
    let identity = Identity {
        id: "pluxy/lines".into(),
        sha256: "ab".repeat(32),
    };
    let served =
        std::thread::spawn(move || serve(in_r, out_w, &identity, description, Arc::new(Producer)));
    assert_eq!(read_frame(&mut out_r).unwrap().unwrap()["type"], "hello");
    let call = json!({"type": "call", "id": "c-1", "op": "scan", "input": input,
        "job": {"id": "j-1", "kind": KIND, "window": 4}});
    write_frame(&mut in_w, &call, MAX_INBOUND).unwrap();
    let mut batches = Vec::new();
    let end = loop {
        let f = read_frame(&mut out_r).unwrap().unwrap();
        if f["type"] != "progress" {
            break f;
        }
        let size = serde_json::to_vec(&f["value"]).unwrap().len();
        assert!(size <= pluxy_program::MAX_BATCH, "a batch of {size} bytes");
        batches.push(f["value"].clone());
        let ack = json!({"type": "ack", "id": "c-1", "seq": batches.len()});
        write_frame(&mut in_w, &ack, MAX_INBOUND).unwrap();
    };
    drop(in_w);
    served.join().unwrap().unwrap();
    (batches, end)
}

#[test]
fn a_files_job_lists_the_files_in_batches() {
    let d = folder("files");
    let root = d.join("root");
    // Enough files for more than one batch.
    for n in 0..300 {
        file(
            &root.join(format!("{:0>120}.log", n)),
            "x",
            1_000_000 + n as i64,
        );
    }
    let (batches, end) = job(json!({"mode": "files", "roots": [root]}));
    assert!(batches.len() > 1);
    let files: Vec<&Value> = batches
        .iter()
        .flat_map(|b| b["files"].as_array().unwrap())
        .collect();
    assert_eq!(files.len(), 300);
    // Newest first.
    assert_eq!(
        files[0],
        &json!({"file": root.join(format!("{:0>120}.log", 299)), "size": 1,
            "mtime_ns": 1_000_299_000_000_000i64})
    );
    assert_eq!(
        end["output"],
        json!({"label": "Lines", "roots": [root], "count": 300, "truncated": false})
    );
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_read_job_sends_each_files_rows_in_parts_and_errors_for_the_rest() {
    let d = folder("readjob");
    let root = d.join("root");
    let a = root.join("a.log");
    // Two models in hour 3600 and one in 7200, a part cut between, then the same hour again.
    file(
        &a,
        "3600 m1 1 2\n3700 m1 10 20\n3601 m2 5 5\ncut\n7300 m1 1 1\n3650 m1 100 0\n",
        50,
    );
    file(&root.join("broken.log"), "3600 m1 1 1\nfail\n", 60);
    file(&root.join("empty.log"), "", 70);
    let s = |p: &Path| p.to_str().unwrap().to_string();
    let ids = [
        s(&a),
        s(&root.join("broken.log")),
        s(&root.join("empty.log")),
        s(&d.join("elsewhere.log")),
        s(&a),
    ];
    let (batches, end) = job(json!({"mode": "read", "roots": [root], "files": ids}));
    let row = |hour: i64, model: &str, i: u64, o: u64, n: u64| {
        json!({"hour": hour, "model": model, "input": i, "output": o, "cache_read": 0,
            "cache_write": 0, "reasoning": 0, "total": i + o, "samples": n})
    };
    assert_eq!(
        batches,
        vec![
            json!({"file": s(&a), "size": 68, "mtime_ns": 50_000_000_000i64, "part": 0,
                "last": false, "rows": [row(3600, "m1", 11, 22, 2), row(3600, "m2", 5, 5, 1)]}),
            json!({"file": s(&a), "size": 68, "mtime_ns": 50_000_000_000i64, "part": 1,
                "last": true, "rows": [row(3600, "m1", 100, 0, 1), row(7200, "m1", 1, 1, 1)]}),
            json!({"file": s(&root.join("broken.log")), "error": "broken line"}),
            json!({"file": s(&root.join("empty.log")), "size": 0, "mtime_ns": 70_000_000_000i64,
                "part": 0, "last": true, "rows": []}),
            json!({"file": s(&d.join("elsewhere.log")), "error": "not one of this source's files"}),
        ]
    );
    assert_eq!(end["output"], json!({"read": 2, "failed": 2}));
    // Neither mode, or not a job: refused.
    let (b, end) = job(json!({"mode": "other"}));
    assert!(b.is_empty() && end["error"]["code"] == "invalid_request");
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_big_file_is_split_into_parts_within_the_batch_limit() {
    let d = folder("big");
    let root = d.join("root");
    let a = root.join("a.log");
    // 2,000 hours: far more rows than one batch holds.
    let lines: String = (0..2000)
        .map(|h| format!("{} model-{} 1 1\n", h * 3600, h % 3))
        .collect();
    file(&a, &lines, 50);
    let (batches, end) = job(json!({"mode": "read", "roots": [root], "files": [a]}));
    assert!(batches.len() > 2, "{} parts", batches.len());
    for (i, b) in batches.iter().enumerate() {
        assert_eq!(b["part"], i);
        assert_eq!(b["last"], i + 1 == batches.len());
    }
    let rows: usize = batches
        .iter()
        .map(|b| b["rows"].as_array().unwrap().len())
        .sum();
    assert_eq!(rows, 2000);
    assert_eq!(end["output"], json!({"read": 1, "failed": 0}));
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_line_reader_gives_lines_and_skips_and_counts_the_too_long() {
    let mut data = b"a\r\n\nbc\n".to_vec();
    data.extend(vec![b'x'; MAX_LINE as usize + 1]);
    data.extend(b"\nok\n");
    data.extend(vec![b'y'; MAX_LINE as usize]);
    data.push(b'\n');
    data.extend(vec![b'z'; MAX_LINE as usize + 5]);
    data.extend(b"\nlast");
    let mut lines = LineReader::new(&data[..]);
    let mut got = Vec::new();
    while let Some(l) = lines.next_line().unwrap() {
        got.push(l.len());
    }
    // The line of exactly MAX_LINE bytes is read, the two longer ones skipped; the last line has
    // no line end.
    assert_eq!(got, vec![2, 0, 2, 2, MAX_LINE as usize, 4]);
    assert_eq!(lines.skipped(), 2);
    // A file that ends inside a too-long line ends there.
    let data = vec![b'q'; MAX_LINE as usize + 10];
    let mut lines = LineReader::new(&data[..]);
    assert!(lines.next_line().unwrap().is_none());
    assert_eq!(lines.skipped(), 1);
}

#[test]
fn a_streamed_parser_reads_a_file_over_the_document_limit_and_counts_a_long_line() {
    use std::io::Write;
    let d = folder("streamed");
    let root = d.join("root");
    let a = root.join("a.log");
    std::fs::create_dir_all(&root).unwrap();
    // Over 16 MiB of small lines, with one over-long line between them.
    let small = "0123456789\n";
    let count = (MAX_DOCUMENT as usize / small.len()) + 1000;
    let mut f = std::io::BufWriter::new(std::fs::File::create(&a).unwrap());
    for _ in 0..count {
        f.write_all(small.as_bytes()).unwrap();
    }
    f.write_all(&vec![b'x'; MAX_LINE as usize + 1]).unwrap();
    f.write_all(b"\n").unwrap();
    f.write_all(small.as_bytes()).unwrap();
    f.flush().unwrap();
    drop(f);
    let size = std::fs::metadata(&a).unwrap().len();
    assert!(size > MAX_DOCUMENT);
    // Read whole it is refused; streamed it is read.
    assert!(read_file(&a).unwrap_err().contains("larger"));
    let (batches, end) =
        job(json!({"mode": "read", "roots": [root], "files": [a], "streamed": true}));
    let rows: Vec<&Value> = batches
        .iter()
        .flat_map(|b| b["rows"].as_array().unwrap())
        .collect();
    let n = count as u64 + 1;
    assert_eq!(
        rows.iter()
            .map(|r| (
                r["model"].as_str().unwrap(),
                r["input"].as_u64().unwrap(),
                r["samples"].as_u64().unwrap()
            ))
            .collect::<Vec<_>>(),
        // Lines of 10 bytes (the line end is not counted); one skipped.
        vec![("lines", n * 10, n), ("skipped", 1, 1)]
    );
    assert_eq!(batches.last().unwrap()["size"], size);
    assert_eq!(end["output"], json!({"read": 1, "failed": 0}));
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_parser_lists_and_reads_the_parts_of_a_file_as_entries_of_their_own() {
    let d = folder("parts");
    let root = d.join("root");
    let a = root.join("a.parts");
    file(&a, "x 300 5\ny 100 7\n", 50);
    file(&root.join("b.parts"), "z 200 9\n", 60);
    let s = |p: &Path, part: &str| {
        format!(
            "{}#{part}",
            p.strip_prefix(&root).unwrap().to_str().unwrap()
        )
    };
    // Every part of every file, newest first by the part's own time.
    let (batches, end) = job(json!({"mode": "files", "roots": [root], "parts": true}));
    assert_eq!(
        batches,
        vec![json!({"files": [
            {"file": s(&a, "x"), "size": 7, "mtime_ns": 300},
            {"file": s(&root.join("b.parts"), "z"), "size": 7, "mtime_ns": 200},
            {"file": s(&a, "y"), "size": 7, "mtime_ns": 100},
        ]})]
    );
    assert_eq!(end["output"]["count"], 3);
    // Each part read alone, at its own revision; a part or file that is not there fails alone.
    let row = |i: u64| {
        json!({"hour": 3600, "model": "m", "input": i, "output": 0, "cache_read": 0,
            "cache_write": 0, "reasoning": 0, "total": i, "samples": 1})
    };
    let ids = [s(&a, "y"), s(&a, "gone"), "elsewhere.parts#x".to_string()];
    let (batches, end) = job(json!({"mode": "read", "roots": [root], "files": ids, "parts": true}));
    assert_eq!(
        batches,
        vec![
            json!({"file": ids[0], "size": 7, "mtime_ns": 100, "part": 0, "last": false,
                "rows": [row(7)]}),
            json!({"file": ids[0], "size": 7, "mtime_ns": 100, "part": 1, "last": true,
                "rows": []}),
            json!({"file": ids[1], "error": "no such part"}),
            json!({"file": ids[2], "error": "no such file"}),
        ]
    );
    assert_eq!(end["output"], json!({"read": 1, "failed": 2}));
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn parts_listed_twice_are_listed_once_at_their_newest_revision_and_unlistable_ones_cut_the_listing()
{
    let d = folder("parts-twice");
    let (one, two) = (d.join("one"), d.join("two"));
    // a.parts under both roots: x is newer under two, y newer under one; z only under two.
    file(&one.join("a.parts"), "x 100 1\ny 400 1\n", 50);
    file(&two.join("a.parts"), "x 300 22\ny 200 1\nz 250 1\n", 60);
    let listed = |roots: Value| {
        let (batches, end) = job(json!({"mode": "files", "roots": roots, "parts": true}));
        let files: Vec<Value> = batches
            .iter()
            .flat_map(|b| b["files"].as_array().unwrap().clone())
            .collect();
        (files, end["output"].clone())
    };
    let (files, out) = listed(json!([one, two]));
    assert_eq!(
        files,
        vec![
            json!({"file": "a.parts#y", "size": 7, "mtime_ns": 400}),
            json!({"file": "a.parts#x", "size": 8, "mtime_ns": 300}),
            json!({"file": "a.parts#z", "size": 7, "mtime_ns": 250}),
        ]
    );
    assert_eq!(
        (&out["count"], &out["truncated"]),
        (&json!(3), &json!(false))
    );
    // A part whose ID is too long is left out, and the listing is cut short; the rest is listed.
    file(&one.join("b.parts"), "long 500 1\nw 500 1\n", 70);
    let (files, out) = listed(json!([one]));
    assert!(files
        .iter()
        .all(|f| f["file"].as_str().unwrap().len() <= MAX_PATH));
    assert!(files.contains(&json!({"file": "b.parts#w", "size": 7, "mtime_ns": 500})));
    assert_eq!(out["truncated"], true);
    // A file that cannot list its parts is listed as itself, and the listing is cut short.
    std::fs::remove_file(one.join("b.parts")).unwrap();
    file(&one.join("c.parts"), "fail\n", 80);
    let (files, out) = listed(json!([one]));
    assert!(files.contains(&json!({"file": "c.parts", "size": 5, "mtime_ns": 80_000_000_000i64})));
    assert_eq!(out["truncated"], true);
    std::fs::remove_dir_all(d).unwrap();
}
