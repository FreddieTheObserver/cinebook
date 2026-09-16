package booking

import (
	"time"

	"github.com/FreddieTheObserver/cinebook/internal/store"
)

// Defaults applied to any Config field left unset.
const (
	DefaultHoldTTL         = 7 * time.Minute
	DefaultMaxSeatsPerHold = 10
)

// Config tunes hold lifetime and how many seats one hold may cover.
type Config struct {
	HoldTTL         time.Duration
	MaxSeatsPerHold int
}

func (c *Config) applyDefaults() {
	if c.HoldTTL <= 0 {
		c.HoldTTL = DefaultHoldTTL
	}
	if c.MaxSeatsPerHold <= 0 {
		c.MaxSeatsPerHold = DefaultMaxSeatsPerHold
	}
}

// Service is the domain entry point for holding, confirming and releasing seats.
type Service struct {
	store *store.Store
	cfg   Config
}

// New builds a Service, filling in defaults for anything Config leaves unset.
func New(s *store.Store, cfg Config) *Service {
	cfg.applyDefaults()
	return &Service{store: s, cfg: cfg}
}

// SeatStatus is a seat's state for one showtime.
type SeatStatus string

// The states a seat can be in.
const (
	SeatFree SeatStatus = "free"
	SeatHeld SeatStatus = "held"
	SeatSold SeatStatus = "sold"
)

// HoldStatus is where a hold sits in its lifecycle.
type HoldStatus string

// The states a hold can be in.
const (
	HoldLive      HoldStatus = "live"
	HoldExpired   HoldStatus = "expired"
	HoldConfirmed HoldStatus = "confirmed"
	HoldReleased  HoldStatus = "released"
)

// Seat is one seat together with its status for a showtime.
type Seat struct {
	ID     int64
	Row    string
	Num    int32
	Kind   string
	Status SeatStatus
}

// SeatRow is one labelled row of an auditorium.
type SeatRow struct {
	Label string
	Seats []Seat
}

// SeatMap is the seating of one showtime, priced.
type SeatMap struct {
	ShowtimeID int64
	PriceMinor int64
	Currency   string
	Rows       []SeatRow
}

// Hold is a claim on seats that lapses at ExpiresAt unless it is confirmed.
type Hold struct {
	Token      string
	ShowtimeID int64
	Status     HoldStatus
	Seats      []Seat
	ExpiresAt  time.Time
	TotalMinor int64
	Currency   string
}

// Booking is a hold that was confirmed before it lapsed.
type Booking struct {
	Ref        string
	ShowtimeID int64
	Seats      []Seat
	TotalMinor int64
	Currency   string
	CreatedAt  time.Time
}

func totalMinor(priceMinor int64, seats int) int64 {
	return priceMinor * int64(seats)
}
