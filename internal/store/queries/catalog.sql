-- name: ListMovies :many
SELECT id, title, runtime_min, rating
  FROM movies
 ORDER BY title, id;

-- name: GetShowtime :one
SELECT id, movie_id, auditorium_id, starts_at, ends_at, price_minor, currency, sales_open
  FROM showtimes
 WHERE id = $1;

-- name: ListShowtimes :many
SELECT s.id, s.movie_id, s.auditorium_id, s.starts_at, s.ends_at,
       s.price_minor, s.currency, s.sales_open,
       m.title AS movie_title,
       a.name  AS auditorium_name
  FROM showtimes s
  JOIN movies m      ON m.id = s.movie_id
  JOIN auditoriums a ON a.id = s.auditorium_id
 WHERE (sqlc.narg('movie_id')::bigint IS NULL OR s.movie_id = sqlc.narg('movie_id')::bigint)
   AND (sqlc.narg('starts_from')::timestamptz IS NULL OR s.starts_at >= sqlc.narg('starts_from')::timestamptz)
   AND (sqlc.narg('starts_before')::timestamptz IS NULL OR s.starts_at < sqlc.narg('starts_before')::timestamptz)
 ORDER BY s.starts_at, s.id;

-- name: GetShowtimeDetail :one
SELECT s.id, s.movie_id, s.auditorium_id, s.starts_at, s.ends_at,
       s.price_minor, s.currency, s.sales_open,
       m.title AS movie_title,
       a.name  AS auditorium_name
  FROM showtimes s
  JOIN movies m      ON m.id = s.movie_id
  JOIN auditoriums a ON a.id = s.auditorium_id
 WHERE s.id = $1;
