ALTER TABLE starred_import_runs
    ADD COLUMN IF NOT EXISTS parser_revision TEXT NOT NULL DEFAULT '1';
