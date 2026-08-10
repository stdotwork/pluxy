use super::*;

#[test]
fn provider_health_stream_detects_only_real_model_text() {
    assert!(provider_health_content_type_is_streaming(
        "text/event-stream; charset=utf-8"
    ));
    assert!(provider_health_content_type_is_streaming(
        "application/x-ndjson"
    ));
    assert!(!provider_health_content_type_is_streaming(
        "application/json"
    ));

    let openai_metadata = br#"data: {"choices":[{"delta":{"role":"assistant","content":""}}]}

"#;
    let openai_text = br#"data: {"choices":[{"delta":{"content":"H"}}]}

"#;
    assert!(!provider_health_stream_has_text(
        "openai-chat",
        openai_metadata
    ));
    assert!(provider_health_stream_has_text("openai-chat", openai_text));
    let openai_reasoning = br#"data: {"choices":[{"delta":{"reasoning_content":"R"}}]}

"#;
    assert!(provider_health_stream_has_text(
        "openai-chat",
        openai_reasoning
    ));

    let responses_metadata = br#"event: response.created
data: {"type":"response.created"}

"#;
    let responses_text = br#"event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"H"}

"#;
    assert!(!provider_health_stream_has_text(
        "openai-responses",
        responses_metadata
    ));
    assert!(provider_health_stream_has_text(
        "openai-responses",
        responses_text
    ));

    let claude_metadata = br#"event: message_start
data: {"type":"message_start"}

"#;
    let claude_text = br#"event: content_block_delta
data: {"delta":{"type":"text_delta","text":"H"}}

"#;
    assert!(!provider_health_stream_has_text("claude", claude_metadata));
    assert!(provider_health_stream_has_text("claude", claude_text));

    let gemini_text = br#"data: {"candidates":[{"content":{"parts":[{"text":"H"}]}}]}

"#;
    assert!(provider_health_stream_has_text("gemini", gemini_text));
}
