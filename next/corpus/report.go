package corpus

import (
	"fmt"
	"sort"
	"strings"
)

// Result is the outcome of one case.
type Result struct {
	Case    *Case
	Verdict Verdict
	// Err is set when the case could not run, or when the live old core no longer matches the
	// recording.
	Err error
}

// Status is a short label for the result.
func (r Result) Status() string {
	switch {
	case r.Err != nil:
		return "ERROR"
	case !r.Verdict.OK():
		return "MISMATCH"
	case r.caseExceptions() > 0:
		return "match with exceptions"
	case len(r.Verdict.Covered) > 0:
		return "match, global only"
	default:
		return "match"
	}
}

// caseExceptions counts the case's own exceptions that covered a difference.
func (r Result) caseExceptions() int {
	n := 0
	for e := range r.Verdict.Covered {
		if !r.isGlobal(e) {
			n++
		}
	}
	return n
}

func (r Result) isGlobal(e *Exception) bool {
	for i := range r.Case.Exceptions {
		if &r.Case.Exceptions[i] == e {
			return false
		}
	}
	return true
}

// Report renders results as a plain-text report. reference says what the new core was
// compared with.
func Report(results []Result, reference string, global []Exception, staleGlobal []*Exception) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Corpus report: %d cases; new core compared with %s.\n", len(results), reference)
	counts := map[string]int{}
	kinds := map[string]int{}
	if len(global) > 0 {
		b.WriteString("\n## Global exceptions (cases/corpus.json)\n\n")
		for i := range global {
			e := &global[i]
			n := 0
			for _, r := range results {
				if len(r.Verdict.Covered[e]) > 0 {
					n++
				}
			}
			if n > 0 {
				kinds[e.Kind]++
			}
			fmt.Fprintf(&b, "(%s) %s, in %d cases: %s\n", e.Kind, e.Path, n, e.Reason)
		}
	}
	group := ""
	for _, r := range results {
		g := r.Case.Format + " / " + r.Case.Provider
		if g != group {
			group = g
			fmt.Fprintf(&b, "\n## %s\n\n", g)
		}
		st := r.Status()
		counts[st]++
		fmt.Fprintf(&b, "%-22s %s\n", st, r.Case.Name)
		if r.Err != nil {
			fmt.Fprintf(&b, "    %s\n", indent(r.Err.Error(), "    "))
		}
		for _, ce := range sortedCovered(r.Verdict.Covered) {
			if r.isGlobal(ce.ex) {
				continue
			}
			kinds[ce.ex.Kind]++
			fmt.Fprintf(&b, "    exception (%s) %s: %s\n", ce.ex.Kind, ce.ex.Path, ce.ex.Reason)
		}
		for _, d := range r.Verdict.Uncovered {
			fmt.Fprintf(&b, "    DIFF %s\n        old: %s\n        new: %s\n", d.Path, Show(d.Want), Show(d.Got))
		}
		for _, e := range r.Verdict.Stale {
			fmt.Fprintf(&b, "    STALE exception %s covers no difference; remove it\n", e.Path)
		}
		for _, w := range r.Verdict.WantFailed {
			wanted := "no value"
			if !w.Exception.Absent {
				wanted = showRaw(w.Exception.Want)
			}
			fmt.Fprintf(&b, "    WANT exception %s wants %s at %s, new core gave %s\n", w.Exception.Path, wanted, w.Path, Show(w.Got))
		}
	}
	for _, e := range staleGlobal {
		fmt.Fprintf(&b, "\nSTALE global exception %s covers no difference in any case; remove it\n", e.Path)
	}
	fmt.Fprintf(&b, "\nSummary: %d match, %d match with global exceptions only, %d match with case exceptions, %d mismatch, %d error.\n",
		counts["match"], counts["match, global only"], counts["match with exceptions"], counts["MISMATCH"], counts["ERROR"])
	fmt.Fprintf(&b, "Exceptions that covered a difference: %d intended, %d gap (each global exception counted once).\n", kinds[KindIntended], kinds[KindGap])
	return b.String()
}

type coveredEntry struct {
	ex    *Exception
	diffs []Difference
}

func sortedCovered(m map[*Exception][]Difference) []coveredEntry {
	out := make([]coveredEntry, 0, len(m))
	for e, d := range m {
		out = append(out, coveredEntry{e, d})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ex.Path < out[j].ex.Path })
	return out
}

func indent(s, prefix string) string {
	return strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+prefix)
}

func showRaw(b []byte) string {
	v, err := decodeValue(b)
	if err != nil {
		return string(b)
	}
	return Show(v)
}
