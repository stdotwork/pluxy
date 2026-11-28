package route

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type ModelEntry struct {
	Name  string `json:"name"`
	Alias string `json:"alias,omitempty"`
}

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

func (m ModelEntry) MarshalJSON() ([]byte, error) {
	if m.Alias == "" || m.Alias == m.Name {
		return json.Marshal(m.Name)
	}
	type plain ModelEntry
	return json.Marshal(plain(m))
}

func (m ModelEntry) public() string {
	if m.Alias != "" {
		return m.Alias
	}
	return m.Name
}

type ModelAlias struct {
	Name        string `json:"name"`
	Alias       string `json:"alias"`
	Fork        bool   `json:"fork,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

func (a *Account) CheckNames() error {
	if err := a.checkNames(); err != nil {
		return fmt.Errorf("account %q: %w", a.ID, err)
	}
	return nil
}

func (a *Account) checkNames() error {
	first := map[string]int{}
	for i, m := range a.Models {
		if strings.TrimSpace(m.Name) == "" {
			return fmt.Errorf("models[%d] has no name", i)
		}
		if j, ok := first[m.public()]; !ok {
			first[m.public()] = i
		} else if a.Models[j].Name != m.Name {
			return fmt.Errorf("models[%d]: the alias %q is already used by models[%d] (%q); an account serves one model under a name, and models do not pool yet", i, m.public(), j, a.Models[j].Name)
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

type Name struct {
	Public      string
	Upstream    string
	Base        string
	DisplayName string
	NonCodex    bool
}

func (a *Account) Names(catalog []string, forcePrefix bool) []Name {
	return a.names(catalog, forcePrefix)
}

func (a *Account) names(catalog []string, forcePrefix bool) []Name {
	return a.prefixed(a.baseNames(catalog), forcePrefix)
}

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

func (a *Account) aliasesOf(name string) []ModelAlias {
	var out []ModelAlias
	for _, r := range a.ModelAlias {
		if strings.EqualFold(r.Name, name) && !strings.EqualFold(r.Alias, name) {
			out = append(out, r)
		}
	}
	return out
}

func (a *Account) aliasNamed(name string) (ModelAlias, bool) {
	for _, r := range a.ModelAlias {
		if r.Alias == name && !strings.EqualFold(r.Name, r.Alias) {
			return r, true
		}
	}
	return ModelAlias{}, false
}

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
