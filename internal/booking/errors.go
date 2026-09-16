package booking

import (
	"errors"
	"fmt"

	"github.com/FreddieTheObserver/cinebook/internal/store"
)

var (
	ErrNotFound             = errors.New("not found")
	ErrHoldExpired          = errors.New("hold expired")
	ErrInvalidSelection     = errors.New("invalid selection")
	ErrSalesClosed          = errors.New("sales are closed for this showtime")
	ErrAlreadyConfirmed     = errors.New("hold already confirmed")
	ErrIdempotencyKeyReused = errors.New("idempotency key already used for a different hold")
	ErrBusy                 = errors.New("busy")
)

// SeatsUnavailable names the seats that blocked a hold, so a client can
// re-render the map without a second round trip.
type SeatsUnavailable struct {
	SeatIDs []int64
	cause   error
}

func (e *SeatsUnavailable) Error() string {
	return fmt.Sprintf("seats unavailable: %v", e.SeatIDs)
}

func (e *SeatsUnavailable) Unwrap() error { return e.cause }

// fromStore restates a store error in domain terms. Anything already expressed
// as a domain error passes through untouched.
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
