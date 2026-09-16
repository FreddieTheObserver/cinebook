package main

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/FreddieTheObserver/cinebook/internal/booking"
	"github.com/FreddieTheObserver/cinebook/internal/obs"
)

// sweep reclaims lapsed holds until ctx ends. Per section 4.3 correctness never
// depends on it, so a failed run is logged and the next one tries again.
func sweep(ctx context.Context, svc *booking.Service, metrics *obs.Metrics, log *slog.Logger, interval time.Duration, batch int32) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(interval)):
		}

		reclaimed, err := svc.Sweep(ctx, batch)
		if ctx.Err() != nil {
			return
		}
		metrics.ObserveSweep(reclaimed, err)
		if err != nil {
			log.Warn("sweep failed", "err", err, "reclaimed", reclaimed)
		} else if reclaimed > 0 {
			log.Info("sweep reclaimed seats", "reclaimed", reclaimed)
		}
	}
}

// Within ten percent either way, so replicas that started together do not
// sweep in lockstep.
func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.9 + 0.2*rand.Float64()))
}
