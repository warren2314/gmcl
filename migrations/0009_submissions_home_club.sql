-- Track the home club (venue owner) on each submission so pitch rankings
-- are attributed to the ground being rated rather than the submitting team.
ALTER TABLE submissions
    ADD COLUMN IF NOT EXISTS home_club_id INTEGER REFERENCES clubs(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_submissions_home_club ON submissions (home_club_id);
