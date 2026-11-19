//! The TOML editor's own tests, on a made-up config no tool writes. Expected bytes are written out
//! by hand, never worked out by the code under test.

use crate::toml::toml_edit::{DocumentMut, Item};
use crate::toml::{leaves, merge, plain, read, refuse, set, table_at, table_mut};
use pluxy_program::Error;
use serde_json::json;

const REFUSAL: &str = "this is not a test config: ";

/// A tool's parse: the library's read, and one table, `[a]`, that must be a table.
fn parse(bytes: Option<&[u8]>) -> Result<DocumentMut, Error> {
    let doc = read(bytes, REFUSAL)?;
    table_at(doc.as_table(), "a", "a", REFUSAL)?;
    Ok(doc)
}

const ORIGINAL: &str = r#"# kept by hand
name = "mine" # my name
count = 3

[a]
x = 1

[other]
future = true
"#;

/// ORIGINAL after a made-up apply: name replaced (its comment kept), `added` and `[a.b]` new.
const APPLIED: &str = r#"# kept by hand
name = "theirs" # my name
count = 3
added = "yes"

[a]
x = 1

[a.b]
y = 2

[other]
future = true
"#;

fn text(b: Option<Vec<u8>>) -> String {
    String::from_utf8(b.expect("a file")).unwrap()
}

#[test]
fn read_takes_none_or_white_space_as_an_empty_document_and_refuses_the_rest_by_name() {
    assert!(read(None, REFUSAL).unwrap().as_table().is_empty());
    assert!(read(Some(b" \n\t"), REFUSAL).unwrap().as_table().is_empty());
    assert!(read(Some(ORIGINAL.as_bytes()), REFUSAL).is_ok());
    // Not UTF-8: the refusal starts with the tool's own words.
    let e = read(Some(&[0xff, 0xfe]), REFUSAL).unwrap_err();
    assert_eq!(
        e.message,
        "this is not a test config: the file is not UTF-8 text"
    );
    // Not TOML: the library's words, whatever the tool.
    let e = read(Some(b"name = \n"), REFUSAL).unwrap_err();
    assert!(
        e.message.starts_with("the file is not TOML: "),
        "{}",
        e.message
    );
}

#[test]
fn refuse_and_table_at_use_the_tools_opening() {
    assert_eq!(
        refuse(REFUSAL, "x is wrong").message,
        "this is not a test config: x is wrong"
    );
    let doc = read(Some(b"a = 3\n[b]\nk = 1\n"), REFUSAL).unwrap();
    let e = table_at(doc.as_table(), "a", "a", REFUSAL).unwrap_err();
    assert_eq!(e.message, "this is not a test config: a is not a table");
    assert!(table_at(doc.as_table(), "b", "b", REFUSAL)
        .unwrap()
        .is_some());
    assert!(table_at(doc.as_table(), "c", "c", REFUSAL)
        .unwrap()
        .is_none());
}

#[test]
fn set_keeps_the_keys_place_and_the_old_values_comment() {
    let mut doc = read(Some(ORIGINAL.as_bytes()), REFUSAL).unwrap();
    let root = doc.as_table_mut();
    set(root, "name", "theirs");
    set(root, "added", "yes");
    assert_eq!(
        doc.to_string(),
        "# kept by hand\nname = \"theirs\" # my name\ncount = 3\nadded = \"yes\"\n\n[a]\nx = 1\n\n[other]\nfuture = true\n"
    );
}

#[test]
fn table_mut_makes_a_missing_table_and_finds_one_that_is_there() {
    let mut doc = read(Some(ORIGINAL.as_bytes()), REFUSAL).unwrap();
    let root = doc.as_table_mut();
    // Implicit: no header of its own while it holds only tables.
    set(table_mut(table_mut(root, "p", true), "q", false), "k", "v");
    set(table_mut(root, "a", false), "x", 2);
    assert_eq!(
        doc.to_string(),
        "# kept by hand\nname = \"mine\" # my name\ncount = 3\n\n[a]\nx = 2\n\n[other]\nfuture = true\n\n[p.q]\nk = \"v\"\n"
    );
}

#[test]
fn plain_compares_values_whatever_their_formatting() {
    let a = read(
        Some(b"k = [1,   2]\nt = { u = 1 }\nd = 1979-05-27\nf = 1.50\n"),
        REFUSAL,
    )
    .unwrap();
    let b = read(Some(b"k = [ 1, 2 ]\n[t]\nu = 1\n"), REFUSAL).unwrap();
    assert_eq!(plain(a.get("k")), plain(b.get("k")));
    assert_eq!(plain(a.get("t")), plain(b.get("t")));
    assert_eq!(plain(a.get("d")), Some(json!({"datetime": "1979-05-27"})));
    assert_eq!(plain(a.get("f")), Some(json!({"float": "1.5"})));
    assert_eq!(plain(a.get("missing")), None);
    assert_eq!(plain(Some(&Item::None)), None);
    let arr = read(Some(b"[[w]]\nn = 1\n[[w]]\nn = 2\n"), REFUSAL).unwrap();
    assert_eq!(plain(arr.get("w")), Some(json!([{"n": 1}, {"n": 2}])));
}

#[test]
fn merge_of_an_untouched_apply_gives_the_original_bytes() {
    let back = merge(
        parse,
        Some(ORIGINAL.as_bytes()),
        Some(APPLIED.as_bytes()),
        APPLIED.as_bytes(),
    )
    .unwrap();
    assert_eq!(text(back), ORIGINAL);
}

#[test]
fn merge_keeps_what_the_person_changed_and_their_comments() {
    // The person changed name, and added a key and a comment inside what apply made.
    let current = APPLIED
        .replace("name = \"theirs\"", "name = \"changed\"")
        .replace("y = 2\n", "y = 2\n# mine\nz = 9\n");
    let out = merge(
        parse,
        Some(ORIGINAL.as_bytes()),
        Some(APPLIED.as_bytes()),
        current.as_bytes(),
    )
    .unwrap();
    assert_eq!(
        text(out),
        r#"# kept by hand
name = "changed" # my name
count = 3

[a]
x = 1

[a.b]
# mine
z = 9

[other]
future = true
"#
    );
}

#[test]
fn merge_of_a_file_apply_made_removes_it_but_keeps_what_the_person_added() {
    let made = "added = \"yes\"\n\n[a.b]\ny = 2\n";
    assert_eq!(
        merge(parse, None, Some(made.as_bytes()), made.as_bytes()).unwrap(),
        None
    );
    let with_theirs = format!("{made}\n[mine]\nk = 1\n");
    let out = merge(parse, None, Some(made.as_bytes()), with_theirs.as_bytes()).unwrap();
    // The blank line before the person's table stays with it.
    assert_eq!(text(out), "\n[mine]\nk = 1\n");
}

#[test]
fn merge_refuses_a_file_the_tools_parse_refuses() {
    let e = merge(
        parse,
        Some(ORIGINAL.as_bytes()),
        Some(APPLIED.as_bytes()),
        b"a = 1\n",
    )
    .unwrap_err();
    assert_eq!(e.message, "this is not a test config: a is not a table");
    assert!(merge(
        parse,
        Some(ORIGINAL.as_bytes()),
        Some(APPLIED.as_bytes()),
        b"[a"
    )
    .is_err());
    // The original and the applied image are read the same way.
    assert!(merge(
        parse,
        Some(b"a = 1\n"),
        Some(APPLIED.as_bytes()),
        APPLIED.as_bytes()
    )
    .is_err());
}

#[test]
fn leaves_name_settings_by_path_down_to_non_tables_and_empty_tables() {
    let l = leaves(
        parse,
        b"top = 1\narr = [1, 2]\n[a]\nx = \"s\"\n[a.empty]\n[a.deep.k]\nv = true\n",
    )
    .unwrap();
    let got: Vec<(String, serde_json::Value)> =
        l.into_iter().map(|(p, v)| (p.join("."), v)).collect();
    assert_eq!(
        got,
        [
            ("top".to_string(), json!(1)),
            ("arr".to_string(), json!([1, 2])),
            ("a.x".to_string(), json!("s")),
            ("a.empty".to_string(), json!({})),
            ("a.deep.k.v".to_string(), json!(true)),
        ]
    );
    // Refused by the tool's parse, or not TOML: no settings.
    assert!(leaves(parse, b"a = 1\n").is_none());
    assert!(leaves(parse, b"= nope").is_none());
}
