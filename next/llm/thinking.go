package llm

import (
	"strconv"
	"strings"
)

// ThinkingLevels are the levels a thinking suffix may name, lowest first; "none" turns thinking
// off and "auto" leaves its amount to the model.
var ThinkingLevels = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// SplitThinkingSuffix splits a model name that ends in a thinking suffix into the model and the
// suffix: a level, none or auto (any case and spacing, written in lower case), or a token budget (a whole
// number of 0 or more; -1 is auto). A name without such a suffix is returned unchanged with an
// empty suffix.
func SplitThinkingSuffix(name string) (model, suffix string) {
	i := strings.LastIndexByte(name, '(')
	if i <= 0 || !strings.HasSuffix(name, ")") {
		return name, ""
	}
	s := strings.ToLower(strings.TrimSpace(name[i+1 : len(name)-1]))
	switch {
	case s == "none" || s == "auto" || isLevel(s):
	case s == "-1":
		s = "auto"
	default:
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return name, ""
		}
		s = strconv.Itoa(n)
	}
	return name[:i], s
}

func isLevel(s string) bool {
	for _, l := range ThinkingLevels {
		if s == l {
			return true
		}
	}
	return false
}

func ThinkingBudget(suffix string) (int, bool) {
	n, err := strconv.Atoi(suffix)
	return n, err == nil
}

// ThinkingEffort returns the reasoning effort a thinking suffix comes to: the level, none or auto
// it names, or for a token budget the level the budget falls in: 0 is none, up to 512 minimal,
// 1024 low, 8192 medium, 24576 high, above that xhigh.
func ThinkingEffort(suffix string) string {
	n, ok := ThinkingBudget(suffix)
	if !ok {
		return suffix
	}
	switch {
	case n == 0:
		return "none"
	case n <= 512:
		return "minimal"
	case n <= 1024:
		return "low"
	case n <= 8192:
		return "medium"
	case n <= 24576:
		return "high"
	}
	return "xhigh"
}
