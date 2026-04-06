-- Store full GMCL questionnaire responses (umpires section + pitch/ground criteria)
ALTER TABLE submissions ADD COLUMN IF NOT EXISTS form_data JSONB;
