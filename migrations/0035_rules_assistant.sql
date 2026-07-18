CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS rule_releases (
    id BIGSERIAL PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('building','active','archived','failed')),
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    published_at TIMESTAMPTZ,
    source_count INTEGER NOT NULL DEFAULT 0,
    chunk_count INTEGER NOT NULL DEFAULT 0,
    changed_source_count INTEGER NOT NULL DEFAULT 0,
    error_message TEXT,
    created_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_rule_releases_active
    ON rule_releases ((status)) WHERE status = 'active';
CREATE UNIQUE INDEX IF NOT EXISTS uq_rule_releases_building
    ON rule_releases ((status)) WHERE status = 'building';

CREATE TABLE IF NOT EXISTS rule_documents (
    id BIGSERIAL PRIMARY KEY,
    release_id BIGINT NOT NULL REFERENCES rule_releases(id) ON DELETE CASCADE,
    canonical_url TEXT NOT NULL,
    title TEXT NOT NULL,
    page_updated_label TEXT,
    content_hash TEXT NOT NULL,
    extracted_text TEXT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (release_id, canonical_url)
);

CREATE TABLE IF NOT EXISTS rule_chunks (
    id BIGSERIAL PRIMARY KEY,
    release_id BIGINT NOT NULL REFERENCES rule_releases(id) ON DELETE CASCADE,
    document_id BIGINT NOT NULL REFERENCES rule_documents(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    rule_reference TEXT,
    heading_path TEXT NOT NULL,
    content TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    embedding vector(1536) NOT NULL,
    search_vector tsvector GENERATED ALWAYS AS (
        to_tsvector('english', coalesce(rule_reference, '') || ' ' || heading_path || ' ' || content)
    ) STORED,
    UNIQUE (document_id, ordinal)
);

CREATE INDEX IF NOT EXISTS idx_rule_chunks_release ON rule_chunks(release_id);
CREATE INDEX IF NOT EXISTS idx_rule_chunks_search ON rule_chunks USING GIN(search_vector);
CREATE INDEX IF NOT EXISTS idx_rule_chunks_embedding
    ON rule_chunks USING hnsw (embedding vector_cosine_ops);

CREATE TABLE IF NOT EXISTS rule_chat_conversations (
    id UUID PRIMARY KEY,
    abuse_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '90 days')
);

CREATE TABLE IF NOT EXISTS rule_chat_messages (
    id UUID PRIMARY KEY,
    conversation_id UUID NOT NULL REFERENCES rule_chat_conversations(id) ON DELETE CASCADE,
    release_id BIGINT REFERENCES rule_releases(id) ON DELETE SET NULL,
    user_question TEXT NOT NULL,
    assistant_answer TEXT NOT NULL,
    clarification_needed BOOLEAN NOT NULL DEFAULT FALSE,
    citations JSONB NOT NULL DEFAULT '[]'::jsonb,
    retrieved_chunk_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    model TEXT NOT NULL,
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_rule_chat_messages_created ON rule_chat_messages(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_rule_chat_conversations_expiry ON rule_chat_conversations(expires_at);

CREATE TABLE IF NOT EXISTS rule_chat_feedback (
    id BIGSERIAL PRIMARY KEY,
    message_id UUID NOT NULL REFERENCES rule_chat_messages(id) ON DELETE CASCADE,
    rating TEXT NOT NULL CHECK (rating IN ('helpful','unhelpful','report')),
    comment TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (message_id, rating)
);

ALTER TABLE sanctions ADD COLUMN IF NOT EXISTS offence_date DATE;
ALTER TABLE sanctions ADD COLUMN IF NOT EXISTS remedy_due_at TIMESTAMPTZ;
ALTER TABLE sanctions ADD COLUMN IF NOT EXISTS appeal_due_at TIMESTAMPTZ;
ALTER TABLE sanctions ADD COLUMN IF NOT EXISTS served_at TIMESTAMPTZ;
ALTER TABLE sanctions ADD COLUMN IF NOT EXISTS rule_release_id BIGINT REFERENCES rule_releases(id) ON DELETE SET NULL;
ALTER TABLE sanctions ADD COLUMN IF NOT EXISTS rule_reference TEXT;

CREATE TABLE IF NOT EXISTS sanction_events (
    id BIGSERIAL PRIMARY KEY,
    sanction_id BIGINT NOT NULL REFERENCES sanctions(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    event_at TIMESTAMPTZ NOT NULL,
    notes TEXT,
    created_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sanction_events_sanction ON sanction_events(sanction_id, event_at);

CREATE OR REPLACE FUNCTION record_sanction_event() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO sanction_events(sanction_id,event_type,event_at,notes,created_by_admin_id)
        VALUES(NEW.id,'issued',NEW.issued_at,NEW.reason,NEW.issued_by_admin_id);
    ELSIF NEW.status IS DISTINCT FROM OLD.status THEN
        INSERT INTO sanction_events(sanction_id,event_type,event_at,notes,created_by_admin_id)
        VALUES(NEW.id,'status_' || NEW.status,now(),NEW.notes,NEW.resolved_by_admin_id);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_record_sanction_event ON sanctions;
CREATE TRIGGER trg_record_sanction_event
AFTER INSERT OR UPDATE OF status ON sanctions
FOR EACH ROW EXECUTE FUNCTION record_sanction_event();
