-- Grading remains attached to both the original immutable submission/receipt
-- and the approved competition entry. Existing entries have no assumed grade.
ALTER TABLE junior_competition_entries ADD COLUMN requested_grade TEXT NOT NULL DEFAULT '' CHECK(length(requested_grade)<=100);
ALTER TABLE junior_competition_entries ADD COLUMN grading_notes TEXT NOT NULL DEFAULT '' CHECK(length(grading_notes)<=2000);
ALTER TABLE junior_competition_entries ADD COLUMN zone TEXT NOT NULL DEFAULT '' CHECK(length(zone)<=100);
ALTER TABLE junior_competition_entries ADD COLUMN source_assignment_id BIGINT REFERENCES junior_form_assignments(id);
ALTER TABLE junior_competition_entries ADD COLUMN source_submission_id BIGINT REFERENCES junior_form_submissions(id);
CREATE TABLE junior_external_ground_bookings (
 id BIGSERIAL PRIMARY KEY,
 ground_id BIGINT NOT NULL REFERENCES junior_grounds(id),
 title TEXT NOT NULL CHECK(length(btrim(title)) BETWEEN 1 AND 200),
 category TEXT NOT NULL CHECK(category IN ('women','senior','other')),
 match_date DATE NOT NULL,
 start_time TEXT NOT NULL CHECK(start_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
 duration_minutes INTEGER NOT NULL CHECK(duration_minutes BETWEEN 1 AND 1440),
 source TEXT NOT NULL CHECK(length(btrim(source)) BETWEEN 1 AND 500),
 cancelled BOOLEAN NOT NULL DEFAULT false,
 created_by_admin_id INTEGER NOT NULL REFERENCES admin_users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX junior_external_ground_day ON junior_external_ground_bookings(ground_id,match_date) WHERE NOT cancelled;
