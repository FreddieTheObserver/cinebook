package store

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound = errors.New("not found")

	// The advisory lock is meant to make this unreachable, so it is a bug
	// signal rather than a routine outcome. See section 4.1.
	ErrSeatRaceLost = errors.New("seat claimed concurrently")

	ErrDuplicateBooking  = errors.New("idempotency key already used")
	ErrHoldAlreadyBooked = errors.New("hold already booked")
	ErrScheduleConflict  = errors.New("showtime overlaps another in the same auditorium")
	ErrBusy              = errors.New("database busy")
	ErrConstraint        = errors.New("constraint violated")
)

const (
	codeForeignKeyViolation = "23503"
	codeUniqueViolation     = "23505"
	codeCheckViolation      = "23514"
	codeExclusionViolation  = "23P01"
	codeLockNotAvailable    = "55P03"
	codeQueryCanceled       = "57014"
)

const (
	constraintLiveSeat       = "seat_occupancy_live_uniq"
	constraintIdempotencyKey = "bookings_customer_ref_idempotency_key_key"
	constraintBookingHold    = "bookings_hold_id_key"
)

// IsInsertConflict reports whether an insert written as ON CONFLICT DO NOTHING
// was suppressed. It returns no row rather than aborting the transaction, so
// the caller decides which constraint it was and what that means.
func IsInsertConflict(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// Classify maps a Postgres error onto a sentinel the caller can branch on,
// keeping the original for logging.
func Classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}

	switch pgErr.Code {
	case codeUniqueViolation:
		switch pgErr.ConstraintName {
		case constraintLiveSeat:
			return fmt.Errorf("%w: %w", ErrSeatRaceLost, err)
		case constraintIdempotencyKey:
			return fmt.Errorf("%w: %w", ErrDuplicateBooking, err)
		case constraintBookingHold:
			return fmt.Errorf("%w: %w", ErrHoldAlreadyBooked, err)
		}
		return fmt.Errorf("%w: %w", ErrConstraint, err)
	case codeExclusionViolation:
		return fmt.Errorf("%w: %w", ErrScheduleConflict, err)
	// Both are load shedding: the lock did not arrive in time, or the whole
	// statement outran its budget.
	case codeLockNotAvailable, codeQueryCanceled:
		return fmt.Errorf("%w: %w", ErrBusy, err)
	case codeCheckViolation, codeForeignKeyViolation:
		return fmt.Errorf("%w: %w", ErrConstraint, err)
	}
	return err
}
