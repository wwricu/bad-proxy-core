package protocols

import (
	"net"

	"github.com/wwricu/bad-proxy-core/common"
)

// FreeOutbound performs no handshake: the connection it wraps already points
// at the target address (direct dial, "freedom" routing).
type FreeOutbound struct {
	*common.Buffered
}

func NewFreeOutbound(conn net.Conn) *FreeOutbound {
	return &FreeOutbound{Buffered: common.NewBuffered(conn)}
}

func (out *FreeOutbound) Connect(targetAddr string) error { return nil }
