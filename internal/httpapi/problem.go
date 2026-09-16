package httpapi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/FreddieTheObserver/cinebook/internal/booking"
	"github.com/FreddieTheObserver/cinebook/internal/store"
)

type problemType struct {
	slug   string
	title  string
	status int
}

// The title is fixed per type, per RFC 9457. Anything specific to one
// occurrence goes in the detail.
var (
	invalidRequest       = problemType{"invalid-request", "The request is malformed", http.StatusBadRequest}
	notFound             = problemType{"not-found", "No such resource", http.StatusNotFound}
	methodNotAllowed     = problemType{"method-not-allowed", "Method not allowed on this resource", http.StatusMethodNotAllowed}
	seatUnavailable      = problemType{"seat-unavailable", "Seats are unavailable", http.StatusConflict}
	salesClosed          = problemType{"sales-closed", "Sales are closed for this showtime", http.StatusConflict}
	holdConfirmed        = problemType{"hold-confirmed", "The hold is already a booking", http.StatusConflict}
	holdExpired          = problemType{"hold-expired", "The hold has expired", http.StatusGone}
	requestTooLarge      = problemType{"request-too-large", "The request body is too large", http.StatusRequestEntityTooLarge}
	unsupportedMediaType = problemType{"unsupported-media-type", "Unsupported media type", http.StatusUnsupportedMediaType}
	invalidSelection     = problemType{"invalid-selection", "The seat selection is invalid", http.StatusUnprocessableEntity}
	idempotencyKeyReused = problemType{"idempotency-key-reused", "The idempotency key belongs to a different hold", http.StatusUnprocessableEntity}
	rateLimited          = problemType{"rate-limited", "Too many requests", http.StatusTooManyRequests}
	internalError        = problemType{"internal", "Internal error", http.StatusInternalServerError}
	busy                 = problemType{"busy", "The showtime is busy", http.StatusServiceUnavailable}
)

// A full lock queue waited out the lock timeout, so retrying at once would
// only rejoin the same queue.
const busyRetryAfter = 2 * time.Second

// problem is an RFC 9457 response. It is also an error, so request validation
// can return one directly.
type problem struct {
	problemType
	detail     string
	retryAfter time.Duration
	seatIDs    []int64
}

func (p *problem) Error() string {
	if p.detail == "" {
		return p.slug
	}
	return p.slug + ": " + p.detail
}

func badRequest(format string, args ...any) *problem {
	return &problem{problemType: invalidRequest, detail: fmt.Sprintf(format, args...)}
}

type problemBody struct {
	Type    string  `json:"type"`
	Title   string  `json:"title"`
	Status  int     `json:"status"`
	Detail  string  `json:"detail,omitempty"`
	SeatIDs []int64 `json:"seat_ids,omitempty"`
}

// The type is a path-absolute reference, which RFC 9457 recommends over a bare
// slug because a relative one would resolve differently under every resource.
func problemTypeURI(slug string) string { return "/problems/" + slug }

func writeProblem(w http.ResponseWriter, p *problem) {
	if p.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(p.retryAfter.Seconds()))))
	}
	// problemBody holds nothing that can fail to encode.
	_ = writeBody(w, p.status, "application/problem+json", problemBody{
		Type:    problemTypeURI(p.slug),
		Title:   p.title,
		Status:  p.status,
		Detail:  p.detail,
		SeatIDs: p.seatIDs,
	})
}

func problemFor(err error) *problem {
	var (
		p           *problem
		unavailable *booking.SeatsUnavailable
	)
	switch {
	case errors.As(err, &p):
		return p
	case errors.As(err, &unavailable):
		return &problem{
			problemType: seatUnavailable,
			detail:      fmt.Sprintf("%d of the requested seats are held or sold", len(unavailable.SeatIDs)),
			seatIDs:     unavailable.SeatIDs,
		}
	case errors.Is(err, booking.ErrNotFound):
		return &problem{problemType: notFound}
	case errors.Is(err, booking.ErrInvalidSelection):
		return &problem{problemType: invalidSelection, detail: err.Error()}
	case errors.Is(err, booking.ErrHoldExpired):
		return &problem{problemType: holdExpired}
	case errors.Is(err, booking.ErrSalesClosed):
		return &problem{problemType: salesClosed}
	case errors.Is(err, booking.ErrAlreadyConfirmed):
		return &problem{problemType: holdConfirmed, detail: "undoing a booking is a refund, which is not supported"}
	case errors.Is(err, booking.ErrIdempotencyKeyReused):
		return &problem{problemType: idempotencyKeyReused}
	case errors.Is(err, booking.ErrBusy):
		return &problem{problemType: busy, retryAfter: busyRetryAfter}
	}
	return &problem{problemType: internalError}
}

func (a *api) fail(w http.ResponseWriter, r *http.Request, err error) {
	log := loggerFrom(r.Context())

	// The server cancels the request context when the client disconnects, and
	// nobody is left to read a response.
	if errors.Is(r.Context().Err(), context.Canceled) {
		log.Info("client went away", "err", err)
		return
	}

	p := problemFor(err)
	switch {
	case errors.Is(err, store.ErrSeatRaceLost):
		log.Error("unique index caught a double claim that the advisory lock should have prevented", "err", err)
	case p.problemType == internalError:
		log.Error("request failed", "err", err)
	}
	writeProblem(w, p)
}
