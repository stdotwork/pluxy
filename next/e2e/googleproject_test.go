package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These tests drive core and the Gemini extension against a fake Google API whose errors name the
// owner's Google project. A client holding a pluxy key keeps Google's error details, which it reads
// (RetryInfo, QuotaFailure), but learns nothing that names the project; the owner's own account
// check and quota reading still show it.

const (
	ownerProjectNumber = "738291456021"
	ownerProjectID     = "acme-prod-4821"
	ownerOrgNumber     = "555000482917"
	ownerFolderNumber  = "666000391824"
)

// suspendedReply is Google's documented CONSUMER_SUSPENDED reply.
const suspendedReply = `{"error":{"code":403,"message":"The consumer 'projects/` + ownerProjectNumber + `' is suspended.","status":"PERMISSION_DENIED",` +
	`"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"CONSUMER_SUSPENDED","domain":"googleapis.com",` +
	`"metadata":{"consumer":"projects/` + ownerProjectNumber + `","service":"generativelanguage.googleapis.com"}}]}}`

// disabledReply is a SERVICE_DISABLED reply: the project number in the message, in the metadata
// (containerInfo, consumer, activationUrl) and in the console links.
const disabledReply = `{"error":{"code":403,"message":"Generative Language API has not been used in project ` + ownerProjectNumber +
	` before or it is disabled. Enable it by visiting https://console.developers.google.com/apis/api/generativelanguage.googleapis.com/overview?project=` + ownerProjectNumber + ` then retry.",` +
	`"status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SERVICE_DISABLED","domain":"googleapis.com",` +
	`"metadata":{"containerInfo":"` + ownerProjectNumber + `","service":"generativelanguage.googleapis.com","consumer":"projects/` + ownerProjectNumber + `",` +
	`"activationUrl":"https://console.developers.google.com/apis/api/generativelanguage.googleapis.com/overview?project=` + ownerProjectNumber + `"}},` +
	`{"@type":"type.googleapis.com/google.rpc.LocalizedMessage","locale":"en-US","message":"Generative Language API has not been used in project ` + ownerProjectNumber + ` before or it is disabled."},` +
	`{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"Google developers console API activation","url":"https://console.developers.google.com/apis/api/generativelanguage.googleapis.com/overview?project=` + ownerProjectNumber + `"}]}]}}`

// quotaReply is a 429 whose QuotaFailure names the project as its subject.
const quotaReply = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details.","status":"RESOURCE_EXHAUSTED","details":[` +
	`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_requests",` +
	`"quotaId":"GenerateRequestsPerMinutePerProjectPerModel-FreeTier","quotaDimensions":{"location":"global","model":"gemini-2.5-flash"},"quotaValue":"15","subject":"project:` + ownerProjectID + `"}]},` +
	`{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"Learn more about Gemini API quotas","url":"https://ai.google.dev/gemini-api/docs/rate-limits"}]},` +
	`{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"34s"}]}}`

// otherNamesReply names the owner in the forms the other replies do not: a project in plain prose,
// the client's address, an organization, a folder, a group, a billing account and a service account.
const otherNamesReply = `{"error":{"code":403,"message":"Permission denied on resource project my-proj-1. Caller foo@my-proj-2.iam.gserviceaccount.com lacks permission.","status":"PERMISSION_DENIED","details":[` +
	`{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"IAM_PERMISSION_DENIED","domain":"googleapis.com"},` +
	`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"subject":"clientip:203.0.113.7","quotaId":"Q"},{"subject":"organization:` + ownerOrgNumber + `"},{"subject":"folder:` + ownerFolderNumber + `"}]},` +
	`{"@type":"type.googleapis.com/google.rpc.ResourceInfo","resource_name":"organizations/` + ownerOrgNumber + `/x","owner":"group:team@example.com"},` +
	`{"@type":"type.googleapis.com/google.rpc.ResourceInfo","owner":"billingAccounts/0123-ABCD"}]}}`

// ownerNames are the names the replies above carry for the owner.
var ownerNames = []string{ownerProjectNumber, ownerProjectID, "my-proj-1", "my-proj-2", "203.0.113.7", ownerOrgNumber, ownerFolderNumber, "team@example.com", "0123-ABCD", "foo@"}

const goodChunk = `{"candidates":[{"content":{"role":"model","parts":[{"text":"Hi"}]},"index":0}]}`

// googleProjectCore runs core with one Gemini account on the fake.
func googleProjectCore(t *testing.T, up *fakeGemini) string {
	t.Helper()
	return startInteractionsCore(t, interactionsAccount{id: "g1", key: interactionsKey, baseURL: up.URL, enabled: true})
}

// clientCall is one request from a client of one API, as the whole exchange.
type clientCall struct {
	name, path, body string
	stream           bool
}

var googleProjectClients = []clientCall{
	{"gemini", "/v1beta/models/gemini-2.5-flash:generateContent", `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`, false},
	{"gemini stream", "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`, true},
	{"chat", "/v1/chat/completions", `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"Hi"}]}`, false},
	{"chat stream", "/v1/chat/completions", `{"model":"gemini-2.5-flash","stream":true,"messages":[{"role":"user","content":"Hi"}]}`, true},
	{"messages", "/v1/messages", `{"model":"gemini-2.5-flash","max_tokens":16,"messages":[{"role":"user","content":"Hi"}]}`, false},
	{"messages stream", "/v1/messages", `{"model":"gemini-2.5-flash","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`, true},
}

// exchange sends c and returns the status and the whole reply, headers and body as one text.
func (c clientCall) exchange(t *testing.T, base string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+c.path, strings.NewReader(c.body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("Anthropic-Version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var all strings.Builder
	resp.Header.Write(&all)
	all.Write(body)
	return resp.StatusCode, all.String()
}

func noProject(t *testing.T, what, reply string) {
	t.Helper()
	for _, id := range ownerNames {
		if strings.Contains(reply, id) {
			t.Errorf("%s names the owner's project %s:\n%s", what, id, reply)
		}
	}
}

// A cooldown reply carries an RFC3339Nano timestamp whose nanoseconds can hold any 6-digit run;
// no entry in ownerNames may match a timestamp's digits.
func TestGoogleProjectNoProjectIgnoresTimestamp(t *testing.T) {
	for _, ts := range []string{
		"2026-11-22T10:58:05.455500043Z",
		"2026-11-22T10:58:05.466600043Z",
	} {
		reply := `{"error":{"message":"all accounts for gemini-2.5-flash are in cooldown until ` + ts + `"}}`
		noProject(t, "cooldown reply", reply)
	}
	for _, id := range ownerNames {
		if strings.Trim(id, "0123456789") == "" && len(id) <= 9 {
			t.Errorf("numeric ownerName %q has %d digits; use >9 digits so it cannot match timestamp nanoseconds", id, len(id))
		}
	}
}

// Every client of every API gets a Google upstream's error without the project, whether the
// upstream answered with an error status or put the error in a reply that was already streaming.
func TestGoogleProjectNotInClientErrors(t *testing.T) {
	for _, r := range []struct{ name, body string }{{"consumer suspended", suspendedReply}, {"service disabled", disabledReply}, {"other names", otherNamesReply}} {
		for _, c := range googleProjectClients {
			for _, mode := range []string{"error status", "error in stream", "error in a 200 reply"} {
				if mode != "error status" && (mode == "error in stream") != c.stream {
					continue
				}
				t.Run(r.name+"/"+c.name+"/"+mode, func(t *testing.T) {
					t.Parallel()
					up := newFakeGemini(t, func(w http.ResponseWriter, req *http.Request) {
						switch mode {
						case "error status":
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusForbidden)
							io.WriteString(w, r.body)
							return
						case "error in a 200 reply":
							w.Header().Set("Content-Type", "application/json")
							io.WriteString(w, r.body)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", goodChunk, r.body)
					})
					base := googleProjectCore(t, up)
					status, reply := c.exchange(t, base)
					if up.all() == "" {
						t.Fatalf("the upstream got no request")
					}
					noProject(t, c.name+" reply", reply)
					if mode == "error status" && status < 400 {
						t.Errorf("status %d, want an error status", status)
					}
					if !strings.Contains(strings.ToLower(reply), "suspended") && !strings.Contains(strings.ToLower(reply), "disabled") && !strings.Contains(strings.ToLower(reply), "permission denied") {
						t.Errorf("the reply lost what the upstream said:\n%s", reply)
					}
					// The account is now blocked; what the next request says must not name the project either.
					_, again := c.exchange(t, base)
					noProject(t, c.name+" reply to the next request", again)
				})
			}
		}
	}
}

// A Gemini client keeps the details: the reason and the service stay, only the project goes.
func TestGoogleProjectKeepsDetailsForGeminiClient(t *testing.T) {
	up := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, disabledReply)
	})
	status, reply := googleProjectClients[0].exchange(t, googleProjectCore(t, up))
	noProject(t, "reply", reply)
	if status != http.StatusForbidden {
		t.Errorf("status %d, want 403", status)
	}
	for _, keep := range []string{`"reason":"SERVICE_DISABLED"`, `"service":"generativelanguage.googleapis.com"`, `"@type":"type.googleapis.com/google.rpc.Help"`, `"status":"PERMISSION_DENIED"`, "overview?project=[redacted]"} {
		if !strings.Contains(reply, keep) {
			t.Errorf("the reply lost %s:\n%s", keep, reply)
		}
	}
}

// A 429 keeps RetryInfo and QuotaFailure; only the subject's project is masked. Retry-After is
// still set from the upstream's retry delay.
func TestGoogleProjectQuotaFailureKeepsRetryInfo(t *testing.T) {
	up := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, quotaReply)
	})
	base := googleProjectCore(t, up)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp := postPath(t, ctx, base, "/v1beta/models/gemini-2.5-flash:generateContent", clientKey, `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	noProject(t, "reply", string(raw))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status %d: %s, want 429", resp.StatusCode, raw)
	}
	if ra := resp.Header.Get("Retry-After"); ra == "" {
		t.Errorf("Retry-After is not set: %v", resp.Header)
	}
	var got struct {
		Error struct {
			Details []json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	want := strings.Replace(quotaReply, "project:"+ownerProjectID, "[redacted]", 1)
	var wantBody struct {
		Error struct {
			Details []json.RawMessage `json:"details"`
		} `json:"error"`
	}
	json.Unmarshal([]byte(want), &wantBody)
	if len(got.Error.Details) != 3 {
		t.Fatalf("details = %s, want RetryInfo, QuotaFailure and Help", raw)
	}
	for i := range wantBody.Error.Details {
		if string(got.Error.Details[i]) != string(wantBody.Error.Details[i]) {
			t.Errorf("detail %d = %s\nwant %s", i, got.Error.Details[i], wantBody.Error.Details[i])
		}
	}
}

// An Interactions client gets the upstream's error masked too: an error status, an error event in
// the stream, and an error object in a reply that was a 200.
func TestGoogleProjectNotInInteractionsErrors(t *testing.T) {
	const body = `{"model":"gemini-2.5-flash","input":"Hi"}`
	streamBody := `{"model":"gemini-2.5-flash","input":"Hi","stream":true}`
	for _, c := range []struct {
		name, req string
		reply     func(w http.ResponseWriter)
	}{
		{"error status", body, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, disabledReply)
		}},
		{"error event in stream", streamBody, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: interaction.start\ndata: {\"event_type\":\"interaction.start\",\"interaction\":{\"id\":\"v1_x\",\"status\":\"in_progress\"}}\n\n")
			fmt.Fprintf(w, "event: error\ndata: {\"event_type\":\"error\",\"error\":%s}\n\n", strings.TrimSuffix(strings.TrimPrefix(suspendedReply, `{"error":`), `}`))
		}},
		{"failed interaction in a 200 reply", body, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"v1_x","status":"failed","error":%s}`, strings.TrimSuffix(strings.TrimPrefix(otherNamesReply, `{"error":`), `}`))
		}},
		{"failed interaction in a stream event", streamBody, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: interaction.start\ndata: {\"event_type\":\"interaction.start\",\"interaction\":{\"id\":\"v1_x\",\"status\":\"in_progress\"}}\n\n")
			fmt.Fprintf(w, "event: interaction.complete\ndata: {\"event_type\":\"interaction.complete\",\"interaction\":{\"id\":\"v1_x\",\"status\":\"failed\",\"error\":%s}}\n\n", strings.TrimSuffix(strings.TrimPrefix(disabledReply, `{"error":`), `}`))
		}},
		{"error in a 200 reply", body, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, suspendedReply)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			up := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) { c.reply(w) })
			base := googleProjectCore(t, up)
			resp := postInteractions(t, context.Background(), base, bearer, c.req)
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			var all strings.Builder
			resp.Header.Write(&all)
			all.Write(raw)
			noProject(t, "reply", all.String())
			if !strings.Contains(string(raw), "error") && !strings.Contains(string(raw), "failed") {
				t.Errorf("the client got no error:\n%s", raw)
			}
			if c.name != "error in a 200 reply" && strings.Contains(c.req, `"stream":true`) && !strings.Contains(string(raw), "interaction.start") {
				t.Errorf("the stream's first event is gone:\n%s", raw)
			}
		})
	}
}

// The owner keeps the project where they look for it: the account check (listed with the
// account) and the quota reading, which a client key cannot read.
func TestGoogleProjectOwnerStillSeesIt(t *testing.T) {
	up := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, disabledReply)
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, quotaReply)
	})
	base := googleProjectCore(t, up)

	var listing struct {
		Accounts []struct {
			ID    string `json:"id"`
			Check *struct {
				State string `json:"state"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"check"`
		} `json:"accounts"`
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		listing.Accounts = nil
		if code := manage(t, base, "GET", "/accounts", nil, &listing); code != http.StatusOK {
			t.Fatalf("GET /accounts: %d", code)
		}
		if len(listing.Accounts) == 1 && listing.Accounts[0].Check != nil && listing.Accounts[0].Check.State == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the account check did not fail: %+v", listing)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if msg := listing.Accounts[0].Check.Error.Message; !strings.Contains(msg, ownerProjectNumber) {
		t.Errorf("the owner's account check lost the project: %q", msg)
	}

	// A client key is not the management key.
	for _, path := range []string{"/accounts", "/accounts/g1/quota"} {
		if code, raw := mcall(t, base, clientKey, http.MethodGet, path, nil); code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Errorf("a client key on %s: %d %s, want it refused", path, code, raw)
		}
	}

	// A 429 a client got is what the quota reading remembers, with the subject as sent.
	if _, reply := googleProjectClients[0].exchange(t, base); strings.Contains(reply, ownerProjectID) {
		t.Errorf("the client reply names the project:\n%s", reply)
	}
	code, raw := mcall(t, base, managementKey, http.MethodGet, "/accounts/g1/quota", nil)
	if code != http.StatusOK || !strings.Contains(string(raw), ownerProjectID) {
		t.Errorf("the owner's quota reading = %d %s, want it to keep the subject project:%s", code, raw, ownerProjectID)
	}
}

// An OpenAI-compatible account that points at Google's own host answers with Google's errors: a
// client of any API gets them without the project. For the array form Google's OpenAI-compatible
// API wraps an error in, upstreamError does not unwrap the array, so the client gets a generic
// status message rather than the upstream body; masking of the array shape itself is covered by
// the unit tests (geminigenerate.TestMaskArrayForm and openai-compatible's
// TestMaskGoogleMasksGoogleShapedErrors/array).
func TestGoogleProjectNotInOpenAICompatibleErrors(t *testing.T) {
	for name, body := range map[string]string{"object": disabledReply, "array": "[" + disabledReply + "]"} {
		for _, c := range []clientCall{
			{"chat", "/v1/chat/completions", `{"model":"gpt-test","messages":[{"role":"user","content":"Hi"}]}`, false},
			{"gemini", "/v1beta/models/gpt-test:generateContent", `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`, false},
		} {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				u := newUpstream(t)
				u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					io.WriteString(w, body)
				}
				status, reply := c.exchange(t, startCore(t, u))
				if bodies, _ := u.received(); len(bodies) == 0 {
					t.Fatalf("the upstream got no request")
				}
				noProject(t, c.name+" reply", reply)
				if status < 400 {
					t.Errorf("status %d, want an error", status)
				}
				if name == "object" {
					if !strings.Contains(reply, "has not been used") || !strings.Contains(reply, "[redacted]") {
						t.Errorf("object reply did not carry the masked upstream message:\n%s", reply)
					}
				} else if !strings.Contains(reply, "upstream returned 403 Forbidden") {
					t.Errorf("array reply = %s, want the generic status message", reply)
				}
			})
		}
	}
}

// An OpenAI-compatible account that points at Google's host can fail inside a reply that was
// already streaming; a client gets that error without the project too.
func TestGoogleProjectNotInOpenAICompatibleStreamErrors(t *testing.T) {
	for _, c := range []clientCall{
		{"chat stream", "/v1/chat/completions", `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"Hi"}]}`, true},
		{"gemini stream", "/v1beta/models/gpt-test:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			u := newUpstream(t)
			u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hi\"}}]}\n\n")
				fmt.Fprintf(w, "data: %s\n\n", disabledReply)
			}
			_, reply := c.exchange(t, startCore(t, u))
			if !strings.Contains(reply, "has not been used") {
				t.Fatalf("the upstream's error did not reach the client:\n%s", reply)
			}
			noProject(t, c.name+" reply", reply)
		})
	}
}

// Errors returned through CountTokens on both extensions and through the OpenAI-compatible
// Endpoint handler are masked too.
func TestGoogleProjectNotInCountTokensAndEndpointErrors(t *testing.T) {
	t.Run("gemini/countTokens", func(t *testing.T) {
		t.Parallel()
		up := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, disabledReply)
		})
		base := googleProjectCore(t, up)
		call := clientCall{
			name: "gemini countTokens",
			path: "/v1beta/models/gemini-2.5-flash:countTokens",
			body: `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`,
		}
		status, reply := call.exchange(t, base)
		if up.all() == "" {
			t.Fatalf("the upstream got no request")
		}
		if status != http.StatusForbidden {
			t.Errorf("status %d, want 403:\n%s", status, reply)
		}
		if !strings.Contains(reply, "has not been used") || !strings.Contains(reply, "[redacted]") {
			t.Errorf("the reply did not carry the masked upstream message:\n%s", reply)
		}
		noProject(t, call.name+" reply", reply)
	})

	t.Run("openai-compatible/countTokens", func(t *testing.T) {
		t.Parallel()
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1beta/models/gpt-test:countTokens" {
				http.NotFound(w, r)
				return
			}
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, disabledReply)
		}))
		defer srv.Close()
		base := startCore(t, &upstream{Server: srv}, func(cfg map[string]any) {
			ext := cfg["extension"].(map[string]any)
			acct := ext["accounts"].([]any)[0].(map[string]any)
			acct["settings"] = map[string]any{"base_url": srv.URL + "/v1beta", "wire_api": "gemini"}
		})
		call := clientCall{
			name: "openai-compatible countTokens",
			path: "/v1beta/models/gpt-test:countTokens",
			body: `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`,
		}
		status, reply := call.exchange(t, base)
		if hits.Load() == 0 {
			t.Fatalf("the upstream got no request")
		}
		if status != http.StatusForbidden {
			t.Errorf("status %d, want 403:\n%s", status, reply)
		}
		if !strings.Contains(reply, "has not been used") || !strings.Contains(reply, "[redacted]") {
			t.Errorf("the reply did not carry the masked upstream message:\n%s", reply)
		}
		noProject(t, call.name+" reply", reply)
	})

	t.Run("openai-compatible/endpoint", func(t *testing.T) {
		t.Parallel()
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/images/generations" {
				http.NotFound(w, r)
				return
			}
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, disabledReply)
		}))
		defer srv.Close()
		base := startCore(t, &upstream{Server: srv})
		call := clientCall{
			name: "openai-compatible images endpoint",
			path: "/v1/images/generations",
			body: `{"model":"gpt-test","prompt":"a cat"}`,
		}
		status, reply := call.exchange(t, base)
		if hits.Load() == 0 {
			t.Fatalf("the upstream got no request")
		}
		if status != http.StatusForbidden {
			t.Errorf("status %d, want 403:\n%s", status, reply)
		}
		if !strings.Contains(reply, "has not been used") || !strings.Contains(reply, "[redacted]") {
			t.Errorf("the reply did not carry the masked upstream message:\n%s", reply)
		}
		noProject(t, call.name+" reply", reply)
	})
}
