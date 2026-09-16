-- Development, test and demo fixture, deliberately not a migration: a replica
-- running goose up in production must not end up with demo data.
--
-- Safe to run at any time. Each run adds whatever is missing from the catalog
-- and from the next seven days of showtimes, and never touches holds or
-- bookings, so a hosted demo is topped up by running it again.

BEGIN;

DO $$
DECLARE
    dune        bigint;
    spirited    bigint;
    oppenheimer bigint;
    screen1     bigint;
    screen2     bigint;
    base        timestamp;
    d           integer;
    added       bigint := 0;
    n           bigint;
    turnaround  interval := interval '15 min';
BEGIN
    -- Titles carry no unique constraint, so existence is checked by hand.
    INSERT INTO movies (title, runtime_min, rating)
    SELECT v.title, v.runtime_min, v.rating
      FROM (VALUES ('Dune: Part Two', 166, '13+'),
                   ('Spirited Away',  125, 'G'),
                   ('Oppenheimer',    180, '15+')) AS v(title, runtime_min, rating)
     WHERE NOT EXISTS (SELECT 1 FROM movies m WHERE m.title = v.title);

    SELECT id INTO dune        FROM movies WHERE title = 'Dune: Part Two' ORDER BY id LIMIT 1;
    SELECT id INTO spirited    FROM movies WHERE title = 'Spirited Away'  ORDER BY id LIMIT 1;
    SELECT id INTO oppenheimer FROM movies WHERE title = 'Oppenheimer'    ORDER BY id LIMIT 1;

    INSERT INTO auditoriums (name) VALUES ('Screen 1'), ('Screen 2') ON CONFLICT (name) DO NOTHING;
    SELECT id INTO screen1 FROM auditoriums WHERE name = 'Screen 1';
    SELECT id INTO screen2 FROM auditoriums WHERE name = 'Screen 2';

    INSERT INTO seats (auditorium_id, row_label, seat_num, kind)
    SELECT screen1, chr(64 + r.n), s.n,
           CASE WHEN r.n = 8 AND s.n IN (1, 12) THEN 'accessible'
                WHEN r.n >= 7                   THEN 'premium'
                ELSE 'standard' END
      FROM generate_series(1, 8) AS r(n), generate_series(1, 12) AS s(n)
    ON CONFLICT (auditorium_id, row_label, seat_num) DO NOTHING;

    INSERT INTO seats (auditorium_id, row_label, seat_num, kind)
    SELECT screen2, chr(64 + r.n), s.n,
           CASE WHEN r.n = 10 AND s.n IN (1, 14) THEN 'accessible'
                ELSE 'standard' END
      FROM generate_series(1, 10) AS r(n), generate_series(1, 14) AS s(n)
    ON CONFLICT (auditorium_id, row_label, seat_num) DO NOTHING;

    -- Times are written in Bangkok local and stored as timestamptz.
    base := date_trunc('day', now() AT TIME ZONE 'Asia/Bangkok');

    FOR d IN 0..6 LOOP
        -- A showtime from an earlier run occupies the same interval in the same
        -- auditorium, so the overlap constraint turns the repeat into a no-op.
        INSERT INTO showtimes (movie_id, auditorium_id, starts_at, ends_at, price_minor, currency)
        SELECT v.movie_id, v.auditorium_id, v.starts_at,
               v.starts_at + make_interval(mins => m.runtime_min) + turnaround,
               v.price_minor, 'THB'
          FROM (VALUES
                    (dune,        screen1, (base + make_interval(days => d, hours => 13))             AT TIME ZONE 'Asia/Bangkok', 18000),
                    (dune,        screen1, (base + make_interval(days => d, hours => 16, mins => 30)) AT TIME ZONE 'Asia/Bangkok', 22000),
                    (dune,        screen1, (base + make_interval(days => d, hours => 20))             AT TIME ZONE 'Asia/Bangkok', 26000),
                    (spirited,    screen2, (base + make_interval(days => d, hours => 14))             AT TIME ZONE 'Asia/Bangkok', 18000),
                    (oppenheimer, screen2, (base + make_interval(days => d, hours => 17, mins => 15)) AT TIME ZONE 'Asia/Bangkok', 22000),
                    (oppenheimer, screen2, (base + make_interval(days => d, hours => 21))             AT TIME ZONE 'Asia/Bangkok', 26000)
               ) AS v(movie_id, auditorium_id, starts_at, price_minor)
          JOIN movies m ON m.id = v.movie_id
        ON CONFLICT DO NOTHING;

        GET DIAGNOSTICS n = ROW_COUNT;
        added := added + n;
    END LOOP;

    RAISE NOTICE 'seed: % showtimes added', added;
END $$;

COMMIT;
