ALTER TABLE umpire_pitch_reports
    ADD COLUMN IF NOT EXISTS source_kind TEXT NOT NULL DEFAULT 'panel_form';

CREATE UNIQUE INDEX IF NOT EXISTS ux_umpire_pitch_reports_play_cricket_ground
    ON umpire_pitch_reports(play_cricket_match_id, source_kind)
    WHERE source_kind = 'play_cricket_ground';
