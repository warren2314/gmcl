CREATE TABLE IF NOT EXISTS starred_finding_reviews (
    id                    BIGSERIAL PRIMARY KEY,
    finding_key           TEXT NOT NULL UNIQUE,
    season_year           INTEGER NOT NULL,
    play_cricket_match_id BIGINT NOT NULL REFERENCES starred_match_imports(play_cricket_match_id) ON DELETE CASCADE,
    match_date            DATE NOT NULL,
    club_name             TEXT NOT NULL,
    club_key              TEXT NOT NULL,
    team_name             TEXT NOT NULL,
    play_cricket_player_id BIGINT,
    player_name           TEXT NOT NULL,
    player_key            TEXT NOT NULL,
    list_type             TEXT NOT NULL CHECK (list_type IN ('A', 'B')),
    status                TEXT NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'accepted', 'draft', 'approved', 'sent', 'send_failed')),
    decision_note         TEXT,
    captain_id            INTEGER REFERENCES captains(id) ON DELETE SET NULL,
    captain_name          TEXT,
    captain_email         TEXT,
    email_subject         TEXT,
    email_body            TEXT,
    reviewed_by           INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    reviewed_at           TIMESTAMPTZ,
    approved_by           INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    approved_at           TIMESTAMPTZ,
    sent_at               TIMESTAMPTZ,
    send_error            TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_starred_finding_reviews_season_status
    ON starred_finding_reviews(season_year, status);
CREATE INDEX IF NOT EXISTS idx_starred_finding_reviews_match
    ON starred_finding_reviews(play_cricket_match_id);
