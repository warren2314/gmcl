CREATE TABLE IF NOT EXISTS captain_change_requests (
    id BIGSERIAL PRIMARY KEY,
    team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    current_captain_id INTEGER NOT NULL REFERENCES captains(id) ON DELETE CASCADE,
    request_type TEXT NOT NULL CHECK (request_type IN ('permanent', 'temporary')),
    nominee_full_name TEXT NOT NULL,
    nominee_email TEXT NOT NULL,
    nominee_phone TEXT,
    effective_from DATE NOT NULL,
    effective_to DATE,
    reason TEXT,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at TIMESTAMPTZ,
    decided_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    decision_note TEXT,
    approved_captain_id INTEGER REFERENCES captains(id) ON DELETE SET NULL,
    CONSTRAINT captain_change_request_dates_valid CHECK (
        (request_type = 'permanent' AND effective_to IS NULL)
        OR
        (request_type = 'temporary' AND effective_to IS NOT NULL AND effective_to >= effective_from)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_captain_change_requests_pending_team
    ON captain_change_requests(team_id)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_captain_change_requests_status_requested
    ON captain_change_requests(status, requested_at DESC);

CREATE INDEX IF NOT EXISTS idx_captain_change_requests_team
    ON captain_change_requests(team_id, requested_at DESC);
