ALTER TABLE starred_candidate_reviews
    ADD COLUMN IF NOT EXISTS first_second_xi_league INTEGER NOT NULL DEFAULT 0;
