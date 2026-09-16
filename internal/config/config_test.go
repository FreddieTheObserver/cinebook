package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"CINEBOOK_DSN": "postgres://localhost/cinebook"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	want := Config{
		Addr:            DefaultAddr,
		DSN:             "postgres://localhost/cinebook",
		LogLevel:        slog.LevelInfo,
		ShutdownTimeout: DefaultShutdownTimeout,
		SweepInterval:   DefaultSweepInterval,
		SweepBatch:      DefaultSweepBatch,
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadReadsEveryVariable(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"CINEBOOK_ADDR":               "127.0.0.1:9000",
		"CINEBOOK_DSN":                "postgres://db/cinebook",
		"CINEBOOK_LOG_LEVEL":          "debug",
		"CINEBOOK_DRAIN_DELAY":        "0s",
		"CINEBOOK_SHUTDOWN_TIMEOUT":   "45s",
		"CINEBOOK_DB_MAX_CONNS":       "32",
		"CINEBOOK_LOCK_TIMEOUT":       "750ms",
		"CINEBOOK_STATEMENT_TIMEOUT":  "3s",
		"CINEBOOK_HOLD_TTL":           "15m",
		"CINEBOOK_MAX_SEATS_PER_HOLD": "6",
		"CINEBOOK_RATE_PER_SECOND":    "2.5",
		"CINEBOOK_RATE_BURST":         "8",
		"CINEBOOK_SWEEP_INTERVAL":     "1m",
		"CINEBOOK_SWEEP_BATCH":        "250",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	want := Config{
		Addr:             "127.0.0.1:9000",
		DSN:              "postgres://db/cinebook",
		LogLevel:         slog.LevelDebug,
		DrainDelay:       0,
		ShutdownTimeout:  45 * time.Second,
		DBMaxConns:       32,
		LockTimeout:      750 * time.Millisecond,
		StatementTimeout: 3 * time.Second,
		HoldTTL:          15 * time.Minute,
		MaxSeatsPerHold:  6,
		RatePerSecond:    2.5,
		RateBurst:        8,
		SweepInterval:    time.Minute,
		SweepBatch:       250,
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	_, err := Load(env(map[string]string{
		"CINEBOOK_LOG_LEVEL":          "loud",
		"CINEBOOK_DRAIN_DELAY":        "-1s",
		"CINEBOOK_LOCK_TIMEOUT":       "0s",
		"CINEBOOK_HOLD_TTL":           "7",
		"CINEBOOK_DB_MAX_CONNS":       "3000000000",
		"CINEBOOK_MAX_SEATS_PER_HOLD": "-2",
		"CINEBOOK_RATE_PER_SECOND":    "NaN",
		"CINEBOOK_RATE_BURST":         "Inf",
	}))
	if err == nil {
		t.Fatal("invalid configuration accepted")
	}

	for _, name := range []string{
		"CINEBOOK_DSN",
		"CINEBOOK_LOG_LEVEL",
		"CINEBOOK_DRAIN_DELAY",
		"CINEBOOK_LOCK_TIMEOUT",
		"CINEBOOK_HOLD_TTL",
		"CINEBOOK_DB_MAX_CONNS",
		"CINEBOOK_MAX_SEATS_PER_HOLD",
		"CINEBOOK_RATE_PER_SECOND",
		"CINEBOOK_RATE_BURST",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not mention %s:\n%v", name, err)
		}
	}
}

func TestLoadRejectsAnInfiniteRate(t *testing.T) {
	_, err := Load(env(map[string]string{"CINEBOOK_DSN": "postgres://db/cinebook", "CINEBOOK_RATE_PER_SECOND": "+Inf"}))
	if err == nil || !strings.Contains(err.Error(), "CINEBOOK_RATE_PER_SECOND") {
		t.Fatalf("got %v, want an error naming CINEBOOK_RATE_PER_SECOND", err)
	}
}
