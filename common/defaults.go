package common

import "time"

// Process-wide defaults, kept in one place so both the config layer (core) and
// the transport layer resolve "unset" the same way. Config values override
// these; these are only the fallbacks.
const (
	// DefaultRelayBufferKiB is the per-stream relay copy buffer size when
	// general.relay_buffer_kib is unset.
	DefaultRelayBufferKiB = 64
	// MaxRelayBufferKiB bounds a configured relay buffer.
	MaxRelayBufferKiB = 1024

	// DefaultPadding is the xhttp X-Padding length range ("min-max"). It is the
	// only padding there is: request size is a property of the transport, not a
	// choice each endpoint makes, so no config can set it and a user is not asked
	// to know what it is for. Xray leaves padding off unless it is configured;
	// ours is on because a tunnel that carries a 30-byte handshake and then a
	// burst has a size worth hiding, and 50-200 bytes is what it costs.
	DefaultPadding = "50-200"
)

// Default timeouts. A zero duration means "no bound" for these (relay idle
// never fires unless configured).
const (
	DefaultHandshakeTimeout = 10 * time.Second
	DefaultDialTimeout      = 15 * time.Second
)
