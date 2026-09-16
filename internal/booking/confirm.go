package booking

import (
	"context"
	"errors"
	"fmt"

	"github.com/FreddieTheObserver/cinebook/internal/store"
	"github.com/FreddieTheObserver/cinebook/internal/store/gen"
)

const refAttempts = 5

// Confirm turns a live hold into a booking. Replaying it returns the booking
// that already exists rather than making a second one.
func (s *Service) Confirm(ctx context.Context, token, idempotencyKey string) (*Booking, error) {
	if idempotencyKey == "" {
		return nil, fmt.Errorf("%w: an idempotency key is required", ErrInvalidSelection)
	}

	held, err := s.store.GetHoldByToken(ctx, token)
	if err != nil {
		return nil, fromStore(store.Classify(err))
	}

	var result Booking
	err = s.store.InShowtimeTx(ctx, held.ShowtimeID, func(ctx context.Context, q *gen.Queries) error {
		// Re-read under the lock. Between the lookup above and here, another
		// transaction may have confirmed, released or expired this hold.
		held, err := q.GetHoldByToken(ctx, token)
		if err != nil {
			return err
		}
		if held.ReleasedAt != nil {
			return ErrHoldExpired
		}

		if held.ConfirmedAt != nil {
			booked, err := q.GetBookingByHold(ctx, held.ID)
			if err != nil {
				return err
			}
			seats, err := q.GetHoldSeats(ctx, held.ID)
			if err != nil {
				return err
			}
			result = bookingFrom(booked, seatsFromHold(seats, SeatSold))
			return nil
		}

		// Checked before anything is written: if this key belongs to a
		// different hold, confirming these seats would strand them.
		switch prior, err := q.GetBookingByIdempotencyKey(ctx, gen.GetBookingByIdempotencyKeyParams{
			CustomerRef: held.CustomerRef, IdempotencyKey: idempotencyKey,
		}); {
		case err == nil && prior.HoldID != held.ID:
			return ErrIdempotencyKeyReused
		case err != nil && !errors.Is(store.Classify(err), store.ErrNotFound):
			return err
		}

		seats, err := q.GetHoldSeats(ctx, held.ID)
		if err != nil {
			return err
		}
		if len(seats) == 0 {
			return ErrHoldExpired
		}

		// Liveness is re-asserted against the database clock in the UPDATE
		// predicate, so the row count is the answer to "was it still live".
		confirmed, err := q.ConfirmHoldSeats(ctx, held.ID)
		if err != nil {
			return err
		}
		if confirmed != int64(len(seats)) {
			return ErrHoldExpired
		}
		switch rows, err := q.ConfirmHold(ctx, held.ID); {
		case err != nil:
			return err
		case rows != 1:
			return ErrHoldExpired
		}

		showtime, err := q.GetShowtime(ctx, held.ShowtimeID)
		if err != nil {
			return err
		}

		booked, err := insertBooking(ctx, q, held, showtime, totalMinor(showtime.PriceMinor, len(seats)), idempotencyKey)
		if err != nil {
			return err
		}
		result = bookingFrom(booked, seatsFromHold(seats, SeatSold))
		return nil
	})
	if err != nil {
		return nil, fromStore(err)
	}
	return &result, nil
}

// Booking reads take no lock.
func (s *Service) GetBooking(ctx context.Context, ref string) (*Booking, error) {
	booked, err := s.store.GetBookingByRef(ctx, ref)
	if err != nil {
		return nil, fromStore(store.Classify(err))
	}
	seats, err := s.store.GetHoldSeats(ctx, booked.HoldID)
	if err != nil {
		return nil, fromStore(store.Classify(err))
	}
	b := bookingFrom(booked, seatsFromHold(seats, SeatSold))
	return &b, nil
}

// insertBooking retries only a collided reference. Anything else is either a
// real conflict or a real error.
func insertBooking(ctx context.Context, q *gen.Queries, held gen.GetHoldByTokenRow, showtime gen.GetShowtimeRow, total int64, idempotencyKey string) (gen.Booking, error) {
	for range refAttempts {
		ref, err := newBookingRef()
		if err != nil {
			return gen.Booking{}, err
		}

		booked, err := q.CreateBooking(ctx, gen.CreateBookingParams{
			Ref:            ref,
			HoldID:         held.ID,
			ShowtimeID:     held.ShowtimeID,
			CustomerRef:    held.CustomerRef,
			TotalMinor:     total,
			Currency:       showtime.Currency,
			IdempotencyKey: idempotencyKey,
		})
		if err == nil {
			return booked, nil
		}
		if !store.IsInsertConflict(err) {
			return gen.Booking{}, err
		}

		// The insert was suppressed. If the key was taken since the check
		// above, this transaction must roll back rather than retry.
		if _, err := q.GetBookingByIdempotencyKey(ctx, gen.GetBookingByIdempotencyKeyParams{
			CustomerRef: held.CustomerRef, IdempotencyKey: idempotencyKey,
		}); err == nil {
			return gen.Booking{}, ErrIdempotencyKeyReused
		}
	}
	return gen.Booking{}, fmt.Errorf("no free booking reference after %d attempts", refAttempts)
}

func bookingFrom(b gen.Booking, seats []Seat) Booking {
	return Booking{
		Ref:        b.Ref,
		ShowtimeID: b.ShowtimeID,
		Seats:      seats,
		TotalMinor: b.TotalMinor,
		Currency:   b.Currency,
		CreatedAt:  b.CreatedAt,
	}
}
