-- Development and test fixture, deliberately not a migration: a replica running
-- goose up in production must not end up with demo data.

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
BEGIN
    IF EXISTS (SELECT 1 FROM auditoriums) THEN
        RAISE NOTICE 'seed: already applied, nothing to do';
        RETURN;
    END IF;

    INSERT INTO movies (title, runtime_min, rating) VALUES ('Dune: Part Two', 166, '13+')  RETURNING id INTO dune;
    INSERT INTO movies (title, runtime_min, rating) VALUES ('Spirited Away',  125, 'G')    RETURNING id INTO spirited;
    INSERT INTO movies (title, runtime_min, rating) VALUES ('Oppenheimer',    180, '15+')  RETURNING id INTO oppenheimer;

    INSERT INTO auditoriums (name) VALUES ('Screen 1') RETURNING id INTO screen1;
    INSERT INTO auditoriums (name) VALUES ('Screen 2') RETURNING id INTO screen2;

    INSERT INTO seats (auditorium_id, row_label, seat_num, kind)
    SELECT screen1, chr(64 + r.n), s.n,
           CASE WHEN r.n = 8 AND s.n IN (1, 12) THEN 'accessible'
                WHEN r.n >= 7                   THEN 'premium'
                ELSE 'standard' END
      FROM generate_series(1, 8) AS r(n), generate_series(1, 12) AS s(n);

    INSERT INTO seats (auditorium_id, row_label, seat_num, kind)
    SELECT screen2, chr(64 + r.n), s.n,
           CASE WHEN r.n = 10 AND s.n IN (1, 14) THEN 'accessible'
                ELSE 'standard' END
      FROM generate_series(1, 10) AS r(n), generate_series(1, 14) AS s(n);

    -- Times are written in Bangkok local and stored as timestamptz.
    base := date_trunc('day', now() AT TIME ZONE 'Asia/Bangkok');

    FOR d IN 0..1 LOOP
        INSERT INTO showtimes (movie_id, auditorium_id, starts_at, price_minor, currency) VALUES
            (dune,        screen1, (base + make_interval(days => d, hours => 13))             AT TIME ZONE 'Asia/Bangkok', 18000, 'THB'),
            (dune,        screen1, (base + make_interval(days => d, hours => 16, mins => 30)) AT TIME ZONE 'Asia/Bangkok', 22000, 'THB'),
            (dune,        screen1, (base + make_interval(days => d, hours => 20))             AT TIME ZONE 'Asia/Bangkok', 26000, 'THB'),
            (spirited,    screen2, (base + make_interval(days => d, hours => 14))             AT TIME ZONE 'Asia/Bangkok', 18000, 'THB'),
            (oppenheimer, screen2, (base + make_interval(days => d, hours => 17, mins => 15)) AT TIME ZONE 'Asia/Bangkok', 22000, 'THB'),
            (oppenheimer, screen2, (base + make_interval(days => d, hours => 21))             AT TIME ZONE 'Asia/Bangkok', 26000, 'THB');
    END LOOP;
END $$;

COMMIT;
