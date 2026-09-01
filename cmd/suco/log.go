package main

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/sucopay/sucopay/internal/config"
)

// newLogger builds the one logger a run has. Nothing in internal/ reaches for
// a logger of its own: a library that logs where its caller cannot see is a
// library whose output nobody configured.
func newLogger(cfg config.Log, w io.Writer) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		return nil, fmt.Errorf("log.level: %q is not a level", cfg.Level)
	}
	options := &slog.HandlerOptions{Level: level}

	switch cfg.Format {
	case "json":
		return slog.New(slog.NewJSONHandler(w, options)), nil
	case "text":
		return slog.New(slog.NewTextHandler(w, options)), nil
	}
	return nil, fmt.Errorf("log.format: %q is not a format", cfg.Format)
}
