ALTER TABLE submissions
    ADD COLUMN IF NOT EXISTS play_cricket_match_id BIGINT
        REFERENCES league_fixtures(play_cricket_match_id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_submissions_play_cricket_match
    ON submissions(play_cricket_match_id);
