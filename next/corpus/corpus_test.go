package corpus

import (
	"context"
	"encoding/json"
	"fmt"
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
//	PLUXY_CORPUS_OLD_CORE=1      also run today's core (built from ../../core) and compare live
//	PLUXY_CORPUS_OLD_CORE_BIN=…  use this old-core binary instead of building one
//	PLUXY_CORPUS_RECORD=1        run the old core and write its observations into the case files
//	PLUXY_CORPUS_REPORT=path     also write the report to path
//	PLUXY_CORPUS_CASE=regexp     run only cases whose format/provider/name matches
//	PLUXY_CORPUS_DUMP_NEW=dir    write each new-core observation to dir/<format>.<provider>.<name>.json
var (
	binDir    string
	oldBinary string
	record    = os.Getenv("PLUXY_CORPUS_RECORD") == "1"
	liveOld   = record || os.Getenv("PLUXY_CORPUS_OLD_CORE") == "1" || os.Getenv("PLUXY_CORPUS_OLD_CORE_BIN") != ""
)

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
	if err := BuildNewCore("..", binDir); err != nil {
		return err
	}
	if !liveOld {
		return nil
	}
	if bin := os.Getenv("PLUXY_CORPUS_OLD_CORE_BIN"); bin != "" {
		oldBinary = bin
		return nil
	}
	oldBinary = filepath.Join(binDir, "old-core")
	start := time.Now()
	if err := BuildOldCore(filepath.Join("..", "..", "core"), oldBinary); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "corpus: built the old core in %s\n", time.Since(start).Round(time.Second))
	return nil
}

// coreSlots limits how many cores run at once; each old core holds tens of MB.
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
					t.Errorf("new core differs from the old core outside the listed exceptions; see the corpus report")
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
	reference := "the old core's recorded observations (set PLUXY_CORPUS_OLD_CORE=1 to run it live)"
	if liveOld {
		reference = "the old core, run live from core/"
	}
	report := Report(ordered, reference, global.Exceptions, staleGlobal)
	fmt.Println(report)
	if path := os.Getenv("PLUXY_CORPUS_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
			t.Error(err)
		}
	}
}

func runCase(t *testing.T, c *Case, global *Global) Result {
	res := Result{Case: c}
	volatile := append(append([]string(nil), global.Volatile...), c.Volatile...)

	newObs, err := observe(t, c, "new", func(dir, upstreamURL string) (*Core, error) {
		return StartNewCore(binDir, dir, c.Provider, upstreamURL, c.AccountModels)
	})
	if err != nil {
		res.Err = fmt.Errorf("new core: %w", err)
		return res
	}
	Mask(newObs, volatile)
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
	}
	if liveOld {
		oldObs, err := observe(t, c, "old", func(dir, upstreamURL string) (*Core, error) {
			return StartOldCore(OldCoreSetup{Binary: oldBinary, WorkDir: dir, Provider: c.Provider, UpstreamURL: upstreamURL, Models: caseModels(c)})
		})
		if err != nil {
			res.Err = fmt.Errorf("old core: %w", err)
			return res
		}
		Mask(oldObs, volatile)
		switch {
		case record:
			if err := c.SaveRecorded(oldObs); err != nil {
				res.Err = err
				return res
			}
		case ref == nil:
			res.Err = fmt.Errorf("case has no recorded_old_core; run with PLUXY_CORPUS_RECORD=1")
		default:
			if d := Diff(map[string]any(ref), map[string]any(oldObs)); len(d) > 0 {
				var b strings.Builder
				b.WriteString("the live old core no longer matches recorded_old_core (re-record with PLUXY_CORPUS_RECORD=1, or mark the path volatile):")
				for _, x := range d {
					fmt.Fprintf(&b, "\n  %s: recorded %s, live %s", x.Path, Show(x.Want), Show(x.Got))
				}
				res.Err = fmt.Errorf("%s", b.String())
			}
		}
		ref = oldObs
	}
	if ref == nil {
		res.Err = fmt.Errorf("case has no recorded_old_core; run with PLUXY_CORPUS_RECORD=1")
		return res
	}
	res.Verdict = Judge(ref, newObs, c.Exceptions, global.Exceptions)
	if res.Verdict.OK() {
		probeCase(t, ref, newObs, c, global)
	}
	return res
}

// observe runs the case once against a core started by start, with a fresh fake upstream.
func observe(t *testing.T, c *Case, label string, start func(dir, upstreamURL string) (*Core, error)) (Observation, error) {
	coreSlots <- struct{}{}
	defer func() { <-coreSlots }()
	up := NewFakeUpstream(c.Upstream)
	defer up.Close()
	dir, err := os.MkdirTemp("", "pluxy-corpus-"+label+"-")
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
		t.Logf("%s core output:\n%s", label, core.Logs())
	}
	return obs, nil
}

// caseModels returns the models the old core lists for the case: AccountModels, or else the
// request's model without a thinking suffix such as "(high)".
func caseModels(c *Case) []string {
	if len(c.AccountModels) > 0 {
		return c.AccountModels
	}
	var body struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(c.Client.Body, &body)
	m := body.Model
	if i := strings.LastIndex(m, "("); i > 0 && strings.HasSuffix(m, ")") {
		m = m[:i]
	}
	if m == "" {
		m = "corpus-model"
	}
	return []string{m}
}
