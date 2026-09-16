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
| 5. Wiring: config, sweeper, metrics, health, Makefile, CI | Done |

## Requirements

- Go 1.26
- Docker, reachable from the shell you build in.
  On WSL that means Docker Desktop with WSL integration enabled for the distribution.
- make

## Quick start

```sh
make db-up      # Postgres 18 from compose.yaml
make run        # applies migrations, then listens on :8080
make seed       # in another shell, once the service has migrated
```

```sh
curl -s localhost:8080/v1/showtimes
curl -s localhost:8080/v1/showtimes/1/seats
curl -si -X POST localhost:8080/v1/showtimes/1/holds \
  -H 'Content-Type: application/json' -H 'X-Customer-Ref: me' \
  -d '{"seat_ids": [1, 2]}'
curl -si -X POST localhost:8080/v1/holds/<token>/confirm -H 'Idempotency-Key: first-try'
```

Section 6 of `ARCHITECTURE.md` lists every route and every error type.

## Local database

```sh
make db-up
make db-down
```

Postgres 18 on port 5432, database `cinebook`, user and password `cinebook`.
Override with `POSTGRES_PORT`, `POSTGRES_DB`, `POSTGRES_USER` or `POSTGRES_PASSWORD`.

This compose file is for development only.
Tests never use it, because they bring up their own container.

## Configuration

`cinebook-api` reads its environment and reports every invalid variable at once.
An unset tuning variable keeps the default of the layer that owns it.

| Variable | Default | Meaning |
| --- | --- | --- |
| `CINEBOOK_DSN` | required | Postgres connection string. The Makefile defaults it to the compose database. |
| `CINEBOOK_ADDR` | `:8080` | Listen address. |
| `CINEBOOK_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. Logs are JSON lines on stdout. |
| `CINEBOOK_DB_MAX_CONNS` | `16` | Pool ceiling, and so the ceiling on concurrent booking transactions. |
| `CINEBOOK_LOCK_TIMEOUT` | `2s` | How long a transaction queues for a showtime lock before answering `503`. |
| `CINEBOOK_STATEMENT_TIMEOUT` | `5s` | Upper bound on any single statement. |
| `CINEBOOK_HOLD_TTL` | `7m` | How long a hold lasts unconfirmed. |
| `CINEBOOK_MAX_SEATS_PER_HOLD` | `10` | Seats one hold may cover. |
| `CINEBOOK_RATE_PER_SECOND` | `5` | Per-customer throttle refill rate, per replica. |
| `CINEBOOK_RATE_BURST` | `20` | Per-customer throttle burst. |
| `CINEBOOK_SWEEP_INTERVAL` | `30s` | Mean time between sweeper runs, jittered by ten percent. |
| `CINEBOOK_SWEEP_BATCH` | `100` | Showtimes the sweeper visits per run. |
| `CINEBOOK_DRAIN_DELAY` | `0s` | How long `/readyz` fails before the listener closes on shutdown. Set it to at least what your load balancer needs to notice. |
| `CINEBOOK_SHUTDOWN_TIMEOUT` | `20s` | How long in-flight requests get to finish once the listener closes. |

## Operating it

| Endpoint | Purpose |
| --- | --- |
| `GET /healthz` | Liveness. Checks nothing external, so a database outage does not restart every replica. |
| `GET /readyz` | Readiness. Fails while draining or when the database is unreachable. A pool with every connection busy counts as reachable, so a hot showtime does not read as an outage. |
| `GET /metrics` | Prometheus. |

On `SIGTERM` or `SIGINT` the service fails readiness, waits out `CINEBOOK_DRAIN_DELAY`, stops accepting connections, and gives in-flight requests `CINEBOOK_SHUTDOWN_TIMEOUT` to finish.
A second signal kills it at once.

The metrics worth a dashboard:

| Metric | Why |
| --- | --- |
| `cinebook_http_request_duration_seconds{route, status}` | Latency and counts per route. Status is a label so that `503` responses, which waited out the lock timeout, do not hide inside the latency of everything else. |
| `cinebook_seat_race_lost_total` | Double claims caught by the unique index. Anything above zero is a bug in the serialization layer. |
| `cinebook_db_pool_acquired_conns` against `cinebook_db_pool_max_conns` | A hot showtime filling the pool, which section 4.6 of `ARCHITECTURE.md` bounds with timeouts. |
| `cinebook_db_pool_empty_acquires_total` | Requests that had to wait for a connection. |
| `cinebook_sweeper_reclaimed_seats_total`, `cinebook_sweeper_runs_total{result}` | Sweeper health. Correctness does not depend on it, but table size does. |

## Migrations

`cinebook-api` applies its embedded migrations at startup.
Replicas that start together take turns on an advisory lock, so only one of them migrates.

For manual work, goose runs as a pinned one-off rather than a dependency, because its CLI imports a driver for every database it supports.

```sh
make migrate-status
make migrate-up
make migrate-down
make migrate-cycle    # up, reset, up: proves every Down undoes its Up
```

All of them act on `CINEBOOK_DSN`.

## Seed data

One cinema, two screens, 236 seats, three films and twelve showtimes across today and tomorrow, priced in satang.

```sh
make seed
```

Running it twice is a no-op.
It is deliberately not a migration, so that a replica applying migrations at startup cannot pick up demo rows.

## Tests

Integration tests start their own Postgres 18 through testcontainers, so Docker has to be reachable.
They do not touch the compose database.

```sh
make test     # go test -race ./...
make check    # vet, staticcheck, sqlc diff and tests, as CI runs them
```

The centrepiece is `TestTwoHundredRacersForOneSeat`, which sends 200 goroutines after a single seat and asserts one winner, 199 conflicts, one live occupancy row, and zero unique violations.
A unique violation there would mean the advisory lock failed to serialize and the index caught the fallout.
It runs twice: against the domain layer, and again over HTTP, where it also asserts one `201`, 199 `409` responses naming the seat, and no race-lost signal from the server.

`TestServiceLifecycle` runs the binary's own entry point against a fresh database.
It proves that migrations, probes, metrics and the sweeper are wired, and that a graceful shutdown lets a hold queued on the showtime lock finish with `201` after the listener has closed.

CI runs `make check` and `make build`, plus `make migrate-cycle` against a Postgres service container.

## Load

`cinebook-loadgen` measures hold latency against contention, so that the timeouts in section 4.6 of `ARCHITECTURE.md` are tuned against numbers rather than taste.

```sh
make loadgen LOADGEN_FLAGS='-levels 1,16,64 -duration 10s -seats 8'
```

Each level runs that many concurrent clients against a pool of seats in one showtime.
Every successful hold is released at once, so the pool stays contended.
Every request names a fresh customer, so the per-customer throttle stays out of the measurement.
It reports p50, p99 and max latency and a count per status.

It writes real holds, so point it only at a database you own.

## Generated code

Queries are hand written under `internal/store/queries`, and sqlc generates the Go into `internal/store/gen`.

```sh
make sqlc         # regenerate
make sqlc-diff    # fails if the generated code has drifted
```

Never edit `internal/store/gen` by hand.

## Layout

```
cmd/cinebook-api/          service binary
cmd/cinebook-loadgen/      contention load
internal/
  booking/                 domain operations and catalog reads
  config/                  env parsing
  httpapi/                 handlers, middleware, problem+json
  obs/                     logging, metrics, health
  store/
    migrations/*.sql       goose, embedded
    seed/*.sql             dev and test fixtures
    queries/*.sql          sqlc input
    gen/                   sqlc output
.github/workflows/ci.yml   check, build, migration cycle
Makefile
```
