package proxy

import (
	"errors"
	"fmt"
	"time"
)

// GeneralConfig is the "general" object in the config file: process-wide
// settings that belong to no inbound, outbound, or router.
type GeneralConfig struct {
	// Stats is a duration string ("30s", "5m"). When set, one metrics summary
	// line is logged per interval.
	Stats string `json:"stats"`
	// HandshakeTimeout bounds connect+handshake on both sides ("10s").
	HandshakeTimeout string `json:"handshake_timeout"`
	// DialTimeout bounds a direct (freedom) connect to the target ("15s").
	DialTimeout string `json:"dial_timeout"`
	// RelayIdleTimeout reclaims a tunnel that has moved zero bytes in either
	// direction for this long ("5m"). Empty/0 (default) never times out, so a
	// legitimately idle long-lived tunnel is never cut.
	RelayIdleTimeout string `json:"relay_idle_timeout"`
	// RelayBufferKiB is the per-stream relay copy buffer size, 1-1024.
	RelayBufferKiB int `json:"relay_buffer_kib"`
}

const (
	defaultHandshakeTimeout = 10 * time.Second
	defaultDialTimeout      = 15 * time.Second
	defaultRelayBufferKiB   = 64
	maxRelayBufferKiB       = 1024
)

// settings is GeneralConfig after validation, with defaults applied.
type settings struct {
	statsEvery       time.Duration
	handshakeTimeout time.Duration
	dialTimeout      time.Duration
	relayIdle        time.Duration
	relayBufferKiB   int
}

func (c Config) resolveSettings() (settings, error) {
	s := settings{
		handshakeTimeout: defaultHandshakeTimeout,
		dialTimeout:      defaultDialTimeout,
		relayBufferKiB:   defaultRelayBufferKiB,
	}
	if c.General.Stats != "" {
		every, err := time.ParseDuration(c.General.Stats)
		if err != nil {
			return settings{}, errors.New("bad general.stats " + c.General.Stats)
		}
		s.statsEvery = every
	}
	if c.General.HandshakeTimeout != "" {
		d, err := time.ParseDuration(c.General.HandshakeTimeout)
		if err != nil {
			return settings{}, errors.New("bad general.handshake_timeout " + c.General.HandshakeTimeout)
		}
		s.handshakeTimeout = d
	}
	if c.General.DialTimeout != "" {
		d, err := time.ParseDuration(c.General.DialTimeout)
		if err != nil {
			return settings{}, errors.New("bad general.dial_timeout " + c.General.DialTimeout)
		}
		s.dialTimeout = d
	}
	if c.General.RelayIdleTimeout != "" {
		d, err := time.ParseDuration(c.General.RelayIdleTimeout)
		if err != nil {
			return settings{}, errors.New("bad general.relay_idle_timeout " + c.General.RelayIdleTimeout)
		}
		if d < 0 {
			return settings{}, errors.New("general.relay_idle_timeout must not be negative")
		}
		s.relayIdle = d
	}
	if c.General.RelayBufferKiB != 0 {
		if c.General.RelayBufferKiB < 1 || c.General.RelayBufferKiB > maxRelayBufferKiB {
			return settings{}, fmt.Errorf("general.relay_buffer_kib must be within 1-%d, got %d",
				maxRelayBufferKiB, c.General.RelayBufferKiB)
		}
		s.relayBufferKiB = c.General.RelayBufferKiB
	}
	return s, nil
}
