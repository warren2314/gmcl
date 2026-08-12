-- Allow investigators to delegate a clearly scoped piece of casework to
-- another authorised sanctions administrator. These are operational tasks;
-- they do not change the case decision, correspondence or outcome.

ALTER TABLE sanction_follow_up_tasks
    DROP CONSTRAINT IF EXISTS sanction_follow_up_tasks_task_type_check;

ALTER TABLE sanction_follow_up_tasks
    ADD CONSTRAINT sanction_follow_up_tasks_task_type_check CHECK (task_type IN
        ('play_cricket_points','fine_recovery','board_intervention','suspended_review',
         'appeal_deadline','ban_expiry','notice_failure','migration_exception',
         'investigation_support'));

CREATE INDEX IF NOT EXISTS idx_sanction_follow_up_tasks_assignee_active
    ON sanction_follow_up_tasks(assigned_admin_id,status,due_at,id)
    WHERE status IN ('open','in_progress');

-- Delegation remains an explicit, attributed administrator action.
