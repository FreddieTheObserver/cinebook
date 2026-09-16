package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/FreddieTheObserver/cinebook/internal/store/gen"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const (
	defaultMaxConns         = 16
	defaultLockTimeout      = 2 * time.Second
	defaultStatementTimeout = 5 * time.Second
)

// Config is the pool and timeout configuration for a Store.
type Config struct {
	DSN              string
	MaxConns         int32
	LockTimeout      time.Duration
	StatementTimeout time.Duration
}

func (c *Config) applyDefaults() {
	if c.MaxConns <= 0 {
		c.MaxConns = defaultMaxConns
	}
	if c.LockTimeout <= 0 {
		c.LockTimeout = defaultLockTimeout
	}
	if c.StatementTimeout <= 0 {
		c.StatementTimeout = defaultStatementTimeout
	}
}

// Store owns the connection pool. Read paths go through the embedded Queries:
// no transaction, no lock, and raw errors the caller must put through Classify.
type Store struct {
	*gen.Queries

	pool *pgxpool.Pool
	cfg  Config
}

// New opens the pool and verifies it can reach the database.
func New(ctx context.Context, cfg Config) (*Store, error) {
	cfg.applyDefaults()

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	// Explicit, because the ceiling on concurrent booking transactions is a
	// capacity decision and not an accident of core count.
	poolCfg.MaxConns = cfg.MaxConns

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	return &Store{Queries: gen.New(pool), pool: pool, cfg: cfg}, nil
}

// Close releases the pool and every connection in it.
func (s *Store) Close() { s.pool.Close() }

// Ping reports whether the pool can still reach the database.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Pool exposes the underlying pool for callers that need it directly.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Migrate applies the embedded migrations. goose needs database/sql, so it
// borrows the existing pool rather than having a second driver configured.
func (s *Store) Migrate(ctx context.Context) error {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("migrations fs: %w", err)
	}

	db := stdlib.OpenDBFromPool(s.pool)
	defer db.Close()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, sub, goose.WithSessionLocker(migrationLocker{}))
	if err != nil {
		return fmt.Errorf("goose provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// Replicas that start together would otherwise race to apply the same
// migrations. goose's own Postgres locker takes a single-argument advisory
// lock, a keyspace section 4.1 reserves for showtimes, so this one uses the
// two-argument form.
type migrationLocker struct{}

const advisoryClassMigrations = 1

func (migrationLocker) SessionLock(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1, 0)`, advisoryClassMigrations); err != nil {
		return fmt.Errorf("take migration lock: %w", err)
	}
	return nil
}

func (migrationLocker) SessionUnlock(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1, 0)`, advisoryClassMigrations); err != nil {
		return fmt.Errorf("release migration lock: %w", err)
	}
	return nil
}
