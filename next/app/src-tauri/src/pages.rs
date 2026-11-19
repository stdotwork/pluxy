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

/// The page webviews' app document. Windows serves a custom scheme as `http://<scheme>.localhost`.
pub fn app_url() -> Url {
    #[cfg(any(windows, target_os = "android"))]
    let s = format!("http://{SCHEME}.localhost/");
    #[cfg(not(any(windows, target_os = "android")))]
    let s = format!("{SCHEME}://localhost/");
    Url::parse(&s).expect("the page scheme's URL")
}

/// The native test's ingress fixture, a document of the page scheme shown in the page's frame.
#[cfg(feature = "native-test")]
pub fn ingress_url() -> Url {
    app_url()
        .join("ingress-test")
        .expect("the ingress fixture's URL")
}

/// Whether url is the ingress fixture, with any query (its second document is `?second`).
#[cfg(feature = "native-test")]
pub fn is_ingress(url: &Url) -> bool {
    let base = ingress_url();
    url.scheme() == base.scheme() && url.host_str() == base.host_str() && url.path() == base.path()
}

#[derive(Clone, Debug, PartialEq)]
pub struct Binding {
    pub extension: String,
    pub page: String,
    pub title: String,
    pub url: Url,
    pub pages_origin: String,
    /// For a page of a package on this device: its release's folder on the asset scheme
    /// (`pluxy-app://localhost/<id>/<sha256>/`; `http://pluxy-app.localhost/<id>/<sha256>/` on
    /// Windows, `device::asset_base`), the only place the page's frame may load from.
    /// None for a page core lists, which loads from pages_origin.
    pub device: Option<String>,
}

impl Binding {
    /// Whether url is on the page's own site: its release's folder for a device page, the
    /// pages site's origin otherwise.
    pub fn on_site(&self, url: &Url) -> bool {
        match &self.device {
            Some(folder) => url.as_str().starts_with(folder.as_str()),
            None => Url::parse(&self.pages_origin).is_ok_and(|o| o.origin() == url.origin()),
        }
    }
    /// For a page of a package on this device: the package's ID and the digest of the release
    /// the page was opened from.
    pub fn device_release(&self) -> Option<(String, String)> {
        let folder = self.device.as_ref()?.strip_suffix('/')?;
        let id = self.extension.strip_prefix("device:")?;
        let (_, digest) = folder.rsplit_once('/')?;
        Some((id.into(), digest.into()))
    }
    /// The frame-src source of the page's wrapper document: the asset scheme as the webview serves
    /// it (`asset_source`) for a device page, whose folder the asset scheme enforces by serving a
    /// page webview only its own folder.
    fn frame_source(&self) -> String {
        match &self.device {
            Some(_) => asset_source(&crate::device::asset_base()),
            None => format!("{}/", self.pages_origin),
        }
    }
}

/// The CSP source that allows the asset scheme at base (`device::asset_base`): the scheme by name
/// where the webview serves it as a scheme of its own, its origin where it is served as http
/// (`http://pluxy-app.localhost` on Windows, which a `pluxy-app:` source does not allow: WebView2
/// would show its error page in the frame instead).
pub(crate) fn asset_source(base: &str) -> String {
    match Url::parse(base) {
        Ok(u) if u.scheme() == "http" || u.scheme() == "https" => u.origin().ascii_serialization(),
        Ok(u) => format!("{}:", u.scheme()),
        Err(_) => format!("{}:", crate::device::ASSET_SCHEME),
    }
}

/// The requests a page webview refuses outright, as a WebView2 filter (`page_frames.rs`): the
/// asset scheme's host over https. Where the asset scheme is served as `http://pluxy-app.localhost`
/// (Windows), its CSP source (`asset_source`) also allows that host over https, as CSP lets an
/// http source match its https form; the app serves nothing there (wry intercepts only the http
/// form), so such a request would reach whatever answers on that host's port.
pub fn refused_filter() -> String {
    format!("https://{}.*", crate::device::ASSET_SCHEME)
}

/// Whether a page webview refuses url outright (`refused_filter`).
pub fn refused_request(url: &str) -> bool {
    Url::parse(url).is_ok_and(|u| {
        u.scheme() == "https"
            && u.host_str().is_some_and(|h| {
                h.strip_prefix(crate::device::ASSET_SCHEME)
                    .is_some_and(|rest| rest.starts_with('.'))
            })
    })
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
impl Document {
    /// No more calls or answers: the page's document is gone.
    fn close(&mut self) {
        self.closed = true;
        self.waiting.clear();
        self.answering.clear();
        self.theme_pending = None;
    }
}
#[derive(Default)]
struct Package {
    recent: VecDeque<Instant>,
}
struct Page {
    binding: Binding,
    document: Document,
    previous_closed: Option<ToWindow>,
    /// Whether the app is told of the page's frame navigations (`frame_navigation`). WebKitGTK
    /// and WKWebView report them to the webview's navigation handler from the start; on Windows
    /// the app hooks WebView2's frame navigations once the webview exists (lib.rs `add_page`).
    frames_watched: bool,
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
                frames_watched: !cfg!(windows),
            },
        );
        Ok(label)
    }
    /// The app now sees page label's frame navigations: its frame may be shown.
    pub fn watch_frames(&mut self, label: &str) {
        if let Some(w) = self.open.get_mut(label) {
            w.frames_watched = true;
        }
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
    /// The labels of the open pages of package id on this device.
    pub fn device_labels(&self, id: &str) -> Vec<String> {
        let prefix = format!("{}{id}/", crate::device::asset_base());
        self.open
            .iter()
            .filter(|(_, w)| {
                w.binding
                    .device
                    .as_ref()
                    .is_some_and(|f| f.starts_with(&prefix))
            })
            .map(|(l, _)| l.clone())
            .collect()
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
    /// Where page label's channel is, for the native test: its document loaded, its hello
    /// admitted, the window's welcome on its way or delivered, closed, and the calls waiting.
    #[cfg(feature = "native-test")]
    pub fn channel_state(&self, label: &str) -> Value {
        self.open.get(label).map_or(Value::Null, |w| {
            let d = &w.document;
            json!({
                "loaded": d.loaded, "hello": d.hello, "welcome": d.welcome, "closed": d.closed,
                "waiting": d.waiting.len(),
            })
        })
    }
    /// Every platform reports subframe navigation: WebKitGTK and WKWebView to the webview's
    /// navigation handler, WebView2 as FrameNavigationStarting. Ignore fragment-only changes (a same-document
    /// navigation: the new address has a fragment and differs only in it), but close on a
    /// second document, including one that drops the fragment, or a nested frame loading another
    /// pages-site document. Close too when the app's own document comes after the frame's: on
    /// Linux and macOS the webview does not say whether the top or the frame navigates, and the
    /// page's document is gone either way.
    pub fn frame_navigation(&mut self, label: &str, url: &Url) {
        let Some(w) = self.open.get_mut(label) else {
            return;
        };
        #[cfg(feature = "native-test")]
        let test_frame = self.ingress_fixture && is_ingress(url);
        #[cfg(not(feature = "native-test"))]
        let test_frame = false;
        if !w.binding.on_site(url) && !test_frame {
            if *url == app_url() && w.document.frame_url.is_some() {
                w.document.close();
            }
            return;
        }
        if let Some(previous) = &w.document.frame_url {
            let mut before = previous.clone();
            before.set_fragment(None);
            let mut after = url.clone();
            after.set_fragment(None);
            if url.fragment().is_some() && before == after && previous.fragment() != url.fragment()
            {
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
            w.document.close();
        }
    }
    /// The engine replaced page label's frame document with an error page of its own (Windows,
    /// page_frames.rs): the page's document is gone.
    #[cfg_attr(not(windows), allow(dead_code))]
    pub fn frame_replaced(&mut self, label: &str) {
        if let Some(w) = self.open.get_mut(label) {
            w.document.close();
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
                // Access (access.rs) answers these natively, for a device page only: a page
                // core lists has no such methods.
                let device = w.binding.device.is_some();
                let params_ok = match c.method.as_str() {
                    "theme.get" | "config.get" => c.params.is_null(),
                    "resources.status" if device => c.params.is_null(),
                    "resources.read" | "resources.list" if device => c.params.is_object(),
                    "config.set" => c.params.as_object().is_some_and(|p| {
                        p.len() == 1 && p.get("values").is_some_and(Value::is_object)
                    }),
                    // A native operation, which the router answers (`route.rs`).
                    "native.call" => c.params.as_object().is_some_and(|p| {
                        p.len() == 3
                            && p.get("package").is_some_and(Value::is_string)
                            && p.get("op").is_some_and(Value::is_string)
                            && p.get("input").is_some_and(Value::is_object)
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
            "hello" => {
                w.document.hello = true;
                if w.document.frame_url.is_none() {
                    #[cfg(feature = "native-test")]
                    let url = if self.ingress_fixture {
                        ingress_url()
                    } else {
                        w.binding.url.clone()
                    };
                    #[cfg(not(feature = "native-test"))]
                    let url = w.binding.url.clone();
                    w.document.frame_url = Some(url);
                }
            }
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
    let csp = format!("default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-src {}; base-uri 'none'; form-action 'none'", w.binding.frame_source());
    #[cfg(feature = "native-test")]
    let csp = if pages.ingress_fixture {
        // The page scheme as a source: by scheme, or on Windows by its http origin.
        #[cfg(not(windows))]
        let own = format!("{SCHEME}:");
        #[cfg(windows)]
        let own = app_url().origin().ascii_serialization();
        csp.replace("frame-src ", &format!("frame-src {own} "))
            .replace("script-src 'self'", &format!("script-src {own}"))
            .replace("connect-src 'self'", &format!("connect-src {own}"))
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
            // The frame's address only once the app sees the frame's navigations (frame.js waits).
            if !w.frames_watched {
                return fail(503, "unavailable");
            }
            let info = json!({"url": w.binding.url.as_str(), "title": w.binding.title, "generation": w.document.generation});
            #[cfg(feature = "native-test")]
            let info = if pages.ingress_fixture {
                json!({"url":ingress_url().as_str(), "title":w.binding.title, "generation":w.document.generation})
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
            device: None,
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
    fn the_app_document_after_the_frame_closes_the_channel() {
        let mut p = Pages::default();
        let label = p.bind(binding("test/alpha")).unwrap();
        // The top's first document is the app's own: nothing to close yet.
        p.frame_navigation(&label, &app_url());
        p.begin_document(&label);
        p.frame_navigation(&label, &app_url());
        assert!(!p.document_closed(&label));
        let url = p.get(&label).unwrap().url.clone();
        p.frame_navigation(&label, &url);
        hello(&mut p, &label, Instant::now());
        assert!(!p.document_closed(&label));
        // Then the app's address again, in the frame or at the top: the page's document is gone.
        p.frame_navigation(&label, &app_url());
        assert!(p.document_closed(&label));
    }

    #[test]
    fn a_replaced_frame_document_closes_the_channel() {
        let mut p = Pages::default();
        let label = p.bind(binding("test/alpha")).unwrap();
        p.begin_document(&label);
        let url = p.get(&label).unwrap().url.clone();
        p.frame_navigation(&label, &url);
        hello(&mut p, &label, Instant::now());
        assert!(!p.document_closed(&label));
        p.frame_replaced(&label);
        assert!(p.document_closed(&label));
        // A page no longer open is left alone.
        p.unbind(&label);
        p.frame_replaced(&label);
    }

    #[test]
    fn frame_address_waits_until_frame_navigations_are_watched() {
        let mut p = Pages::default();
        let label = p.bind(binding("test/alpha")).unwrap();
        p.begin_document(&label);
        // Windows: the app hooks the frame's navigations after the webview exists.
        if cfg!(windows) {
            let (s, _) = serve(&mut p, &label, "GET", "/frame", b"");
            assert_eq!(s.status, 503);
            let body: Value = serde_json::from_slice(&s.body).unwrap();
            assert_eq!(body["error"]["code"], "unavailable");
            assert!(!String::from_utf8_lossy(&s.body).contains("localhost:8401"));
            p.watch_frames(&label);
        }
        let (s, _) = serve(&mut p, &label, "GET", "/frame", b"");
        assert_eq!(s.status, 200);
        let info: Value = serde_json::from_slice(&s.body).unwrap();
        assert_eq!(
            info["url"],
            "http://localhost:8401/test/alpha/hash/index.html"
        );
        assert!(info.get("native_frame_navigation").is_none());
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
            let mut other = url.clone();
            other.set_fragment(Some("other"));
            p.frame_navigation(&label, &other);
            assert!(!p.document_closed(&label));
            // Dropping the fragment loads a new document.
            let mut next = url.clone();
            next.set_fragment(None);
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
    fn a_page_refuses_the_asset_host_over_https() {
        // What Windows' CSP source `http://pluxy-app.localhost` also allows, over https at any port.
        let prefix = refused_filter().trim_end_matches('*').to_string();
        for url in [
            "https://pluxy-app.localhost/example/notes/x/index.html",
            "https://pluxy-app.localhost:443/x.js",
            "https://pluxy-app.localhost:8443/x.js",
            "HTTPS://PLUXY-APP.LOCALHOST/x.js",
        ] {
            assert!(refused_request(url), "{url}");
            // The filter WebView2 is given sees it too.
            assert!(
                Url::parse(url).unwrap().as_str().starts_with(&prefix),
                "{url}"
            );
        }
        for url in [
            "http://pluxy-app.localhost/example/notes/x/index.html",
            "pluxy-app://localhost/x.js",
            "https://pluxy-page.localhost/",
            "https://pluxy-apps.localhost/x.js",
            "https://example.com/pluxy-app.x",
            "not a url",
        ] {
            assert!(!refused_request(url), "{url}");
            if let Ok(u) = Url::parse(url) {
                assert!(!u.as_str().starts_with(&prefix), "{url}");
            }
        }
    }

    #[test]
    fn a_device_frame_source_allows_the_asset_scheme_as_served() {
        // Linux and macOS serve the asset scheme by its name; Windows as an http origin.
        for base in ["pluxy-app://localhost/", "http://pluxy-app.localhost/"] {
            let src = asset_source(base);
            let page = Url::parse(&format!(
                "{base}example/notes/{}/index.html",
                "a".repeat(64)
            ))
            .unwrap();
            // CSP: a scheme source (`x:`) allows by scheme, a host source by origin.
            let allows = |u: &Url| match src.strip_suffix(':') {
                Some(scheme) => u.scheme() == scheme,
                None => u.origin().ascii_serialization() == src,
            };
            assert!(allows(&page), "frame-src {src} refuses {page}");
            for other in [
                "http://localhost:8401/x/index.html",
                "http://pluxy-page.localhost/",
            ] {
                assert!(
                    !allows(&Url::parse(other).unwrap()),
                    "frame-src {src} allows {other}"
                );
            }
        }
    }

    #[test]
    fn device_pages_stay_in_their_release_folder() {
        let now = Instant::now();
        let mut p = Pages::default();
        let base = crate::device::asset_base();
        let folder = format!("{base}example/notes/{}/", "a".repeat(64));
        let url = Url::parse(&format!("{folder}index.html")).unwrap();
        let b = Binding {
            extension: "device:example/notes".into(),
            page: "notes".into(),
            title: "Notes".into(),
            url: url.clone(),
            pages_origin: base.clone(),
            device: Some(folder.clone()),
        };
        assert!(b.on_site(&url));
        for other in [
            format!("{base}example/notes/{}/index.html", "b".repeat(64)),
            format!("{base}example/other/{}/index.html", "a".repeat(64)),
            "http://localhost:8401/example/notes/index.html".into(),
        ] {
            assert!(!b.on_site(&Url::parse(&other).unwrap()), "{other}");
        }
        // A core page's binding never matches the asset scheme.
        assert!(!binding("example/notes").on_site(&url));
        let label = p.bind(b).unwrap();
        assert_eq!(p.device_labels("example/notes"), vec![label.clone()]);
        assert!(p.device_labels("example/other").is_empty());
        p.begin_document(&label);
        let (served, _) = serve_at(&mut p, &label, "GET", "/", b"", now);
        let source = if cfg!(any(windows, target_os = "android")) {
            "frame-src http://pluxy-app.localhost;"
        } else {
            "frame-src pluxy-app:;"
        };
        assert!(served.csp.contains(source), "{}", served.csp);
        p.frame_navigation(&label, &url);
        hello(&mut p, &label, now);
        assert!(!p.document_closed(&label));
        // Another release's file is off site: the webview's navigation check refuses it.
        let other = Url::parse(&format!(
            "{base}example/notes/{}/index.html",
            "b".repeat(64)
        ))
        .unwrap();
        p.frame_navigation(&label, &other);
        assert!(!p.document_closed(&label));
        // A second document from its own folder closes the channel.
        p.frame_navigation(&label, &Url::parse(&format!("{folder}other.html")).unwrap());
        assert!(p.document_closed(&label));
    }
    #[test]
    fn only_device_pages_have_access_methods() {
        let now = Instant::now();
        let mut p = Pages::default();
        let core = page(&mut p, "example/files", now);
        for (method, params) in [
            ("resources.status", Value::Null),
            ("resources.read", json!({"resource": "config"})),
            ("resources.list", json!({"resource": "tree"})),
        ] {
            refused(
                request(&mut p, &core, "call", call("core", method, params), now),
                400,
                "unknown_method",
                Some("core"),
            );
        }
        let base = crate::device::asset_base();
        let digest = "a".repeat(64);
        let folder = format!("{base}example/files/{digest}/");
        let b = Binding {
            extension: "device:example/files".into(),
            page: "files".into(),
            title: "Files".into(),
            url: Url::parse(&format!("{folder}index.html")).unwrap(),
            pages_origin: base,
            device: Some(folder),
        };
        assert_eq!(
            b.device_release(),
            Some(("example/files".into(), digest.clone()))
        );
        assert_eq!(binding("example/files").device_release(), None);
        let label = p.bind(b).unwrap();
        p.begin_document(&label);
        hello(&mut p, &label, now);
        for (id, method, params) in [
            ("s", "resources.status", Value::Null),
            ("r", "resources.read", json!({"resource": "config"})),
            (
                "l",
                "resources.list",
                json!({"resource": "tree", "path": ""}),
            ),
        ] {
            let (served, msg) = request(&mut p, &label, "call", call(id, method, params), now);
            assert_eq!(served.status, 204);
            assert_eq!(msg.unwrap().data["method"], method);
        }
        // Parameters of the wrong shape never reach the broker.
        for (method, params) in [
            ("resources.status", json!({})),
            ("resources.read", json!("config")),
            ("resources.list", Value::Null),
        ] {
            refused(
                request(&mut p, &label, "call", call("bad", method, params), now),
                400,
                "bad_request",
                Some("bad"),
            );
        }
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
