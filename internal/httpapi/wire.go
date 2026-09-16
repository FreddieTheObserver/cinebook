package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/FreddieTheObserver/cinebook/internal/booking"
)

type holdRequest struct {
	SeatIDs []int64 `json:"seat_ids"`
}

type movieJSON struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	RuntimeMin int32  `json:"runtime_min"`
	Rating     string `json:"rating"`
}

type showtimeJSON struct {
	ID             int64     `json:"id"`
	MovieID        int64     `json:"movie_id"`
	MovieTitle     string    `json:"movie_title"`
	AuditoriumID   int64     `json:"auditorium_id"`
	AuditoriumName string    `json:"auditorium_name"`
	StartsAt       time.Time `json:"starts_at"`
	EndsAt         time.Time `json:"ends_at"`
	PriceMinor     int64     `json:"price_minor"`
	Currency       string    `json:"currency"`
	SalesOpen      bool      `json:"sales_open"`
}

type seatMapJSON struct {
	ShowtimeID int64         `json:"showtime_id"`
	PriceMinor int64         `json:"price_minor"`
	Currency   string        `json:"currency"`
	Rows       []seatRowJSON `json:"rows"`
}

type seatRowJSON struct {
	Label string           `json:"label"`
	Seats []seatStatusJSON `json:"seats"`
}

type seatStatusJSON struct {
	ID     int64  `json:"id"`
	Number int32  `json:"number"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

// Seats inside a hold or booking carry no status of their own, because the
// hold's status is the one that means something.
type seatJSON struct {
	ID     int64  `json:"id"`
	Row    string `json:"row"`
	Number int32  `json:"number"`
	Kind   string `json:"kind"`
}

type holdJSON struct {
	Token      string     `json:"token"`
	ShowtimeID int64      `json:"showtime_id"`
	Status     string     `json:"status"`
	Seats      []seatJSON `json:"seats"`
	ExpiresAt  time.Time  `json:"expires_at"`
	TotalMinor int64      `json:"total_minor"`
	Currency   string     `json:"currency"`
}

type bookingJSON struct {
	Ref        string     `json:"ref"`
	ShowtimeID int64      `json:"showtime_id"`
	Seats      []seatJSON `json:"seats"`
	TotalMinor int64      `json:"total_minor"`
	Currency   string     `json:"currency"`
	CreatedAt  time.Time  `json:"created_at"`
}

func moviesFrom(movies []booking.Movie) []movieJSON {
	out := make([]movieJSON, 0, len(movies))
	for _, m := range movies {
		out = append(out, movieJSON{ID: m.ID, Title: m.Title, RuntimeMin: m.RuntimeMin, Rating: m.Rating})
	}
	return out
}

func showtimesFrom(showtimes []booking.Showtime) []showtimeJSON {
	out := make([]showtimeJSON, 0, len(showtimes))
	for _, s := range showtimes {
		out = append(out, showtimeJSON{
			ID:             s.ID,
			MovieID:        s.MovieID,
			MovieTitle:     s.MovieTitle,
			AuditoriumID:   s.AuditoriumID,
			AuditoriumName: s.AuditoriumName,
			StartsAt:       s.StartsAt.UTC(),
			EndsAt:         s.EndsAt.UTC(),
			PriceMinor:     s.PriceMinor,
			Currency:       s.Currency,
			SalesOpen:      s.SalesOpen,
		})
	}
	return out
}

func seatMapFrom(m *booking.SeatMap) seatMapJSON {
	rows := make([]seatRowJSON, 0, len(m.Rows))
	for _, row := range m.Rows {
		seats := make([]seatStatusJSON, 0, len(row.Seats))
		for _, s := range row.Seats {
			seats = append(seats, seatStatusJSON{ID: s.ID, Number: s.Num, Kind: s.Kind, Status: string(s.Status)})
		}
		rows = append(rows, seatRowJSON{Label: row.Label, Seats: seats})
	}
	return seatMapJSON{ShowtimeID: m.ShowtimeID, PriceMinor: m.PriceMinor, Currency: m.Currency, Rows: rows}
}

func seatsFrom(seats []booking.Seat) []seatJSON {
	out := make([]seatJSON, 0, len(seats))
	for _, s := range seats {
		out = append(out, seatJSON{ID: s.ID, Row: s.Row, Number: s.Num, Kind: s.Kind})
	}
	return out
}

func holdFrom(h *booking.Hold) holdJSON {
	return holdJSON{
		Token:      h.Token,
		ShowtimeID: h.ShowtimeID,
		Status:     string(h.Status),
		Seats:      seatsFrom(h.Seats),
		ExpiresAt:  h.ExpiresAt.UTC(),
		TotalMinor: h.TotalMinor,
		Currency:   h.Currency,
	}
}

func bookingFrom(b *booking.Booking) bookingJSON {
	return bookingJSON{
		Ref:        b.Ref,
		ShowtimeID: b.ShowtimeID,
		Seats:      seatsFrom(b.Seats),
		TotalMinor: b.TotalMinor,
		Currency:   b.Currency,
		CreatedAt:  b.CreatedAt.UTC(),
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) error {
	return writeBody(w, status, "application/json", v)
}

// writeBody fails only when v cannot be encoded, and then nothing has been
// written, so the caller can still answer with a problem.
func writeBody(w http.ResponseWriter, status int, contentType string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode response: %w", err)
	}

	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("X-Content-Type-Options", "nosniff")
	// Seat maps go stale in seconds, and holds and bookings carry bearer
	// capabilities, so nothing here belongs in a shared cache.
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	// A failed write means the client is gone, and there is nobody to tell.
	_, _ = w.Write(append(body, '\n'))
	return nil
}
