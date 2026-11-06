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
    let bare = path.split('?').next().unwrap_or("");
    if bare.split('/').any(|s| s == "..") {
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

impl Client {
    /// A client for the core at base (an origin, such as `http://127.0.0.1:8400`). With certs,
    /// https replies are checked against those certificates only; otherwise against the usual
    /// public roots.
    pub fn new(base: &str, certs: Option<Vec<Certificate<'static>>>) -> Client {
        let roots = match certs {
            Some(c) => RootCerts::Specific(Arc::new(c)),
            None => RootCerts::WebPki,
        };
        let agent: ureq::Agent = ureq::Agent::config_builder()
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
            "GET" => self.agent.get(&url).header("Authorization", &auth).call(),
            "DELETE" => self
                .agent
                .delete(&url)
                .header("Authorization", &auth)
                .call(),
            "POST" | "PATCH" => {
                let req = if method == "POST" {
                    self.agent.post(&url)
                } else {
                    self.agent.patch(&url)
                }
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
            Err(e) => {
                return Reply::failed(0, "unavailable", format!("core could not be reached: {e}"))
            }
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
    fn a_core_that_cannot_be_reached_is_status_0() {
        // Port 9 (discard) on loopback: nothing listens there in the test environment.
        let r = Client::new("http://127.0.0.1:9", None).call("k", "GET", "/status", None);
        assert_eq!(r.status, 0);
        assert_eq!(r.error.unwrap().code, "unavailable");
    }

    #[test]
    fn bad_methods_and_pem_are_refused() {
        let c = Client::new("http://127.0.0.1:9", None);
        assert_eq!(c.call("k", "PUT", "/status", None).status, 400);
        assert!(certificates(b"not a pem").is_err());
    }
}
