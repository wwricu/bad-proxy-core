package router

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"testing"
)

// Regression: a missing rules.dat must only disable named lists, not the whole
// router. Inline domain:/full:/keyword:/regexp: rules still have to load and
// match.
func TestNewRouterSurvivesMissingRulesFile(t *testing.T) {
	r, err := NewRouter("us", []string{"domain:abc.com", "full:exact.io", "geosite:missing"}, "does_not_exist.dat")
	if err != nil {
		t.Fatalf("NewRouter returned err on missing rules file: %v", err)
	}
	if r == nil {
		t.Fatal("NewRouter returned nil router")
	}
	// inline domain rule works with correct suffix semantics
	if !r.MatchAny("www.abc.com") {
		t.Error("inline domain: rule did not match www.abc.com")
	}
	if r.MatchAny("xabc.com") {
		t.Error("inline domain: rule wrongly matched substring xabc.com")
	}
	// inline full rule still matches exactly
	if !r.MatchAny("exact.io") {
		t.Error("inline full: rule did not match exact.io")
	}
	// a named list resolves to nothing without the file, must not panic/match
	if r.MatchAny("whatever.com") {
		t.Error("unexpected match from an unresolvable named list")
	}
}

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

// keyword: is the plain-substring matcher (Aho-Corasick): it ignores label
// boundaries, unlike domain:.
func TestKeywordMatcher(t *testing.T) {
	m := NewKeywordMatcher([]string{"bing", "ads"})
	cases := []struct {
		host string
		want bool
	}{
		{"www.bing.com", true},
		{"bing.net", true},
		{"ads.example.com", true},
		{"xbingo.y", true}, // substring ignores label boundaries
		{"doubleclick.net", false},
		{"BING.COM", true}, // case-insensitive
		{"", false},
	}
	for _, c := range cases {
		if got := m.MatchAny(c.host); got != c.want {
			t.Errorf("MatchAny(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestKeywordMatcherEmpty(t *testing.T) {
	m := NewKeywordMatcher(nil)
	if m.MatchAny("anything") {
		t.Fatal("empty keyword matcher should match nothing")
	}
}

// Named lists in rules.dat reference other lists via include entries (dlc
// categories). NewRouter must expand them transitively, and cycle-guard
// against lists that include each other.
func TestNamedListIncludeExpansion(t *testing.T) {
	all := map[string]*ParsedList{
		"CN": {Name: "CN", Entry: []Entry{
			{Type: "domain", Value: "direct.cn"},
			{Type: "include", Value: "tencent"},
			{Type: "include", Value: "geo-cn"},
		}},
		"TENCENT": {Name: "TENCENT", Entry: []Entry{
			{Type: "domain", Value: "qq.com"},
			{Type: "include", Value: "cn"}, // cycle back to the parent
		}},
		"GEO-CN": {Name: "GEO-CN", Entry: []Entry{
			{Type: "full", Value: "exact.cn"},
			{Type: "include", Value: "nonexistent-list"},
		}},
	}
	path := filepath.Join(t.TempDir(), "rules.dat")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := gob.NewEncoder(f).Encode(all); err != nil {
		t.Fatal(err)
	}
	f.Close()

	r, err := NewRouter("direct", []string{"cn"}, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"direct.cn", "www.qq.com", "exact.cn"} {
		if !r.MatchAny(host) {
			t.Errorf("include-expanded list did not match %q", host)
		}
	}
	if r.MatchAny("other.com") {
		t.Error("router matched a host outside the lists")
	}
}
