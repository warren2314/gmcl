-- Junior operational status is separate from shared club identity and senior reporting.
CREATE TABLE junior_club_status (
    club_id INTEGER PRIMARY KEY REFERENCES clubs(id),
    inactive BOOLEAN NOT NULL DEFAULT false,
    reason TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE junior_club_seasons DROP CONSTRAINT junior_club_seasons_state_check;
ALTER TABLE junior_club_seasons ADD CONSTRAINT junior_club_seasons_state_check
    CHECK (state IN ('active', 'withdrawn', 'not_entered'));
