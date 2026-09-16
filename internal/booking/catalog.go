package booking

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/FreddieTheObserver/cinebook/internal/store"
	"github.com/FreddieTheObserver/cinebook/internal/store/gen"
)

// Movie is one film in the catalog.
type Movie struct {
	ID         int64
	Title      string
	RuntimeMin int32
	Rating     string
}

// Showtime is one screening of a movie in an auditorium.
type Showtime struct {
	ID             int64
	MovieID        int64
	MovieTitle     string
	AuditoriumID   int64
	AuditoriumName string
	StartsAt       time.Time
	EndsAt         time.Time
	PriceMinor     int64
	Currency       string
	SalesOpen      bool
}

// ShowtimeFilter narrows ListShowtimes. A zero field applies no constraint.
type ShowtimeFilter struct {
	MovieID      int64
	StartsFrom   time.Time
	StartsBefore time.Time
}

// ListMovies returns the whole catalog, ordered by title.
func (s *Service) ListMovies(ctx context.Context) ([]Movie, error) {
	rows, err := s.store.ListMovies(ctx)
	if err != nil {
		return nil, fromStore(store.Classify(err))
	}
	movies := make([]Movie, 0, len(rows))
	for _, r := range rows {
		movies = append(movies, Movie{ID: r.ID, Title: r.Title, RuntimeMin: r.RuntimeMin, Rating: r.Rating})
	}
	return movies, nil
}

// ListShowtimes returns screenings in start order.
func (s *Service) ListShowtimes(ctx context.Context, f ShowtimeFilter) ([]Showtime, error) {
	params := gen.ListShowtimesParams{MovieID: pgtype.Int8{Int64: f.MovieID, Valid: f.MovieID != 0}}
	if !f.StartsFrom.IsZero() {
		params.StartsFrom = &f.StartsFrom
	}
	if !f.StartsBefore.IsZero() {
		params.StartsBefore = &f.StartsBefore
	}

	rows, err := s.store.ListShowtimes(ctx, params)
	if err != nil {
		return nil, fromStore(store.Classify(err))
	}
	showtimes := make([]Showtime, 0, len(rows))
	for _, r := range rows {
		showtimes = append(showtimes, showtimeFrom(r))
	}
	return showtimes, nil
}

// GetShowtime returns one screening with its movie and auditorium named.
func (s *Service) GetShowtime(ctx context.Context, id int64) (*Showtime, error) {
	row, err := s.store.GetShowtimeDetail(ctx, id)
	if err != nil {
		return nil, fromStore(store.Classify(err))
	}
	// Both queries select the same columns, so the rows convert.
	showtime := showtimeFrom(gen.ListShowtimesRow(row))
	return &showtime, nil
}

func showtimeFrom(r gen.ListShowtimesRow) Showtime {
	return Showtime{
		ID:             r.ID,
		MovieID:        r.MovieID,
		MovieTitle:     r.MovieTitle,
		AuditoriumID:   r.AuditoriumID,
		AuditoriumName: r.AuditoriumName,
		StartsAt:       r.StartsAt,
		EndsAt:         r.EndsAt,
		PriceMinor:     r.PriceMinor,
		Currency:       r.Currency,
		SalesOpen:      r.SalesOpen,
	}
}
