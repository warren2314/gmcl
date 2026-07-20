-- Decisions on players who meet the 30 June first-XI appearance threshold but
-- are not present on the published starred list.
CREATE TABLE IF NOT EXISTS starred_candidate_reviews (
    id                     BIGSERIAL PRIMARY KEY,
    candidate_key          TEXT NOT NULL UNIQUE,
    season_year            INTEGER NOT NULL,
    club_name              TEXT NOT NULL,
    club_key               TEXT NOT NULL,
    play_cricket_player_id BIGINT,
    player_name            TEXT NOT NULL,
    player_key             TEXT NOT NULL,
    first_xi_league        INTEGER NOT NULL,
    all_league             INTEGER NOT NULL,
    percentage             NUMERIC(7,6) NOT NULL,
    status                 TEXT NOT NULL DEFAULT 'accepted'
                           CHECK (status IN ('accepted')),
    decision_note          TEXT,
    reviewed_by            INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    reviewed_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_starred_candidate_reviews_season_status
    ON starred_candidate_reviews(season_year, status);
