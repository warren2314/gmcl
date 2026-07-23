ALTER TABLE starred_candidate_reviews
    DROP CONSTRAINT IF EXISTS starred_candidate_reviews_status_check;

ALTER TABLE starred_candidate_reviews
    ADD CONSTRAINT starred_candidate_reviews_status_check
    CHECK (status IN ('requested', 'accepted'));

ALTER TABLE starred_candidate_reviews
    ADD COLUMN IF NOT EXISTS requested_cutoff DATE;
