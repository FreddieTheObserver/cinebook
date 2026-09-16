package obs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMetricsExposition(t *testing.T) {
	// pgxpool connects lazily, so an unreachable address still yields stats.
	cfg, err := pgxpool.ParseConfig("postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.MaxConns = 7
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)

	m := NewMetrics(pool.Stat)
	m.ObserveRequest("GET /v1/movies", http.StatusOK, 3*time.Millisecond)
	m.ObserveRequest("GET /v1/movies", http.StatusOK, 4*time.Millisecond)
	m.ObserveRequest("unmatched", http.StatusNotFound, time.Millisecond)
	m.SeatRaceLost()
	m.ObserveSweep(3, nil)
	m.ObserveSweep(0, errors.New("lock timeout"))

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	for _, want := range []string{
		`cinebook_http_request_duration_seconds_count{route="GET /v1/movies",status="200"} 2`,
		`cinebook_http_request_duration_seconds_count{route="unmatched",status="404"} 1`,
		`cinebook_http_request_duration_seconds_bucket{route="GET /v1/movies",status="200",le="0.005"} 2`,
		`cinebook_seat_race_lost_total 1`,
		`cinebook_sweeper_runs_total{result="ok"} 1`,
		`cinebook_sweeper_runs_total{result="error"} 1`,
		`cinebook_sweeper_reclaimed_seats_total 3`,
		`cinebook_db_pool_max_conns 7`,
		`cinebook_db_pool_acquired_conns 0`,
		`cinebook_db_pool_empty_acquire_wait_seconds_total 0`,
	} {
		if !strings.Contains("\n"+body, "\n"+want+"\n") {
			t.Errorf("exposition is missing the line %q", want)
		}
	}
	if !strings.Contains(body, "\ngo_goroutines ") {
		t.Error("exposition is missing the Go runtime collector")
	}
}

func TestHealth(t *testing.T) {
	pingErr := error(nil)
	pinged := 0
	h := NewHealth(func(ctx context.Context) error {
		pinged++
		if _, ok := ctx.Deadline(); !ok {
			t.Error("readiness ping has no deadline")
		}
		return pingErr
	})

	probe := func(handler http.HandlerFunc) (int, string) {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		body, _ := io.ReadAll(rec.Body)
		return rec.Code, strings.TrimSpace(string(body))
	}

	if code, _ := probe(h.Ready); code != http.StatusOK {
		t.Fatalf("ready with a reachable database: got %d", code)
	}

	pingErr = errors.New("connection refused")
	if code, body := probe(h.Ready); code != http.StatusServiceUnavailable || body != "database unreachable" {
		t.Fatalf("ready with an unreachable database: got %d %q", code, body)
	}
	if code, _ := probe(h.Live); code != http.StatusOK {
		t.Fatalf("liveness must not depend on the database: got %d", code)
	}

	pingErr = nil
	h.Drain()
	before := pinged
	if code, body := probe(h.Ready); code != http.StatusServiceUnavailable || body != "draining" {
		t.Fatalf("ready while draining: got %d %q", code, body)
	}
	if pinged != before {
		t.Fatal("a draining replica still pinged the database")
	}
	if code, _ := probe(h.Live); code != http.StatusOK {
		t.Fatalf("live while draining: got %d", code)
	}
}
