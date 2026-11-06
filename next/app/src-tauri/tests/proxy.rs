//! A core on this computer is reached directly, never through a proxy from the environment: a
//! proxy would see the management key. Its own test program, since it sets the proxy variables.

use std::io::{Read, Write};
use std::net::TcpListener;
use std::sync::mpsc;
use std::time::Duration;

/// A listener that answers each request with 200 and sends what it got.
fn listen(tx: mpsc::Sender<String>) -> String {
    let l = TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = l.local_addr().unwrap().to_string();
    std::thread::spawn(move || {
        for s in l.incoming() {
            let Ok(mut s) = s else { break };
            let mut buf = [0u8; 4096];
            let n = s.read(&mut buf).unwrap_or(0);
            let _ = tx.send(String::from_utf8_lossy(&buf[..n]).into_owned());
            let _ =
                s.write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}");
        }
    });
    addr
}

#[test]
fn a_loopback_core_is_reached_directly() {
    let (core_tx, core_rx) = mpsc::channel();
    let (proxy_tx, proxy_rx) = mpsc::channel();
    let core = listen(core_tx);
    let proxy = listen(proxy_tx);
    for var in [
        "ALL_PROXY",
        "all_proxy",
        "HTTP_PROXY",
        "http_proxy",
        "HTTPS_PROXY",
        "https_proxy",
    ] {
        std::env::set_var(var, format!("http://{proxy}"));
    }
    std::env::remove_var("NO_PROXY");
    std::env::remove_var("no_proxy");

    let client = pluxy_app::client::Client::new(&format!("http://{core}"), None);
    let r = client.call("secret-key-for-the-test", "GET", "/status", None);
    assert_eq!(r.status, 200, "{r:?}");
    let got = core_rx
        .recv_timeout(Duration::from_secs(5))
        .expect("core got nothing");
    assert!(got.contains("secret-key-for-the-test"), "{got}");
    assert!(
        proxy_rx.recv_timeout(Duration::from_millis(500)).is_err(),
        "the proxy got the request"
    );
}
