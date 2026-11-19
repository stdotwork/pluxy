package main

import (
	"fmt"
	"math"
	"strings"
)

// Check-mode budgets. Each was set from the baseline on main at 35ad0d646 (docs/perf.md): the
// median of three check runs on GitHub's ubuntu-latest runner (4 vCPU) and of five full runs on
// an 8 vCPU Linux machine. Time budgets are ratios to the direct path in the same run, so the
// mock's timing and the client's own costs cancel out; machine speed does not, so each leaves
// room for two to three times the overhead measured on CI. Lower a budget when a fix lowers
// its measured value.
const (
	// BudgetStreamPassTTFTRatio bounds proxy p50 TTFT / direct p50 TTFT in stream-pass (mock
	// TTFT 100 ms). Baseline 1.08 (CI) and 1.13 (8 vCPU): pluxy adds 8 to 13 ms at a 100 KB body.
	BudgetStreamPassTTFTRatio = 1.25

	// BudgetStreamTranslateTTFTRatio bounds the same ratio in stream-translate (Messages to
	// Chat Completions). Baseline 1.11 (CI) and 1.16 (8 vCPU).
	BudgetStreamTranslateTTFTRatio = 1.30

	// BudgetStreamPassChunkDelayFraction bounds proxy p99 chunk delay / the mock's chunk
	// interval (10 ms) in stream-pass. Baseline 0.046 (CI) and 0.053 (8 vCPU): about 0.5 ms.
	BudgetStreamPassChunkDelayFraction = 0.2

	// BudgetStreamTranslateChunkDelayFraction is the same bound in stream-translate. Baseline
	// 0.047 (CI) and 0.055 (8 vCPU).
	BudgetStreamTranslateChunkDelayFraction = 0.2

	// BudgetNonstreamPassTotalRatio bounds proxy p50 total / direct p50 total in
	// nonstream-pass (mock latency 100 ms). Baseline 1.08 (CI) and 1.13 (8 vCPU).
	BudgetNonstreamPassTotalRatio = 1.25

	// BudgetAgentsTotalP99Ratio bounds proxy p99 total / direct p99 total with 80 streams open
	// at once (about 3.3 s each). Baseline 1.09 (CI, 4 vCPU shared by both sides) and 1.03
	// (8 vCPU).
	BudgetAgentsTotalP99Ratio = 1.20

	// BudgetIdleRSSMB bounds the resident memory (MB) of core and its extension processes,
	// started and idle, before any request. Baseline 44 (CI) and 42 (8 vCPU).
	BudgetIdleRSSMB = 100.0

	// BudgetAgentsRSSPerStreamKB bounds (peak RSS under agents - idle RSS) / 80 streams, in KB,
	// at a 100 KB request body. Baseline 1016 (CI) and about 1,500 (8 vCPU, where the Go
	// runtime runs more threads).
	BudgetAgentsRSSPerStreamKB = 3072.0
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
		gates = append(gates, GateResult{
			Name:   "idle RSS (core + extensions)",
			Value:  "not measured (non-Linux)",
			Budget: fmt.Sprintf("<= %.2f MB", BudgetIdleRSSMB),
			Passed: true,
		})
		gates = append(gates, GateResult{
			Name:   "agents RSS per open stream",
			Value:  "not measured (non-Linux)",
			Budget: fmt.Sprintf("<= %.2f KB/stream", BudgetAgentsRSSPerStreamKB),
			Passed: true,
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
