//! Calls to core's management API, version 0. Only this module sends the management key, and only
//! to `<base>/v0/management/...` of the core the app is connected to: no redirects are followed,
//! and the key is never put anywhere else.

use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use ureq::tls::{Certificate, RootCerts, TlsConfig};

pub const MANAGEMENT: &str = "/v0/management";

/// The most of a reply the app reads. Core's largest replies (settings, lists) are far smaller.
const MAX_REPLY: u64 = 16 << 20;
/// The most the app sends in one call.
pub const MAX_BODY: usize = 4 << 20;

/// A management API error, as core sends it.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct ApiError {
    pub code: String,
    pub message: String,
}

/// A management API reply, as the kit's `Reply` has it. Status 0: core could not be reached.
#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct Reply {
    pub status: u16,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub body: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<ApiError>,
}

impl Reply {
    pub fn failed(status: u16, code: &str, message: impl Into<String>) -> Reply {
        Reply {
            status,
            body: None,
            error: Some(ApiError {
                code: code.into(),
                message: message.into(),
            }),
        }
    }
}

/// A client for one core. Cheap to clone: clones share their connections.
#[derive(Clone)]
pub struct Client {
    base: String,
    agent: ureq::Agent,
    /// Nothing is sent unless Tailscale carries the connection (`Route::tailscale_only`).
    tailscale_only: bool,
    /// The address every connection is pinned to (`Route::pin`), when there is one.
    pin: Option<std::net::SocketAddr>,
    /// With `tailscale_only` and no pin: the address the name last resolved to, which the next
    /// check of the interfaces says whether Tailscale carries (`Client::peer`).
    seen: Arc<Mutex<Option<std::net::IpAddr>>>,
    /// How many TLS handshakes this client's connections have completed (`TlsDone`): a call that
    /// ends in a reset or an early close says it was the handshake that failed only if this did
    /// not move meanwhile (`Client::kind`).
    tls_done: Arc<AtomicUsize>,
}

/// Where a client's connections go (`Client::routed`).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Route {
    /// Every connection goes to this address, whatever the core's name resolves to later; the
    /// request still names the core's host, and https still checks its certificate for that name.
    /// Ignored when a proxy from the environment carries the calls.
    pub pin: Option<std::net::SocketAddr>,
    /// Each new connection is made only to an address Tailscale carries from this computer
    /// (`tailnet::carries_with`, with the interfaces as they are then); otherwise nothing is
    /// sent, and the call fails as `tailscale`.
    pub tailscale_only: bool,
}

/// Why nothing was sent: Tailscale does not carry the connection to the core's address from this
/// computer (`Route::tailscale_only`).
#[derive(Debug)]
pub struct NotOverTailscale;

impl std::fmt::Display for NotOverTailscale {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("Tailscale does not carry the connection to this address")
    }
}

impl std::error::Error for NotOverTailscale {}

/// The resolver of a routed client: the pinned address, or what the name resolves to, and with
/// `tailscale_only` only the addresses Tailscale carries now.
#[derive(Debug)]
struct Routed(Route, Arc<Mutex<Option<std::net::IpAddr>>>);

impl ureq::unversioned::resolver::Resolver for Routed {
    fn resolve(
        &self,
        uri: &ureq::http::Uri,
        config: &ureq::config::Config,
        timeout: ureq::unversioned::transport::NextTimeout,
    ) -> Result<ureq::unversioned::resolver::ResolvedSocketAddrs, ureq::Error> {
        use ureq::unversioned::resolver::DefaultResolver;
        let found = match self.0.pin {
            Some(addr) => {
                let mut one = self.empty();
                one.push(addr);
                one
            }
            None => DefaultResolver::default().resolve(uri, config, timeout)?,
        };
        if !self.0.tailscale_only {
            return Ok(found);
        }
        let locals = crate::tailnet::local_addresses().unwrap_or_default();
        let mut carried = self.empty();
        for addr in found.iter() {
            if crate::tailnet::carries_with(addr.ip(), &locals) {
                carried.push(*addr);
            }
        }
        // What the status line is told about this link: the address Tailscale carries, or else
        // the first one found, which the interfaces then say it does not.
        if let Ok(mut seen) = self.1.lock() {
            *seen = carried.iter().chain(found.iter()).next().map(|a| a.ip());
        }
        if carried.is_empty() {
            return Err(ureq::Error::Io(std::io::Error::other(NotOverTailscale)));
        }
        Ok(carried)
    }
}

/// Whether calls to base go through a proxy from the environment: never to a core on this
/// computer or over plain http, where a proxy would see the key; over https, as the environment
/// says, since a proxy sees no key there.
pub fn proxied(base: &str) -> bool {
    let direct =
        url::Url::parse(base).is_ok_and(|u| crate::conn::is_loopback(&u) || u.scheme() == "http");
    !direct && ureq::Proxy::try_from_env().is_some()
}

/// The calls core takes as PUT: the config, the theme that is on, and one stored theme, whose
/// ID parts are 1 to 64 of a-z, 0-9 and "-".
fn put_allowed(path: &str) -> bool {
    fn part(s: &str) -> bool {
        (1..=64).contains(&s.len())
            && s.bytes()
                .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-')
    }
    if path == "/config" || path == "/theme" {
        return true;
    }
    let mut parts = path.strip_prefix("/themes/").unwrap_or("").split('/');
    matches!(
        (parts.next(), parts.next(), parts.next()),
        (Some(publisher), Some(name), None) if part(publisher) && part(name)
    )
}

/// Why path may not be sent: it must be an absolute path under the management API, with no `..`
/// segment, no fragment and no characters a URL cannot hold as they are.
pub fn path_problem(path: &str) -> Option<String> {
    let bad = || Some(format!("bad management path {path}"));
    if !path.starts_with('/') || path.starts_with("//") {
        return bad();
    }
    // No dot segments, also percent-encoded: a server or proxy that decodes and normalises the
    // path would take the call outside the management API.
    let bare = path.split('?').next().unwrap_or("");
    if bare
        .split('/')
        .map(|s| s.to_ascii_lowercase().replace("%2e", "."))
        .any(|s| s == "." || s == "..")
    {
        return bad();
    }
    if !path
        .bytes()
        .all(|b| b.is_ascii_graphic() && b != b'#' && b != b'\\')
    {
        return bad();
    }
    None
}

/// Core's route that replaces the management key and returns the new one.
pub const ROTATE: &str = "/management-key/rotate";

/// Whether path is under the management key's own routes, whose replies hold the key: only
/// native code calls them, never a webview through core_call. Compared as a server might route
/// it: percent-decoded, case-folded and without empty or `.` segments.
pub fn native_only(path: &str) -> bool {
    let bare = path.split(['?', '#']).next().unwrap_or("");
    let bytes = bare.as_bytes();
    let mut decoded = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        let hex = |b: u8| (b as char).to_digit(16);
        match (
            bytes[i],
            bytes.get(i + 1).copied().and_then(hex),
            bytes.get(i + 2).copied().and_then(hex),
        ) {
            (b'%', Some(hi), Some(lo)) => {
                decoded.push((hi * 16 + lo) as u8);
                i += 3;
            }
            (b, _, _) => {
                decoded.push(b);
                i += 1;
            }
        }
    }
    decoded
        .split(|&b| b == b'/')
        .find(|s| !s.is_empty() && *s != b".")
        .is_some_and(|s| s.eq_ignore_ascii_case(b"management-key"))
}

/// The certificates in a PEM file, to trust for one core.
pub fn certificates(pem: &[u8]) -> Result<Vec<Certificate<'static>>, String> {
    let mut certs = Vec::new();
    for item in ureq::tls::parse_pem(pem) {
        match item {
            Ok(ureq::tls::PemItem::Certificate(c)) => certs.push(c),
            Ok(_) => {}
            Err(e) => return Err(format!("the certificate file is not PEM: {e}")),
        }
    }
    if certs.is_empty() {
        return Err("the certificate file holds no certificate".into());
    }
    Ok(certs)
}

/// What a failed call's reply says: the reason when the app refused a connection the core's
/// process did not accept (addresses and process IDs only), otherwise a fixed text, since a
/// library error may include request data.
fn transport_message(error: &ureq::Error) -> String {
    #[cfg(windows)]
    if let ureq::Error::Io(e) = error {
        if let Some(why) = e
            .get_ref()
            .and_then(|e| e.downcast_ref::<core_tcp::NotTheCore>())
        {
            return format!("nothing sent to the app's core: {why}");
        }
    }
    if not_over_tailscale(error) {
        return format!("nothing sent: {NotOverTailscale}");
    }
    "core transport failed".into()
}

/// Whether error is a routed client's refusal to send outside Tailscale (`Route::tailscale_only`).
fn not_over_tailscale(error: &ureq::Error) -> bool {
    matches!(error, ureq::Error::Io(e) if e.get_ref().is_some_and(|e| e.is::<NotOverTailscale>()))
}

/// Counts the TLS handshakes a client's connections complete: a connector placed after ureq's own
/// TLS one, which sees a transport only once its handshake is done.
#[derive(Debug)]
struct TlsDone(Arc<AtomicUsize>);

impl ureq::unversioned::transport::Connector<Box<dyn ureq::unversioned::transport::Transport>>
    for TlsDone
{
    type Out = Box<dyn ureq::unversioned::transport::Transport>;

    fn connect(
        &self,
        _: &ureq::unversioned::transport::ConnectionDetails,
        chained: Option<Self::Out>,
    ) -> Result<Option<Self::Out>, ureq::Error> {
        if chained.as_ref().is_some_and(|t| t.is_tls()) {
            self.0.fetch_add(1, Ordering::SeqCst);
        }
        Ok(chained)
    }
}

/// Whether error is the end of a connection that a peer gave no answer on: reset (Unix and
/// Windows alike) or aborted (what Windows reports for a reset it saw while sending), or closed
/// before it said anything. On Windows that is how a server that speaks plain http ends a TLS
/// handshake it cannot read (it closes with the ClientHello unread, which resets the connection
/// before the client reads the 400 it sent: Windows drops the received bytes of a reset
/// connection, Linux hands them over, and rustls then fails on them as `InvalidData`).
fn connection_cut(e: &std::io::Error) -> bool {
    use std::io::ErrorKind::{ConnectionAborted, ConnectionReset, UnexpectedEof};
    matches!(
        e.kind(),
        ConnectionReset | ConnectionAborted | UnexpectedEof
    )
}

// Classify transport failures without formatting library errors: those may include request data.
// handshaking: the call was over https and no TLS handshake had completed when it failed, so a
// cut connection (`connection_cut`) ended the handshake itself; after one, the same error is the
// connection failing, `io`.
fn transport_kind(error: &ureq::Error, handshaking: bool) -> &'static str {
    if not_over_tailscale(error) {
        return "tailscale";
    }
    match error {
        ureq::Error::Tls(_) | ureq::Error::Rustls(_) | ureq::Error::TlsRequired => "tls",
        ureq::Error::Timeout(_) => "timeout",
        ureq::Error::HostNotFound => "dns",
        ureq::Error::Io(e) => {
            // Rustls can surface through an I/O wrapper; inspect its type, never its text.
            if let Some(inner) = e.get_ref().and_then(|e| e.downcast_ref::<ureq::Error>()) {
                return transport_kind(inner, handshaking);
            }
            if e.kind() == std::io::ErrorKind::InvalidData || (handshaking && connection_cut(e)) {
                "tls"
            } else {
                "io"
            }
        }
        ureq::Error::Protocol(_) => "protocol",
        _ => "unavailable",
    }
}

/// The Windows client for the app's own core: its own plain TCP connector and transport, since
/// ureq's TCP transport keeps its socket private and the check needs the socket's two ends.
/// Written against ureq's unversioned transport API, which changes in minor releases (ureq is
/// pinned to 3.5 in Cargo.toml for this).
#[cfg(windows)]
mod core_tcp {
    use std::io::{self, Read, Write};
    use std::net::TcpStream;
    use std::sync::{Arc, Weak};
    use ureq::unversioned::transport::{
        Buffers, ConnectionDetails, Connector, LazyBuffers, NextTimeout, ShutdownHandle, Transport,
    };

    /// Connects to the app's core and passes the connection on only once
    /// `managed::accepted_by` says the core's own process accepted that very connection (its
    /// own local and remote address and port). The pin keeps the core's process ID from passing
    /// to another process for as long as this client lives.
    #[derive(Debug)]
    pub(super) struct CoreConnector(pub(super) Arc<crate::owned::Pin>);

    fn refused(why: String) -> ureq::Error {
        ureq::Error::Io(io::Error::new(io::ErrorKind::PermissionDenied, why))
    }

    /// Why a connection was refused because the core's process did not accept it: addresses and
    /// process IDs only, so the reply may carry it (`refusal`).
    #[derive(Debug)]
    pub(super) struct NotTheCore(pub(super) String);

    impl std::fmt::Display for NotTheCore {
        fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            f.write_str(&self.0)
        }
    }

    impl std::error::Error for NotTheCore {}

    fn timed_out(e: io::Error, reason: ureq::Timeout) -> ureq::Error {
        match e.kind() {
            io::ErrorKind::TimedOut | io::ErrorKind::WouldBlock => ureq::Error::Timeout(reason),
            _ => ureq::Error::Io(e),
        }
    }

    impl Connector for CoreConnector {
        type Out = Box<dyn Transport>;

        fn connect(
            &self,
            details: &ConnectionDetails,
            _chained: Option<()>,
        ) -> Result<Option<Self::Out>, ureq::Error> {
            if details.uri.scheme_str() != Some("http") {
                return Err(refused(
                    "the app's core is reached over plain http on this computer only".into(),
                ));
            }
            let wait = details.timeout.not_zero().map(|t| *t);
            let mut last = None;
            let mut stream = None;
            for addr in details.addrs.iter() {
                let tried = match wait {
                    Some(t) => TcpStream::connect_timeout(addr, t),
                    None => TcpStream::connect(addr),
                };
                match tried {
                    Ok(s) => {
                        stream = Some(s);
                        break;
                    }
                    Err(e) => last = Some(e),
                }
            }
            let stream = match (stream, last) {
                (Some(s), _) => s,
                (None, Some(e)) => return Err(timed_out(e, ureq::Timeout::Connect)),
                (None, None) => {
                    return Err(refused("the core's address resolved to nothing".into()))
                }
            };
            if details.config.no_delay() {
                stream.set_nodelay(true)?;
            }
            let (local, peer) = (stream.local_addr()?, stream.peer_addr()?);
            if let Err(why) = crate::managed::accepted_by(self.0.id(), local, peer) {
                // Addresses and process IDs only.
                eprintln!("pluxy-app: nothing sent to the app's core: {why}");
                return Err(ureq::Error::Io(io::Error::new(
                    io::ErrorKind::PermissionDenied,
                    NotTheCore(why),
                )));
            }
            let stream = Arc::new(stream);
            (details.register_shutdown)(Arc::downgrade(&stream) as Weak<dyn ShutdownHandle>)?;
            Ok(Some(Box::new(CoreStream {
                stream,
                buffers: LazyBuffers::new(
                    details.config.input_buffer_size(),
                    details.config.output_buffer_size(),
                ),
            })))
        }
    }

    /// A plain TCP connection to the core, as ureq's own TCP transport is.
    struct CoreStream {
        stream: Arc<TcpStream>,
        buffers: LazyBuffers,
    }

    impl std::fmt::Debug for CoreStream {
        fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            f.debug_struct("CoreStream")
                .field("peer", &self.stream.peer_addr().ok())
                .finish()
        }
    }

    /// The time left, or a timeout once none is.
    fn left(t: NextTimeout) -> Result<Option<std::time::Duration>, ureq::Error> {
        if t.after.is_zero() {
            return Err(ureq::Error::Timeout(t.reason));
        }
        Ok(t.not_zero().map(|d| *d))
    }

    impl Transport for CoreStream {
        fn buffers(&mut self) -> &mut dyn Buffers {
            &mut self.buffers
        }

        fn transmit_output(
            &mut self,
            amount: usize,
            timeout: NextTimeout,
        ) -> Result<(), ureq::Error> {
            let timeout = timeout.for_write();
            self.stream.set_write_timeout(left(timeout)?)?;
            let output = &self.buffers.output()[..amount];
            (&*self.stream)
                .write_all(output)
                .map_err(|e| timed_out(e, timeout.reason))
        }

        fn await_input(&mut self, timeout: NextTimeout) -> Result<bool, ureq::Error> {
            let timeout = timeout.for_read();
            self.stream.set_read_timeout(left(timeout)?)?;
            let input = self.buffers.input_append_buf();
            let amount = loop {
                match (&*self.stream).read(input) {
                    Ok(n) => break n,
                    Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
                    Err(e) => return Err(timed_out(e, timeout.reason)),
                }
            };
            self.buffers.input_appended(amount);
            Ok(amount > 0)
        }

        fn is_open(&mut self) -> bool {
            // Open while nothing waits to be read and the connection is not closed.
            let mut s = &*self.stream;
            if s.set_nonblocking(true).is_err() {
                return false;
            }
            let open =
                matches!(s.read(&mut [0u8]), Err(e) if e.kind() == io::ErrorKind::WouldBlock);
            s.set_nonblocking(false).is_ok() && open
        }
    }
}

impl Client {
    /// A client for the core at base (an origin, such as `http://127.0.0.1:8400`). With certs,
    /// https replies are checked against those certificates only; otherwise against the usual
    /// public roots.
    pub fn new(base: &str, certs: Option<Vec<Certificate<'static>>>) -> Client {
        Client::build(base, certs)
    }

    /// A client for the core the app started, at base on this computer. On Windows, where the
    /// app holds no copy of the core's listening socket, each new connection is checked to be
    /// the core's own before anything is sent on it (`managed::accepted_by`): another program
    /// on the core's port (one listening there since the core ended, or one on a wildcard
    /// address over it all along) gets no request, management key and all, the call fails as
    /// `io`, and the app's log says why. The client holds a handle on the core's process for
    /// as long as it lives (`ManagedCore::pin`), so the process ID it checks against is never
    /// another process's. Elsewhere the app holds the core's socket while it calls
    /// (`managed::ManagedCore::hold`).
    pub fn for_core(base: &str, core: &crate::managed::ManagedCore) -> Result<Client, String> {
        #[cfg(windows)]
        {
            Ok(Client::with_pin(base, core.pin()?))
        }
        #[cfg(not(windows))]
        {
            let _ = core;
            Ok(Client::build(base, None))
        }
    }

    /// A client that sends only on connections the pinned process accepted (`for_core`).
    #[cfg(windows)]
    pub(crate) fn with_pin(base: &str, pin: crate::owned::Pin) -> Client {
        use ureq::unversioned::resolver::DefaultResolver;
        Client {
            base: base.trim_end_matches('/').to_string(),
            agent: ureq::Agent::with_parts(
                Client::config(base, None),
                core_tcp::CoreConnector(Arc::new(pin)),
                DefaultResolver::default(),
            ),
            tailscale_only: false,
            pin: None,
            seen: Arc::default(),
            tls_done: Arc::default(),
        }
    }

    /// A client for the core at base whose connections go where route says (`Route`).
    pub fn routed(
        base: &str,
        certs: Option<Vec<Certificate<'static>>>,
        mut route: Route,
    ) -> Client {
        if route == Route::default() {
            return Client::build(base, certs);
        }
        if proxied(base) {
            // The proxy's address is resolved, not the core's.
            route.pin = None;
        }
        let seen = Arc::new(Mutex::new(None));
        let tls_done = Arc::<AtomicUsize>::default();
        Client {
            base: base.trim_end_matches('/').to_string(),
            agent: ureq::Agent::with_parts(
                Client::config(base, certs),
                Client::connector(&tls_done),
                Routed(route, seen.clone()),
            ),
            tailscale_only: route.tailscale_only,
            pin: route.pin,
            seen,
            tls_done,
        }
    }

    fn build(base: &str, certs: Option<Vec<Certificate<'static>>>) -> Client {
        let tls_done = Arc::<AtomicUsize>::default();
        Client {
            base: base.trim_end_matches('/').to_string(),
            agent: ureq::Agent::with_parts(
                Client::config(base, certs),
                Client::connector(&tls_done),
                ureq::unversioned::resolver::DefaultResolver::default(),
            ),
            tailscale_only: false,
            pin: None,
            seen: Arc::default(),
            tls_done,
        }
    }

    /// ureq's own connectors, then `TlsDone`.
    fn connector(tls_done: &Arc<AtomicUsize>) -> impl ureq::unversioned::transport::Connector {
        use ureq::unversioned::transport::{Connector, DefaultConnector};
        DefaultConnector::default().chain(TlsDone(tls_done.clone()))
    }

    /// Why a call that began when `tls_done` read before failed with error, as a code (`transport_kind`).
    fn kind(&self, error: &ureq::Error, before: usize) -> &'static str {
        let handshaking =
            self.base.starts_with("https:") && self.tls_done.load(Ordering::SeqCst) == before;
        transport_kind(error, handshaking)
    }

    fn config(base: &str, certs: Option<Vec<Certificate<'static>>>) -> ureq::config::Config {
        let roots = match certs {
            Some(c) => RootCerts::Specific(Arc::new(c)),
            None => RootCerts::WebPki,
        };
        let proxy = if proxied(base) {
            ureq::Proxy::try_from_env()
        } else {
            None
        };
        ureq::Agent::config_builder()
            .proxy(proxy)
            .http_status_as_error(false)
            .max_redirects(0)
            .timeout_connect(Some(Duration::from_secs(5)))
            // Installing a package can take a while; a call that hangs longer has failed.
            .timeout_global(Some(Duration::from_secs(300)))
            .user_agent("pluxy-app")
            .tls_config(TlsConfig::builder().root_certs(roots).build())
            .build()
    }

    pub fn base(&self) -> &str {
        &self.base
    }

    /// Whether this client sends only where Tailscale carries the connection (`Route`).
    pub fn tailscale_only(&self) -> bool {
        self.tailscale_only
    }

    /// The address the client's connections go to: its pin, or else the address its name last
    /// resolved to when a connection was last made (none before that).
    pub fn peer(&self) -> Option<std::net::IpAddr> {
        self.pin
            .map(|p| p.ip())
            .or_else(|| self.seen.lock().ok().and_then(|s| *s))
    }

    /// Calls `method` on path (under `/v0/management`) with key; with an empty key, with no
    /// Authorization header at all.
    pub fn call(&self, key: &str, method: &str, path: &str, body: Option<&Value>) -> Reply {
        self.send(key, method, path, body, None)
    }

    /// Whether core takes client key key: the status of its client API's `GET /v1/models` with it
    /// as bearer token (200 when it does, 401 once it is no longer one of core's client keys), or
    /// the transport's failure. Nothing of the reply is read but its status.
    pub fn client_key_status(&self, key: &str) -> Result<u16, &'static str> {
        let before = self.tls_done.load(Ordering::SeqCst);
        self.agent
            .get(&format!("{}/v1/models", self.base))
            .config()
            .timeout_global(Some(Duration::from_secs(30)))
            .build()
            .header("Authorization", &format!("Bearer {key}"))
            .call()
            .map(|r| r.status().as_u16())
            .map_err(|e| self.kind(&e, before))
    }

    /// Calls as call does, giving up after limit instead of the client's 300 seconds.
    pub fn call_within(
        &self,
        key: &str,
        method: &str,
        path: &str,
        body: Option<&Value>,
        limit: Duration,
    ) -> Reply {
        self.send(key, method, path, body, Some(limit))
    }

    fn send(
        &self,
        key: &str,
        method: &str,
        path: &str,
        body: Option<&Value>,
        limit: Option<Duration>,
    ) -> Reply {
        /// The request with the client's limit replaced by limit, and the key as its bearer
        /// token; an empty key sends no Authorization header.
        fn within<B>(
            r: ureq::RequestBuilder<B>,
            limit: Option<Duration>,
            key: &str,
        ) -> ureq::RequestBuilder<B> {
            let r = match limit {
                Some(l) => r.config().timeout_global(Some(l)).build(),
                None => r,
            };
            if key.is_empty() {
                r
            } else {
                r.header("Authorization", &format!("Bearer {key}"))
            }
        }
        if let Some(p) = path_problem(path) {
            return Reply::failed(400, "invalid_request", p);
        }
        let url = format!("{}{MANAGEMENT}{path}", self.base);
        let before = self.tls_done.load(Ordering::SeqCst);
        let encoded = match body {
            Some(b) => match serde_json::to_vec(b) {
                Ok(v) if v.len() <= MAX_BODY => Some(v),
                Ok(_) => return Reply::failed(413, "too_large", "the request is too large"),
                Err(e) => return Reply::failed(400, "invalid_request", e.to_string()),
            },
            None => None,
        };
        let result = match method {
            "GET" => within(self.agent.get(&url), limit, key).call(),
            "DELETE" => within(self.agent.delete(&url), limit, key).call(),
            "POST" | "PATCH" | "PUT" => {
                if method == "PUT" && !put_allowed(path) {
                    return Reply::failed(
                        400,
                        "invalid_request",
                        "PUT is only allowed for /config, /theme and /themes/{publisher}/{name}",
                    );
                }
                let req = within(
                    if method == "POST" {
                        self.agent.post(&url)
                    } else if method == "PUT" {
                        self.agent.put(&url)
                    } else {
                        self.agent.patch(&url)
                    },
                    limit,
                    key,
                );
                match encoded {
                    Some(b) => req.header("Content-Type", "application/json").send(&b[..]),
                    None => req.send_empty(),
                }
            }
            _ => return Reply::failed(400, "invalid_request", format!("bad method {method}")),
        };
        let mut resp = match result {
            Ok(r) => r,
            Err(e) => return Reply::failed(0, self.kind(&e, before), transport_message(&e)),
        };
        let status = resp.status().as_u16();
        let bytes = match resp.body_mut().with_config().limit(MAX_REPLY).read_to_vec() {
            Ok(b) => b,
            Err(e) => return Reply::failed(0, "unavailable", format!("reading core's reply: {e}")),
        };
        let json: Option<Value> = if bytes.is_empty() {
            None
        } else {
            serde_json::from_slice(&bytes).ok()
        };
        if (200..300).contains(&status) {
            return Reply {
                status,
                body: json,
                error: None,
            };
        }
        let error = json
            .as_ref()
            .and_then(|j| j.get("error"))
            .and_then(|e| serde_json::from_value::<ApiError>(e.clone()).ok());
        Reply {
            status,
            body: None,
            error,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn paths_stay_under_the_management_api() {
        assert_eq!(path_problem("/status"), None);
        assert_eq!(path_problem("/accounts/a%2Fb?x=1"), None);
        for bad in [
            "status",
            "//evil.example/x",
            "/../panel",
            "/a/../../x",
            "/%2e%2e/%2E%2e/public",
            "/a/.%2e/x",
            "/a/%2e/x",
            "/a/./x",
            "/a#frag",
            "/a\\b",
            "/a b",
            "/a\n",
            "",
        ] {
            assert!(path_problem(bad).is_some(), "{bad:?} was allowed");
        }
    }

    #[test]
    fn the_management_key_routes_are_native_only_however_spelled() {
        for path in [
            ROTATE,
            "/management-key/rotate/stage",
            "/management-key/rotate/commit",
            "/management-key/%72otate",
            "/%6Danagement-key/rotate",
            "/%6danagement%2Dkey%2frotate",
            "/Management-Key/rotate/",
            "/management-key/rotate?x=1",
            "//management-key/rotate",
            "/./management-key/rotate",
            "/management-key",
            "/management-key#x",
        ] {
            assert!(native_only(path), "{path:?} was allowed");
        }
        for path in [
            "/status",
            "/client-keys",
            "/config",
            "/management-keys",
            "/x/management-key/rotate",
            "/status?next=/management-key/rotate",
            "/management-key%",
            "",
        ] {
            assert!(!native_only(path), "{path:?} was refused");
        }
    }

    #[test]
    fn a_core_that_cannot_be_reached_is_status_0() {
        // Port 1 on loopback: no test listens there and nothing hands it out, unlike a port
        // taken from `:0` and freed, which a test running at the same time can be given.
        let r = Client::new("http://127.0.0.1:1", None).call("k", "GET", "/status", None);
        assert_eq!(r.status, 0);
        assert_eq!(r.error.unwrap().code, "io");
    }

    /// How a connection cut during the TLS handshake reads, and how the same errors read
    /// otherwise. Windows ends the handshake with a server that speaks plain http as a reset (or
    /// an abort, when it saw the reset while sending), not as the bytes of its 400 reply that
    /// Linux hands rustls: so those errors, fed here as they come, are the handshake failing
    /// while no handshake has completed, and the connection failing after one, or over http.
    #[test]
    fn a_cut_connection_is_a_handshake_failure_only_before_the_handshake_completed() {
        use std::io::{Error, ErrorKind};
        let io = |kind: ErrorKind| ureq::Error::Io(Error::from(kind));
        for kind in [
            ErrorKind::ConnectionReset,
            ErrorKind::ConnectionAborted,
            ErrorKind::UnexpectedEof,
        ] {
            assert_eq!(transport_kind(&io(kind), true), "tls", "{kind:?}");
            assert_eq!(transport_kind(&io(kind), false), "io", "{kind:?}");
        }
        // Not a cut connection: nothing listening, no route, no permission stay I/O whatever the
        // scheme; garbage where a TLS record should be (Linux's shape) is the handshake either way.
        for kind in [
            ErrorKind::ConnectionRefused,
            ErrorKind::BrokenPipe,
            ErrorKind::PermissionDenied,
            ErrorKind::NotConnected,
        ] {
            assert_eq!(transport_kind(&io(kind), true), "io", "{kind:?}");
        }
        assert_eq!(transport_kind(&io(ErrorKind::InvalidData), false), "tls");
    }

    /// The same, on a real connection: an https address whose server closes with the ClientHello
    /// unread, which resets the connection (on Windows, with no bytes of a reply for the client
    /// to read; here too, the reset arriving before any is read). A person who typed https for a
    /// plain-http pluxy is told the handshake failed, not "failed (I/O)", and a plain http
    /// address that resets the same way stays I/O.
    #[test]
    fn https_to_a_server_that_resets_the_handshake_is_a_tls_failure() {
        /// Resets the first connection once its first bytes have arrived, unread.
        fn resetter() -> String {
            let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
            let addr = listener.local_addr().unwrap();
            std::thread::spawn(move || {
                let (stream, _) = listener.accept().unwrap();
                let mut byte = [0];
                while matches!(stream.peek(&mut byte), Ok(0)) {}
                drop(stream);
            });
            addr.to_string()
        }
        let r =
            Client::new(&format!("https://{}", resetter()), None).call("", "GET", "/status", None);
        assert_eq!(r.status, 0);
        assert_eq!(r.error.unwrap().code, "tls");
        // Over http the same reset is the connection failing.
        let r =
            Client::new(&format!("http://{}", resetter()), None).call("", "GET", "/status", None);
        assert_eq!(r.status, 0);
        assert_eq!(r.error.unwrap().code, "io");
    }

    /// On Windows a client for the app's core sends nothing on a connection another process
    /// accepted, on IPv4 and IPv6 loopback, and sends as usual on one the core accepted. The
    /// listener is this test's own: the client's pin holds this process, said to be process 4
    /// (Windows' System process) for "another process" and this one's own ID for the core.
    /// `managed.rs` checks the same with real processes, against the real core.
    #[cfg(windows)]
    #[test]
    fn a_core_client_sends_only_on_connections_the_core_accepted() {
        use std::io::{Read, Write};
        for bind in ["127.0.0.1:0", "[::1]:0"] {
            let listener = std::net::TcpListener::bind(bind).unwrap();
            let addr = listener.local_addr().unwrap();
            // The bytes each of two connections brought; a whole request is answered.
            let server = std::thread::spawn(move || {
                let mut got = Vec::new();
                for _ in 0..2 {
                    let (mut s, _) = listener.accept().unwrap();
                    s.set_read_timeout(Some(Duration::from_secs(5))).unwrap();
                    let mut head = Vec::new();
                    let mut buf = [0u8; 4096];
                    while let Ok(n @ 1..) = s.read(&mut buf) {
                        head.extend_from_slice(&buf[..n]);
                        if head.windows(4).any(|w| w == b"\r\n\r\n") {
                            s.write_all(b"HTTP/1.1 200 OK\r\ncontent-length: 2\r\nconnection: close\r\n\r\n{}")
                                .unwrap();
                            break;
                        }
                    }
                    got.push(head.len());
                }
                got
            });
            let base = format!("http://{addr}");
            let other = crate::owned::Pin::this_process_as(4);
            let refused = Client::with_pin(&base, other).call("k", "GET", "/status", None);
            let this = crate::owned::Pin::this_process_as(std::process::id());
            let taken = Client::with_pin(&base, this).call("k", "GET", "/status", None);
            let got = server.join().unwrap();
            assert_eq!(refused.status, 0, "{bind}: {refused:?}");
            let error = refused.error.unwrap();
            assert_eq!(error.code, "io", "{bind}");
            // The reply says which process accepted it instead (a release build has no stderr).
            assert!(
                error
                    .message
                    .starts_with("nothing sent to the app's core: ")
                    && error.message.contains(&format!(
                        "was accepted by process {}, not the core (4)",
                        std::process::id()
                    )),
                "{bind}: {}",
                error.message
            );
            assert_eq!(
                got[0], 0,
                "{bind}: the request went to a listener not the core's"
            );
            assert_eq!(taken.status, 200, "{bind}: {taken:?}");
            assert!(got[1] > 0, "{bind}");
        }
    }

    #[test]
    fn bad_methods_and_pem_are_refused() {
        // Refused before anything is sent.
        let c = Client::new("http://127.0.0.1:1", None);
        assert_eq!(c.call("k", "PUT", "/status", None).status, 400);
        for path in [
            "/themes",
            "/themes/t3",
            "/themes/t3/a/b",
            "/themes/T3/night",
            "/themes/t3/",
            "/themes//night",
            "/themes/t3/..",
            "/themes/t3/a.b",
            "/themes/t3/a_b",
            "/themes/t3/a%2fb",
            "/themes/t3/%2e%2e",
            "/themes/t3/night?x=1",
            "/themes/t3/n\u{e9}",
            "/theme/x",
            "/theme?x=1",
            "/config?x=1",
        ] {
            assert!(!put_allowed(path), "{path}");
            assert_eq!(c.call("k", "PUT", path, None).status, 400, "{path}");
        }
        let long = "a".repeat(65);
        assert!(!put_allowed(&format!("/themes/{long}/night")));
        assert!(!put_allowed(&format!("/themes/t3/{long}")));
        assert!(put_allowed(&format!("/themes/t3/{}", "a".repeat(64))));
        // The theme calls are sent: nothing listens there, so the reply is status 0, not a refusal.
        assert_eq!(c.call("k", "PUT", "/theme", None).status, 0);
        assert_eq!(c.call("k", "PUT", "/themes/t3/nightfall", None).status, 0);
        for path in [
            "/config",
            "/theme",
            "/themes/t3/nightfall",
            "/themes/a-1/b-2",
        ] {
            assert!(put_allowed(path), "{path}");
        }
        assert_eq!(c.call("k", "TRACE", "/config", None).status, 400);
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap();
        let server = std::thread::spawn(move || {
            use std::io::{Read, Write};
            let (mut stream, _) = listener.accept().unwrap();
            stream
                .set_read_timeout(Some(Duration::from_secs(5)))
                .unwrap();
            let mut head = Vec::new();
            let mut byte = [0];
            while !head.ends_with(b"\r\n\r\n") {
                stream.read_exact(&mut byte).unwrap();
                head.push(byte[0]);
            }
            assert!(head.starts_with(b"PUT /v0/management/config HTTP/1.1\r\n"));
            stream.write_all(b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}").unwrap();
        });
        let put = Client::new(&format!("http://{addr}"), None).call("k", "PUT", "/config", None);
        assert_eq!(put.status, 200);
        server.join().unwrap();
        assert!(certificates(b"not a pem").is_err());
    }
}
