//! The YAML editor's own tests, on a made-up file no tool writes. Expected bytes are written out
//! by hand, never worked out by the code under test.

use crate::yaml::{edit, leaves, parse, Yamls};
use crate::Merge;
use serde_json::{json, Value};

const PRODUCT: &str = "Test Program";

const ORIGINAL: &str = "# kept by hand\nname: mine # my name\ncount: 3\nlist:\n  - one\n  - two\nsection:\n  x: 1\n  y: 'two'\nother:\n  future: true\n";

fn p(text: &str) -> Value {
    parse(Some(text.as_bytes()), PRODUCT).unwrap()
}

fn text(b: Vec<u8>) -> String {
    String::from_utf8(b).unwrap()
}

/// ORIGINAL's file edited to want, as a tool's apply does.
fn edited(original: &str, want: &Value) -> Result<String, pluxy_program::Error> {
    let cur = p(original);
    edit(Some(original.as_bytes()), &cur, want, PRODUCT).map(text)
}

#[test]
fn parse_reads_a_mapping_and_takes_nothing_or_null_as_an_empty_one() {
    assert_eq!(
        p("a: 1\nb:\n  - x\n  - 2.5\n  - true\n  - ~\n"),
        json!({"a": 1, "b": ["x", 2.5, true, null]})
    );
    assert_eq!(parse(None, PRODUCT).unwrap(), json!({}));
    for empty in ["", "# only a comment\n", "~\n", "null\n", "---\nnull\n"] {
        assert_eq!(p(empty), json!({}), "{empty:?}");
    }
    // Keys that are numbers or booleans are read as their text; the order is the file's.
    let v = p("z: 1\n1: a\ntrue: b\n");
    assert_eq!(
        v.as_object().unwrap().keys().collect::<Vec<_>>(),
        ["z", "1", "true"]
    );
}

#[test]
fn parse_refuses_what_a_settings_file_is_not() {
    for (bytes, says) in [
        ("a: [1\n", "the file is not YAML"),
        ("- a\n- b\n", "top level is not a mapping"),
        ("just text\n", "top level is not a mapping"),
        ("a: 1\n---\nb: 2\n", "more than one YAML document"),
        ("? [x]\n: 1\n", "a key that is not text"),
    ] {
        let e = parse(Some(bytes.as_bytes()), PRODUCT).unwrap_err();
        assert!(e.message.contains(says), "{bytes:?}: {}", e.message);
    }
    let e = parse(Some(&[0xff, 0xfe]), PRODUCT).unwrap_err();
    assert_eq!(e.message, "the file is not UTF-8 text");
}

#[test]
fn a_value_the_tools_settings_do_not_use_is_refused_naming_the_program() {
    let e = parse(Some(b"a: !custom 1\n"), PRODUCT).unwrap_err();
    assert_eq!(
        e.message,
        "the file uses a YAML tag or a value Test Program's settings do not use"
    );
    let e = parse(Some(b"a: !custom 1\n"), "Other Program").unwrap_err();
    assert!(
        e.message.contains("Other Program's settings"),
        "{}",
        e.message
    );
}

#[test]
fn edit_changes_only_the_keys_that_differ_keeping_comments_order_and_quoting() {
    let mut want = p(ORIGINAL);
    want["name"] = json!("theirs");
    want["section"]["x"] = json!(2);
    want["added"] = json!("yes");
    assert_eq!(
        edited(ORIGINAL, &want).unwrap(),
        "# kept by hand\nname: theirs # my name\ncount: 3\nlist:\n  - one\n  - two\nsection:\n  x: 2\n  y: 'two'\nother:\n  future: true\nadded: 'yes'\n"
    );
}

#[test]
fn edit_removes_keys_want_drops_and_leaves_the_file_alone_when_nothing_differs() {
    let mut want = p(ORIGINAL);
    want.as_object_mut().unwrap().remove("list");
    want["section"].as_object_mut().unwrap().remove("y");
    assert_eq!(
        edited(ORIGINAL, &want).unwrap(),
        "# kept by hand\nname: mine # my name\ncount: 3\nsection:\n  x: 1\nother:\n  future: true\n"
    );
    // The same value: the same bytes, even ones that are not tidy.
    let untidy = "a:   1   # c\n\n\nb: [1,2]\n";
    assert_eq!(edited(untidy, &p(untidy)).unwrap(), untidy);
}

#[test]
fn edit_builds_new_mappings_and_sequences_in_the_order_given() {
    let want = json!({"z": {"b": 1, "a": [{"k": "v"}, [1, 2], null, false, "s", 1.5]}});
    let out = edited("", &want).unwrap();
    assert_eq!(p(&out), want);
    // Order is the order given, not alphabetical.
    assert!(out.find("b: 1").unwrap() < out.find("a:").unwrap(), "{out}");
}

#[test]
fn edit_replaces_a_document_that_is_only_null_and_keeps_its_comments() {
    for null in ["~\n", "null\n", "---\nnull\n"] {
        let out = edited(null, &json!({"k": {"v": 1}})).unwrap();
        assert_eq!(out, "k:\n  v: 1\n", "{null:?}");
    }
    assert_eq!(
        edited("# mine\nnull\n", &json!({"k": 1})).unwrap(),
        "# mine\nk: 1\n"
    );
    assert_eq!(
        edited("# only a comment\n", &json!({"k": 1})).unwrap(),
        "# only a comment\nk: 1\n"
    );
}

#[test]
fn edit_quotes_what_would_read_back_as_something_else() {
    let want = json!({"a": "yes: no", "b": "# not a comment", "c": "123", "d": "true", "e": "", "f": " pad"});
    let out = edited("", &want).unwrap();
    assert_eq!(p(&out), want, "{out}");
}

#[test]
fn edit_refuses_a_value_that_would_not_read_back_as_what_was_asked() {
    // Not a mapping: a programming error, not a file the person has.
    let e = edit(None, &json!([1]), &json!({}), PRODUCT).unwrap_err();
    assert_eq!(e.message, "a settings file's value is a mapping");
    // A file whose text is not UTF-8 is refused before any edit.
    let e = edit(Some(&[0xff]), &json!({}), &json!({"a": 1}), PRODUCT).unwrap_err();
    assert_eq!(e.message, "the file is not UTF-8 text");
}

#[test]
fn leaves_name_settings_by_path() {
    let l = leaves(ORIGINAL.as_bytes(), PRODUCT).unwrap();
    let paths: Vec<String> = l.iter().map(|(p, _)| p.join(".")).collect();
    assert_eq!(
        paths,
        [
            "name",
            "count",
            "list",
            "section.x",
            "section.y",
            "other.future"
        ]
    );
    assert_eq!(leaves(b"a: [1\n", PRODUCT), None);
}

const APPLIED: &str = "# kept by hand\nname: theirs # my name\ncount: 3\nlist:\n  - one\n  - two\nsection:\n  x: 1\n  y: 'two'\n  added: 5\nother:\n  future: true\n";

#[test]
fn merge_of_an_untouched_apply_gives_the_original_bytes() {
    let out = Yamls(PRODUCT)
        .merge(
            Some(ORIGINAL.as_bytes()),
            Some(APPLIED.as_bytes()),
            Some(APPLIED.as_bytes()),
        )
        .unwrap();
    assert_eq!(text(out.unwrap()), ORIGINAL);
}

#[test]
fn merge_takes_out_only_untouched_values_keeping_the_persons_edits_and_comments() {
    // The person changed `name` again and added a key of their own next to what apply added.
    let current = APPLIED
        .replace("name: theirs", "name: changed")
        .replace("  added: 5\n", "  added: 5\n  mine: 7 # mine\n");
    let out = Yamls(PRODUCT)
        .merge(
            Some(ORIGINAL.as_bytes()),
            Some(APPLIED.as_bytes()),
            Some(current.as_bytes()),
        )
        .unwrap();
    assert_eq!(
        text(out.unwrap()),
        "# kept by hand\nname: changed # my name\ncount: 3\nlist:\n  - one\n  - two\nsection:\n  x: 1\n  y: 'two'\n  mine: 7 # mine\nother:\n  future: true\n"
    );
}

#[test]
fn merge_of_a_file_apply_made_removes_it_but_keeps_what_the_person_added() {
    let made = "k:\n  v: 1\n  w: 2\n";
    let m = Yamls(PRODUCT);
    assert_eq!(
        m.merge(None, Some(made.as_bytes()), Some(made.as_bytes()))
            .unwrap(),
        None
    );
    // Reordered by another program: the values are what is compared.
    let reordered = "k:\n  w: 2\n  v: 1\n";
    assert_eq!(
        m.merge(None, Some(made.as_bytes()), Some(reordered.as_bytes()))
            .unwrap(),
        None
    );
    // The file is gone: nothing to clear.
    assert_eq!(
        m.merge(Some(b"a: 1\n"), Some(made.as_bytes()), None)
            .unwrap(),
        None
    );
    let theirs = "k:\n  v: 1\n  w: 2\nmine: 2\n";
    let out = m
        .merge(None, Some(made.as_bytes()), Some(theirs.as_bytes()))
        .unwrap();
    assert_eq!(text(out.unwrap()), "mine: 2\n");
}

#[test]
fn merge_refuses_a_current_file_that_is_not_the_tools() {
    let e = Yamls(PRODUCT)
        .merge(
            Some(ORIGINAL.as_bytes()),
            Some(APPLIED.as_bytes()),
            Some(b"- a\n"),
        )
        .unwrap_err();
    assert!(
        e.message.contains("top level is not a mapping"),
        "{}",
        e.message
    );
}
