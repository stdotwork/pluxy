package sdk

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func burst(t *testing.T, client *http.Client, url string, n int, gate *sync.WaitGroup) {
	t.Helper()
	gate.Add(n)
	var done sync.WaitGroup
	for i := 0; i < n; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			resp, err := client.Get(url)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			if _, err := io.Copy(io.Discard, resp.Body); err != nil {
				t.Error(err)
			}
		}()
	}
	done.Wait()
}

func connsAfterBursts(t *testing.T, base *http.Transport, n int) (first, second int64) {
	t.Helper()
	var accepted atomic.Int64
	var gate sync.WaitGroup
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gate.Done()
		gate.Wait()
		io.WriteString(w, "ok")
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			accepted.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	t.Cleanup(base.CloseIdleConnections)
	client := &http.Client{Transport: Upstream(base)}
	burst(t, client, srv.URL, n, &gate)
	first = accepted.Load()
	burst(t, client, srv.URL, n, &gate)
	return first, accepted.Load() - first
}

func TestUpstreamKeepsIdleConnectionsPerHost(t *testing.T) {
	const n = 8
	first, second := connsAfterBursts(t, &http.Transport{MaxIdleConns: 100}, n)
	if first != n {
		t.Fatalf("first burst opened %d connections, want %d", first, n)
	}
	if second != 0 {
		t.Fatalf("second burst opened %d new connections, want 0 (the first burst's %d should be idle)", second, n)
	}
}

func TestUpstreamKeepsExplicitIdleConnsPerHost(t *testing.T) {
	const n = 8
	first, second := connsAfterBursts(t, &http.Transport{MaxIdleConns: 100, MaxIdleConnsPerHost: 3}, n)
	if first != n || second != n-3 {
		t.Fatalf("connections opened: first burst %d (want %d), second %d (want %d)", first, n, second, n-3)
	}
}

func TestUpstreamLeavesUnlimitedIdleConnsAlone(t *testing.T) {
	base := &http.Transport{}
	Upstream(base)
	if base.MaxIdleConnsPerHost != 0 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want 0", base.MaxIdleConnsPerHost)
	}
}
