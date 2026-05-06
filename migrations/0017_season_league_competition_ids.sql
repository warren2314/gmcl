-- Allows a season to restrict fixture counting to specific Play-Cricket
-- competition IDs (e.g. league only, excluding cup/knockout competitions).
-- Empty array means all competitions count.
ALTER TABLE seasons
    ADD COLUMN IF NOT EXISTS league_competition_ids TEXT[] NOT NULL DEFAULT '{}';
