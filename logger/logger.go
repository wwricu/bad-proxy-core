// Package logger is the engine's unified logging. Every line carries the
// timestamp, the caller's file:line, and the function name, and is filtered by
// level into two sinks: a log file that captures everything, and stdout that
// keeps only the higher-severity lines you want to watch live.
//
// The timestamp comes from the standard library log package; the source is
// added explicitly via runtime.Callers, because that package has no func-name
// flag and its Lshortfile would report these wrappers rather than the real
// call site.
package logger

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
)

type Level int

const (
	Debug Level = iota
	Info
	Warn
	Error
	// Off disables a sink entirely; nothing reaches it.
	Off Level = 100
)

// ParseLevel maps a config string to a Level. Unrecognised or empty input
// reports ok=false so the caller can fall back to a default.
func ParseLevel(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return Debug, true
	case "info":
		return Info, true
	case "warn", "warning":
		return Warn, true
	case "error":
		return Error, true
	case "off", "none":
		return Off, true
	}
	return Debug, false
}

func (l Level) String() string {
	switch l {
	case Debug:
		return "DEBUG"
	case Info:
		return "INFO"
	case Warn:
		return "WARN"
	case Error:
		return "ERROR"
	case Off:
		return "OFF"
	}
	return "INFO"
}

const flags = log.Ldate | log.Ltime | log.Lmicroseconds

var (
	// The console sink is always available; before Configure it stands alone.
	console   = log.New(os.Stdout, "", flags)
	consoleAt = Info

	file       *log.Logger // nil until Configure opens a path
	fileAt     = Debug
	fileHandle *os.File
)

// Configure sets the two level thresholds and (optionally) opens a log file.
// fileLevel gates the file sink, consoleLevel gates stdout; pass logger.Off to
// silence a sink, "" for the path to keep console-only. An error opening the
// file is returned so the caller can fall back to console-only logging.
func Configure(path string, fileLevel, consoleLevel Level) error {
	fileAt, consoleAt = fileLevel, consoleLevel
	if path == "" {
		Close()
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	Close() // release any previous file
	fileHandle = f
	file = log.New(f, "", flags)
	return nil
}

// Close releases the log file handle, if one is open. Safe to call repeatedly.
func Close() {
	if fileHandle != nil {
		_ = fileHandle.Close()
		fileHandle = nil
	}
	file = nil
}

// sourceFor renders "file:line func" for the real call site: three frames
// above sourceFor (emit, the Debugf/Infof/... wrapper, then the caller).
func sourceFor() string {
	var pcs [1]uintptr
	// 0=runtime.Callers, 1=sourceFor, 2=emit, 3=Debugf/..., 4=real caller
	if runtime.Callers(4, pcs[:]) == 0 {
		return "? ?:?"
	}
	f, _ := runtime.CallersFrames(pcs[:]).Next()
	file := f.File
	if i := strings.LastIndexByte(file, '/'); i >= 0 {
		file = file[i+1:]
	}
	fn := "?"
	if s := f.Function; s != "" {
		if i := strings.LastIndexByte(s, '/'); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.IndexByte(s, '.'); j >= 0 { // drop package qualifier
			s = s[j+1:]
		}
		fn = s
	}
	return fmt.Sprintf("%s:%d %s", file, f.Line, fn)
}

// emit writes one levelled line to whichever sinks accept it.
func emit(l Level, tag, format string, args ...any) {
	wantFile := file != nil && l >= fileAt
	if !wantFile && l < consoleAt {
		return // neither sink wants this level
	}
	msg := tag + " " + sourceFor() + " " + fmt.Sprintf(format, args...)
	if wantFile {
		file.Output(2, msg)
	}
	if l >= consoleAt {
		console.Output(2, msg)
	}
}

func Debugf(format string, args ...any) { emit(Debug, "[DEBUG]", format, args...) }
func Infof(format string, args ...any)  { emit(Info, "[INFO]", format, args...) }
func Warnf(format string, args ...any)  { emit(Warn, "[WARN]", format, args...) }
func Errorf(format string, args ...any) { emit(Error, "[ERROR]", format, args...) }
