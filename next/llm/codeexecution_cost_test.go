package llm

import (
	"math/rand"
	"slices"
	"strings"
	"testing"
)

func referenceAppend(runs []CodeExecution, run CodeExecution) []CodeExecution {
	leftOut := false
	if n := len(runs); n > 0 && runs[n-1] == codeExecutionsLeftOut {
		runs, leftOut = runs[:n-1], true
	}
	size := codeExecutionSize(run)
	for _, r := range runs {
		size += codeExecutionSize(r)
	}
	if len(run.Code) > MaxCodeExecutionCodeBytes || size > MaxCodeExecutionBytes {
		leftOut = true
	} else {
		runs = append(runs, run)
	}
	if leftOut {
		runs = append(runs, codeExecutionsLeftOut)
	}
	return runs
}

func TestCodeRunsWorkGrowsWithTheRunsAdded(t *testing.T) {
	const n = 50000
	codeRunSteps.Store(0)
	var runs CodeRuns
	for range n {
		runs.Append(CodeExecution{Code: "x", Language: "python"})
	}
	if got := runs.List(); len(got) != n {
		t.Fatalf("runs = %d, want %d", len(got), n)
	}
	if steps := codeRunSteps.Load(); steps > 2*n {
		t.Errorf("%d appends took %d steps", n, steps)
	}
}

func TestCodeRunsMatchTheRescanOnRandomRuns(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for round := range 40 {
		var ref []CodeExecution
		var runs CodeRuns
		for range 400 {
			run := CodeExecution{Code: strings.Repeat("c", rng.Intn(3000)), Output: strings.Repeat("o", rng.Intn(20000))}
			switch rng.Intn(20) {
			case 0:
				run.Code = strings.Repeat("c", MaxCodeExecutionCodeBytes+rng.Intn(2))
			case 1:
				run.Output = strings.Repeat("o", MaxCodeExecutionBytes/2)
			}
			if rng.Intn(15) == 0 {
				ref = append(ref[:len(ref):len(ref)], codeExecutionsLeftOut)
				if n := len(ref); n > 1 && ref[n-2] == codeExecutionsLeftOut {
					ref = ref[:n-1]
				}
				runs.LeaveOut()
			}
			ref = referenceAppend(ref, run)
			runs.Append(run)
			if got := runs.List(); !slices.Equal(got, ref) {
				t.Fatalf("round %d: %d runs, want %d", round, len(got), len(ref))
			}
		}
	}
}
