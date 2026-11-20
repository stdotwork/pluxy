package corpus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Environment switches:
//
//	PLUXY_CORPUS_REPORT=path     also write the report to path
//	PLUXY_CORPUS_CASE=regexp     run only cases whose format/provider/name matches
//	PLUXY_CORPUS_DUMP_NEW=dir    write each new-core observation to dir/<format>.<provider>.<name>.json
//	PLUXY_CORPUS_LOGS=1          with -v, log each core's output
var binDir string

func TestMain(m *testing.M) {
	code := 1
	dir, err := os.MkdirTemp("", "pluxy-corpus-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binDir = dir
	if err := setup(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	} else {
		code = m.Run()
	}
	os.RemoveAll(dir)
	os.Exit(code)
}

func setup() error {
	return BuildNewCore("..", binDir)
}

// coreSlots limits how many cores run at once.
var coreSlots = make(chan struct{}, 4)

func TestCorpus(t *testing.T) {
	global, err := LoadGlobal("cases")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := LoadCases("cases")
	if err != nil {
		t.Fatal(err)
	}
	if filter := os.Getenv("PLUXY_CORPUS_CASE"); filter != "" {
		re := regexp.MustCompile(filter)
		var kept []*Case
		for _, c := range cases {
			if re.MatchString(c.Format + "/" + c.Provider + "/" + c.Name) {
				kept = append(kept, c)
			}
		}
		cases = kept
	}
	if len(cases) == 0 {
		t.Fatal("no corpus cases found")
	}

	var mu sync.Mutex
	results := map[*Case]Result{}
	t.Run("cases", func(t *testing.T) {
		for _, c := range cases {
			t.Run(c.Format+"/"+c.Provider+"/"+c.Name, func(t *testing.T) {
				t.Parallel()
				r := runCase(t, c, global)
				mu.Lock()
				results[c] = r
				mu.Unlock()
				if r.Err != nil {
					t.Error(r.Err)
				}
				if !r.Verdict.OK() {
					t.Errorf("new core differs from the old core's recorded reply outside the listed exceptions; see the corpus report")
				}
			})
		}
	})

	ordered := make([]Result, 0, len(cases))
	for _, c := range cases {
		if r, ok := results[c]; ok {
			ordered = append(ordered, r)
		}
	}
	// A global exception that covers nothing in any case is stale. Only judged over the
	// whole corpus.
	var staleGlobal []*Exception
	if os.Getenv("PLUXY_CORPUS_CASE") == "" {
		for i := range global.Exceptions {
			e := &global.Exceptions[i]
			used := false
			for _, r := range ordered {
				if len(r.Verdict.Covered[e]) > 0 {
					used = true
				}
			}
			if !used {
				staleGlobal = append(staleGlobal, e)
				t.Errorf("global exception %s covers no difference in any case", e.Path)
			}
		}
	}
	report := Report(ordered, "the old core's recorded replies (recorded_old_core in the case files)", global.Exceptions, staleGlobal)
	fmt.Println(report)
	if path := os.Getenv("PLUXY_CORPUS_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
			t.Error(err)
		}
	}
}

func runCase(t *testing.T, c *Case, global *Global) Result {
	res := Result{Case: c}
	var volatile []string
	for _, v := range global.Volatile {
		volatile = append(volatile, ForEachReply(v)...)
	}
	volatile = append(volatile, c.Volatile...)

	newObs, err := observe(t, c, func(dir, upstreamURL string) (*Core, error) {
		return StartNewCore(binDir, dir, c.Provider, upstreamURL, newCoreModels(c), UpstreamKeys(c.Accounts), c.NewCoreConfig, c.NewCore, managementKey(c))
	})
	if err != nil {
		res.Err = fmt.Errorf("new core: %w", err)
		return res
	}
	label := func(o Observation) {
		if c.LabelAccounts {
			LabelAccounts(o)
		}
	}
	label(newObs)
	Mask(newObs, volatile)
	SortAt(newObs, c.Unordered)
	if dir := os.Getenv("PLUXY_CORPUS_DUMP_NEW"); dir != "" {
		b, _ := json.Marshal(newObs)
		if b, err = FormatJSON(b); err == nil {
			err = os.WriteFile(filepath.Join(dir, c.Format+"."+c.Provider+"."+c.Name+".json"), b, 0o644)
		}
		if err != nil {
			res.Err = err
			return res
		}
	}

	var ref Observation
	if len(c.Recorded) > 0 {
		if ref, err = decodeObservation(c.Recorded); err != nil {
			res.Err = fmt.Errorf("recorded_old_core: %w", err)
			return res
		}
		Mask(ref, volatile)
		SortAt(ref, c.Unordered)
	}
	if ref == nil {
		res.Err = fmt.Errorf("case has no recorded_old_core, and the old core is gone, so none can be recorded (next/corpus/README.md, \"Adding cases\")")
		return res
	}
	res.Verdict = Judge(ref, newObs, c.Exceptions, global.Exceptions)
	if res.Verdict.OK() {
		probeCase(t, ref, newObs, c, global)
	}
	return res
}

// observe runs the case once against a core started by start, with a fresh fake upstream.
func observe(t *testing.T, c *Case, start func(dir, upstreamURL string) (*Core, error)) (Observation, error) {
	coreSlots <- struct{}{}
	defer func() { <-coreSlots }()
	up := NewFakeUpstream(c.Upstream, withAccountCheck(Providers[c.Provider].Aside))
	defer up.Close()
	dir, err := os.MkdirTemp("", "pluxy-corpus-new-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	core, err := start(dir, up.URL)
	if err != nil {
		return nil, err
	}
	defer core.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	obs, err := Run(ctx, c, core.URL, up)
	if err != nil {
		return nil, fmt.Errorf("%w\ncore output:\n%s", err, core.Logs())
	}
	if testing.Verbose() && os.Getenv("PLUXY_CORPUS_LOGS") == "1" {
		t.Logf("new core output:\n%s", core.Logs())
	}
	return obs, nil
}

// newCoreModels returns the models the new core's accounts list for the case: AccountModels, or
// else, unless the provider is a CatalogModels one, the request's model (caseModels).
func newCoreModels(c *Case) []string {
	if len(c.AccountModels) > 0 || Providers[c.Provider].CatalogModels {
		return c.AccountModels
	}
	return caseModels(c)
}

// managementKey is the management key a case uses: the corpus's when the case calls its management
// API, else none. The new core always gets a key, a placeholder when this returns none.
func managementKey(c *Case) string {
	if c.UsesManagement() {
		return ManagementKey
	}
	return ""
}

// caseModels returns the models a case's accounts list: AccountModels, or else the
// request's model without a thinking suffix such as "(high)". A Gemini request names its model in
// the path (/v1beta/models/{model}:{method}), a WebSocket client in its first message.
func caseModels(c *Case) []string {
	if len(c.AccountModels) > 0 {
		return c.AccountModels
	}
	var body struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(c.Client.Body, &body)
	for _, s := range c.Client.WebSocket {
		// A WebSocket client names its model in its first message that has one.
		if body.Model == "" && len(s.Send) > 0 {
			_ = json.Unmarshal(s.Send, &body)
		}
	}
	m := body.Model
	if _, rest, ok := strings.Cut(c.Client.Path, "/models/"); ok && m == "" {
		m, _, _ = strings.Cut(rest, ":")
	}
	if i := strings.LastIndex(m, "("); i > 0 && strings.HasSuffix(m, ")") {
		m = m[:i]
	}
	if m == "" {
		m = "corpus-model"
	}
	return []string{m}
}

// withAccountCheck adds to aside an answer for the new core's account check of an
// OpenAI-compatible account (GET {base_url}/models, when it starts): 404, no model list, so the
// account is not checked. Today's core makes no such call, and the check is not part of a case.
func withAccountCheck(aside func(*http.Request) (Reply, bool)) func(*http.Request) (Reply, bool) {
	return func(r *http.Request) (Reply, bool) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			return Reply{Status: http.StatusNotFound, Headers: map[string]string{"Content-Type": "application/json"},
				Body: json.RawMessage(`{"error":{"message":"not found"}}`)}, true
		}
		if aside != nil {
			return aside(r)
		}
		return Reply{}, false
	}
}
