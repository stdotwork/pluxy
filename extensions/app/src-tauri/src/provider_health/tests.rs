use super::*;
use crate::GuiApiKeyEntry;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

#[test]
fn provider_proxy_override_preserves_direct_and_global_inheritance() {
    let upstream = reqwest::Url::parse("https://provider.invalid/v1/responses").unwrap();
    let global = "http://127.0.0.1:18080";
    assert_eq!(provider_health_proxy_url(&upstream, None, global), global);
    assert_eq!(provider_health_proxy_url(&upstream, Some("  "), global), global);
    assert_eq!(provider_health_proxy_url(&upstream, Some(" direct "), global), "");
    assert_eq!(provider_health_proxy_url(&upstream, Some("socks5://127.0.0.1:19080"), global), "socks5://127.0.0.1:19080");
    let local = reqwest::Url::parse("http://127.0.0.1:8317/v1/responses").unwrap();
    assert_eq!(provider_health_proxy_url(&local, None, global), "");
}

#[tokio::test]
async fn provider_health_uses_request_proxy_instead_of_global_proxy() {
    let (port, server) = mock_core_response("200 OK", "text/event-stream", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"OK\"}]}}]}\n\n", "").await;
    let request: ProviderHealthProbeRequest = serde_json::from_value(serde_json::json!({
        "url": "http://provider.invalid/v1beta/models/test:streamGenerateContent?alt=sse",
        "header": {}, "data": "{}", "protocol": "gemini",
        "proxyUrl": format!("http://127.0.0.1:{port}"),
    })).unwrap();
    let url = reqwest::Url::parse(&request.url).unwrap();
    let selected = provider_health_proxy_url(&url, request.proxy_url.as_deref(), "http://127.0.0.1:1");
    let client = build_http_client_with_proxy(reqwest::Client::builder().timeout(Duration::from_secs(3)), selected, "proxy test").unwrap();
    assert!(execute_health_probe(None, client, request).await.unwrap().first_token_latency_ms.is_some());
    let wire_request = server.await.unwrap();
    assert!(wire_request.starts_with("POST http://provider.invalid/v1beta/models/test:streamGenerateContent?alt=sse HTTP/1.1"));
}

async fn mock_core_response(
    status: &str,
    content_type: &str,
    initial_body: &str,
    delayed_body: &str,
) -> (u16, tokio::task::JoinHandle<String>) {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let head = format!(
        "HTTP/1.1 {status}\r\nContent-Type: {content_type}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{initial_body}",
        initial_body.len() + delayed_body.len(),
    );
    let delayed_body = delayed_body.to_string();
    let server = tokio::spawn(async move {
        tokio::time::timeout(Duration::from_secs(5), async move {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut received = Vec::new();
            loop {
                let mut chunk = [0; 2048];
                let size = socket.read(&mut chunk).await.unwrap();
                assert!(size > 0, "request ended before its body");
                received.extend_from_slice(&chunk[..size]);
                let Some(end) = received.windows(4).position(|window| window == b"\r\n\r\n") else {
                    continue;
                };
                let headers = String::from_utf8_lossy(&received[..end]);
                let length = headers.lines().find_map(|line| {
                    let (name, value) = line.split_once(':')?;
                    name.eq_ignore_ascii_case("content-length")
                        .then(|| value.trim().parse::<usize>().unwrap())
                }).unwrap_or(0);
                if received.len() >= end + 4 + length {
                    break;
                }
            }
            socket.write_all(head.as_bytes()).await.unwrap();
            if !delayed_body.is_empty() {
                tokio::time::sleep(Duration::from_millis(60)).await;
                let _ = socket.write_all(delayed_body.as_bytes()).await;
            }
            String::from_utf8(received).unwrap()
        }).await.expect("mock kernel timed out")
    });
    (port, server)
}

fn probe_config(port: u16) -> GuiConfigFile {
    GuiConfigFile {
        port,
        api_keys: vec![
            GuiApiKeyEntry { key: " ".to_string(), remark: String::new() },
            GuiApiKeyEntry { key: " test-client-access-key ".to_string(), remark: String::new() },
        ],
        management_secret_key: "must-never-be-used-for-inference".to_string(),
        // A local kernel probe must succeed independently of upstream proxy settings.
        proxy_url: "http://127.0.0.1:1".to_string(),
        ..GuiConfigFile::default()
    }
}

#[tokio::test]
async fn core_health_probe_uses_local_inference_key_and_waits_for_model_text() {
    let metadata = "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n";
    let text = "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n";
    let (port, server) = mock_core_response("200 OK", "text/event-stream", metadata, text).await;
    let result = probe_core_model(&probe_config(port), "  custom/model-alias  ", Some(2_000))
        .await.unwrap();
    assert!(result.first_token_latency_ms.unwrap() >= 50);
    assert_eq!(result.first_token_latency_ms, Some(result.response_latency_ms));
    let request = server.await.unwrap();
    assert!(request.starts_with("POST /v1/chat/completions HTTP/1.1\r\n"));
    assert!(request.to_ascii_lowercase().contains("authorization: bearer test-client-access-key\r\n"));
    assert!(!request.contains("must-never-be-used-for-inference"));
    let body: serde_json::Value = serde_json::from_str(request.split_once("\r\n\r\n").unwrap().1).unwrap();
    assert_eq!(body["model"], "custom/model-alias");
    assert_eq!(body["stream"], true);
    assert_eq!(body["max_tokens"], 16);
    assert_eq!(body["messages"][0]["role"], "user");
    let payload = serde_json::to_value(result).unwrap();
    assert!(payload["firstTokenLatencyMs"].as_u64().is_some());
    assert!(payload["responseLatencyMs"].as_u64().is_some());
}

#[tokio::test]
async fn core_health_probe_rejects_http_errors_redirects_and_empty_streams() {
    for (status, content_type, body, expected) in [
        ("401 Unauthorized", "application/json", "access denied", "HTTP 401"),
        ("200 OK", "application/json", "{\"choices\":[]}", "streaming response"),
        ("200 OK", "text/event-stream", "data: [DONE]\n\n", "first token"),
        ("302 Found\r\nLocation: http://127.0.0.1:1/v1/chat/completions", "text/plain", "", "HTTP 302"),
    ] {
        let (port, server) = mock_core_response(status, content_type, body, "").await;
        let error = probe_core_model(&probe_config(port), "test-model", Some(2_000)).await.err().unwrap();
        assert!(error.contains(expected), "unexpected probe error: {error}");
        server.await.unwrap();
    }
}

#[tokio::test]
async fn core_health_probe_rejects_invalid_model_and_port_without_a_request() {
    let config = probe_config(0);
    for model in ["", "  ", "bad\nmodel", &"a".repeat(241)] {
        let error = probe_core_model(&config, model, None).await.err().unwrap();
        assert!(error.contains("model"), "unexpected validation error: {error}");
    }
    assert!(probe_core_model(&config, "valid-model", None).await.err().unwrap().contains("port"));
}

#[tokio::test]
async fn core_health_model_list_preserves_all_provider_models() {
    let body = r#"{"data":[{"id":"gpt-test"},{"id":"claude-test"},{"id":"gemini-test"},{"id":"custom-alias"},{"id":"Foo","alias":"metadata-alias"},{"id":"foo"},{"id":"Foo"}]}"#;
    let (port, server) = mock_core_response("200 OK", "application/json", body, "").await;
    let config = probe_config(port);
    let models = fetch_core_models(&config).await.unwrap();
    assert_eq!(models.iter().map(|model| model.name.as_str()).collect::<Vec<_>>(),
        vec!["gpt-test", "claude-test", "gemini-test", "custom-alias", "Foo", "foo"]);
    let request = server.await.unwrap();
    assert!(request.starts_with("GET /v1/models HTTP/1.1\r\n"));
    assert!(request.to_ascii_lowercase().contains("authorization: bearer test-client-access-key\r\n"));
    assert!(!request.contains("must-never-be-used-for-inference"));
}

#[test]
fn core_health_probe_timeout_is_bounded() {
    assert_eq!(health_probe_timeout(None), Duration::from_secs(15));
    assert_eq!(health_probe_timeout(Some(0)), Duration::from_secs(1));
    assert_eq!(health_probe_timeout(Some(u64::MAX)), Duration::from_secs(120));
}

#[test]
fn core_model_parser_keeps_public_ids_without_agent_alias_rewrites() {
    let models = parse_core_models(&serde_json::json!({ "data": [
        { "id": "Foo", "name": "Display Foo", "alias": "never-use-this-alias" },
        { "id": "foo", "displayName": "Lowercase foo" },
        { "id": "Foo", "displayName": "duplicate" },
        { "name": "legacy-model", "alias": "legacy-alias" },
    ] })).unwrap();
    assert_eq!(serde_json::to_value(models).unwrap(), serde_json::json!([
        { "name": "Foo", "displayName": "Display Foo" },
        { "name": "foo", "displayName": "Lowercase foo" },
        { "name": "legacy-model" },
    ]));
    let catalog = parse_core_models(&serde_json::json!({ "models": {
        "public-id": { "alias": "private-alias", "display_name": "Public model" },
    } })).unwrap();
    assert_eq!(catalog[0].name, "public-id");
    assert!(parse_core_models(&serde_json::json!({ "data": [{}] })).is_err());
    assert!(parse_core_models(&serde_json::json!({ "data": null })).is_err());
    assert!(parse_core_models(&serde_json::json!({ "data": [] })).unwrap().is_empty());
}

#[tokio::test]
async fn core_health_accepts_confirmed_reasoning_only_budget_exhaustion_without_ttft() {
    for finish_reason in ["max_tokens", "length"] {
        let body = format!("data: {}\n\ndata: [DONE]\n\n", serde_json::json!({
            "choices": [{ "delta": {}, "finish_reason": finish_reason }],
            "usage": { "completion_tokens": 16, "completion_tokens_details": { "reasoning_tokens": 16 } },
        }));
        let (port, server) = mock_core_response("200 OK", "text/event-stream", &body, "").await;
        let result = probe_core_model(&probe_config(port), "gemini-thinking", Some(2_000)).await.unwrap();
        assert!(result.first_token_latency_ms.is_none());
        assert!(result.response_latency_ms > 0);
        server.await.unwrap();
    }
}

#[test]
fn openai_terminal_success_requires_both_exhausted_budget_and_valid_reasoning_usage() {
    for value in [
        serde_json::json!({ "choices": [{ "delta": {}, "finish_reason": "max_tokens" }] }),
        serde_json::json!({ "choices": [{ "finish_reason": "max_tokens" }], "usage": {
            "completion_tokens": 16, "completion_tokens_details": { "reasoning_tokens": 0 },
        } }),
        serde_json::json!({ "choices": [{ "finish_reason": "max_tokens" }], "usage": {
            "completion_tokens": 4, "completion_tokens_details": { "reasoning_tokens": 16 },
        } }),
        serde_json::json!({ "choices": [{ "finish_reason": "content_filter" }], "usage": {
            "completion_tokens": 16, "completion_tokens_details": { "reasoning_tokens": 16 },
        } }),
        serde_json::json!({ "choices": [], "usage": {
            "completion_tokens": 16, "completion_tokens_details": { "reasoning_tokens": 16 },
        } }),
    ] {
        let body = format!("data: {value}\n\ndata: [DONE]\n\n");
        assert!(!provider_health_stream_has_terminal_success("openai-chat", body.as_bytes()));
    }
}

#[tokio::test]
async fn remote_core_models_use_its_origin_and_first_client_key() {
    let _guard = REMOTE_CONFIG_TEST_LOCK.lock().await;
    use std::io::{Read, Write};
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let origin = format!("http://{}", listener.local_addr().unwrap());
    let server = std::thread::spawn(move || {
        for expected in ["get /v8/management/config.yaml ", "get /v1/models "] {
            let (mut socket, _) = listener.accept().unwrap();
            socket.set_read_timeout(Some(Duration::from_secs(5))).unwrap();
            let mut request = Vec::new();
            let mut buffer = [0; 1024];
            while !request.windows(4).any(|part| part == b"\r\n\r\n") {
                let count = socket.read(&mut buffer).unwrap();
                assert!(count > 0);
                request.extend_from_slice(&buffer[..count]);
            }
            let request = String::from_utf8(request).unwrap().to_ascii_lowercase();
            assert!(request.starts_with(expected), "{request}");
            let (authorization, body) = if expected.contains("management") {
                ("remote-management-key", "server:\n  host: 0.0.0.0\n  port: 9443\naccess:\n  api-keys:\n    - remote-client-key\n    - second-key\n")
            } else {
                ("remote-client-key", r#"{"data":[{"id":"remote-model"}]}"#)
            };
            assert!(request.contains(&format!("authorization: bearer {authorization}\r\n")), "{request}");
            write!(socket, "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.len()).unwrap();
        }
    });
    let config = GuiConfigFile {
        connection_mode: CoreConnectionMode::Remote,
        remote_management_origin: origin,
        remote_management_key: "remote-management-key".into(),
        ..probe_config(0)
    };
    let models = fetch_core_models(&config).await.unwrap();
    server.join().unwrap();
    assert_eq!(models.iter().map(|model| model.name.as_str()).collect::<Vec<_>>(), vec!["remote-model"]);
}

// The production snapshot has one slot; serialize tests that intentionally assert cache hits.
static REMOTE_CONFIG_TEST_LOCK: tokio::sync::Mutex<()> = tokio::sync::Mutex::const_new(());

async fn mock_remote_probe_core(
    yaml_delay: Duration,
    inference_delay: Duration,
) -> (GuiConfigFile, tokio::sync::mpsc::UnboundedReceiver<String>, tokio::task::JoinHandle<()>) {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let origin = format!("http://{}", listener.local_addr().unwrap());
    let (requests, received) = tokio::sync::mpsc::unbounded_channel();
    let server = tokio::spawn(async move {
        loop {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut request = Vec::new();
            loop {
                let mut chunk = [0; 2048];
                let size = socket.read(&mut chunk).await.unwrap();
                assert!(size > 0);
                request.extend_from_slice(&chunk[..size]);
                let Some(end) = request.windows(4).position(|part| part == b"\r\n\r\n") else {
                    continue;
                };
                let headers = String::from_utf8_lossy(&request[..end]);
                let length = headers.lines().find_map(|line| {
                    let (name, value) = line.split_once(':')?;
                    name.eq_ignore_ascii_case("content-length")
                        .then(|| value.trim().parse::<usize>().unwrap())
                }).unwrap_or(0);
                if request.len() >= end + 4 + length { break; }
            }
            let request = String::from_utf8(request).unwrap().to_ascii_lowercase();
            let (content_type, body, delay) = if request.starts_with("get /v8/management/config.yaml ") {
                let key = if request.contains("authorization: bearer second-management-key\r\n") {
                    "second-client-key"
                } else {
                    assert!(request.contains("authorization: bearer remote-management-key\r\n"));
                    "remote-client-key"
                };
                ("application/yaml", format!("config-version: 8\naccess: {{api-keys: [{key}]}}\n"), yaml_delay)
            } else {
                assert!(request.starts_with("post /v1/chat/completions "), "{request}");
                assert!(request.contains("authorization: bearer remote-client-key\r\n")
                    || request.contains("authorization: bearer second-client-key\r\n"));
                assert!(!request.contains("management-key"));
                ("text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n".to_string(), inference_delay)
            };
            requests.send(request).unwrap();
            tokio::time::sleep(delay).await;
            let response = format!("HTTP/1.1 200 OK\r\nContent-Type: {content_type}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.len());
            let _ = socket.write_all(response.as_bytes()).await;
        }
    });
    let config = GuiConfigFile {
        connection_mode: CoreConnectionMode::Remote,
        remote_management_origin: origin,
        remote_management_key: "remote-management-key".into(),
        ..probe_config(0)
    };
    (config, received, server)
}

#[tokio::test]
async fn concurrent_remote_health_probes_share_yaml_but_isolate_origins_and_credentials() {
    let _guard = REMOTE_CONFIG_TEST_LOCK.lock().await;
    let (config, mut requests, server) = mock_remote_probe_core(Duration::from_millis(20), Duration::ZERO).await;
    let (first, second, third) = tokio::join!(
        probe_core_model(&config, "one", Some(5_000)),
        probe_core_model(&config, "two", Some(5_000)),
        probe_core_model(&config, "three", Some(5_000)),
    );
    assert!(first.is_ok() && second.is_ok() && third.is_ok());
    let mut yaml_reads = 0;
    let mut probes = 0;
    while let Ok(request) = requests.try_recv() {
        if request.starts_with("get ") { yaml_reads += 1; } else { probes += 1; }
    }
    assert_eq!((yaml_reads, probes), (1, 3));

    let changed_credentials = GuiConfigFile {
        remote_management_key: "second-management-key".into(),
        ..config.clone()
    };
    probe_core_model(&changed_credentials, "four", Some(5_000)).await.unwrap();
    assert!(requests.try_recv().unwrap().starts_with("get /v8/management/config.yaml "));
    assert!(requests.try_recv().unwrap().contains("authorization: bearer second-client-key\r\n"));
    // Returning to old credentials must not reuse the newer credentials' client key.
    probe_core_model(&config, "five", Some(5_000)).await.unwrap();
    assert!(requests.try_recv().unwrap().starts_with("get /v8/management/config.yaml "));
    assert!(requests.try_recv().unwrap().contains("authorization: bearer remote-client-key\r\n"));

    let (other, mut other_requests, other_server) = mock_remote_probe_core(Duration::ZERO, Duration::ZERO).await;
    probe_core_model(&other, "six", Some(5_000)).await.unwrap();
    assert!(other_requests.try_recv().unwrap().starts_with("get /v8/management/config.yaml "));
    assert!(other_requests.try_recv().unwrap().starts_with("post /v1/chat/completions "));
    server.abort();
    other_server.abort();
}

#[tokio::test]
async fn remote_health_probe_timeout_includes_discovery_and_inference() {
    let _guard = REMOTE_CONFIG_TEST_LOCK.lock().await;
    // First stall discovery itself, then make each phase fit individually but not together.
    for (yaml_ms, inference_ms, expected_requests) in [(1_500, 0, 1), (650, 650, 2)] {
        let (config, mut requests, server) = mock_remote_probe_core(
            Duration::from_millis(yaml_ms), Duration::from_millis(inference_ms),
        ).await;
        let started = Instant::now();
        let error = probe_core_model(&config, "test-model", Some(1_000)).await.err().unwrap();
        assert_eq!(error, "Health check request timed out");
        assert!(started.elapsed() < Duration::from_millis(1_600));
        let mut count = 0;
        while requests.try_recv().is_ok() { count += 1; }
        assert_eq!(count, expected_requests);
        server.abort();
    }
}

#[tokio::test]
async fn concurrent_remote_config_failures_are_cached_too() {
    let _guard = REMOTE_CONFIG_TEST_LOCK.lock().await;
    for (status, body, expected) in [
        ("401 Unauthorized", "denied", "401"),
        ("200 OK", "access: [", "Failed to parse"),
    ] {
        let (port, server) = mock_core_response(status, "application/yaml", body, "").await;
        let config = GuiConfigFile {
            connection_mode: CoreConnectionMode::Remote,
            remote_management_origin: format!("http://127.0.0.1:{port}"),
            remote_management_key: "remote-management-key".into(),
            ..probe_config(0)
        };
        let (first, second, third) = tokio::join!(
            crate::core_connection::remote_core_config_document(&config),
            crate::core_connection::remote_core_config_document(&config),
            crate::core_connection::remote_core_config_document(&config),
        );
        let error = first.unwrap_err();
        assert!(error.contains(expected), "{error}");
        assert_eq!(second.unwrap_err(), error);
        assert_eq!(third.unwrap_err(), error);
        assert!(server.await.unwrap().starts_with("GET /v8/management/config.yaml "));
    }
}
