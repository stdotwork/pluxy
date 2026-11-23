package geminigenerate

import "sort"

// onScan, when set, is told how much text the masking code walks: the bytes the JSON scanners
// pass over and the steps of the name matcher. Tests set it to hold the work to a bound that does
// not depend on the clock; it is nil otherwise.
var onScan func(n int)

func scanned(n int) {
	if onScan != nil {
		onScan(n)
	}
}

// nameMatcher finds the names of a list in a string in one pass (Aho-Corasick), so the cost of
// masking a string is its own length and the names it holds, not the number of names known. The
// list is ordered by rank: the rank of a name is its place in it, and a lower rank wins where two
// matches overlap.
type nameMatcher struct {
	list  []string
	first []int32 // first child of a node, 0 for none (the root is node 0 and never a child)
	next  []int32 // next sibling
	char  []byte  // the byte on the edge into the node
	fail  []int32 // longest proper suffix of the node's text that is also a node
	out   []int32 // rank of the name that ends at the node, -1 for none
	link  []int32 // nearest node on the fail chain that ends a name, 0 for none
}

func newNameMatcher(list []string) *nameMatcher {
	m := &nameMatcher{list: list}
	m.addNode(0)
	for rank, id := range list {
		u := int32(0)
		for i := 0; i < len(id); i++ {
			v := m.child(u, id[i])
			if v == 0 {
				v = m.addNode(id[i])
				m.next[v] = m.first[u]
				m.first[u] = v
			}
			u = v
		}
		m.out[u] = int32(rank)
	}
	queue := make([]int32, 0, len(m.char))
	for v := m.first[0]; v != 0; v = m.next[v] {
		queue = append(queue, v)
	}
	for q := 0; q < len(queue); q++ {
		u := queue[q]
		for v := m.first[u]; v != 0; v = m.next[v] {
			c := m.char[v]
			f := m.fail[u]
			for f != 0 && m.child(f, c) == 0 {
				f = m.fail[f]
			}
			g := m.child(f, c)
			m.fail[v] = g
			if m.out[g] >= 0 {
				m.link[v] = g
			} else {
				m.link[v] = m.link[g]
			}
			queue = append(queue, v)
		}
	}
	return m
}

func (m *nameMatcher) addNode(c byte) int32 {
	m.first = append(m.first, 0)
	m.next = append(m.next, 0)
	m.char = append(m.char, c)
	m.fail = append(m.fail, 0)
	m.out = append(m.out, -1)
	m.link = append(m.link, 0)
	return int32(len(m.char) - 1)
}

func (m *nameMatcher) child(u int32, c byte) int32 {
	for v := m.first[u]; v != 0; v = m.next[v] {
		if m.char[v] == c {
			return v
		}
	}
	return 0
}

type nameMatch struct {
	rank       int32
	start, end int
}

// mask replaces each name in s the way replacing the names one at a time in rank order would:
// a name stands alone (see standalone) in the text the lower ranks left, and a match that
// overlaps one already taken is dropped. A name is looked for in s as it stands, not in the text
// the replacements put in.
func (m *nameMatcher) mask(s string) string {
	scanned(len(s))
	var found []nameMatch
	u := int32(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		for u != 0 && m.child(u, c) == 0 {
			u = m.fail[u]
		}
		u = m.child(u, c)
		n := u
		if m.out[n] < 0 {
			n = m.link[n]
		}
		for ; n != 0; n = m.link[n] {
			scanned(1)
			rank := m.out[n]
			found = append(found, nameMatch{rank, i + 1 - len(m.list[rank]), i + 1})
		}
	}
	if len(found) == 0 {
		return s
	}
	sort.Slice(found, func(a, b int) bool {
		if found[a].rank != found[b].rank {
			return found[a].rank < found[b].rank
		}
		return found[a].start < found[b].start
	})
	var taken []bool // taken[i]: byte i is in a match already replaced
	var kept []nameMatch
	for g := 0; g < len(found); {
		h := g
		for h < len(found) && found[h].rank == found[g].rank {
			h++
		}
		from := len(kept)
		lastEnd := 0
		for _, f := range found[g:h] {
			if f.start < lastEnd || !standalone(s, taken, f.start, f.end) || overlaps(taken, f) {
				continue
			}
			kept = append(kept, f)
			lastEnd = f.end
		}
		if len(kept) > from && taken == nil {
			taken = make([]bool, len(s))
		}
		for _, f := range kept[from:] {
			for i := f.start; i < f.end; i++ {
				taken[i] = true
			}
		}
		g = h
	}
	if len(kept) == 0 {
		return s
	}
	sort.Slice(kept, func(a, b int) bool { return kept[a].start < kept[b].start })
	var b []byte
	last := 0
	for _, f := range kept {
		b = append(b, s[last:f.start]...)
		b = append(b, Redacted...)
		last = f.end
	}
	b = append(b, s[last:]...)
	return string(b)
}

func overlaps(taken []bool, f nameMatch) bool {
	if taken == nil {
		return false
	}
	for i := f.start; i < f.end; i++ {
		if taken[i] {
			return true
		}
	}
	return false
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isTokenByte(c byte) bool { return isAlnum(c) || c == '_' || c == '-' }

// standalone reports whether s[k:end] stands alone: not next to a letter, digit, underscore or
// hyphen, nor to a dot that has a letter or digit beyond it (so 12 leaves 123, 12.5 and a model
// name such as gemini-2.5-flash alone). A byte in taken is part of a replaced name: it is read as
// the marker's own bracket, which is neither.
func standalone(s string, taken []bool, k, end int) bool {
	in := func(i int) bool { return taken != nil && taken[i] }
	before := k == 0 || in(k-1) || !isTokenByte(s[k-1]) && !(s[k-1] == '.' && k >= 2 && isAlnum(s[k-2]) && !in(k-2))
	after := end == len(s) || in(end) || !isTokenByte(s[end]) && !(s[end] == '.' && end+1 < len(s) && isAlnum(s[end+1]) && !in(end+1))
	return before && after
}
