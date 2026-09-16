package booking

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/FreddieTheObserver/cinebook/internal/store/gen"
)

// The shapes the schema check constraints enforce.
var (
	holdTokenPattern  = regexp.MustCompile(`^[A-Z2-7]{26}$`)
	bookingRefPattern = regexp.MustCompile(`^CB-[0-9A-Z]{4}-[0-9A-Z]{4}$`)
)

func TestNewHoldTokenMatchesTheSchema(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		tok, err := newHoldToken()
		if err != nil {
			t.Fatalf("new token: %v", err)
		}
		if !holdTokenPattern.MatchString(tok) {
			t.Fatalf("token %q does not match %s", tok, holdTokenPattern)
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("token %q generated twice", tok)
		}
		seen[tok] = struct{}{}
	}
}

func TestNewBookingRefMatchesTheSchema(t *testing.T) {
	for range 1000 {
		ref, err := newBookingRef()
		if err != nil {
			t.Fatalf("new ref: %v", err)
		}
		if !bookingRefPattern.MatchString(ref) {
			t.Fatalf("ref %q does not match %s", ref, bookingRefPattern)
		}
		if i := strings.IndexAny(ref[3:], "ILOU"); i >= 0 {
			t.Fatalf("ref %q contains a confusable character", ref)
		}
	}
}

func TestNormalizeSeats(t *testing.T) {
	tests := []struct {
		name  string
		in    []int64
		max   int
		want  []int64
		valid bool
	}{
		{name: "sorts", in: []int64{9, 3, 7}, max: 10, want: []int64{3, 7, 9}, valid: true},
		{name: "single", in: []int64{4}, max: 10, want: []int64{4}, valid: true},
		{name: "at the limit", in: []int64{1, 2, 3}, max: 3, want: []int64{1, 2, 3}, valid: true},
		{name: "empty", in: nil, max: 10},
		{name: "over the limit", in: []int64{1, 2, 3, 4}, max: 3},
		{name: "duplicate", in: []int64{5, 5}, max: 10},
		{name: "zero id", in: []int64{0}, max: 10},
		{name: "negative id", in: []int64{-1}, max: 10},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeSeats(tc.in, tc.max)
			if !tc.valid {
				if !errors.Is(err, ErrInvalidSelection) {
					t.Fatalf("got %v, want ErrInvalidSelection", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestNormalizeSeatsDoesNotAliasTheInput(t *testing.T) {
	in := []int64{3, 1, 2}
	if _, err := normalizeSeats(in, 10); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if in[0] != 3 {
		t.Fatalf("caller slice was reordered: %v", in)
	}
}

func TestProjectSeatRows(t *testing.T) {
	rows := []gen.GetSeatMapRow{
		{ID: 1, RowLabel: "A", SeatNum: 1, Kind: "standard", Status: "free"},
		{ID: 2, RowLabel: "A", SeatNum: 2, Kind: "standard", Status: "held"},
		{ID: 3, RowLabel: "B", SeatNum: 1, Kind: "premium", Status: "sold"},
		{ID: 4, RowLabel: "B", SeatNum: 2, Kind: "accessible", Status: "free"},
	}

	got := projectSeatRows(rows)
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0].Label != "A" || len(got[0].Seats) != 2 {
		t.Fatalf("row A wrong: %+v", got[0])
	}
	if got[1].Label != "B" || len(got[1].Seats) != 2 {
		t.Fatalf("row B wrong: %+v", got[1])
	}
	if got[0].Seats[1].Status != SeatHeld {
		t.Fatalf("status not carried through: %+v", got[0].Seats[1])
	}
	if got[1].Seats[0].Status != SeatSold || got[1].Seats[0].Kind != "premium" {
		t.Fatalf("seat detail not carried through: %+v", got[1].Seats[0])
	}
}

func TestProjectSeatRowsOnEmptyInput(t *testing.T) {
	if got := projectSeatRows(nil); got == nil || len(got) != 0 {
		t.Fatalf("got %v, want an empty non-nil slice", got)
	}
}

func TestTotalMinor(t *testing.T) {
	if got := totalMinor(22000, 3); got != 66000 {
		t.Fatalf("got %d, want 66000", got)
	}
	if got := totalMinor(22000, 0); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}
