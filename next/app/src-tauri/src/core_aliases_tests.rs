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
    for needle in ["a, b", "cfg", "Restore Backup", "keeps its own name", ALIAS] {
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
    ] {
        assert!(plan(model, alias, &m, &a).is_err(), "{model:?} {alias:?}");
    }
}

#[test]
fn account_ids_are_one_path_segment() {
    assert_eq!(segment("work-1_a.b~"), "work-1_a.b~");
    assert_eq!(segment("a/b c?%"), "a%2Fb%20c%3F%25");
}
