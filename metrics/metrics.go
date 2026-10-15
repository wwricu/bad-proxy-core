// Package metrics holds the engine's live counters. Hot-path cost is one
// atomic increment per event: call sites cache the *ProtoStats pointer at
// construction time, so the map lookup happens once at startup, never per
// connection.
package metrics

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

type ProtoStats struct {
	Accepted      atomic.Int64 // inbound connections
	HandshakeFail atomic.Int64
	DialOK        atomic.Int64 // outbound dials to upstream
	DialFail      atomic.Int64
}

type Metrics struct {
	mu     sync.Mutex
	protos map[string]*ProtoStats

	TunnelsActive atomic.Int64
	BytesUp       atomic.Int64
	BytesDown     atomic.Int64
	CopyErr       atomic.Int64
}

// Default is the process-wide registry; a second instance is legal for
// tests but nothing else needs one.
var Default = New()

func New() *Metrics {
	return &Metrics{protos: make(map[string]*ProtoStats, 8)}
}

// Proto returns the per-protocol counters, creating the entry on first use.
// Call during construction, not in accept loops.
func (m *Metrics) Proto(name string) *ProtoStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.protos[name]
	if !ok {
		p = &ProtoStats{}
		m.protos[name] = p
	}
	return p
}

func (m *Metrics) Snapshot() string {
	var b strings.Builder
	fmt.Fprintf(&b, "tunnels=%d up=%s down=%s copy_err=%d",
		m.TunnelsActive.Load(), humanBytes(m.BytesUp.Load()),
		humanBytes(m.BytesDown.Load()), m.CopyErr.Load())
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.protos))
	for name := range m.protos {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		p := m.protos[name]
		fmt.Fprintf(&b, " | %s acc=%d hs_fail=%d dial_ok=%d dial_fail=%d",
			name, p.Accepted.Load(), p.HandshakeFail.Load(),
			p.DialOK.Load(), p.DialFail.Load())
	}
	return b.String()
}

func sortStrings(s []string) { // stdlib-only insertion sort; n is tiny
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}
