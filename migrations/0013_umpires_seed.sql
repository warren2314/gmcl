ALTER TABLE umpires ADD CONSTRAINT IF NOT EXISTS umpires_full_name_unique UNIQUE (full_name);
