package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const logTestExt = "claude"

const logTestSecret = "SECRET-0123456789abcdefghij"

func captureModelLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := modelLogOut
	modelLogOut = &buf
	modelLogMu.Lock()
	clear(modelLogLast)
	modelLogMu.Unlock()
	t.Cleanup(func() { modelLogOut = prev })
	return &buf
}

func TestFailedModelReadIsLogged(t *testing.T) {
	buf := captureModelLog(t)
	var status atomic.Int32
	status.Store(401)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status.Load() == 200 {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"id":"claude-test-model","display_name":"T"}],"has_more":false}`)
			return
		}
		w.WriteHeader(int(status.Load()))
		io.WriteString(w, "denied for "+logTestSecret)
	}))
	defer srv.Close()
	settings, _ := json.Marshal(map[string]string{"base_url": srv.URL})
	call := sdk.ModelsCall{Account: protocol.Account{ID: "acct", Settings: settings, Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: logTestSecret}}}
	list := func() {
		t.Helper()
		if _, err := listModels(context.Background(), call); err != nil {
			t.Fatal(err)
		}
	}
	lines := func() int { return strings.Count(buf.String(), "\n") }

	list()
	if lines() != 1 || !strings.Contains(buf.String(), "HTTP 401") || !strings.Contains(buf.String(), "built-in list") {
		t.Fatalf("after a 401 the log is %q, want one line naming HTTP 401", buf.String())
	}
	list()
	if lines() != 1 {
		t.Errorf("the same failure again was logged again: %q", buf.String())
	}
	status.Store(500)
	list()
	if lines() != 2 || !strings.Contains(buf.String(), "HTTP 500") {
		t.Errorf("a different failure was not logged: %q", buf.String())
	}
	status.Store(200)
	list()
	if lines() != 2 {
		t.Errorf("a read that worked was logged: %q", buf.String())
	}
	status.Store(500)
	list()
	if lines() != 3 {
		t.Errorf("a failure after a read that worked was not logged: %q", buf.String())
	}
	if strings.Contains(buf.String(), logTestSecret) || strings.Contains(buf.String(), srv.URL) {
		t.Errorf("the log holds the credential or the URL: %q", buf.String())
	}
}

func TestModelReadLogRedactsCredential(t *testing.T) {
	buf := captureModelLog(t)
	modelReadFailed("acct", &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: logTestSecret}, "request failed: bad "+logTestSecret)
	if got := buf.String(); got == "" || strings.Contains(got, logTestSecret) {
		t.Errorf("log = %q, want one line without the credential", got)
	}
}

func TestModelReadLogCannotReachProvider(t *testing.T) {
	buf := captureModelLog(t)
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	settings, _ := json.Marshal(map[string]string{"base_url": url})
	call := sdk.ModelsCall{Account: protocol.Account{ID: "acct", Settings: settings, Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: logTestSecret}}}
	if _, err := listModels(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	want := logTestExt + ": reading the model list failed (the connection failed); serving the built-in list\n"
	if got := buf.String(); got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

func TestModelReadLogHoldsNoReplyText(t *testing.T) {
	buf := captureModelLog(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			io.WriteString(c, "denied-for-person@example.test-acct-claim-123456\r\n\r\n")
			c.Close()
		}
	}()
	settings, _ := json.Marshal(map[string]string{"base_url": "http://" + ln.Addr().String()})
	call := sdk.ModelsCall{Account: protocol.Account{ID: "acct", Settings: settings, Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: logTestSecret}}}
	if _, err := listModels(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "the request failed") {
		t.Errorf("log = %q, want the failed request logged", got)
	}
	for _, leak := range []string{"person@example.test", "acct-claim-123456", "denied-for", "127.0.0.1"} {
		if strings.Contains(got, leak) {
			t.Errorf("log = %q, holds %q", got, leak)
		}
	}
}

func TestModelReadCancelledIsNotLogged(t *testing.T) {
	buf := captureModelLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	settings, _ := json.Marshal(map[string]string{"base_url": srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	call := sdk.ModelsCall{Account: protocol.Account{ID: "acct", Settings: settings, Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: logTestSecret}}}
	if _, err := listModels(ctx, call); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("log = %q, want nothing", buf.String())
	}
}
