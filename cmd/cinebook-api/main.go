package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/FreddieTheObserver/cinebook/internal/booking"
	"github.com/FreddieTheObserver/cinebook/internal/config"
	"github.com/FreddieTheObserver/cinebook/internal/httpapi"
	"github.com/FreddieTheObserver/cinebook/internal/obs"
	"github.com/FreddieTheObserver/cinebook/internal/store"
)

// The write timeout sits well past the lock and statement timeouts, so it only
// ever cuts off a client that stopped reading.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 2 * time.Minute
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// After the first signal starts a graceful shutdown, a second one kills.
	context.AfterFunc(ctx, stop)

	err := run(ctx, os.Getenv, os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cinebook-api: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	log := obs.NewLogger(stdout, cfg.LogLevel)
	slog.SetDefault(log)

	st, err := store.New(ctx, store.Config{
		DSN:              cfg.DSN,
		MaxConns:         cfg.DBMaxConns,
		LockTimeout:      cfg.LockTimeout,
		StatementTimeout: cfg.StatementTimeout,
	})
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		return err
	}

	svc := booking.New(st, booking.Config{HoldTTL: cfg.HoldTTL, MaxSeatsPerHold: cfg.MaxSeatsPerHold})
	metrics := obs.NewMetrics(st.Pool().Stat)
	health := obs.NewHealth(st.Ping)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health.Live)
	mux.HandleFunc("GET /readyz", health.Ready)
	mux.Handle("GET /metrics", metrics.Handler())
	mux.Handle("/", httpapi.New(svc, log, metrics, httpapi.Config{
		RatePerSecond: cfg.RatePerSecond,
		RateBurst:     cfg.RateBurst,
	}))

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	sweepCtx, stopSweeping := context.WithCancel(ctx)
	defer stopSweeping()
	var sweeper sync.WaitGroup
	sweeper.Go(func() { sweep(sweepCtx, svc, metrics, log, cfg.SweepInterval, cfg.SweepBatch) })

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	log.Info("listening", "addr", ln.Addr().String())

	select {
	case err := <-served:
		stopSweeping()
		sweeper.Wait()
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	// Readiness fails first and the listener stays open for the drain delay, so
	// a load balancer can notice before connections start being refused.
	health.Drain()
	log.Info("draining", "delay", cfg.DrainDelay)
	time.Sleep(cfg.DrainDelay)

	log.Info("shutting down", "timeout", cfg.ShutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	sweeper.Wait()
	if err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	log.Info("stopped")
	return nil
}
