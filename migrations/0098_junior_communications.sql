CREATE TABLE junior_messages (
    id BIGSERIAL PRIMARY KEY,
    season_id BIGINT NOT NULL REFERENCES junior_seasons(id),
    club_id INTEGER REFERENCES clubs(id),
    kind TEXT NOT NULL,
    record_type TEXT NOT NULL DEFAULT '',
    record_id BIGINT NOT NULL DEFAULT 0,
    idempotency_key TEXT NOT NULL UNIQUE,
    subject TEXT NOT NULL CHECK (length(btrim(subject)) BETWEEN 1 AND 250),
    body TEXT NOT NULL CHECK (length(btrim(body)) BETWEEN 1 AND 50000),
    created_by_admin_id INTEGER REFERENCES admin_users(id),
    created_by_account_id BIGINT REFERENCES junior_accounts(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE junior_message_recipients (
    id BIGSERIAL PRIMARY KEY,
    message_id BIGINT NOT NULL REFERENCES junior_messages(id),
    email TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','sending','accepted','failed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    lease_until TIMESTAMPTZ,
    accepted_at TIMESTAMPTZ,
    UNIQUE(message_id,email),
    UNIQUE(id,message_id)
);
CREATE TABLE junior_message_audiences (
    id BIGSERIAL PRIMARY KEY,
    message_id BIGINT NOT NULL REFERENCES junior_messages(id),
    recipient_id BIGINT NOT NULL,
    club_id INTEGER NOT NULL REFERENCES clubs(id),
    contact_id BIGINT,
    name TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (recipient_id,message_id) REFERENCES junior_message_recipients(id,message_id),
    FOREIGN KEY (contact_id,club_id) REFERENCES junior_contacts(id,club_id)
);
CREATE UNIQUE INDEX junior_message_audience_unique ON junior_message_audiences(message_id,recipient_id,club_id,COALESCE(contact_id,0));
CREATE INDEX junior_message_audiences_club ON junior_message_audiences(club_id,message_id);
CREATE TABLE junior_message_attempts (
    id BIGSERIAL PRIMARY KEY,
    recipient_id BIGINT NOT NULL REFERENCES junior_message_recipients(id),
    actor_admin_id INTEGER NOT NULL REFERENCES admin_users(id),
    status TEXT NOT NULL CHECK (status IN ('accepted','failed')),
    failure TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE FUNCTION junior_message_snapshot_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN RAISE EXCEPTION 'junior message snapshots cannot be deleted'; END IF;
    IF TG_TABLE_NAME='junior_message_recipients' THEN
        IF NEW.id=OLD.id AND NEW.message_id=OLD.message_id AND NEW.email=OLD.email AND NEW.name=OLD.name THEN RETURN NEW; END IF;
    END IF;
    RAISE EXCEPTION 'junior message content and audience snapshots are immutable';
END;
$$;
CREATE TRIGGER junior_messages_immutable BEFORE UPDATE OR DELETE ON junior_messages FOR EACH ROW EXECUTE FUNCTION junior_message_snapshot_guard();
CREATE TRIGGER junior_recipients_snapshot_immutable BEFORE UPDATE OR DELETE ON junior_message_recipients FOR EACH ROW EXECUTE FUNCTION junior_message_snapshot_guard();
CREATE TRIGGER junior_audiences_immutable BEFORE UPDATE OR DELETE ON junior_message_audiences FOR EACH ROW EXECUTE FUNCTION junior_message_snapshot_guard();
CREATE TRIGGER junior_attempts_immutable BEFORE UPDATE OR DELETE ON junior_message_attempts FOR EACH ROW EXECUTE FUNCTION junior_message_snapshot_guard();

-- Until a verified provider connector exists, inbound correspondence is
-- explicitly recorded by staff, never described as a connected/synced inbox.
CREATE TABLE junior_incoming_messages (
    id BIGSERIAL PRIMARY KEY,
    club_id INTEGER REFERENCES clubs(id),
    from_email TEXT NOT NULL,
    subject TEXT NOT NULL CHECK (length(btrim(subject)) BETWEEN 1 AND 250),
    body TEXT NOT NULL CHECK (length(btrim(body)) BETWEEN 1 AND 50000),
    record_type TEXT NOT NULL DEFAULT '',
    record_id BIGINT NOT NULL DEFAULT 0,
    recorded_by_admin_id INTEGER NOT NULL REFERENCES admin_users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TRIGGER junior_incoming_immutable BEFORE UPDATE OR DELETE ON junior_incoming_messages FOR EACH ROW EXECUTE FUNCTION junior_message_snapshot_guard();
CREATE TABLE junior_incoming_club_links (
    id BIGSERIAL PRIMARY KEY,
    incoming_id BIGINT NOT NULL REFERENCES junior_incoming_messages(id),
    club_id INTEGER NOT NULL REFERENCES clubs(id),
    actor_admin_id INTEGER NOT NULL REFERENCES admin_users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TRIGGER junior_incoming_links_immutable BEFORE UPDATE OR DELETE ON junior_incoming_club_links FOR EACH ROW EXECUTE FUNCTION junior_message_snapshot_guard();
