package common

import "time"

// Process-wide defaults, kept in one place so both the config layer (core) and
// the transport layer resolve "unset" the same way. Config values override
// these; these are only the fallbacks.
const (
	// DefaultRelayBufferKiB is the per-stream relay copy buffer size when
	// general.relay_buffer_kib is unset. 32 KiB is Go's own io.Copy default: it
	// is big enough to keep the chunked loop at full speed while a parked copy
	// pins only one buffer per direction, so an idle tunnel costs 64 KiB rather
	// than 128 KiB.
	DefaultRelayBufferKiB = 32
	// MaxRelayBufferKiB bounds a configured relay buffer.
	MaxRelayBufferKiB = 1024

	// DefaultPadding is the xhttp x_padding length range ("min-max"). It is
	// Xray's own default, and it is the only padding there is: the wire format
	// is fixed by the transport, not by a choice each endpoint makes, so no
	// config sets it and a user is not asked to know what it is for. Xray's
	// server does not require the parameter, so nothing here demands it either;
	// it is drawn on every request only so request sizes keep moving.
	DefaultPadding = "100-1000"
)

// Default timeouts. Handshake and dial bounds are hard; the relay idle bound is
// a reclamation timer that can be switched off explicitly with "0s".
const (
	DefaultHandshakeTimeout = 10 * time.Second
	DefaultDialTimeout      = 15 * time.Second
	// DefaultRelayIdleTimeout reclaims a tunnel that has moved zero bytes in
	// either direction. It is on by default because this value is also what the
	// embedded clients get, and a client-side timer that fires too eagerly cuts
	// legitimate push long-polls, which sit at zero bytes for minutes on end on
	// purpose. 10 minutes clears the tunnel well before the kernel's own
	// keepalive would (2 hours idle on Linux) yet stays above any real
	// application-layer hold. A public server should tighten it to a few minutes.
	DefaultRelayIdleTimeout = 10 * time.Minute
)
