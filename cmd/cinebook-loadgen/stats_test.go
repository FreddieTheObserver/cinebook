package main

import (
	"net/http"
	"testing"
	"time"
)

func TestPercentileUsesNearestRank(t *testing.T) {
	s := newStats()
	for _, ms := range []int{7, 1, 10, 3, 5, 2, 9, 4, 8, 6} {
		s.record(http.StatusCreated, time.Duration(ms)*time.Millisecond)
	}

	for p, want := range map[float64]time.Duration{
		0:    1 * time.Millisecond,
		0.10: 1 * time.Millisecond,
		0.50: 5 * time.Millisecond,
		0.51: 6 * time.Millisecond,
		0.99: 10 * time.Millisecond,
		1:    10 * time.Millisecond,
	} {
		if got := s.percentile(p); got != want {
			t.Errorf("p%v: got %v, want %v", p*100, got, want)
		}
	}
}

func TestPercentileOfNothingIsZero(t *testing.T) {
	if got := newStats().percentile(0.99); got != 0 {
		t.Fatalf("got %v, want 0", got)
	}
}

func TestMergeAndCounts(t *testing.T) {
	a, b := newStats(), newStats()
	a.record(http.StatusCreated, time.Millisecond)
	a.record(http.StatusConflict, time.Millisecond)
	a.errors = 1
	b.record(http.StatusConflict, time.Millisecond)
	b.record(http.StatusInternalServerError, time.Millisecond)
	b.record(http.StatusBadGateway, time.Millisecond)

	a.merge(b)
	a.elapsed = 2 * time.Second

	if a.requests() != 5 || a.statuses[http.StatusConflict] != 2 || a.errors != 1 {
		t.Fatalf("merge lost data: %+v", a)
	}
	if a.other() != 2 {
		t.Fatalf("got %d other, want 2", a.other())
	}
	if a.throughput() != 2.5 {
		t.Fatalf("got throughput %v, want 2.5", a.throughput())
	}
}

func TestParseLevels(t *testing.T) {
	got, err := parseLevels("1, 8,64")
	if err != nil || len(got) != 3 || got[0] != 1 || got[1] != 8 || got[2] != 64 {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"", "0", "4,-1", "four", "1,,2"} {
		if _, err := parseLevels(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
