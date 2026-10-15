package proxy

import (
	"encoding/json"
	"testing"
	"time"
)

func TestResolveSettingsDefaults(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"inbounds":[],"outbounds":[]}`), &c); err != nil {
		t.Fatal(err)
	}
	s, err := c.resolveSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.statsEvery != 0 {
		t.Errorf("stats should default off, got %v", s.statsEvery)
	}
	if s.handshakeTimeout != defaultHandshakeTimeout {
		t.Errorf("handshake default = %v, want %v", s.handshakeTimeout, defaultHandshakeTimeout)
	}
	if s.dialTimeout != defaultDialTimeout {
		t.Errorf("dial default = %v, want %v", s.dialTimeout, defaultDialTimeout)
	}
	if s.relayBufferKiB != defaultRelayBufferKiB {
		t.Errorf("buffer default = %d, want %d", s.relayBufferKiB, defaultRelayBufferKiB)
	}
}

func TestResolveSettingsFromJson(t *testing.T) {
	var c Config
	body := `{"general":{"stats":"30s","handshake_timeout":"5s","dial_timeout":"2s","relay_idle_timeout":"3m","relay_buffer_kib":128}}`
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	s, err := c.resolveSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.statsEvery != 30*time.Second || s.handshakeTimeout != 5*time.Second || s.dialTimeout != 2*time.Second {
		t.Errorf("duration parsing wrong: %+v", s)
	}
	if s.relayIdle != 3*time.Minute {
		t.Errorf("relay_idle_timeout = %v, want 3m", s.relayIdle)
	}
	if s.relayBufferKiB != 128 {
		t.Errorf("relay_buffer_kib = %d, want 128", s.relayBufferKiB)
	}
}

func TestResolveSettingsRejectsBadInput(t *testing.T) {
	cases := []string{
		`{"general":{"stats":"nope"}}`,
		`{"general":{"handshake_timeout":"3"}}`,
		`{"general":{"dial_timeout":"xx"}}`,
		`{"general":{"relay_idle_timeout":"-5s"}}`,
		`{"general":{"relay_buffer_kib":0}}`, // treated as unset, must be valid
		`{"general":{"relay_buffer_kib":2048}}`,
	}
	for i, body := range cases {
		var c Config
		if err := json.Unmarshal([]byte(body), &c); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		_, err := c.resolveSettings()
		wantErr := i != 4 // relay_buffer_kib 0 falls back to default, others must error
		if wantErr && err == nil {
			t.Errorf("case %d (%s): expected error, got none", i, body)
		}
		if !wantErr && err != nil {
			t.Errorf("case %d (%s): unexpected error %v", i, body, err)
		}
	}
}
