package main

import (
	"math"
	"net/http"
	"slices"
	"time"
)

type stats struct {
	latencies []time.Duration
	statuses  map[int]int
	errors    int
	elapsed   time.Duration
}

func newStats() *stats { return &stats{statuses: map[int]int{}} }

func (s *stats) record(status int, latency time.Duration) {
	s.latencies = append(s.latencies, latency)
	s.statuses[status]++
}

func (s *stats) merge(o *stats) {
	s.latencies = append(s.latencies, o.latencies...)
	for status, n := range o.statuses {
		s.statuses[status] += n
	}
	s.errors += o.errors
}

func (s *stats) requests() int { return len(s.latencies) }

func (s *stats) throughput() float64 {
	if s.elapsed <= 0 {
		return 0
	}
	return float64(s.requests()) / s.elapsed.Seconds()
}

// percentile uses the nearest-rank method, so it always reports a latency that
// was actually observed.
func (s *stats) percentile(p float64) time.Duration {
	if len(s.latencies) == 0 {
		return 0
	}
	if !slices.IsSorted(s.latencies) {
		slices.Sort(s.latencies)
	}
	rank := int(math.Ceil(p * float64(len(s.latencies))))
	return s.latencies[max(rank, 1)-1]
}

func (s *stats) other() int {
	n := 0
	for status, count := range s.statuses {
		switch status {
		case http.StatusCreated, http.StatusConflict, http.StatusServiceUnavailable, http.StatusTooManyRequests:
		default:
			n += count
		}
	}
	return n
}
