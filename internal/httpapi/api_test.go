package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/FreddieTheObserver/cinebook/internal/booking"
	"github.com/FreddieTheObserver/cinebook/internal/store"
)

const seedPath = "../store/seed/0001_one_cinema.sql"

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
	if err := setupSchema(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "setup: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	if err := testcontainers.TerminateContainer(container); err != nil {
		fmt.Fprintf(os.Stderr, "terminate: %v\n", err)
	}
	os.Exit(code)
}

func setupSchema(ctx context.Context) error {
	s, err := store.New(ctx, store.Config{DSN: testDSN})
	if err != nil {
		return err
	}
	defer s.Close()

	if err := s.Migrate(ctx); err != nil {
		return err
	}
	seed, err := os.ReadFile(seedPath)
	if err != nil {
		return fmt.Errorf("read seed: %w", err)
	}
	if _, err := s.Pool().Exec(ctx, string(seed)); err != nil {
		return fmt.Errorf("apply seed: %w", err)
	}
	return nil
}

type options struct {
	ttl         time.Duration
	lockTimeout time.Duration
	maxConns    int32
	rate        float64
	burst       int
}

type env struct {
	server   *httptest.Server
	store    *store.Store
	logs     *logSink
	observed *observations
}

func newEnv(t *testing.T, opt options) *env {
	t.Helper()

	if opt.ttl == 0 {
		opt.ttl = time.Minute
	}
	if opt.lockTimeout == 0 {
		opt.lockTimeout = 2 * time.Second
	}
	if opt.maxConns == 0 {
		opt.maxConns = 8
	}

	st, err := store.New(t.Context(), store.Config{
		DSN:              testDSN,
		MaxConns:         opt.maxConns,
		LockTimeout:      opt.lockTimeout,
		StatementTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(st.Close)

	const reset = `TRUNCATE bookings, seat_occupancy, holds RESTART IDENTITY CASCADE`
	if _, err := st.Pool().Exec(t.Context(), reset); err != nil {
		t.Fatalf("reset booking state: %v", err)
	}

	logs, observed := &logSink{}, &observations{}
	svc := booking.New(st, booking.Config{HoldTTL: opt.ttl})
	handler := New(svc, slog.New(logs), observed, Config{RatePerSecond: opt.rate, RateBurst: opt.burst})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &env{server: server, store: st, logs: logs, observed: observed}
}

type observations struct {
	mu       sync.Mutex
	routes   []string
	raceLost int
}

func (o *observations) ObserveRequest(route string, _ int, _ time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.routes = append(o.routes, route)
}

func (o *observations) SeatRaceLost() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.raceLost++
}

func (o *observations) snapshot() ([]string, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.routes), o.raceLost
}

type logSink struct {
	mu      sync.Mutex
	records []slog.Record
}

func (s *logSink) Enabled(context.Context, slog.Level) bool { return true }

func (s *logSink) Handle(_ context.Context, r slog.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r.Clone())
	return nil
}

func (s *logSink) WithAttrs([]slog.Attr) slog.Handler { return s }
func (s *logSink) WithGroup(string) slog.Handler      { return s }

func (s *logSink) snapshot() []slog.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.records)
}

func (s *logSink) errorMessages() []string {
	var out []string
	for _, r := range s.snapshot() {
		if r.Level >= slog.LevelError {
			out = append(out, r.Message)
		}
	}
	return out
}

type response struct {
	status int
	header http.Header
	body   []byte
	close  bool
}

func (e *env) newRequest(t *testing.T, method, path, body string) *http.Request {
	t.Helper()

	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, e.server.URL+path, rd)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func (e *env) send(t *testing.T, req *http.Request) response {
	t.Helper()

	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: body, close: resp.Close}
}

func (e *env) get(t *testing.T, path string) response {
	t.Helper()
	return e.send(t, e.newRequest(t, http.MethodGet, path, ""))
}

func (e *env) hold(t *testing.T, showtimeID int64, customer string, seats ...int64) response {
	t.Helper()
	return e.send(t, e.holdRequest(t, showtimeID, customer, seats...))
}

func (e *env) holdRequest(t *testing.T, showtimeID int64, customer string, seats ...int64) *http.Request {
	t.Helper()

	body, err := json.Marshal(holdRequest{SeatIDs: seats})
	if err != nil {
		t.Fatalf("encode hold request: %v", err)
	}
	req := e.newRequest(t, http.MethodPost, fmt.Sprintf("/v1/showtimes/%d/holds", showtimeID), string(body))
	req.Header.Set(headerCustomerRef, customer)
	return req
}

func (e *env) confirm(t *testing.T, token, key string) response {
	t.Helper()

	req := e.newRequest(t, http.MethodPost, "/v1/holds/"+token+"/confirm", "")
	req.Header.Set(headerIdempotencyKey, key)
	return e.send(t, req)
}

func expectStatus(t *testing.T, r response, status int) response {
	t.Helper()
	if r.status != status {
		t.Fatalf("got %d %s, want %d", r.status, r.body, status)
	}
	return r
}

func expectProblem(t *testing.T, r response, status int, slug string) problemBody {
	t.Helper()

	expectStatus(t, r, status)
	if ct := r.header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("got Content-Type %q, want application/problem+json", ct)
	}
	p := decode[problemBody](t, r)
	if p.Type != problemTypeURI(slug) || p.Status != status {
		t.Fatalf("got problem %+v, want %s with status %d", p, slug, status)
	}
	return p
}

func decode[T any](t *testing.T, r response) T {
	t.Helper()

	var v T
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
	return v
}

func showtimeSeats(t *testing.T, st *store.Store, n int32) (int64, []int64) {
	t.Helper()

	const q = `
SELECT st.id,
       (SELECT array_agg(picked.id)
          FROM (SELECT seat.id FROM seats seat
                 WHERE seat.auditorium_id = st.auditorium_id
                 ORDER BY seat.id LIMIT $1) picked)
  FROM showtimes st
 ORDER BY st.id
 LIMIT 1`

	var showtimeID int64
	var seatIDs []int64
	if err := st.Pool().QueryRow(t.Context(), q, n).Scan(&showtimeID, &seatIDs); err != nil {
		t.Fatalf("pick showtime: %v", err)
	}
	return showtimeID, seatIDs
}

func countRows(t *testing.T, st *store.Store, query string, args ...any) int64 {
	t.Helper()

	var n int64
	if err := st.Pool().QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func seatStatuses(m seatMapJSON) map[int64]string {
	out := map[int64]string{}
	for _, row := range m.Rows {
		for _, s := range row.Seats {
			out[s.ID] = s.Status
		}
	}
	return out
}

func TestBookingJourney(t *testing.T) {
	e := newEnv(t, options{})
	showtimeID, seats := showtimeSeats(t, e.store, 2)

	movies := decode[struct {
		Movies []movieJSON `json:"movies"`
	}](t, expectStatus(t, e.get(t, "/v1/movies"), http.StatusOK))
	if len(movies.Movies) == 0 {
		t.Fatal("no movies listed")
	}

	movieID := movies.Movies[0].ID
	showtimes := decode[struct {
		Showtimes []showtimeJSON `json:"showtimes"`
	}](t, expectStatus(t, e.get(t, fmt.Sprintf("/v1/showtimes?movie_id=%d", movieID)), http.StatusOK))
	if len(showtimes.Showtimes) == 0 {
		t.Fatalf("no showtimes listed for movie %d", movieID)
	}
	for _, s := range showtimes.Showtimes {
		if s.MovieID != movieID {
			t.Fatalf("showtime %d is for movie %d, want %d", s.ID, s.MovieID, movieID)
		}
	}

	seatMapPath := fmt.Sprintf("/v1/showtimes/%d/seats", showtimeID)
	before := decode[seatMapJSON](t, expectStatus(t, e.get(t, seatMapPath), http.StatusOK))
	for _, id := range seats {
		if got := seatStatuses(before)[id]; got != "free" {
			t.Fatalf("seat %d starts %q, want free", id, got)
		}
	}

	created := expectStatus(t, e.hold(t, showtimeID, "cust-a", seats...), http.StatusCreated)
	held := decode[holdJSON](t, created)
	if loc := created.header.Get("Location"); loc != "/v1/holds/"+held.Token {
		t.Fatalf("got Location %q for hold %q", loc, held.Token)
	}
	if held.Status != "live" || len(held.Seats) != 2 || held.TotalMinor != 2*before.PriceMinor {
		t.Fatalf("unexpected hold: %+v", held)
	}

	fetched := decode[holdJSON](t, expectStatus(t, e.get(t, created.header.Get("Location")), http.StatusOK))
	if fetched.Token != held.Token || fetched.Status != "live" {
		t.Fatalf("fetched hold differs: %+v", fetched)
	}

	confirmed := expectStatus(t, e.confirm(t, held.Token, "key-1"), http.StatusCreated)
	booked := decode[bookingJSON](t, confirmed)
	if loc := confirmed.header.Get("Location"); loc != "/v1/bookings/"+booked.Ref {
		t.Fatalf("got Location %q for booking %q", loc, booked.Ref)
	}
	if booked.TotalMinor != held.TotalMinor || len(booked.Seats) != 2 {
		t.Fatalf("booking does not match the hold: %+v", booked)
	}

	replayed := decode[bookingJSON](t, expectStatus(t, e.confirm(t, held.Token, "key-1"), http.StatusCreated))
	if replayed.Ref != booked.Ref {
		t.Fatalf("replay produced %q, want %q", replayed.Ref, booked.Ref)
	}

	found := decode[bookingJSON](t, expectStatus(t, e.get(t, confirmed.header.Get("Location")), http.StatusOK))
	if found.Ref != booked.Ref {
		t.Fatalf("got booking %q, want %q", found.Ref, booked.Ref)
	}

	after := decode[seatMapJSON](t, expectStatus(t, e.get(t, seatMapPath), http.StatusOK))
	for _, id := range seats {
		if got := seatStatuses(after)[id]; got != "sold" {
			t.Fatalf("seat %d ends %q, want sold", id, got)
		}
	}

	release := e.send(t, e.newRequest(t, http.MethodDelete, "/v1/holds/"+held.Token, ""))
	expectProblem(t, release, http.StatusConflict, "hold-confirmed")
}

// Encoding and decoding with the same struct would hide a wrong tag, so the
// field names are pinned against raw JSON.
func TestWireShapes(t *testing.T) {
	e := newEnv(t, options{})
	showtimeID, seats := showtimeSeats(t, e.store, 1)

	keys := func(t *testing.T, raw []byte) []string {
		t.Helper()
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return slices.Sorted(maps.Keys(obj))
	}
	field := func(t *testing.T, raw []byte, name string) []byte {
		t.Helper()
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return obj[name]
	}
	first := func(t *testing.T, raw []byte) []byte {
		t.Helper()
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
			t.Fatalf("want a non-empty array, got %s", raw)
		}
		return arr[0]
	}
	expect := func(t *testing.T, what string, raw []byte, want ...string) {
		t.Helper()
		slices.Sort(want)
		if got := keys(t, raw); !slices.Equal(got, want) {
			t.Errorf("%s: got keys %v, want %v", what, got, want)
		}
	}

	movies := expectStatus(t, e.get(t, "/v1/movies"), http.StatusOK).body
	expect(t, "movie", first(t, field(t, movies, "movies")), "id", "title", "runtime_min", "rating")

	showtimes := expectStatus(t, e.get(t, "/v1/showtimes"), http.StatusOK).body
	showtime := first(t, field(t, showtimes, "showtimes"))
	expect(t, "showtime", showtime,
		"id", "movie_id", "movie_title", "auditorium_id", "auditorium_name",
		"starts_at", "ends_at", "price_minor", "currency", "sales_open")
	if starts := string(field(t, showtime, "starts_at")); !strings.HasSuffix(starts, `Z"`) {
		t.Errorf("starts_at %s is not UTC", starts)
	}

	seatMap := expectStatus(t, e.get(t, fmt.Sprintf("/v1/showtimes/%d/seats", showtimeID)), http.StatusOK).body
	expect(t, "seat map", seatMap, "showtime_id", "price_minor", "currency", "rows")
	row := first(t, field(t, seatMap, "rows"))
	expect(t, "seat row", row, "label", "seats")
	expect(t, "seat map seat", first(t, field(t, row, "seats")), "id", "number", "kind", "status")

	hold := expectStatus(t, e.hold(t, showtimeID, "cust-a", seats...), http.StatusCreated).body
	expect(t, "hold", hold, "token", "showtime_id", "status", "seats", "expires_at", "total_minor", "currency")
	expect(t, "hold seat", first(t, field(t, hold, "seats")), "id", "row", "number", "kind")

	var token string
	if err := json.Unmarshal(field(t, hold, "token"), &token); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	booked := expectStatus(t, e.confirm(t, token, "key-1"), http.StatusCreated).body
	expect(t, "booking", booked, "ref", "showtime_id", "seats", "total_minor", "currency", "created_at")

	conflict := e.hold(t, showtimeID, "cust-b", seats...)
	expectStatus(t, conflict, http.StatusConflict)
	expect(t, "problem", conflict.body, "type", "title", "status", "detail", "seat_ids")
	if got := string(field(t, conflict.body, "type")); got != `"/problems/seat-unavailable"` {
		t.Errorf("got problem type %s", got)
	}
}

func TestHoldConflictNamesTheBlockingSeats(t *testing.T) {
	e := newEnv(t, options{})
	showtimeID, seats := showtimeSeats(t, e.store, 3)

	expectStatus(t, e.hold(t, showtimeID, "cust-a", seats[:2]...), http.StatusCreated)

	p := expectProblem(t, e.hold(t, showtimeID, "cust-b", seats...), http.StatusConflict, "seat-unavailable")
	if !slices.Equal(p.SeatIDs, seats[:2]) {
		t.Fatalf("got seat_ids %v, want %v", p.SeatIDs, seats[:2])
	}
}

func TestHoldRejectsInvalidRequests(t *testing.T) {
	e := newEnv(t, options{})
	showtimeID, seats := showtimeSeats(t, e.store, 11)

	const foreign = `
SELECT seat.id FROM seats seat
 WHERE seat.auditorium_id <> (SELECT auditorium_id FROM showtimes WHERE id = $1)
 ORDER BY seat.id LIMIT 1`
	var foreignSeat int64
	if err := e.store.Pool().QueryRow(t.Context(), foreign, showtimeID).Scan(&foreignSeat); err != nil {
		t.Fatalf("pick foreign seat: %v", err)
	}

	tests := []struct {
		name   string
		req    func() *http.Request
		status int
		slug   string
	}{
		{
			name: "no customer",
			req: func() *http.Request {
				req := e.holdRequest(t, showtimeID, "cust-a", seats[0])
				req.Header.Del(headerCustomerRef)
				return req
			},
			status: http.StatusBadRequest, slug: "invalid-request",
		},
		{
			name:   "customer with a space",
			req:    func() *http.Request { return e.holdRequest(t, showtimeID, "cust a", seats[0]) },
			status: http.StatusBadRequest, slug: "invalid-request",
		},
		{
			name: "not JSON",
			req: func() *http.Request {
				req := e.holdRequest(t, showtimeID, "cust-a", seats[0])
				req.Header.Set("Content-Type", "text/plain")
				return req
			},
			status: http.StatusUnsupportedMediaType, slug: "unsupported-media-type",
		},
		{
			name: "unknown field",
			req: func() *http.Request {
				req := e.newRequest(t, http.MethodPost, fmt.Sprintf("/v1/showtimes/%d/holds", showtimeID), `{"seat_id": [1]}`)
				req.Header.Set(headerCustomerRef, "cust-a")
				return req
			},
			status: http.StatusBadRequest, slug: "invalid-request",
		},
		{
			name:   "no seats",
			req:    func() *http.Request { return e.holdRequest(t, showtimeID, "cust-a") },
			status: http.StatusUnprocessableEntity, slug: "invalid-selection",
		},
		{
			name:   "too many seats",
			req:    func() *http.Request { return e.holdRequest(t, showtimeID, "cust-a", seats...) },
			status: http.StatusUnprocessableEntity, slug: "invalid-selection",
		},
		{
			name:   "seat from another auditorium",
			req:    func() *http.Request { return e.holdRequest(t, showtimeID, "cust-a", seats[0], foreignSeat) },
			status: http.StatusUnprocessableEntity, slug: "invalid-selection",
		},
		{
			name:   "unknown showtime",
			req:    func() *http.Request { return e.holdRequest(t, 1<<40, "cust-a", seats[0]) },
			status: http.StatusNotFound, slug: "not-found",
		},
		{
			name: "malformed showtime id",
			req: func() *http.Request {
				req := e.holdRequest(t, showtimeID, "cust-a", seats[0])
				req.URL.Path = "/v1/showtimes/abc/holds"
				return req
			},
			status: http.StatusNotFound, slug: "not-found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expectProblem(t, e.send(t, tc.req()), tc.status, tc.slug)
		})
	}

	if held := countRows(t, e.store, `SELECT count(*) FROM holds`); held != 0 {
		t.Fatalf("rejected requests created %d holds", held)
	}
}

func TestOversizedBodyClosesTheConnection(t *testing.T) {
	e := newEnv(t, options{})
	showtimeID, seats := showtimeSeats(t, e.store, 1)

	body := fmt.Sprintf(`{"seat_ids": [%d]}`, seats[0]) + strings.Repeat(" ", 4*maxBodyBytes)
	req := e.newRequest(t, http.MethodPost, fmt.Sprintf("/v1/showtimes/%d/holds", showtimeID), body)
	req.Header.Set(headerCustomerRef, "cust-a")

	resp := e.send(t, req)
	expectProblem(t, resp, http.StatusRequestEntityTooLarge, "request-too-large")
	// The client strips Connection: close from the header and reports it here.
	if !resp.close {
		t.Fatal("connection kept alive, so the server would go on reading the rest of the body")
	}
}

func TestHoldWhenSalesAreClosed(t *testing.T) {
	e := newEnv(t, options{})
	showtimeID, seats := showtimeSeats(t, e.store, 1)

	const setSales = `UPDATE showtimes SET sales_open = $2 WHERE id = $1`
	if _, err := e.store.Pool().Exec(t.Context(), setSales, showtimeID, false); err != nil {
		t.Fatalf("close sales: %v", err)
	}
	t.Cleanup(func() {
		if _, err := e.store.Pool().Exec(context.Background(), setSales, showtimeID, true); err != nil {
			t.Errorf("reopen sales: %v", err)
		}
	})

	expectProblem(t, e.hold(t, showtimeID, "cust-a", seats...), http.StatusConflict, "sales-closed")
}

func TestReleaseFreesSeats(t *testing.T) {
	e := newEnv(t, options{})
	showtimeID, seats := showtimeSeats(t, e.store, 2)

	held := decode[holdJSON](t, expectStatus(t, e.hold(t, showtimeID, "cust-a", seats...), http.StatusCreated))

	for range 2 {
		release := e.send(t, e.newRequest(t, http.MethodDelete, "/v1/holds/"+held.Token, ""))
		expectStatus(t, release, http.StatusNoContent)
	}

	released := decode[holdJSON](t, expectStatus(t, e.get(t, "/v1/holds/"+held.Token), http.StatusOK))
	if released.Status != "released" {
		t.Fatalf("got status %q, want released", released.Status)
	}
	expectStatus(t, e.hold(t, showtimeID, "cust-b", seats...), http.StatusCreated)
}

func TestConfirmRejectsInvalidRequests(t *testing.T) {
	e := newEnv(t, options{})
	showtimeID, seats := showtimeSeats(t, e.store, 2)

	first := decode[holdJSON](t, expectStatus(t, e.hold(t, showtimeID, "cust-a", seats[0]), http.StatusCreated))
	second := decode[holdJSON](t, expectStatus(t, e.hold(t, showtimeID, "cust-a", seats[1]), http.StatusCreated))

	noKey := e.newRequest(t, http.MethodPost, "/v1/holds/"+first.Token+"/confirm", "")
	expectProblem(t, e.send(t, noKey), http.StatusBadRequest, "invalid-request")

	expectProblem(t, e.confirm(t, strings.Repeat("A", 26), "key-1"), http.StatusNotFound, "not-found")

	expectStatus(t, e.confirm(t, first.Token, "key-1"), http.StatusCreated)
	expectProblem(t, e.confirm(t, second.Token, "key-1"), http.StatusUnprocessableEntity, "idempotency-key-reused")

	if got := countRows(t, e.store, `SELECT count(*) FROM bookings`); got != 1 {
		t.Fatalf("got %d bookings, want 1", got)
	}
}

// A NUL byte or invalid UTF-8 reaching Postgres as text would raise an error
// rather than find nothing, so these must not come back as 500.
func TestMalformedIdentifiersAreNotFound(t *testing.T) {
	e := newEnv(t, options{})

	for _, path := range []string{
		"/v1/holds/%00",
		"/v1/holds/%FF%FE",
		"/v1/bookings/CB-%00",
		"/v1/showtimes/0/seats",
		"/v1/showtimes/99999999999999999999/seats",
	} {
		expectProblem(t, e.get(t, path), http.StatusNotFound, "not-found")
	}
	if errs := e.logs.errorMessages(); len(errs) > 0 {
		t.Fatalf("malformed identifiers logged errors: %v", errs)
	}
}

func TestConfirmAfterExpiryIsGone(t *testing.T) {
	e := newEnv(t, options{ttl: time.Second})
	showtimeID, seats := showtimeSeats(t, e.store, 1)

	held := decode[holdJSON](t, expectStatus(t, e.hold(t, showtimeID, "cust-a", seats...), http.StatusCreated))
	time.Sleep(1500 * time.Millisecond)

	expectProblem(t, e.confirm(t, held.Token, "key-1"), http.StatusGone, "hold-expired")
}

func TestContendedShowtimeAnswersBusy(t *testing.T) {
	e := newEnv(t, options{lockTimeout: 200 * time.Millisecond})
	showtimeID, seats := showtimeSeats(t, e.store, 1)

	tx, err := e.store.Pool().Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock($1)`, showtimeID); err != nil {
		t.Fatalf("take showtime lock: %v", err)
	}

	resp := e.hold(t, showtimeID, "cust-a", seats...)
	expectProblem(t, resp, http.StatusServiceUnavailable, "busy")
	if got := resp.header.Get("Retry-After"); got != "2" {
		t.Fatalf("got Retry-After %q, want 2", got)
	}
}

func TestThrottleIsPerCustomer(t *testing.T) {
	e := newEnv(t, options{rate: 0.01, burst: 2})

	asCustomer := func(customer string) response {
		req := e.newRequest(t, http.MethodGet, "/v1/movies", "")
		req.Header.Set(headerCustomerRef, customer)
		return e.send(t, req)
	}

	expectStatus(t, asCustomer("cust-a"), http.StatusOK)
	expectStatus(t, asCustomer("cust-a"), http.StatusOK)

	limited := asCustomer("cust-a")
	expectProblem(t, limited, http.StatusTooManyRequests, "rate-limited")
	if wait, err := strconv.Atoi(limited.header.Get("Retry-After")); err != nil || wait < 1 {
		t.Fatalf("got Retry-After %q, want a positive number of seconds", limited.header.Get("Retry-After"))
	}

	expectStatus(t, asCustomer("cust-b"), http.StatusOK)
	for range 3 {
		expectStatus(t, e.get(t, "/v1/movies"), http.StatusOK)
	}
}

func TestUnmatchedRequestsAnswerWithProblems(t *testing.T) {
	e := newEnv(t, options{})

	expectProblem(t, e.get(t, "/v1/nothing-here"), http.StatusNotFound, "not-found")

	put := e.send(t, e.newRequest(t, http.MethodPut, "/v1/movies", ""))
	expectProblem(t, put, http.StatusMethodNotAllowed, "method-not-allowed")
	if allow := put.header.Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Fatalf("got Allow %q, want it to include GET", allow)
	}

	// Path cleaning redirects even when nothing matches, and that must pass
	// through untouched rather than turn into a problem.
	client := *e.server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Get(e.server.URL + "/v1//nothing-here")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect || resp.Header.Get("Location") != "/v1/nothing-here" {
		t.Fatalf("got %d to %q, want a redirect to the clean path", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestRequestID(t *testing.T) {
	e := newEnv(t, options{})

	withID := func(id string) string {
		req := e.newRequest(t, http.MethodGet, "/v1/movies", "")
		if id != "" {
			req.Header.Set(headerRequestID, id)
		}
		return e.send(t, req).header.Get(headerRequestID)
	}

	if got := withID("lb-7f3a"); got != "lb-7f3a" {
		t.Errorf("got %q, want the caller's id echoed", got)
	}
	if got := withID(""); len(got) != 26 {
		t.Errorf("got %q, want a generated id", got)
	}
	if got := withID("has space"); got == "has space" || len(got) != 26 {
		t.Errorf("got %q, want an invalid id replaced", got)
	}
}

func TestLogsAndMetricsNameTheRouteNotThePath(t *testing.T) {
	e := newEnv(t, options{})
	showtimeID, seats := showtimeSeats(t, e.store, 1)

	held := decode[holdJSON](t, expectStatus(t, e.hold(t, showtimeID, "cust-a", seats...), http.StatusCreated))
	expectStatus(t, e.get(t, "/v1/holds/"+held.Token), http.StatusOK)
	expectStatus(t, e.get(t, "/v1/holds/"+held.Token+"/nope"), http.StatusNotFound)

	var routes []string
	for _, r := range e.logs.snapshot() {
		r.Attrs(func(a slog.Attr) bool {
			if strings.Contains(a.Value.String(), held.Token) {
				t.Errorf("log %q attribute %s carries the hold token", r.Message, a.Key)
			}
			if r.Message == "request" && a.Key == "route" {
				routes = append(routes, a.Value.String())
			}
			return true
		})
	}
	want := []string{"POST /v1/showtimes/{id}/holds", "GET /v1/holds/{token}", "unmatched"}
	if !slices.Equal(routes, want) {
		t.Fatalf("got logged routes %v, want %v", routes, want)
	}
	if observed, _ := e.observed.snapshot(); !slices.Equal(observed, want) {
		t.Fatalf("got observed routes %v, want %v", observed, want)
	}
}

// The centrepiece of section 9, over the wire. A unique violation still reaches
// the client as a 409, so only the server's own signals can show it.
func TestTwoHundredRacersForOneSeat(t *testing.T) {
	e := newEnv(t, options{lockTimeout: 30 * time.Second, maxConns: 25})
	showtimeID, seats := showtimeSeats(t, e.store, 1)

	const racers = 200
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: racers}}
	t.Cleanup(client.CloseIdleConnections)

	requests := make([]*http.Request, racers)
	for i := range racers {
		requests[i] = e.holdRequest(t, showtimeID, fmt.Sprintf("cust-%03d", i), seats...)
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		statuses = map[int]int{}
		failures []error
	)
	start := make(chan struct{})
	for _, req := range requests {
		wg.Go(func() {
			<-start
			resp, err := client.Do(req)
			if err == nil {
				defer resp.Body.Close()
				var p problemBody
				if resp.StatusCode == http.StatusConflict {
					err = json.NewDecoder(resp.Body).Decode(&p)
				}
				if err == nil && resp.StatusCode == http.StatusConflict && !slices.Equal(p.SeatIDs, seats) {
					err = fmt.Errorf("conflict named seats %v, want %v", p.SeatIDs, seats)
				}
			}

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			statuses[resp.StatusCode]++
		})
	}
	close(start)
	wg.Wait()

	if len(failures) > 0 {
		t.Fatalf("%d racers failed, first: %v", len(failures), failures[0])
	}
	if errs := e.logs.errorMessages(); len(errs) > 0 {
		t.Errorf("server logged %d errors, first: %s", len(errs), errs[0])
	}
	if _, raceLost := e.observed.snapshot(); raceLost != 0 {
		t.Errorf("%d racers hit the unique index: the advisory lock did not serialize them", raceLost)
	}
	if statuses[http.StatusCreated] != 1 || statuses[http.StatusConflict] != racers-1 {
		t.Errorf("got statuses %v, want one 201 and %d 409", statuses, racers-1)
	}

	live := countRows(t, e.store,
		`SELECT count(*) FROM live_seat_occupancy WHERE showtime_id = $1 AND seat_id = $2`,
		showtimeID, seats[0])
	if live != 1 {
		t.Errorf("got %d live occupancy rows for the seat, want 1", live)
	}
}
