package format_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// The client formats the service tier and verbosity tests read a request from. Each takes the
// fields to add to a minimal request.
var tierClients = map[string]func(t *testing.T, extra string) llm.Request{
	"chat": func(t *testing.T, extra string) llm.Request {
		r, err := openaichat.ParseRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]` + extra + `}`))
		if err != nil {
			t.Fatal(err)
		}
		return r
	},
	"responses": func(t *testing.T, extra string) llm.Request {
		r, err := openairesponses.ParseRequest([]byte(`{"model":"m","input":"hi"` + extra + `}`))
		if err != nil {
			t.Fatal(err)
		}
		return r
	},
	"messages": func(t *testing.T, extra string) llm.Request {
		r, err := anthropicmessages.ParseRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]` + extra + `}`))
		if err != nil {
			t.Fatal(err)
		}
		return r
	},
	"gemini": func(t *testing.T, extra string) llm.Request {
		r, err := geminigenerate.ParseRequest("m", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]`+extra+`}`))
		if err != nil {
			t.Fatal(err)
		}
		return r
	},
}

// The upstream formats the tests write a request to, each for a caller that says its upstream takes
// the service tier and the verbosity (messages-fast also takes fast mode).
var tierTargets = map[string]func(llm.Request) ([]byte, []llm.Loss, error){
	"chat": func(r llm.Request) ([]byte, []llm.Loss, error) {
		return openaichat.EncodeRequestWith(r, openaichat.EncodeOptions{ServiceTier: true, Verbosity: true})
	},
	"responses": func(r llm.Request) ([]byte, []llm.Loss, error) {
		return openairesponses.EncodeRequestWith(r, openairesponses.EncodeOptions{ServiceTier: true, Verbosity: true})
	},
	"messages": func(r llm.Request) ([]byte, []llm.Loss, error) {
		return anthropicmessages.EncodeRequestWith(r, anthropicmessages.EncodeOptions{ServiceTier: true})
	},
	"messages-fast": func(r llm.Request) ([]byte, []llm.Loss, error) {
		return anthropicmessages.EncodeRequestWith(r, anthropicmessages.EncodeOptions{ServiceTier: true, FastMode: true})
	},
	"gemini": func(r llm.Request) ([]byte, []llm.Loss, error) {
		return geminigenerate.EncodeRequestWith(r, geminigenerate.EncodeOptions{ServiceTier: true})
	},
}

// tierTargetsOff are the same formats for a caller that does not say so: the encoders' plain entry
// points.
var tierTargetsOff = map[string]func(llm.Request) ([]byte, []llm.Loss, error){
	"chat":      openaichat.EncodeRequest,
	"responses": openairesponses.EncodeRequest,
	"messages": func(r llm.Request) ([]byte, []llm.Loss, error) {
		return anthropicmessages.EncodeRequest(r)
	},
	"gemini": geminigenerate.EncodeRequest,
}

// tierFields are the body fields that say a service tier in any of the formats.
var tierFields = []string{"service_tier", "speed", "serviceTier"}

// checkTier encodes req for target and checks what it says about the tier: want is "" for nothing
// said and nothing lost, "field=value" for that top-level field and no loss, or "drop:path" for
// no tier said and the tier reported as dropped at path.
func checkTier(t *testing.T, req llm.Request, target, want string) {
	t.Helper()
	body, losses, err := tierTargets[target](req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	said := ""
	for _, f := range tierFields {
		if v, ok := m[f]; ok {
			if said != "" {
				t.Errorf("%s: body says a tier twice: %s", target, body)
			}
			var s string
			_ = json.Unmarshal(v, &s)
			said = f + "=" + s
		}
	}
	var dropped []string
	for _, l := range losses {
		if l.Kind == llm.LossDropped {
			dropped = append(dropped, l.Path)
		}
	}
	switch {
	case want == "":
		if said != "" || len(dropped) != 0 {
			t.Errorf("%s: said %q and dropped %q, want neither: %s", target, said, dropped, body)
		}
	case len(want) > 5 && want[:5] == "drop:":
		if said != "" || !slices.Equal(dropped, []string{want[5:]}) {
			t.Errorf("%s: said %q and dropped %q, want nothing said and %q dropped: %s", target, said, dropped, want[5:], body)
		}
	default:
		if said != want || len(dropped) != 0 {
			t.Errorf("%s: said %q and dropped %q, want %q and nothing dropped: %s", target, said, dropped, want, body)
		}
	}
}

// Every service tier a client can name, written to every upstream format: the tier it means there,
// or a loss where that format has none. A paid tier is written only for the client's own priority
// tier (fast is the same tier); auto, default and the tiers of no other format are never moved to
// priority, fast mode or another tier.
func TestServiceTierMatrix(t *testing.T) {
	for _, c := range []struct {
		name, client, extra string
		// want, by target
		chat, responses, messages, messagesFast, gemini string
	}{
		// OpenAI's own spellings go to the other OpenAI format as the client wrote them.
		{"chat auto", "chat", `,"service_tier":"auto"`, "service_tier=auto", "service_tier=auto", "service_tier=auto", "service_tier=auto", "drop:service_tier"},
		{"chat default", "chat", `,"service_tier":"default"`, "service_tier=default", "service_tier=default", "service_tier=standard_only", "service_tier=standard_only", "serviceTier=standard"},
		{"chat flex", "chat", `,"service_tier":"flex"`, "service_tier=flex", "service_tier=flex", "drop:service_tier", "drop:service_tier", "serviceTier=flex"},
		{"chat scale", "chat", `,"service_tier":"scale"`, "service_tier=scale", "service_tier=scale", "drop:service_tier", "drop:service_tier", "drop:service_tier"},
		{"chat priority", "chat", `,"service_tier":"priority"`, "service_tier=priority", "service_tier=priority", "drop:service_tier", "speed=fast", "serviceTier=priority"},
		{"chat fast", "chat", `,"service_tier":"fast"`, "service_tier=fast", "service_tier=fast", "drop:service_tier", "speed=fast", "serviceTier=priority"},
		{"chat ultrafast", "chat", `,"service_tier":"ultrafast"`, "service_tier=ultrafast", "service_tier=ultrafast", "drop:service_tier", "drop:service_tier", "drop:service_tier"},
		// A tier OpenAI's references do not list is the client's own on its own wire only: Responses
		// lists ultrafast and Chat Completions does not.
		{"chat unknown", "chat", `,"service_tier":"turbo"`, "service_tier=turbo", "drop:service_tier", "drop:service_tier", "drop:service_tier", "drop:service_tier"},
		{"responses unknown", "responses", `,"service_tier":"turbo"`, "drop:service_tier", "service_tier=turbo", "drop:service_tier", "drop:service_tier", "drop:service_tier"},
		{"responses ultrafast", "responses", `,"service_tier":"ultrafast"`, "drop:service_tier", "service_tier=ultrafast", "drop:service_tier", "drop:service_tier", "drop:service_tier"},
		{"chat none", "chat", ``, "", "", "", "", ""},
		{"responses auto", "responses", `,"service_tier":"auto"`, "service_tier=auto", "service_tier=auto", "service_tier=auto", "service_tier=auto", "drop:service_tier"},
		{"responses fast", "responses", `,"service_tier":"fast"`, "service_tier=fast", "service_tier=fast", "drop:service_tier", "speed=fast", "serviceTier=priority"},
		{"responses flex", "responses", `,"service_tier":"flex"`, "service_tier=flex", "service_tier=flex", "drop:service_tier", "drop:service_tier", "serviceTier=flex"},
		// Messages: standard_only is the default tier, fast speed the priority tier.
		{"messages auto", "messages", `,"service_tier":"auto"`, "service_tier=auto", "service_tier=auto", "service_tier=auto", "service_tier=auto", "drop:service_tier"},
		{"messages standard_only", "messages", `,"service_tier":"standard_only"`, "service_tier=default", "service_tier=default", "service_tier=standard_only", "service_tier=standard_only", "serviceTier=standard"},
		{"messages speed fast", "messages", `,"speed":"fast"`, "service_tier=priority", "service_tier=priority", "speed=fast", "speed=fast", "serviceTier=priority"},
		// Speed that is not fast says no tier: it stays a field of the client's, written back to its
		// own format and reported where another has no place for it.
		{"messages speed standard", "messages", `,"speed":"standard"`, "drop:speed", "drop:speed", "speed=standard", "speed=standard", "drop:speed"},
		// Gemini: standard is the default tier; unspecified names none.
		{"gemini standard", "gemini", `,"serviceTier":"standard"`, "service_tier=default", "service_tier=default", "service_tier=standard_only", "service_tier=standard_only", "serviceTier=standard"},
		{"gemini flex", "gemini", `,"serviceTier":"flex"`, "service_tier=flex", "service_tier=flex", "drop:serviceTier", "drop:serviceTier", "serviceTier=flex"},
		{"gemini priority", "gemini", `,"serviceTier":"priority"`, "service_tier=priority", "service_tier=priority", "drop:serviceTier", "speed=fast", "serviceTier=priority"},
		{"gemini unspecified", "gemini", `,"serviceTier":"unspecified"`, "", "", "", "", ""},
		{"gemini unknown", "gemini", `,"serviceTier":"turbo"`, "drop:serviceTier", "drop:serviceTier", "drop:serviceTier", "drop:serviceTier", "serviceTier=turbo"},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := tierClients[c.client](t, c.extra)
			for target, want := range map[string]string{"chat": c.chat, "responses": c.responses, "messages": c.messages, "messages-fast": c.messagesFast, "gemini": c.gemini} {
				t.Run(target, func(t *testing.T) { checkTier(t, req, target, want) })
			}
		})
	}
}

// A field a format cannot read into the model, a value that is not a string, stays among the
// request's kept fields: the same format writes it back, any other reports it dropped.
func TestServiceTierUnreadableStays(t *testing.T) {
	for _, c := range []struct {
		client, extra, field string
	}{
		{"chat", `,"service_tier":5`, "service_tier"},
		{"chat", `,"service_tier":null`, "service_tier"},
		{"chat", `,"service_tier":""`, "service_tier"},
		{"responses", `,"service_tier":{"a":1}`, "service_tier"},
	} {
		req := tierClients[c.client](t, c.extra)
		if req.Options.ServiceTier != "" {
			t.Errorf("%s %s: read as tier %q, want none", c.client, c.extra, req.Options.ServiceTier)
		}
		var want json.RawMessage
		var top map[string]json.RawMessage
		_ = json.Unmarshal(req.Native.Body, &top)
		want = top[c.field]
		body, _, err := tierTargets[c.client](req)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]json.RawMessage
		_ = json.Unmarshal(body, &got)
		if string(got[c.field]) != string(want) {
			t.Errorf("%s %s: written back as %s, want %s", c.client, c.extra, got[c.field], want)
		}
		other := "messages"
		_, losses, err := tierTargets[other](req)
		if err != nil {
			t.Fatal(err)
		}
		var paths []string
		for _, l := range losses {
			paths = append(paths, l.Path)
		}
		if !slices.Contains(paths, c.field) {
			t.Errorf("%s %s: losses %q, want %s reported", c.client, c.extra, paths, c.field)
		}
	}
}

// Verbosity goes between Chat Completions and Responses, and is reported dropped everywhere else.
func TestVerbosityMatrix(t *testing.T) {
	for _, c := range []struct {
		name, client, extra string
		read                string // the verbosity read into the model
		chat, responses     string // the verbosity written; "" for none (reported dropped if one was read)
		dropped             string // the path reported dropped by Messages and Gemini (and by a wire that lists no such verbosity)
	}{
		{"chat", "chat", `,"verbosity":"low"`, "low", "low", "low", "verbosity"},
		{"responses", "responses", `,"text":{"verbosity":"high"}`, "high", "high", "high", "text.verbosity"},
		{"responses with format", "responses", `,"text":{"verbosity":"medium","format":{"type":"json_object"}}`, "medium", "medium", "medium", "text.verbosity"},
		// A value OpenAI's references do not list is the client's own on its own wire only.
		{"chat unknown", "chat", `,"verbosity":"loud"`, "loud", "loud", "", "verbosity"},
		{"responses unknown", "responses", `,"text":{"verbosity":"loud"}`, "loud", "", "loud", "text.verbosity"},
		{"none", "chat", ``, "", "", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := tierClients[c.client](t, c.extra)
			if req.Options.Verbosity != c.read {
				t.Errorf("read as %q, want %q", req.Options.Verbosity, c.read)
			}
			lossPaths := func(losses []llm.Loss) []string {
				var paths []string
				for _, l := range losses {
					if strings.Contains(l.Path, "verbosity") {
						paths = append(paths, l.Path)
					}
				}
				return paths
			}
			wantLoss := func(written string) []string {
				if c.read != "" && written == "" {
					return []string{c.dropped}
				}
				return nil
			}
			body, losses, err := tierTargets["chat"](req)
			if err != nil {
				t.Fatal(err)
			}
			var chat struct {
				Verbosity string `json:"verbosity"`
			}
			_ = json.Unmarshal(body, &chat)
			if chat.Verbosity != c.chat || !slices.Equal(lossPaths(losses), wantLoss(c.chat)) {
				t.Errorf("Chat: verbosity %q, losses %+v, want %q and %q dropped: %s", chat.Verbosity, losses, c.chat, wantLoss(c.chat), body)
			}
			body, losses, err = tierTargets["responses"](req)
			if err != nil {
				t.Fatal(err)
			}
			var resp struct {
				Text struct {
					Verbosity string          `json:"verbosity"`
					Format    json.RawMessage `json:"format"`
				} `json:"text"`
			}
			_ = json.Unmarshal(body, &resp)
			if resp.Text.Verbosity != c.responses || !slices.Equal(lossPaths(losses), wantLoss(c.responses)) {
				t.Errorf("Responses: verbosity %q, losses %+v, want %q and %q dropped: %s", resp.Text.Verbosity, losses, c.responses, wantLoss(c.responses), body)
			}
			for _, target := range []string{"messages", "gemini"} {
				_, losses, err := tierTargets[target](req)
				if err != nil {
					t.Fatal(err)
				}
				want := []string(nil)
				if c.dropped != "" {
					want = []string{c.dropped}
				}
				if paths := lossPaths(losses); !slices.Equal(paths, want) {
					t.Errorf("%s: dropped %q, want %q", target, paths, want)
				}
			}
		})
	}
}

// A Responses request's text keeps its other fields and its format beside the verbosity, and a
// text that is not an object keeps all of itself.
func TestVerbosityKeepsTextFields(t *testing.T) {
	req := tierClients["responses"](t, `,"text":{"verbosity":"low","other":1,"format":{"type":"json_schema","name":"n","schema":{"type":"object"},"extra":true}}`)
	body, _, err := tierTargets["responses"](req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Text map[string]json.RawMessage `json:"text"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if string(out.Text["verbosity"]) != `"low"` || string(out.Text["other"]) != `1` {
		t.Errorf("text = %v, want verbosity low and other 1", out.Text)
	}
	var f map[string]json.RawMessage
	_ = json.Unmarshal(out.Text["format"], &f)
	if string(f["extra"]) != `true` || string(f["name"]) != `"n"` {
		t.Errorf("text.format = %s, want its name and extra kept", out.Text["format"])
	}
	req = tierClients["responses"](t, `,"text":{"verbosity":7}`)
	if req.Options.Verbosity != "" {
		t.Errorf("verbosity 7 read as %q, want none", req.Options.Verbosity)
	}
	body, _, _ = tierTargets["responses"](req)
	if got := json.RawMessage(body); !json.Valid(got) || string(rawField(t, body, "text")) != `{"verbosity":7}` {
		t.Errorf("text = %s, want {\"verbosity\":7} kept", rawField(t, body, "text"))
	}
}

func rawField(t *testing.T, body []byte, key string) json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	return m[key]
}

// Fast speed is the priority tier, and the service_tier beside it, which says nothing about speed
// (standard_only keeps the request off Priority Tier capacity), stays the client's own field: a
// Messages upstream gets both, any other format reports the field dropped.
func TestServiceTierSpeedBesideServiceTier(t *testing.T) {
	req := tierClients["messages"](t, `,"speed":"fast","service_tier":"standard_only"`)
	if req.Options.ServiceTier != llm.ServiceTierPriority {
		t.Fatalf("read as %q, want priority", req.Options.ServiceTier)
	}
	body, losses, err := tierTargets["messages-fast"](req)
	if err != nil {
		t.Fatal(err)
	}
	if string(rawField(t, body, "speed")) != `"fast"` || string(rawField(t, body, "service_tier")) != `"standard_only"` || len(losses) != 0 {
		t.Errorf("Messages: speed %s, service_tier %s, losses %+v, want fast, standard_only and none", rawField(t, body, "speed"), rawField(t, body, "service_tier"), losses)
	}
	body, losses, err = tierTargets["chat"](req)
	if err != nil {
		t.Fatal(err)
	}
	if string(rawField(t, body, "service_tier")) != `"priority"` || len(losses) != 1 || losses[0].Path != "service_tier" {
		t.Errorf("Chat: service_tier %s, losses %+v, want priority and the client's service_tier dropped", rawField(t, body, "service_tier"), losses)
	}
}

// Each format's own words for a tier are read into the model's: OpenAI's as written, Messages'
// standard_only as default and fast speed as priority, Gemini's standard as default.
func TestServiceTierRead(t *testing.T) {
	for _, c := range []struct {
		client, extra, want string
	}{
		{"chat", `,"service_tier":"fast"`, "fast"},
		{"chat", `,"service_tier":"scale"`, "scale"},
		{"responses", `,"service_tier":"ultrafast"`, "ultrafast"},
		{"messages", `,"service_tier":"auto"`, "auto"},
		{"messages", `,"service_tier":"standard_only"`, "default"},
		{"messages", `,"service_tier":"other"`, ""},
		{"messages", `,"speed":"fast"`, "priority"},
		{"messages", `,"speed":"FAST"`, ""},
		{"gemini", `,"serviceTier":"standard"`, "default"},
		{"gemini", `,"serviceTier":"flex"`, "flex"},
		{"gemini", `,"serviceTier":"priority"`, "priority"},
		{"gemini", `,"serviceTier":"unspecified"`, ""},
		{"gemini", `,"serviceTier":"SERVICE_TIER_FLEX"`, ""},
		{"chat", ``, ""},
	} {
		if got := tierClients[c.client](t, c.extra).Options.ServiceTier; got != c.want {
			t.Errorf("%s %s: read as %q, want %q", c.client, c.extra, got, c.want)
		}
	}
}

// Without the caller saying its upstream takes the field, a request from another format goes
// without a service tier and without a verbosity, and each is reported as dropped at the client's
// field. A client's own tier and verbosity are written back to its own format either way.
func TestServiceTierUnsentWithoutOption(t *testing.T) {
	for _, c := range []struct {
		name, client, extra string
		own                 string // the target that is the client's own format
		ownSays             string // what the client's own target writes: "field=value"
		dropped             []string
	}{
		{"chat", "chat", `,"service_tier":"priority","verbosity":"low"`, "chat", "service_tier=priority", []string{"service_tier", "verbosity"}},
		{"responses", "responses", `,"service_tier":"fast","text":{"verbosity":"low"}`, "responses", "service_tier=fast", []string{"service_tier", "text.verbosity"}},
		{"messages", "messages", `,"service_tier":"auto"`, "messages", "service_tier=auto", []string{"service_tier"}},
		{"messages fast", "messages", `,"speed":"fast"`, "messages", "speed=fast", []string{"speed"}},
		{"gemini", "gemini", `,"serviceTier":"flex"`, "gemini", "serviceTier=flex", []string{"serviceTier"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := tierClients[c.client](t, c.extra)
			for target, encode := range tierTargetsOff {
				body, losses, err := encode(req)
				if err != nil {
					t.Fatal(err)
				}
				var m map[string]json.RawMessage
				if err := json.Unmarshal(body, &m); err != nil {
					t.Fatal(err)
				}
				var paths []string
				for _, l := range losses {
					if l.Kind == llm.LossDropped && (strings.Contains(l.Path, "erbosity") || slices.Contains(tierFields, l.Path)) {
						paths = append(paths, l.Path)
					}
				}
				said := ""
				for _, f := range tierFields {
					if v, ok := m[f]; ok {
						var s string
						_ = json.Unmarshal(v, &s)
						said = f + "=" + s
					}
				}
				if target == c.own {
					if said != c.ownSays || len(paths) != 0 {
						t.Errorf("%s (the client's own): said %q, dropped %q, want %q and nothing dropped: %s", target, said, paths, c.ownSays, body)
					}
					continue
				}
				if said != "" || rawField(t, body, "verbosity") != nil || strings.Contains(string(rawField(t, body, "text")), "verbosity") {
					t.Errorf("%s: body says a tier or verbosity: %s", target, body)
				}
				if !slices.Equal(paths, c.dropped) {
					t.Errorf("%s: dropped %q, want %q", target, paths, c.dropped)
				}
			}
		})
	}
}
