-- name: CreateBooking :one
INSERT INTO bookings (ref, hold_id, showtime_id, customer_ref, total_minor, currency, idempotency_key)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT DO NOTHING
RETURNING id, ref, hold_id, showtime_id, customer_ref, total_minor, currency, idempotency_key, created_at;

-- name: GetBookingByRef :one
SELECT id, ref, hold_id, showtime_id, customer_ref, total_minor, currency, idempotency_key, created_at
  FROM bookings
 WHERE ref = $1;

-- name: GetBookingByIdempotencyKey :one
SELECT id, ref, hold_id, showtime_id, customer_ref, total_minor, currency, idempotency_key, created_at
  FROM bookings
 WHERE customer_ref = $1
   AND idempotency_key = $2;

-- name: GetBookingByHold :one
SELECT id, ref, hold_id, showtime_id, customer_ref, total_minor, currency, idempotency_key, created_at
  FROM bookings
 WHERE hold_id = $1;
