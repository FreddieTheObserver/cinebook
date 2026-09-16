package booking

import (
	"context"
	"errors"

	"github.com/FreddieTheObserver/cinebook/internal/store"
	"github.com/FreddieTheObserver/cinebook/internal/store/gen"
)

// Sweep reclaims lapsed holds ahead of the next contender for their seats. It
// keeps the table small and gives expiry a number to report. Correctness does
// not depend on it running, so a busy showtime is skipped rather than waited on.
func (s *Service) Sweep(ctx context.Context, limit int32) (int64, error) {
	showtimes, err := s.store.ListShowtimesWithExpiredSeats(ctx, limit)
	if err != nil {
		return 0, fromStore(store.Classify(err))
	}

	var reclaimed int64
	for _, showtimeID := range showtimes {
		err := s.store.InShowtimeTx(ctx, showtimeID, func(ctx context.Context, q *gen.Queries) error {
			n, err := q.ReclaimExpiredSeatsForShowtime(ctx, showtimeID)
			if err != nil {
				return err
			}
			reclaimed += n
			return nil
		})
		switch {
		case err == nil:
		case errors.Is(err, store.ErrBusy):
			// A real request holds the lock and will reclaim these seats itself.
			continue
		default:
			return reclaimed, fromStore(err)
		}
	}
	return reclaimed, nil
}
