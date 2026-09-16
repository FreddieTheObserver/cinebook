# CineBook Architecture

A concurrent cinema seat booking service in Go.
The single hard requirement is that a seat in a showtime can never be sold twice, under any amount of concurrency, crash, or clock skew.
Everything below is chosen to make that property provable rather than hopeful.

Status: decisions pinned, no application code written yet.
Last updated: 2026-09-16.

## 1. Scope

In scope for the first milestone:

- A catalog of movies, auditoriums, seats, and showtimes.
- A two-phase booking flow: hold seats with a TTL, then confirm or let the hold lapse.
- A seat map read model showing which seats are free, held, or sold.
- A stateless HTTP/JSON API that can run as multiple replicas behind a load balancer.
- Integration tests against a real Postgres, including a contention test that proves the no-double-booking invariant.

Everything explicitly deferred is listed in section 11.

## 2. Environment

Development happens inside WSL2 Ubuntu 26.04, because that is where the Go toolchain lives and because native ext4 keeps `go build` and the test cache fast.

| Thing | Value |
| --- | --- |
| Repo | `~/cinebook` in WSL |
| Module | `github.com/FreddieTheObserver/cinebook` |
| Go | 1.26.0 (linux/amd64) |
| Postgres | 18, run as a container |

Docker Desktop 29 on Windows has WSL integration enabled for Ubuntu, so `docker info` works from inside WSL and the integration test strategy in section 9 has the daemon it depends on.

## 3. Stack

| Concern | Choice | Why this one |
| --- | --- | --- |
| HTTP router | stdlib `net/http` `ServeMux` | Since Go 1.22 it does method and wildcard patterns, which is the only thing a framework was ever needed for here. No dependency, no framework lifecycle, no migration debt. |
| Postgres driver | `jackc/pgx/v5` with `pgxpool` | Native protocol rather than `database/sql`. Gives typed `pgconn.PgError` codes, which the booking path needs in order to tell a unique violation (`23505`) from a lock timeout (`55P03`). |
| Query layer | `sqlc` | The SQL stays hand written and reviewable, which matters when the SQL *is* the concurrency control. Generated Go gives compile-time checked structs and no hand-rolled `Scan` boilerplate. |
| Migrations | `pressly/goose` with `embed.FS` | Embedded in the binary, so a replica cannot run against a schema it was not built for. Simpler failure modes than golang-migrate dirty-state handling. The library only: its CLI imports a driver for every database it supports, which would add around 70 indirect modules, so it is run as a pinned `go run ...@version` instead of entering `go.mod`. |
| Config | Env vars, small hand-written typed loader | Around 40 lines. A config library here would be more surface than substance. |
| Logging | stdlib `log/slog`, JSON handler | Structured, stdlib, request-scoped child logger carrying a request id. |
| Metrics | `prometheus/client_golang` at `/metrics` | The interesting numbers are contention numbers, and they need to be observable rather than inferred. |
| Errors on the wire | RFC 9457 `application/problem+json` | Stable machine-readable `type` slugs, so clients branch on a string and not on prose. |
| Tests | `testcontainers-go` plus stdlib `testing` | Real Postgres, since the logic under test is Postgres locking semantics. Mocks would test nothing. |

Total direct dependency count is five.
That is deliberate.
`testcontainers` ships its Postgres helper as a separate module, so it accounts for two `require` lines on its own.

## 4. The concurrency model

This is the core decision and the rest of the design follows from it.

### 4.1 Postgres is the arbiter

Seat exclusivity is a database invariant, not application logic.
The application is stateless and holds no seat state in memory, which is what makes horizontal scaling and crash recovery uninteresting rather than dangerous.

Two mechanisms work together.

**A serialization point per showtime.**
Every write path that touches seats begins with an advisory transaction lock keyed on the showtime id:

```sql
SELECT pg_advisory_xact_lock($1);  -- $1 = showtimes.id (bigint)
```

This makes all seat mutation for one showtime strictly serial, while different showtimes proceed fully in parallel.
Serial execution per showtime removes every interleaving question at a stroke: no deadlock ordering rules, no lost updates, no retry loops, and multi-seat atomicity comes for free.

An advisory lock is used rather than `SELECT ... FOR UPDATE` on the showtime row.
This is a refinement of the strategy we picked, for three reasons.
It writes no tuple, so it produces no row-level lock churn or bloat on a row that every single booking touches.
It releases automatically at commit or rollback, including on a crashed connection.
It does not entangle seat booking with unrelated metadata updates to the showtime row.

The tradeoff is that the advisory lock keyspace is global per database.
We reserve the single-argument advisory keyspace exclusively for showtime serialization, and document it here so a future feature does not collide with it.

**A hard uniqueness invariant.**
Serialization is how we intend to be correct.
The unique index is why we are correct even if that intent has a bug:

```sql
CREATE UNIQUE INDEX seat_occupancy_live_uniq
    ON seat_occupancy (showtime_id, seat_id)
    WHERE released_at IS NULL;
```

At most one unreleased occupancy row can exist per seat per showtime.
No application defect, no missed lock, and no rogue client can violate that.
In normal operation this index never fires, so a `23505` from it is a bug signal rather than a routine outcome.

### 4.2 Isolation level

`READ COMMITTED`, not `SERIALIZABLE`.

Because the critical section is protected by an explicit lock, the stronger isolation level would buy nothing and would cost serialization failures that the application would then have to retry.
Choosing explicit locking over optimistic retries is the whole point: contention becomes a queue with a bounded wait, not a retry storm.

### 4.3 Hold expiry, and why the sweeper is not load bearing

A hold has a TTL, and an expired hold's seats must become bookable again.
The two naive approaches both have a flaw:

- A background job that deletes expired holds makes correctness depend on that job actually running.
- A purely lazy read-time predicate cannot be expressed in the partial unique index, because `now()` is not immutable and so cannot appear in an index predicate.

The resolution is to reclaim expired seats *inside the same critical section that wants them*.
A hold request, having taken the showtime lock, first releases any expired unconfirmed occupancy for the seats it wants, and then inserts its own rows:

```sql
-- step 1: reclaim, inside the lock
UPDATE seat_occupancy
   SET released_at = now()
 WHERE showtime_id = $1
   AND seat_id = ANY($2)
   AND released_at IS NULL
   AND confirmed_at IS NULL
   AND expires_at <= now();

-- step 2: claim
INSERT INTO seat_occupancy (showtime_id, seat_id, hold_id, expires_at) ...
```

Expiry is therefore materialized exactly when it matters, and never depends on a timer.
The background sweeper exists only to keep the table small and to emit a reclaimed-holds metric.
If the sweeper dies, throughput and table size suffer; correctness does not.
The sweeper takes the same per-showtime advisory lock, so it introduces no interleaving of its own.

### 4.4 Liveness has exactly one definition

Because expired rows may linger until reclaimed, every read has to apply the liveness predicate.
Two copies of that predicate would eventually drift, so it exists once, as a view:

```sql
CREATE VIEW live_seat_occupancy AS
SELECT *
  FROM seat_occupancy
 WHERE released_at IS NULL
   AND (confirmed_at IS NOT NULL OR expires_at > now());
```

Read paths query the view and take no lock at all.
A seat map read is a plain `READ COMMITTED` snapshot, so it can be marginally stale, which is correct and expected: a seat map is advisory, and the hold request is the authority.

### 4.5 Time comes from the database

`expires_at` is set and compared using the Postgres `now()` clock, never an application clock.
All timestamps are `timestamptz` and all arithmetic is UTC.
With several replicas, application clock skew would otherwise make a hold's lifetime depend on which replica happened to answer.

### 4.6 Bounded waiting

Serializing per showtime means a popular showtime forms a queue, and every queued transaction is holding a pool connection.
Left alone, one hot premiere could consume the whole connection pool and stall unrelated showtimes.

So every booking transaction sets its own bounds:

```sql
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '5s';
```

A transaction that cannot get the showtime lock in time fails fast with `55P03`, and the API answers `503` with `Retry-After`.
Shedding load is preferable to exhausting the pool.
`pgxpool` max connections is set explicitly rather than left at the `4 * NumCPU` default, since the ceiling on concurrent booking transactions is a capacity decision and not an accident of core count.

### 4.7 The races, and where each one dies

| Race | Resolution |
| --- | --- |
| Two clients hold the same seat simultaneously | Both serialize on the showtime advisory lock. The second sees a live occupancy row and gets `409`. |
| Confirm arrives at the instant the hold expires | Confirm takes the same lock and re-asserts liveness against the database clock in its `UPDATE` predicate, then checks that the affected row count equals the seat count. Whoever holds the lock first wins, deterministically. |
| The same confirm request is delivered twice | Confirm re-reads the hold under the lock, and returns the booking that already exists rather than making another. The idempotency key is checked before anything is written, because confirming the seats and only then rejecting the key would strand them. |
| A generated booking reference collides | The insert carries `ON CONFLICT DO NOTHING`, so the collision returns no row instead of aborting the transaction, and another reference is drawn. Five attempts, then the request fails. |
| Client crashes mid-hold | The hold lapses on its TTL and is reclaimed by the next contender for those seats. |
| Server crashes mid-transaction | The transaction rolls back and the advisory lock releases with the connection. There is no cleanup step to forget to run. |
| Sweeper races a confirm | Both take the showtime lock, so they cannot interleave. |
| An application bug lets a double claim through | The partial unique index rejects it. The request fails; the invariant does not. |

## 5. Data model

Internal primary keys are `bigint` identity columns.
They keep indexes compact, and for showtimes the key doubles as the advisory lock key with no hashing and no collisions.

Public identifiers are chosen per table according to whether guessing one is harmful.
Movie, auditorium, and showtime ids appear in URLs as plain integers, because the catalog is public and enumerating it is not a threat.
A hold token and a booking reference are bearer-like capabilities, so they are unguessable: the hold token is 128 random bits in base32, and the booking reference is a shorter human-readable code such as `CB-7K2M-9QX4`.

```
movies       (id, title, runtime_min, rating, ...)
auditoriums  (id, name, ...)
seats        (id, auditorium_id, row_label, seat_num, kind)     -- physical seats
showtimes    (id, movie_id, auditorium_id, starts_at, ends_at, price_minor, currency, sales_open)
  EXCLUDE (auditorium_id WITH =, tstzrange(starts_at, ends_at) WITH &&)

holds        (id, token unique, showtime_id, customer_ref,
              expires_at, confirmed_at, released_at, created_at)

seat_occupancy (id, showtime_id, seat_id, hold_id,
                expires_at, confirmed_at, released_at, created_at)
  UNIQUE (showtime_id, seat_id) WHERE released_at IS NULL

bookings     (id, ref unique, hold_id unique, showtime_id, customer_ref,
              total_minor, currency, idempotency_key, created_at)
  UNIQUE (customer_ref, idempotency_key)
```

`seat_occupancy` carries the one invariant that matters, so it is kept deliberately narrow.
`expires_at` is denormalized onto it from `holds` so that the reclaim step in section 4.3 is a single-table `UPDATE` with no join inside the critical section.
Money is stored as integer minor units with an explicit currency column, never as a float.
Seeded data is priced in THB, so a minor unit is one satang.

An auditorium cannot run two films at once, so `showtimes` carries an exclusion constraint over the interval each screening occupies.
That needs `ends_at` as a stored column rather than a generated one, because `timestamptz + interval` is `STABLE` and not `IMMUTABLE`, and because an index expression cannot reach `movies.runtime_min` in another table.
`ends_at` is when the auditorium is free again, so it covers trailers and turnaround and not only the feature runtime.

`bookings.idempotency_key` is unique per customer rather than globally.
The key is chosen by the client, so a global constraint would let one customer's `1` collide with another's.
It is `NOT NULL`, which makes the `Idempotency-Key` header mandatory on confirm rather than optional.

## 6. API surface

Versioned under `/v1`, JSON in and out, errors as `application/problem+json`.

```
GET    /v1/movies
GET    /v1/showtimes?movie_id=&from=&to=
GET    /v1/showtimes/{id}/seats        seat map: free | held | sold
POST   /v1/showtimes/{id}/holds        -> token, expires_at, seats, total_minor
GET    /v1/holds/{token}
DELETE /v1/holds/{token}               release early
POST   /v1/holds/{token}/confirm       -> booking ref   (Idempotency-Key header)
GET    /v1/bookings/{ref}

GET    /healthz    liveness, no dependencies
GET    /readyz     readiness, checks the pool
GET    /metrics    prometheus
```

Status codes carry meaning, since a booking client has to branch on them:

| Code | `type` slug | Meaning |
| --- | --- | --- |
| 409 | `seat-unavailable` | One or more seats are taken. The body lists exactly which ones in `seat_ids`, so the client can re-render the map without a second round trip. |
| 410 | `hold-expired` | The hold lapsed before confirm. |
| 422 | `invalid-selection` | Seat does not belong to this showtime, or too many seats requested. |
| 429 | `rate-limited` | Per-customer request throttle. Includes `Retry-After`. |
| 503 | `busy` | Lock timeout on a contended showtime. Includes `Retry-After`. |
| 409 | `sales-closed` | The showtime is not selling seats. |
| 409 | `hold-confirmed` | Release was asked for a hold that is already a booking. Undoing that is a refund, which is deferred. |
| 422 | `idempotency-key-reused` | The key belongs to a booking made from a different hold. |
| 400 | `invalid-request` | Malformed body, query parameter or header, including a missing `X-Customer-Ref` on hold or `Idempotency-Key` on confirm. |
| 404 | `not-found` | No such showtime, hold, booking or route. A malformed id is a 404 as well, since it cannot name anything. |
| 405 | `method-not-allowed` | The path exists, but not for this method. `Allow` lists the methods that do. |
| 413 | `request-too-large` | The body is over 64 KiB. |
| 415 | `unsupported-media-type` | A request body that is not `application/json`. |
| 500 | `internal` | Anything unexpected. The detail is withheld, and the error is logged against the request id. |

The `type` member is the slug under `/problems/`, for example `/problems/seat-unavailable`.
RFC 9457 recommends against a bare relative reference, because it would resolve differently under every resource.

`X-Customer-Ref` and `Idempotency-Key` are 1 to 255 visible ASCII characters.
A confirm replay answers `201` with the original booking, so a client that lost the first response sees exactly what it would have seen.
Responses carry `Cache-Control: no-store`, since seat maps go stale in seconds and holds and bookings carry bearer capabilities.
Access logs record the matched route and never the path, for the same reason.

The throttle is a token bucket per `X-Customer-Ref`, and requests without that header are not throttled in process.
Behind a load balancer the remote address says nothing about who is asking, so anonymous traffic is left to the edge.

Defaults chosen, all configurable, all open to revision per section 12: hold TTL is 7 minutes, a single hold covers at most 10 seats, and the throttle allows 5 requests per second with a burst of 20.

## 7. Repo layout

```
~/cinebook/
  cmd/cinebook-api/          the service binary
  cmd/cinebook-loadgen/      contention load generator
  internal/
    config/                  env parsing
    booking/                 domain: hold, confirm, release, expire, seat map, catalog
    store/
      migrations/*.sql       goose, embedded
      seed/*.sql             dev and test fixtures, never applied in production
      queries/*.sql          sqlc input, the SQL that matters
      gen/                   sqlc output, generated
      store.go tx.go         pool, transaction helper
      errors.go              Postgres error codes to sentinels
    httpapi/                 handlers, middleware, problem+json
    obs/                     slog, prometheus, health
  compose.yaml               Postgres 18 for local dev
  sqlc.yaml
  Makefile
  ARCHITECTURE.md
```

Everything lives under `internal/`, so the module exports nothing and there is no accidental public API to keep stable.
Migrations sit inside `internal/store` because `embed.FS` cannot reach outside its own package directory.
Seed data sits beside them rather than inside them, so that a replica running migrations at boot cannot acquire demo rows.

## 8. Implementation order

Built in horizontal layers, each one complete before the next starts.

1. **Schema.** All goose migrations, the unique index, the liveness view, and seed data for one cinema. Verified by applying up and down cleanly.
2. **Store.** All sqlc queries, the transaction helper, `lock_timeout` handling, and error classification for `23505` and `55P03`.
3. **Domain.** Hold, confirm, release, expire, and the seat map projection, together with the contention tests.
4. **HTTP.** All handlers, middleware, problem+json mapping, and request validation.
5. **Wiring and operability.** Config, graceful shutdown, the sweeper goroutine, metrics, health checks, Makefile, and CI.

## 9. Testing

The invariant is a concurrency property, so the test strategy is built around actually trying to violate it.

- **Unit tests** for pure logic only: pricing, seat map projection, reference generation, validation.
- **Integration tests** against real Postgres 18 via testcontainers, one container per package, migrations applied at startup.
- **The contention test**, which is the centrepiece: 200 goroutines race for the same single seat. The assertions are that exactly one gets `201`, that 199 get `409`, that `live_seat_occupancy` holds exactly one row for that seat, and critically that no `23505` ever surfaced as a `500`. A unique violation reaching the client means the serialization layer failed and the index caught it, which is a passing invariant but a failing design.
- **Expiry tests** using a deliberately short TTL, covering confirm-just-before and confirm-just-after expiry.
- **Idempotency tests** replaying the same confirm concurrently and asserting exactly one booking row.
- Everything runs under `-race` in CI, alongside `go vet`, `staticcheck`, a `sqlc diff` check so generated code cannot drift from the SQL, and a migrate up-down-up cycle.
- **Load** via `cmd/cinebook-loadgen`, reporting p50 and p99 hold latency against contention level, so that the timeouts in section 4.6 get tuned against measurement rather than taste.

## 10. Known limits of this design

Stated plainly, because they are consequences of the choices above rather than oversights.

- Throughput per showtime is bounded by serial transaction execution, on the order of a thousand seat operations per second for a single showtime. Different showtimes scale out freely. For a cinema this is ample; for a stadium ticket drop it would not be, and the fix would be per-seat locking with deadlock-avoiding ordering, which is a strictly more complex design to earn later if it is ever needed.
- Postgres is a single point of failure and the global throughput ceiling.
- The advisory lock keyspace reservation is a convention, enforced by this document rather than by the type system.
- Seat map reads are slightly stale by design.
- The request throttle lives in process memory, so with N replicas one customer can reach N times the configured rate. A shared limit needs shared state, which is not worth adding while `X-Customer-Ref` is itself unauthenticated.

## 11. Deferred

Not in the first milestone, and none of it invalidates the above:

authentication and authorization (the first milestone takes a trusted `X-Customer-Ref` header, which is a stub and not a security model), payments and refunds, seat-class and dynamic pricing, sales cutoff rules relative to showtime start, partial holds and waitlists, an admin interface, ticket delivery, and multi-region deployment.

## 12. Open questions

Defaults are in place for all of these, so none of them block starting work.

1. Hold TTL of 7 minutes: about right for a cinema with no payment step in the flow, probably too short once payment lands.
2. Maximum of 10 seats per hold.
3. Whether a customer may hold seats across several showtimes at once, or only one active hold at a time.
4. Whether `409` should return the whole refreshed seat map rather than just the conflicting seat ids.
5. Turnaround between screenings is 15 minutes in the seed, which is a scheduling policy that belongs to an admin interface once one exists.
