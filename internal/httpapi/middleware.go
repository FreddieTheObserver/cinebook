package httpapi

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

type loggerKey struct{}

const unmatchedRoute = "unmatched"

func loggerFrom(ctx context.Context) *slog.Logger {
	if log, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return log
	}
	return slog.Default()
}

// instrument must stay outermost and be the only middleware that replaces the
// request, because ServeMux records the matched pattern on the *http.Request it
// receives and the access log reads it back from this one.
func (a *api) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		id, ok, err := headerToken(r, headerRequestID)
		if !ok || err != nil {
			id = rand.Text()
		}
		w.Header().Set(headerRequestID, id)

		log := a.log.With("request_id", id)
		r = r.WithContext(context.WithValue(r.Context(), loggerKey{}, log))
		// Against the server's own writer: past the limit, MaxBytesReader asks
		// it to close the connection through a type assertion a wrapper hides.
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		rec := &recorder{ResponseWriter: w}

		defer func() {
			v := recover()
			if v != nil && v != http.ErrAbortHandler {
				log.Error("handler panicked", "panic", v, "stack", string(debug.Stack()))
				if rec.status == 0 {
					writeProblem(rec, &problem{problemType: internalError})
					v = nil
				} else {
					// Too late for a problem response, so abort the connection
					// rather than let a truncated body pass as complete.
					v = http.ErrAbortHandler
				}
			}

			// Not the path: hold tokens and booking references in it are bearer
			// capabilities, and would also give metrics unbounded cardinality.
			route := r.Pattern
			if route == "" {
				route = unmatchedRoute
			}
			elapsed := time.Since(start)
			a.observer.ObserveRequest(route, rec.status, elapsed)
			log.Info("request",
				"method", r.Method,
				"route", route,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration", elapsed,
			)

			if v != nil {
				panic(v)
			}
		}()

		next.ServeHTTP(rec, r)
	})
}

// throttle limits requests per customer. Requests that name no customer are
// left to whatever sits in front of the service, since behind a load balancer
// the remote address says nothing about who is asking.
func (a *api) throttle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		customer, ok, err := headerToken(r, headerCustomerRef)
		if err != nil {
			writeProblem(w, problemFor(err))
			return
		}
		if ok {
			if allowed, wait := a.limiter.allow(customer, time.Now()); !allowed {
				writeProblem(w, &problem{problemType: rateLimited, retryAfter: wait})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
