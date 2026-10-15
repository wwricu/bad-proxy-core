package router

import "testing"

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
