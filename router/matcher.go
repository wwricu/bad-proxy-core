package router

import (
	"regexp"
	"strings"

	"github.com/wwricu/bad-proxy-core/common"
	"github.com/wwricu/bad-proxy-core/logger"
)

// Matcher is one rule family. A Router composes the four below and asks each in
// turn, so a new rule type only means a new Matcher here plus one line in
// NewRouter.
type Matcher interface {
	MatchAny(key string) bool
}

// FullMatcher implements the "full:" rule - exact host set membership.
type FullMatcher map[string]bool

func NewFullMatcher(fullDomains []string) Matcher {
	var fullMatcher FullMatcher = make(map[string]bool, len(fullDomains))
	for _, domain := range fullDomains {
		fullMatcher[domain] = true
	}
	return fullMatcher
}

func (matcher FullMatcher) MatchAny(key string) bool {
	_, exist := matcher[key]
	return exist
}

// RegexMatcher implements the "regexp:" rule - the host matches if any compiled
// pattern matches it.
type RegexMatcher []*regexp.Regexp

func NewRegexMatcher(regexStrings []string) Matcher {
	var regexMatcher RegexMatcher = make([]*regexp.Regexp, 0, len(regexStrings))
	for _, ex := range regexStrings {
		compiled, err := regexp.Compile(ex)
		if err != nil {
			logger.Warnf("bad rule regex, skipped: %s", ex)
			continue
		}
		regexMatcher = append(regexMatcher, compiled)
	}
	return regexMatcher
}

func (matcher RegexMatcher) MatchAny(key string) bool {
	for _, re := range matcher {
		if re.MatchString(key) {
			return true
		}
	}
	return false
}

// DomainMatcher implements the "domain:" rule: a host matches when it equals a
// registered domain or is a subdomain of one - suffix match at a label
// boundary. It is not substring matching.
//
// Patterns are stored in a trie keyed by the domain's labels in reverse
// ("www.abc.com" -> com -> abc -> www), so a host match is a walk from its
// rightmost label leftwards; hitting a terminal node means the host is that
// domain or a subdomain. This costs O(labels) per lookup and can never produce
// the substring false positives a byte automaton gives ("xabc.com" or
// "abc.com.evil.cn" matching rule "abc.com").
type DomainMatcher struct {
	root *domainNode
}

type domainNode struct {
	children map[string]*domainNode
	terminal bool
}

func NewDomainMatcher(domains []string) Matcher {
	root := &domainNode{}
	for _, d := range domains {
		if labels := splitLabels(d); len(labels) > 0 {
			root.insert(labels)
		}
	}
	return &DomainMatcher{root: root}
}

func (m *DomainMatcher) MatchAny(key string) bool {
	node := m.root
	labels := splitLabels(key)
	// walk from the top-level domain downwards; a terminal reached at any depth
	// is a suffix match, so "abc.com" also catches "www.abc.com"
	for i := len(labels) - 1; i >= 0; i-- {
		child, exist := node.children[labels[i]]
		if !exist {
			return false
		}
		node = child
		if node.terminal {
			return true
		}
	}
	return false
}

func (n *domainNode) insert(labels []string) {
	node := n
	for i := len(labels) - 1; i >= 0; i-- {
		if node.children == nil {
			node.children = make(map[string]*domainNode)
		}
		child, exist := node.children[labels[i]]
		if !exist {
			child = &domainNode{}
			node.children[labels[i]] = child
		}
		node = child
	}
	node.terminal = true
}

// splitLabels lowercases and splits a host on '.'. Domains are case
// insensitive. A trailing dot ("abc.com.") is the FQDN root and is dropped so
// it matches "abc.com".
func splitLabels(domain string) []string {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	if domain == "" {
		return nil
	}
	return strings.Split(domain, ".")
}

// KeywordMatcher does pure substring matching over all configured keywords in
// one scan via the Aho-Corasick automaton. Unlike domain:, keyword: ignores
// label boundaries: "bing" matches "www.bing.com" and "bing.net" alike.
type KeywordMatcher struct {
	ac *common.ACAutomaton
}

func NewKeywordMatcher(keywords []string) Matcher {
	lowered := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		if kw = strings.ToLower(strings.TrimSpace(kw)); kw != "" {
			lowered = append(lowered, kw)
		}
	}
	return &KeywordMatcher{ac: common.NewACAutomaton(lowered)}
}

func (m *KeywordMatcher) MatchAny(key string) bool {
	return m.ac.MatchAny(strings.ToLower(key))
}
