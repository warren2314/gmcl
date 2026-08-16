INSERT INTO sanction_recipient_directory(recipient_role, name, email, active)
VALUES ('discipline', 'Dave Rogers', 'rogers@gtrmcrcricket.co.uk', TRUE)
ON CONFLICT (recipient_role, email) DO UPDATE
SET name = EXCLUDED.name,
    active = TRUE;
