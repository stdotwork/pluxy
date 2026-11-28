package geminigenerate

import (
	"slices"
	"strings"
)

var onScan func(n int)

func scanned(n int) {
	if onScan != nil {
		onScan(n)
	}
}

type nameMatcher struct {
	list    []string
	first   []int32
	next    []int32
	char    []byte
	fail    []int32
	out     []int32
	link    []int32
	special []int32
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

const markerWord = "redacted"

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
