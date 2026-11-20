//! The program host and the spawn service against Windows' own `wsl.exe` and a real WSL
//! distribution, on `windows-latest` (the WSL jobs of
//! `.github/workflows/next-app-tools-platforms.yml`, which set the distributions up and run these
//! with `--ignored`). Nothing here stands in for WSL: the tools run in the distribution through
//! the host's own `wsl.exe` command, and what they did is read back inside the distribution as
//! root.
//!
//! The job's distributions: `PLUXY_WSL_TEST_DISTRO` (default user `alice`, user `bob`), of WSL
//! version `PLUXY_WSL_TEST_VERSION`. `PLUXY_WSL_TEST_CLAUDE_CODE` is the Windows path of
//! pluxy/claude-code's Linux program (built for `x86_64-unknown-linux-musl`),
//! `PLUXY_WSL_TEST_CLAUDE_CODE_WINDOWS` that of its Windows program. Each test makes its own
//! package folders (in the temporary folder, on drive C, which the distribution mounts), its own
//! files in the distribution, and removes them when it ends.

use super::*;
use crate::environments::Record;
use crate::spawn::Request;
use crate::vault::Vault;
use std::path::Path;

const CLAUDE_CODE: &str = "pluxy/claude-code";
/// The values an apply writes, as the window gives them (`native/claude-code.ts`).
const BASE_URL: &str = "http://127.0.0.1:41999";
/// What a fixture settings file holds before anything is applied.
const ORIGINAL: &str = "{\"theme\": \"light\"}\n";

fn var(name: &str) -> String {
    std::env::var(name).unwrap_or_else(|_| panic!("{name} is not set: run from the WSL job"))
}

fn distro() -> String {
    var("PLUXY_WSL_TEST_DISTRO")
}

fn wsl_version() -> u8 {
    var("PLUXY_WSL_TEST_VERSION").parse().unwrap()
}

/// System32's `wsl.exe`, as the host names it.
fn wsl_exe() -> PathBuf {
    PathBuf::from(var("SystemRoot"))
        .join("System32")
        .join("wsl.exe")
}

/// Runs `wsl.exe` with args to its end: exit code and output. A launch WSL 1 refuses with
/// 0xd0000225 (nothing ran) is tried again, as pluxy/wsl's tests do.
fn wsl(args: &[&str]) -> (i32, String, String) {
    for attempt in 1..=5 {
        let out = std::process::Command::new(wsl_exe())
            .args(args)
            .env("WSL_UTF8", "1")
            .stdin(std::process::Stdio::null())
            .output()
            .unwrap();
        let (o, e) = (
            String::from_utf8_lossy(&out.stdout).into_owned(),
            String::from_utf8_lossy(&out.stderr).into_owned(),
        );
        let code = out.status.code().unwrap_or(-1);
        if code == -1 && (o.contains("0xd0000225") || e.contains("0xd0000225")) && attempt < 5 {
            eprintln!("retry: WSL refused a launch with 0xd0000225 ({attempt})");
            std::thread::sleep(Duration::from_secs(2));
            continue;
        }
        return (code, o, e);
    }
    unreachable!()
}

/// Runs a shell script in the test distribution as root (checks and fixtures; never a signal).
fn as_root(script: &str) -> String {
    let d = distro();
    let (code, out, err) = wsl(&["-d", &d, "-u", "root", "--exec", "/bin/sh", "-c", script]);
    assert_eq!(code, 0, "{script}: {out} {err}");
    out
}

/// The processes of the distribution whose command line holds marker (`pgrep -f`, run directly,
/// so no shell's own command line holds it): one `<pid> <command line>` line each.
fn processes(marker: &str) -> Vec<String> {
    let d = distro();
    let (code, out, err) = wsl(&[
        "-d", &d, "-u", "root", "--exec", "pgrep", "-a", "-f", "--", marker,
    ]);
    // 1: none matched.
    assert!(code == 0 || code == 1, "pgrep: {code} {out} {err}");
    out.lines().map(String::from).collect()
}

/// Whether no process holding marker runs within ten seconds.
fn none_left(marker: &str) -> bool {
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        if processes(marker).is_empty() {
            return true;
        }
        if Instant::now() >= deadline {
            return false;
        }
        std::thread::sleep(Duration::from_millis(500));
    }
}

/// `stat -c '%U %a %F'` of path in the distribution, as root.
fn stat(path: &str) -> String {
    as_root(&format!("stat -c '%U %a %F' '{path}'"))
        .trim()
        .to_string()
}

fn tag() -> String {
    let mut rnd = [0u8; 6];
    getrandom::fill(&mut rnd).unwrap();
    rnd.iter().map(|b| format!("{b:02x}")).collect()
}

/// The host's record of user in the distribution, as pluxy/wsl's `check` gives it: the user's
/// number and home read in the distribution now; drive C mounted at `/mnt/c`, where programs may
/// run (what `check` reads on these runners: pluxy/wsl's own real-WSL test).
fn record(user: &str, uid: u64, home: &str) -> Record {
    let d = distro();
    Record::from_check(
        &d,
        user,
        &json!({"distro": d, "user": user, "uid": uid, "home": home, "arch": "amd64",
            "wsl": wsl_version(), "automount": "/mnt/",
            "drives": [{"letter": "c", "path": "/mnt/c", "exec": true}]}),
        0,
    )
    .unwrap()
}

/// bob's user number in the distribution.
fn uid_of_bob() -> u64 {
    as_root("id -u bob").trim().parse().unwrap()
}

/// A release folder of pluxy/claude-code in root: its manifest (from the source tree) and
/// program, copied in as `bin/<file>`; the spec that runs it, with archive digest sha256.
fn release(root: &Path, name: &str, program: &Path, file: &str, sha256: &str) -> Spec {
    let manifest = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../app-tools/claude-code/pluxy-extension.json");
    let dir = root.join(name);
    std::fs::create_dir_all(dir.join("bin")).unwrap();
    std::fs::copy(&manifest, dir.join("pluxy-extension.json")).unwrap();
    std::fs::copy(program, dir.join("bin").join(file)).unwrap();
    // Its full path: the distribution reaches it by the drive's path, never a short (8.3) name.
    let dir = plain(std::fs::canonicalize(&dir).unwrap());
    let m: Value = serde_json::from_slice(&std::fs::read(&manifest).unwrap()).unwrap();
    let operations = m["app"]["operations"]
        .as_array()
        .unwrap()
        .iter()
        .map(|o| Operation {
            id: o["id"].as_str().unwrap().into(),
            input_schema: o["input_schema"].clone(),
            output_schema: o["output_schema"].clone(),
        })
        .collect();
    let program = dir.join("bin").join(file);
    Spec {
        id: CLAUDE_CODE.into(),
        sha256: sha256.into(),
        generation: 1,
        program_sha256: sha256_hex(&std::fs::read(&program).unwrap()),
        program,
        dir,
        operations,
        state_version: m["app"]["state"]["version"].as_i64().unwrap(),
        environment: Environment::Local,
        wsl: None,
    }
}

/// Spec, as its package's instance in record's distribution as its user (`Spec::in_wsl`).
fn in_wsl(spec: &Spec, record: &Record) -> Spec {
    Spec {
        environment: record.environment(),
        wsl: Some(record.clone()),
        ..spec.clone()
    }
}

/// A test's world: a settings file in bob's home in the distribution, claude-code's Linux and
/// Windows releases, a host that runs the Linux one as the WSL instances of `records` and the
/// Windows one on this computer, with the real vault. Dropped, it stops every tool and removes
/// what the test made, in the distribution and here.
struct World {
    tag: String,
    /// The fixture folder in bob's home, and its settings file.
    folder: String,
    file: String,
    root: PathBuf,
    vault: Arc<Vault>,
    host: Host,
    /// `/home/bob/.local/state/pluxy` (where claude-code's data folder goes) was there before the
    /// test: then it is not the test's to remove.
    state_was_there: bool,
    /// Users the test added to the distribution.
    users: Mutex<Vec<String>>,
}

impl World {
    /// With the WSL instances of records (bob's first); local is this computer's.
    fn new(records: Vec<Record>) -> World {
        let tag = tag();
        let folder = format!("/home/bob/pluxy-real-{tag}");
        let file = format!("{folder}/settings.json");
        as_root(&format!(
            "set -e; install -d -o bob -g bob -m 755 '{folder}'; printf '%s' '{ORIGINAL}' > '{file}'; \
             chown bob:bob '{file}'; chmod 644 '{file}'"
        ));
        let state_was_there =
            as_root("test -e /home/bob/.local/state/pluxy && echo yes || echo no").trim() == "yes";
        let root = std::env::temp_dir().join(format!("pluxy-wsl-real-{tag}"));
        std::fs::create_dir_all(&root).unwrap();
        let root = plain(std::fs::canonicalize(&root).unwrap());
        let linux = release(
            &root,
            "linux",
            Path::new(&var("PLUXY_WSL_TEST_CLAUDE_CODE")),
            "claude-code",
            &"c1".repeat(32),
        );
        let windows = release(
            &root,
            "windows",
            Path::new(&var("PLUXY_WSL_TEST_CLAUDE_CODE_WINDOWS")),
            "claude-code.exe",
            &"c2".repeat(32),
        );
        let host = Host::for_tests(
            Box::new(move |i: &Instance| match &i.environment {
                Environment::Local if i.package == CLAUDE_CODE => Ok(windows.clone()),
                Environment::Wsl { .. } if i.package == CLAUDE_CODE => records
                    .iter()
                    .find(|r| r.environment() == i.environment)
                    .map(|r| in_wsl(&linux, r))
                    .ok_or_else(|| format!("{} is not set up", i.name())),
                _ => Err(format!("{} is not installed on this device", i.name())),
            }),
            Limits::default(),
        );
        let vault = Arc::new(Vault::open(root.join("vault")));
        host.set_private(Arc::clone(&vault) as Arc<dyn Private>);
        // The local instance's home and data folders, as the app gives them.
        let (home, tools) = (root.join("home"), root.join("tools"));
        std::fs::create_dir_all(&home).unwrap();
        std::fs::create_dir_all(&tools).unwrap();
        host.set_tool_env(ToolEnv {
            home,
            tools,
            system: ToolEnv::system(),
        });
        World {
            tag,
            folder,
            file,
            root,
            vault,
            host,
            state_was_there,
            users: Mutex::default(),
        }
    }

    /// Calls op of instance with input, from the window, giving the call secrets as the router
    /// does (`Context::secrets`).
    fn call(
        &self,
        instance: &Instance,
        op: &str,
        input: Value,
        secrets: &[(&str, &str)],
    ) -> Result<Value, CallError> {
        let context = Context {
            secrets: secrets
                .iter()
                .map(|(r, v)| (r.to_string(), v.to_string()))
                .collect(),
            ..Context::main()
        };
        self.host.run_call_with(
            self.host.begin_call(None).unwrap(),
            instance,
            op,
            input,
            Arc::new(context),
            Cancel::default(),
        )
    }

    fn ok(&self, instance: &Instance, op: &str, input: Value) -> Value {
        self.call(instance, op, input.clone(), &[])
            .unwrap_or_else(|e| panic!("{} {op} {input}: {e}", instance.name()))
    }

    /// A preview of file through instance.
    fn preview(&self, instance: &Instance, file: &str) -> Value {
        self.ok(instance, "preview", json!({"targets": [{"file": file}]}))
    }

    /// Applies the window's values to file through instance, its key given as a call secret.
    fn apply(&self, instance: &Instance, file: &str, key: &str) -> Value {
        let mut input = json!({
            "base_url": BASE_URL,
            "opus": "claude-opus-5-5", "opus_1m": false,
            "sonnet": "claude-sonnet-5-5", "sonnet_1m": false,
            "haiku": "claude-haiku-5-5", "haiku_1m": false,
            "fable": "", "fable_1m": false,
            "startup_model": "opus", "subagent_model": "haiku",
            "max_context_tokens": 200000, "auto_compact_pct": 80,
            "disable_auto_compact": false,
        });
        let reference = format!("r-{}", "5e".repeat(16));
        input["targets"] = self.preview(instance, file)["targets"].clone();
        input["key"] = json!(reference);
        self.call(instance, "apply", input, &[(&reference, key)])
            .unwrap_or_else(|e| panic!("{} apply: {e}", instance.name()))
    }

    /// Clear or restore of file through instance, on a fresh preview's pins.
    fn undo(&self, instance: &Instance, op: &str, file: &str) -> Value {
        let targets = self.preview(instance, file)["targets"].clone();
        self.ok(instance, op, json!({"targets": targets}))
    }

    /// The names of the vault's package folders.
    fn vault_folders(&self) -> BTreeSet<String> {
        std::fs::read_dir(self.root.join("vault").join("packages"))
            .map(|d| {
                d.filter_map(|e| e.ok())
                    .map(|e| e.file_name().to_string_lossy().into_owned())
                    .collect()
            })
            .unwrap_or_default()
    }

    /// A user of the distribution, removed when the test ends, made by root's command line
    /// `useradd` with args and name.
    fn add_user(&self, name: &str, args: &str) {
        self.users.lock().unwrap().push(name.to_string());
        as_root(&format!("useradd {args} {name}"));
    }

    /// The mark of "now" in the distribution, for `changed_since`.
    fn mark(&self) -> String {
        let m = format!("/tmp/pluxy-real-{}.mark", self.tag);
        as_root(&format!("touch '{m}'"));
        // On a file system with coarse times, a change in the mark's own tick would not be newer.
        std::thread::sleep(Duration::from_millis(1100));
        m
    }
}

/// The files and folders under the distribution's homes (`/home`, `/root`) made, written or
/// changed since mark.
fn changed_since(mark: &str) -> String {
    as_root(&format!(
        "find /home /root -xdev \\( -newer '{mark}' -o -cnewer '{mark}' \\) -print"
    ))
}

impl Drop for World {
    fn drop(&mut self) {
        self.host.stop_all();
        let mut script = format!("rm -rf '{}' /tmp/pluxy-real-{}*", self.folder, self.tag);
        if !self.state_was_there {
            script.push_str("; rm -rf /home/bob/.local/state/pluxy");
        }
        for u in self.users.lock().unwrap_or_else(|e| e.into_inner()).iter() {
            script.push_str(&format!("; userdel {u}"));
        }
        let d = distro();
        let (code, out, err) = wsl(&["-d", &d, "-u", "root", "--exec", "/bin/sh", "-c", &script]);
        if code != 0 {
            eprintln!("cleaning up in {d} failed ({code}): {out} {err}");
        }
        let _ = std::fs::remove_dir_all(&self.root);
    }
}

/// The instance of claude-code that record names.
fn instance_of(record: &Record) -> Instance {
    Instance {
        package: CLAUDE_CODE.into(),
        environment: record.environment(),
    }
}

/// The vault folder name of claude-code's records on this computer: the ID's SHA-256 in hex.
fn local_folder() -> String {
    sha256_hex(CLAUDE_CODE.as_bytes())
}

/// The vault folder name of claude-code's records in WSL distro as user: the local one, `@` and
/// the first 32 hex digits of SHA-256 of `wsl\0<distro>\0<user>` (the vault's rule for an
/// instance in WSL).
fn wsl_folder(distro: &str, user: &str) -> String {
    let suffix = sha256_hex(format!("wsl\0{distro}\0{user}").as_bytes());
    format!("{}@{}", local_folder(), &suffix[..32])
}

#[test]
#[ignore = "needs a real WSL distribution (the WSL job)"]
fn wsl_real_claude_code_in_wsl_writes_as_bob_and_keeps_records_apart_from_the_local_instance() {
    let d = distro();
    let bob = record("bob", uid_of_bob(), "/home/bob");
    let w = World::new(vec![bob.clone()]);
    let in_wsl = instance_of(&bob);
    let local = Instance::local(CLAUDE_CODE);
    let data = "/home/bob/.local/state/pluxy/tools/pluxy/claude-code";
    assert_eq!(
        as_root(&format!("test -e {data} && echo yes || echo no")).trim(),
        "no",
        "claude-code's data folder in the distribution is there before the test"
    );
    // The local instance's file, on this computer.
    let windows_file = w.root.join("home").join("settings.json");
    std::fs::write(&windows_file, ORIGINAL).unwrap();
    let windows_file_text = windows_file.to_str().unwrap().to_string();
    let read_wsl = || as_root(&format!("cat '{}'", w.file));
    let read_windows = || std::fs::read_to_string(&windows_file).unwrap();

    // Preview: the file pinned, nothing applied, and nothing made in the distribution.
    let v = w.preview(&in_wsl, &w.file);
    assert_eq!(v["targets"][0]["target"], w.file.as_str(), "{v}");
    assert_eq!(v["applied"], false, "{v}");
    assert_eq!(
        as_root(&format!("test -e {data} && echo yes || echo no")).trim(),
        "no",
        "a preview made the data folder"
    );
    assert!(w.vault_folders().is_empty(), "{:?}", w.vault_folders());

    // Apply: written by bob, owner-only; the data folder bob's and owner-only; the records the
    // instance's own, in its suffixed vault folder.
    let key = format!("sk-pluxy-wsl-real-{}", w.tag);
    let o = w.apply(&in_wsl, &w.file, &key);
    assert_eq!(o["outcome"], "applied", "{o}");
    assert_eq!(stat(&w.file), "bob 600 regular file");
    let text = read_wsl();
    assert!(
        text.contains(BASE_URL) && text.contains(&key) && text.contains("\"theme\""),
        "{text}"
    );
    assert_eq!(stat(data), "bob 700 directory");
    assert_eq!(
        w.vault_folders(),
        BTreeSet::from([wsl_folder(&d, "bob")]),
        "the WSL instance's records are not in its own vault folder"
    );
    let v = w.preview(&in_wsl, &w.file);
    assert_eq!(v["applied"], true, "{v}");
    assert_eq!(v["settings"]["state"], "configured", "{v}");
    assert_eq!(v["settings"]["key_set"], true, "{v}");

    // Clear: the values go, the person's stay.
    let o = w.undo(&in_wsl, "clear", &w.file);
    assert_eq!(o["outcome"], "cleared", "{o}");
    let text = read_wsl();
    assert!(
        !text.contains(&key) && !text.contains(BASE_URL) && text.contains("\"theme\""),
        "{text}"
    );
    assert_eq!(w.preview(&in_wsl, &w.file)["applied"], false);

    // The same package on this computer: its own records, in the unsuffixed folder.
    let local_key = format!("sk-pluxy-local-real-{}", w.tag);
    let o = w.apply(&local, &windows_file_text, &local_key);
    assert_eq!(o["outcome"], "applied", "{o}");
    assert!(read_windows().contains(&local_key), "{}", read_windows());
    assert_eq!(
        w.vault_folders(),
        BTreeSet::from([local_folder(), wsl_folder(&d, "bob")])
    );

    // Both applied; each restores only its own.
    let o = w.apply(&in_wsl, &w.file, &key);
    assert_eq!(o["outcome"], "applied", "{o}");
    assert!(read_wsl().contains(&key));
    let o = w.undo(&in_wsl, "restore", &w.file);
    assert_eq!(o["outcome"], "restored", "{o}");
    assert_eq!(read_wsl(), ORIGINAL);
    assert_eq!(stat(&w.file).split(' ').next(), Some("bob"));
    assert!(
        read_windows().contains(&local_key),
        "the WSL instance's restore reached the local file: {}",
        read_windows()
    );
    let o = w.undo(&local, "restore", &windows_file_text);
    assert_eq!(o["outcome"], "restored", "{o}");
    assert_eq!(read_windows(), ORIGINAL);
    assert_eq!(read_wsl(), ORIGINAL);

    // Their journals: each instance's own, all ended.
    let (j_wsl, j_local) = (
        w.vault.journals_in(&in_wsl).unwrap(),
        w.vault.journals_in(&local).unwrap(),
    );
    assert!(!j_wsl.is_empty() && !j_local.is_empty());
    assert!(j_wsl.iter().chain(&j_local).all(|j| j.outcome.is_some()));
    assert!(
        j_wsl.iter().all(|a| j_local.iter().all(|b| a.id != b.id)),
        "{j_wsl:?} {j_local:?}"
    );
    // The tool runs in the distribution until the host stops it, and no process of it is left.
    let marker = format!("pluxy-wsl-real-{}", w.tag);
    assert!(
        !processes(&marker).is_empty(),
        "no process of the WSL instance runs"
    );
    w.host.stop_all();
    assert!(none_left(&marker), "{:?}", processes(&marker));
}

/// A refused start of the WSL instance of record in w: the call fails as expected says, after
/// which nothing changed under the distribution's homes, no process of the test runs there, no
/// tool of the instance runs here, and the instance has no vault records. Then the positive
/// control on the same route: bob's instance previews bob's file.
fn refused_with_no_outside_effects(
    w: &World,
    record: &Record,
    bob: &Record,
    expected: &dyn Fn(&CallError, &Status),
) {
    let instance = instance_of(record);
    let mark = w.mark();
    let e = w
        .call(
            &instance,
            "preview",
            json!({"targets": [{"file": w.file}]}),
            &[],
        )
        .unwrap_err();
    // The run ends, and its last stderr lines (none when wsl.exe itself refused) reach the status.
    let deadline = Instant::now() + Duration::from_secs(5);
    let mut status = w.host.status_in(&instance);
    while (status.pid.is_some() || status.log.is_empty()) && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(100));
        status = w.host.status_in(&instance);
    }
    expected(&e, &status);
    assert_eq!(status.pid, None, "{status:?}");
    assert_eq!(
        changed_since(&mark),
        "",
        "files changed in the distribution"
    );
    let marker = format!("pluxy-wsl-real-{}", w.tag);
    assert!(none_left(&marker), "{:?}", processes(&marker));
    let suffix = format!("@{}", &wsl_folder(&record.distro, &record.user)[65..]);
    assert!(
        w.vault_folders().iter().all(|f| !f.ends_with(&suffix)),
        "{:?}",
        w.vault_folders()
    );
    // The positive control.
    let v = w.preview(&instance_of(bob), &w.file);
    assert_eq!(v["targets"][0]["target"], w.file.as_str(), "{v}");
}

#[test]
#[ignore = "needs a real WSL distribution (the WSL job)"]
fn wsl_real_a_user_missing_in_the_distribution_is_refused_with_no_outside_effects() {
    let uid = uid_of_bob();
    let bob = record("bob", uid, "/home/bob");
    let missing = record("pluxynosuchuser", uid + 100, "/home/pluxynosuchuser");
    let w = World::new(vec![bob.clone(), missing.clone()]);
    refused_with_no_outside_effects(&w, &missing, &bob, &|e, _| {
        assert!(e.message.contains("did not start: wsl.exe said "), "{e:?}");
        // WSL 1 says it with an error code of its own (`0xd000020c`).
        if wsl_version() == 2 {
            assert!(e.message.contains("User not found."), "{e:?}");
        }
    });
}

#[test]
#[ignore = "needs a real WSL distribution (the WSL job)"]
fn wsl_real_a_distribution_not_there_is_refused_with_no_outside_effects() {
    let bob = record("bob", uid_of_bob(), "/home/bob");
    let gone = Record {
        distro: "PluxyNoSuchDistro".into(),
        ..bob.clone()
    };
    let w = World::new(vec![bob.clone(), gone.clone()]);
    refused_with_no_outside_effects(&w, &gone, &bob, &|e, _| {
        assert!(
            e.message.contains(
                "did not start: wsl.exe said There is no distribution with the supplied name."
            ),
            "{e:?}"
        );
    });
}

#[test]
#[ignore = "needs a real WSL distribution (the WSL job)"]
fn wsl_real_a_record_naming_another_user_than_the_program_runs_as_is_refused_before_hello() {
    let d = distro();
    let uid = uid_of_bob();
    let bob = record("bob", uid, "/home/bob");
    // A user name made again for another account: here, a second name for bob's user number
    // (listed after bob, so the number's name is bob's). wsl.exe runs the program as that
    // number; the program's identity check finds bob, not the record's user, and stops.
    let alias = format!("pluxyre{}", &tag()[..6]);
    let stale = record(&alias, uid, "/home/bob");
    let w = World::new(vec![bob.clone(), stale.clone()]);
    w.add_user(
        &alias,
        &format!("-o -u {uid} -g bob -M -N -d /nonexistent-{alias} -s /bin/sh"),
    );
    refused_with_no_outside_effects(&w, &stale, &bob, &|e, status| {
        // A tool that ends before hello fails its start, which a call sees as unavailable.
        assert_eq!(e.code, "extension_unavailable", "{e:?}");
        assert!(e.message.ends_with("exited as it started"), "{e:?}");
        let log = status.log.join("\n");
        assert!(
            log.contains(&format!("pluxy expected WSL {d}/{alias}, this is {d}/bob")),
            "{e:?} {status:?}"
        );
    });
}

/// Knows no secret.
struct NoValues;

impl crate::spawn::Values for NoValues {
    fn value(&self, _: &str, id: &str) -> Result<(String, Option<u64>), String> {
        Err(format!("no secret {id}"))
    }
    fn still(&self, _: &[crate::spawn::Given]) -> Result<(), String> {
        Ok(())
    }
}

#[test]
#[ignore = "needs a real WSL distribution (the WSL job)"]
fn wsl_real_a_managed_launch_through_wsl_exe_runs_as_bob_with_its_variable_and_ends_at_its_stop() {
    let d = distro();
    let t = tag();
    let folder = format!("/tmp/pluxy-real-{t}");
    let standin = format!("{folder}/standin");
    let seen = format!("{folder}/seen");
    // A stand-in program: records how it was started, then runs until it is ended.
    let script = format!(
        "#!/bin/sh\n\
         {{ printf 'argv'; for a in \"$@\"; do printf ' [%s]' \"$a\"; done; printf '\\n'\n\
         printf 'cwd %s\\nuser %s\\nvar %s\\nwslenv %s\\nprofile %s\\n' \"$(pwd)\" \"$(id -un)\" \
         \"${{STANDIN_MODE-unset}}\" \"${{WSLENV-unset}}\" \"${{USERPROFILE-unset}}\"; }} > {seen}.part\n\
         mv {seen}.part {seen}\n\
         while :; do sleep 1; done\n"
    );
    let cleanup = OnDrop(|| {
        let (code, out, err) = wsl(&["-d", &d, "-u", "root", "--exec", "rm", "-rf", "--", &folder]);
        if code != 0 {
            eprintln!("cleaning up {folder} failed ({code}): {out} {err}");
        }
    });
    as_root(&format!(
        "set -e; install -d -o bob -g bob -m 700 {folder}; cat > {standin} <<'EOF'\n{script}EOF\n\
         chmod 755 {standin}"
    ));
    let (_, request) = crate::spawn::parse(
        &json!({"lifetime": "managed", "environment": {"set": {"STANDIN_MODE": format!("mode-{t}")}}}),
        &|_| false,
    )
    .unwrap();
    assert_eq!(
        request,
        Request {
            set: BTreeMap::from([("STANDIN_MODE".to_string(), format!("mode-{t}"))]),
            ..Request::default()
        }
    );
    let exe = wsl_exe();
    let profile = PathBuf::from(var("USERPROFILE"));
    let launch = Launch {
        opened: Box::new(crate::spawn::Plain {
            executable: std::fs::File::open(&exe).unwrap(),
            cwd: open_dir(&profile),
        }),
        named: exe,
        argv: [
            "-d",
            &d,
            "-u",
            "bob",
            "--cd",
            "/home/bob",
            "--exec",
            &standin,
            "a b",
            "\"q\"",
            "ü",
        ]
        .iter()
        .map(|s| s.to_string())
        .collect(),
        lifetime: Lifetime::Managed,
        request,
        secrets: vec![],
        stdin: None,
    };
    let rt = Runtimes::open(None, Box::new(NoValues));
    let _stop = OnDrop(|| rt.stop_all());
    let r = rt.start("pluxy/test", launch).unwrap();
    let id = r["runtime"].as_str().unwrap().to_string();
    let deadline = Instant::now() + Duration::from_secs(60);
    let said = loop {
        let out = as_root(&format!("cat {seen} 2>/dev/null || true"));
        if !out.is_empty() {
            break out;
        }
        assert!(Instant::now() < deadline, "the stand-in never started");
        std::thread::sleep(Duration::from_millis(500));
    };
    assert_eq!(
        said,
        format!(
            "argv [a b] [\"q\"] [ü]\ncwd /home/bob\nuser bob\nvar mode-{t}\n\
             wslenv STANDIN_MODE/u\nprofile unset\n"
        )
    );
    // It runs in the distribution until its stop, which ends it there.
    assert!(!processes(&standin).is_empty(), "the stand-in does not run");
    assert_eq!(rt.status("pluxy/test", &id).unwrap()["state"], "running");
    let s = rt.stop("pluxy/test", &id).unwrap();
    assert_eq!(s["state"], "exited", "{s}");
    assert!(none_left(&standin), "{:?}", processes(&standin));
    drop(cleanup);
}
