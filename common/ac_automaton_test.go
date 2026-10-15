package common

import (
	"math/rand"
	"strings"
	"testing"
)

// naive reports whether any pattern occurs as a substring of key.
func naive(patterns []string, key string) bool {
	for _, p := range patterns {
		if p != "" && strings.Contains(key, p) {
			return true
		}
	}
	return false
}

func TestACMatchesNaiveSubstring(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []byte{'a', 'b', 'c'} // tiny alphabet forces overlaps/failure links
	randStr := func(maxLen int) string {
		n := rng.Intn(maxLen + 1)
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(b)
	}

	for trial := 0; trial < 2000; trial++ {
		pcount := 1 + rng.Intn(6)
		patterns := make([]string, pcount)
		for i := range patterns {
			patterns[i] = randStr(4)
		}
		ac := NewACAutomaton(patterns)
		for k := 0; k < 30; k++ {
			key := randStr(10)
			if got, want := ac.MatchAny(key), naive(patterns, key); got != want {
				t.Fatalf("patterns=%q key=%q ac=%v naive=%v", patterns, key, got, want)
			}
		}
	}
}

func TestACSuffixEmitPropagation(t *testing.T) {
	// "he" is a suffix of "she": a text containing "she" must also register
	// the "he" match through the failure/emit chain.
	ac := NewACAutomaton([]string{"he"})
	if !ac.MatchAny("she") {
		t.Fatal(`MatchAny("she") = false, want true (pattern "he" is a substring)`)
	}
	if ac.MatchAny("shx") {
		t.Fatal(`MatchAny("shx") = true, want false`)
	}
}

func TestACMultiBytePattern(t *testing.T) {
	// byte-wise iteration: a multi-byte UTF-8 pattern must match exactly.
	ac := NewACAutomaton([]string{"中文"})
	if !ac.MatchAny("说中文的") {
		t.Fatal("multi-byte pattern failed to match")
	}
	if ac.MatchAny("说中 文的") { // 中 and 文 separated, pattern bytes absent
		t.Fatal("false positive on partial multi-byte")
	}
}

func TestACEscapeCycles(t *testing.T) {
	// overlapping patterns that would loop if failure links pointed to an
	// equal-or-longer state. Must terminate and answer correctly.
	ac := NewACAutomaton([]string{"a", "aa", "aaa"})
	for _, key := range []string{"", "a", "aa", "aaa", "aaaa", "baaab", "bbbb"} {
		if ac.MatchAny(key) != naive([]string{"a", "aa", "aaa"}, key) {
			t.Fatalf("key=%q mismatch", key)
		}
	}
}
