-- +goose Up

CREATE TABLE movies (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title       text        NOT NULL CHECK (length(btrim(title)) > 0),
    runtime_min integer     NOT NULL CHECK (runtime_min > 0),
    rating      text        NOT NULL CHECK (length(btrim(rating)) > 0),
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auditoriums (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text        NOT NULL UNIQUE CHECK (length(btrim(name)) > 0),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE seats (
    id            bigint  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    auditorium_id bigint  NOT NULL REFERENCES auditoriums (id),
    row_label     text    NOT NULL CHECK (length(btrim(row_label)) > 0),
    seat_num      integer NOT NULL CHECK (seat_num > 0),
    kind          text    NOT NULL DEFAULT 'standard'
                          CHECK (kind IN ('standard', 'accessible', 'premium')),
    UNIQUE (auditorium_id, row_label, seat_num)
);

CREATE TABLE showtimes (
    id            bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    movie_id      bigint      NOT NULL REFERENCES movies (id),
    auditorium_id bigint      NOT NULL REFERENCES auditoriums (id),
    starts_at     timestamptz NOT NULL,
    price_minor   bigint      NOT NULL CHECK (price_minor >= 0),  -- satang for THB
    currency      text        NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    sales_open    boolean     NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (auditorium_id, starts_at)
);

CREATE INDEX showtimes_movie_starts_idx ON showtimes (movie_id, starts_at);
CREATE INDEX showtimes_starts_idx       ON showtimes (starts_at);

CREATE TABLE holds (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token        text        NOT NULL UNIQUE CHECK (token ~ '^[A-Z2-7]{26}$'),  -- 128 bits, base32
    showtime_id  bigint      NOT NULL REFERENCES showtimes (id),
    customer_ref text        NOT NULL CHECK (length(btrim(customer_ref)) > 0),
    expires_at   timestamptz NOT NULL,
    confirmed_at timestamptz,
    released_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT holds_terminal_state_once
        CHECK (confirmed_at IS NULL OR released_at IS NULL)
);

CREATE INDEX holds_showtime_idx ON holds (showtime_id);

-- Serves the section 4.3 sweeper, and nothing else.
CREATE INDEX holds_expiring_idx ON holds (expires_at)
    WHERE released_at IS NULL AND confirmed_at IS NULL;

CREATE TABLE seat_occupancy (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    showtime_id  bigint      NOT NULL REFERENCES showtimes (id),
    seat_id      bigint      NOT NULL REFERENCES seats (id),
    hold_id      bigint      NOT NULL REFERENCES holds (id),
    expires_at   timestamptz NOT NULL,  -- denormalized from holds, keeps reclaim join-free
    confirmed_at timestamptz,
    released_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT seat_occupancy_terminal_state_once
        CHECK (confirmed_at IS NULL OR released_at IS NULL)
);

-- The invariant. A 23505 from this index is a bug signal, not a routine outcome.
CREATE UNIQUE INDEX seat_occupancy_live_uniq
    ON seat_occupancy (showtime_id, seat_id)
    WHERE released_at IS NULL;

CREATE INDEX seat_occupancy_hold_idx ON seat_occupancy (hold_id);

CREATE TABLE bookings (
    id              bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ref             text        NOT NULL UNIQUE CHECK (ref ~ '^CB-[0-9A-Z]{4}-[0-9A-Z]{4}$'),
    hold_id         bigint      NOT NULL UNIQUE REFERENCES holds (id),
    showtime_id     bigint      NOT NULL REFERENCES showtimes (id),
    customer_ref    text        NOT NULL CHECK (length(btrim(customer_ref)) > 0),
    total_minor     bigint      NOT NULL CHECK (total_minor >= 0),  -- frozen at purchase
    currency        text        NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    idempotency_key text        NOT NULL CHECK (length(btrim(idempotency_key)) > 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    -- Scoped to the customer: the key is client chosen, so two customers
    -- picking "1" must not collide.
    UNIQUE (customer_ref, idempotency_key)
);

CREATE INDEX bookings_showtime_idx ON bookings (showtime_id);

-- Section 4.4. The one definition of live, so no read path can drift from it.
CREATE VIEW live_seat_occupancy AS
SELECT id,
       showtime_id,
       seat_id,
       hold_id,
       expires_at,
       confirmed_at,
       released_at,
       created_at
  FROM seat_occupancy
 WHERE released_at IS NULL
   AND (confirmed_at IS NOT NULL OR expires_at > now());

-- +goose Down

DROP VIEW live_seat_occupancy;
DROP TABLE bookings;
DROP TABLE seat_occupancy;
DROP TABLE holds;
DROP TABLE showtimes;
DROP TABLE seats;
DROP TABLE auditoriums;
DROP TABLE movies;
