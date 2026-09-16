package obs

import (
	"io"
	"log/slog"
)

// NewLogger returns the service logger, writing JSON lines to w.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}
