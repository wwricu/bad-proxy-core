package router

import (
	"strings"

	"github.com/wwricu/bad-proxy-core/logger"
)

type Config struct {
	Tag   string   `json:"tag"`
	Rules []string `json:"rules"`
}

// Router composes the four matchers (see matcher.go) and asks each in turn.
type Router struct {
	Tag      string
	matchers []Matcher
}

func (router Router) MatchAny(key string) bool {
	for _, m := range router.matchers {
		if m.MatchAny(key) {
			return true
		}
	}
	return false
}

// NewRouter compiles one routing rule set. It cannot fail: an unreadable
// rules.dat and an unknown named list are both skipped without remedy, because
// a router that lost part of its list still has to route the rest.
func NewRouter(tag string, rules []string, routerPath string) (router *Router) {
	allRules, readErr := readAllFromGob(routerPath)
	if readErr != nil {
		// a missing/broken rules.dat only disables named lists (geosite/geoip);
		// inline full:/domain:/regexp: rules must still load and route
		logger.Warnf("rules file %s unavailable, named rules disabled: %v", routerPath, readErr)
		allRules = map[string]*ParsedList{}
	}

	fullDomains := make([]string, 0)
	domains := make([]string, 0)
	keywords := make([]string, 0)
	regexStrings := make([]string, 0)

	// add stores one entry under its rule type. include entries are expanded
	// inside a named list; seen guards cycles between lists.
	var add func(entry Entry, seen map[string]bool)
	add = func(entry Entry, seen map[string]bool) {
		switch RuleType(entry.Type) {
		case FULL:
			fullDomains = append(fullDomains, entry.Value)
		case DOMAIN:
			domains = append(domains, entry.Value)
		case KEYWORD:
			keywords = append(keywords, entry.Value)
		case REGEXP:
			regexStrings = append(regexStrings, entry.Value)
		case INCLUDE:
			name := strings.ToUpper(entry.Value)
			if seen[name] {
				return
			}
			seen[name] = true
			if sub, exist := allRules[name]; exist && sub != nil {
				for _, e := range sub.Entry {
					add(e, seen)
				}
			}
		}
	}

	for _, rule := range rules {
		ruleType, ruleStr := RULE, rule
		if kv := strings.SplitN(rule, ":", 2); len(kv) > 1 {
			ruleType, ruleStr = RuleType(kv[0]), kv[1]
		}
		switch ruleType {
		case FULL, DOMAIN, KEYWORD, REGEXP:
			add(Entry{Type: string(ruleType), Value: ruleStr}, nil)
		default: // "rule:string" || "string"
			if allRules[strings.ToUpper(ruleStr)] == nil {
				break
			}
			add(Entry{Type: string(INCLUDE), Value: ruleStr},
				map[string]bool{})
		}
	}

	router = &Router{
		Tag: tag,
		matchers: []Matcher{ // cheapest first; regexp last
			NewFullMatcher(fullDomains),
			NewDomainMatcher(domains),
			NewKeywordMatcher(keywords),
			NewRegexMatcher(regexStrings),
		},
	}
	logger.Infof("router %s: %d rules from %d named lists", tag,
		len(fullDomains)+len(regexStrings)+len(domains)+len(keywords), len(allRules))
	return
}
