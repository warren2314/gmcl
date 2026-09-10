ALTER TABLE sanction_effect_revisions
    DROP CONSTRAINT sanction_effect_revisions_effect_type_check;
ALTER TABLE sanction_effect_revisions
    ADD CONSTRAINT sanction_effect_revisions_effect_type_check CHECK (effect_type IN
        ('yellow_card','red_card','scheduled_red','suspended_red','player_ban','team_ban','fine',
         'card_points','points_adjustment','warning','no_action')),
    ADD COLUMN target_season_id INTEGER REFERENCES seasons(id) ON DELETE RESTRICT,
    ADD COLUMN red_card_count INTEGER NOT NULL DEFAULT 1 CHECK (red_card_count BETWEEN 1 AND 100),
    ADD CONSTRAINT sanction_effect_scheduled_red_fields CHECK (
        effect_type <> 'scheduled_red' OR
        (target_season_id IS NOT NULL AND starts_at IS NOT NULL AND NULLIF(BTRIM(trigger_condition),'') IS NULL));

-- A future award has one operational application, tied to its immutable effect
-- identity. Approval retries cannot create an additional points task.
ALTER TABLE sanction_follow_up_tasks
    ADD COLUMN effect_key UUID,
    ADD COLUMN not_before TIMESTAMPTZ;
CREATE UNIQUE INDEX idx_sanction_task_effect_once
    ON sanction_follow_up_tasks(task_type,effect_key) WHERE effect_key IS NOT NULL;
