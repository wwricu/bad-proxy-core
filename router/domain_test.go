package router

import "testing"

// The whole point of replacing the byte automaton: domain: is a label-boundary
// suffix match, not a substring match. These are the false positives a naive
// substring automaton produced, plus the cases that must still match.
func TestDomainMatcher(t *testing.T) {
	m := NewDomainMatcher([]string{"abc.com", "example.org"})

	cases := []struct {
		host string
		want bool
	}{
		// exact and subdomain - must match
		{"abc.com", true},
		{"www.abc.com", true},
		{"a.b.c.abc.com", true},
		{"example.org", true},
		{"x.example.org", true},
		// substring false positives - must NOT match
		{"xabc.com", false},        // "abc.com" is a substring, not a suffix
		{"abc.com.evil.cn", false}, // domain is a prefix, not a suffix
		{"notabc.com", false},
		{"abc.comm", false},
		{"com", false}, // a single label is not the full domain
		{"abc.net", false},
		{"", false},
		// case insensitivity and trailing root dot
		{"WWW.ABC.COM", true},
		{"abc.com.", true},
		{"www.abc.com.", true},
	}
	for _, c := range cases {
		if got := m.MatchAny(c.host); got != c.want {
			t.Errorf("MatchAny(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// A single-label rule ("onion") behaves as top-level-domain suffix matching:
// it must catch any host whose last label equals it, and nothing else.
func TestDomainMatcherSingleLabel(t *testing.T) {
	m := NewDomainMatcher([]string{"onion"})
	cases := []struct {
		host string
		want bool
	}{
		{"onion", true},
		{"abc.onion", true},
		{"a.b.onion", true},
		{"onionish", false},
		{"xonion.com", false},
	}
	for _, c := range cases {
		if got := m.MatchAny(c.host); got != c.want {
			t.Errorf("MatchAny(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestDomainMatcherEmpty(t *testing.T) {
	m := NewDomainMatcher(nil)
	if m.MatchAny("anything.com") {
		t.Fatal("empty matcher must never match")
	}
}
