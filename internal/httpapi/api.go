package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/FreddieTheObserver/cinebook/internal/booking"
)

// Defaults applied to any Config field left unset.
const (
	DefaultRatePerSecond = 5
	DefaultRateBurst     = 20
)

// Config tunes the per-customer request throttle.
type Config struct {
	RatePerSecond float64
	RateBurst     int
}

func (c *Config) applyDefaults() {
	if c.RatePerSecond <= 0 {
		c.RatePerSecond = DefaultRatePerSecond
	}
	if c.RateBurst <= 0 {
		c.RateBurst = DefaultRateBurst
	}
}

// Observer receives what the API measures.
type Observer interface {
	// ObserveRequest is called once per request. Route is the matched pattern,
	// or "unmatched", so it never carries anything from the path.
	ObserveRequest(route string, status int, elapsed time.Duration)
	// SeatRaceLost is called when the unique index caught a double claim.
	SeatRaceLost()
}

type api struct {
	svc      *booking.Service
	log      *slog.Logger
	observer Observer
	limiter  *limiter
	mux      *http.ServeMux
}

// New returns the handler for every /v1 route, with errors answered as
// application/problem+json.
func New(svc *booking.Service, log *slog.Logger, observer Observer, cfg Config) http.Handler {
	cfg.applyDefaults()
	a := &api{
		svc:      svc,
		log:      log,
		observer: observer,
		limiter:  newLimiter(cfg.RatePerSecond, cfg.RateBurst),
		mux:      http.NewServeMux(),
	}

	a.handle("GET /v1/movies", a.listMovies)
	a.handle("GET /v1/showtimes", a.listShowtimes)
	a.handle("GET /v1/showtimes/{id}/seats", a.seatMap)
	a.handle("POST /v1/showtimes/{id}/holds", a.createHold)
	a.handle("GET /v1/holds/{token}", a.getHold)
	a.handle("DELETE /v1/holds/{token}", a.releaseHold)
	a.handle("POST /v1/holds/{token}/confirm", a.confirmHold)
	a.handle("GET /v1/bookings/{ref}", a.getBooking)

	return a.instrument(a.throttle(http.HandlerFunc(a.route)))
}

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func (a *api) handle(pattern string, h handlerFunc) {
	a.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			a.fail(w, r, err)
		}
	})
}

// ServeMux answers a request no pattern matches in plain text. Only its 404
// and 405 are rewritten, because an unmatched request can also be answered
// with a path-cleaning redirect.
func (a *api) route(w http.ResponseWriter, r *http.Request) {
	if _, pattern := a.mux.Handler(r); pattern != "" {
		a.mux.ServeHTTP(w, r)
		return
	}
	a.mux.ServeHTTP(&unmatchedWriter{ResponseWriter: w}, r)
}

type unmatchedWriter struct {
	http.ResponseWriter
	rewritten bool
}

func (w *unmatchedWriter) WriteHeader(code int) {
	switch code {
	case http.StatusNotFound:
		writeProblem(w.ResponseWriter, &problem{problemType: notFound})
	case http.StatusMethodNotAllowed:
		writeProblem(w.ResponseWriter, &problem{problemType: methodNotAllowed})
	default:
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.rewritten = true
}

func (w *unmatchedWriter) Write(b []byte) (int, error) {
	if w.rewritten {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}
