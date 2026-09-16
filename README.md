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

Then open http://localhost:8080 for the browser UI, or talk to the API directly:

```sh
curl -s localhost:8080/v1/showtimes
curl -s localhost:8080/v1/showtimes/1/seats
curl -si -X POST localhost:8080/v1/showtimes/1/holds \
  -H 'Content-Type: application/json' -H 'X-Customer-Ref: me' \
  -d '{"seat_ids": [1, 2]}'
curl -si -X POST localhost:8080/v1/holds/<token>/confirm -H 'Idempotency-Key: first-try'
```

Section 6 of `ARCHITECTURE.md` lists every route and every error type.

## Browser UI

The binary serves a small UI at `/`, next to the API at `/v1/`, from files embedded in it.
It is plain HTML, CSS and ES modules, so there is nothing to install or build.

| Page | API |
| --- | --- |
| Films and showtimes | `GET /v1/movies`, `GET /v1/showtimes?movie_id=&from=` |
| Seat grid, refreshed every five seconds | `GET /v1/showtimes/{id}`, `GET /v1/showtimes/{id}/seats` |
| Hold, with a countdown to `expires_at` | `POST /v1/showtimes/{id}/holds`, `GET /v1/holds/{token}` |
| Confirm or release | `POST /v1/holds/{token}/confirm`, `DELETE /v1/holds/{token}` |
| Booking | `GET /v1/bookings/{ref}` |

The customer is a random `demo-` id kept in this browser's local storage and sent as `X-Customer-Ref` when holding seats.
It makes plain that the header is a stub for a sign-in that does not exist yet.
Clear site data to become a different customer, or open a private window to race yourself for a seat.

The Go tests check that the pages load, that every module import resolves, and that no view builds markup from strings.
There is no browser test in CI.

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

## Deploying to Render

`render.yaml` is a Render Blueprint for a free web service built from the `Dockerfile` and a free Postgres 18, both in Singapore.

1. Push the repository to GitHub.
2. In the Render dashboard, choose New, then Blueprint, and select the repository.
   Render creates the database, builds the image and starts the service, which applies the migrations as it starts.
3. Seed the database from your machine, using the external URL from the database's Connect menu:

```sh
read -rs CINEBOOK_DSN && export CINEBOOK_DSN    # paste the external URL
make seed-remote
```

Run `make seed-remote` again whenever the demo needs another week of showtimes.
After that, every push to the connected branch deploys automatically.

What the free tier changes:

- The service sleeps after 15 minutes without traffic and takes about a minute to wake, so the first page after a quiet spell is slow.
- The database is deleted 30 days after it was created, after a 14-day grace period, and has no backups.
  Recreate it from the dashboard and seed it again, or move to a paid plan.
- The health check is `/healthz`, not `/readyz`.
  Render restarts an instance that fails its check for a minute, and a restart cannot fix an unreachable database.
  The listener only opens once migrations are done, so a new deploy still cannot take traffic early.
- `/metrics` is public, because Render exposes a single port.
  It carries request counts and pool statistics, and no customer data.

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

One cinema, two screens, 236 seats, three films, and six showtimes a day for the next seven days, priced in satang.

```sh
make seed
```

Run it again whenever you like.
Each run adds whatever is missing from the catalog and from the next seven days, and leaves holds and bookings alone, which is how a long-running demo stays current.
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
  web/                     browser UI, embedded
  obs/                     logging, metrics, health
  store/
    migrations/*.sql       goose, embedded
    seed/*.sql             dev and test fixtures
    queries/*.sql          sqlc input
    gen/                   sqlc output
.github/workflows/ci.yml   check, build, image, migration cycle
Dockerfile                 static binary on distroless
render.yaml                Render Blueprint
Makefile
```
