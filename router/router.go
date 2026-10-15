package router

import (
	"log"
	"regexp"
	"strings"
)

type Config struct {
	Tag   string   `json:"tag"`
	Rules []string `json:"rules"`
}

type Matcher interface {
	MatchAny(key string) bool
}

type FullMatcher map[string]bool

func (matcher FullMatcher) MatchAny(key string) bool {
	_, exist := matcher[key]
	return exist
}

func NewFullMatcher(fullDomains []string) Matcher {
	var fullMatcher FullMatcher = make(map[string]bool, len(fullDomains))
	for _, domain := range fullDomains {
		fullMatcher[domain] = true
	}
	return fullMatcher
}

type RegexMatcher []*regexp.Regexp

func NewRegexMatcher(regexStrings []string) Matcher {
	var regexMatcher RegexMatcher = make([]*regexp.Regexp, 0, len(regexStrings))
	for _, ex := range regexStrings {
		compiled, err := regexp.Compile(ex)
		if err != nil {
			log.Println("bad rule regex, skipped:", ex)
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

func NewRouter(tag string, rules []string, routerPath string) (router *Router, err error) {
	allRules, readErr := readAllFromGob(routerPath)
	if readErr != nil {
		// a missing/broken rules.dat only disables named lists (geosite/geoip);
		// inline full:/domain:/regexp: rules must still load and route
		log.Printf("rules file %s unavailable, named rules disabled: %v\n", routerPath, readErr)
		allRules = map[string]*ParsedList{}
	}

	fullDomains := make([]string, 0)
	domains := make([]string, 0)
	keywords := make([]string, 0)
	regexStrings := make([]string, 0)
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
			if ruleList == nil || exist != true {
				break
			}
			for _, entry := range ruleList.Entry {
				switch entry.Type {
				case string(FULL):
					fullDomains = append(fullDomains, entry.Value)
				case string(DOMAIN):
					domains = append(domains, entry.Value)
				case string(KEYWORD):
					keywords = append(keywords, entry.Value)
				case string(REGEXP):
					regexStrings = append(regexStrings, entry.Value)
				}
			}
		}
	}
	router = &Router{
		Tag:      tag,
		matchers: make([]Matcher, 0),
	}
	log.Printf(
		"%d rules with %d domains loaded\n",
		len(allRules),
		len(fullDomains)+len(regexStrings)+len(domains)+len(keywords),
	)
	router.matchers = append(router.matchers, NewFullMatcher(fullDomains))
	router.matchers = append(router.matchers, NewRegexMatcher(regexStrings))
	router.matchers = append(router.matchers, NewDomainMatcher(domains))
	router.matchers = append(router.matchers, NewKeywordMatcher(keywords))
	return
}
