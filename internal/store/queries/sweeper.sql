-- name: ListShowtimesWithExpiredSeats :many
SELECT DISTINCT showtime_id
  FROM seat_occupancy
 WHERE released_at IS NULL
   AND confirmed_at IS NULL
   AND expires_at <= now()
 ORDER BY showtime_id
 LIMIT $1;

-- name: ReclaimExpiredSeatsForShowtime :execrows
UPDATE seat_occupancy
   SET released_at = now()
 WHERE showtime_id = $1
   AND released_at IS NULL
   AND confirmed_at IS NULL
   AND expires_at <= now();
