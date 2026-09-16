package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/FreddieTheObserver/cinebook/internal/store/gen"
)

//go:embed seed/*.sql
var seedFS embed.FS

var testDSN string

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:18",
		postgres.WithDatabase("cinebook"),
		postgres.WithUsername("cinebook"),
		postgres.WithPassword("cinebook"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres: %v\n", err)
		os.Exit(1)
	}

	testDSN, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "connection string: %v\n", err)
		os.Exit(1)
	}
	if err := setupSchema(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "setup: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	if err := testcontainers.TerminateContainer(container); err != nil {
		fmt.Fprintf(os.Stderr, "terminate: %v\n", err)
	}
	os.Exit(code)
}

func setupSchema(ctx context.Context) error {
	s, err := New(ctx, Config{DSN: testDSN})
	if err != nil {
		return err
	}
	defer s.Close()

	if err := s.Migrate(ctx); err != nil {
		return err
	}
	seed, err := seedFS.ReadFile("seed/0001_one_cinema.sql")
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, string(seed)); err != nil {
		return fmt.Errorf("apply seed: %w", err)
	}
	return nil
}

func newTestStore(t *testing.T) *Store {
	t.Helper()

	s, err := New(t.Context(), Config{
		DSN:              testDSN,
		MaxConns:         8,
		LockTimeout:      300 * time.Millisecond,
		StatementTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(s.Close)

	const reset = `TRUNCATE bookings, seat_occupancy, holds RESTART IDENTITY CASCADE`
	if _, err := s.pool.Exec(t.Context(), reset); err != nil {
		t.Fatalf("reset booking state: %v", err)
	}
	return s
}

func showtimeWithSeats(t *testing.T, s *Store, n int32) (int64, []int64) {
	t.Helper()

	const q = `
SELECT st.id,
       (SELECT array_agg(picked.id)
          FROM (SELECT seat.id FROM seats seat
                 WHERE seat.auditorium_id = st.auditorium_id
                 ORDER BY seat.id LIMIT $1) picked)
  FROM showtimes st
 ORDER BY st.id
 LIMIT 1`

	var showtimeID int64
	var seatIDs []int64
	if err := s.pool.QueryRow(t.Context(), q, n).Scan(&showtimeID, &seatIDs); err != nil {
		t.Fatalf("pick showtime: %v", err)
	}
	if len(seatIDs) != int(n) {
		t.Fatalf("got %d seats, want %d", len(seatIDs), n)
	}
	return showtimeID, seatIDs
}

func token(c string) string { return strings.Repeat(c, 26) }

func hold(t *testing.T, ctx context.Context, q *gen.Queries, showtimeID int64, tok, customer string, ttl int32, seats []int64) gen.Hold {
	t.Helper()

	h, err := q.CreateHold(ctx, gen.CreateHoldParams{
		Token:       tok,
		ShowtimeID:  showtimeID,
		CustomerRef: customer,
		TtlSeconds:  ttl,
	})
	if err != nil {
		t.Fatalf("create hold: %v", err)
	}
	if len(seats) > 0 {
		n, err := q.ClaimSeats(ctx, gen.ClaimSeatsParams{SeatIds: seats, HoldID: h.ID})
		if err != nil {
			t.Fatalf("claim seats: %v", err)
		}
		if n != int64(len(seats)) {
			t.Fatalf("claimed %d seats, want %d", n, len(seats))
		}
	}
	return h
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestConcurrentMigrationsOnAFreshDatabase(t *testing.T) {
	admin := newTestStore(t)
	const name = "migrate_race"
	if _, err := admin.pool.Exec(t.Context(), "DROP DATABASE IF EXISTS "+name); err != nil {
		t.Fatalf("drop database: %v", err)
	}
	if _, err := admin.pool.Exec(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	dsn := strings.Replace(testDSN, "/cinebook?", "/"+name+"?", 1)
	if dsn == testDSN {
		t.Fatalf("could not point %q at the fresh database", testDSN)
	}
	const replicas = 6
	errs := make(chan error, replicas)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range replicas {
		wg.Go(func() {
			s, err := New(t.Context(), Config{DSN: dsn, MaxConns: 4})
			if err != nil {
				errs <- err
				return
			}
			defer s.Close()
			<-start
			errs <- s.Migrate(t.Context())
		})
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("replica migration failed: %v", err)
		}
	}
}

func TestHoldMarksSeatsHeld(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	showtimeID, seats := showtimeWithSeats(t, s, 3)

	err := s.InShowtimeTx(ctx, showtimeID, func(ctx context.Context, q *gen.Queries) error {
		hold(t, ctx, q, showtimeID, token("A"), "cust-a", 420, seats)
		return nil
	})
	if err != nil {
		t.Fatalf("hold transaction: %v", err)
	}

	rows, err := s.GetSeatMap(ctx, showtimeID)
	if err != nil {
		t.Fatalf("seat map: %v", err)
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.Status]++
	}
	if counts["held"] != len(seats) {
		t.Fatalf("held=%d, want %d (counts: %v)", counts["held"], len(seats), counts)
	}
}

func TestDoubleClaimHitsTheUniqueIndex(t *testing.T) {
	s := newTestStore(t)
	showtimeID, seats := showtimeWithSeats(t, s, 1)

	err := s.InShowtimeTx(t.Context(), showtimeID, func(ctx context.Context, q *gen.Queries) error {
		hold(t, ctx, q, showtimeID, token("A"), "cust-a", 420, seats)

		second, err := q.CreateHold(ctx, gen.CreateHoldParams{
			Token: token("B"), ShowtimeID: showtimeID, CustomerRef: "cust-b", TtlSeconds: 420,
		})
		if err != nil {
			return err
		}
		_, err = q.ClaimSeats(ctx, gen.ClaimSeatsParams{SeatIds: seats, HoldID: second.ID})
		return err
	})
	if !errors.Is(err, ErrSeatRaceLost) {
		t.Fatalf("got %v, want ErrSeatRaceLost", err)
	}
}

func TestExpiredSeatsAreReclaimedInsideTheLock(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	showtimeID, seats := showtimeWithSeats(t, s, 2)

	if err := s.InShowtimeTx(ctx, showtimeID, func(ctx context.Context, q *gen.Queries) error {
		hold(t, ctx, q, showtimeID, token("A"), "cust-a", -1, seats)
		return nil
	}); err != nil {
		t.Fatalf("expired hold: %v", err)
	}

	conflicts, err := s.FindSeatConflicts(ctx, gen.FindSeatConflictsParams{ShowtimeID: showtimeID, SeatIds: seats})
	if err != nil {
		t.Fatalf("find conflicts: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expired hold still live: %v", conflicts)
	}

	err = s.InShowtimeTx(ctx, showtimeID, func(ctx context.Context, q *gen.Queries) error {
		n, err := q.ReclaimExpiredSeats(ctx, gen.ReclaimExpiredSeatsParams{ShowtimeID: showtimeID, SeatIds: seats})
		if err != nil {
			return err
		}
		if n != int64(len(seats)) {
			return fmt.Errorf("reclaimed %d, want %d", n, len(seats))
		}
		hold(t, ctx, q, showtimeID, token("B"), "cust-b", 420, seats)
		return nil
	})
	if err != nil {
		t.Fatalf("reclaim and reclaim-then-hold: %v", err)
	}
}

func TestLockTimeoutClassifiesAsBusy(t *testing.T) {
	s := newTestStore(t)
	showtimeID, _ := showtimeWithSeats(t, s, 1)

	holding := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.InShowtimeTx(context.Background(), showtimeID, func(ctx context.Context, q *gen.Queries) error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding

	err := s.InShowtimeTx(t.Context(), showtimeID, func(ctx context.Context, q *gen.Queries) error {
		return errors.New("body must not run while another transaction holds the lock")
	})
	close(release)
	wg.Wait()

	if !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v, want ErrBusy", err)
	}
}

func TestConfirmRejectsAnExpiredHold(t *testing.T) {
	s := newTestStore(t)
	showtimeID, seats := showtimeWithSeats(t, s, 2)

	err := s.InShowtimeTx(t.Context(), showtimeID, func(ctx context.Context, q *gen.Queries) error {
		h := hold(t, ctx, q, showtimeID, token("A"), "cust-a", -1, seats)

		n, err := q.ConfirmHoldSeats(ctx, h.ID)
		if err != nil {
			return err
		}
		if n != 0 {
			return fmt.Errorf("confirmed %d seats on an expired hold, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
}

func TestConfirmedSeatsOutliveTheirExpiry(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	showtimeID, seats := showtimeWithSeats(t, s, 2)

	var holdID int64
	err := s.InShowtimeTx(ctx, showtimeID, func(ctx context.Context, q *gen.Queries) error {
		h := hold(t, ctx, q, showtimeID, token("A"), "cust-a", 420, seats)
		holdID = h.ID

		n, err := q.ConfirmHoldSeats(ctx, h.ID)
		if err != nil {
			return err
		}
		if n != int64(len(seats)) {
			return fmt.Errorf("confirmed %d seats, want %d", n, len(seats))
		}
		if _, err := q.ConfirmHold(ctx, h.ID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}

	const expire = `UPDATE seat_occupancy SET expires_at = now() - interval '1 hour' WHERE hold_id = $1`
	if _, err := s.pool.Exec(ctx, expire, holdID); err != nil {
		t.Fatalf("force expiry: %v", err)
	}

	conflicts, err := s.FindSeatConflicts(ctx, gen.FindSeatConflictsParams{ShowtimeID: showtimeID, SeatIds: seats})
	if err != nil {
		t.Fatalf("find conflicts: %v", err)
	}
	if len(conflicts) != len(seats) {
		t.Fatalf("confirmed seats dropped out of the live view: got %d, want %d", len(conflicts), len(seats))
	}
}

// insertBooking writes without ON CONFLICT, so the constraint actually raises
// and Classify has something to map.
func insertBooking(t *testing.T, s *Store, ref string, holdID, showtimeID int64, customer, key string) error {
	t.Helper()

	const q = `
INSERT INTO bookings (ref, hold_id, showtime_id, customer_ref, total_minor, currency, idempotency_key)
VALUES ($1, $2, $3, $4, 22000, 'THB', $5)`
	_, err := s.pool.Exec(t.Context(), q, ref, holdID, showtimeID, customer, key)
	return err
}

func TestDuplicateIdempotencyKeyIsClassified(t *testing.T) {
	s := newTestStore(t)
	showtimeID, seats := showtimeWithSeats(t, s, 2)

	var first, second gen.Hold
	if err := s.InShowtimeTx(t.Context(), showtimeID, func(ctx context.Context, q *gen.Queries) error {
		first = hold(t, ctx, q, showtimeID, token("A"), "cust-a", 420, seats[:1])
		second = hold(t, ctx, q, showtimeID, token("B"), "cust-a", 420, seats[1:])
		return nil
	}); err != nil {
		t.Fatalf("holds: %v", err)
	}

	if err := insertBooking(t, s, "CB-7K2M-9QX4", first.ID, showtimeID, "cust-a", "same-key"); err != nil {
		t.Fatalf("first booking: %v", err)
	}
	err := insertBooking(t, s, "CB-7K2M-9QX5", second.ID, showtimeID, "cust-a", "same-key")
	if !errors.Is(Classify(err), ErrDuplicateBooking) {
		t.Fatalf("got %v, want ErrDuplicateBooking", err)
	}
}

func TestOneBookingPerHold(t *testing.T) {
	s := newTestStore(t)
	showtimeID, seats := showtimeWithSeats(t, s, 1)

	var h gen.Hold
	if err := s.InShowtimeTx(t.Context(), showtimeID, func(ctx context.Context, q *gen.Queries) error {
		h = hold(t, ctx, q, showtimeID, token("A"), "cust-a", 420, seats)
		return nil
	}); err != nil {
		t.Fatalf("hold: %v", err)
	}

	if err := insertBooking(t, s, "CB-7K2M-9QX4", h.ID, showtimeID, "cust-a", "key-one"); err != nil {
		t.Fatalf("first booking: %v", err)
	}
	err := insertBooking(t, s, "CB-7K2M-9QX5", h.ID, showtimeID, "cust-a", "key-two")
	if !errors.Is(Classify(err), ErrHoldAlreadyBooked) {
		t.Fatalf("got %v, want ErrHoldAlreadyBooked", err)
	}
}

// CreateBooking carries ON CONFLICT DO NOTHING, so a conflict must come back as
// an empty result with the transaction still usable.
func TestCreateBookingSuppressesConflict(t *testing.T) {
	s := newTestStore(t)
	showtimeID, seats := showtimeWithSeats(t, s, 2)

	err := s.InShowtimeTx(t.Context(), showtimeID, func(ctx context.Context, q *gen.Queries) error {
		first := hold(t, ctx, q, showtimeID, token("A"), "cust-a", 420, seats[:1])
		second := hold(t, ctx, q, showtimeID, token("B"), "cust-a", 420, seats[1:])

		book := func(h gen.Hold, ref, key string) error {
			_, err := q.CreateBooking(ctx, gen.CreateBookingParams{
				Ref: ref, HoldID: h.ID, ShowtimeID: showtimeID, CustomerRef: "cust-a",
				TotalMinor: 22000, Currency: "THB", IdempotencyKey: key,
			})
			return err
		}
		if err := book(first, "CB-7K2M-9QX4", "same-key"); err != nil {
			return fmt.Errorf("first booking: %w", err)
		}
		if err := book(second, "CB-7K2M-9QX5", "same-key"); !IsInsertConflict(err) {
			return fmt.Errorf("second booking returned %v, want a suppressed conflict", err)
		}

		// The transaction must still be usable after the suppressed conflict.
		if _, err := q.GetBookingByHold(ctx, first.ID); err != nil {
			return fmt.Errorf("transaction unusable after conflict: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("conflict handling: %v", err)
	}
}

func TestMissingRowClassifiesAsNotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetHoldByToken(t.Context(), token("Z"))
	if !errors.Is(Classify(err), ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
