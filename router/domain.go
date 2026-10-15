package router

import "strings"

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
