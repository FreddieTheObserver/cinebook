package booking

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/FreddieTheObserver/cinebook/internal/store"
)

const seedPath = "../store/seed/0001_one_cinema.sql"

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
	s, err := store.New(ctx, store.Config{DSN: testDSN})
	if err != nil {
		return err
	}
	defer s.Close()

	if err := s.Migrate(ctx); err != nil {
		return err
	}
	seed, err := os.ReadFile(seedPath)
	if err != nil {
		return fmt.Errorf("read seed: %w", err)
	}
	if _, err := s.Pool().Exec(ctx, string(seed)); err != nil {
		return fmt.Errorf("apply seed: %w", err)
	}
	return nil
}

type options struct {
	ttl         time.Duration
	lockTimeout time.Duration
	maxConns    int32
}

func newService(t *testing.T, opt options) (*Service, *store.Store) {
	t.Helper()

	if opt.ttl == 0 {
		opt.ttl = time.Minute
	}
	if opt.lockTimeout == 0 {
		opt.lockTimeout = 2 * time.Second
	}
	if opt.maxConns == 0 {
		opt.maxConns = 8
	}

	st, err := store.New(t.Context(), store.Config{
		DSN:              testDSN,
		MaxConns:         opt.maxConns,
		LockTimeout:      opt.lockTimeout,
		StatementTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(st.Close)

	const reset = `TRUNCATE bookings, seat_occupancy, holds RESTART IDENTITY CASCADE`
	if _, err := st.Pool().Exec(t.Context(), reset); err != nil {
		t.Fatalf("reset booking state: %v", err)
	}

	return New(st, Config{HoldTTL: opt.ttl}), st
}

func showtimeSeats(t *testing.T, st *store.Store, n int32) (int64, []int64) {
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
	if err := st.Pool().QueryRow(t.Context(), q, n).Scan(&showtimeID, &seatIDs); err != nil {
		t.Fatalf("pick showtime: %v", err)
	}
	return showtimeID, seatIDs
}

func countRows(t *testing.T, st *store.Store, query string, args ...any) int64 {
	t.Helper()

	var n int64
	if err := st.Pool().QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestHoldThenConfirm(t *testing.T) {
	svc, st := newService(t, options{})
	ctx := t.Context()
	showtimeID, seats := showtimeSeats(t, st, 3)

	held, err := svc.Hold(ctx, showtimeID, "cust-a", seats)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if held.Status != HoldLive || len(held.Seats) != 3 {
		t.Fatalf("unexpected hold: %+v", held)
	}
	price := countRows(t, st, `SELECT price_minor FROM showtimes WHERE id = $1`, showtimeID)
	if held.TotalMinor != 3*price || held.Currency != "THB" {
		t.Fatalf("got %d %s, want %d THB", held.TotalMinor, held.Currency, 3*price)
	}
	if !holdTokenPattern.MatchString(held.Token) {
		t.Fatalf("token %q malformed", held.Token)
	}

	booked, err := svc.Confirm(ctx, held.Token, "key-1")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !bookingRefPattern.MatchString(booked.Ref) {
		t.Fatalf("ref %q malformed", booked.Ref)
	}
	if booked.TotalMinor != held.TotalMinor || len(booked.Seats) != 3 {
		t.Fatalf("booking does not match the hold: %+v", booked)
	}

	found, err := svc.GetBooking(ctx, booked.Ref)
	if err != nil {
		t.Fatalf("get booking: %v", err)
	}
	if found.Ref != booked.Ref {
		t.Fatalf("got %q, want %q", found.Ref, booked.Ref)
	}
}

func TestHoldNamesTheSeatsThatBlockedIt(t *testing.T) {
	svc, st := newService(t, options{})
	ctx := t.Context()
	showtimeID, seats := showtimeSeats(t, st, 3)

	if _, err := svc.Hold(ctx, showtimeID, "cust-a", seats[:2]); err != nil {
		t.Fatalf("first hold: %v", err)
	}

	_, err := svc.Hold(ctx, showtimeID, "cust-b", seats)
	var unavailable *SeatsUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("got %v, want SeatsUnavailable", err)
	}
	if len(unavailable.SeatIDs) != 2 {
		t.Fatalf("got %v, want the two held seats %v", unavailable.SeatIDs, seats[:2])
	}
}

func TestHoldRejectsASeatFromAnotherAuditorium(t *testing.T) {
	svc, st := newService(t, options{})
	ctx := t.Context()
	showtimeID, seats := showtimeSeats(t, st, 1)

	const foreign = `
SELECT seat.id FROM seats seat
 WHERE seat.auditorium_id <> (SELECT auditorium_id FROM showtimes WHERE id = $1)
 ORDER BY seat.id LIMIT 1`
	var foreignSeat int64
	if err := st.Pool().QueryRow(ctx, foreign, showtimeID).Scan(&foreignSeat); err != nil {
		t.Fatalf("pick foreign seat: %v", err)
	}

	_, err := svc.Hold(ctx, showtimeID, "cust-a", []int64{seats[0], foreignSeat})
	if !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("got %v, want ErrInvalidSelection", err)
	}
}

func TestReleaseFreesSeatsImmediately(t *testing.T) {
	svc, st := newService(t, options{})
	ctx := t.Context()
	showtimeID, seats := showtimeSeats(t, st, 2)

	held, err := svc.Hold(ctx, showtimeID, "cust-a", seats)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if err := svc.Release(ctx, held.Token); err != nil {
		t.Fatalf("release: %v", err)
	}

	again, err := svc.GetHold(ctx, held.Token)
	if err != nil {
		t.Fatalf("get hold: %v", err)
	}
	if again.Status != HoldReleased {
		t.Fatalf("got status %q, want released", again.Status)
	}
	if _, err := svc.Hold(ctx, showtimeID, "cust-b", seats); err != nil {
		t.Fatalf("second customer could not take the released seats: %v", err)
	}
}

func TestReleaseRejectsAConfirmedHold(t *testing.T) {
	svc, st := newService(t, options{})
	ctx := t.Context()
	showtimeID, seats := showtimeSeats(t, st, 1)

	held, err := svc.Hold(ctx, showtimeID, "cust-a", seats)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if _, err := svc.Confirm(ctx, held.Token, "key-1"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if err := svc.Release(ctx, held.Token); !errors.Is(err, ErrAlreadyConfirmed) {
		t.Fatalf("got %v, want ErrAlreadyConfirmed", err)
	}
}

func TestConfirmJustBeforeExpiry(t *testing.T) {
	svc, st := newService(t, options{ttl: 5 * time.Second})
	ctx := t.Context()
	showtimeID, seats := showtimeSeats(t, st, 1)

	held, err := svc.Hold(ctx, showtimeID, "cust-a", seats)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if _, err := svc.Confirm(ctx, held.Token, "key-1"); err != nil {
		t.Fatalf("confirm inside the TTL failed: %v", err)
	}
}

func TestConfirmJustAfterExpiry(t *testing.T) {
	svc, st := newService(t, options{ttl: time.Second})
	ctx := t.Context()
	showtimeID, seats := showtimeSeats(t, st, 1)

	held, err := svc.Hold(ctx, showtimeID, "cust-a", seats)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)

	if _, err := svc.Confirm(ctx, held.Token, "key-1"); !errors.Is(err, ErrHoldExpired) {
		t.Fatalf("got %v, want ErrHoldExpired", err)
	}
	if got := countRows(t, st, `SELECT count(*) FROM bookings`); got != 0 {
		t.Fatalf("expired confirm created %d bookings", got)
	}
}

func TestExpiredHoldIsReclaimedByTheNextContender(t *testing.T) {
	svc, st := newService(t, options{ttl: time.Second})
	ctx := t.Context()
	showtimeID, seats := showtimeSeats(t, st, 2)

	first, err := svc.Hold(ctx, showtimeID, "cust-a", seats)
	if err != nil {
		t.Fatalf("first hold: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)

	if _, err := svc.Hold(ctx, showtimeID, "cust-b", seats); err != nil {
		t.Fatalf("second hold should have reclaimed the lapsed seats: %v", err)
	}

	lapsed, err := svc.GetHold(ctx, first.Token)
	if err != nil {
		t.Fatalf("get lapsed hold: %v", err)
	}
	if lapsed.Status != HoldExpired && lapsed.Status != HoldReleased {
		t.Fatalf("got status %q, want expired or released", lapsed.Status)
	}
}

func TestConfirmReplayReturnsTheSameBooking(t *testing.T) {
	svc, st := newService(t, options{})
	ctx := t.Context()
	showtimeID, seats := showtimeSeats(t, st, 2)

	held, err := svc.Hold(ctx, showtimeID, "cust-a", seats)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}

	first, err := svc.Confirm(ctx, held.Token, "key-1")
	if err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	second, err := svc.Confirm(ctx, held.Token, "key-1")
	if err != nil {
		t.Fatalf("replayed confirm: %v", err)
	}
	if first.Ref != second.Ref {
		t.Fatalf("replay produced a different reference: %q then %q", first.Ref, second.Ref)
	}
	if got := countRows(t, st, `SELECT count(*) FROM bookings`); got != 1 {
		t.Fatalf("got %d bookings, want 1", got)
	}
}

func TestConcurrentConfirmMakesExactlyOneBooking(t *testing.T) {
	svc, st := newService(t, options{lockTimeout: 20 * time.Second, maxConns: 16})
	ctx := t.Context()
	showtimeID, seats := showtimeSeats(t, st, 2)

	held, err := svc.Hold(ctx, showtimeID, "cust-a", seats)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}

	const racers = 16
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		refs = map[string]int{}
		bad  []error
	)
	start := make(chan struct{})
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			booked, err := svc.Confirm(ctx, held.Token, "key-1")

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				bad = append(bad, err)
				return
			}
			refs[booked.Ref]++
		}()
	}
	close(start)
	wg.Wait()

	if len(bad) > 0 {
		t.Fatalf("%d of %d confirms failed, first: %v", len(bad), racers, bad[0])
	}
	if len(refs) != 1 {
		t.Fatalf("got %d distinct references, want 1: %v", len(refs), refs)
	}
	if got := countRows(t, st, `SELECT count(*) FROM bookings`); got != 1 {
		t.Fatalf("got %d booking rows, want 1", got)
	}
}

func TestIdempotencyKeyCannotBeReusedForAnotherHold(t *testing.T) {
	svc, st := newService(t, options{})
	ctx := t.Context()
	showtimeID, seats := showtimeSeats(t, st, 2)

	first, err := svc.Hold(ctx, showtimeID, "cust-a", seats[:1])
	if err != nil {
		t.Fatalf("first hold: %v", err)
	}
	if _, err := svc.Confirm(ctx, first.Token, "key-1"); err != nil {
		t.Fatalf("first confirm: %v", err)
	}

	second, err := svc.Hold(ctx, showtimeID, "cust-a", seats[1:])
	if err != nil {
		t.Fatalf("second hold: %v", err)
	}
	if _, err := svc.Confirm(ctx, second.Token, "key-1"); !errors.Is(err, ErrIdempotencyKeyReused) {
		t.Fatalf("got %v, want ErrIdempotencyKeyReused", err)
	}

	// The rejected confirm must not have stranded the second hold's seats.
	stranded, err := svc.GetHold(ctx, second.Token)
	if err != nil {
		t.Fatalf("get second hold: %v", err)
	}
	if stranded.Status != HoldLive {
		t.Fatalf("got status %q, want live", stranded.Status)
	}
	if got := countRows(t, st, `SELECT count(*) FROM bookings`); got != 1 {
		t.Fatalf("got %d bookings, want 1", got)
	}
}

func TestSeatMapReportsFreeHeldAndSold(t *testing.T) {
	svc, st := newService(t, options{})
	ctx := t.Context()
	showtimeID, seats := showtimeSeats(t, st, 4)

	sold, err := svc.Hold(ctx, showtimeID, "cust-a", seats[:2])
	if err != nil {
		t.Fatalf("hold to sell: %v", err)
	}
	if _, err := svc.Confirm(ctx, sold.Token, "key-1"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := svc.Hold(ctx, showtimeID, "cust-b", seats[2:]); err != nil {
		t.Fatalf("hold to hold: %v", err)
	}

	seatMap, err := svc.SeatMap(ctx, showtimeID)
	if err != nil {
		t.Fatalf("seat map: %v", err)
	}
	counts := map[SeatStatus]int{}
	total := 0
	for _, row := range seatMap.Rows {
		for _, seat := range row.Seats {
			counts[seat.Status]++
			total++
		}
	}
	if counts[SeatSold] != 2 || counts[SeatHeld] != 2 {
		t.Fatalf("got %v, want 2 sold and 2 held", counts)
	}
	if counts[SeatFree] != total-4 {
		t.Fatalf("got %d free of %d total", counts[SeatFree], total)
	}
	if seatMap.Currency != "THB" {
		t.Fatalf("got currency %q", seatMap.Currency)
	}
}

func TestListMoviesOrdersByTitle(t *testing.T) {
	svc, st := newService(t, options{})

	movies, err := svc.ListMovies(t.Context())
	if err != nil {
		t.Fatalf("list movies: %v", err)
	}
	if want := countRows(t, st, `SELECT count(*) FROM movies`); int64(len(movies)) != want {
		t.Fatalf("got %d movies, want %d", len(movies), want)
	}
	for i := 1; i < len(movies); i++ {
		if movies[i].Title < movies[i-1].Title {
			t.Fatalf("not ordered by title: %q before %q", movies[i-1].Title, movies[i].Title)
		}
	}
}

func TestListShowtimesFilters(t *testing.T) {
	svc, st := newService(t, options{})
	ctx := t.Context()

	all, err := svc.ListShowtimes(ctx, ShowtimeFilter{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if want := countRows(t, st, `SELECT count(*) FROM showtimes`); int64(len(all)) != want {
		t.Fatalf("got %d showtimes, want %d", len(all), want)
	}
	for i := 1; i < len(all); i++ {
		if all[i].StartsAt.Before(all[i-1].StartsAt) {
			t.Fatalf("not ordered by start: %v before %v", all[i-1].StartsAt, all[i].StartsAt)
		}
	}

	movieID := all[0].MovieID
	byMovie, err := svc.ListShowtimes(ctx, ShowtimeFilter{MovieID: movieID})
	if err != nil {
		t.Fatalf("list by movie: %v", err)
	}
	if want := countRows(t, st, `SELECT count(*) FROM showtimes WHERE movie_id = $1`, movieID); int64(len(byMovie)) != want {
		t.Fatalf("got %d showtimes for movie %d, want %d", len(byMovie), movieID, want)
	}
	for _, s := range byMovie {
		if s.MovieID != movieID {
			t.Fatalf("showtime %d is for movie %d, want %d", s.ID, s.MovieID, movieID)
		}
	}

	from, before := all[1].StartsAt, all[3].StartsAt
	window, err := svc.ListShowtimes(ctx, ShowtimeFilter{StartsFrom: from, StartsBefore: before})
	if err != nil {
		t.Fatalf("list window: %v", err)
	}
	if len(window) != 2 || window[0].ID != all[1].ID || window[1].ID != all[2].ID {
		t.Fatalf("window [%v, %v) returned %+v, want showtimes %d and %d", from, before, window, all[1].ID, all[2].ID)
	}
}

func TestSweepReclaimsLapsedHolds(t *testing.T) {
	svc, st := newService(t, options{ttl: time.Second})
	ctx := t.Context()
	showtimeID, seats := showtimeSeats(t, st, 3)

	if _, err := svc.Hold(ctx, showtimeID, "cust-a", seats); err != nil {
		t.Fatalf("hold: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)

	reclaimed, err := svc.Sweep(ctx, 100)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if reclaimed != int64(len(seats)) {
		t.Fatalf("reclaimed %d, want %d", reclaimed, len(seats))
	}

	again, err := svc.Sweep(ctx, 100)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if again != 0 {
		t.Fatalf("second sweep reclaimed %d, want 0", again)
	}
}

// The centrepiece. Two hundred customers reach for the same seat at once, and
// the assertion that matters most is that no unique violation ever surfaced:
// that would mean the serialization layer failed and the index caught it.
func TestTwoHundredRacersForOneSeat(t *testing.T) {
	svc, st := newService(t, options{lockTimeout: 30 * time.Second, maxConns: 25})
	ctx := t.Context()
	showtimeID, seats := showtimeSeats(t, st, 1)

	const racers = 200
	var (
		wg          sync.WaitGroup
		mu          sync.Mutex
		succeeded   int
		unavailable int
		raceLost    int
		busy        int
		other       []error
	)

	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Hold(ctx, showtimeID, fmt.Sprintf("cust-%03d", i), seats)

			mu.Lock()
			defer mu.Unlock()
			var conflict *SeatsUnavailable
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, store.ErrSeatRaceLost):
				raceLost++
			case errors.As(err, &conflict):
				unavailable++
			case errors.Is(err, ErrBusy):
				busy++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if raceLost != 0 {
		t.Errorf("%d racers hit the unique index: the advisory lock did not serialize them", raceLost)
	}
	if succeeded != 1 {
		t.Errorf("got %d successful holds, want exactly 1", succeeded)
	}
	if unavailable != racers-1 {
		t.Errorf("got %d seat conflicts, want %d", unavailable, racers-1)
	}
	if busy != 0 || len(other) > 0 {
		t.Errorf("got %d busy and %d other errors, first: %v", busy, len(other), append(other, nil)[0])
	}

	live := countRows(t, st,
		`SELECT count(*) FROM live_seat_occupancy WHERE showtime_id = $1 AND seat_id = $2`,
		showtimeID, seats[0])
	if live != 1 {
		t.Errorf("got %d live occupancy rows for the seat, want 1", live)
	}
	if holds := countRows(t, st, `SELECT count(*) FROM holds`); holds != 1 {
		t.Errorf("got %d hold rows, want 1", holds)
	}
}
