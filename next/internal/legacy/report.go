package legacy

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/management"
)

// The lists of an import report (management.ImportReport).
const (
	Brought = iota
	Changed
	NotBrought
)

// Row is one row of an import report.
type Row = management.ImportRow

// Entry is a row of a report, the list it is in and the item it accounts for: a key of the
// config ("config.yaml: server.host") or a file of the auth folder ("auth folder: a.json").
// A row with no item says something about the import that is not a key of the file, such as
// the server.host it does not set.
type Entry struct {
	List int
	Item string
	Row  Row
}

// Ledger is an import report being built: the items it must account for and its rows. Report
// refuses a ledger in which an item has no row or more than one, so nothing is dropped
// without a word.
type Ledger struct {
	items   []string
	entries []Entry
}

// ConfigItem names the key of a config.yaml at path (dotted, a list entry as [i]) as an item.
func ConfigItem(path string) string { return "config.yaml: " + path }

// AuthItem names a file of the auth folder as an item.
func AuthItem(name string) string { return "auth folder: " + name }

// Expect adds an item the report must account for.
func (l *Ledger) Expect(item string) { l.items = append(l.items, item) }

// Add adds a row to list for item (empty: a row about no item).
func (l *Ledger) Add(list int, item string, row Row) {
	l.entries = append(l.entries, Entry{List: list, Item: item, Row: row})
}

// Has reports whether item has a row.
func (l *Ledger) Has(item string) bool {
	return slices.ContainsFunc(l.entries, func(e Entry) bool { return e.Item == item })
}

// Update changes item's rows with f, which gets each row's list and row to change.
func (l *Ledger) Update(item string, f func(list *int, row *Row)) {
	for i := range l.entries {
		if l.entries[i].Item == item {
			f(&l.entries[i].List, &l.entries[i].Row)
		}
	}
}

// Merge adds o's items and rows to l.
func (l *Ledger) Merge(o *Ledger) {
	l.items = append(l.items, o.items...)
	l.entries = append(l.entries, o.entries...)
}

// Check fails, naming the item, when an item has no row or more than one, or a row is in no
// list.
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

// Report returns the ledger as a report, after Check.
func (l *Ledger) Report(dryRun bool) (management.ImportReport, error) {
	r := management.ImportReport{DryRun: dryRun, Brought: []Row{}, Changed: []Row{}, NotBrought: []Row{}, Needs: []management.ImportNeed{}}
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

// unit reports whether the key at path is one item of the report as a whole: the payload rules,
// whose rules go across together, and this core's own settings.
func unit(path string) bool {
	return path == "requests.payload" || path == "payload" || slices.Contains(newKeys, path)
}

// section reports whether the list at path is a provider section, whose entries are each one
// item (an entry is a group of keys, which become accounts together).
func section(path string) bool {
	for _, f := range families {
		if path == "api-keys."+f.v8 || path == f.old {
			return true
		}
	}
	return false
}

// leaves calls f with the path of each item of the value v at path: a key whose value is
// text, a number, true or false, a list or an empty mapping is one item, as is a unit; a
// mapping is the items below it, and a provider section each of its entries.
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

// trail is a path below a value: the part of a leaf's path after the value's own.
func trail(leaf, path string) string { return strings.TrimPrefix(leaf, path) }
