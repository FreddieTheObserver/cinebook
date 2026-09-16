# CineBook

A cinema seat booking service in Go, built so that two customers can never end up holding the same seat.

Seat exclusivity is a database invariant rather than application logic.
Every write path that touches seats serializes on a per-showtime advisory lock, and a partial unique index stands behind it as a hard guarantee.
`ARCHITECTURE.md` explains the reasoning, the concurrency model and the trade-offs.
This file is only about running it.

## Status

Built in horizontal layers, each complete before the next starts.

| Step | State |
| --- | --- |
| 1. Schema, liveness view, seed | Done |
| 2. Store: sqlc queries, transaction helper, error classification | Done |
| 3. Domain: hold, confirm, release, expire, seat map | Done |
| 4. HTTP: handlers, middleware, problem+json | Done |
| 5. Wiring: config, sweeper, metrics, health, Makefile, CI | Not started |

There is no service binary yet.
`internal/httpapi` is a complete `http.Handler`, and `cmd/cinebook-api` wires it up in step 5 along with config and graceful shutdown.

## Requirements

- Go 1.26
- Docker, reachable from the shell you build in.
  On WSL that means Docker Desktop with WSL integration enabled for the distribution.

## Local database

```sh
docker compose up -d
```

Postgres 18 on port 5432, database `cinebook`, user and password `cinebook`.
Override with `POSTGRES_PORT`, `POSTGRES_DB`, `POSTGRES_USER` or `POSTGRES_PASSWORD`.

This compose file is for development only.
Tests never use it, because they bring up their own container.

## Migrations

goose runs as a pinned one-off rather than a dependency, because its CLI imports a driver for every database it supports.

```sh
export CINEBOOK_DSN="postgres://cinebook:cinebook@localhost:5432/cinebook?sslmode=disable"
alias goose='go run github.com/pressly/goose/v3/cmd/goose@v3.28.0 -dir internal/store/migrations postgres "$CINEBOOK_DSN"'

goose up
goose status
goose down
```

Once `cmd/cinebook-api` exists it will apply the same migrations from its own embedded copy at startup.

## Seed data

One cinema, two screens, 236 seats, three films and twelve showtimes across today and tomorrow, priced in satang.

```sh
docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U cinebook -d cinebook \
  < internal/store/seed/0001_one_cinema.sql
```

Running it twice is a no-op.
It is deliberately not a migration, so that a replica applying migrations at startup cannot pick up demo rows.

## Tests

Integration tests start their own Postgres 18 through testcontainers, so Docker has to be reachable.
They do not touch the compose database.

```sh
go test -race ./...
```

The centrepiece is `TestTwoHundredRacersForOneSeat`, which sends 200 goroutines after a single seat and asserts one winner, 199 conflicts, one live occupancy row, and zero unique violations.
A unique violation there would mean the advisory lock failed to serialize and the index caught the fallout.
It runs twice: against the domain layer, and again over HTTP, where it also asserts one `201`, 199 `409` responses naming the seat, and no error in the server log.

## Generated code

Queries are hand written under `internal/store/queries`, and sqlc generates the Go into `internal/store/gen`.

```sh
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 diff    # fails if the generated code has drifted
```

Never edit `internal/store/gen` by hand.

## Layout

```
cmd/cinebook-api/          service binary        (step 5)
cmd/cinebook-loadgen/      contention load       (step 5)
internal/
  booking/                 domain operations
  config/                  env parsing           (step 5)
  httpapi/                 handlers, middleware, problem+json
  obs/                     logging, metrics      (step 5)
  store/
    migrations/*.sql       goose, embedded
    seed/*.sql             dev and test fixtures
    queries/*.sql          sqlc input
    gen/                   sqlc output
```
