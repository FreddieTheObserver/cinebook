package booking

import (
	"errors"
	"fmt"

	"github.com/FreddieTheObserver/cinebook/internal/store"
)

// Sentinels the HTTP layer maps onto status codes.
var (
	ErrNotFound             = errors.New("not found")
	ErrHoldExpired          = errors.New("hold expired")
	ErrInvalidSelection     = errors.New("invalid selection")
	ErrSalesClosed          = errors.New("sales are closed for this showtime")
	ErrAlreadyConfirmed     = errors.New("hold already confirmed")
	ErrIdempotencyKeyReused = errors.New("idempotency key already used for a different hold")
	ErrBusy                 = errors.New("busy")
)

// SeatsUnavailable carries the blocking seat ids, so a client can re-render the
// map without a second round trip.
type SeatsUnavailable struct {
	SeatIDs []int64
	cause   error
}

// Error implements error.
func (e *SeatsUnavailable) Error() string {
	return fmt.Sprintf("seats unavailable: %v", e.SeatIDs)
}

// Unwrap exposes the store error underneath, which may be store.ErrSeatRaceLost.
func (e *SeatsUnavailable) Unwrap() error { return e.cause }

func fromStore(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrBusy):
		return fmt.Errorf("%w: %w", ErrBusy, err)
	case errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	return err
}
