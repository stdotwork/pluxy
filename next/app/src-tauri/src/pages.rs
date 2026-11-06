//! Extension pages. Each page the window shows gets a webview of its own, labelled `page-<n>`,
//! that no capability names, so it can call no app command and no Tauri plugin. It loads one
//! app document (`../page/`) from the `pluxy-page:` scheme, which shows the extension's page in
//! a sandboxed frame from core's pages site. The scheme is the webview's one way out: `/send`
//! passes the page's channel messages to the window, tagged with the webview they came from,
//! never with anything the page says about itself.

use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::collections::HashMap;
use url::Url;

/// The scheme the page webviews load the app's document from.
pub const SCHEME: &str = "pluxy-page";
/// The most one message from a page webview may hold. The window's channel refuses calls over
/// 64 KiB with an answer; this only keeps the transport bounded.
pub const MAX_SEND: usize = 256 << 10;
/// The most page webviews open at once.
pub const MAX_OPEN: usize = 16;

const INDEX: &str = include_str!("../../page/index.html");
const SCRIPT: &str = include_str!("../../page/frame.js");
const STYLE: &str = include_str!("../../page/frame.css");

/// The page a webview shows, as the window opened it.
#[derive(Clone, Debug, PartialEq)]
pub struct Binding {
    pub extension: String,
    pub page: String,
    pub title: String,
    pub url: Url,
    pub pages_origin: String,
}

/// Why url may not be shown as a page from pages_origin: it must be an http or https address on
/// that origin.
pub fn page_url(url: &str, pages_origin: &str) -> Result<Url, String> {
    let u = Url::parse(url).map_err(|_| "the page address is not a URL".to_string())?;
    let o = Url::parse(pages_origin).map_err(|_| "the pages site is not a URL".to_string())?;
    if !matches!(o.scheme(), "http" | "https") {
        return Err("extension pages must be served over http or https".into());
    }
    if u.origin() != o.origin() {
        return Err("the page is not on the extension pages site".into());
    }
    Ok(u)
}

/// The open page webviews.
#[derive(Default)]
pub struct Pages {
    next: u64,
    open: HashMap<String, Binding>,
}

impl Pages {
    /// A label for a new page webview, bound to b.
    pub fn bind(&mut self, b: Binding) -> Result<String, String> {
        if self.open.len() >= MAX_OPEN {
            return Err("too many extension pages are open".into());
        }
        self.next += 1;
        let label = format!("page-{}", self.next);
        self.open.insert(label.clone(), b);
        Ok(label)
    }

    pub fn get(&self, label: &str) -> Option<&Binding> {
        self.open.get(label)
    }

    pub fn unbind(&mut self, label: &str) -> Option<Binding> {
        self.open.remove(label)
    }

    pub fn labels(&self) -> Vec<String> {
        self.open.keys().cloned().collect()
    }

    pub fn clear(&mut self) {
        self.open.clear();
    }
}

/// A message from a page webview, passed on to the window as it is: the window's channel checks
/// everything in it.
#[derive(Debug, Deserialize)]
pub struct Sent {
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(default)]
    pub data: Value,
}

/// What the window gets: the message, and the webview it came from.
#[derive(Clone, Debug, Serialize)]
pub struct ToWindow {
    pub label: String,
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(skip_serializing_if = "Value::is_null")]
    pub data: Value,
}

/// A reply of the `pluxy-page:` scheme.
#[derive(Debug, PartialEq)]
pub struct Served {
    pub status: u16,
    pub kind: &'static str,
    pub body: Vec<u8>,
    /// The policy of the app document: its own files, frames from the pages site only.
    pub csp: String,
}

fn served(status: u16, kind: &'static str, body: impl Into<Vec<u8>>, csp: String) -> Served {
    Served {
        status,
        kind,
        body: body.into(),
        csp,
    }
}

/// Answers a request of the `pluxy-page:` scheme from the webview labelled label. Ok(Some) is a
/// message for the window. Only page webviews get anything; the window and any other webview
/// get 403.
pub fn serve(
    pages: &Pages,
    label: &str,
    method: &str,
    path: &str,
    body: &[u8],
) -> (Served, Option<ToWindow>) {
    let none = "default-src 'none'".to_string();
    let Some(b) = pages.get(label) else {
        return (served(403, "text/plain", "not a page webview", none), None);
    };
    let csp = format!(
        "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; \
         frame-src {}/; base-uri 'none'; form-action 'none'",
        b.pages_origin
    );
    match (method, path) {
        ("GET", "/") => (served(200, "text/html; charset=utf-8", INDEX, csp), None),
        ("GET", "/frame.js") => (
            served(200, "text/javascript; charset=utf-8", SCRIPT, csp),
            None,
        ),
        ("GET", "/frame.css") => (served(200, "text/css; charset=utf-8", STYLE, csp), None),
        ("GET", "/frame") => {
            let info = serde_json::json!({"url": b.url.as_str(), "title": b.title});
            (served(200, "application/json", info.to_string(), csp), None)
        }
        ("POST", "/send") => {
            if body.len() > MAX_SEND {
                return (served(413, "text/plain", "too large", csp), None);
            }
            let Ok(sent) = serde_json::from_slice::<Sent>(body) else {
                return (served(400, "text/plain", "not a message", csp), None);
            };
            if !matches!(sent.kind.as_str(), "hello" | "call" | "closed") {
                return (served(400, "text/plain", "not a message", csp), None);
            }
            let msg = ToWindow {
                label: label.to_string(),
                kind: sent.kind,
                data: sent.data,
            };
            (served(204, "text/plain", "", csp), Some(msg))
        }
        _ => (served(404, "text/plain", "not found", csp), None),
    }
}

/// What the window sends a page webview: kept to the kinds the app document handles.
pub fn delivery(msg: &Value) -> Result<String, String> {
    let kind = msg.get("type").and_then(Value::as_str).unwrap_or("");
    if !matches!(kind, "welcome" | "refused" | "message") {
        return Err(format!("not a page message: {kind:?}"));
    }
    // JSON is a JavaScript expression, so the script holds the message as data only.
    let text = serde_json::to_string(msg).map_err(|e| e.to_string())?;
    if text.len() > 2 * MAX_SEND {
        return Err("the message is too large".into());
    }
    Ok(format!("window.__pluxyDeliver({text})"))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn binding() -> Binding {
        Binding {
            extension: "test/alpha".into(),
            page: "usage".into(),
            title: "Alpha usage".into(),
            url: Url::parse("http://localhost:8401/test/alpha/abc/index.html").unwrap(),
            pages_origin: "http://localhost:8401".into(),
        }
    }

    #[test]
    fn pages_come_from_the_pages_site_only() {
        assert!(page_url(
            "http://localhost:8401/a/b/1/index.html",
            "http://localhost:8401"
        )
        .is_ok());
        assert!(page_url("http://localhost:8402/a.html", "http://localhost:8401").is_err());
        assert!(page_url("https://localhost:8401/a.html", "http://localhost:8401").is_err());
        assert!(page_url("javascript:alert(1)", "http://localhost:8401").is_err());
        assert!(page_url("file:///etc/passwd", "file:///").is_err());
        assert!(page_url("tauri://localhost/index.html", "tauri://localhost").is_err());
    }

    #[test]
    fn only_page_webviews_are_served_and_messages_are_tagged_by_label() {
        let mut pages = Pages::default();
        let label = pages.bind(binding()).unwrap();
        assert_eq!(label, "page-1");

        let (r, m) = serve(&pages, "main", "GET", "/", b"");
        assert_eq!((r.status, m.is_none()), (403, true));
        let (r, _) = serve(&pages, "page-9", "GET", "/frame", b"");
        assert_eq!(r.status, 403);

        let (r, _) = serve(&pages, &label, "GET", "/", b"");
        assert_eq!(r.status, 200);
        assert!(r.csp.contains("frame-src http://localhost:8401/"));
        let (r, _) = serve(&pages, &label, "GET", "/frame", b"");
        assert!(String::from_utf8(r.body)
            .unwrap()
            .contains("/test/alpha/abc/index.html"));

        // Whatever the message says about itself, the window gets the webview's label.
        let (r, m) = serve(
            &pages,
            &label,
            "POST",
            "/send",
            br#"{"type":"call","data":{"v":0,"id":"1","method":"config.get","params":null},"label":"page-2"}"#,
        );
        assert_eq!(r.status, 204);
        let m = m.unwrap();
        assert_eq!((m.label.as_str(), m.kind.as_str()), ("page-1", "call"));

        for bad in [&b"{}"[..], b"not json", br#"{"type":"welcome"}"#] {
            assert_eq!(serve(&pages, &label, "POST", "/send", bad).0.status, 400);
        }
        let big = vec![b' '; MAX_SEND + 1];
        assert_eq!(serve(&pages, &label, "POST", "/send", &big).0.status, 413);
        assert_eq!(
            serve(&pages, &label, "GET", "/etc/passwd", b"").0.status,
            404
        );

        pages.unbind(&label);
        assert_eq!(serve(&pages, &label, "GET", "/", b"").0.status, 403);
    }

    #[test]
    fn deliveries_are_data() {
        let js = delivery(&serde_json::json!({"type":"message","data":"</script>\u{2028}`${x}`"}))
            .unwrap();
        assert!(js.starts_with("window.__pluxyDeliver({") && js.ends_with("})"));
        assert!(delivery(&serde_json::json!({"type":"eval"})).is_err());
    }

    #[test]
    fn open_pages_are_bounded() {
        let mut pages = Pages::default();
        for _ in 0..MAX_OPEN {
            pages.bind(binding()).unwrap();
        }
        assert!(pages.bind(binding()).is_err());
    }
}
