package route

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type Step struct {
	Model    string   `json:"model"`
	Provider string   `json:"provider,omitempty"`
	Accounts []string `json:"accounts,omitempty"`
}

func (s Step) String() string {
	out := s.Model
	if s.Provider == "" && needsEscape(s) {
		out += "@"
	} else if s.Provider != "" {
		out += "@" + s.Provider
	}
	if len(s.Accounts) > 0 {
		out += ":" + strings.Join(s.Accounts, ",")
	}
	return out
}

var providerID = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9.-]*$`)

func needsEscape(s Step) bool {
	text := s.Model
	if len(s.Accounts) > 0 {
		text += ":" + strings.Join(s.Accounts, ",")
	}
	if i := strings.LastIndexByte(text, '@'); i >= 0 {
		if p, _, _ := strings.Cut(text[i+1:], ":"); p == "" || providerID.MatchString(p) {
			return true
		}
	}
	return len(s.Accounts) == 0 && strings.Contains(s.Model, ":")
}

const MaxChainSteps = 8

func ValidateChain(name string, steps []Step, chains map[string][]Step) error {
	switch {
	case strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name:
		return fmt.Errorf("routing.chains: a chain's name must be a model name, not empty and without spaces around it (%q)", name)
	case len(steps) == 0:
		return fmt.Errorf("routing.chains.%s: a chain needs at least one step", name)
	case len(steps) > MaxChainSteps:
		return fmt.Errorf("routing.chains.%s has %d steps; a chain has at most %d", name, len(steps), MaxChainSteps)
	}
	for i, s := range steps {
		if strings.TrimSpace(s.Model) == "" {
			return fmt.Errorf("routing.chains.%s: step %d names no model", name, i+1)
		}
		if _, ok := chains[s.Model]; ok && s.Model != name {
			return fmt.Errorf("routing.chains.%s: step %d names the chain %q; a step names a model, never another chain", name, i+1, s.Model)
		}
		if slices.Contains(s.Accounts, "") {
			return fmt.Errorf("routing.chains.%s: step %d names an empty account ID", name, i+1)
		}
		if j := slices.IndexFunc(steps[:i], func(o Step) bool { return sameStep(o, s) }); j >= 0 {
			return fmt.Errorf("routing.chains.%s: step %d repeats step %d", name, i+1, j+1)
		}
	}
	return nil
}

func sameStep(a, b Step) bool {
	return a.Model == b.Model && a.Provider == b.Provider && slices.Equal(slices.Sorted(slices.Values(a.Accounts)), slices.Sorted(slices.Values(b.Accounts)))
}

func ValidateChains(chains map[string][]Step) error {
	names := make([]string, 0, len(chains))
	for n := range chains {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if err := ValidateChain(n, chains[n], chains); err != nil {
			return err
		}
	}
	return nil
}

type target struct {
	a     *Account
	model string
	step  int
}

type targetKey struct {
	id, model string
}

func (t target) key() targetKey { return targetKey{t.a.ID, t.model} }

func (c *call) stepNumber(t target) int {
	if !c.chain {
		return 0
	}
	return t.step + 1
}

func (r *Router) stepsFor(model string) ([]Step, bool) {
	if steps, ok := r.cfg.Chains[model]; ok {
		return steps, true
	}
	return []Step{{Model: model}}, false
}

func (r *Router) targets(steps []Step, accounts []*Account, served map[string][]string) []target {
	var out []target
	for i, s := range steps {
		out = append(out, r.stepTargets(s, i, accounts, served)...)
	}
	return out
}

func (r *Router) stepTargets(s Step, i int, accounts []*Account, served map[string][]string) []target {
	var out []target
	listed := func(t target) bool { return len(t.a.Models) > 0 || served[t.a.ID] != nil }
	for _, a := range accounts {
		if s.Provider != "" && a.Extension != s.Provider || len(s.Accounts) > 0 && !slices.Contains(s.Accounts, a.ID) {
			continue
		}
		if up, ok := a.resolve(s.Model, r.cfg.ForceModelPrefix, served[a.ID]); ok {
			out = append(out, target{a: a, model: up, step: i})
		}
	}
	if s.Provider == "" && len(s.Accounts) == 0 && len(out) > 1 {
		cands := out
		if slices.ContainsFunc(out, listed) {
			cands = slices.DeleteFunc(slices.Clone(out), func(t target) bool { return !listed(t) })
		}
		first := cands[0]
		for _, t := range cands[1:] {
			if t.a.Priority > first.a.Priority {
				first = t
			}
		}
		out = slices.DeleteFunc(out, func(t target) bool { return t.a.Extension != first.a.Extension })
	}
	return out
}

func rotationKey(model string, chain bool, step int) string {
	if !chain {
		return model
	}
	return model + "\x00" + strconv.Itoa(step)
}

func (r *Router) CheckSteps(ctx context.Context, steps []Step) error {
	known, creds := r.knownSnapshot()
	accounts := r.routed(known)
	served := r.served(ctx, accounts, creds, r.catalogFunc())
	for i, s := range steps {
		targets := r.stepTargets(s, i, accounts, served)
		for _, id := range s.Accounts {
			switch {
			case !slices.ContainsFunc(known, func(a *Account) bool { return a.ID == id }):
				return fmt.Errorf("step %d, %s: no account %q", i+1, s, id)
			case !slices.ContainsFunc(targets, func(t target) bool { return t.a.ID == id }):
				return fmt.Errorf("step %d, %s: account %q does not serve it", i+1, s, id)
			}
		}
		if serving, _ := split(targets); len(serving) == 0 {
			return fmt.Errorf("step %d, %s: no account serves it", i+1, s)
		}
	}
	return nil
}
