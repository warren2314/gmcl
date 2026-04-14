CREATE TABLE IF NOT EXISTS captain_form_config (
    id SERIAL PRIMARY KEY,
    season_id INTEGER NOT NULL REFERENCES seasons(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    intro_text TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (season_id)
);
