package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/lmittmann/tint"
)

// New creates a structured logger with the specified format and level, writing to os.Stderr.
// Use NewWithWriter to inject a custom writer (e.g., for testing).
func New(format, level string) (*slog.Logger, error) {
	return NewWithWriter(format, level, os.Stderr)
}

// NewWithWriter creates a structured logger with the specified format, level, and custom writer.
// format must be "logfmt" or "json"; level must be "debug", "info", "warn", or "error" (case-insensitive).
func NewWithWriter(format, level string, w io.Writer) (*slog.Logger, error) {
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, err
	}
	var handler slog.Handler
	switch format {
	case "logfmt":
		handler = tint.NewHandler(w, &tint.Options{
			Level:      lvl,
			TimeFormat: time.RFC3339,
		})
	case "json":
		handler = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	default:
		return nil, fmt.Errorf("invalid log format %q: must be logfmt or json", format)
	}
	return slog.New(handler), nil
}

func parseLevel(level string) (slog.Level, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return 0, fmt.Errorf("invalid log level %q: must be debug, info, warn, or error", level)
	}
	return lvl, nil
}
