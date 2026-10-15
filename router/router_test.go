package router

import "testing"

// Regression: a missing rules.dat must only disable named lists, not the whole
// router. Inline domain:/full:/regexp: rules still have to load and match.
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
