package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MockTiming configures the mock upstream's response timing for a scenario.
type MockTiming struct {
	TTFT     time.Duration
	Chunks   int
	Interval time.Duration
	Latency  time.Duration
}

type mockRecord struct {
	recvNanos int64
	expected  string
}

// MockServer is an in-process HTTP upstream serving Anthropic Messages, OpenAI Chat Completions,
// and /v1/models on 127.0.0.1 with deadline-paced SSE chunk delivery.
type MockServer struct {
	srv      *http.Server
	ln       net.Listener
	addr     string
	newConns atomic.Int64

	timingMu sync.RWMutex
	timing   MockTiming

	recMu   sync.Mutex
	records map[string]*mockRecord
}

// StartMock starts the mock upstream on addr (default "127.0.0.1:0" when empty).
func StartMock(addr string) (*MockServer, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("mock listen %s: %w", addr, err)
	}
	m := &MockServer{
		ln:      ln,
		addr:    ln.Addr().String(),
		records: make(map[string]*mockRecord),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", m.handleModels)
	mux.HandleFunc("/v1/messages", m.handleMessages)
	mux.HandleFunc("/v1/chat/completions", m.handleChat)
	m.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				m.newConns.Add(1)
			}
		},
	}
	go func() {
		_ = m.srv.Serve(ln)
	}()
	return m, nil
}

// Addr returns the mock's listening host:port.
func (m *MockServer) Addr() string {
	return m.addr
}

// RootURL returns the mock's http://host:port root URL.
func (m *MockServer) RootURL() string {
	return "http://" + m.addr
}

// BaseURL returns the mock's http://host:port/v1 base URL.
func (m *MockServer) BaseURL() string {
	return "http://" + m.addr + "/v1"
}

// SetTiming updates the mock's response timing for the next scenario.
func (m *MockServer) SetTiming(t MockTiming) {
	m.timingMu.Lock()
	m.timing = t
	m.timingMu.Unlock()
}

func (m *MockServer) getTiming() MockTiming {
	m.timingMu.RLock()
	t := m.timing
	m.timingMu.RUnlock()
	return t
}

// NewConns returns the number of new TCP connections observed since the start of the mock.
func (m *MockServer) NewConns() int64 {
	return m.newConns.Load()
}

// ResetRecords clears any leftover per-request records between scenarios.
func (m *MockServer) ResetRecords() {
	m.recMu.Lock()
	clear(m.records)
	m.recMu.Unlock()
}

// TakeRecord retrieves and removes the mock-side record for reqID.
func (m *MockServer) TakeRecord(reqID string) (recvNanos int64, expected string, ok bool) {
	m.recMu.Lock()
	rec := m.records[reqID]
	if rec != nil {
		delete(m.records, reqID)
	}
	m.recMu.Unlock()
	if rec == nil {
		return 0, "", false
	}
	return rec.recvNanos, rec.expected, true
}

func (m *MockServer) recordRecv(reqID string, recvNanos int64) {
	if reqID == "" {
		return
	}
	m.recMu.Lock()
	m.records[reqID] = &mockRecord{recvNanos: recvNanos}
	m.recMu.Unlock()
}

func (m *MockServer) setExpected(reqID, expected string) {
	if reqID == "" {
		return
	}
	m.recMu.Lock()
	if rec := m.records[reqID]; rec != nil {
		rec.expected = expected
	}
	m.recMu.Unlock()
}

// Close shuts down the mock server.
func (m *MockServer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.srv.Shutdown(ctx)
}

func (m *MockServer) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"bench-messages","object":"model","created":1700000000,"owned_by":"bench"},{"id":"bench-chat","object":"model","created":1700000000,"owned_by":"bench"}]}`)
}

var bmReqPrefix = []byte("bm-req:")

func extractReqID(body []byte) string {
	idx := bytes.Index(body, bmReqPrefix)
	if idx < 0 {
		return ""
	}
	rest := body[idx+len(bmReqPrefix):]
	semi := bytes.IndexByte(rest, ';')
	if semi <= 0 || semi > 64 {
		return ""
	}
	return string(rest[:semi])
}

func isStreamRequest(body []byte) bool {
	return bytes.Contains(body, []byte(`"stream":true`)) || bytes.Contains(body, []byte(`"stream": true`))
}

func sleepUntil(ctx context.Context, target time.Time) bool {
	d := time.Until(target)
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func sleepDuration(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// deltaFiller pads each streamed text delta with 32 bytes of realistic letters and spaces after
// its marker so SSE frames match model-sized chunks while ScanMarkers still parses every marker.
const deltaFiller = " the quick brown fox jumps along"

func formatMarker(reqID string, seq int, sendNanos int64) string {
	var b strings.Builder
	b.Grow(48)
	b.WriteString("bm:")
	b.WriteString(reqID)
	b.WriteByte(':')
	b.WriteString(strconv.Itoa(seq))
	b.WriteByte(':')
	b.WriteString(strconv.FormatInt(sendNanos, 10))
	b.WriteString(";")
	return b.String()
}

func (m *MockServer) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	recvNanos := time.Now().UnixNano()
	reqID := extractReqID(body)
	m.recordRecv(reqID, recvNanos)

	t := m.getTiming()
	if !isStreamRequest(body) {
		if !sleepDuration(r.Context(), t.Latency) {
			return
		}
		text := formatMarker(reqID, 0, time.Now().UnixNano())
		m.setExpected(reqID, text)

		resp := struct {
			ID         string `json:"id"`
			Type       string `json:"type"`
			Role       string `json:"role"`
			Model      string `json:"model"`
			Content    []any  `json:"content"`
			StopReason string `json:"stop_reason"`
			Usage      any    `json:"usage"`
		}{
			ID:    "msg_bench_" + reqID,
			Type:  "message",
			Role:  "assistant",
			Model: "bench-messages",
			Content: []any{
				map[string]string{"type": "text", "text": text},
			},
			StopReason: "end_turn",
			Usage:      map[string]int{"input_tokens": 100, "output_tokens": 10},
		}
		out, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	start := time.Now()
	fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_bench_%s\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"bench-messages\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":100,\"output_tokens\":1}}}\n\n", reqID)
	fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	flusher.Flush()

	var expected strings.Builder
	expected.Grow(t.Chunks * (48 + len(deltaFiller)))
	for i := 0; i < t.Chunks; i++ {
		due := start.Add(t.TTFT + time.Duration(i)*t.Interval)
		if !sleepUntil(r.Context(), due) {
			return
		}
		chunkText := formatMarker(reqID, i, time.Now().UnixNano()) + deltaFiller
		expected.WriteString(chunkText)
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", chunkText)
		flusher.Flush()
	}

	m.setExpected(reqID, expected.String())
	fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":%d}}\n\n", t.Chunks)
	fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	flusher.Flush()
}

func (m *MockServer) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	recvNanos := time.Now().UnixNano()
	reqID := extractReqID(body)
	m.recordRecv(reqID, recvNanos)

	t := m.getTiming()
	if !isStreamRequest(body) {
		if !sleepDuration(r.Context(), t.Latency) {
			return
		}
		text := formatMarker(reqID, 0, time.Now().UnixNano())
		m.setExpected(reqID, text)

		resp := struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			Model   string `json:"model"`
			Choices []any  `json:"choices"`
			Usage   any    `json:"usage"`
		}{
			ID:      "chatcmpl-bench-" + reqID,
			Object:  "chat.completion",
			Created: 1700000000,
			Model:   "bench-chat",
			Choices: []any{
				map[string]any{
					"index":         0,
					"message":       map[string]string{"role": "assistant", "content": text},
					"finish_reason": "stop",
				},
			},
			Usage: map[string]int{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110},
		}
		out, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	start := time.Now()
	head := fmt.Sprintf(`"id":"chatcmpl-bench-%s","object":"chat.completion.chunk","created":1700000000,"model":"bench-chat"`, reqID)
	fmt.Fprintf(w, "data: {%s,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n", head)
	flusher.Flush()

	var expected strings.Builder
	expected.Grow(t.Chunks * (48 + len(deltaFiller)))
	for i := 0; i < t.Chunks; i++ {
		due := start.Add(t.TTFT + time.Duration(i)*t.Interval)
		if !sleepUntil(r.Context(), due) {
			return
		}
		chunkText := formatMarker(reqID, i, time.Now().UnixNano()) + deltaFiller
		expected.WriteString(chunkText)
		fmt.Fprintf(w, "data: {%s,\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", head, chunkText)
		flusher.Flush()
	}

	m.setExpected(reqID, expected.String())
	fmt.Fprintf(w, "data: {%s,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n", head)
	fmt.Fprintf(w, "data: {%s,\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\n", head, t.Chunks, 100+t.Chunks)
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}
