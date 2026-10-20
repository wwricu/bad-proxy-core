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
// rules.dat and a malformed inline rule are both reported and skipped, because
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

	// named lists reference each other with include entries (dlc categories
	// and compound lists), so expand them recursively. seen guards cycles.
	var appendEntries func(entries []Entry, seen map[string]bool)
	appendEntries = func(entries []Entry, seen map[string]bool) {
		for _, entry := range entries {
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
					continue
				}
				seen[name] = true
				if sub, exist := allRules[name]; exist && sub != nil {
					appendEntries(sub.Entry, seen)
				}
			}
		}
	}

	for _, rule := range rules {
		ruleType, ruleStr := RULE, rule
		kv := strings.Split(rule, ":")
		if len(kv) > 1 {
			ruleType, ruleStr = RuleType(kv[0]), kv[1]
		}
		switch ruleType {
		case FULL: // "full:string"
			fullDomains = append(fullDomains, ruleStr)
		case DOMAIN: // "domain:string"
			domains = append(domains, ruleStr)
		case KEYWORD: // "keyword:string"
			keywords = append(keywords, ruleStr)
		case REGEXP: // "regex:string"
			regexStrings = append(regexStrings, ruleStr)
		default: // "rule:string" || "string"
			ruleList, exist := allRules[strings.ToUpper(ruleStr)]
			if !exist || ruleList == nil {
				break
			}
			appendEntries(ruleList.Entry, map[string]bool{strings.ToUpper(ruleStr): true})
		}
	}
	router = &Router{
		Tag:      tag,
		matchers: make([]Matcher, 0),
	}
	logger.Infof("router %s: %d rules from %d named lists", tag,
		len(fullDomains)+len(regexStrings)+len(domains)+len(keywords), len(allRules))
	router.matchers = append(router.matchers, NewFullMatcher(fullDomains))
	router.matchers = append(router.matchers, NewRegexMatcher(regexStrings))
	router.matchers = append(router.matchers, NewDomainMatcher(domains))
	router.matchers = append(router.matchers, NewKeywordMatcher(keywords))
	return
}
