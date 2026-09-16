package config

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"
)

// Config is everything cinebook-api reads from its environment. A tuning field
// left at zero keeps the default of the layer that owns it, so each default is
// defined exactly once.
type Config struct {
	Addr            string
	DSN             string
	LogLevel        slog.Level
	DrainDelay      time.Duration
	ShutdownTimeout time.Duration

	DBMaxConns       int32
	LockTimeout      time.Duration
	StatementTimeout time.Duration

	HoldTTL         time.Duration
	MaxSeatsPerHold int

	RatePerSecond float64
	RateBurst     int

	SweepInterval time.Duration
	SweepBatch    int32
}

// Defaults for the settings that no other layer owns.
const (
	DefaultAddr            = ":8080"
	DefaultShutdownTimeout = 20 * time.Second
	DefaultSweepInterval   = 30 * time.Second
	DefaultSweepBatch      = 100
)

// Load reads the configuration through getenv and reports every invalid
// variable at once, rather than one per restart.
func Load(getenv func(string) string) (Config, error) {
	l := loader{getenv: getenv}
	cfg := Config{
		Addr:             l.text("CINEBOOK_ADDR", DefaultAddr),
		DSN:              l.text("CINEBOOK_DSN", ""),
		LogLevel:         l.level("CINEBOOK_LOG_LEVEL"),
		DrainDelay:       l.duration("CINEBOOK_DRAIN_DELAY", 0, true),
		ShutdownTimeout:  l.duration("CINEBOOK_SHUTDOWN_TIMEOUT", DefaultShutdownTimeout, false),
		DBMaxConns:       int32(l.integer("CINEBOOK_DB_MAX_CONNS", 0, 32)),
		LockTimeout:      l.duration("CINEBOOK_LOCK_TIMEOUT", 0, false),
		StatementTimeout: l.duration("CINEBOOK_STATEMENT_TIMEOUT", 0, false),
		HoldTTL:          l.duration("CINEBOOK_HOLD_TTL", 0, false),
		MaxSeatsPerHold:  int(l.integer("CINEBOOK_MAX_SEATS_PER_HOLD", 0, 32)),
		RatePerSecond:    l.number("CINEBOOK_RATE_PER_SECOND", 0),
		RateBurst:        int(l.integer("CINEBOOK_RATE_BURST", 0, 32)),
		SweepInterval:    l.duration("CINEBOOK_SWEEP_INTERVAL", DefaultSweepInterval, false),
		SweepBatch:       int32(l.integer("CINEBOOK_SWEEP_BATCH", DefaultSweepBatch, 32)),
	}
	if cfg.DSN == "" {
		l.errs = append(l.errs, errors.New("CINEBOOK_DSN is required"))
	}
	return cfg, errors.Join(l.errs...)
}

type loader struct {
	getenv func(string) string
	errs   []error
}

func (l *loader) fail(name, want, raw string) {
	l.errs = append(l.errs, fmt.Errorf("%s must be %s, got %q", name, want, raw))
}

func (l *loader) text(name, def string) string {
	if raw := l.getenv(name); raw != "" {
		return raw
	}
	return def
}

func (l *loader) duration(name string, def time.Duration, zeroAllowed bool) time.Duration {
	raw := l.getenv(name)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	switch {
	case zeroAllowed && (err != nil || d < 0):
		l.fail(name, "a duration such as 5s, or 0s", raw)
		return def
	case !zeroAllowed && (err != nil || d <= 0):
		l.fail(name, "a positive duration such as 30s", raw)
		return def
	}
	return d
}

func (l *loader) integer(name string, def int64, bits int) int64 {
	raw := l.getenv(name)
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, bits)
	if err != nil || n <= 0 {
		l.fail(name, "a positive integer", raw)
		return def
	}
	return n
}

func (l *loader) number(name string, def float64) float64 {
	raw := l.getenv(name)
	if raw == "" {
		return def
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || !(f > 0) || math.IsInf(f, 1) {
		l.fail(name, "a positive number", raw)
		return def
	}
	return f
}

func (l *loader) level(name string) slog.Level {
	raw := l.getenv(name)
	var level slog.Level
	if raw == "" {
		return level
	}
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		l.fail(name, "one of debug, info, warn or error", raw)
	}
	return level
}
