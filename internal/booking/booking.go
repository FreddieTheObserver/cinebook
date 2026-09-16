// Package booking holds the domain operations: hold, confirm, release, expire,
// and the seat map projection.
package booking

import (
	"time"

	"github.com/FreddieTheObserver/cinebook/internal/store"
)

const (
	DefaultHoldTTL         = 7 * time.Minute
	DefaultMaxSeatsPerHold = 10
)

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

type Service struct {
	store *store.Store
	cfg   Config
}

func New(s *store.Store, cfg Config) *Service {
	cfg.applyDefaults()
	return &Service{store: s, cfg: cfg}
}

type SeatStatus string

const (
	SeatFree SeatStatus = "free"
	SeatHeld SeatStatus = "held"
	SeatSold SeatStatus = "sold"
)

type HoldStatus string

const (
	HoldLive      HoldStatus = "live"
	HoldExpired   HoldStatus = "expired"
	HoldConfirmed HoldStatus = "confirmed"
	HoldReleased  HoldStatus = "released"
)

type Seat struct {
	ID     int64
	Row    string
	Num    int32
	Kind   string
	Status SeatStatus
}

type SeatRow struct {
	Label string
	Seats []Seat
}

type SeatMap struct {
	ShowtimeID int64
	PriceMinor int64
	Currency   string
	Rows       []SeatRow
}

type Hold struct {
	Token      string
	ShowtimeID int64
	Status     HoldStatus
	Seats      []Seat
	ExpiresAt  time.Time
	TotalMinor int64
	Currency   string
}

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
