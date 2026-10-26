package api

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// benchSSEEvents returns Responses-style SSE deltas whose text mixes common words and random
// identifiers, so they compress like real model output.
func benchSSEEvents(n int) []string {
	r := rand.New(rand.NewSource(1))
	words := strings.Fields("the function return error context request response model input output tool call result value index token cache stream event message content type text file path line")
	events := make([]string, n)
	for i := range events {
		var delta strings.Builder
		for j, k := 0, 4+r.Intn(12); j < k; j++ {
			delta.WriteString(words[r.Intn(len(words))])
			if r.Intn(10) == 0 {
				fmt.Fprintf(&delta, "_%x", r.Int31())
			}
			delta.WriteByte(' ')
		}
		events[i] = fmt.Sprintf("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":%d,\"item_id\":\"msg_%x\",\"delta\":%q}\n\n", i, r.Int63(), delta.String())
	}
	return events
}

func benchCompressionEngine(events []string, hold <-chan struct{}, started *sync.WaitGroup) *gin.Engine {
	engine := gin.New()
	engine.Use(responseCompressionMiddleware(&config.Config{}))
	engine.GET("/sse", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		for i, event := range events {
			_, _ = c.Writer.WriteString(event)
			c.Writer.Flush()
			if i == 0 && hold != nil {
				started.Done()
				<-hold
			}
		}
	})
	return engine
}

// BenchmarkResponseCompressionSSE serves whole compressed SSE responses concurrently.
func BenchmarkResponseCompressionSSE(b *testing.B) {
	gin.SetMode(gin.ReleaseMode)
	for _, n := range []int{20, 500} {
		events := benchSSEEvents(n)
		for _, encoding := range []string{"zstd", "gzip"} {
			b.Run(fmt.Sprintf("%s/%devents", encoding, n), func(b *testing.B) {
				engine := benchCompressionEngine(events, nil, nil)
				var wire int64
				var mu sync.Mutex
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						req := httptest.NewRequest(http.MethodGet, "/sse", nil)
						req.Header.Set("Accept-Encoding", encoding)
						rec := httptest.NewRecorder()
						engine.ServeHTTP(rec, req)
						if rec.Header().Get("Content-Encoding") != encoding {
							b.Fatal("response not compressed")
						}
						mu.Lock()
						wire = int64(rec.Body.Len())
						mu.Unlock()
					}
				})
				b.ReportMetric(float64(wire), "wire-B")
			})
		}
	}
}

// BenchmarkResponseCompressionOpenStreams measures the heap each open compressed SSE stream
// holds: 256 clients connected at once, each paused after its first event.
func BenchmarkResponseCompressionOpenStreams(b *testing.B) {
	gin.SetMode(gin.ReleaseMode)
	const streams = 256
	events := benchSSEEvents(2)
	for _, encoding := range []string{"zstd", "gzip"} {
		b.Run(encoding, func(b *testing.B) {
			var perStream float64
			for i := 0; i < b.N; i++ {
				hold := make(chan struct{})
				var started, done sync.WaitGroup
				engine := benchCompressionEngine(events, hold, &started)
				runtime.GC()
				var before, during runtime.MemStats
				runtime.ReadMemStats(&before)
				started.Add(streams)
				for s := 0; s < streams; s++ {
					done.Add(1)
					go func() {
						defer done.Done()
						req := httptest.NewRequest(http.MethodGet, "/sse", nil)
						req.Header.Set("Accept-Encoding", encoding)
						engine.ServeHTTP(httptest.NewRecorder(), req)
					}()
				}
				started.Wait()
				runtime.GC()
				runtime.ReadMemStats(&during)
				perStream += float64(int64(during.HeapInuse)-int64(before.HeapInuse)) / streams
				close(hold)
				done.Wait()
			}
			b.ReportMetric(perStream/float64(b.N)/1024, "heap-KiB/stream")
		})
	}
}
