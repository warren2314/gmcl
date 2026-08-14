-- Align case 170's existing open investigation-support work with its current
-- case owner. Earlier assignment code changed only the case owner, leaving
-- owner-held tasks on the prior administrator. Preserve role-specific and
-- deliberately delegated work by limiting this correction to case 170's
-- active investigation_support tasks.
DO $$
DECLARE
    target_admin_id INTEGER;
    task_row RECORD;
    after_row JSONB;
BEGIN
    SELECT assigned_admin_id
    INTO target_admin_id
    FROM sanction_cases
    WHERE id = 170;

    IF target_admin_id IS NOT NULL THEN
        FOR task_row IN
            SELECT task.id, to_jsonb(task) AS before_data
            FROM sanction_follow_up_tasks task
            WHERE task.case_id = 170
              AND task.task_type = 'investigation_support'
              AND task.status IN ('open', 'in_progress')
              AND task.assigned_admin_id IS DISTINCT FROM target_admin_id
            FOR UPDATE
        LOOP
            UPDATE sanction_follow_up_tasks task
            SET assigned_admin_id = target_admin_id,
                updated_at = now()
            WHERE task.id = task_row.id
            RETURNING to_jsonb(task) INTO after_row;

            INSERT INTO sanction_follow_up_task_events(
                task_id, actor_admin_id, actor_label, reason,
                before_data, after_data, request_id
            ) VALUES (
                task_row.id, NULL, 'system migration',
                'Aligned case 170 investigation task with the current case owner',
                task_row.before_data, after_row,
                'migration-0078-case-170-owner-task'
            );
        END LOOP;
    END IF;
END $$;
