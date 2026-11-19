package route

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Fallback chains (routing.chains). A chain is a flat list of steps under the model name a
// client asks for. A request for the name is served by the first step's accounts while one of
// them can serve it now, and by the next step's only once none can: every account of the step is
// blocked, busy or has refused the request in a way that is safe to try elsewhere. Steps are
// tiers above priority, so moving on needs no wait, and a request that no step can serve now
// waits, as any request does, for the first account of any step to come back. A request never
// moves after its reply has started, nor for an error about the request itself, since the
// router's rules for both hold across steps. A step names a model directly, never another chain,
// so a chain cannot loop.
//
// With no chain, a name is served by the accounts of one provider: the provider of the
// highest-priority account that serves it, the first in the config's order on a tie. Two
// providers that serve one name do not share its requests unless a chain says so.

// Step is one step of a fallback chain: a model, and optionally the one provider (an
// extension's ID, such as pluxy/claude) and the accounts (their IDs) that serve it. A step with
// neither is served as a request for its model with no chain is: by the accounts of one
// provider.
type Step struct {
	Model    string   `json:"model"`
	Provider string   `json:"provider,omitempty"`
	Accounts []string `json:"accounts,omitempty"`
}

// String is the step as the pluxy command writes it: model[@provider][:account,account].
func (s Step) String() string {
	out := s.Model
	if s.Provider != "" {
		out += "@" + s.Provider
	}
	if len(s.Accounts) > 0 {
		out += ":" + strings.Join(s.Accounts, ",")
	}
	return out
}

// MaxChainSteps is the most steps a chain has.
const MaxChainSteps = 8

// ValidateChain checks one chain, among chains (the config's, with this one in it): its name,
// its number of steps, and that each step names a model and not another chain. A step may name
// the chain's own name: that is the model of the name.
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
	}
	return nil
}

func validateChains(chains map[string][]Step) error {
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

// target is an account a request may use for one of its steps.
type target struct {
	a     *Account
	model string // the account's name for the step's model
	step  int    // the step's index; 0 for a request whose model has no chain
}

// targetKey names a target in a request: the same account may serve two steps.
type targetKey struct {
	step int
	id   string
}

func (t target) key() targetKey { return targetKey{t.step, t.a.ID} }

// stepNumber is t's step as attempts and replies count it: from 1 in a chain, 0 without one.
func (c *call) stepNumber(t target) int {
	if !c.chain {
		return 0
	}
	return t.step + 1
}

// stepsFor returns the steps of a request for model: its chain's, or one step naming it.
func (r *Router) stepsFor(model string) ([]Step, bool) {
	if steps, ok := r.cfg.Chains[model]; ok {
		return steps, true
	}
	return []Step{{Model: model}}, false
}

// targets returns the accounts of accounts that serve each of steps, in step order and, within a
// step, in the accounts' order. served is the model lists of accounts without models of their own.
func (r *Router) targets(steps []Step, accounts []*Account, served map[string][]string) []target {
	var out []target
	for i, s := range steps {
		out = append(out, r.stepTargets(s, i, accounts, served)...)
	}
	return out
}

// stepTargets returns the accounts that serve step s, the step at index i. With no provider and
// no accounts named, they are one provider's: that of the highest-priority account that serves
// the step's model, the first in order on a tie. An account that serves the model because its
// model list is not known (it serves any model) decides only when no account lists the model:
// otherwise one whose list could not be read would take every name from the providers that
// list it.
func (r *Router) stepTargets(s Step, i int, accounts []*Account, served map[string][]string) []target {
	var out []target
	listed := func(t target) bool { return len(t.a.Models) > 0 || served[t.a.ID] != nil }
	for _, a := range accounts {
		if s.Provider != "" && a.Extension != s.Provider || len(s.Accounts) > 0 && !slices.Contains(s.Accounts, a.ID) {
			continue
		}
		// Blocks on a model are kept under its upstream name, which is the account's own.
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

// rotationKey is the strategy's memory for a request for model at step: the model's own with no
// chain, and the chain's step's with one, so a chain's turns do not move its model's.
func rotationKey(model string, chain bool, step int) string {
	if !chain {
		return model
	}
	return model + "\x00" + strconv.Itoa(step)
}

// CheckSteps reports the first of steps that no account the router routes over serves, as a
// management call that sets a chain refuses it. An account whose model list is not known serves
// any model, as for a request.
func (r *Router) CheckSteps(ctx context.Context, steps []Step) error {
	accounts, creds := r.snapshot()
	served := r.served(ctx, accounts, creds, r.catalogFunc())
	for i, s := range steps {
		if len(r.stepTargets(s, i, accounts, served)) == 0 {
			return fmt.Errorf("step %d, %s: no account serves it", i+1, s)
		}
	}
	return nil
}
