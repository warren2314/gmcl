ALTER TABLE sanction_intakes
    ADD COLUMN IF NOT EXISTS is_training BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_sanction_intakes_training_queue
    ON sanction_intakes(external_created_at DESC, id DESC)
    WHERE is_training;
