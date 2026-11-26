use super::*;

/// `GET /models` serving each (name, accounts).
fn models(served: &[(&str, &[&str])]) -> Value {
    json!({"models": served.iter().map(|(id, on)| json!({"id": id, "accounts": on})).collect::<Vec<_>>()})
}

/// `GET /accounts` with each (id, source, model_alias).
fn accounts(list: &[(&str, &str, Value)]) -> Value {
    json!({"accounts": list.iter().map(|(id, source, rules)| {
        let mut a = json!({"id": id, "source": source});
        if !rules.is_null() {
            a["model_alias"] = rules.clone();
        }
        a
    }).collect::<Vec<_>>()})
}

const MODEL: &str = "gpt-6";
const ALIAS: &str = "claude-opus-5-x";

#[test]
fn every_login_account_serving_the_model_gets_the_alias_and_config_accounts_are_named() {
    let kept = json!([{"name": "other", "alias": "o"}]);
    let p = plan(
        MODEL,
        ALIAS,
        &models(&[(MODEL, &["a", "cfg", "b"]), ("other-model", &["c"])]),
        &accounts(&[
            ("a", "login", Value::Null),
            ("cfg", "config", Value::Null),
            ("b", "login", kept.clone()),
            ("c", "login", Value::Null),
        ]),
    )
    .unwrap();
    assert_eq!(
        p,
        Plan {
            accounts: vec![("a".into(), Value::Null), ("b".into(), kept)],
            have: vec![],
            config: vec!["cfg".into()],
        }
    );
    let text = summary("http://127.0.0.1:1", MODEL, ALIAS, &p);
    for needle in ["a, b", "cfg", "Restore backup", "keeps its own name", ALIAS] {
        assert!(text.contains(needle), "{needle}: {text}");
    }
}

#[test]
fn a_model_only_config_accounts_serve_or_none_serves_is_refused() {
    let e = plan(
        MODEL,
        ALIAS,
        &models(&[(MODEL, &["cfg"])]),
        &accounts(&[("cfg", "config", Value::Null)]),
    )
    .unwrap_err();
    assert!(
        e.contains("only by accounts set in core's config (cfg)"),
        "{e}"
    );
    let e = plan(MODEL, ALIAS, &models(&[]), &accounts(&[])).unwrap_err();
    assert!(e.contains("serves no model named gpt-6"), "{e}");
}

#[test]
fn an_alias_taken_for_anything_else_is_refused_and_one_already_there_is_reported() {
    let a = |rules: Value| accounts(&[("a", "login", rules)]);
    // Served by another account.
    let e = plan(
        MODEL,
        ALIAS,
        &models(&[(MODEL, &["a"]), (ALIAS, &["z"])]),
        &a(Value::Null),
    )
    .unwrap_err();
    assert!(
        e.contains("core already serves claude-opus-5-x (on z)"),
        "{e}"
    );
    // The account's rule names the alias, whatever its case, for another model.
    let e = plan(
        MODEL,
        ALIAS,
        &models(&[(MODEL, &["a"])]),
        &a(json!([{"name": "gpt-5", "alias": "Claude-Opus-5-X"}])),
    )
    .unwrap_err();
    assert!(
        e.contains("already has the alias Claude-Opus-5-X for gpt-5"),
        "{e}"
    );
    // Already there (the model's case aside): nothing to change.
    let p = plan(
        MODEL,
        ALIAS,
        &models(&[(MODEL, &["a"]), (ALIAS, &["a"])]),
        &a(json!([{"name": "GPT-6", "alias": ALIAS, "fork": true}])),
    )
    .unwrap();
    assert!(p.accounts.is_empty() && p.have == ["a"], "{p:?}");
}

#[test]
fn names_are_checked_before_core_is_read() {
    let m = models(&[(MODEL, &["a"])]);
    let a = accounts(&[("a", "login", Value::Null)]);
    for (model, alias) in [
        ("", ALIAS),
        (MODEL, ""),
        (MODEL, "claude x"),
        (MODEL, MODEL),
        // Core matches a model's name whatever its case: no alias at all.
        (MODEL, "GPT-6"),
    ] {
        assert!(plan(model, alias, &m, &a).is_err(), "{model:?} {alias:?}");
    }
}

#[test]
fn account_ids_are_one_path_segment() {
    assert_eq!(segment("work-1_a.b~"), "work-1_a.b~");
    assert_eq!(segment("a/b c?%"), "a%2Fb%20c%3F%25");
}

#[test]
fn a_model_no_listed_account_serves_is_not_called_config_only_and_ids_count_once() {
    let a = accounts(&[("a", "login", Value::Null)]);
    for served in [models(&[(MODEL, &[])]), models(&[(MODEL, &["gone", ""])])] {
        let e = plan(MODEL, ALIAS, &served, &a).unwrap_err();
        assert_eq!(e, "core lists no account serving gpt-6");
    }
    let p = plan(MODEL, ALIAS, &models(&[(MODEL, &["a", "", "a"])]), &a).unwrap();
    assert_eq!(p.ids(), ["a"]);
}

#[test]
fn control_characters_in_account_ids_cannot_add_lines_to_the_confirmation() {
    let p = Plan {
        accounts: vec![("a\nKey for https://elsewhere".into(), Value::Null)],
        have: vec![],
        config: vec!["cfg\r\u{1b}[2J".into()],
    };
    let text = summary("http://127.0.0.1:1", MODEL, ALIAS, &p);
    assert!(!text.chars().any(char::is_control), "{text:?}");
    assert!(text.contains("a\\u{a}Key for https://elsewhere"), "{text}");
    assert!(text.contains("cfg\\u{d}\\u{1b}[2J"), "{text}");
}

// ---- write: against a local listener standing in for core's management API

use std::io::{BufRead, BufReader, Read, Write as _};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

/// A request as the stand-in core took it: method, path under the management API, JSON body.
pub(crate) type Seen = Arc<Mutex<Vec<(String, String, Value)>>>;

/// Serves each request with answer(method, path, body, n) (n: the requests so far), one per
/// connection; gives a caller of it and what it was sent. `client_keys.rs`'s tests use it too.
pub(crate) fn core(
    answer: impl Fn(&str, &str, &Value, usize) -> (u16, Value) + Send + 'static,
) -> (Caller, Seen) {
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let base = format!("http://{}", listener.local_addr().unwrap());
    let seen: Seen = Arc::default();
    let log = Arc::clone(&seen);
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let mut stream = stream.unwrap();
            let mut r = BufReader::new(stream.try_clone().unwrap());
            let mut line = String::new();
            r.read_line(&mut line).unwrap();
            let mut parts = line.split_whitespace();
            let method = parts.next().unwrap_or_default().to_string();
            let path = parts.next().unwrap_or_default();
            let path = path
                .strip_prefix(crate::client::MANAGEMENT)
                .unwrap_or(path)
                .to_string();
            let mut length = 0;
            loop {
                let mut h = String::new();
                r.read_line(&mut h).unwrap();
                if h.trim().is_empty() {
                    break;
                }
                if let Some((k, v)) = h.split_once(':') {
                    if k.eq_ignore_ascii_case("content-length") {
                        length = v.trim().parse().unwrap();
                    }
                }
            }
            let mut body = vec![0; length];
            r.read_exact(&mut body).unwrap();
            let body: Value = serde_json::from_slice(&body).unwrap_or(Value::Null);
            let n = log.lock().unwrap().len();
            let (status, reply) = answer(&method, &path, &body, n);
            log.lock().unwrap().push((method, path, body));
            let reply = reply.to_string();
            let _ = write!(
                stream,
                "HTTP/1.1 {status} X\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{reply}",
                reply.len()
            );
        }
    });
    (Caller::for_test(&base, "test-management-key"), seen)
}

/// Accounts a (no aliases) and b (one of its own), both added by login.
fn kept() -> Value {
    json!([{"name": "other", "alias": "o"}])
}
fn listed() -> Value {
    accounts(&[("a", "login", Value::Null), ("b", "login", kept())])
}
/// Core's models: gpt-6 on a and b, with the alias on them too when aliased.
fn served(aliased: bool) -> Value {
    if aliased {
        models(&[(MODEL, &["a", "b"]), (ALIAS, &["a", "b"])])
    } else {
        models(&[(MODEL, &["a", "b"])])
    }
}
fn asked() -> Plan {
    plan(MODEL, ALIAS, &served(false), &listed()).unwrap()
}
fn failed(status: u16, message: &str) -> (u16, Value) {
    (status, json!({"error": {"code": "x", "message": message}}))
}
fn added(list: Value) -> Value {
    let mut rules = list.as_array().cloned().unwrap_or_default();
    rules.push(json!({"name": MODEL, "alias": ALIAS, "fork": true}));
    json!({ "model_alias": rules })
}
/// The PATCHes core was sent, by account.
fn patches(seen: &Seen) -> Vec<(String, Value)> {
    seen.lock()
        .unwrap()
        .iter()
        .filter(|(m, _, _)| m == "PATCH")
        .map(|(_, p, b)| (p.clone(), b.clone()))
        .collect()
}
const SHORT: Duration = Duration::from_millis(300);

#[test]
fn a_confirmed_alias_is_added_to_each_account_after_its_own() {
    // The re-read sees no alias; once both accounts are written core lists it on them.
    let (caller, seen) = core(|method, path, _, n| match (method, path) {
        ("GET", "/models") => (200, served(n > 2)),
        ("GET", "/accounts") => (200, listed()),
        ("PATCH", _) => (200, json!({"account": {}})),
        _ => failed(404, "no such call"),
    });
    let v = write(Ok(caller), MODEL, ALIAS, &asked(), SHORT).unwrap();
    assert_eq!(v["accounts"], json!(["a", "b"]));
    assert_eq!(v["already"], false);
    assert_eq!(
        patches(&seen),
        [
            ("/accounts/a".to_string(), added(Value::Null)),
            ("/accounts/b".to_string(), added(kept())),
        ]
    );
}

#[test]
fn a_write_core_refuses_part_way_puts_the_accounts_written_back() {
    let (caller, seen) = core(|method, path, _, _| match (method, path) {
        ("GET", "/models") => (200, served(false)),
        ("GET", "/accounts") => (200, listed()),
        ("PATCH", "/accounts/b") => failed(500, "the store is read-only"),
        ("PATCH", _) => (200, json!({"account": {}})),
        _ => failed(404, "no such call"),
    });
    let e = write(Ok(caller), MODEL, ALIAS, &asked(), SHORT).unwrap_err();
    assert_eq!(
        e,
        "core did not add the alias to b: the store is read-only; the alias was taken out again"
    );
    // a had none: null sets it back to none.
    assert_eq!(
        patches(&seen),
        [
            ("/accounts/a".to_string(), added(Value::Null)),
            ("/accounts/b".to_string(), added(kept())),
            ("/accounts/a".to_string(), json!({ "model_alias": null })),
        ]
    );
}

#[test]
fn an_alias_core_does_not_then_serve_is_taken_out_of_every_account() {
    let (caller, seen) = core(|method, path, _, _| match (method, path) {
        ("GET", "/models") => (200, served(false)),
        ("GET", "/accounts") => (200, listed()),
        ("PATCH", _) => (200, json!({"account": {}})),
        _ => failed(404, "no such call"),
    });
    let e = write(Ok(caller), MODEL, ALIAS, &asked(), SHORT).unwrap_err();
    assert!(
        e.starts_with("core does not serve claude-opus-5-x on a, b"),
        "{e}"
    );
    assert!(e.ends_with("; the alias was taken out again"), "{e}");
    // Each account gets back the list it had: none for a, its own rule for b.
    assert_eq!(
        patches(&seen)[2..],
        [
            ("/accounts/a".to_string(), json!({ "model_alias": null })),
            ("/accounts/b".to_string(), json!({ "model_alias": kept() })),
        ]
    );
}

#[test]
fn a_core_that_cannot_be_asked_after_the_write_has_it_taken_out_at_once() {
    let lists = Arc::new(AtomicUsize::new(0));
    let (caller, seen) = core(move |method, path, _, _| match (method, path) {
        // The re-read answers; asked whether it lists the alias, core no longer takes the key.
        ("GET", "/models") if lists.fetch_add(1, Ordering::SeqCst) == 0 => (200, served(false)),
        ("GET", "/models") => failed(401, "missing or invalid management key"),
        ("GET", "/accounts") => (200, listed()),
        ("PATCH", _) => (200, json!({"account": {}})),
        _ => failed(404, "no such call"),
    });
    let started = Instant::now();
    let e = write(Ok(caller), MODEL, ALIAS, &asked(), Duration::from_secs(30)).unwrap_err();
    assert!(
        started.elapsed() < Duration::from_secs(10),
        "it waited for core"
    );
    assert_eq!(
        e,
        "whether core serves claude-opus-5-x could not be read (core did not answer GET /models: missing or invalid management key); the alias was taken out again"
    );
    assert_eq!(patches(&seen).len(), 4);
}

#[test]
fn nothing_is_sent_when_the_connection_or_core_s_accounts_changed_since_the_person_was_asked() {
    let (caller, seen) = core(|method, path, _, _| match (method, path) {
        // Since the person was asked, b no longer serves the model.
        ("GET", "/models") => (200, models(&[(MODEL, &["a"])])),
        ("GET", "/accounts") => (200, listed()),
        _ => (200, json!({})),
    });
    let changed = "The connection changed. Create the alias again.";
    let e = write(Err(changed.into()), MODEL, ALIAS, &asked(), SHORT).unwrap_err();
    assert_eq!(e, changed);
    assert!(seen.lock().unwrap().is_empty(), "a call went out");
    let e = write(Ok(caller), MODEL, ALIAS, &asked(), SHORT).unwrap_err();
    assert_eq!(
        e,
        "core's accounts changed while you were asked. Create the alias again."
    );
    assert!(patches(&seen).is_empty(), "core was changed");
}
