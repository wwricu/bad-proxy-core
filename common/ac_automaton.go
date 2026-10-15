package common

// ACAutomaton is an Aho-Corasick multi-pattern substring matcher. It answers
// MatchAny("does any pattern occur as a substring of the key?") in a single
// O(len(key)) scan regardless of how many patterns are loaded, which is the
// right tool for keyword rules (pure substring matching), not domain rules.
type ACAutomaton struct {
	success map[uint8]*ACAutomaton
	failure *ACAutomaton
	emit    bool
}

func NewTrie() *ACAutomaton {
	trie := &ACAutomaton{
		success: make(map[uint8]*ACAutomaton),
		failure: nil,
		emit:    false,
	}
	trie.failure = trie
	return trie
}

// Add inserts one pattern. Iteration is byte-wise, not `for i := range s`,
// because range yields rune indices and would skip continuation bytes of a
// multi-byte UTF-8 pattern.
func (root *ACAutomaton) Add(value string) {
	curr := root
	for i := 0; i < len(value); i++ {
		ch := value[i]
		child, exist := curr.success[ch]
		if !exist {
			child = NewTrie()
			curr.success[ch] = child
		}
		curr = child
	}
	curr.emit = true
}

func NewACAutomaton(patterns []string) *ACAutomaton {
	ac := NewTrie()
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		ac.Add(pattern)
	}
	ac.build()
	return ac
}

// MatchAny reports whether any pattern appears as a substring of key.
func (root *ACAutomaton) MatchAny(key string) bool {
	curr := root
	for i := 0; i < len(key); i++ {
		c := key[i]
		next, exist := curr.success[c]
		for !exist && curr != root {
			curr = curr.failure
			next, exist = curr.success[c]
		}
		if exist {
			curr = next
		} // else curr == root with no edge on c: stay at root
		if curr.emit {
			return true
		}
	}
	return false
}

// build wires failure links breadth-first and propagates emit along them, so
// a state reports a hit when any pattern ends at it OR at any suffix state.
func (root *ACAutomaton) build() {
	queue := NewQueue[*ACAutomaton](0, 1000)
	root.failure = root
	// depth-1 states fall back to root directly
	for _, child := range root.success {
		child.failure = root
		if err := queue.Push(child); err != nil {
			panic(err)
		}
	}
	for queue.Size() != 0 {
		state := queue.Pop()
		for c, child := range state.success {
			if err := queue.Push(child); err != nil {
				panic(err)
			}
			// longest proper suffix of `child` that is a trie state
			fail := state.failure
			target := root
			for fail != root {
				if next, exist := fail.success[c]; exist {
					target = next
					break
				}
				fail = fail.failure
			}
			if target == root {
				// no longer suffix; fall back to root's own edge on c, which
				// is always shorter than child (child has depth >= 2 here)
				if next, exist := root.success[c]; exist && next != child {
					target = next
				}
			}
			child.failure = target
			// inherit suffix matches: a pattern ending at a shorter suffix
			// is also present in the text this state represents
			child.emit = child.emit || child.failure.emit
		}
	}
}
