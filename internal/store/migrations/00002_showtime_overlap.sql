-- +goose Up

CREATE EXTENSION IF NOT EXISTS btree_gist;

-- Denormalized rather than generated: timestamptz + interval is STABLE, not
-- IMMUTABLE, so it cannot be a generated column or an index expression.
ALTER TABLE showtimes ADD COLUMN ends_at timestamptz;

UPDATE showtimes s
   SET ends_at = s.starts_at + make_interval(mins => m.runtime_min + 15)
  FROM movies m
 WHERE m.id = s.movie_id;

ALTER TABLE showtimes
    ALTER COLUMN ends_at SET NOT NULL,
    ADD CONSTRAINT showtimes_ends_after_start CHECK (ends_at > starts_at);

COMMENT ON COLUMN showtimes.ends_at IS
    'When the auditorium is free again: feature runtime plus trailers and turnaround.';

-- Subsumed by the exclusion constraint below, which cannot admit two shows at
-- the same instant once ends_at > starts_at is guaranteed.
ALTER TABLE showtimes DROP CONSTRAINT showtimes_auditorium_id_starts_at_key;

ALTER TABLE showtimes
    ADD CONSTRAINT showtimes_no_overlap
    EXCLUDE USING gist (
        auditorium_id WITH =,
        tstzrange(starts_at, ends_at, '[)') WITH &&
    );

-- +goose Down

ALTER TABLE showtimes DROP CONSTRAINT showtimes_no_overlap;
ALTER TABLE showtimes ADD CONSTRAINT showtimes_auditorium_id_starts_at_key UNIQUE (auditorium_id, starts_at);
ALTER TABLE showtimes DROP CONSTRAINT showtimes_ends_after_start;
ALTER TABLE showtimes DROP COLUMN ends_at;
DROP EXTENSION IF EXISTS btree_gist;
