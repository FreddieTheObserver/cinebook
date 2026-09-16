-- name: GetSeatMap :many
SELECT seat.id, seat.row_label, seat.seat_num, seat.kind,
       CASE WHEN live.id IS NULL              THEN 'free'
            WHEN live.confirmed_at IS NOT NULL THEN 'sold'
            ELSE 'held'
       END::text AS status
  FROM showtimes st
  JOIN seats seat ON seat.auditorium_id = st.auditorium_id
  LEFT JOIN live_seat_occupancy live
         ON live.showtime_id = st.id AND live.seat_id = seat.id
 WHERE st.id = $1
 ORDER BY seat.row_label, seat.seat_num;

-- name: CountSeatsInShowtime :one
SELECT count(*)::bigint
  FROM seats seat
  JOIN showtimes st ON st.id = sqlc.arg('showtime_id')
                   AND seat.auditorium_id = st.auditorium_id
 WHERE seat.id = ANY(sqlc.arg('seat_ids')::bigint[]);
