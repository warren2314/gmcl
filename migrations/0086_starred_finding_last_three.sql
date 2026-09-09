-- Rule 4.6.3.3.5.1 findings use the same review workflow as List A/B.
ALTER TABLE starred_finding_reviews
    DROP CONSTRAINT starred_finding_reviews_list_type_check;
ALTER TABLE starred_finding_reviews
    ADD CONSTRAINT starred_finding_reviews_list_type_check
    CHECK (list_type IN ('A', 'B', 'Last 3'));
