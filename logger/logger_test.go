package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]struct {
		want Level
		ok   bool
	}{
		"debug":   {Debug, true},
		"INFO":    {Info, true},
		"warn":    {Warn, true},
		"error":   {Error, true},
		" off ":   {Off, true},
		"":        {Debug, false},
		"verbose": {Debug, false},
	}
	for in, c := range cases {
		got, ok := ParseLevel(in)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseLevel(%q) = %v,%v want %v,%v", in, got, ok, c.want, c.ok)
		}
	}
}

// The file sink must receive every line at or above its threshold, carry the
// level tag, and report the real caller (this test file + function + line).
// The console is silenced with Off so the routing of each sink is isolated.
func TestFileSinkLevelsAndSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := Configure(path, Info, Off); err != nil {
		t.Fatal(err)
	}
	defer func() { Close(); Configure("", Info, Warn) }()

	Debugf("hidden debug %d", 1) // below fileAt=Info -> not written
	Infof("hello %s", "world")
	Errorf("boom %d", 42)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if strings.Contains(log, "hidden debug") {
		t.Error("Debugf leaked below the file threshold")
	}
	// line layout: "<time> [LEVEL] file:line func <message>"
	if !strings.Contains(log, "[INFO]") || !strings.Contains(log, "hello world") ||
		!strings.Contains(log, "[ERROR]") || !strings.Contains(log, "boom 42") {
		t.Fatalf("missing levelled lines:\n%s", log)
	}
	// source fields: this file, the enclosing test function, and a line number
	if !strings.Contains(log, "logger_test.go") || !strings.Contains(log, "TestFileSinkLevelsAndSource") {
		t.Fatalf("source (file/function) not reported:\n%s", log)
	}
}
