package router

import (
	"strings"

	"github.com/wwricu/bad-proxy-core/structure"
)

// KeywordMatcher does pure substring matching over all configured keywords in
// one scan via the Aho-Corasick automaton. Unlike domain:, keyword: ignores
// label boundaries: "bing" matches "www.bing.com" and "bing.net" alike.
type KeywordMatcher struct {
	ac *structure.ACAutomaton
}

func NewKeywordMatcher(keywords []string) Matcher {
	lowered := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		if kw = strings.ToLower(strings.TrimSpace(kw)); kw != "" {
			lowered = append(lowered, kw)
		}
	}
	return &KeywordMatcher{ac: structure.NewACAutomaton(lowered)}
}

func (m *KeywordMatcher) MatchAny(key string) bool {
	return m.ac.MatchAny(strings.ToLower(key))
}
