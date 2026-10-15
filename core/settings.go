package core

import (
	"errors"
	"fmt"
	"time"

	"github.com/wwricu/bad-proxy-core/logger"
)

// GeneralConfig is the "general" object in the config file: process-wide
// settings that belong to no inbound, outbound, or router.
type GeneralConfig struct {
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
	// LogFile is the path the detailed log is appended to. Empty (default)
	// means console-only.
	LogFile string `json:"log_file"`
	// LogLevel is the minimum level written to LogFile ("debug","info","warn",
	// "error","off"); default "debug".
	LogLevel string `json:"log_level"`
	// ConsoleLevel is the minimum level echoed to stdout; default "info". Raise
	// it to "warn"/"error" to keep stdout to only the critical lines.
	ConsoleLevel string `json:"console_level"`
}

const (
	defaultHandshakeTimeout = 10 * time.Second
	defaultDialTimeout      = 15 * time.Second
	defaultRelayBufferKiB   = 64
	maxRelayBufferKiB       = 1024
)

// settings is GeneralConfig after validation, with defaults applied.
type settings struct {
	handshakeTimeout time.Duration
	dialTimeout      time.Duration
	relayIdle        time.Duration
	relayBufferKiB   int
	logFile          string
	logLevel         logger.Level
	consoleLevel     logger.Level
}

func (c Config) resolveSettings() (settings, error) {
	s := settings{
		handshakeTimeout: defaultHandshakeTimeout,
		dialTimeout:      defaultDialTimeout,
		relayBufferKiB:   defaultRelayBufferKiB,
		logLevel:         logger.Debug,
		consoleLevel:     logger.Info,
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
	if c.General.LogLevel != "" {
		l, ok := logger.ParseLevel(c.General.LogLevel)
		if !ok {
			return settings{}, errors.New("bad general.log_level " + c.General.LogLevel)
		}
		s.logLevel = l
	}
	if c.General.ConsoleLevel != "" {
		l, ok := logger.ParseLevel(c.General.ConsoleLevel)
		if !ok {
			return settings{}, errors.New("bad general.console_level " + c.General.ConsoleLevel)
		}
		s.consoleLevel = l
	}
	s.logFile = c.General.LogFile
	return s, nil
}
