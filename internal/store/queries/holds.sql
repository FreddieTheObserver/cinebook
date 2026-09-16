-- name: LockShowtime :exec
SELECT pg_advisory_xact_lock(sqlc.arg('showtime_id')::bigint);

-- name: ReclaimExpiredSeats :execrows
UPDATE seat_occupancy
   SET released_at = now()
 WHERE showtime_id = sqlc.arg('showtime_id')
   AND seat_id = ANY(sqlc.arg('seat_ids')::bigint[])
   AND released_at IS NULL
   AND confirmed_at IS NULL
   AND expires_at <= now();

-- name: FindSeatConflicts :many
SELECT seat_id
  FROM live_seat_occupancy
 WHERE showtime_id = sqlc.arg('showtime_id')
   AND seat_id = ANY(sqlc.arg('seat_ids')::bigint[])
 ORDER BY seat_id;

-- name: CreateHold :one
INSERT INTO holds (token, showtime_id, customer_ref, expires_at)
VALUES (sqlc.arg('token'), sqlc.arg('showtime_id'), sqlc.arg('customer_ref'),
        now() + make_interval(secs => sqlc.arg('ttl_seconds')::int))
RETURNING id, token, showtime_id, customer_ref, expires_at, confirmed_at, released_at, created_at;

-- name: ClaimSeats :execrows
INSERT INTO seat_occupancy (showtime_id, seat_id, hold_id, expires_at)
SELECT h.showtime_id, unnest(sqlc.arg('seat_ids')::bigint[]), h.id, h.expires_at
  FROM holds h
 WHERE h.id = sqlc.arg('hold_id');

-- name: GetHoldByToken :one
SELECT id, token, showtime_id, customer_ref, expires_at, confirmed_at, released_at, created_at,
       (released_at IS NULL AND (confirmed_at IS NOT NULL OR expires_at > now()))::boolean AS live
  FROM holds
 WHERE token = $1;

-- name: GetHoldSeats :many
SELECT so.seat_id, seat.row_label, seat.seat_num, seat.kind
  FROM seat_occupancy so
  JOIN seats seat ON seat.id = so.seat_id
 WHERE so.hold_id = $1
   AND so.released_at IS NULL
 ORDER BY seat.row_label, seat.seat_num;

-- name: ReleaseHoldSeats :execrows
UPDATE seat_occupancy
   SET released_at = now()
 WHERE hold_id = $1
   AND released_at IS NULL
   AND confirmed_at IS NULL;

-- name: ReleaseHold :execrows
UPDATE holds
   SET released_at = now()
 WHERE id = $1
   AND released_at IS NULL
   AND confirmed_at IS NULL;

-- name: ConfirmHoldSeats :execrows
UPDATE seat_occupancy
   SET confirmed_at = now()
 WHERE hold_id = $1
   AND released_at IS NULL
   AND confirmed_at IS NULL
   AND expires_at > now();

-- name: ConfirmHold :execrows
UPDATE holds
   SET confirmed_at = now()
 WHERE id = $1
   AND released_at IS NULL
   AND confirmed_at IS NULL
   AND expires_at > now();
