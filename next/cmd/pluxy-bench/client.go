package main

import (
	"bufio"
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

// Marker represents a parsed bm:<reqID>:<seq>:<sendNanos>; token.
type Marker struct {
	ReqID     string
	Seq       int
	SendNanos int64
}

var markerPrefix = []byte("bm:")

// ScanMarkers finds all well-formed bm:<reqID>:<seq>:<sendNanos>; markers inside data.
func ScanMarkers(data []byte) []Marker {
	var out []Marker
	pos := 0
	for pos < len(data) {
		idx := bytes.Index(data[pos:], markerPrefix)
		if idx < 0 {
			break
		}
		start := pos + idx + len(markerPrefix)
		semi := bytes.IndexByte(data[start:], ';')
		if semi < 0 {
			break
		}
		// Limit field scan to reasonable length so malformed text doesn't skip real markers.
		if semi > 96 {
			pos = start
			continue
		}
		body := data[start : start+semi]
		c1 := bytes.IndexByte(body, ':')
		if c1 <= 0 {
			pos = start
			continue
		}
		c2Rel := bytes.IndexByte(body[c1+1:], ':')
		if c2Rel <= 0 {
			pos = start
			continue
		}
		c2 := c1 + 1 + c2Rel
		reqID := string(body[:c1])
		seq, err1 := strconv.Atoi(string(body[c1+1 : c2]))
		sendNanos, err2 := strconv.ParseInt(string(body[c2+1:]), 10, 64)
		if err1 != nil || err2 != nil {
			pos = start
			continue
		}
		out = append(out, Marker{
			ReqID:     reqID,
			Seq:       seq,
			SendNanos: sendNanos,
		})
		pos = start + semi + 1
	}
	return out
}

// SSEEventCallback is called for each SSE event payload as soon as its terminating blank line
// arrives, with arrivalNanos stamped immediately upon reading.
type SSEEventCallback func(data []byte, arrivalNanos int64)

// readLineComplete reads one '\n'-terminated line (without trailing '\r' or '\n') from r,
// accumulating across bufio.ErrBufferFull if a line spans multiple buffer fills.
func readLineComplete(r *bufio.Reader) ([]byte, error) {
	var full []byte
	for {
		frag, err := r.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			full = append(full, frag...)
			continue
		}
		if len(full) > 0 {
			full = append(full, frag...)
			frag = full
		}
		frag = bytes.TrimSuffix(frag, []byte("\n"))
		frag = bytes.TrimSuffix(frag, []byte("\r"))
		return frag, err
	}
}

// ReadSSE reads an SSE stream from r and invokes cb for each non-empty event's joined data.
func ReadSSE(r io.Reader, cb SSEEventCallback) error {
	br := bufio.NewReaderSize(r, 16*1024)
	var dataBuf bytes.Buffer
	flushEvent := func() {
		if dataBuf.Len() == 0 {
			return
		}
		arrival := time.Now().UnixNano()
		// Strip trailing '\n' from last data line join.
		b := dataBuf.Bytes()
		if len(b) > 0 && b[len(b)-1] == '\n' {
			b = b[:len(b)-1]
		}
		cb(b, arrival)
		dataBuf.Reset()
	}

	for {
		line, err := readLineComplete(br)
		if len(line) == 0 {
			flushEvent()
		} else if bytes.HasPrefix(line, []byte("data:")) {
			payload := line[len("data:"):]
			if len(payload) > 0 && payload[0] == ' ' {
				payload = payload[1:]
			}
			dataBuf.Write(payload)
			dataBuf.WriteByte('\n')
		}
		if err != nil {
			if err == io.EOF {
				flushEvent()
				return nil
			}
			return err
		}
	}
}

type sseTextExtract struct {
	Type  string `json:"type"`
	Delta *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

func extractSSEText(data []byte) string {
	if bytes.Equal(data, []byte("[DONE]")) {
		return ""
	}
	var ex sseTextExtract
	if json.Unmarshal(data, &ex) != nil {
		return ""
	}
	if ex.Type == "content_block_delta" && ex.Delta != nil && ex.Delta.Type == "text_delta" {
		return ex.Delta.Text
	}
	if len(ex.Choices) > 0 {
		return ex.Choices[0].Delta.Content
	}
	return ""
}

type nonStreamExtract struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func extractNonStreamText(body []byte) string {
	var ex nonStreamExtract
	if json.Unmarshal(body, &ex) != nil {
		return ""
	}
	if len(ex.Content) > 0 {
		var b strings.Builder
		for _, c := range ex.Content {
			if c.Type == "text" {
				b.WriteString(c.Text)
			}
		}
		return b.String()
	}
	if len(ex.Choices) > 0 {
		return ex.Choices[0].Message.Content
	}
	return ""
}

// ScenarioSpec defines one benchmark scenario's workload and mock timing.
type ScenarioSpec struct {
	Name        string
	Stream      bool
	Translate   bool
	Concurrency int
	Requests    int           // fixed request count if > 0
	Duration    time.Duration // closed-loop duration if > 0
	BodyKB      int
	TTFT        time.Duration
	Chunks      int
	Interval    time.Duration
	Latency     time.Duration
}

// SideRunResult holds the raw measurements of one run of one side (direct or proxy) for a scenario.
type SideRunResult struct {
	ReqPathP50MS    float64 `json:"req_path_p50_ms"`
	ReqPathP99MS    float64 `json:"req_path_p99_ms"`
	TTFTP50MS       float64 `json:"ttft_p50_ms,omitempty"`
	TTFTP99MS       float64 `json:"ttft_p99_ms,omitempty"`
	ChunkSamples    int     `json:"chunk_samples,omitempty"` // chunk markers the client read; 0 means no chunk delay was measured
	ChunkDelayP50MS float64 `json:"chunk_delay_p50_ms,omitempty"`
	ChunkDelayP99MS float64 `json:"chunk_delay_p99_ms,omitempty"`
	TotalP50MS      float64 `json:"total_p50_ms"`
	TotalP99MS      float64 `json:"total_p99_ms"`
	RPS             float64 `json:"rps"`
	Completed       int     `json:"completed"`
	Errors          int     `json:"errors"`
	UpstreamConns   int64   `json:"upstream_conns"`
	FidelityFails   int     `json:"fidelity_failures"`
	ElapsedS        float64 `json:"elapsed_s"`

	// Footprint metrics (populated on proxy side on Linux).
	Footprint *SideFootprint `json:"footprint,omitempty"`
}

type reqSample struct {
	reqPathMS    float64
	hasReqPath   bool
	ttftMS       float64
	hasTTFT      bool
	chunkDelayMS []float64
	totalMS      float64
	err          bool
	fidelityFail bool
}

var globalReqSeq atomic.Uint64

func nextReqID() string {
	n := globalReqSeq.Add(1)
	return fmt.Sprintf("%08x", uint32(n))
}

func newBenchTransport(concurrency int) *http.Transport {
	return &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        max(concurrency, 1),
		MaxIdleConnsPerHost: max(concurrency, 1),
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}
}

// RunSide executes spec once against targetURL (either mock direct or core proxy), collecting
// latency, throughput, fidelity, and mock upstream connection counts.
func RunSide(
	ctx context.Context,
	spec ScenarioSpec,
	isProxy bool,
	targetURL string,
	apiKey string,
	model string,
	mock *MockServer,
) (SideRunResult, error) {
	useChat := !isProxy && spec.Translate
	var tmpl *RequestTemplate
	var err error
	if useChat {
		tmpl, err = BuildChatTemplate(model, spec.Stream, spec.BodyKB)
	} else {
		tmpl, err = BuildMessagesTemplate(model, spec.Stream, spec.BodyKB)
	}
	if err != nil {
		return SideRunResult{}, err
	}

	endpoint := strings.TrimRight(targetURL, "/") + "/v1/messages"
	if useChat {
		endpoint = strings.TrimRight(targetURL, "/") + "/v1/chat/completions"
	}

	tr := newBenchTransport(spec.Concurrency)
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		Timeout:   120 * time.Second,
	}

	mock.ResetRecords()
	mock.SetTiming(MockTiming{
		TTFT:     spec.TTFT,
		Chunks:   spec.Chunks,
		Interval: spec.Interval,
		Latency:  spec.Latency,
	})
	connsBefore := mock.NewConns()

	doOne := func() reqSample {
		reqID := nextReqID()
		body := tmpl.Materialize(reqID)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return reqSample{err: true}
		}
		req.Header.Set("Content-Type", "application/json")
		if useChat {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		} else {
			req.Header.Set("X-Api-Key", apiKey)
			req.Header.Set("Anthropic-Version", "2023-06-01")
		}

		startNanos := time.Now().UnixNano()
		resp, err := client.Do(req)
		if err != nil {
			mock.TakeRecord(reqID)
			return reqSample{err: true}
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			_, _ = io.Copy(io.Discard, resp.Body)
			mock.TakeRecord(reqID)
			return reqSample{err: true}
		}

		var sample reqSample
		var joined strings.Builder
		var markers []Marker

		if spec.Stream {
			var firstMarkerNanos int64
			err := ReadSSE(resp.Body, func(data []byte, arrivalNanos int64) {
				if txt := extractSSEText(data); txt != "" {
					joined.WriteString(txt)
				}
				found := ScanMarkers(data)
				for _, mk := range found {
					if firstMarkerNanos == 0 {
						firstMarkerNanos = arrivalNanos
					}
					delayMS := float64(arrivalNanos-mk.SendNanos) / 1e6
					sample.chunkDelayMS = append(sample.chunkDelayMS, delayMS)
					markers = append(markers, mk)
				}
			})
			lastByteNanos := time.Now().UnixNano()
			if err != nil {
				mock.TakeRecord(reqID)
				return reqSample{err: true}
			}
			sample.totalMS = float64(lastByteNanos-startNanos) / 1e6
			if firstMarkerNanos > 0 {
				sample.ttftMS = float64(firstMarkerNanos-startNanos) / 1e6
				sample.hasTTFT = true
			}
		} else {
			raw, err := io.ReadAll(resp.Body)
			lastByteNanos := time.Now().UnixNano()
			if err != nil {
				mock.TakeRecord(reqID)
				return reqSample{err: true}
			}
			sample.totalMS = float64(lastByteNanos-startNanos) / 1e6
			txt := extractNonStreamText(raw)
			joined.WriteString(txt)
			markers = ScanMarkers([]byte(txt))
		}

		recvNanos, expected, ok := mock.TakeRecord(reqID)
		if ok && recvNanos > 0 {
			sample.reqPathMS = float64(recvNanos-startNanos) / 1e6
			sample.hasReqPath = true
		}

		wantChunks := 1
		if spec.Stream {
			wantChunks = spec.Chunks
		}
		if !ok || len(markers) != wantChunks || joined.String() != expected {
			sample.fidelityFail = true
		} else {
			for i, mk := range markers {
				if mk.ReqID != reqID || mk.Seq != i {
					sample.fidelityFail = true
					break
				}
			}
		}
		return sample
	}

	var mu sync.Mutex
	var samples []reqSample

	startWall := time.Now()
	if spec.Requests > 0 {
		var nextIdx atomic.Int64
		var wg sync.WaitGroup
		workers := max(spec.Concurrency, 1)
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func() {
				defer wg.Done()
				var local []reqSample
				for {
					idx := int(nextIdx.Add(1) - 1)
					if idx >= spec.Requests || ctx.Err() != nil {
						break
					}
					local = append(local, doOne())
				}
				mu.Lock()
				samples = append(samples, local...)
				mu.Unlock()
			}()
		}
		wg.Wait()
	} else {
		deadline := time.Now().Add(spec.Duration)
		var wg sync.WaitGroup
		workers := max(spec.Concurrency, 1)
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func() {
				defer wg.Done()
				var local []reqSample
				for time.Now().Before(deadline) && ctx.Err() == nil {
					local = append(local, doOne())
				}
				mu.Lock()
				samples = append(samples, local...)
				mu.Unlock()
			}()
		}
		wg.Wait()
	}
	elapsed := time.Since(startWall)
	connsAfter := mock.NewConns()

	var reqPaths, ttfts, chunkDelays, totals []float64
	var completed, errs, fidelityFails int
	for _, s := range samples {
		if s.err {
			errs++
			continue
		}
		completed++
		if s.fidelityFail {
			fidelityFails++
		}
		if s.hasReqPath {
			reqPaths = append(reqPaths, s.reqPathMS)
		}
		if s.hasTTFT {
			ttfts = append(ttfts, s.ttftMS)
		}
		chunkDelays = append(chunkDelays, s.chunkDelayMS...)
		totals = append(totals, s.totalMS)
	}

	res := SideRunResult{
		ReqPathP50MS:  Percentile(reqPaths, 0.50),
		ReqPathP99MS:  Percentile(reqPaths, 0.99),
		TotalP50MS:    Percentile(totals, 0.50),
		TotalP99MS:    Percentile(totals, 0.99),
		Completed:     completed,
		Errors:        errs,
		UpstreamConns: connsAfter - connsBefore,
		FidelityFails: fidelityFails,
		ElapsedS:      elapsed.Seconds(),
	}
	if elapsed.Seconds() > 0 {
		res.RPS = float64(completed) / elapsed.Seconds()
	}
	if spec.Stream {
		res.TTFTP50MS = Percentile(ttfts, 0.50)
		res.TTFTP99MS = Percentile(ttfts, 0.99)
		res.ChunkSamples = len(chunkDelays)
		res.ChunkDelayP50MS = Percentile(chunkDelays, 0.50)
		res.ChunkDelayP99MS = Percentile(chunkDelays, 0.99)
	}
	return res, nil
}
