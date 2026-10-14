package protocols

import "net"

// FreeOutbound performs no handshake: the connection it wraps already points
// at the target address (direct dial, "freedom" routing).
type FreeOutbound struct {
	*Buffered
}

func NewFreeOutbound(conn net.Conn) *FreeOutbound {
	return &FreeOutbound{Buffered: NewBuffered(conn)}
}

func (out *FreeOutbound) Connect(targetAddr string) error { return nil }
