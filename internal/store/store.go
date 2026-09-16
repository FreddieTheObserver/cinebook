package store

import (
	"context"
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

// Store owns the pool. The embedded Queries serve read paths directly, which
// take no transaction and no lock, and whose errors are raw: call Classify.
type Store struct {
	*gen.Queries

	pool *pgxpool.Pool
	cfg  Config
}

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

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Migrate applies the embedded migrations through a temporary database/sql
// handle over the same pool, so no second driver is configured.
func (s *Store) Migrate(ctx context.Context) error {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("migrations fs: %w", err)
	}

	db := stdlib.OpenDBFromPool(s.pool)
	defer db.Close()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, sub)
	if err != nil {
		return fmt.Errorf("goose provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}
