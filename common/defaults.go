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

	// DefaultPadding is the xhttp X-Padding length range ("min-max") applied
	// when an endpoint leaves padding unset, so request-size fingerprinting is
	// blunted without any configuration.
	DefaultPadding = "50-200"
)

// Default timeouts. A zero duration means "no bound" for these (relay idle
// never fires unless configured).
const (
	DefaultHandshakeTimeout = 10 * time.Second
	DefaultDialTimeout      = 15 * time.Second
)
