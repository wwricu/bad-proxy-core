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

	// DefaultPadding is the xhttp x_padding length range ("min-max"). It is
	// Xray's own default, and it is the only padding there is: the wire format
	// is fixed by the transport and by what an Xray server demands, not by a
	// choice each endpoint makes, so no config sets it and a user is not asked
	// to know what it is for.
	DefaultPadding = "100-1000"
)

// Default timeouts. A zero duration means "no bound" for these (relay idle
// never fires unless configured).
const (
	DefaultHandshakeTimeout = 10 * time.Second
	DefaultDialTimeout      = 15 * time.Second
)
