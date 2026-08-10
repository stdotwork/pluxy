use super::{build_http_client_with_proxy, is_loopback_host, GuiConfigState, APP_USER_AGENT};
use futures_util::StreamExt;
use serde::{Deserialize, Serialize};
use std::{
    collections::HashMap,
    time::{Duration, Instant},
};

const MAX_PROVIDER_HEALTH_STREAM_BYTES: usize = 256 * 1024;

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct ProviderHealthProbeRequest {
    url: String,
    header: HashMap<String, String>,
    data: String,
    protocol: String,
    timeout_ms: Option<u64>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct ProviderHealthProbeResponse {
    first_token_latency_ms: u64,
}

fn provider_health_value_has_text(value: Option<&serde_json::Value>) -> bool {
    match value {
        Some(serde_json::Value::String(text)) => !text.trim().is_empty(),
        Some(serde_json::Value::Array(items)) => items
            .iter()
            .any(|item| provider_health_value_has_text(Some(item))),
        Some(serde_json::Value::Object(object)) => ["text", "content"]
            .iter()
            .any(|key| provider_health_value_has_text(object.get(*key))),
        _ => false,
    }
}

fn provider_health_json_has_text(protocol: &str, value: &serde_json::Value) -> bool {
    match protocol {
        "openai-chat" => value
            .get("choices")
            .and_then(serde_json::Value::as_array)
            .is_some_and(|choices| {
                choices.iter().any(|choice| {
                    provider_health_value_has_text(choice.pointer("/delta/content"))
                        || provider_health_value_has_text(
                            choice.pointer("/delta/reasoning_content"),
                        )
                        || provider_health_value_has_text(choice.pointer("/delta/reasoning"))
                        || provider_health_value_has_text(choice.pointer("/delta/thinking"))
                        || provider_health_value_has_text(choice.pointer("/message/content"))
                })
            }),
        "openai-responses" => {
            (matches!(
                value.get("type").and_then(serde_json::Value::as_str),
                Some(
                    "response.output_text.delta"
                        | "response.reasoning_text.delta"
                        | "response.reasoning_summary_text.delta"
                )
            ) && provider_health_value_has_text(value.get("delta")))
                || value
                    .get("output")
                    .and_then(serde_json::Value::as_array)
                    .is_some_and(|output| {
                        output.iter().any(|item| {
                            item.get("content")
                                .and_then(serde_json::Value::as_array)
                                .is_some_and(|content| {
                                    content.iter().any(|part| {
                                        provider_health_value_has_text(part.get("text"))
                                    })
                                })
                        })
                    })
        }
        "claude" => {
            provider_health_value_has_text(value.pointer("/delta/text"))
                || provider_health_value_has_text(value.pointer("/delta/thinking"))
                || value
                    .get("content")
                    .and_then(serde_json::Value::as_array)
                    .is_some_and(|content| {
                        content
                            .iter()
                            .any(|part| provider_health_value_has_text(part.get("text")))
                    })
        }
        "gemini" => value
            .get("candidates")
            .and_then(serde_json::Value::as_array)
            .is_some_and(|candidates| {
                candidates.iter().any(|candidate| {
                    candidate
                        .pointer("/content/parts")
                        .and_then(serde_json::Value::as_array)
                        .is_some_and(|parts| {
                            parts
                                .iter()
                                .any(|part| provider_health_value_has_text(part.get("text")))
                        })
                })
            }),
        _ => false,
    }
}

pub(crate) fn provider_health_stream_has_text(protocol: &str, bytes: &[u8]) -> bool {
    let text = String::from_utf8_lossy(bytes);
    text.lines().any(|line| {
        let line = line.trim();
        let data = line.strip_prefix("data:").map(str::trim).unwrap_or(line);
        if data.is_empty() || data == "[DONE]" {
            return false;
        }
        serde_json::from_str::<serde_json::Value>(data)
            .ok()
            .is_some_and(|value| provider_health_json_has_text(protocol, &value))
    })
}

pub(crate) fn provider_health_content_type_is_streaming(content_type: &str) -> bool {
    let content_type = content_type.to_ascii_lowercase();
    content_type.contains("text/event-stream")
        || content_type.contains("application/x-ndjson")
        || content_type.contains("application/json-seq")
}

#[tauri::command]
pub(crate) async fn provider_health_probe(
    gui_config_state: tauri::State<'_, GuiConfigState>,
    request: ProviderHealthProbeRequest,
) -> Result<ProviderHealthProbeResponse, String> {
    let url = reqwest::Url::parse(request.url.trim())
        .map_err(|error| format!("健康检测地址无效: {error}"))?;
    if !matches!(url.scheme(), "http" | "https") || url.host_str().is_none() {
        return Err("健康检测仅支持 HTTP 或 HTTPS 地址".to_string());
    }
    if !matches!(
        request.protocol.as_str(),
        "openai-chat" | "openai-responses" | "claude" | "gemini"
    ) {
        return Err("不支持的健康检测协议".to_string());
    }
    if request.data.len() > 64 * 1024 {
        return Err("健康检测请求体过大".to_string());
    }

    let timeout = Duration::from_millis(request.timeout_ms.unwrap_or(15_000).clamp(1_000, 120_000));
    let config = gui_config_state.snapshot()?;
    let proxy_url = url
        .host_str()
        .filter(|host| !is_loopback_host(host))
        .map(|_| config.proxy_url.as_str())
        .unwrap_or_default();
    let client = build_http_client_with_proxy(
        reqwest::Client::builder()
            .connect_timeout(Duration::from_secs(10))
            .timeout(timeout),
        proxy_url,
        "创建健康检测客户端失败",
    )?;
    let mut headers = reqwest::header::HeaderMap::new();
    for (name, value) in request.header {
        let name = reqwest::header::HeaderName::from_bytes(name.as_bytes())
            .map_err(|error| format!("健康检测请求头名称无效: {error}"))?;
        let value = reqwest::header::HeaderValue::from_str(&value)
            .map_err(|error| format!("健康检测请求头值无效: {error}"))?;
        headers.insert(name, value);
    }
    if !headers.contains_key(reqwest::header::USER_AGENT) {
        headers.insert(
            reqwest::header::USER_AGENT,
            reqwest::header::HeaderValue::from_static(APP_USER_AGENT),
        );
    }

    let started_at = Instant::now();
    let response = client
        .post(url)
        .headers(headers)
        .body(request.data)
        .send()
        .await
        .map_err(|error| format!("健康检测请求失败: {error}"))?;
    let status = response.status();
    if !status.is_success() {
        let detail = response.text().await.unwrap_or_default();
        let detail = detail.trim();
        return Err(if detail.is_empty() {
            format!("上游返回 HTTP {}", status.as_u16())
        } else {
            format!("上游返回 HTTP {}: {}", status.as_u16(), detail)
        });
    }
    let content_type = response
        .headers()
        .get(reqwest::header::CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .unwrap_or("")
        .to_ascii_lowercase();
    if !provider_health_content_type_is_streaming(&content_type) {
        return Err("上游未返回流式响应，无法测量首字延迟".to_string());
    }

    let mut stream = response.bytes_stream();
    let mut received = Vec::new();
    while let Some(chunk) = stream.next().await {
        let chunk = chunk.map_err(|error| format!("读取健康检测流失败: {error}"))?;
        if received.len().saturating_add(chunk.len()) > MAX_PROVIDER_HEALTH_STREAM_BYTES {
            return Err("健康检测在限制范围内未收到模型首字".to_string());
        }
        received.extend_from_slice(&chunk);
        if provider_health_stream_has_text(&request.protocol, &received) {
            return Ok(ProviderHealthProbeResponse {
                first_token_latency_ms: started_at.elapsed().as_millis().max(1) as u64,
            });
        }
    }
    Err("健康检测未收到模型首字".to_string())
}
