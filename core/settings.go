package core

import (
	"errors"
	"fmt"
	"time"

	"github.com/wwricu/bad-proxy-core/common"
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
	// direction for this long ("5m"). Unset (default) applies
	// common.DefaultRelayIdleTimeout; an explicit "0s" disables reclamation
	// entirely, so a legitimately idle long-lived tunnel is never cut.
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
	defaultHandshakeTimeout = common.DefaultHandshakeTimeout
	defaultDialTimeout      = common.DefaultDialTimeout
	defaultRelayIdle        = common.DefaultRelayIdleTimeout
	defaultRelayBufferKiB   = common.DefaultRelayBufferKiB
	maxRelayBufferKiB       = common.MaxRelayBufferKiB
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

// duration resolves one timeout field: unset keeps the default, and a negative
// window is refused here rather than armed as a deadline in the past.
func duration(name, raw string, def time.Duration) (time.Duration, error) {
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, errors.New("bad general." + name + " " + raw)
	}
	if d < 0 {
		return 0, errors.New("general." + name + " must not be negative")
	}
	return d, nil
}

// level resolves one log level field the same way.
func level(name, raw string, def logger.Level) (logger.Level, error) {
	if raw == "" {
		return def, nil
	}
	l, ok := logger.ParseLevel(raw)
	if !ok {
		return 0, errors.New("bad general." + name + " " + raw)
	}
	return l, nil
}

func (c Config) resolveSettings() (settings, error) {
	handshake, err := duration("handshake_timeout", c.General.HandshakeTimeout, defaultHandshakeTimeout)
	if err != nil {
		return settings{}, err
	}
	dial, err := duration("dial_timeout", c.General.DialTimeout, defaultDialTimeout)
	if err != nil {
		return settings{}, err
	}
	idle, err := duration("relay_idle_timeout", c.General.RelayIdleTimeout, defaultRelayIdle)
	if err != nil {
		return settings{}, err
	}
	file, err := level("log_level", c.General.LogLevel, logger.Debug)
	if err != nil {
		return settings{}, err
	}
	console, err := level("console_level", c.General.ConsoleLevel, logger.Info)
	if err != nil {
		return settings{}, err
	}
	buffer := defaultRelayBufferKiB
	if c.General.RelayBufferKiB != 0 { // 0 means unset, not "no buffer"
		if c.General.RelayBufferKiB < 1 || c.General.RelayBufferKiB > maxRelayBufferKiB {
			return settings{}, fmt.Errorf("general.relay_buffer_kib must be within 1-%d, got %d",
				maxRelayBufferKiB, c.General.RelayBufferKiB)
		}
		buffer = c.General.RelayBufferKiB
	}
	return settings{
		handshakeTimeout: handshake,
		dialTimeout:      dial,
		relayIdle:        idle,
		relayBufferKiB:   buffer,
		logFile:          c.General.LogFile,
		logLevel:         file,
		consoleLevel:     console,
	}, nil
}
