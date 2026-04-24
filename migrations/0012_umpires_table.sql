CREATE TABLE IF NOT EXISTS umpires (
    id BIGSERIAL PRIMARY KEY,
    forename TEXT NOT NULL,
    surname  TEXT NOT NULL,
    active   BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT umpires_name_unique UNIQUE (forename, surname)
);
