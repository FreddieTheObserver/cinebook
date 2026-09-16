package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	var (
		base       = flag.String("url", "http://localhost:8080", "base URL of cinebook-api")
		showtimeID = flag.Int64("showtime", 0, "showtime to contend for; 0 picks the first one selling seats")
		levels     = flag.String("levels", "1,4,16,64,128", "comma-separated numbers of concurrent clients")
		duration   = flag.Duration("duration", 10*time.Second, "how long each level runs")
		seats      = flag.Int("seats", 16, "size of the seat pool the clients contend for")
	)
	flag.Parse()

	clients, err := parseLevels(*levels)
	if err == nil && (*duration <= 0 || *seats <= 0) {
		err = errors.New("-duration and -seats must be positive")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "cinebook-loadgen: %v\n", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Stdout, *base, *showtimeID, clients, *duration, *seats); err != nil {
		fmt.Fprintf(os.Stderr, "cinebook-loadgen: %v\n", err)
		os.Exit(1)
	}
}

func parseLevels(raw string) ([]int, error) {
	var out []int
	for field := range strings.SplitSeq(raw, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("-levels: %q is not a positive integer", field)
		}
		out = append(out, n)
	}
	return out, nil
}

func run(ctx context.Context, stdout io.Writer, base string, showtimeID int64, levels []int, duration time.Duration, poolSize int) error {
	c := &client{
		base: strings.TrimSuffix(base, "/"),
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{MaxIdleConnsPerHost: slices.Max(levels)},
		},
		run: strconv.FormatInt(time.Now().UnixNano(), 36),
	}

	if showtimeID == 0 {
		var err error
		if showtimeID, err = c.firstOpenShowtime(ctx); err != nil {
			return err
		}
	}
	pool, err := c.freeSeats(ctx, showtimeID, poolSize)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "showtime %d, %d contended seats, %v per level\n\n", showtimeID, len(pool), duration)
	// Fixed widths rather than tabwriter, which aligns only within one flush
	// and so cannot print each level as soon as it finishes.
	const row = "%7v %8v %6v %10v %10v %10v %6v %6v %6v %6v %6v %6v\n"
	fmt.Fprintf(stdout, row, "clients", "requests", "req/s", "p50", "p99", "max", "201", "409", "503", "429", "other", "errors")
	for _, n := range levels {
		if ctx.Err() != nil {
			break
		}
		s := c.level(ctx, showtimeID, pool, n, duration)
		fmt.Fprintf(stdout, row,
			n, s.requests(), fmt.Sprintf("%.0f", s.throughput()),
			round(s.percentile(0.50)), round(s.percentile(0.99)), round(s.percentile(1)),
			s.statuses[http.StatusCreated], s.statuses[http.StatusConflict],
			s.statuses[http.StatusServiceUnavailable], s.statuses[http.StatusTooManyRequests],
			s.other(), s.errors)
	}
	return nil
}

func round(d time.Duration) time.Duration { return d.Round(10 * time.Microsecond) }

// level runs n clients against the pool until duration passes. Requests
// already sent when time is up are allowed to finish, so the tail is measured
// rather than cut off.
func (c *client) level(ctx context.Context, showtimeID int64, pool []int64, n int, duration time.Duration) *stats {
	deadline := time.Now().Add(duration)
	start := time.Now()
	results := make([]*stats, n)

	var wg sync.WaitGroup
	for i := range n {
		results[i] = newStats()
		wg.Go(func() {
			s := results[i]
			for time.Now().Before(deadline) && ctx.Err() == nil {
				seat := pool[rand.IntN(len(pool))]
				began := time.Now()
				status, token, err := c.hold(showtimeID, seat)
				if err != nil {
					s.errors++
					continue
				}
				s.record(status, time.Since(began))
				if status == http.StatusCreated {
					c.release(token)
				}
			}
		})
	}
	wg.Wait()

	total := newStats()
	for _, s := range results {
		total.merge(s)
	}
	total.elapsed = time.Since(start)
	return total
}

type client struct {
	base string
	http *http.Client
	run  string
	seq  atomic.Int64
}

func (c *client) getJSON(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET %s: %d %s", path, resp.StatusCode, body)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func (c *client) firstOpenShowtime(ctx context.Context) (int64, error) {
	var list struct {
		Showtimes []struct {
			ID        int64 `json:"id"`
			SalesOpen bool  `json:"sales_open"`
		} `json:"showtimes"`
	}
	if err := c.getJSON(ctx, "/v1/showtimes", &list); err != nil {
		return 0, err
	}
	for _, s := range list.Showtimes {
		if s.SalesOpen {
			return s.ID, nil
		}
	}
	return 0, errors.New("no showtime is selling seats")
}

func (c *client) freeSeats(ctx context.Context, showtimeID int64, n int) ([]int64, error) {
	var seatMap struct {
		Rows []struct {
			Seats []struct {
				ID     int64  `json:"id"`
				Status string `json:"status"`
			} `json:"seats"`
		} `json:"rows"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("/v1/showtimes/%d/seats", showtimeID), &seatMap); err != nil {
		return nil, err
	}
	var free []int64
	for _, row := range seatMap.Rows {
		for _, seat := range row.Seats {
			if seat.Status == "free" && len(free) < n {
				free = append(free, seat.ID)
			}
		}
	}
	if len(free) == 0 {
		return nil, fmt.Errorf("showtime %d has no free seats", showtimeID)
	}
	return free, nil
}

// Every request names a fresh customer, so the per-customer throttle measures
// nothing here and the numbers are about seat contention alone.
func (c *client) hold(showtimeID, seat int64) (int, string, error) {
	body := fmt.Sprintf(`{"seat_ids": [%d]}`, seat)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/v1/showtimes/%d/holds", c.base, showtimeID), strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Customer-Ref", fmt.Sprintf("loadgen-%s-%d", c.run, c.seq.Add(1)))

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	var held struct {
		Token string `json:"token"`
	}
	if resp.StatusCode == http.StatusCreated {
		if err := json.NewDecoder(resp.Body).Decode(&held); err != nil {
			return 0, "", err
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode, held.Token, nil
}

// Released at once, so the pool stays contended rather than filling up.
func (c *client) release(token string) {
	req, err := http.NewRequest(http.MethodDelete, c.base+"/v1/holds/"+token, nil)
	if err != nil {
		return
	}
	if resp, err := c.http.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}
