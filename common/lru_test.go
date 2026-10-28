package common

import "testing"

// At capacity the oldest key must be evicted so the new one fits; without that,
// Add fails and the cache turns into a permanent seen-set.
func TestLRUEvictsOldestAtCapacity(t *testing.T) {
	lru := NewLRU(2)
	for _, key := range []string{"a", "b"} {
		if err := lru.Add(key); err != nil {
			t.Fatalf("add %q: %v", key, err)
		}
	}
	if err := lru.Add("c"); err != nil {
		t.Fatalf("add at capacity should evict, got %v", err)
	}
	// b is still inside the window; a was evicted to make room for c.
	if err := lru.Add("b"); err == nil {
		t.Fatal("b should still be remembered")
	}
	if err := lru.Add("a"); err != nil {
		t.Fatalf("a should have been evicted, got %v", err)
	}
}
