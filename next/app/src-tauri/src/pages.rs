//! The native boundary for page webviews, including requests posted directly by a hostile frame.
use serde::{Deserialize, Serialize};
use serde_json::{json, value::RawValue, Value};
use std::collections::{HashMap, HashSet, VecDeque};
use std::time::{Duration, Instant};
use url::Url;

pub const SCHEME: &str = "pluxy-page";
pub const MAX_SEND: usize = 256 << 10;
pub const MAX_CALL: usize = 64 << 10;
pub const MAX_OPEN: usize = 16;
pub const MAX_PACKAGE_PAGES: usize = 4;
pub const MAX_RATE: usize = 30;
pub const MAX_WAITING: usize = 8;
pub const MAX_PACKAGE_RATE: usize = 60;
pub const MAX_PACKAGE_WAITING: usize = 16;
const MAX_PACKAGE_BUDGETS: usize = 64;
// 50 settings of 4096 runes, each requiring up to six JSON bytes, plus envelope.
const MAX_REPLY: usize = 2 << 20;
const INDEX: &str = include_str!("../../page/index.html");
const SCRIPT: &str = include_str!("../../page/frame.js");
const STYLE: &str = include_str!("../../page/frame.css");

#[derive(Clone, Debug, PartialEq)]
pub struct Binding {
    pub extension: String,
    pub page: String,
    pub title: String,
    pub url: Url,
    pub pages_origin: String,
}

#[derive(Default)]
struct Document {
    generation: String,
    loaded: bool,
    hello: bool,
    welcome: bool,
    closed: bool,
    closed_notified: bool,
    recent: VecDeque<Instant>,
    waiting: HashSet<String>,
    answering: HashSet<String>,
    replies: HashSet<u64>,
    next_reply: u64,
    theme_active: bool,
    theme_pending: Option<Value>,
    frame_url: Option<Url>,
}
#[derive(Default)]
struct Package {
    recent: VecDeque<Instant>,
}
struct Page {
    binding: Binding,
    document: Document,
    previous_closed: Option<ToWindow>,
}
#[derive(Default)]
pub struct Pages {
    next: u64,
    generation: u64,
    open: HashMap<String, Page>,
    packages: HashMap<String, Package>,
    #[cfg(feature = "native-test")]
    pub ingress_fixture: bool,
}
fn expire(recent: &mut VecDeque<Instant>, now: Instant) {
    while recent
        .front()
        .is_some_and(|t| now.duration_since(*t) >= Duration::from_secs(1))
    {
        recent.pop_front();
    }
}
impl Pages {
    pub fn bind(&mut self, binding: Binding) -> Result<String, String> {
        let now = Instant::now();
        for p in self.packages.values_mut() {
            expire(&mut p.recent, now);
        }
        self.packages.retain(|id, p| {
            !p.recent.is_empty() || self.open.values().any(|w| &w.binding.extension == id)
        });
        if self.open.len() >= MAX_OPEN
            || self
                .open
                .values()
                .filter(|w| w.binding.extension == binding.extension)
                .count()
                >= MAX_PACKAGE_PAGES
        {
            return Err("too many extension pages are open".into());
        }
        if !self.packages.contains_key(&binding.extension)
            && self.packages.len() >= MAX_PACKAGE_BUDGETS
        {
            return Err("too many extension packages opened recently".into());
        }
        self.next = self.next.checked_add(1).ok_or("page labels exhausted")?;
        self.packages.entry(binding.extension.clone()).or_default();
        let label = format!("page-{}", self.next);
        self.open.insert(
            label.clone(),
            Page {
                binding,
                document: Document::default(),
                previous_closed: None,
            },
        );
        Ok(label)
    }
    pub fn get(&self, label: &str) -> Option<&Binding> {
        self.open.get(label).map(|w| &w.binding)
    }
    pub fn unbind(&mut self, label: &str) -> Option<Binding> {
        self.open.remove(label).map(|w| w.binding)
    }
    pub fn labels(&self) -> Vec<String> {
        self.open.keys().cloned().collect()
    }
    pub fn clear(&mut self) {
        self.open.clear();
    }
    /// Called only for the top document's native navigation callback, never a frame's fetch.
    /// The sequence binds lifetime; it is not a secret or proof of which frame sent a request.
    pub fn begin_document(&mut self, label: &str) {
        self.generation = self
            .generation
            .checked_add(1)
            .expect("document generations exhausted");
        if let Some(w) = self.open.get_mut(label) {
            // A second top-level document gets a fresh generation but cannot reconnect.
            let loaded = w.document.loaded;
            if loaded && !w.document.closed_notified {
                w.previous_closed = Some(ToWindow {
                    label: label.into(),
                    generation: w.document.generation.clone(),
                    kind: "closed".into(),
                    data: Value::Null,
                });
            }
            w.document = Document {
                generation: self.generation.to_string(),
                loaded: true,
                closed: loaded,
                closed_notified: loaded,
                ..Document::default()
            };
        }
    }
    /// Return each native closure once, using the generation the window already knows.
    pub fn take_closed(&mut self, label: &str) -> Option<ToWindow> {
        let w = self.open.get_mut(label)?;
        if let Some(previous) = w.previous_closed.take() {
            return Some(previous);
        }
        if !w.document.closed || w.document.closed_notified {
            return None;
        }
        w.document.closed_notified = true;
        Some(ToWindow {
            label: label.into(),
            generation: w.document.generation.clone(),
            kind: "closed".into(),
            data: Value::Null,
        })
    }
    pub fn document_closed(&self, label: &str) -> bool {
        self.open.get(label).is_none_or(|w| w.document.closed)
    }
    /// WebKit reports subframe navigation. Ignore fragment-only changes, but close on a
    /// second document or a nested frame loading another pages-site document.
    pub fn frame_navigation(&mut self, label: &str, url: &Url) {
        let Some(w) = self.open.get_mut(label) else {
            return;
        };
        #[cfg(feature = "native-test")]
        let test_frame =
            self.ingress_fixture && url.scheme() == SCHEME && url.path() == "/ingress-test";
        #[cfg(not(feature = "native-test"))]
        let test_frame = false;
        if !Url::parse(&w.binding.pages_origin).is_ok_and(|origin| origin.origin() == url.origin())
            && !test_frame
        {
            return;
        }
        if let Some(previous) = &w.document.frame_url {
            let mut before = previous.clone();
            before.set_fragment(None);
            let mut after = url.clone();
            after.set_fragment(None);
            if before == after && previous.fragment() != url.fragment() {
                w.document.frame_url = Some(url.clone());
                return;
            }
        }
        if w.document.frame_url.is_none()
            && !w.document.hello
            && (*url == w.binding.url || test_frame)
        {
            w.document.frame_url = Some(url.clone());
        } else {
            w.document.closed = true;
            w.document.waiting.clear();
            w.document.answering.clear();
            w.document.theme_pending = None;
        }
    }
    fn refusal(&self, label: &str, sent: Sent, now: Instant) -> Result<ToWindow, Refusal> {
        let w = self
            .open
            .get(label)
            .ok_or_else(|| Refusal::new(403, "bad_request"))?;
        let d = &w.document;
        // Gate cheap metadata before parsing the call or looking for its id.
        let fail = |status, code| Refusal {
            status,
            code,
            id: call_id(sent.data.get()),
        };
        if !d.loaded || d.closed || sent.generation != d.generation {
            return Err(fail(409, "bad_request"));
        }
        let data = match sent.kind.as_str() {
            "hello" => {
                if !sent.data.get().trim_start().starts_with('{') {
                    return Err(fail(400, "bad_request"));
                }
                if d.hello {
                    return Err(fail(409, "bad_request"));
                }
                let h: Hello<'_> =
                    serde_json::from_str(sent.data.get()).map_err(|_| fail(400, "bad_request"))?;
                if h.pluxy != "page" || h.kind != "hello" {
                    return Err(fail(400, "bad_request"));
                }
                if !h.versions.is_some_and(hello_versions) {
                    return Err(Refusal::new(400, "hello_refused"));
                }
                serde_json::from_str(sent.data.get()).map_err(|_| fail(400, "bad_request"))?
            }
            "call" => {
                if sent.data.get().len() > MAX_CALL {
                    return Err(fail(413, "too_large"));
                }
                let package = &w.binding.extension;
                let package_waiting: usize = self
                    .open
                    .values()
                    .filter(|p| &p.binding.extension == package)
                    .map(|p| p.document.waiting.len())
                    .sum();
                if d.waiting.len() >= MAX_WAITING
                    || d.recent
                        .iter()
                        .filter(|t| now.duration_since(**t) < Duration::from_secs(1))
                        .count()
                        >= MAX_RATE
                    || package_waiting >= MAX_PACKAGE_WAITING
                    || self.packages[package]
                        .recent
                        .iter()
                        .filter(|t| now.duration_since(**t) < Duration::from_secs(1))
                        .count()
                        >= MAX_PACKAGE_RATE
                {
                    return Err(fail(429, "rate_limited"));
                }
                let c: Call =
                    serde_json::from_str(sent.data.get()).map_err(|_| fail(400, "bad_request"))?;
                let params_ok = match c.method.as_str() {
                    "theme.get" | "config.get" => c.params.is_null(),
                    "config.set" => c.params.as_object().is_some_and(|p| {
                        p.len() == 1 && p.get("values").is_some_and(Value::is_object)
                    }),
                    _ => return Err(fail(400, "unknown_method")),
                };
                let data: Value =
                    serde_json::from_str(sent.data.get()).map_err(|_| fail(400, "bad_request"))?;
                if !data.as_object().is_some_and(|o| {
                    o.len() == 4
                        && ["v", "id", "method", "params"]
                            .iter()
                            .all(|k| o.contains_key(*k))
                }) || c.v != 0
                    || !valid_id(&c.id)
                    || !params_ok
                    || !d.welcome
                    || d.waiting.contains(&c.id)
                {
                    return Err(fail(400, "bad_request"));
                }
                data
            }
            "closed" => {
                if sent.data.get().trim() != "null" {
                    return Err(fail(400, "bad_request"));
                }
                Value::Null
            }
            _ => return Err(fail(400, "bad_request")),
        };
        Ok(ToWindow {
            label: label.into(),
            generation: d.generation.clone(),
            kind: sent.kind,
            data,
        })
    }
    fn admit(&mut self, label: &str, sent: Sent, now: Instant) -> Result<ToWindow, Refusal> {
        let msg = self.refusal(label, sent, now)?;
        let w = self.open.get_mut(label).unwrap();
        match msg.kind.as_str() {
            "hello" => w.document.hello = true,
            "call" => {
                expire(&mut w.document.recent, now);
                w.document.recent.push_back(now);
                w.document
                    .waiting
                    .insert(msg.data["id"].as_str().unwrap().into());
                let p = self.packages.get_mut(&w.binding.extension).unwrap();
                expire(&mut p.recent, now);
                p.recent.push_back(now);
            }
            "closed" => {
                w.document.closed = true;
                // This message itself is forwarded; no second native notification is needed.
                w.document.closed_notified = true;
                w.document.waiting.clear();
                w.document.replies.clear();
            }
            _ => unreachable!(),
        }
        Ok(msg)
    }
    /// Reserve a bounded reply slot. A native eval callback releases it after consumption;
    /// an unresponsive renderer cannot accumulate an unbounded queue of eval scripts.
    pub fn prepare_delivery(
        &mut self,
        label: &str,
        generation: &str,
        msg: &Value,
    ) -> Result<Option<(String, u64)>, String> {
        let w = self.open.get(label).ok_or("unbound page")?;
        let d = &w.document;
        if d.generation != generation || d.closed || !d.hello {
            return Err("stale page document".into());
        }
        let kind = msg
            .get("type")
            .and_then(Value::as_str)
            .ok_or("missing reply type")?;
        match kind {
            "welcome" | "refused" if !d.welcome && d.replies.is_empty() => {}
            "message" if d.welcome => {
                let data = &msg["data"];
                if let Some(id) = data.get("id") {
                    if !id
                        .as_str()
                        .is_some_and(|id| d.waiting.contains(id) && !d.answering.contains(id))
                    {
                        return Err("reply is not for a pending call".into());
                    }
                } else if data.get("event").and_then(Value::as_str) != Some("theme") {
                    return Err("unknown channel event".into());
                }
            }
            _ => return Err("unexpected page reply".into()),
        }
        let mut outgoing = msg.clone();
        if serde_json::to_vec(&outgoing)
            .map_err(|e| e.to_string())?
            .len()
            > MAX_REPLY
        {
            if let Some(id) = msg["data"]["id"].as_str() {
                outgoing = call_error(id, "too_large");
            } else {
                return Err("the reply is too large".into());
            }
        }
        let theme = outgoing["data"]["event"] == "theme";
        if theme && d.theme_active {
            self.open.get_mut(label).unwrap().document.theme_pending = Some(outgoing);
            return Ok(None);
        }
        let text = serde_json::to_string(&outgoing).map_err(|e| e.to_string())?;
        let w = self.open.get_mut(label).unwrap();
        let ticket = w
            .document
            .next_reply
            .checked_add(1)
            .ok_or("reply tickets exhausted")?;
        w.document.next_reply = ticket;
        w.document.replies.insert(ticket);
        if theme {
            w.document.theme_active = true;
        }
        // The page may receive the welcome before the eval callback reaches this process.
        // It can call immediately; reservation still prevents another welcome from queuing.
        if kind == "welcome" {
            w.document.welcome = true;
        }
        if let Some(id) = msg["data"]["id"].as_str() {
            w.document.answering.insert(id.into());
        }
        let generation = serde_json::to_string(generation).unwrap();
        Ok(Some((
            format!("window.__pluxyDeliver({text},{generation})"),
            ticket,
        )))
    }
    pub fn finish_delivery(
        &mut self,
        label: &str,
        generation: &str,
        msg: &Value,
        ticket: u64,
        delivered: bool,
    ) -> Option<Value> {
        let w = self.open.get_mut(label)?;
        if w.document.generation != generation || !w.document.replies.remove(&ticket) {
            return None;
        }
        if let Some(id) = msg["data"]["id"].as_str() {
            w.document.answering.remove(id);
        }
        if !delivered {
            if msg["type"] == "welcome" {
                w.document.welcome = false;
            }
            if let Some(id) = msg["data"]["id"].as_str() {
                if msg["data"]["error"]["code"] != "unavailable" {
                    return Some(call_error(id, "unavailable"));
                }
                // A destroyed/unresponsive renderer cannot receive even an error. Close its
                // native channel rather than leave calls permanently occupying slots.
                w.document.closed = true;
                w.document.waiting.clear();
            }
            w.document.theme_active = false;
            return w.document.theme_pending.take();
        }
        match msg["type"].as_str() {
            Some("welcome") => w.document.welcome = true,
            Some("refused") => w.document.closed = true,
            Some("message") => {
                if let Some(id) = msg["data"]["id"].as_str() {
                    w.document.waiting.remove(id);
                }
            }
            _ => {}
        }
        if msg["data"]["event"] == "theme" {
            w.document.theme_active = false;
            return w.document.theme_pending.take();
        }
        None
    }
}
fn call_error(id: &str, code: &str) -> Value {
    json!({"type":"message","data":{"v":0,"id":id,"error":{"code":code,"message":"native page delivery failed"}}})
}
fn valid_id(id: &str) -> bool {
    !id.is_empty() && id.encode_utf16().count() <= 64
}
#[derive(Deserialize)]
struct Identity<'a> {
    #[serde(borrow)]
    id: &'a RawValue,
}
fn call_id(data: &str) -> Option<String> {
    let identity: Identity<'_> = serde_json::from_str(data).ok()?;
    // Every UTF-16 code unit can use six bytes as a JSON escape. Reject a larger id
    // before allocating it, even while identifying an oversized transport refusal.
    if identity.id.get().len() > 64 * 6 + 2 {
        return None;
    }
    let id: String = serde_json::from_str(identity.id.get()).ok()?;
    valid_id(&id).then_some(id)
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Sent {
    generation: String,
    #[serde(rename = "type")]
    kind: String,
    data: Box<RawValue>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Call {
    v: u8,
    id: String,
    method: String,
    params: Value,
}
#[derive(Deserialize)]
struct Hello<'a> {
    pluxy: String,
    #[serde(rename = "type")]
    kind: String,
    #[serde(borrow)]
    versions: Option<&'a RawValue>,
}
fn hello_versions(raw: &RawValue) -> bool {
    // Borrow array items and stop at the seventeenth, before building any Value or Vec.
    struct Versions;
    impl<'de> serde::de::Visitor<'de> for Versions {
        type Value = bool;
        fn expecting(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
            f.write_str("at most sixteen channel versions")
        }
        fn visit_seq<A: serde::de::SeqAccess<'de>>(self, mut seq: A) -> Result<bool, A::Error> {
            let mut compatible = false;
            for i in 0..=16 {
                let Some(item) = seq.next_element::<&RawValue>()? else {
                    return Ok(compatible);
                };
                if i == 16 {
                    return Err(serde::de::Error::custom("too many versions"));
                }
                compatible |= item.get().len() <= 24
                    && serde_json::from_str::<f64>(item.get()).is_ok_and(|v| v == 0.0);
            }
            Ok(false)
        }
    }
    serde::Deserializer::deserialize_seq(
        &mut serde_json::Deserializer::from_str(raw.get()),
        Versions,
    )
    .unwrap_or(false)
}
#[derive(Clone, Debug, Serialize)]
pub struct ToWindow {
    pub label: String,
    pub generation: String,
    #[serde(rename = "type")]
    pub kind: String,
    pub data: Value,
}
#[derive(Debug, PartialEq)]
pub struct Served {
    pub status: u16,
    pub kind: &'static str,
    pub body: Vec<u8>,
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
struct Refusal {
    status: u16,
    code: &'static str,
    id: Option<String>,
}
impl Refusal {
    fn new(status: u16, code: &'static str) -> Self {
        Self {
            status,
            code,
            id: None,
        }
    }
    fn served(self, csp: String) -> Served {
        if self.code == "hello_refused" {
            return served(
                self.status,
                "application/json",
                json!({"pluxy":"host","type":"refused","reason":"no channel version in common"})
                    .to_string(),
                csp,
            );
        }
        served(self.status, "application/json", json!({"v":0,"id":self.id,"error":{"code":self.code,"message":"the native page transport refused this message"}}).to_string(), csp)
    }
}

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

/// The native scheme handler. Its caller supplies the webview label, never request data.
pub fn serve(
    pages: &mut Pages,
    label: &str,
    method: &str,
    path: &str,
    body: &[u8],
) -> (Served, Option<ToWindow>) {
    serve_at(pages, label, method, path, body, Instant::now())
}
fn serve_at(
    pages: &mut Pages,
    label: &str,
    method: &str,
    path: &str,
    body: &[u8],
    now: Instant,
) -> (Served, Option<ToWindow>) {
    let Some(w) = pages.open.get(label) else {
        return (
            Refusal {
                status: 403,
                code: "bad_request",
                id: body_id(body),
            }
            .served("default-src 'none'".into()),
            None,
        );
    };
    let csp = format!("default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-src {}/; base-uri 'none'; form-action 'none'", w.binding.pages_origin);
    #[cfg(feature = "native-test")]
    let csp = if pages.ingress_fixture {
        csp.replace("frame-src ", "frame-src pluxy-page: ")
            .replace("script-src 'self'", "script-src pluxy-page:")
            .replace("connect-src 'self'", "connect-src pluxy-page:")
    } else {
        csp
    };
    let fail = |status, code| {
        (
            Refusal {
                status,
                code,
                id: body_id(body),
            }
            .served(csp.clone()),
            None,
        )
    };
    match (method, path) {
        ("GET", "/") => (served(200, "text/html; charset=utf-8", INDEX, csp), None),
        ("GET", "/frame.js") => (
            served(200, "text/javascript; charset=utf-8", SCRIPT, csp),
            None,
        ),
        ("GET", "/frame.css") => (served(200, "text/css; charset=utf-8", STYLE, csp), None),
        ("GET", "/frame") => {
            let info = json!({"url": w.binding.url.as_str(), "title": w.binding.title, "generation": w.document.generation});
            #[cfg(feature = "native-test")]
            let info = if pages.ingress_fixture {
                json!({"url":"pluxy-page://localhost/ingress-test", "title":w.binding.title, "generation":w.document.generation})
            } else {
                info
            };
            (served(200, "application/json", info.to_string(), csp), None)
        }
        #[cfg(feature = "native-test")]
        ("GET", "/ingress-test.js") => (
            served(
                200,
                "text/javascript",
                include_str!("../../page/ingress-test.js"),
                csp,
            ),
            None,
        ),
        #[cfg(feature = "native-test")]
        ("GET", "/ingress-test") => (
            served(
                200,
                "text/html",
                "<script defer src=ingress-test.js></script>",
                csp,
            ),
            None,
        ),
        ("POST", "/send") => {
            if body.len() > MAX_SEND {
                return fail(413, "too_large");
            }
            let Ok(sent) = serde_json::from_slice::<Sent>(body) else {
                return fail(400, "bad_request");
            };
            match pages.admit(label, sent, now) {
                Ok(msg) => (served(204, "text/plain", "", csp), Some(msg)),
                Err(e) => {
                    if e.code == "hello_refused" {
                        let d = &mut pages.open.get_mut(label).unwrap().document;
                        d.hello = true;
                        d.closed = true;
                    }
                    (e.served(csp), None)
                }
            }
        }
        _ => fail(404, "bad_request"),
    }
}
#[derive(Deserialize)]
struct BodyIdentity<'a> {
    #[serde(borrow)]
    data: &'a RawValue,
}
fn body_id(body: &[u8]) -> Option<String> {
    if body.len() > MAX_SEND {
        return None;
    }
    let body: BodyIdentity<'_> = serde_json::from_slice(body).ok()?;
    call_id(body.data.get())
}

#[cfg(test)]
mod tests {
    use super::*;
    fn binding(extension: &str) -> Binding {
        Binding {
            extension: extension.into(),
            page: "usage".into(),
            title: "Usage".into(),
            url: Url::parse("http://localhost:8401/test/alpha/hash/index.html").unwrap(),
            pages_origin: "http://localhost:8401".into(),
        }
    }
    fn request(
        p: &mut Pages,
        label: &str,
        kind: &str,
        data: Value,
        now: Instant,
    ) -> (Served, Option<ToWindow>) {
        let generation = p
            .open
            .get(label)
            .map(|w| w.document.generation.clone())
            .unwrap_or_default();
        let body =
            serde_json::to_vec(&json!({"generation":generation,"type":kind,"data":data})).unwrap();
        serve_at(p, label, "POST", "/send", &body, now)
    }
    fn call(id: &str, method: &str, params: Value) -> Value {
        json!({"v":0,"id":id,"method":method,"params":params})
    }
    fn hello(p: &mut Pages, label: &str, now: Instant) {
        assert_eq!(
            request(
                p,
                label,
                "hello",
                json!({"pluxy":"page","type":"hello","v":0,"versions":[0]}),
                now
            )
            .0
            .status,
            204
        );
        deliver(p, label, json!({"type":"welcome","data":{"v":0}}), true);
    }
    fn page(p: &mut Pages, ext: &str, now: Instant) -> String {
        let label = p.bind(binding(ext)).unwrap();
        p.begin_document(&label);
        hello(p, &label, now);
        label
    }
    fn deliver(p: &mut Pages, label: &str, msg: Value, ok: bool) {
        let generation = p.open[label].document.generation.clone();
        let (_, ticket) = p
            .prepare_delivery(label, &generation, &msg)
            .unwrap()
            .unwrap();
        let next = p.finish_delivery(label, &generation, &msg, ticket, ok);
        if let Some(next) = next {
            deliver(p, label, next, true);
        }
    }
    fn answer(p: &mut Pages, label: &str, id: &str) {
        deliver(
            p,
            label,
            json!({"type":"message","data":{"v":0,"id":id,"result":{}}}),
            true,
        );
    }
    fn refused(r: (Served, Option<ToWindow>), status: u16, code: &str, id: Option<&str>) {
        assert_eq!(r.0.status, status);
        assert!(r.1.is_none());
        let v: Value = serde_json::from_slice(&r.0.body).unwrap();
        assert_eq!(v["error"]["code"], code);
        assert_eq!(v["id"].as_str(), id);
    }
    #[test]
    fn schemas_methods_and_call_size() {
        let mut p = Pages::default();
        let now = Instant::now();
        let label = page(&mut p, "test/alpha", now);
        for bad in [
            json!({"v":0,"id":"bad","method":"theme.get","params":null,"extra":1}),
            call("bad", "config.get", json!({})),
            call("bad", "config.set", json!({"values":[],"extra":1})),
            call("bad", "theme.get", json!([])),
            json!({"v":1,"id":"bad","method":"theme.get","params":null}),
        ] {
            refused(
                request(&mut p, &label, "call", bad, now),
                400,
                "bad_request",
                Some("bad"),
            );
        }
        refused(
            request(
                &mut p,
                &label,
                "call",
                call("bad", "missing", Value::Null),
                now,
            ),
            400,
            "unknown_method",
            Some("bad"),
        );
        refused(
            request(
                &mut p,
                &label,
                "call",
                call(
                    "big",
                    "config.set",
                    json!({"values":{"x":"x".repeat(MAX_CALL)}}),
                ),
                now,
            ),
            413,
            "too_large",
            Some("big"),
        );
        assert_eq!(
            request(
                &mut p,
                &label,
                "call",
                call("valid", "config.get", Value::Null),
                now
            )
            .0
            .status,
            204
        );
        assert_eq!(p.open[&label].document.waiting.len(), 1);
    }
    #[test]
    fn transport_exact_boundary_preserves_ids() {
        let mut p = Pages::default();
        let now = Instant::now();
        let label = page(&mut p, "test/alpha", now);
        let base=serde_json::to_vec(&json!({"generation":p.open[&label].document.generation,"type":"call","data":call("size","theme.get",Value::Null)})).unwrap();
        let mut body = base.clone();
        body.resize(MAX_SEND, b' ');
        assert_eq!(serve(&mut p, &label, "POST", "/send", &body).0.status, 204);
        answer(&mut p, &label, "size");
        body.push(b' ');
        refused(
            serve(&mut p, &label, "POST", "/send", &body),
            413,
            "too_large",
            None,
        );
        assert_eq!(serve(&mut p, &label, "POST", "/send", &base).0.status, 204);
    }
    #[test]
    fn envelope_boundary_and_utf16_ids() {
        let mut p = Pages::default();
        let now = Instant::now();
        let label = page(&mut p, "test/alpha", now);
        let mut c = call("size", "config.set", json!({"values":{"x":""}}));
        let overhead = serde_json::to_vec(&c).unwrap().len();
        c["params"]["values"]["x"] = json!("x".repeat(MAX_CALL - overhead));
        assert_eq!(
            request(&mut p, &label, "call", c.clone(), now).0.status,
            204
        );
        answer(&mut p, &label, "size");
        c["params"]["values"]["x"] = json!("x".repeat(MAX_CALL - overhead + 1));
        refused(
            request(&mut p, &label, "call", c, now),
            413,
            "too_large",
            Some("size"),
        );
        let id = "😀".repeat(32);
        assert_eq!(
            request(
                &mut p,
                &label,
                "call",
                call(&id, "theme.get", Value::Null),
                now
            )
            .0
            .status,
            204
        );
        refused(
            request(
                &mut p,
                &label,
                "call",
                call(&"😀".repeat(33), "theme.get", Value::Null),
                now,
            ),
            400,
            "bad_request",
            None,
        );
    }
    #[test]
    fn rolling_page_rate_includes_answered_calls() {
        let mut p = Pages::default();
        let now = Instant::now();
        let label = page(&mut p, "test/alpha", now);
        for i in 0..MAX_RATE {
            let id = i.to_string();
            assert_eq!(
                request(
                    &mut p,
                    &label,
                    "call",
                    call(&id, "theme.get", Value::Null),
                    now
                )
                .0
                .status,
                204
            );
            answer(&mut p, &label, &id);
        }
        refused(
            request(
                &mut p,
                &label,
                "call",
                call("over", "theme.get", Value::Null),
                now + Duration::from_millis(999),
            ),
            429,
            "rate_limited",
            Some("over"),
        );
        assert_eq!(
            request(
                &mut p,
                &label,
                "call",
                call("next", "theme.get", Value::Null),
                now + Duration::from_secs(1)
            )
            .0
            .status,
            204
        );
    }
    #[test]
    fn in_flight_ends_only_on_delivery_or_close() {
        let mut p = Pages::default();
        let now = Instant::now();
        let label = page(&mut p, "test/alpha", now);
        for i in 0..MAX_WAITING {
            assert_eq!(
                request(
                    &mut p,
                    &label,
                    "call",
                    call(&i.to_string(), "config.get", Value::Null),
                    now
                )
                .0
                .status,
                204
            );
        }
        refused(
            request(
                &mut p,
                &label,
                "call",
                call("over", "config.get", Value::Null),
                now,
            ),
            429,
            "rate_limited",
            Some("over"),
        );
        let generation = p.open[&label].document.generation.clone();
        let reply = json!({"type":"message","data":{"v":0,"id":"0","result":{}}});
        let (_, ticket) = p
            .prepare_delivery(&label, &generation, &reply)
            .unwrap()
            .unwrap();
        let error = p
            .finish_delivery(&label, &generation, &reply, ticket, false)
            .unwrap();
        refused(
            request(
                &mut p,
                &label,
                "call",
                call("over", "config.get", Value::Null),
                now,
            ),
            429,
            "rate_limited",
            Some("over"),
        );
        deliver(&mut p, &label, error, true);
        assert_eq!(
            request(
                &mut p,
                &label,
                "call",
                call("next", "config.get", Value::Null),
                now
            )
            .0
            .status,
            204
        );
        p.unbind(&label);
        let other = page(&mut p, "test/alpha", now);
        assert_eq!(
            request(
                &mut p,
                &other,
                "call",
                call("control", "config.get", Value::Null),
                now
            )
            .0
            .status,
            204
        );
    }
    #[test]
    fn package_budgets_span_pages_and_reopen() {
        let mut p = Pages::default();
        let now = Instant::now();
        let labels: Vec<_> = (0..4).map(|_| page(&mut p, "test/alpha", now)).collect();
        for label in &labels[..2] {
            for i in 0..8 {
                assert_eq!(
                    request(
                        &mut p,
                        label,
                        "call",
                        call(&i.to_string(), "config.get", Value::Null),
                        now
                    )
                    .0
                    .status,
                    204
                );
            }
        }
        refused(
            request(
                &mut p,
                &labels[2],
                "call",
                call("over", "config.get", Value::Null),
                now,
            ),
            429,
            "rate_limited",
            Some("over"),
        );
        answer(&mut p, &labels[0], "0");
        assert_eq!(
            request(
                &mut p,
                &labels[2],
                "call",
                call("control", "config.get", Value::Null),
                now
            )
            .0
            .status,
            204
        );
        for label in labels {
            p.unbind(&label);
        }
        for _ in 0..(MAX_PACKAGE_RATE - 17) {
            let label = page(&mut p, "test/alpha", now);
            assert_eq!(
                request(
                    &mut p,
                    &label,
                    "call",
                    call("one", "theme.get", Value::Null),
                    now
                )
                .0
                .status,
                204
            );
            p.unbind(&label);
        }
        let label = page(&mut p, "test/alpha", now);
        refused(
            request(
                &mut p,
                &label,
                "call",
                call("over", "theme.get", Value::Null),
                now,
            ),
            429,
            "rate_limited",
            Some("over"),
        );
        let other = page(&mut p, "test/beta", now);
        assert_eq!(
            request(
                &mut p,
                &other,
                "call",
                call("control", "theme.get", Value::Null),
                now
            )
            .0
            .status,
            204
        );
    }
    #[test]
    fn hello_generation_and_unbound_routes() {
        let mut p = Pages::default();
        let now = Instant::now();
        let label = page(&mut p, "test/alpha", now);
        refused(
            request(
                &mut p,
                &label,
                "hello",
                json!({"pluxy":"page","type":"hello","v":0,"versions":[0]}),
                now,
            ),
            409,
            "bad_request",
            None,
        );
        let body=serde_json::to_vec(&json!({"generation":"wrong","type":"call","data":call("wrong","theme.get",Value::Null)})).unwrap();
        refused(
            serve(&mut p, &label, "POST", "/send", &body),
            409,
            "bad_request",
            Some("wrong"),
        );
        assert_eq!(
            request(
                &mut p,
                &label,
                "call",
                call("control", "theme.get", Value::Null),
                now
            )
            .0
            .status,
            204
        );
        let generation = p.open[&label].document.generation.clone();
        p.begin_document(&label);
        assert_ne!(generation, p.open[&label].document.generation);
        refused(
            request(
                &mut p,
                &label,
                "call",
                call("closed", "theme.get", Value::Null),
                now,
            ),
            409,
            "bad_request",
            Some("closed"),
        );
        assert!(p
            .prepare_delivery(
                &label,
                &generation,
                &json!({"type":"message","data":{"id":"control"}})
            )
            .is_err());
        p.unbind(&label);
        assert_eq!(serve(&mut p, &label, "GET", "/frame", b"").0.status, 403);
        assert_eq!(serve(&mut p, "main", "GET", "/frame", b"").0.status, 403);
        let next = page(&mut p, "test/alpha", now);
        assert_eq!(
            request(
                &mut p,
                &next,
                "call",
                call("control", "theme.get", Value::Null),
                now
            )
            .0
            .status,
            204
        );
    }
    #[test]
    fn large_replies_errors_and_coalesced_themes() {
        let mut p = Pages::default();
        let now = Instant::now();
        let label = page(&mut p, "test/alpha", now);
        let gen = p.open[&label].document.generation.clone();
        for i in 0..8 {
            assert_eq!(
                request(
                    &mut p,
                    &label,
                    "call",
                    call(&i.to_string(), "config.get", Value::Null),
                    now
                )
                .0
                .status,
                204
            );
        }
        let cjk: serde_json::Map<String, Value> = (0..50)
            .map(|i| (format!("field{i}"), json!("界".repeat(4096))))
            .collect();
        let cjk = json!({"type":"message","data":{"v":0,"id":"3","result":{"values":cjk}}});
        let (js, ticket) = p.prepare_delivery(&label, &gen, &cjk).unwrap().unwrap();
        assert!(js.len() > 600_000);
        assert!(!js.contains("too_large"));
        p.finish_delivery(&label, &gen, &cjk, ticket, true);
        let values: serde_json::Map<String, Value> = (0..50)
            .map(|i| (format!("field{i}"), json!("\u{1}".repeat(4096))))
            .collect();
        let large = json!({"type":"message","data":{"v":0,"id":"0","result":{"values":values}}});
        let (js, ticket) = p.prepare_delivery(&label, &gen, &large).unwrap().unwrap();
        assert!(js.len() > 1_200_000);
        p.finish_delivery(&label, &gen, &large, ticket, true);
        assert!(!p.open[&label].document.waiting.contains("0"));
        let too_big =
            json!({"type":"message","data":{"v":0,"id":"1","result":"x".repeat(MAX_REPLY)}});
        let (js, ticket) = p.prepare_delivery(&label, &gen, &too_big).unwrap().unwrap();
        assert!(js.contains("too_large"));
        p.finish_delivery(&label, &gen, &too_big, ticket, true);
        assert!(!p.open[&label].document.waiting.contains("1"));
        let reply = json!({"type":"message","data":{"v":0,"id":"2","result":{}}});
        let (_, ticket) = p.prepare_delivery(&label, &gen, &reply).unwrap().unwrap();
        let error = p
            .finish_delivery(&label, &gen, &reply, ticket, false)
            .unwrap();
        assert_eq!(error["data"]["error"]["code"], "unavailable");
        assert_eq!(error["data"]["id"], "2");
        deliver(&mut p, &label, error, true);
        assert!(!p.open[&label].document.waiting.contains("2"));
        let theme = json!({"type":"message","data":{"v":0,"event":"theme","data":0}});
        let (_, ticket) = p.prepare_delivery(&label, &gen, &theme).unwrap().unwrap();
        for i in 1..100 {
            assert!(p
                .prepare_delivery(
                    &label,
                    &gen,
                    &json!({"type":"message","data":{"v":0,"event":"theme","data":i}})
                )
                .unwrap()
                .is_none());
        }
        assert_eq!(p.open[&label].document.replies.len(), 1);
        let latest = p
            .finish_delivery(&label, &gen, &theme, ticket, true)
            .unwrap();
        assert_eq!(latest["data"]["data"], 99);
        deliver(&mut p, &label, latest, true);
        assert!(!p.open[&label].document.theme_active);
        assert_eq!(
            request(
                &mut p,
                &label,
                "call",
                call("control", "theme.get", Value::Null),
                now
            )
            .0
            .status,
            204
        );
    }
    #[test]
    fn raw_call_size_and_hello_parity() {
        let mut p = Pages::default();
        let now = Instant::now();
        let label = page(&mut p, "test/alpha", now);
        let gen = p.open[&label].document.generation.clone();
        let data = format!(
            r#"{{"v":0,"id":"raw","method":"config.set","params":{{"values":{{"n":100000000000000000000,"padding":"{}"}}}}}}"#,
            "x".repeat(MAX_CALL)
        );
        assert!(data.len() > MAX_CALL);
        let body = format!(r#"{{"generation":"{gen}","type":"call","data":{data}}}"#);
        refused(
            serve(&mut p, &label, "POST", "/send", body.as_bytes()),
            413,
            "too_large",
            Some("raw"),
        );
        let label = p.bind(binding("test/beta")).unwrap();
        p.begin_document(&label);
        let r = request(
            &mut p,
            &label,
            "hello",
            json!({"pluxy":"page","type":"hello","versions":vec![0;17]}),
            now,
        );
        assert_eq!(r.0.status, 400);
        assert!(r.1.is_none());
        assert_eq!(
            serde_json::from_slice::<Value>(&r.0.body).unwrap()["type"],
            "refused"
        );
        let label = p.bind(binding("test/gamma")).unwrap();
        p.begin_document(&label);
        assert_eq!(
            request(
                &mut p,
                &label,
                "hello",
                json!({"pluxy":"page","type":"hello","versions":["future",0],"extra":true}),
                now
            )
            .0
            .status,
            204
        );
    }
    #[test]
    fn missing_duplicate_fields_and_unknown_transport_shapes() {
        let mut p = Pages::default();
        let now = Instant::now();
        let label = page(&mut p, "test/alpha", now);
        for data in [
            json!({"v":0,"id":"bad","method":"theme.get"}),
            json!({"id":"bad","method":"theme.get","params":null}),
            json!({"v":0,"id":"bad","params":null}),
        ] {
            refused(
                request(&mut p, &label, "call", data, now),
                400,
                "bad_request",
                Some("bad"),
            );
        }
        let generation = &p.open[&label].document.generation;
        let body = format!(
            r#"{{"generation":"{generation}","type":"call","data":{{"v":0,"id":"duplicate","method":"theme.get","method":"config.get","params":null}}}}"#
        );
        refused(
            serve(&mut p, &label, "POST", "/send", body.as_bytes()),
            400,
            "bad_request",
            Some("duplicate"),
        );
        assert_eq!(
            request(
                &mut p,
                &label,
                "call",
                call("control", "theme.get", Value::Null),
                now
            )
            .0
            .status,
            204
        );
    }
    #[test]
    fn retained_package_budgets_and_protocol_handshakes_are_bounded() {
        let mut p = Pages::default();
        let now = Instant::now();
        for i in 0..MAX_PACKAGE_BUDGETS {
            let label = page(&mut p, &format!("test/p{i}"), now);
            assert_eq!(
                request(
                    &mut p,
                    &label,
                    "call",
                    call("one", "theme.get", Value::Null),
                    now
                )
                .0
                .status,
                204
            );
            p.unbind(&label);
        }
        assert!(p.bind(binding("test/overflow")).is_err());
        let label = page(&mut p, "test/p0", now);
        assert_eq!(
            request(
                &mut p,
                &label,
                "call",
                call("control", "theme.get", Value::Null),
                now
            )
            .0
            .status,
            204
        );
        let mut p = Pages::default();
        let label = p.bind(binding("test/alpha")).unwrap();
        p.begin_document(&label);
        refused(
            request(
                &mut p,
                &label,
                "call",
                call("early", "theme.get", Value::Null),
                now,
            ),
            400,
            "bad_request",
            Some("early"),
        );
        refused(
            request(
                &mut p,
                &label,
                "hello",
                json!({"pluxy":"wrong","type":"hello","versions":[]}),
                now,
            ),
            400,
            "bad_request",
            None,
        );
        hello(&mut p, &label, now);
        assert_eq!(
            request(
                &mut p,
                &label,
                "call",
                call("control", "theme.get", Value::Null),
                now
            )
            .0
            .status,
            204
        );
        assert_eq!(
            request(&mut p, &label, "closed", Value::Null, now).0.status,
            204
        );
        refused(
            request(
                &mut p,
                &label,
                "call",
                call("late", "theme.get", Value::Null),
                now,
            ),
            409,
            "bad_request",
            Some("late"),
        );
        let other = page(&mut p, "test/alpha", now);
        assert_eq!(
            request(
                &mut p,
                &other,
                "call",
                call("control", "theme.get", Value::Null),
                now
            )
            .0
            .status,
            204
        );
    }
    #[test]
    fn native_frame_navigation_closes_before_load_and_nested_navigation_fails_closed() {
        let mut p = Pages::default();
        let now = Instant::now();
        let label = p.bind(binding("test/alpha")).unwrap();
        p.begin_document(&label);
        let url = p.get(&label).unwrap().url.clone();
        p.frame_navigation(&label, &url);
        hello(&mut p, &label, now);
        assert_eq!(
            request(
                &mut p,
                &label,
                "call",
                call("control", "theme.get", Value::Null),
                now
            )
            .0
            .status,
            204
        );
        p.frame_navigation(&label, &url);
        assert!(p.document_closed(&label));
        refused(
            request(
                &mut p,
                &label,
                "call",
                call("late", "theme.get", Value::Null),
                now,
            ),
            409,
            "bad_request",
            Some("late"),
        );
        let next = page(&mut p, "test/beta", now);
        p.frame_navigation(&next, &Url::parse("http://localhost:8401/nested").unwrap());
        assert!(p.document_closed(&next));
    }
    #[test]
    fn normalized_origins_fragments_and_single_close_notification() {
        let now = Instant::now();
        let mut p = Pages::default();
        for origin in ["https://example.com:443", "https://例え.テスト:443"] {
            let mut b = binding("test/alpha");
            b.pages_origin = origin.into();
            b.url = Url::parse(&format!("{origin}/index.html")).unwrap();
            let url = b.url.clone();
            let label = p.bind(b).unwrap();
            p.begin_document(&label);
            p.frame_navigation(&label, &url);
            hello(&mut p, &label, now);
            let mut hash = url.clone();
            hash.set_fragment(Some("section"));
            p.frame_navigation(&label, &hash);
            assert!(!p.document_closed(&label));
            assert!(p.take_closed(&label).is_none());
            hash.set_fragment(None);
            p.frame_navigation(&label, &hash);
            assert!(!p.document_closed(&label));
            let mut next = url.clone();
            next.set_path("/next.html");
            p.frame_navigation(&label, &next);
            let closed = p.take_closed(&label).unwrap();
            assert_eq!(closed.kind, "closed");
            assert_eq!(closed.label, label);
            assert!(p.take_closed(&label).is_none());
            p.frame_navigation(&label, &next);
            assert!(p.take_closed(&label).is_none());
            p.unbind(&label);
        }
        let label = page(&mut p, "test/beta", now);
        assert_eq!(
            request(
                &mut p,
                &label,
                "call",
                call("one", "theme.get", Value::Null),
                now
            )
            .0
            .status,
            204
        );
        let gen = p.open[&label].document.generation.clone();
        let error = call_error("one", "unavailable");
        let (_, ticket) = p.prepare_delivery(&label, &gen, &error).unwrap().unwrap();
        p.finish_delivery(&label, &gen, &error, ticket, false);
        assert_eq!(p.take_closed(&label).unwrap().generation, gen);
        assert!(p.take_closed(&label).is_none());
    }
    #[test]
    fn hello_arrays_are_refused_before_window_forwarding() {
        let mut p = Pages::default();
        let now = Instant::now();
        let label = p.bind(binding("test/alpha")).unwrap();
        p.begin_document(&label);
        refused(
            request(&mut p, &label, "hello", json!(["page", "hello", [0]]), now),
            400,
            "bad_request",
            None,
        );
        hello(&mut p, &label, now);
    }
    #[test]
    fn pages_packages_and_origins() {
        let mut p = Pages::default();
        for i in 0..MAX_OPEN {
            p.bind(binding(&format!("test/p{i}"))).unwrap();
        }
        assert!(p.bind(binding("test/extra")).is_err());
        p.clear();
        for _ in 0..MAX_PACKAGE_PAGES {
            p.bind(binding("test/alpha")).unwrap();
        }
        assert!(p.bind(binding("test/alpha")).is_err());
        assert!(p.bind(binding("test/beta")).is_ok());
        assert!(page_url("http://localhost:8401/a", "http://localhost:8401").is_ok());
        for url in [
            "http://localhost:8402/a",
            "https://localhost:8401/a",
            "file:///etc/passwd",
            "javascript:alert(1)",
        ] {
            assert!(page_url(url, "http://localhost:8401").is_err());
        }
    }
}
