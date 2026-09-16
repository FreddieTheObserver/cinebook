package booking

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/FreddieTheObserver/cinebook/internal/store"
	"github.com/FreddieTheObserver/cinebook/internal/store/gen"
)

// Hold claims seats for a customer until the TTL lapses.
func (s *Service) Hold(ctx context.Context, showtimeID int64, customerRef string, seatIDs []int64) (*Hold, error) {
	if customerRef == "" {
		return nil, fmt.Errorf("%w: a customer reference is required", ErrInvalidSelection)
	}
	seatIDs, err := normalizeSeats(seatIDs, s.cfg.MaxSeatsPerHold)
	if err != nil {
		return nil, err
	}
	token, err := newHoldToken()
	if err != nil {
		return nil, err
	}

	var result Hold
	err = s.store.InShowtimeTx(ctx, showtimeID, func(ctx context.Context, q *gen.Queries) error {
		showtime, err := q.GetShowtime(ctx, showtimeID)
		if err != nil {
			return err
		}
		if !showtime.SalesOpen {
			return ErrSalesClosed
		}

		owned, err := q.CountSeatsInShowtime(ctx, gen.CountSeatsInShowtimeParams{
			ShowtimeID: showtimeID, SeatIds: seatIDs,
		})
		if err != nil {
			return err
		}
		if owned != int64(len(seatIDs)) {
			return fmt.Errorf("%w: seat does not belong to this showtime", ErrInvalidSelection)
		}

		// Reclaim first, so expiry is materialized when it matters and never
		// depends on the sweeper having run.
		if _, err := q.ReclaimExpiredSeats(ctx, gen.ReclaimExpiredSeatsParams{
			ShowtimeID: showtimeID, SeatIds: seatIDs,
		}); err != nil {
			return err
		}

		taken, err := q.FindSeatConflicts(ctx, gen.FindSeatConflictsParams{
			ShowtimeID: showtimeID, SeatIds: seatIDs,
		})
		if err != nil {
			return err
		}
		if len(taken) > 0 {
			return &SeatsUnavailable{SeatIDs: taken}
		}

		held, err := q.CreateHold(ctx, gen.CreateHoldParams{
			Token:       token,
			ShowtimeID:  showtimeID,
			CustomerRef: customerRef,
			TtlSeconds:  int32(s.cfg.HoldTTL.Seconds()),
		})
		if err != nil {
			return err
		}

		claimed, err := q.ClaimSeats(ctx, gen.ClaimSeatsParams{SeatIds: seatIDs, HoldID: held.ID})
		if err != nil {
			return err
		}
		if claimed != int64(len(seatIDs)) {
			return fmt.Errorf("claimed %d of %d seats", claimed, len(seatIDs))
		}

		rows, err := q.GetHoldSeats(ctx, held.ID)
		if err != nil {
			return err
		}

		result = Hold{
			Token:      held.Token,
			ShowtimeID: showtimeID,
			Status:     HoldLive,
			Seats:      seatsFromHold(rows, SeatHeld),
			ExpiresAt:  held.ExpiresAt,
			TotalMinor: totalMinor(showtime.PriceMinor, len(seatIDs)),
			Currency:   showtime.Currency,
		}
		return nil
	})
	if err != nil {
		return nil, holdError(err, seatIDs)
	}
	return &result, nil
}

// GetHold reports a hold and its seats. No lock, so this is a snapshot rather
// than an authority.
func (s *Service) GetHold(ctx context.Context, token string) (*Hold, error) {
	if !validHoldToken(token) {
		return nil, ErrNotFound
	}
	held, err := s.store.GetHoldByToken(ctx, token)
	if err != nil {
		return nil, fromStore(store.Classify(err))
	}
	showtime, err := s.store.GetShowtime(ctx, held.ShowtimeID)
	if err != nil {
		return nil, fromStore(store.Classify(err))
	}
	rows, err := s.store.GetHoldSeats(ctx, held.ID)
	if err != nil {
		return nil, fromStore(store.Classify(err))
	}

	status := holdStatus(held)
	seatStatus := SeatHeld
	if status == HoldConfirmed {
		seatStatus = SeatSold
	}

	return &Hold{
		Token:      held.Token,
		ShowtimeID: held.ShowtimeID,
		Status:     status,
		Seats:      seatsFromHold(rows, seatStatus),
		ExpiresAt:  held.ExpiresAt,
		TotalMinor: totalMinor(showtime.PriceMinor, len(rows)),
		Currency:   showtime.Currency,
	}, nil
}

// Release gives seats back before the TTL lapses.
func (s *Service) Release(ctx context.Context, token string) error {
	if !validHoldToken(token) {
		return ErrNotFound
	}
	held, err := s.store.GetHoldByToken(ctx, token)
	if err != nil {
		return fromStore(store.Classify(err))
	}

	return fromStore(s.store.InShowtimeTx(ctx, held.ShowtimeID, func(ctx context.Context, q *gen.Queries) error {
		held, err := q.GetHoldByToken(ctx, token)
		if err != nil {
			return err
		}
		if held.ConfirmedAt != nil {
			return ErrAlreadyConfirmed
		}
		if _, err := q.ReleaseHoldSeats(ctx, held.ID); err != nil {
			return err
		}
		_, err = q.ReleaseHold(ctx, held.ID)
		return err
	}))
}

func holdStatus(h gen.GetHoldByTokenRow) HoldStatus {
	switch {
	case h.ReleasedAt != nil:
		return HoldReleased
	case h.ConfirmedAt != nil:
		return HoldConfirmed
	case !h.Live:
		return HoldExpired
	}
	return HoldLive
}

func seatsFromHold(rows []gen.GetHoldSeatsRow, status SeatStatus) []Seat {
	seats := make([]Seat, 0, len(rows))
	for _, r := range rows {
		seats = append(seats, Seat{
			ID: r.SeatID, Row: r.RowLabel, Num: r.SeatNum, Kind: r.Kind, Status: status,
		})
	}
	return seats
}

// A lost race with the unique index still reads as a seat conflict to the
// client, but per section 4.1 reaching that branch at all is a bug signal.
func holdError(err error, requested []int64) error {
	if errors.Is(err, store.ErrSeatRaceLost) {
		return &SeatsUnavailable{SeatIDs: requested, cause: err}
	}
	return fromStore(err)
}

func normalizeSeats(ids []int64, max int) ([]int64, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: no seats requested", ErrInvalidSelection)
	}
	if len(ids) > max {
		return nil, fmt.Errorf("%w: %d seats requested, at most %d per hold", ErrInvalidSelection, len(ids), max)
	}

	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("%w: seat id %d is not valid", ErrInvalidSelection, id)
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("%w: seat %d requested twice", ErrInvalidSelection, id)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}
