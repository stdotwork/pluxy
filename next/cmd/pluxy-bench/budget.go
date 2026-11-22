package main

import (
	"fmt"
	"math"
	"runtime"
	"strings"
)

// hostOS is the platform whose footprint the gates expect to see; a test sets it.
var hostOS = runtime.GOOS

// Check-mode budgets. They were set from the median of three `-check` runs on main after
// performance fixes 1 to 5 (#518, #522, #535, #552, #559), on GitHub's ubuntu-latest runners
// (4 vCPU): runs 44852547274 (main at d9d1ee2d9), 44850768612 and 44848492287. Time budgets
// are ratios to the direct path in the same run, so the mock's timing and the client's own costs
// cancel out; machine speed does not, so each leaves room for two to three times the overhead
// measured on CI, and these take three. Where three times the median overhead is above the
// budget a fix already set, the budget stays. Lower a budget when a fix lowers its measured
// value.
const (
	// BudgetStreamPassTTFTRatio bounds proxy p50 TTFT / direct p50 TTFT in stream-pass (mock
	// TTFT 100 ms). Median of three CI runs: 1.034 (run 44852547274, main at d9d1ee2d9), 1.047
	// (run 44850768612) and 1.030 (run 44848492287); at a 100 KB body, pluxy adds 3.5 ms in the
	// median run. Three times that overhead is 1.102, so the budget stays at 1.10.
	BudgetStreamPassTTFTRatio = 1.10

	// BudgetStreamTranslateTTFTRatio bounds the same ratio in stream-translate (Messages to
	// Chat Completions). Median of three CI runs: 1.039 (run 44852547274, main at d9d1ee2d9),
	// 1.052 (run 44850768612) and 1.033 (run 44848492287): 3.9 ms at a 100 KB body in the
	// median run. Three times that overhead is 1.117, down from 1.15.
	BudgetStreamTranslateTTFTRatio = 1.12

	// BudgetStreamPassChunkDelayFraction bounds proxy p99 chunk delay / the mock's chunk
	// interval (10 ms) in stream-pass. Median of three CI runs: 0.050 (run 44852547274, main
	// at d9d1ee2d9), 0.052 (run 44850768612) and 0.046 (run 44848492287), about 0.5 ms. Three
	// times the median is 0.15, down from 0.2; the widest run measured 0.064.
	BudgetStreamPassChunkDelayFraction = 0.15

	// BudgetStreamTranslateChunkDelayFraction is the same bound in stream-translate. Median of
	// three CI runs: 0.051 (run 44852547274, main at d9d1ee2d9), 0.061 (run 44850768612) and
	// 0.053 (run 44848492287). Three times the median is 0.159, down from 0.2; the widest run
	// measured 0.079.
	BudgetStreamTranslateChunkDelayFraction = 0.16

	// BudgetNonstreamPassTotalRatio bounds proxy p50 total / direct p50 total in
	// nonstream-pass (mock latency 100 ms). Median of three CI runs: 1.036 (run 44852547274,
	// main at d9d1ee2d9), 1.049 (run 44850768612) and 1.033 (run 44848492287); at a 100 KB
	// body, pluxy adds 3.6 ms in the median run. Three times that overhead is 1.108, so the
	// budget stays at 1.10.
	BudgetNonstreamPassTotalRatio = 1.10

	// BudgetAgentsTotalP99Ratio bounds proxy p99 total / direct p99 total with 80 streams open
	// at once (about 3.3 s each). Median of three CI runs: 1.032 (run 44852547274, main at
	// d9d1ee2d9), 1.053 (run 44850768612) and 1.031 (run 44848492287). Three times that
	// overhead is 1.096, so the budget stays at 1.10.
	BudgetAgentsTotalP99Ratio = 1.10

	// BudgetIdleRSSMB bounds the resident memory (MB) of core and its extension processes,
	// started and idle, before any request. Median of three CI runs: 45.74 MB (run 44852547274,
	// main at d9d1ee2d9), 45.45 MB (run 44850768612) and 43.77 MB (run 44848492287). Three
	// times the median is 136 MB, so the budget stays at 100.
	BudgetIdleRSSMB = 100.0

	// BudgetAgentsRSSPerStreamKB bounds (peak RSS under agents - idle RSS) / 80 streams, in KB,
	// at a 100 KB request body. Median of three CI runs: 632.40 KB (run 44852547274, main at
	// d9d1ee2d9), 754.55 KB (run 44850768612) and 710.80 KB (run 44848492287), against about
	// 800 KB before the fixes. Three times the median is 2,132, so the budget is 2,200, down
	// from 3,072.
	BudgetAgentsRSSPerStreamKB = 2200.0
)

// GateResult captures one check gate's measured value, budget limit, and pass/fail outcome.
type GateResult struct {
	Name   string  `json:"name"`
	Value  string  `json:"value"`
	Budget string  `json:"budget"`
	Passed bool    `json:"passed"`
	Detail string  `json:"detail,omitempty"`
	RawVal float64 `json:"raw_value"`
	RawMax float64 `json:"raw_budget"`
}

// EvaluateCheckGates checks all budget gates against the benchmark results.
func EvaluateCheckGates(rep *BenchReport) []GateResult {
	byName := make(map[string]*ScenarioSummary, len(rep.Scenarios))
	for i := range rep.Scenarios {
		byName[rep.Scenarios[i].Name] = &rep.Scenarios[i]
	}

	var gates []GateResult

	// 1. Hard gate: zero fidelity failures and zero errors across every scenario, side, and run.
	var totalErrors, totalFidelity int
	for _, sc := range rep.Scenarios {
		for _, r := range sc.Runs {
			totalErrors += r.Direct.Errors + r.Proxy.Errors
			totalFidelity += r.Direct.FidelityFails + r.Proxy.FidelityFails
		}
	}
	gates = append(gates, GateResult{
		Name:   "fidelity failures (hard)",
		Value:  fmt.Sprintf("%d", totalFidelity),
		Budget: "0",
		Passed: totalFidelity == 0,
		RawVal: float64(totalFidelity),
		RawMax: 0,
	})
	gates = append(gates, GateResult{
		Name:   "request errors (hard)",
		Value:  fmt.Sprintf("%d", totalErrors),
		Budget: "0",
		Passed: totalErrors == 0,
		RawVal: float64(totalErrors),
		RawMax: 0,
	})

	ratioGate := func(name, scName string, budget float64, pick func(RunPair) (num, den float64)) {
		sc := byName[scName]
		if sc == nil || len(sc.Runs) == 0 {
			gates = append(gates, GateResult{
				Name:   name,
				Value:  "missing",
				Budget: fmt.Sprintf("<= %.3f", budget),
				Passed: false,
				RawMax: budget,
			})
			return
		}
		ratios := make([]float64, 0, len(sc.Runs))
		for _, r := range sc.Runs {
			num, den := pick(r)
			if den <= 0 {
				ratios = append(ratios, math.Inf(1))
			} else {
				ratios = append(ratios, num/den)
			}
		}
		sum := Summarize(ratios)
		gates = append(gates, GateResult{
			Name:   name,
			Value:  fmt.Sprintf("%.3fx (%.3f..%.3f)", sum.Median, sum.Min, sum.Max),
			Budget: fmt.Sprintf("<= %.3fx", budget),
			Passed: sum.Median <= budget,
			RawVal: sum.Median,
			RawMax: budget,
		})
	}

	// 2. TTFT ratios.
	ratioGate("stream-pass TTFT p50 ratio (proxy/direct)", "stream-pass", BudgetStreamPassTTFTRatio, func(r RunPair) (float64, float64) {
		return r.Proxy.TTFTP50MS, r.Direct.TTFTP50MS
	})
	ratioGate("stream-translate TTFT p50 ratio (proxy/direct)", "stream-translate", BudgetStreamTranslateTTFTRatio, func(r RunPair) (float64, float64) {
		return r.Proxy.TTFTP50MS, r.Direct.TTFTP50MS
	})

	// 3. Chunk delay p99 as a fraction of mock interval.
	chunkGate := func(name, scName string, budget float64) {
		sc := byName[scName]
		if sc == nil || len(sc.Runs) == 0 || sc.IntervalMS <= 0 {
			gates = append(gates, GateResult{
				Name:   name,
				Value:  "missing",
				Budget: fmt.Sprintf("<= %.3f", budget),
				Passed: false,
				RawMax: budget,
			})
			return
		}
		for _, r := range sc.Runs {
			if r.Proxy.ChunkSamples == 0 {
				// No chunk marker was read, so there is no delay to compare with the budget.
				gates = append(gates, GateResult{
					Name:   name,
					Value:  "not measured (no chunk delays recorded)",
					Budget: fmt.Sprintf("<= %.3f", budget),
					Passed: false,
					RawMax: budget,
				})
				return
			}
		}
		fracs := make([]float64, 0, len(sc.Runs))
		for _, r := range sc.Runs {
			fracs = append(fracs, r.Proxy.ChunkDelayP99MS/sc.IntervalMS)
		}
		sum := Summarize(fracs)
		gates = append(gates, GateResult{
			Name:   name,
			Value:  fmt.Sprintf("%.3f (%.3f..%.3f) [p99=%.2fms / %.0fms]", sum.Median, sum.Min, sum.Max, sc.Proxy.ChunkDelayP99MS.Median, sc.IntervalMS),
			Budget: fmt.Sprintf("<= %.3f", budget),
			Passed: sum.Median <= budget,
			RawVal: sum.Median,
			RawMax: budget,
		})
	}
	chunkGate("stream-pass chunk delay p99 fraction", "stream-pass", BudgetStreamPassChunkDelayFraction)
	chunkGate("stream-translate chunk delay p99 fraction", "stream-translate", BudgetStreamTranslateChunkDelayFraction)

	// 4. nonstream-pass p50 total ratio.
	ratioGate("nonstream-pass total p50 ratio (proxy/direct)", "nonstream-pass", BudgetNonstreamPassTotalRatio, func(r RunPair) (float64, float64) {
		return r.Proxy.TotalP50MS, r.Direct.TotalP50MS
	})

	// 5. agents p99 total ratio.
	ratioGate("agents total p99 ratio (proxy/direct)", "agents", BudgetAgentsTotalP99Ratio, func(r RunPair) (float64, float64) {
		return r.Proxy.TotalP99MS, r.Direct.TotalP99MS
	})

	// 6. Footprint RSS gates (Linux).
	if rep.Footprint.IdleBefore.Measured {
		idleMB := TotalRSSMB(rep.Footprint.IdleBefore.Processes)
		gates = append(gates, GateResult{
			Name:   "idle RSS (core + extensions)",
			Value:  fmt.Sprintf("%.2f MB", idleMB),
			Budget: fmt.Sprintf("<= %.2f MB", BudgetIdleRSSMB),
			Passed: idleMB <= BudgetIdleRSSMB,
			RawVal: idleMB,
			RawMax: BudgetIdleRSSMB,
		})
		if sc := byName["agents"]; sc != nil && sc.PeakTotalRSSMB.Median > 0 {
			perStreamKB := ((sc.PeakTotalRSSMB.Median - idleMB) * 1024.0) / float64(sc.Concurrency)
			if perStreamKB < 0 {
				perStreamKB = 0
			}
			gates = append(gates, GateResult{
				Name:   "agents RSS per open stream",
				Value:  fmt.Sprintf("%.2f KB/stream (peak=%.2f MB, idle=%.2f MB)", perStreamKB, sc.PeakTotalRSSMB.Median, idleMB),
				Budget: fmt.Sprintf("<= %.2f KB/stream", BudgetAgentsRSSPerStreamKB),
				Passed: perStreamKB <= BudgetAgentsRSSPerStreamKB,
				RawVal: perStreamKB,
				RawMax: BudgetAgentsRSSPerStreamKB,
			})
		} else {
			gates = append(gates, GateResult{
				Name:   "agents RSS per open stream",
				Value:  "not measured",
				Budget: fmt.Sprintf("<= %.2f KB/stream", BudgetAgentsRSSPerStreamKB),
				Passed: false,
				RawMax: BudgetAgentsRSSPerStreamKB,
			})
		}
	} else {
		// Only a platform without /proc may skip the memory gates. On Linux an unsampled
		// footprint (for example -proxy-url without -proxy-pid) is a failure: nothing was
		// measured, so nothing can be said to be within the budget.
		unmeasured := "not measured (non-Linux)"
		passed := true
		if hostOS == "linux" {
			unmeasured = "not measured (no footprint sample; -proxy-pid missing or process unreadable)"
			passed = false
		}
		gates = append(gates, GateResult{
			Name:   "idle RSS (core + extensions)",
			Value:  unmeasured,
			Budget: fmt.Sprintf("<= %.2f MB", BudgetIdleRSSMB),
			Passed: passed,
			RawMax: BudgetIdleRSSMB,
		})
		gates = append(gates, GateResult{
			Name:   "agents RSS per open stream",
			Value:  unmeasured,
			Budget: fmt.Sprintf("<= %.2f KB/stream", BudgetAgentsRSSPerStreamKB),
			Passed: passed,
			RawMax: BudgetAgentsRSSPerStreamKB,
		})
	}

	return gates
}

// FormatGateSummary prints the check-mode gate table and returns the failing gate names.
func FormatGateSummary(gates []GateResult) (string, []string) {
	var b strings.Builder
	var failed []string
	b.WriteString("## Check Gates\n\n")
	b.WriteString("| Gate | Measured | Budget | Status |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, g := range gates {
		status := "PASS"
		if !g.Passed {
			status = "FAIL"
			failed = append(failed, fmt.Sprintf("%s (%s > %s)", g.Name, g.Value, g.Budget))
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", g.Name, g.Value, g.Budget, status)
	}
	b.WriteByte('\n')
	if len(failed) > 0 {
		fmt.Fprintf(&b, "Check FAILED (%d gate(s) over budget):\n", len(failed))
		for _, f := range failed {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	} else {
		b.WriteString("Check PASSED: all gates within budget.\n")
	}
	return b.String(), failed
}
