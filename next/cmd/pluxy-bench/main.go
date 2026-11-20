// Command pluxy-bench measures the latency, streaming fidelity, throughput, CPU, and memory
// footprint added by pluxy between an LLM client and an in-process mock upstream.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	benchClientKey   = "pluxy-bench-client-key"
	benchUpstreamKey = "sk-pluxy-bench-upstream"
)

// BenchReport is the full benchmark output written to stdout (Markdown) and optionally -json.
type BenchReport struct {
	TimestampUTC string            `json:"timestamp_utc"`
	Flags        map[string]any    `json:"flags"`
	Host         HostInfo          `json:"host"`
	Footprint    FootprintSummary  `json:"footprint"`
	Scenarios    []ScenarioSummary `json:"scenarios"`
	CheckGates   []GateResult      `json:"check_gates,omitempty"`
	WallTimeS    float64           `json:"wall_time_s"`
}

// BinarySizes records the compiled binary sizes for pluxy and openai-compatible.
type BinarySizes struct {
	BenchCoreBytes          int64  `json:"bench_core_bytes"`
	BenchExtBytes           int64  `json:"bench_extension_bytes"`
	ReleaseCoreBytes        int64  `json:"release_core_bytes"`
	ReleaseExtBytes         int64  `json:"release_extension_bytes"`
	ReleaseFlagsDescription string `json:"release_flags"`
}

// StartTimeSummary records cold-start timings of pluxy.
type StartTimeSummary struct {
	Measured         bool    `json:"measured"`
	ExecToListenMS   float64 `json:"exec_to_listen_ms,omitempty"`
	ExecToFirstReqMS float64 `json:"exec_to_first_req_ms,omitempty"`
}

// FootprintSummary holds non-scenario-specific footprint numbers.
type FootprintSummary struct {
	StartTime   StartTimeSummary `json:"start_time"`
	BinarySizes *BinarySizes     `json:"binary_sizes,omitempty"`
	IdleBefore  IdleSnapshot     `json:"idle_before"`
	IdleAfter   IdleSnapshot     `json:"idle_after"`
}

// RunPair holds one paired run of direct vs proxy for a scenario.
type RunPair struct {
	RunIndex     int           `json:"run_index"`
	FirstSide    string        `json:"first_side"`
	Direct       SideRunResult `json:"direct"`
	Proxy        SideRunResult `json:"proxy"`
	AddedTTFT50  float64       `json:"added_ttft_p50_ms,omitempty"`
	AddedTTFT99  float64       `json:"added_ttft_p99_ms,omitempty"`
	AddedChunk50 float64       `json:"added_chunk_delay_p50_ms,omitempty"`
	AddedChunk99 float64       `json:"added_chunk_delay_p99_ms,omitempty"`
	AddedTotal50 float64       `json:"added_total_p50_ms"`
	AddedTotal99 float64       `json:"added_total_p99_ms"`
}

// SideSummary holds median/range summaries for one side across runs.
type SideSummary struct {
	ReqPathP50MS    MetricSummary `json:"req_path_p50_ms"`
	ReqPathP99MS    MetricSummary `json:"req_path_p99_ms"`
	TTFTP50MS       MetricSummary `json:"ttft_p50_ms,omitempty"`
	TTFTP99MS       MetricSummary `json:"ttft_p99_ms,omitempty"`
	ChunkDelayP50MS MetricSummary `json:"chunk_delay_p50_ms,omitempty"`
	ChunkDelayP99MS MetricSummary `json:"chunk_delay_p99_ms,omitempty"`
	TotalP50MS      MetricSummary `json:"total_p50_ms"`
	TotalP99MS      MetricSummary `json:"total_p99_ms"`
	RPS             MetricSummary `json:"rps"`
	UpstreamConns   MetricSummary `json:"upstream_conns"`
	TotalErrors     int           `json:"total_errors"`
	TotalFidelity   int           `json:"total_fidelity_failures"`
}

// AddedSummary holds median/range summaries of proxy minus direct latencies.
type AddedSummary struct {
	TTFTP50MS       MetricSummary `json:"ttft_p50_ms,omitempty"`
	TTFTP99MS       MetricSummary `json:"ttft_p99_ms,omitempty"`
	ChunkDelayP50MS MetricSummary `json:"chunk_delay_p50_ms,omitempty"`
	ChunkDelayP99MS MetricSummary `json:"chunk_delay_p99_ms,omitempty"`
	TotalP50MS      MetricSummary `json:"total_p50_ms"`
	TotalP99MS      MetricSummary `json:"total_p99_ms"`
}

// ProcessFootprintSummary aggregates RSS and CPU per process across runs.
type ProcessFootprintSummary struct {
	Name        string        `json:"name"`
	PeakRSSMB   MetricSummary `json:"peak_rss_mb"`
	CPUCores    MetricSummary `json:"cpu_cores"`
	CPUMSPerReq MetricSummary `json:"cpu_ms_per_req"`
}

// ScenarioSummary holds aggregated and raw per-run results for one scenario.
type ScenarioSummary struct {
	Name           string                    `json:"name"`
	Stream         bool                      `json:"stream"`
	Translate      bool                      `json:"translate"`
	Concurrency    int                       `json:"concurrency"`
	BodyKB         int                       `json:"body_kb"`
	IntervalMS     float64                   `json:"interval_ms,omitempty"`
	Direct         SideSummary               `json:"direct"`
	Proxy          SideSummary               `json:"proxy"`
	Added          AddedSummary              `json:"added"`
	PeakTotalRSSMB MetricSummary             `json:"peak_total_rss_mb,omitempty"`
	Processes      []ProcessFootprintSummary `json:"processes,omitempty"`
	Runs           []RunPair                 `json:"runs"`
}

func main() {
	if err := runMain(); err != nil {
		fmt.Fprintf(os.Stderr, "pluxy-bench: %v\n", err)
		os.Exit(1)
	}
}

func runMain() error {
	startWall := time.Now()

	var (
		runsFlag           = flag.Int("runs", 5, "number of paired direct/proxy runs (alternating order)")
		ttftFlag           = flag.Duration("ttft", 0, "override time to first text delta")
		chunksFlag         = flag.Int("chunks", 0, "override number of text deltas")
		intervalFlag       = flag.Duration("interval", 0, "override interval between text deltas")
		latencyFlag        = flag.Duration("latency", 0, "override non-streamed reply latency")
		bodyKBFlag         = flag.Int("body-kb", 100, "request body size in KB for standard scenarios")
		tlsFlag            = flag.Bool("tls", false, "attempt TLS upstream (note: extensions clear env and do not inherit SSL_CERT_FILE)")
		profileDirFlag     = flag.String("profile", "", "write CPU and heap pprof files for the selected scenarios (default agents and throughput) into DIR using go build -overlay")
		jsonFileFlag       = flag.String("json", "", "write full report including raw per-run values as JSON to FILE")
		proxyURLFlag       = flag.String("proxy-url", "", "measure an already-running proxy at URL instead of starting pluxy")
		proxyKeyFlag       = flag.String("proxy-key", "", "API key for -proxy-url")
		proxyPIDFlag       = flag.Int("proxy-pid", 0, "PID of -proxy-url process for Linux footprint measurement")
		proxyMsgModelFlag  = flag.String("proxy-model-messages", "bench-messages", "model name for Messages pass-through scenarios")
		proxyChatModelFlag = flag.String("proxy-model-chat", "bench-chat", "model name for Chat translation scenarios")
		mockAddrFlag       = flag.String("mock-addr", "127.0.0.1:0", "listen address for the mock upstream")
		checkFlag          = flag.Bool("check", false, "run CI regression gate check against budget.go")
		scenariosFlag      = flag.String("scenarios", "", "comma-separated subset of scenarios to run")
	)
	flag.Parse()

	if *tlsFlag {
		// Checked against next/internal/host/host.go (Extension.start): cmd.Env is reset to
		// PATH, PLUXY_PROTOCOL_VERSIONS, PLUXY_SOCKET, PLUXY_EXTENSION_ID (and SYSTEMROOT /
		// PLUXY_TEST_LOGIN_HOSTS), so extensions do not inherit SSL_CERT_FILE.
		return errors.New("-tls is unsupported without changing product code: next/internal/host/host.go clears cmd.Env when spawning extensions so SSL_CERT_FILE is not inherited")
	}

	setFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) {
		setFlags[f.Name] = true
	})

	runs := *runsFlag
	if *checkFlag && !setFlags["runs"] {
		runs = 3
	}
	if runs < 1 {
		return fmt.Errorf("-runs must be >= 1, got %d", runs)
	}

	nextRoot, err := findModuleRoot()
	if err != nil {
		return err
	}
	repoRoot := filepath.Dir(nextRoot)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mock, err := StartMock(*mockAddrFlag)
	if err != nil {
		return err
	}
	defer mock.Close()

	specs := buildScenarios(*checkFlag, *bodyKBFlag, setFlags, *ttftFlag, *chunksFlag, *intervalFlag, *latencyFlag)
	if *profileDirFlag != "" {
		specs, err = selectProfileScenarios(specs, *scenariosFlag)
		if err != nil {
			return err
		}
		written, err := RunProfileMode(ctx, nextRoot, *profileDirFlag, specs, mock)
		if err != nil {
			return err
		}
		fmt.Printf("Wrote %d profile files to %s:\n", len(written), *profileDirFlag)
		for _, p := range written {
			fmt.Printf("  %s\n", p)
		}
		return nil
	} else if *scenariosFlag != "" {
		specs, err = filterScenarios(specs, *scenariosFlag)
		if err != nil {
			return err
		}
	}

	host := CollectHostInfo(repoRoot)

	var (
		proxyURL     = strings.TrimRight(*proxyURLFlag, "/")
		proxyKey     = *proxyKeyFlag
		proxyPID     = *proxyPIDFlag
		footprint    FootprintSummary
		coreProc     *coreProcess
		buildCleanup func()
		idleSettle   = 10 * time.Second
	)
	if *checkFlag {
		idleSettle = 2 * time.Second
	}

	if proxyURL == "" {
		binDir, err := os.MkdirTemp("", "pluxy-bench-bin-")
		if err != nil {
			return err
		}
		buildCleanup = func() { _ = os.RemoveAll(binDir) }
		defer buildCleanup()

		coreBin, extBin, err := buildBenchBinaries(nextRoot, binDir)
		if err != nil {
			return err
		}
		if err := writeExtensionManifest(binDir); err != nil {
			return err
		}
		sizes, err := measureBinarySizes(nextRoot, coreBin, extBin, !*checkFlag)
		if err != nil {
			return err
		}
		footprint.BinarySizes = sizes

		cfgDir, err := os.MkdirTemp("", "pluxy-bench-cfg-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(cfgDir)
		cfgPath := filepath.Join(cfgDir, "config.json")
		if err := writeCoreConfig(cfgPath, filepath.Join(binDir, "pluxy-extension.json"), mock.BaseURL()); err != nil {
			return err
		}

		// Dedicated cold-start probe for start times so the 10s idle settle stays traffic-free.
		startTime, err := measureColdStart(ctx, coreBin, cfgPath, mock)
		if err != nil {
			return fmt.Errorf("cold start probe: %w", err)
		}
		footprint.StartTime = startTime

		coreProc, err = startCoreProcess(coreBin, cfgPath, nil)
		if err != nil {
			return err
		}
		defer coreProc.Stop()

		proxyURL = coreProc.BaseURL
		proxyKey = benchClientKey
		proxyPID = coreProc.PID

		select {
		case <-time.After(idleSettle):
		case <-ctx.Done():
			return ctx.Err()
		}
		footprint.IdleBefore = MeasureIdleRSS(proxyPID)
	} else {
		if proxyKey == "" {
			return errors.New("-proxy-key is required when -proxy-url is set")
		}
		if proxyPID > 0 {
			footprint.IdleBefore = MeasureIdleRSS(proxyPID)
		}
	}

	// Execute runs, alternating which side runs first on each run.
	perScenarioRuns := make([][]RunPair, len(specs))
	for r := 0; r < runs; r++ {
		proxyFirst := r%2 == 1
		for sIdx, spec := range specs {
			model := *proxyMsgModelFlag
			if spec.Translate {
				model = *proxyChatModelFlag
			}
			var directRes, proxyRes SideRunResult
			runSide := func(isProxy bool) error {
				if isProxy {
					var tracker *FootprintTracker
					if proxyPID > 0 {
						tracker = StartFootprintTracker(proxyPID)
					}
					res, err := RunSide(ctx, spec, true, proxyURL, proxyKey, model, mock)
					if tracker != nil {
						res.Footprint = tracker.Stop(res.Completed)
					}
					proxyRes = res
					return err
				}
				res, err := RunSide(ctx, spec, false, mock.RootURL(), benchUpstreamKey, model, mock)
				directRes = res
				return err
			}

			firstSide := "direct"
			if proxyFirst {
				firstSide = "proxy"
				if err := runSide(true); err != nil {
					return fmt.Errorf("run %d %s proxy: %w", r+1, spec.Name, err)
				}
				if err := runSide(false); err != nil {
					return fmt.Errorf("run %d %s direct: %w", r+1, spec.Name, err)
				}
			} else {
				if err := runSide(false); err != nil {
					return fmt.Errorf("run %d %s direct: %w", r+1, spec.Name, err)
				}
				if err := runSide(true); err != nil {
					return fmt.Errorf("run %d %s proxy: %w", r+1, spec.Name, err)
				}
			}

			pair := RunPair{
				RunIndex:     r + 1,
				FirstSide:    firstSide,
				Direct:       directRes,
				Proxy:        proxyRes,
				AddedTotal50: proxyRes.TotalP50MS - directRes.TotalP50MS,
				AddedTotal99: proxyRes.TotalP99MS - directRes.TotalP99MS,
			}
			if spec.Stream {
				pair.AddedTTFT50 = proxyRes.TTFTP50MS - directRes.TTFTP50MS
				pair.AddedTTFT99 = proxyRes.TTFTP99MS - directRes.TTFTP99MS
				pair.AddedChunk50 = proxyRes.ChunkDelayP50MS - directRes.ChunkDelayP50MS
				pair.AddedChunk99 = proxyRes.ChunkDelayP99MS - directRes.ChunkDelayP99MS
			}
			perScenarioRuns[sIdx] = append(perScenarioRuns[sIdx], pair)
		}
	}

	if proxyPID > 0 && !*checkFlag {
		select {
		case <-time.After(idleSettle):
		case <-ctx.Done():
			return ctx.Err()
		}
		footprint.IdleAfter = MeasureIdleRSS(proxyPID)
	}
	host.LoadAfter = ReadLoadAvg()

	summaries := make([]ScenarioSummary, len(specs))
	for i, spec := range specs {
		summaries[i] = summarizeScenario(spec, perScenarioRuns[i])
	}

	rep := BenchReport{
		TimestampUTC: time.Now().UTC().Format(time.RFC3339),
		Flags: map[string]any{
			"runs":          runs,
			"body_kb":       *bodyKBFlag,
			"check":         *checkFlag,
			"proxy_url":     *proxyURLFlag,
			"idle_settle_s": idleSettle.Seconds(),
		},
		Host:      host,
		Footprint: footprint,
		Scenarios: summaries,
		WallTimeS: time.Since(startWall).Seconds(),
	}

	var failedGates []string
	if *checkFlag {
		rep.CheckGates = EvaluateCheckGates(&rep)
	}

	if *jsonFileFlag != "" {
		jb, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		jb = append(jb, '\n')
		if err := os.WriteFile(*jsonFileFlag, jb, 0o644); err != nil {
			return err
		}
	}

	fmt.Print(renderMarkdown(&rep))
	if *checkFlag {
		gateText, failed := FormatGateSummary(rep.CheckGates)
		failedGates = failed
		fmt.Print(gateText)
	}
	fmt.Printf("\nTotal benchmark wall time: %.2f s\n", rep.WallTimeS)

	if len(failedGates) > 0 {
		return fmt.Errorf("check failed: %d gate(s) over budget", len(failedGates))
	}
	return nil
}

func buildScenarios(
	check bool,
	bodyKB int,
	setFlags map[string]bool,
	ttft time.Duration,
	chunks int,
	interval time.Duration,
	latency time.Duration,
) []ScenarioSpec {
	var specs []ScenarioSpec
	if check {
		// Compact request counts keep the full 3-run check under 90s on a 4-vCPU runner while
		// preserving real TTFT (100ms), interval (10ms, >=100 chunks/run for p99), and 8s agents.
		specs = []ScenarioSpec{
			{
				Name:        "stream-pass",
				Stream:      true,
				Concurrency: 1,
				Requests:    5,
				BodyKB:      bodyKB,
				TTFT:        100 * time.Millisecond,
				Chunks:      10,
				Interval:    10 * time.Millisecond,
			},
			{
				Name:        "stream-translate",
				Stream:      true,
				Translate:   true,
				Concurrency: 1,
				Requests:    5,
				BodyKB:      bodyKB,
				TTFT:        100 * time.Millisecond,
				Chunks:      10,
				Interval:    10 * time.Millisecond,
			},
			{
				Name:        "nonstream-pass",
				Stream:      false,
				Concurrency: 1,
				Requests:    5,
				BodyKB:      bodyKB,
				Latency:     100 * time.Millisecond,
			},
			{
				Name:        "agents",
				Stream:      true,
				Concurrency: 80,
				Duration:    8 * time.Second,
				BodyKB:      bodyKB,
				TTFT:        300 * time.Millisecond,
				Chunks:      100,
				Interval:    30 * time.Millisecond,
			},
		}
	} else {
		specs = []ScenarioSpec{
			{
				Name:        "stream-pass",
				Stream:      true,
				Concurrency: 1,
				Requests:    30,
				BodyKB:      bodyKB,
				TTFT:        100 * time.Millisecond,
				Chunks:      50,
				Interval:    10 * time.Millisecond,
			},
			{
				Name:        "stream-translate",
				Stream:      true,
				Translate:   true,
				Concurrency: 1,
				Requests:    30,
				BodyKB:      bodyKB,
				TTFT:        100 * time.Millisecond,
				Chunks:      50,
				Interval:    10 * time.Millisecond,
			},
			{
				Name:        "nonstream-pass",
				Stream:      false,
				Concurrency: 1,
				Requests:    30,
				BodyKB:      bodyKB,
				Latency:     100 * time.Millisecond,
			},
			{
				Name:        "nonstream-translate",
				Stream:      false,
				Translate:   true,
				Concurrency: 1,
				Requests:    30,
				BodyKB:      bodyKB,
				Latency:     100 * time.Millisecond,
			},
			{
				Name:        "agents",
				Stream:      true,
				Concurrency: 80,
				Duration:    20 * time.Second,
				BodyKB:      bodyKB,
				TTFT:        300 * time.Millisecond,
				Chunks:      100,
				Interval:    30 * time.Millisecond,
			},
			{
				Name:        "throughput",
				Stream:      false,
				Concurrency: 16,
				Duration:    10 * time.Second,
				BodyKB:      bodyKB,
				Latency:     0,
			},
			{
				Name:        "large-body",
				Stream:      true,
				Concurrency: 1,
				Requests:    20,
				BodyKB:      800,
				TTFT:        100 * time.Millisecond,
				Chunks:      10,
				Interval:    10 * time.Millisecond,
			},
		}
	}

	for i := range specs {
		if specs[i].Stream {
			if setFlags["ttft"] {
				specs[i].TTFT = ttft
			}
			if setFlags["chunks"] {
				specs[i].Chunks = chunks
			}
			if setFlags["interval"] {
				specs[i].Interval = interval
			}
		} else if setFlags["latency"] {
			specs[i].Latency = latency
		}
	}
	return specs
}

func filterScenarios(specs []ScenarioSpec, filter string) ([]ScenarioSpec, error) {
	want := make(map[string]bool)
	for _, part := range strings.Split(filter, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			want[part] = true
		}
	}
	var out []ScenarioSpec
	for _, s := range specs {
		if want[s.Name] {
			out = append(out, s)
			delete(want, s.Name)
		}
	}
	if len(want) > 0 {
		var unknown []string
		for k := range want {
			unknown = append(unknown, k)
		}
		slices.Sort(unknown)
		return nil, fmt.Errorf("unknown scenario(s): %s", strings.Join(unknown, ", "))
	}
	return out, nil
}

func summarizeScenario(spec ScenarioSpec, runs []RunPair) ScenarioSummary {
	summarizeSide := func(pick func(RunPair) SideRunResult) SideSummary {
		var rp50, rp99, tt50, tt99, cd50, cd99, tot50, tot99, rps, conns []float64
		var errs, fid int
		for _, r := range runs {
			sr := pick(r)
			rp50 = append(rp50, sr.ReqPathP50MS)
			rp99 = append(rp99, sr.ReqPathP99MS)
			tt50 = append(tt50, sr.TTFTP50MS)
			tt99 = append(tt99, sr.TTFTP99MS)
			cd50 = append(cd50, sr.ChunkDelayP50MS)
			cd99 = append(cd99, sr.ChunkDelayP99MS)
			tot50 = append(tot50, sr.TotalP50MS)
			tot99 = append(tot99, sr.TotalP99MS)
			rps = append(rps, sr.RPS)
			conns = append(conns, float64(sr.UpstreamConns))
			errs += sr.Errors
			fid += sr.FidelityFails
		}
		s := SideSummary{
			ReqPathP50MS:  Summarize(rp50),
			ReqPathP99MS:  Summarize(rp99),
			TotalP50MS:    Summarize(tot50),
			TotalP99MS:    Summarize(tot99),
			RPS:           Summarize(rps),
			UpstreamConns: Summarize(conns),
			TotalErrors:   errs,
			TotalFidelity: fid,
		}
		if spec.Stream {
			s.TTFTP50MS = Summarize(tt50)
			s.TTFTP99MS = Summarize(tt99)
			s.ChunkDelayP50MS = Summarize(cd50)
			s.ChunkDelayP99MS = Summarize(cd99)
		}
		return s
	}

	var addTT50, addTT99, addCD50, addCD99, addTot50, addTot99 []float64
	var peakTotals []float64
	procOrder := []string{}
	procSeen := map[string]bool{}
	procRSS := map[string][]float64{}
	procCores := map[string][]float64{}
	procMS := map[string][]float64{}

	for _, r := range runs {
		addTot50 = append(addTot50, r.AddedTotal50)
		addTot99 = append(addTot99, r.AddedTotal99)
		if spec.Stream {
			addTT50 = append(addTT50, r.AddedTTFT50)
			addTT99 = append(addTT99, r.AddedTTFT99)
			addCD50 = append(addCD50, r.AddedChunk50)
			addCD99 = append(addCD99, r.AddedChunk99)
		}
		if fp := r.Proxy.Footprint; fp != nil && fp.Measured {
			peakTotals = append(peakTotals, TotalRSSMB(fp.PeakRSS))
			for _, pr := range fp.PeakRSS {
				if !procSeen[pr.Name] {
					procSeen[pr.Name] = true
					procOrder = append(procOrder, pr.Name)
				}
				procRSS[pr.Name] = append(procRSS[pr.Name], pr.RSSMB)
			}
			for _, pc := range fp.CPU {
				if !procSeen[pc.Name] {
					procSeen[pc.Name] = true
					procOrder = append(procOrder, pc.Name)
				}
				procCores[pc.Name] = append(procCores[pc.Name], pc.Cores)
				procMS[pc.Name] = append(procMS[pc.Name], pc.MSPerReq)
			}
		}
	}

	added := AddedSummary{
		TotalP50MS: Summarize(addTot50),
		TotalP99MS: Summarize(addTot99),
	}
	if spec.Stream {
		added.TTFTP50MS = Summarize(addTT50)
		added.TTFTP99MS = Summarize(addTT99)
		added.ChunkDelayP50MS = Summarize(addCD50)
		added.ChunkDelayP99MS = Summarize(addCD99)
	}

	var procs []ProcessFootprintSummary
	for _, name := range procOrder {
		procs = append(procs, ProcessFootprintSummary{
			Name:        name,
			PeakRSSMB:   Summarize(procRSS[name]),
			CPUCores:    Summarize(procCores[name]),
			CPUMSPerReq: Summarize(procMS[name]),
		})
	}

	sum := ScenarioSummary{
		Name:        spec.Name,
		Stream:      spec.Stream,
		Translate:   spec.Translate,
		Concurrency: spec.Concurrency,
		BodyKB:      spec.BodyKB,
		Direct:      summarizeSide(func(r RunPair) SideRunResult { return r.Direct }),
		Proxy:       summarizeSide(func(r RunPair) SideRunResult { return r.Proxy }),
		Added:       added,
		Processes:   procs,
		Runs:        runs,
	}
	if spec.Stream {
		sum.IntervalMS = float64(spec.Interval) / float64(time.Millisecond)
	}
	if len(peakTotals) > 0 {
		sum.PeakTotalRSSMB = Summarize(peakTotals)
	}
	return sum
}

func renderMarkdown(rep *BenchReport) string {
	var b strings.Builder
	b.WriteString("# pluxy-bench Report\n\n")
	fmt.Fprintf(&b, "- **Timestamp (UTC):** %s\n", rep.TimestampUTC)
	fmt.Fprintf(&b, "- **Git commit:** `%s`\n", rep.Host.GitCommit)
	fmt.Fprintf(&b, "- **Go version:** `%s`\n", rep.Host.GoVersion)
	fmt.Fprintf(&b, "- **OS / Arch:** `%s`\n", rep.Host.OSArch)
	fmt.Fprintf(&b, "- **CPU:** %s (%d logical CPUs)\n", rep.Host.CPUModel, rep.Host.CPUCount)
	fmt.Fprintf(&b, "- **Memory:** %s\n", rep.Host.Memory)
	fmt.Fprintf(&b, "- **Load average:** before `%s` -> after `%s`\n", rep.Host.LoadBefore, rep.Host.LoadAfter)
	fmt.Fprintf(&b, "- **Flags:** runs=%d, body_kb=%d, check=%v, idle_settle=%.0fs\n",
		rep.Flags["runs"], rep.Flags["body_kb"], rep.Flags["check"], rep.Flags["idle_settle_s"])
	b.WriteByte('\n')

	b.WriteString("## Footprint & Startup\n\n")
	if rep.Footprint.StartTime.Measured {
		fmt.Fprintf(&b, "- **Start time:** exec->listening `%.2f ms`, exec->first proxied request `%.2f ms`\n",
			rep.Footprint.StartTime.ExecToListenMS, rep.Footprint.StartTime.ExecToFirstReqMS)
	} else {
		b.WriteString("- **Start time:** not measured\n")
	}
	if bs := rep.Footprint.BinarySizes; bs != nil {
		fmt.Fprintf(&b, "- **Binary sizes (bench `go build -buildvcs=false`):** `pluxy` %.2f MiB (%d B), `openai-compatible` %.2f MiB (%d B)\n",
			float64(bs.BenchCoreBytes)/(1024*1024), bs.BenchCoreBytes,
			float64(bs.BenchExtBytes)/(1024*1024), bs.BenchExtBytes)
		if bs.ReleaseCoreBytes > 0 {
			fmt.Fprintf(&b, "- **Binary sizes (release `%s`):** `pluxy` %.2f MiB (%d B), `openai-compatible` %.2f MiB (%d B)\n",
				bs.ReleaseFlagsDescription,
				float64(bs.ReleaseCoreBytes)/(1024*1024), bs.ReleaseCoreBytes,
				float64(bs.ReleaseExtBytes)/(1024*1024), bs.ReleaseExtBytes)
		}
	}
	formatIdle := func(label string, snap IdleSnapshot) {
		if !snap.Measured {
			fmt.Fprintf(&b, "- **%s:** not measured\n", label)
			return
		}
		parts := make([]string, 0, len(snap.Processes))
		for _, p := range snap.Processes {
			parts = append(parts, fmt.Sprintf("%s=%.2f MB", p.Name, p.RSSMB))
		}
		fmt.Fprintf(&b, "- **%s:** %s\n", label, strings.Join(parts, ", "))
	}
	formatIdle("Idle RSS before", rep.Footprint.IdleBefore)
	formatIdle("Idle RSS after", rep.Footprint.IdleAfter)
	b.WriteByte('\n')

	for _, sc := range rep.Scenarios {
		fmt.Fprintf(&b, "## Scenario: `%s` (conc=%d, body=%d KB, stream=%v, translate=%v)\n\n",
			sc.Name, sc.Concurrency, sc.BodyKB, sc.Stream, sc.Translate)
		b.WriteString("| Metric | Direct | Proxy | Added (Proxy - Direct) |\n")
		b.WriteString("| --- | --- | --- | --- |\n")
		fmt.Fprintf(&b, "| Request path p50 (ms) | %s | %s | - |\n",
			FormatSummary(sc.Direct.ReqPathP50MS, 2), FormatSummary(sc.Proxy.ReqPathP50MS, 2))
		fmt.Fprintf(&b, "| Request path p99 (ms) | %s | %s | - |\n",
			FormatSummary(sc.Direct.ReqPathP99MS, 2), FormatSummary(sc.Proxy.ReqPathP99MS, 2))
		if sc.Stream {
			fmt.Fprintf(&b, "| TTFT p50 (ms) | %s | %s | %s |\n",
				FormatSummary(sc.Direct.TTFTP50MS, 2), FormatSummary(sc.Proxy.TTFTP50MS, 2), FormatSummary(sc.Added.TTFTP50MS, 2))
			fmt.Fprintf(&b, "| TTFT p99 (ms) | %s | %s | %s |\n",
				FormatSummary(sc.Direct.TTFTP99MS, 2), FormatSummary(sc.Proxy.TTFTP99MS, 2), FormatSummary(sc.Added.TTFTP99MS, 2))
			fmt.Fprintf(&b, "| Chunk delay p50 (ms) | %s | %s | %s |\n",
				FormatSummary(sc.Direct.ChunkDelayP50MS, 3), FormatSummary(sc.Proxy.ChunkDelayP50MS, 3), FormatSummary(sc.Added.ChunkDelayP50MS, 3))
			fmt.Fprintf(&b, "| Chunk delay p99 (ms) | %s | %s | %s |\n",
				FormatSummary(sc.Direct.ChunkDelayP99MS, 3), FormatSummary(sc.Proxy.ChunkDelayP99MS, 3), FormatSummary(sc.Added.ChunkDelayP99MS, 3))
		}
		fmt.Fprintf(&b, "| Total p50 (ms) | %s | %s | %s |\n",
			FormatSummary(sc.Direct.TotalP50MS, 2), FormatSummary(sc.Proxy.TotalP50MS, 2), FormatSummary(sc.Added.TotalP50MS, 2))
		fmt.Fprintf(&b, "| Total p99 (ms) | %s | %s | %s |\n",
			FormatSummary(sc.Direct.TotalP99MS, 2), FormatSummary(sc.Proxy.TotalP99MS, 2), FormatSummary(sc.Added.TotalP99MS, 2))
		fmt.Fprintf(&b, "| Completed RPS | %s | %s | - |\n",
			FormatSummary(sc.Direct.RPS, 2), FormatSummary(sc.Proxy.RPS, 2))
		fmt.Fprintf(&b, "| Upstream conns opened | %s | %s | - |\n",
			FormatSummary(sc.Direct.UpstreamConns, 0), FormatSummary(sc.Proxy.UpstreamConns, 0))
		fmt.Fprintf(&b, "| Errors (sum) | %d | %d | - |\n", sc.Direct.TotalErrors, sc.Proxy.TotalErrors)
		fmt.Fprintf(&b, "| Fidelity failures (sum) | %d | %d | - |\n", sc.Direct.TotalFidelity, sc.Proxy.TotalFidelity)
		b.WriteByte('\n')

		if len(sc.Processes) > 0 {
			b.WriteString("| Process | Peak RSS (MB) | CPU (cores) | CPU (ms/req) |\n")
			b.WriteString("| --- | --- | --- | --- |\n")
			for _, p := range sc.Processes {
				fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
					p.Name, FormatSummary(p.PeakRSSMB, 2), FormatSummary(p.CPUCores, 3), FormatSummary(p.CPUMSPerReq, 3))
			}
			b.WriteByte('\n')
		} else {
			b.WriteString("_Footprint: not measured_\n\n")
		}
	}
	return b.String()
}

func findModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run inside the next module (no go.mod found in current directory or parents)")
		}
		dir = parent
	}
}

func binaryName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func buildBenchBinaries(nextRoot, binDir string) (coreBin, extBin string, err error) {
	coreBin = filepath.Join(binDir, binaryName("pluxy"))
	extBin = filepath.Join(binDir, binaryName("openai-compatible"))
	for _, b := range []struct{ out, pkg string }{
		{coreBin, "./cmd/pluxy"},
		{extBin, "./extensions/openai-compatible"},
	} {
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", b.out, b.pkg)
		cmd.Dir = nextRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("building %s: %w\n%s", b.pkg, err, out)
		}
	}
	return coreBin, extBin, nil
}

func measureBinarySizes(nextRoot, coreBin, extBin string, buildRelease bool) (*BinarySizes, error) {
	benchCoreStat, err := os.Stat(coreBin)
	if err != nil {
		return nil, err
	}
	benchExtStat, err := os.Stat(extBin)
	if err != nil {
		return nil, err
	}
	if !buildRelease {
		return &BinarySizes{
			BenchCoreBytes: benchCoreStat.Size(),
			BenchExtBytes:  benchExtStat.Size(),
		}, nil
	}
	relDir, err := os.MkdirTemp("", "pluxy-bench-rel-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(relDir)

	relCore := filepath.Join(relDir, binaryName("pluxy"))
	relExt := filepath.Join(relDir, binaryName("openai-compatible"))
	for _, b := range []struct{ out, pkg string }{
		{relCore, "./cmd/pluxy"},
		{relExt, "./extensions/openai-compatible"},
	} {
		cmd := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-ldflags", "-s -w", "-o", b.out, b.pkg)
		cmd.Dir = nextRoot
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("building release %s: %w\n%s", b.pkg, err, out)
		}
	}
	relCoreStat, err := os.Stat(relCore)
	if err != nil {
		return nil, err
	}
	relExtStat, err := os.Stat(relExt)
	if err != nil {
		return nil, err
	}
	return &BinarySizes{
		BenchCoreBytes:          benchCoreStat.Size(),
		BenchExtBytes:           benchExtStat.Size(),
		ReleaseCoreBytes:        relCoreStat.Size(),
		ReleaseExtBytes:         relExtStat.Size(),
		ReleaseFlagsDescription: "CGO_ENABLED=0 -buildvcs=false -trimpath -ldflags=-s -w",
	}, nil
}

func writeExtensionManifest(binDir string) error {
	manifest := map[string]any{
		"manifest_version": 0,
		"id":               "pluxy/openai-compatible",
		"version":          "0.1.0",
		"protocol":         []int{0},
		"programs": map[string]string{
			runtime.GOOS + "/" + runtime.GOARCH: binaryName("openai-compatible"),
		},
	}
	b, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(binDir, "pluxy-extension.json"), b, 0o644)
}

func writeCoreConfig(cfgPath, manifestPath, mockBaseURL string) error {
	cfg := map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{benchClientKey},
		"extension": map[string]any{
			"manifest": manifestPath,
			"accounts": []any{
				map[string]any{
					"id":     "bench-messages",
					"models": []string{"bench-messages"},
					"settings": map[string]any{
						"base_url": mockBaseURL,
						"wire_api": "messages",
					},
					"credential": map[string]any{
						"kind":    "api_key",
						"api_key": benchUpstreamKey,
					},
				},
				map[string]any{
					"id":     "bench-chat",
					"models": []string{"bench-chat"},
					"settings": map[string]any{
						"base_url": mockBaseURL,
						"wire_api": "chat",
					},
					"credential": map[string]any{
						"kind":    "api_key",
						"api_key": benchUpstreamKey,
					},
				},
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, b, 0o600)
}

type coreProcess struct {
	BaseURL  string
	PID      int
	ExecAt   time.Time
	ListenAt time.Time

	cmd      *exec.Cmd
	exited   chan struct{}
	stopOnce sync.Once
}

func startCoreProcess(coreBin, cfgPath string, extraEnv []string) (*coreProcess, error) {
	cmd := exec.Command(coreBin, "-config", cfgPath)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	execAt := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting pluxy: %w", err)
	}

	addrCh := make(chan string, 1)
	var logMu sync.Mutex
	var logLines []string
	exited := make(chan struct{})

	go func() {
		sc := bufio.NewScanner(stderrPipe)
		sc.Buffer(make([]byte, 0, 16*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSuffix(sc.Text(), "\r")
			logMu.Lock()
			logLines = append(logLines, line)
			logMu.Unlock()
			if rest, ok := strings.CutPrefix(line, "pluxy: listening on "); ok {
				select {
				case addrCh <- rest:
				default:
				}
			}
		}
		close(addrCh)
	}()

	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	proc := &coreProcess{
		PID:    cmd.Process.Pid,
		ExecAt: execAt,
		cmd:    cmd,
		exited: exited,
	}

	select {
	case addr, ok := <-addrCh:
		if !ok || addr == "" {
			<-exited
			logMu.Lock()
			dump := strings.Join(logLines, "\n")
			logMu.Unlock()
			return nil, fmt.Errorf("pluxy exited before reporting listening address:\n%s", dump)
		}
		proc.ListenAt = time.Now()
		proc.BaseURL = addr
		return proc, nil
	case <-time.After(15 * time.Second):
		_ = proc.Stop()
		logMu.Lock()
		dump := strings.Join(logLines, "\n")
		logMu.Unlock()
		return nil, fmt.Errorf("timed out waiting for pluxy listening address:\n%s", dump)
	}
}

func (p *coreProcess) Stop() error {
	var err error
	p.stopOnce.Do(func() {
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(os.Interrupt)
		}
		select {
		case <-p.exited:
		case <-time.After(10 * time.Second):
			if p.cmd != nil && p.cmd.Process != nil {
				err = p.cmd.Process.Kill()
			}
			<-p.exited
		}
	})
	return err
}

func measureColdStart(ctx context.Context, coreBin, cfgPath string, mock *MockServer) (StartTimeSummary, error) {
	mock.ResetRecords()
	mock.SetTiming(MockTiming{Latency: 0})

	proc, err := startCoreProcess(coreBin, cfgPath, nil)
	if err != nil {
		return StartTimeSummary{}, err
	}
	defer proc.Stop()

	reqID := nextReqID()
	body := []byte(fmt.Sprintf(`{"model":"bench-messages","stream":false,"max_tokens":16,"messages":[{"role":"user","content":"warmup bm-req:%s;"}]}`, reqID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, proc.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return StartTimeSummary{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", benchClientKey)
	req.Header.Set("Anthropic-Version", "2023-06-01")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return StartTimeSummary{}, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	client.CloseIdleConnections()
	if resp.StatusCode != http.StatusOK {
		return StartTimeSummary{}, fmt.Errorf("warmup request returned status %d", resp.StatusCode)
	}
	firstReqAt := time.Now()
	mock.TakeRecord(reqID)

	return StartTimeSummary{
		Measured:         true,
		ExecToListenMS:   float64(proc.ListenAt.Sub(proc.ExecAt)) / float64(time.Millisecond),
		ExecToFirstReqMS: float64(firstReqAt.Sub(proc.ExecAt)) / float64(time.Millisecond),
	}, nil
}
