// Package logx mirrors build/common/logger.py: colored console output plus a
// lazily created per-run log file at <package-root>/logs/build_<ts>.log.
package logx

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/paths"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/ui"
)

var (
	// Out and Err are swappable for tests.
	Out io.Writer = os.Stdout
	Err io.Writer = os.Stderr

	mu      sync.Mutex
	logFile *os.File
	// fileEnabled is flipped by the CLI entry point; library and test use
	// stays console-only so `go test` never writes into <root>/logs/.
	fileEnabled bool
	// fileDisabled is set after a failed attempt so we don't retry per line.
	fileDisabled bool
)

// EnableFileLog turns on the per-run log file (called once by the CLI).
func EnableFileLog() {
	mu.Lock()
	defer mu.Unlock()
	fileEnabled = true
}

func ensureLogFile() *os.File {
	if !fileEnabled || fileDisabled {
		return nil
	}
	if logFile != nil {
		return logFile
	}
	root, err := paths.Root()
	if err != nil {
		// Outside a repo checkout (e.g. --help from $HOME): console-only.
		fileDisabled = true
		return nil
	}
	logDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		fileDisabled = true
		return nil
	}
	now := time.Now()
	path := filepath.Join(logDir, fmt.Sprintf("build_%s.log", now.Format("2006-01-02_15-04-05")))
	f, err := os.Create(path)
	if err != nil {
		fileDisabled = true
		return nil
	}
	fmt.Fprintf(f, "BrowserOS Build Log - Started at %s\n", now.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(f, "%s\n\n", repeat("=", 80))
	logFile = f
	return logFile
}

func repeat(s string, n int) string {
	out := ""
	for range n {
		out += s
	}
	return out
}

// ToFile writes a raw line to the log file only (used for subprocess output).
func ToFile(message string) {
	mu.Lock()
	defer mu.Unlock()
	if f := ensureLogFile(); f != nil {
		fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), message)
	}
}

// Info prints a plain message.
func Info(message string) {
	fmt.Fprintln(Out, message)
	ToFile("INFO: " + message)
}

// Warning prints a yellow warning.
func Warning(message string) {
	fmt.Fprintln(Out, ui.Warning("⚠️  "+message))
	ToFile("WARNING: " + message)
}

// Error prints a red error to stderr.
func Error(message string) {
	fmt.Fprintln(Err, ui.Error("❌ "+message))
	ToFile("ERROR: " + message)
}

// Success prints a green success message.
func Success(message string) {
	fmt.Fprintln(Out, ui.Success("✅ "+message))
	ToFile("SUCCESS: " + message)
}

// Debug prints a dim debug message when enabled.
func Debug(message string, enabled bool) {
	if !enabled {
		return
	}
	fmt.Fprintln(Out, ui.Muted("🔍 "+message))
	ToFile("DEBUG: " + message)
}

// Close closes the log file if open.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if logFile != nil {
		logFile.Close()
		logFile = nil
	}
}
