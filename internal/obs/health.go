package obs

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

const readinessTimeout = time.Second

// Health answers liveness and readiness probes.
type Health struct {
	ping     func(context.Context) error
	draining atomic.Bool
}

// NewHealth builds probes that reach the database through ping.
func NewHealth(ping func(context.Context) error) *Health {
	return &Health{ping: ping}
}

// Live answers 200 whenever the process can serve HTTP at all. It checks no
// dependency, so a database outage does not get every replica restarted.
func (h *Health) Live(w http.ResponseWriter, _ *http.Request) {
	writeProbe(w, http.StatusOK, "ok")
}

// Ready answers 200 when this replica should receive traffic.
func (h *Health) Ready(w http.ResponseWriter, r *http.Request) {
	if h.draining.Load() {
		writeProbe(w, http.StatusServiceUnavailable, "draining")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()
	if err := h.ping(ctx); err != nil {
		writeProbe(w, http.StatusServiceUnavailable, "database unreachable")
		return
	}
	writeProbe(w, http.StatusOK, "ok")
}

// Drain marks the replica not ready for good, so a load balancer stops routing
// to it while in-flight requests finish.
func (h *Health) Drain() { h.draining.Store(true) }

func writeProbe(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body+"\n")
}
