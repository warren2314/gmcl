CREATE TABLE IF NOT EXISTS report_exemptions (
    id BIGSERIAL PRIMARY KEY,
    season_id INTEGER NOT NULL REFERENCES seasons(id) ON DELETE CASCADE,
    week_id INTEGER NOT NULL REFERENCES weeks(id) ON DELETE CASCADE,
    team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    play_cricket_match_id BIGINT REFERENCES league_fixtures(play_cricket_match_id) ON DELETE SET NULL,
    match_date DATE NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_report_exemptions_fixture
    ON report_exemptions(team_id, play_cricket_match_id)
    WHERE play_cricket_match_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_report_exemptions_week_team
    ON report_exemptions(week_id, team_id);

CREATE INDEX IF NOT EXISTS idx_report_exemptions_match_date
    ON report_exemptions(match_date);
