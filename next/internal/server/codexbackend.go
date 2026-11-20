package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

// codexBackendRoot is where the Codex CLI's chatgpt_base_url points when it is set to core: the
// ChatGPT backend's own root, which the CLI adds each call's path to.
const codexBackendRoot = "/backend-api"

// codexBackendEndpoint passes a Codex CLI call to the ChatGPT backend on, as the CLI made it: the
// call's method, path, query and body go to the account's extension in a codexBackendCall, and
// the backend's reply comes back as it is. The headers are those alpha search passes.
var codexBackendEndpoint = endpoint{
	format:        llm.FormatCodexBackend,
	headers:       alphaSearchEndpoint.headers,
	modelOptional: true,
}

// codexBackendCall is the Native.Body of a codex.backend call. The extension reads the same
// shape (next/extensions/codex); next/protocol/README.md's endpoint table describes it.
type codexBackendCall struct {
	Method string `json:"method"`
	// Path is the request path below /backend-api, escaped as the client sent it.
	Path string `json:"path"`
	// Query is the raw query string, without its "?".
	Query string `json:"query,omitempty"`
	// ContentType is the request body's, when it has one.
	ContentType string `json:"content_type,omitempty"`
	Body        []byte `json:"body,omitempty"`
}

// codexBackendRoutes are the calls to the ChatGPT backend that the Codex CLI makes when its
// chatgpt_base_url points at core, other than the ones core serves by other routes (responses,
// responses/compact and alpha/search under /backend-api/codex): each is a method and a path below
// /backend-api, and nothing else is passed on. {name} stands for one path segment. The list was
// read from the Codex CLI's source (github.com/openai/codex, codex-rs, commit
// 076ac7856b1235947f271930bd7437bae54a0dfc); each group names the file and line that builds its
// URL. The CLI sends the same calls with a /api/codex prefix to a Codex API host rather than a
// ChatGPT one; those are not the ChatGPT backend and are not listed.
//
// Not listed, on purpose: sign-in and token calls (auth.openai.com, device codes,
// accounts/switch-workspace-token, wham/agent-identities/jwks), WebSocket and WebRTC calls
// (realtime, wham/remote/control/server), the telemetry post (codex/analytics-events/events) and
// the MCP transport at ps/mcp, none of which a one-reply pass-through serves, the model
// catalog (codex/models), which is core's own list of the accounts' models, not the backend's, and
// DELETE public/plugins/workspace/{plugin}, which the CLI accepts only with a 204 reply
// (core-plugins/src/remote/share.rs:290) where an endpoint reply is always a 200.
var codexBackendRoutes = []string{
	// backend-client/src/client.rs (account, tasks, settings and transcription), client/*.rs
	// (usage and limits).
	"GET /wham/usage",                                            // client/rate_limit_resets.rs:131
	"GET /wham/rate-limit-reset-credits",                         // client/rate_limit_resets.rs:141
	"POST /wham/rate-limit-reset-credits/consume",                // client/rate_limit_resets.rs:155
	"GET /wham/accounts/check",                                   // client.rs:566
	"POST /wham/accounts/send_add_credits_nudge_email",           // client.rs:894
	"GET /wham/profiles/me",                                      // client.rs:585
	"GET /wham/tasks/list",                                       // client.rs:627
	"GET /wham/tasks/{task}",                                     // client.rs:663
	"GET /wham/tasks/{task}/turns/{turn}/sibling_turns",          // client.rs:684
	"POST /wham/tasks",                                           // client.rs:757
	"GET /wham/config/bundle",                                    // client.rs:715
	"GET /wham/settings/user",                                    // client.rs:911
	"GET /wham/workspace-messages",                               // client.rs:904
	"GET /wham/usage/daily-token-usage-breakdown",                // client/analytics.rs:42
	"GET /wham/usage/credit-usage-events",                        // client/analytics.rs:43
	"GET /wham/usage/daily-workspace-user-token-usage-breakdown", // client/analytics.rs:44
	"GET /wham/usage/daily-workspace-user-credit-usage",          // client/analytics.rs:47
	"GET /wham/analytics/daily-workspace-usage-counts",           // client/analytics.rs:48
	"GET /wham/analytics/daily-plugin-usage-metrics",             // client/analytics.rs:49
	"GET /wham/analytics/daily-skill-usage-metrics",              // client/analytics.rs:50
	"GET /wham/usage/plan_limit_history",                         // client/plan_history.rs:68
	"POST /wham/usage/thread-estimates/query",                    // client/chatgpt_turn_cost.rs:55
	"POST /wham/usage/thread_usage/query_v2",                     // client/task_usage.rs:99
	"POST /wham/usage/thread_usage/query",                        // client/thread_usage.rs:122
	"POST /transcribe",                                           // client.rs:522
	// cloud-tasks/src/env_detect.rs
	"GET /wham/environments",                               // :97
	"GET /wham/environments/by-repo/github/{owner}/{repo}", // :64
	// codex-api/src/files.rs, which core/src/mcp_openai_file.rs:268 calls with chatgpt_base_url.
	"POST /files",                 // :155
	"POST /files/{file}/uploaded", // :369
	// codex-api/src/endpoint/memories.rs:33, on the model provider's base (/backend-api/codex).
	"POST /codex/memories/trace_summarize",
	// The plugin service: core-plugins/src/remote.rs, remote/search.rs, remote/share.rs and
	// remote_legacy.rs.
	"GET /ps/plugins/suggested/codex",           // remote.rs:1171
	"GET /ps/plugins/list",                      // remote.rs:2347
	"GET /ps/plugins/workspace/shared",          // remote.rs:2369
	"GET /ps/plugins/workspace/created",         // remote/share.rs:386
	"GET /ps/plugins/installed",                 // remote.rs:2390
	"GET /ps/plugins/search",                    // remote/search.rs:44
	"GET /ps/plugins/{plugin}",                  // remote.rs:2438
	"GET /ps/plugins/{plugin}/skills/{skill}",   // remote.rs:2466
	"POST /ps/plugins/{plugin}/install",         // remote.rs:1752
	"POST /ps/plugins/{plugin}/uninstall",       // remote.rs:1849
	"PUT /ps/plugins/{plugin}/shares",           // remote/share.rs:321
	"POST /public/plugins/workspace",            // remote/share.rs:464
	"POST /public/plugins/workspace/{plugin}",   // remote/share.rs:462
	"POST /public/plugins/workspace/upload-url", // remote/share.rs:406
	"GET /plugins/featured",                     // remote_legacy.rs:131
	"POST /plugins/{plugin}/enable",             // remote_legacy.rs:172
	"POST /plugins/{plugin}/uninstall",          // remote_legacy.rs:181
}

// serveCodexBackend serves one codexBackendRoutes call: the request, whatever its body, goes to
// an account that serves codex.backend calls, which sends it to the ChatGPT backend with the
// account's token.
func serveCodexBackend(w http.ResponseWriter, r *http.Request, g *Generation) {
	arrived := time.Now()
	f := clientFormat{}
	if g.Endpoint == nil {
		f.writeError(w, &llm.Error{Code: llm.CodeNotFound, Status: http.StatusNotFound, Message: "this endpoint is not served"})
		return
	}
	path := strings.TrimPrefix(r.URL.EscapedPath(), codexBackendRoot)
	if !safeBackendPath(path) {
		f.writeError(w, &llm.Error{Code: llm.CodeNotFound, Status: http.StatusNotFound, Message: "this endpoint is not served"})
		return
	}
	body, e, ok := readRequestBody(w, r)
	if !ok {
		return
	}
	if e != nil {
		f.writeError(w, e)
		return
	}
	call := codexBackendCall{Method: r.Method, Path: path, Query: r.URL.RawQuery, ContentType: r.Header.Get("Content-Type"), Body: body}
	if r.Method == http.MethodHead {
		call.Method = http.MethodGet
	}
	if mediaType(call.ContentType) == "application/json" {
		keepBody(r, body)
	}
	raw, err := json.Marshal(call)
	if err != nil {
		f.writeError(w, &llm.Error{Code: llm.CodeInternal, Status: http.StatusInternalServerError, Message: err.Error()})
		return
	}
	req := llm.Request{Native: &llm.Native{Format: codexBackendEndpoint.format, Body: raw}}
	routeEndpoint(w, r, g, codexBackendEndpoint, f, req, body, arrived)
}

// safeBackendPath reports whether path, a request path below /backend-api as the client sent it
// (escaped), stays where the route that matched it put it: it starts with a slash and no segment,
// once unescaped, is empty, "." or "..", or holds a slash, a backslash or a control character
// (an escaped one would let the backend read the path as another).
func safeBackendPath(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	for _, seg := range strings.Split(path[1:], "/") {
		s, err := url.PathUnescape(seg)
		if err != nil || s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\") {
			return false
		}
		for _, c := range s {
			if c < 0x20 || c == 0x7f {
				return false
			}
		}
	}
	return true
}
