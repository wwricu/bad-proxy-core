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
	// direction for this long ("10m"). Unset (default) applies
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
	handshake, err := duration("handshake_timeout", c.General.HandshakeTimeout, common.DefaultHandshakeTimeout)
	if err != nil {
		return settings{}, err
	}
	if handshake <= 0 {
		return settings{}, errors.New("general.handshake_timeout must be positive")
	}
	dial, err := duration("dial_timeout", c.General.DialTimeout, common.DefaultDialTimeout)
	if err != nil {
		return settings{}, err
	}
	if dial <= 0 {
		return settings{}, errors.New("general.dial_timeout must be positive")
	}
	idle, err := duration("relay_idle_timeout", c.General.RelayIdleTimeout, common.DefaultRelayIdleTimeout)
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
	buffer := common.DefaultRelayBufferKiB
	if c.General.RelayBufferKiB != 0 { // 0 means unset, not "no buffer"
		if c.General.RelayBufferKiB < 1 || c.General.RelayBufferKiB > common.MaxRelayBufferKiB {
			return settings{}, fmt.Errorf("general.relay_buffer_kib must be within 1-%d, got %d",
				common.MaxRelayBufferKiB, c.General.RelayBufferKiB)
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
