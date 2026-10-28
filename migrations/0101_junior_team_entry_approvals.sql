CREATE TABLE junior_team_entry_approvals (
 submission_id BIGINT PRIMARY KEY,
 assignment_id BIGINT NOT NULL,
 preview_hash TEXT NOT NULL,
 items JSONB NOT NULL,
 approved_by_admin_id INTEGER NOT NULL REFERENCES admin_users(id),
 approved_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 FOREIGN KEY(submission_id,assignment_id) REFERENCES junior_form_submissions(id,assignment_id)
);
CREATE TRIGGER junior_team_entry_approval_immutable BEFORE UPDATE OR DELETE ON junior_team_entry_approvals
 FOR EACH ROW EXECUTE FUNCTION junior_form_immutable();
