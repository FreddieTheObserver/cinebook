package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/FreddieTheObserver/cinebook/internal/store/gen"
)

// TxFunc is a transaction body, run against queries bound to that transaction.
type TxFunc func(ctx context.Context, q *gen.Queries) error

// InShowtimeTx runs fn under the per-showtime advisory lock. The lock is taken
// here rather than by callers, so a seat write path cannot forget it.
func (s *Store) InShowtimeTx(ctx context.Context, showtimeID int64, fn TxFunc) error {
	return s.InTx(ctx, func(ctx context.Context, q *gen.Queries) error {
		if err := q.LockShowtime(ctx, showtimeID); err != nil {
			return fmt.Errorf("lock showtime %d: %w", showtimeID, err)
		}
		return fn(ctx, q)
	})
}

// InTx runs fn in a READ COMMITTED transaction bounded by the configured
// timeouts. Errors out of here are always classified.
func (s *Store) InTx(ctx context.Context, fn TxFunc) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Classify(fmt.Errorf("begin: %w", err))
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.applyTimeouts(ctx, tx); err != nil {
		return Classify(err)
	}
	if err := fn(ctx, s.Queries.WithTx(tx)); err != nil {
		return Classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Classify(fmt.Errorf("commit: %w", err))
	}
	return nil
}

// set_config with is_local rather than SET LOCAL, because SET takes no bind
// parameters.
func (s *Store) applyTimeouts(ctx context.Context, tx pgx.Tx) error {
	const q = `SELECT set_config('lock_timeout', $1, true), set_config('statement_timeout', $2, true)`
	_, err := tx.Exec(ctx, q,
		fmt.Sprintf("%dms", s.cfg.LockTimeout.Milliseconds()),
		fmt.Sprintf("%dms", s.cfg.StatementTimeout.Milliseconds()))
	if err != nil {
		return fmt.Errorf("set timeouts: %w", err)
	}
	return nil
}
