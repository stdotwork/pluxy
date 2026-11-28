package legacy

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

const (
	Brought = iota
	Changed
	NotBrought
)

type Row struct {
	What     string `json:"what"`
	Name     string `json:"name,omitempty"`
	As       string `json:"as,omitempty"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Why      string `json:"why,omitempty"`
	KeepWith string `json:"keep_with,omitempty"`
}

type Lists struct {
	Brought, Changed, NotBrought []Row
}

type Entry struct {
	List int
	Item string
	Row  Row
}

type Ledger struct {
	items   []string
	entries []Entry
}

func ConfigItem(path string) string { return "config.yaml: " + path }

func AuthItem(name string) string { return "auth folder: " + name }

func (l *Ledger) Expect(item string) { l.items = append(l.items, item) }

func (l *Ledger) Add(list int, item string, row Row) {
	l.entries = append(l.entries, Entry{List: list, Item: item, Row: row})
}

func (l *Ledger) Has(item string) bool {
	return slices.ContainsFunc(l.entries, func(e Entry) bool { return e.Item == item })
}

func (l *Ledger) Update(item string, f func(list *int, row *Row)) {
	for i := range l.entries {
		if l.entries[i].Item == item {
			f(&l.entries[i].List, &l.entries[i].Row)
		}
	}
}

func (l *Ledger) Merge(o *Ledger) {
	l.items = append(l.items, o.items...)
	l.entries = append(l.entries, o.entries...)
}

func (l *Ledger) Check() error {
	count := map[string]int{}
	for _, e := range l.entries {
		if e.List < Brought || e.List > NotBrought {
			return fmt.Errorf("the import report put %s in no list", cmp.Or(e.Item, e.Row.What))
		}
		count[e.Item]++
	}
	for _, it := range l.items {
		switch count[it] {
		case 1:
		case 0:
			return fmt.Errorf("the import report does not account for %s: nothing was imported", it)
		default:
			return fmt.Errorf("the import report names %s %d times: nothing was imported", it, count[it])
		}
	}
	return nil
}

func (l *Ledger) Report() (Lists, error) {
	r := Lists{Brought: []Row{}, Changed: []Row{}, NotBrought: []Row{}}
	if err := l.Check(); err != nil {
		return r, err
	}
	for _, e := range l.entries {
		switch e.List {
		case Brought:
			r.Brought = append(r.Brought, e.Row)
		case Changed:
			r.Changed = append(r.Changed, e.Row)
		default:
			r.NotBrought = append(r.NotBrought, e.Row)
		}
	}
	return r, nil
}

func unit(path string) bool {
	return path == "requests.payload" || path == "payload" || slices.Contains(newKeys, path)
}

func section(path string) bool {
	for _, f := range families {
		if path == "api-keys."+f.v8 || path == f.old {
			return true
		}
	}
	return false
}

func leaves(path string, v any, f func(string)) {
	if unit(path) {
		f(path)
		return
	}
	switch v := v.(type) {
	case tree:
		if len(v) == 0 && path != "" {
			f(path)
			return
		}
		for _, k := range slices.Sorted(maps.Keys(v)) {
			p := k
			if path != "" {
				p = path + "." + k
			}
			leaves(p, v[k], f)
		}
	case []any:
		if !section(path) || len(v) == 0 {
			f(path)
			return
		}
		for i := range v {
			f(path + "[" + strconv.Itoa(i) + "]")
		}
	default:
		f(path)
	}
}

func trail(leaf, path string) string { return strings.TrimPrefix(leaf, path) }
