package metrics

import (
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestSnapshotPerProto(t *testing.T) {
	m := New()
	btp, vless := m.Proto("btp"), m.Proto("vless")
	if m.Proto("btp") != btp {
		t.Fatal("Proto must return the same cached pointer")
	}
	btp.Accepted.Add(3)
	vless.DialOK.Add(2)
	m.BytesUp.Store(2048)
	m.BytesDown.Store(1024)

	line := m.Snapshot()
	for _, want := range []string{"up=2.0KB", "down=1.0KB", "btp acc=3", "vless acc=0 hs_fail=0 dial_ok=2"} {
		if !strings.Contains(line, want) {
			t.Fatalf("snapshot %q missing %q", line, want)
		}
	}
	// protocol sections must be alphabetically stable across calls
	i := strings.Index(line, "btp")
	j := strings.Index(line, "vless")
	if i > j {
		t.Fatalf("sections not sorted: %q", line)
	}
}

func TestConcurrentAdds(t *testing.T) {
	m := New()
	p := m.Proto("trojan")
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				p.Accepted.Add(1)
				m.CopyErr.Add(1)
			}
		}()
	}
	wg.Wait()
	if p.Accepted.Load() != 8000 || m.CopyErr.Load() != 8000 {
		t.Fatalf("lost updates: accepted=%d copyErr=%d", p.Accepted.Load(), m.CopyErr.Load())
	}
}

// documents ordering used by Snapshot
func TestSortStrings(t *testing.T) {
	s := []string{"c", "a", "b"}
	sortStrings(s)
	if !sort.StringsAreSorted(s) {
		t.Fatalf("not sorted: %v", s)
	}
}
