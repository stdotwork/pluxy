//! Calls to core's management API, version 0. Only this module sends the management key, and only
//! to `<base>/v0/management/...` of the core the app is connected to: no redirects are followed,
//! and the key is never put anywhere else.

use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::sync::Arc;
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

// Classify transport failures without formatting library errors: those may include request data.
fn transport_kind(error: &ureq::Error) -> &'static str {
    match error {
        ureq::Error::Tls(_) | ureq::Error::Rustls(_) | ureq::Error::TlsRequired => "tls",
        ureq::Error::Timeout(_) => "timeout",
        ureq::Error::HostNotFound => "dns",
        ureq::Error::Io(e) => {
            // Rustls can surface through an I/O wrapper; inspect its type, never its text.
            if let Some(inner) = e.get_ref().and_then(|e| e.downcast_ref::<ureq::Error>()) {
                return transport_kind(inner);
            }
            if e.kind() == std::io::ErrorKind::InvalidData {
                "tls"
            } else {
                "io"
            }
        }
        ureq::Error::Protocol(_) => "protocol",
        _ => "unavailable",
    }
}

impl Client {
    /// A client for the core at base (an origin, such as `http://127.0.0.1:8400`). With certs,
    /// https replies are checked against those certificates only; otherwise against the usual
    /// public roots.
    pub fn new(base: &str, certs: Option<Vec<Certificate<'static>>>) -> Client {
        let roots = match certs {
            Some(c) => RootCerts::Specific(Arc::new(c)),
            None => RootCerts::WebPki,
        };
        // A core on this computer is reached directly: a proxy from the environment would see
        // the key. Others honour the proxy settings; over https a proxy sees no key.
        let direct = url::Url::parse(base).is_ok_and(|u| crate::conn::is_loopback(&u));
        let proxy = if direct {
            None
        } else {
            ureq::Proxy::try_from_env()
        };
        let agent: ureq::Agent = ureq::Agent::config_builder()
            .proxy(proxy)
            .http_status_as_error(false)
            .max_redirects(0)
            .timeout_connect(Some(Duration::from_secs(5)))
            // Installing a package can take a while; a call that hangs longer has failed.
            .timeout_global(Some(Duration::from_secs(300)))
            .user_agent("pluxy-app")
            .tls_config(TlsConfig::builder().root_certs(roots).build())
            .build()
            .into();
        Client {
            base: base.trim_end_matches('/').to_string(),
            agent,
        }
    }

    pub fn base(&self) -> &str {
        &self.base
    }

    /// Calls `method` on path (under `/v0/management`) with key.
    pub fn call(&self, key: &str, method: &str, path: &str, body: Option<&Value>) -> Reply {
        self.send(key, method, path, body, None)
    }

    /// Whether core takes client key key: the status of its client API's `GET /v1/models` with it
    /// as bearer token (200 when it does, 401 once it is no longer one of core's client keys), or
    /// the transport's failure. Nothing of the reply is read but its status.
    pub fn client_key_status(&self, key: &str) -> Result<u16, &'static str> {
        self.agent
            .get(&format!("{}/v1/models", self.base))
            .config()
            .timeout_global(Some(Duration::from_secs(30)))
            .build()
            .header("Authorization", &format!("Bearer {key}"))
            .call()
            .map(|r| r.status().as_u16())
            .map_err(|e| transport_kind(&e))
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
        fn within<B>(
            r: ureq::RequestBuilder<B>,
            limit: Option<Duration>,
        ) -> ureq::RequestBuilder<B> {
            match limit {
                Some(l) => r.config().timeout_global(Some(l)).build(),
                None => r,
            }
        }
        if let Some(p) = path_problem(path) {
            return Reply::failed(400, "invalid_request", p);
        }
        let url = format!("{}{MANAGEMENT}{path}", self.base);
        let auth = format!("Bearer {key}");
        let encoded = match body {
            Some(b) => match serde_json::to_vec(b) {
                Ok(v) if v.len() <= MAX_BODY => Some(v),
                Ok(_) => return Reply::failed(413, "too_large", "the request is too large"),
                Err(e) => return Reply::failed(400, "invalid_request", e.to_string()),
            },
            None => None,
        };
        let result = match method {
            "GET" => within(self.agent.get(&url), limit)
                .header("Authorization", &auth)
                .call(),
            "DELETE" => within(self.agent.delete(&url), limit)
                .header("Authorization", &auth)
                .call(),
            "POST" | "PATCH" | "PUT" => {
                if method == "PUT" && path != "/config" {
                    return Reply::failed(
                        400,
                        "invalid_request",
                        "PUT is only allowed for /config",
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
                )
                .header("Authorization", &auth);
                match encoded {
                    Some(b) => req.header("Content-Type", "application/json").send(&b[..]),
                    None => req.send_empty(),
                }
            }
            _ => return Reply::failed(400, "invalid_request", format!("bad method {method}")),
        };
        let mut resp = match result {
            Ok(r) => r,
            Err(e) => return Reply::failed(0, transport_kind(&e), "core transport failed"),
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
        // A port that was free a moment ago: nothing listens there.
        let port = std::net::TcpListener::bind("127.0.0.1:0")
            .unwrap()
            .local_addr()
            .unwrap()
            .port();
        let r = Client::new(&format!("http://127.0.0.1:{port}"), None)
            .call("k", "GET", "/status", None);
        assert_eq!(r.status, 0);
        assert_eq!(r.error.unwrap().code, "io");
    }

    #[test]
    fn bad_methods_and_pem_are_refused() {
        // Refused before anything is sent.
        let c = Client::new("http://127.0.0.1:1", None);
        assert_eq!(c.call("k", "PUT", "/status", None).status, 400);
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
