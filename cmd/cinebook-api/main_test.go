package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/FreddieTheObserver/cinebook/internal/store"
)

const seedPath = "../../internal/store/seed/0001_one_cinema.sql"

var testDSN string

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:18",
		postgres.WithDatabase("cinebook"),
		postgres.WithUsername("cinebook"),
		postgres.WithPassword("cinebook"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres: %v\n", err)
		os.Exit(1)
	}
	testDSN, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "connection string: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	if err := testcontainers.TerminateContainer(container); err != nil {
		fmt.Fprintf(os.Stderr, "terminate: %v\n", err)
	}
	os.Exit(code)
}

type logLines struct {
	mu    sync.Mutex
	lines []map[string]any
}

func (l *logLines) has(msg string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if line["msg"] == msg {
			return true
		}
	}
	return false
}

// startService runs the binary's run function and returns its address once it
// is listening, and a channel that yields run's result.
func startService(t *testing.T, ctx context.Context, vars map[string]string) (string, *logLines, <-chan error) {
	t.Helper()

	pr, pw := io.Pipe()
	logs := &logLines{}
	listening := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			var line map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
				t.Errorf("log line is not JSON: %s", scanner.Bytes())
				continue
			}
			logs.mu.Lock()
			logs.lines = append(logs.lines, line)
			logs.mu.Unlock()
			if line["msg"] == "listening" {
				listening <- line["addr"].(string)
			}
		}
	}()

	result := make(chan error, 1)
	go func() {
		err := run(ctx, func(name string) string { return vars[name] }, pw)
		_ = pw.Close()
		result <- err
	}()

	select {
	case addr := <-listening:
		return "http://" + addr, logs, result
	case err := <-result:
		t.Fatalf("service exited before listening: %v", err)
	case <-time.After(time.Minute):
		t.Fatal("service did not start listening within a minute")
	}
	return "", nil, nil
}

type reply struct {
	status int
	header http.Header
	body   string
}

func do(method, url, body string, header map[string]string) (reply, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return reply{}, err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return reply{}, err
	}
	return reply{status: resp.StatusCode, header: resp.Header, body: string(b)}, nil
}

func call(t *testing.T, method, url, body string, header map[string]string) reply {
	t.Helper()
	r, err := do(method, url, body, header)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return r
}

func eventually(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRunRefusesAnInvalidConfiguration(t *testing.T) {
	err := run(t.Context(), func(string) string { return "" }, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "CINEBOOK_DSN") {
		t.Fatalf("got %v, want an error naming CINEBOOK_DSN", err)
	}
}

func TestServiceLifecycle(t *testing.T) {
	ctx, shutdown := context.WithCancel(context.Background())
	defer shutdown()

	base, logs, result := startService(t, ctx, map[string]string{
		"CINEBOOK_DSN":            testDSN,
		"CINEBOOK_ADDR":           "127.0.0.1:0",
		"CINEBOOK_DRAIN_DELAY":    "1s",
		"CINEBOOK_LOCK_TIMEOUT":   "15s",
		"CINEBOOK_HOLD_TTL":       "1s",
		"CINEBOOK_SWEEP_INTERVAL": "200ms",
	})

	// run has applied the migrations by now, so the fixture can go in.
	st, err := store.New(t.Context(), store.Config{DSN: testDSN})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()
	seed, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	if _, err := st.Pool().Exec(t.Context(), string(seed)); err != nil {
		t.Fatalf("apply seed: %v", err)
	}

	var showtimeID, seatA, seatB int64
	const pick = `
SELECT st.id, min(seat.id), max(seat.id)
  FROM showtimes st
  JOIN seats seat ON seat.auditorium_id = st.auditorium_id
 WHERE st.id = (SELECT min(id) FROM showtimes)
 GROUP BY st.id`
	if err := st.Pool().QueryRow(t.Context(), pick).Scan(&showtimeID, &seatA, &seatB); err != nil {
		t.Fatalf("pick seats: %v", err)
	}
	holdURL := fmt.Sprintf("%s/v1/showtimes/%d/holds", base, showtimeID)
	holdHeader := map[string]string{"Content-Type": "application/json", "X-Customer-Ref": "cust-a"}

	t.Run("probes and routes", func(t *testing.T) {
		if r := call(t, http.MethodGet, base+"/healthz", "", nil); r.status != http.StatusOK {
			t.Errorf("healthz: got %d", r.status)
		}
		if r := call(t, http.MethodGet, base+"/readyz", "", nil); r.status != http.StatusOK {
			t.Errorf("readyz: got %d %s", r.status, r.body)
		}
		if r := call(t, http.MethodGet, base+"/v1/movies", "", nil); r.status != http.StatusOK || !strings.Contains(r.body, `"movies"`) {
			t.Errorf("movies: got %d %s", r.status, r.body)
		}
		if r := call(t, http.MethodGet, base+"/v1/nothing-here", "", nil); r.status != http.StatusNotFound || r.header.Get("Content-Type") != "application/problem+json" {
			t.Errorf("unknown API path: got %d %q", r.status, r.header.Get("Content-Type"))
		}
		if r := call(t, http.MethodGet, base+"/", "", nil); r.status != http.StatusOK || !strings.Contains(r.body, `src="/js/app.js"`) {
			t.Errorf("UI: got %d %.80q", r.status, r.body)
		}
		if r := call(t, http.MethodGet, base+"/js/api.js", "", nil); r.status != http.StatusOK {
			t.Errorf("UI script: got %d", r.status)
		}

		metrics := call(t, http.MethodGet, base+"/metrics", "", nil).body
		for _, want := range []string{
			`cinebook_http_request_duration_seconds_count{route="GET /v1/movies",status="200"} 1`,
			`cinebook_db_pool_max_conns 16`,
			`cinebook_seat_race_lost_total 0`,
		} {
			if !strings.Contains(metrics, want+"\n") {
				t.Errorf("metrics are missing %q", want)
			}
		}
		if strings.Contains(metrics, `route="GET /healthz"`) {
			t.Error("probes are counted as API traffic")
		}
	})

	t.Run("sweeper reclaims a lapsed hold", func(t *testing.T) {
		body := fmt.Sprintf(`{"seat_ids": [%d]}`, seatB)
		if r := call(t, http.MethodPost, holdURL, body, holdHeader); r.status != http.StatusCreated {
			t.Fatalf("hold: got %d %s", r.status, r.body)
		}
		eventually(t, 10*time.Second, "the sweeper to reclaim the lapsed seat", func() bool {
			return strings.Contains(call(t, http.MethodGet, base+"/metrics", "", nil).body, "cinebook_sweeper_reclaimed_seats_total 1\n")
		})
	})

	t.Run("graceful shutdown finishes the request in flight", func(t *testing.T) {
		tx, err := st.Pool().Begin(t.Context())
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock($1)`, showtimeID); err != nil {
			t.Fatalf("take showtime lock: %v", err)
		}

		type outcome struct {
			reply
			err error
		}
		inFlight := make(chan outcome, 1)
		go func() {
			r, err := do(http.MethodPost, holdURL, fmt.Sprintf(`{"seat_ids": [%d]}`, seatA), holdHeader)
			inFlight <- outcome{r, err}
		}()
		eventually(t, 10*time.Second, "the hold to queue on the showtime lock", func() bool {
			var waiting int
			const q = `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`
			return st.Pool().QueryRow(t.Context(), q).Scan(&waiting) == nil && waiting > 0
		})

		shutdown()
		eventually(t, 5*time.Second, "readiness to report draining", func() bool {
			r := call(t, http.MethodGet, base+"/readyz", "", nil)
			return r.status == http.StatusServiceUnavailable && strings.TrimSpace(r.body) == "draining"
		})
		// Only once the listener is closed is the server really shutting down.
		// Releasing the lock earlier would let the hold finish during the drain
		// delay, and an abrupt Close would pass as well.
		eventually(t, 10*time.Second, "the listener to close", func() bool {
			_, err := do(http.MethodGet, base+"/healthz", "", nil)
			return err != nil
		})

		if err := tx.Rollback(t.Context()); err != nil {
			t.Fatalf("release showtime lock: %v", err)
		}

		select {
		case r := <-inFlight:
			if r.err != nil || r.status != http.StatusCreated {
				t.Fatalf("in-flight hold: got %d %s %v, want 201", r.status, r.body, r.err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("in-flight hold never completed")
		}

		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("run: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("service did not stop")
		}
		if !logs.has("stopped") {
			t.Fatal("no stopped log line")
		}
	})
}
