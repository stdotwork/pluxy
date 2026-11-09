package route

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ModelEntry is one model an account's config lists: Name is the model's name upstream, Alias
// the name clients use for it (default Name). In JSON it is the name as a string, or an object
// {"name": ..., "alias": ...}.
type ModelEntry struct {
	Name  string `json:"name"`
	Alias string `json:"alias,omitempty"`
}

// UnmarshalJSON reads a model name or a {"name", "alias"} object.
func (m *ModelEntry) UnmarshalJSON(b []byte) error {
	if b = bytes.TrimSpace(b); len(b) > 0 && b[0] == '"' {
		*m = ModelEntry{}
		return json.Unmarshal(b, &m.Name)
	}
	type plain ModelEntry
	var p plain
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return fmt.Errorf("a model is a name or {\"name\": ..., \"alias\": ...}: %w", err)
	}
	*m = ModelEntry(p)
	return nil
}

// MarshalJSON writes a model without an alias as its name.
func (m ModelEntry) MarshalJSON() ([]byte, error) {
	if m.Alias == "" || m.Alias == m.Name {
		return json.Marshal(m.Name)
	}
	type plain ModelEntry
	return json.Marshal(plain(m))
}

// public is the name clients use for the model.
func (m ModelEntry) public() string {
	if m.Alias != "" {
		return m.Alias
	}
	return m.Name
}

// ModelAlias renames a model the account serves, as today's oauth-model-alias does: requests for
// Alias go to the model Name. Without Fork the model is served only as Alias; with Fork under both
// names. Name matches regardless of case.
type ModelAlias struct {
	Name  string `json:"name"`
	Alias string `json:"alias"`
	Fork  bool   `json:"fork,omitempty"`
	// DisplayName replaces the model's display name in model lists.
	DisplayName string `json:"display_name,omitempty"`
}

// checkNames checks the account's naming settings.
func (a *Account) checkNames() error {
	for i, m := range a.Models {
		if strings.TrimSpace(m.Name) == "" {
			return fmt.Errorf("models[%d] has no name", i)
		}
	}
	for i, r := range a.ModelAlias {
		if strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.Alias) == "" {
			return fmt.Errorf("model_alias[%d] needs a name and an alias", i)
		}
	}
	if p := a.Prefix; p != "" && (strings.Contains(p, "/") || strings.TrimSpace(p) != p) {
		return errors.New("prefix must not contain \"/\" or surrounding spaces")
	}
	return nil
}

// Name is one name an account serves a model under.
type Name struct {
	// Public is the name clients use, with the account's prefix when it has one.
	Public string
	// Upstream is the model's name upstream: what requests for Public are sent as.
	Upstream string
	// Base is the name the model had before an alias renamed it: Upstream for a model of the
	// extension's list, the config entry's name for one of the account's own.
	Base string
	// DisplayName is a display name an alias gave the model; empty when it gave none.
	DisplayName string
}

// Names returns every name the account serves models under, given the upstream names catalog
// its extension lists for it, as names does.
func (a *Account) Names(catalog []string, forcePrefix bool) []Name {
	return a.names(catalog, forcePrefix)
}

// names returns every name the account serves models under, given the models it serves without
// renaming: its config's Models, else catalog (the upstream names its extension lists for it).
// The rules apply in today's core's order: exclusions on those names, then aliases, then the
// prefix. forcePrefix is routing's force-model-prefix: a prefixed account then serves its models
// only with the prefix, except one whose name is the prefix itself.
func (a *Account) names(catalog []string, forcePrefix bool) []Name {
	return a.prefixed(a.baseNames(catalog), forcePrefix)
}

// baseNames is names without the prefix.
func (a *Account) baseNames(catalog []string) []Name {
	var base []Name
	if len(a.Models) > 0 {
		for _, m := range a.Models {
			base = append(base, Name{Public: m.public(), Upstream: m.Name, Base: m.Name})
		}
	} else {
		for _, id := range catalog {
			base = append(base, Name{Public: id, Upstream: id, Base: id})
		}
	}

	var out []Name
	seen := map[string]bool{}
	add := func(n Name) {
		if !seen[n.Public] {
			seen[n.Public] = true
			out = append(out, n)
		}
	}
	for _, n := range base {
		if a.excluded(n.Public) {
			continue
		}
		rules := a.aliasesOf(n.Public)
		keep := len(rules) == 0
		for _, r := range rules {
			keep = keep || r.Fork
		}
		if keep {
			add(n)
		}
		for _, r := range rules {
			add(Name{Public: r.Alias, Upstream: n.Upstream, Base: n.Public, DisplayName: r.DisplayName})
		}
	}
	return out
}

// prefixed applies the account's prefix to names.
func (a *Account) prefixed(names []Name, forcePrefix bool) []Name {
	if a.Prefix == "" {
		return names
	}
	var out []Name
	for _, n := range names {
		if !forcePrefix || n.Public == a.Prefix {
			out = append(out, n)
		}
		p := n
		p.Public = a.Prefix + "/" + n.Public
		out = append(out, p)
	}
	return out
}

// resolve returns the upstream name of model on the account, and whether the account serves it.
// An account whose config lists Models serves only those (as renamed); one that lists none
// serves the models of catalog, the upstream names its extension lists for it (as renamed);
// with no catalog either, any model its rules leave it: not excluded, not renamed away by an
// alias without Fork.
func (a *Account) resolve(model string, forcePrefix bool, catalog []string) (string, bool) {
	for _, name := range a.unprefixed(model, forcePrefix) {
		if len(a.Models) > 0 || len(catalog) > 0 {
			for _, n := range a.baseNames(catalog) {
				if n.Public == name {
					return n.Upstream, true
				}
			}
			continue
		}
		if r, ok := a.aliasNamed(name); ok {
			if !a.excluded(r.Name) {
				return r.Name, true
			}
			continue
		}
		hidden := false
		for _, r := range a.aliasesOf(name) {
			hidden = hidden || !r.Fork
		}
		if !hidden && !a.excluded(name) {
			return name, true
		}
	}
	return "", false
}

// unprefixed returns the names model may stand for on the account once its prefix is taken off.
func (a *Account) unprefixed(model string, forcePrefix bool) []string {
	if a.Prefix == "" {
		return []string{model}
	}
	if rest, ok := strings.CutPrefix(model, a.Prefix+"/"); ok && rest != "" {
		return []string{rest}
	}
	if !forcePrefix || model == a.Prefix {
		return []string{model}
	}
	return nil
}

// aliasesOf returns the alias rules that rename the model called name (any case).
func (a *Account) aliasesOf(name string) []ModelAlias {
	var out []ModelAlias
	for _, r := range a.ModelAlias {
		if strings.EqualFold(r.Name, name) && !strings.EqualFold(r.Alias, name) {
			out = append(out, r)
		}
	}
	return out
}

// aliasNamed returns the alias rule whose alias is name.
func (a *Account) aliasNamed(name string) (ModelAlias, bool) {
	for _, r := range a.ModelAlias {
		if r.Alias == name && !strings.EqualFold(r.Name, r.Alias) {
			return r, true
		}
	}
	return ModelAlias{}, false
}

// excluded reports whether one of the account's ExcludedModels patterns matches name. A pattern
// is a name, or has "*" standing for any text (prefix-*, *-suffix, *part*); case does not count.
func (a *Account) excluded(name string) bool {
	name = strings.ToLower(name)
	for _, p := range a.ExcludedModels {
		if matchPattern(strings.ToLower(strings.TrimSpace(p)), name) {
			return true
		}
	}
	return false
}

func matchPattern(pattern, s string) bool {
	if pattern == "" {
		return false
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	first, last := parts[0], parts[len(parts)-1]
	if !strings.HasPrefix(s, first) {
		return false
	}
	s = s[len(first):]
	if !strings.HasSuffix(s, last) {
		return false
	}
	s = s[:len(s)-len(last)]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	return true
}
