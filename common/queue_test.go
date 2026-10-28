package common

import "testing"

// A full ring used to report size 0 because it represented full and empty the
// same way. That made LRU eviction unreachable and a push into a full queue
// silently advanced the tail. The explicit count keeps the two states apart.
func TestQueueCountsAtCapacity(t *testing.T) {
	q := NewQueue[int](3, 3)
	for _, v := range []int{1, 2, 3} {
		if err := q.Push(v); err != nil {
			t.Fatalf("push %d: %v", v, err)
		}
	}
	if got := q.Size(); got != 3 {
		t.Fatalf("size at capacity = %d, want 3", got)
	}
	if err := q.Push(9); err == nil {
		t.Fatal("push into a full bounded queue succeeded")
	}
	if got := q.Size(); got != 3 {
		t.Fatalf("failed push changed size to %d", got)
	}
	if got := q.Pop(); got != 1 {
		t.Fatalf("pop = %d, want the oldest element 1", got)
	}
	if err := q.Push(4); err != nil {
		t.Fatalf("push after pop: %v", err)
	}
	if got := q.Pop(); got != 2 {
		t.Fatalf("pop = %d, want 2", got)
	}
}
