package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/FreddieTheObserver/cinebook/internal/booking"
)

func TestProblemFor(t *testing.T) {
	tests := []struct {
		err        error
		want       problemType
		retryAfter time.Duration
	}{
		{&booking.SeatsUnavailable{SeatIDs: []int64{3, 4}}, seatUnavailable, 0},
		{fmt.Errorf("%w: no rows", booking.ErrNotFound), notFound, 0},
		{fmt.Errorf("%w: seat 5 requested twice", booking.ErrInvalidSelection), invalidSelection, 0},
		{booking.ErrHoldExpired, holdExpired, 0},
		{booking.ErrSalesClosed, salesClosed, 0},
		{booking.ErrAlreadyConfirmed, holdConfirmed, 0},
		{booking.ErrIdempotencyKeyReused, idempotencyKeyReused, 0},
		{fmt.Errorf("%w: lock timeout", booking.ErrBusy), busy, busyRetryAfter},
		{fmt.Errorf("wrapped: %w", badRequest("nope")), invalidRequest, 0},
		{errors.New("connection reset"), internalError, 0},
	}

	for _, tc := range tests {
		t.Run(tc.want.slug, func(t *testing.T) {
			got := problemFor(tc.err)
			if got.problemType != tc.want || got.retryAfter != tc.retryAfter {
				t.Fatalf("got %+v, want %s with retry after %v", got, tc.want.slug, tc.retryAfter)
			}
		})
	}
}

func TestProblemForCarriesTheBlockingSeats(t *testing.T) {
	got := problemFor(&booking.SeatsUnavailable{SeatIDs: []int64{3, 4}})
	if !slices.Equal(got.seatIDs, []int64{3, 4}) {
		t.Fatalf("got seat ids %v, want [3 4]", got.seatIDs)
	}
}

func TestInvalidSelectionDetailDoesNotRepeatTheTitle(t *testing.T) {
	got := problemFor(fmt.Errorf("%w: 11 seats requested, at most 10 per hold", booking.ErrInvalidSelection))
	if got.detail != "11 seats requested, at most 10 per hold" {
		t.Fatalf("got detail %q", got.detail)
	}
}

func TestProblemForHidesInternalDetail(t *testing.T) {
	if got := problemFor(errors.New("password authentication failed for user cinebook")); got.detail != "" {
		t.Fatalf("internal error leaked detail %q", got.detail)
	}
}

func TestRetryAfterRoundsUp(t *testing.T) {
	for wait, want := range map[time.Duration]string{
		200 * time.Millisecond:  "1",
		time.Second:             "1",
		1500 * time.Millisecond: "2",
		99 * time.Second:        "99",
	} {
		rec := httptest.NewRecorder()
		writeProblem(rec, &problem{problemType: rateLimited, retryAfter: wait})
		if got := rec.Header().Get("Retry-After"); got != want {
			t.Errorf("wait %v: got Retry-After %q, want %q", wait, got, want)
		}
	}
}

func TestLimiterSpendsTheBurstThenRefills(t *testing.T) {
	l := newLimiter(2, 3)
	t0 := time.Now()

	for i := range 3 {
		if ok, _ := l.allow("a", t0); !ok {
			t.Fatalf("request %d inside the burst was refused", i)
		}
	}
	ok, wait := l.allow("a", t0)
	if ok {
		t.Fatal("request past the burst was allowed")
	}
	if wait != 500*time.Millisecond {
		t.Fatalf("got wait %v, want 500ms at 2 per second", wait)
	}

	if ok, _ := l.allow("a", t0.Add(499*time.Millisecond)); ok {
		t.Fatal("allowed before a whole token refilled")
	}
	if ok, _ := l.allow("a", t0.Add(time.Second)); !ok {
		t.Fatal("refused after a token refilled")
	}
}

func TestLimiterCapsRefillAtTheBurst(t *testing.T) {
	l := newLimiter(10, 2)
	t0 := time.Now()

	l.allow("a", t0)
	later := t0.Add(time.Hour)
	for i := range 2 {
		if ok, _ := l.allow("a", later); !ok {
			t.Fatalf("request %d refused after a long idle", i)
		}
	}
	if ok, _ := l.allow("a", later); ok {
		t.Fatal("an hour idle banked more than the burst")
	}
}

func TestLimiterKeysAreIndependent(t *testing.T) {
	l := newLimiter(1, 1)
	t0 := time.Now()

	l.allow("a", t0)
	if ok, _ := l.allow("a", t0); ok {
		t.Fatal("a was not limited")
	}
	if ok, _ := l.allow("b", t0); !ok {
		t.Fatal("b was limited by a's usage")
	}
}

func TestLimiterIgnoresAClockThatTrails(t *testing.T) {
	l := newLimiter(1, 1)
	t0 := time.Now()

	l.allow("a", t0)
	l.allow("a", t0.Add(-time.Hour))
	if ok, _ := l.allow("a", t0.Add(time.Second)); !ok {
		t.Fatal("a trailing clock drained the bucket")
	}
}

func TestLimiterSweepDropsOnlyFullBuckets(t *testing.T) {
	l := newLimiter(1, 2)
	t0 := time.Now()

	for i := range minSweepAt - 1 {
		l.allow(fmt.Sprintf("idle-%d", i), t0)
	}
	l.allow("busy", t0)
	l.allow("busy", t0)

	// Idle buckets are full again after a second; busy is still one short.
	l.allow("new", t0.Add(time.Second))

	if _, ok := l.buckets["busy"]; !ok {
		t.Fatal("sweep dropped a bucket that was not full")
	}
	if len(l.buckets) != 2 {
		t.Fatalf("got %d buckets after the sweep, want busy and new", len(l.buckets))
	}
	if l.sweepAt != minSweepAt {
		t.Fatalf("got next sweep at %d, want %d", l.sweepAt, minSweepAt)
	}
}

func TestDecodeJSON(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		want        problemType
	}{
		{"missing content type", "", `{"seat_ids": [1]}`, unsupportedMediaType},
		{"form content type", "application/x-www-form-urlencoded", `{"seat_ids": [1]}`, unsupportedMediaType},
		{"empty body", "application/json", ``, invalidRequest},
		{"malformed", "application/json", `{"seat_ids": [1`, invalidRequest},
		{"not an object", "application/json", `[1, 2]`, invalidRequest},
		{"unknown field", "application/json", `{"seats": [1]}`, invalidRequest},
		{"wrong type", "application/json", `{"seat_ids": ["1"]}`, invalidRequest},
		{"fractional id", "application/json", `{"seat_ids": [1.5]}`, invalidRequest},
		{"trailing value", "application/json", `{"seat_ids": [1]} {}`, invalidRequest},
		{"trailing garbage", "application/json", `{"seat_ids": [1]} x`, invalidRequest},
		{"too large", "application/json", `{"seat_ids": [1]}` + strings.Repeat(" ", maxBodyBytes), requestTooLarge},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			req.Body = http.MaxBytesReader(httptest.NewRecorder(), req.Body, maxBodyBytes)
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			var dst holdRequest
			err := decodeJSON(req, &dst)

			var p *problem
			if !errors.As(err, &p) || p.problemType != tc.want {
				t.Fatalf("got %v, want %s", err, tc.want.slug)
			}
			if p.detail == "" {
				t.Fatal("no detail to say what was wrong")
			}
		})
	}
}

func TestDecodeJSONAcceptsAHoldRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"seat_ids": [4, 5]}`+"\n"))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	var dst holdRequest
	if err := decodeJSON(req, &dst); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !slices.Equal(dst.SeatIDs, []int64{4, 5}) {
		t.Fatalf("got %v", dst.SeatIDs)
	}
}

func TestShowtimeFilter(t *testing.T) {
	from := time.Date(2026, 9, 16, 6, 0, 0, 0, time.UTC)

	got, err := showtimeFilter(url.Values{
		"movie_id": {"7"},
		"from":     {"2026-09-16T13:00:00+07:00"},
		"to":       {"2026-09-17T00:00:00Z"},
	})
	if err != nil {
		t.Fatalf("valid filter rejected: %v", err)
	}
	if got.MovieID != 7 || !got.StartsFrom.Equal(from) || got.StartsBefore.IsZero() {
		t.Fatalf("got %+v", got)
	}

	if got, err := showtimeFilter(url.Values{}); err != nil || got != (booking.ShowtimeFilter{}) {
		t.Fatalf("empty query: got %+v, %v", got, err)
	}

	for name, q := range map[string]url.Values{
		"zero movie":          {"movie_id": {"0"}},
		"negative movie":      {"movie_id": {"-3"}},
		"non-numeric movie":   {"movie_id": {"dune"}},
		"empty movie":         {"movie_id": {""}},
		"repeated movie":      {"movie_id": {"1", "2"}},
		"date without time":   {"from": {"2026-09-16"}},
		"plus decoded as ' '": {"from": {"2026-09-16T13:00:00 07:00"}},
		"to before from":      {"from": {"2026-09-17T00:00:00Z"}, "to": {"2026-09-16T00:00:00Z"}},
		"to equal to from":    {"from": {"2026-09-17T00:00:00Z"}, "to": {"2026-09-17T00:00:00Z"}},
	} {
		t.Run(name, func(t *testing.T) {
			var p *problem
			if _, err := showtimeFilter(q); !errors.As(err, &p) || p.problemType != invalidRequest {
				t.Fatalf("got %v, want invalid-request", err)
			}
		})
	}
}

func TestHeaderToken(t *testing.T) {
	tests := []struct {
		name    string
		values  []string
		want    string
		present bool
		valid   bool
	}{
		{name: "absent", valid: true},
		{name: "valid", values: []string{"cust-42"}, want: "cust-42", present: true, valid: true},
		{name: "at the limit", values: []string{strings.Repeat("k", maxHeaderTokenLen)}, want: strings.Repeat("k", maxHeaderTokenLen), present: true, valid: true},
		{name: "empty", values: []string{""}},
		{name: "space", values: []string{"cust 42"}},
		{name: "too long", values: []string{strings.Repeat("k", maxHeaderTokenLen+1)}},
		{name: "not ASCII", values: []string{"cüst"}},
		{name: "invalid UTF-8", values: []string{"cust\xff"}},
		{name: "repeated", values: []string{"a", "b"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			for _, v := range tc.values {
				req.Header.Add(headerCustomerRef, v)
			}

			got, present, err := headerToken(req, headerCustomerRef)
			if tc.valid != (err == nil) {
				t.Fatalf("got error %v, want valid=%v", err, tc.valid)
			}
			if got != tc.want || present != tc.present {
				t.Fatalf("got %q present=%v, want %q present=%v", got, present, tc.want, tc.present)
			}
		})
	}
}

func panicValue(f func()) (v any) {
	defer func() { v = recover() }()
	f()
	return nil
}

func TestInstrumentTurnsAPanicIntoAProblem(t *testing.T) {
	logs := &logSink{}
	a := &api{log: slog.New(logs), observer: &observations{}}
	h := a.instrument(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))

	rec := httptest.NewRecorder()
	if v := panicValue(func() { h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil)) }); v != nil {
		t.Fatalf("panic escaped: %v", v)
	}
	if rec.Code != http.StatusInternalServerError || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if errs := logs.errorMessages(); len(errs) != 1 {
		t.Fatalf("got error logs %v, want one", errs)
	}
}

func TestInstrumentAbortsAPanicAfterHeadersAreSent(t *testing.T) {
	a := &api{log: slog.New(&logSink{}), observer: &observations{}}
	h := a.instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		panic("boom")
	}))

	v := panicValue(func() { h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil)) })
	if v != http.ErrAbortHandler {
		t.Fatalf("got panic %v, want http.ErrAbortHandler", v)
	}
}
