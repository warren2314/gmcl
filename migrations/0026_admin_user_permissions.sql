CREATE TABLE IF NOT EXISTS admin_user_permissions (
    admin_user_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    permission TEXT NOT NULL,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (admin_user_id, permission)
);

CREATE INDEX IF NOT EXISTS idx_admin_user_permissions_permission
    ON admin_user_permissions(permission);

INSERT INTO admin_user_permissions (admin_user_id, permission)
SELECT id, 'view_umpire_feedback'
FROM admin_users
WHERE LOWER(username) IN ('guyhaynes', 'denverthornton')
ON CONFLICT DO NOTHING;
