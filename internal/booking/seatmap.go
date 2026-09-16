package booking

import (
	"context"

	"github.com/FreddieTheObserver/cinebook/internal/store"
	"github.com/FreddieTheObserver/cinebook/internal/store/gen"
)

// SeatMap reads through the liveness view and takes no lock, so it may be
// marginally stale. The hold request is the authority.
func (s *Service) SeatMap(ctx context.Context, showtimeID int64) (*SeatMap, error) {
	showtime, err := s.store.GetShowtime(ctx, showtimeID)
	if err != nil {
		return nil, fromStore(store.Classify(err))
	}
	rows, err := s.store.GetSeatMap(ctx, showtimeID)
	if err != nil {
		return nil, fromStore(store.Classify(err))
	}

	return &SeatMap{
		ShowtimeID: showtimeID,
		PriceMinor: showtime.PriceMinor,
		Currency:   showtime.Currency,
		Rows:       projectSeatRows(rows),
	}, nil
}

// projectSeatRows groups the flat result into rows. The query orders by row
// label then seat number, so one pass is enough.
func projectSeatRows(rows []gen.GetSeatMapRow) []SeatRow {
	out := []SeatRow{}
	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1].Label != r.RowLabel {
			out = append(out, SeatRow{Label: r.RowLabel})
		}
		current := &out[len(out)-1]
		current.Seats = append(current.Seats, Seat{
			ID: r.ID, Row: r.RowLabel, Num: r.SeatNum, Kind: r.Kind, Status: SeatStatus(r.Status),
		})
	}
	return out
}
