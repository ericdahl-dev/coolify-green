// Package logx emits debug logging when COOLIFY_GREEN_DEBUG is set. The TUI
// owns stdout, so anything noisy has to be opt-in and go to stderr.
package logx

import (
	"os"
	"strings"

	"github.com/charmbracelet/log"
)

var debug *log.Logger

func init() {
	if strings.TrimSpace(os.Getenv("COOLIFY_GREEN_DEBUG")) == "" {
		return
	}
	debug = log.NewWithOptions(os.Stderr, log.Options{
		Level:           log.DebugLevel,
		ReportTimestamp: true,
		Prefix:          "coolify-green",
	})
}

// Debug emits a debug log line when COOLIFY_GREEN_DEBUG is non-empty.
func Debug(msg string, kv ...any) {
	if debug != nil {
		debug.Debug(msg, kv...)
	}
}
