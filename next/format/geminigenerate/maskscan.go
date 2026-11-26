package geminigenerate

import (
	"slices"
	"strings"
)

// onScan, when set, is told how much the masking code does: the bytes the JSON scanners pass over,
// the steps of the name search, and each name tried against a string. Tests set it to hold the
// work to a bound that does not depend on the clock; it is nil otherwise.
var onScan func(n int)

func scanned(n int) {
	if onScan != nil {
		onScan(n)
	}
}

// nameMatcher tells which of a list of names a string holds, in one pass over the string
// (Aho-Corasick), so that only those names are tried against it. The list is ordered by rank: a
// lower rank is replaced first.
//
// A string costs one pass over its length and one step for each distinct name it holds, whatever
// the number of names known; memory is the size of the names and of the string's hits, never one
// entry for every name that ends at every position. Replacing a name that the string holds scans
// the string once more, as replacing every name one after the other did.
type nameMatcher struct {
	list    []string
	first   []int32 // first child of a node, 0 for none (the root is node 0 and never a child)
	next    []int32 // next sibling
	char    []byte  // the byte on the edge into the node
	fail    []int32 // longest proper suffix of the node's text that is also a node
	out     []int32 // rank of the name that ends at the node, -1 for none
	link    []int32 // nearest node on the fail chain that ends a name, 0 for none
	special []int32 // ranks of the names that hold the marker's word, in rank order
	seen    []uint32
	gen     uint32
	found   []int32
}

func newNameMatcher(list []string) *nameMatcher {
	m := &nameMatcher{list: list, seen: make([]uint32, len(list))}
	m.addNode(0)
	for rank, id := range list {
		if strings.ContainsAny(id, "[]") || strings.Contains(id, markerWord) {
			m.special = append(m.special, int32(rank))
		}
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

// markerWord is the letters of the marker. A name that is not in a string as it stands matches in
// it only where a replacement wrote a marker, and takes in some of the marker's text: its
// brackets, or its word (a name that stands alone inside a marker is "redacted" itself, since the
// letters have letters beside them). Such a name holds a bracket or the word.
const markerWord = "redacted"

// hold returns the ranks of the names s holds, lowest first. The slice is the matcher's own and
// lasts until the next call.
func (m *nameMatcher) hold(s string) []int32 {
	scanned(len(s))
	m.gen++
	if m.gen == 0 {
		clear(m.seen)
		m.gen = 1
	}
	m.found = m.found[:0]
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
		// A name already found in s has every name that ends inside it found too, so the walk
		// stops at the first one it has seen.
		for ; n != 0; n = m.link[n] {
			rank := m.out[n]
			if m.seen[rank] == m.gen {
				break
			}
			m.seen[rank] = m.gen
			m.found = append(m.found, rank)
			scanned(1)
		}
	}
	slices.Sort(m.found)
	return m.found
}

// mask replaces the names in s one after the other in rank order, each where it stands alone, in
// the text the ones before left; the result is the same as trying every name, because a name that
// s does not hold can match only across the text of a marker an earlier replacement wrote, and
// the names that can are tried whenever s holds a marker.
func (m *nameMatcher) mask(s string) string {
	held := m.hold(s)
	marked := strings.Contains(s, Redacted)
	special := m.special
	for len(held) > 0 || len(special) > 0 {
		var rank int32
		switch {
		case len(special) == 0 || len(held) > 0 && held[0] <= special[0]:
			rank = held[0]
			if len(special) > 0 && special[0] == rank {
				special = special[1:]
			}
			held = held[1:]
		default:
			rank = special[0]
			special = special[1:]
			if !marked {
				continue
			}
		}
		scanned(1)
		if r := replaceToken(s, m.list[rank]); r != s {
			s, marked = r, true
		}
	}
	return s
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isTokenByte(c byte) bool { return isAlnum(c) || c == '_' || c == '-' }

// replaceToken replaces id in s where it stands alone: not next to a letter, digit, underscore
// or hyphen, nor to a dot that has a letter or digit beyond it (so 12 leaves 123, 12.5 and a
// model name such as gemini-2.5-flash alone).
func replaceToken(s, id string) string {
	if id == "" || !strings.Contains(s, id) {
		return s
	}
	var b strings.Builder
	i := 0
	for {
		k := strings.Index(s[i:], id)
		if k < 0 {
			break
		}
		k += i
		end := k + len(id)
		before := k == 0 || !isTokenByte(s[k-1]) && !(s[k-1] == '.' && k >= 2 && isAlnum(s[k-2]))
		after := end == len(s) || !isTokenByte(s[end]) && !(s[end] == '.' && end+1 < len(s) && isAlnum(s[end+1]))
		if before && after {
			b.WriteString(s[i:k])
			b.WriteString(Redacted)
			i = end
		} else {
			b.WriteString(s[i : k+1])
			i = k + 1
		}
	}
	b.WriteString(s[i:])
	return b.String()
}
