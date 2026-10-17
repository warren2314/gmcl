CREATE TABLE junior_forms (
 id BIGSERIAL PRIMARY KEY,
 season_id BIGINT NOT NULL REFERENCES junior_seasons(id),
 title TEXT NOT NULL CHECK(length(title) BETWEEN 1 AND 200),
 current_version_id BIGINT,
 created_by_admin_id INTEGER NOT NULL REFERENCES admin_users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE junior_form_versions (
 id BIGSERIAL PRIMARY KEY,
 form_id BIGINT NOT NULL REFERENCES junior_forms(id),
 revision INTEGER NOT NULL CHECK(revision>0),
 title TEXT NOT NULL CHECK(length(title) BETWEEN 1 AND 200),
 description TEXT NOT NULL DEFAULT '',
 fields JSONB NOT NULL CHECK(jsonb_typeof(fields)='array'),
 due_date DATE NOT NULL,
 requires_review BOOLEAN NOT NULL DEFAULT true,
 published_by_admin_id INTEGER NOT NULL REFERENCES admin_users(id),
 published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(form_id,revision), UNIQUE(id,form_id)
);
ALTER TABLE junior_forms ADD CONSTRAINT junior_forms_current_version_fk
 FOREIGN KEY(current_version_id,id) REFERENCES junior_form_versions(id,form_id);
CREATE TABLE junior_form_assignments (
 id BIGSERIAL PRIMARY KEY,
 form_id BIGINT NOT NULL REFERENCES junior_forms(id),
 version_id BIGINT NOT NULL,
 club_id INTEGER NOT NULL REFERENCES clubs(id),
 status TEXT NOT NULL DEFAULT 'assigned' CHECK(status IN('assigned','returned','submitted','completed','superseded')),
 latest_submission_id BIGINT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 FOREIGN KEY(version_id,form_id) REFERENCES junior_form_versions(id,form_id),
 UNIQUE(version_id,club_id)
);
CREATE TABLE junior_form_drafts (
 assignment_id BIGINT PRIMARY KEY REFERENCES junior_form_assignments(id),
 revision INTEGER NOT NULL CHECK(revision>0),
 values JSONB NOT NULL DEFAULT '{}',
 updated_by_account_id BIGINT REFERENCES junior_accounts(id),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE junior_form_submissions (
 id BIGSERIAL PRIMARY KEY,
 assignment_id BIGINT NOT NULL REFERENCES junior_form_assignments(id),
 revision INTEGER NOT NULL CHECK(revision>0),
 values JSONB NOT NULL,
 submitted_by_account_id BIGINT NOT NULL REFERENCES junior_accounts(id),
 submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(assignment_id,revision), UNIQUE(id,assignment_id)
);
ALTER TABLE junior_form_assignments ADD CONSTRAINT junior_form_latest_submission_fk
 FOREIGN KEY(latest_submission_id,id) REFERENCES junior_form_submissions(id,assignment_id);
CREATE TABLE junior_form_reviews (
 id BIGSERIAL PRIMARY KEY,
 assignment_id BIGINT NOT NULL REFERENCES junior_form_assignments(id),
 submission_id BIGINT NOT NULL,
 action TEXT NOT NULL CHECK(action IN('complete','return')),
 reason TEXT NOT NULL,
 reviewed_by_admin_id INTEGER NOT NULL REFERENCES admin_users(id),
 reviewed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 FOREIGN KEY(submission_id,assignment_id) REFERENCES junior_form_submissions(id,assignment_id)
);
CREATE INDEX junior_form_assignment_work_idx ON junior_form_assignments(club_id,status,form_id);
CREATE FUNCTION junior_form_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'published junior forms and submitted responses are immutable'; END;
$$;
CREATE TRIGGER junior_form_version_immutable BEFORE UPDATE OR DELETE ON junior_form_versions
 FOR EACH ROW EXECUTE FUNCTION junior_form_immutable();
CREATE TRIGGER junior_form_submission_immutable BEFORE UPDATE OR DELETE ON junior_form_submissions
 FOR EACH ROW EXECUTE FUNCTION junior_form_immutable();
CREATE TRIGGER junior_form_review_immutable BEFORE UPDATE OR DELETE ON junior_form_reviews
 FOR EACH ROW EXECUTE FUNCTION junior_form_immutable();
