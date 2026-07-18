# GMCL Rules Assistant

The rules assistant is a retrieval-augmented public Q&A service. It answers only from an atomically published snapshot of the GMCL rules and attaches source links to every material answer.

## Configuration

- `OPENAI_API_KEY` enables embeddings, sync, and chat.
- `OPENAI_CHAT_MODEL` defaults to `gpt-5.6-terra`.
- `OPENAI_EMBEDDING_MODEL` defaults to `text-embedding-3-small` and must return 1,536 dimensions.
- `RULES_SOURCE_URL` defaults to the GMCL rules main menu.
- `RULES_ASSISTANT_SECRET` provides the HMAC key for rotating abuse identifiers. Set a distinct random production value.

The Postgres service uses the `pgvector/pgvector:pg16` image while retaining the existing `db_data` volume. Migration `0035_rules_assistant.sql` enables the vector extension and adds versioned source, chat-quality, and sanction-timeline storage.

## Initial sync and schedule

1. Deploy the database image and application migration.
2. In the admin portal, open **Rules Assistant** and run the initial sync.
3. Confirm all eight rule groups and the expected chunk count appear in the active snapshot.
4. Schedule a nightly HMAC-signed `POST /internal/sync-rules` from n8n.

A crawl is published only when validation and every embedding succeed. The prior active release remains live on failure. Super-admins can reactivate an archived release.

## Quality and privacy

The admin page shows recent questions, answers, feedback, model, and latency. Conversation records use a random browser identifier and rotating HMAC abuse key; raw IP addresses and names are not stored. Records expire after 90 days and can be purged with `Service.PurgeExpired` from the normal privacy cleanup schedule.

Before public launch, run the evaluation questions in `rules-assistant-eval.json` against the active snapshot and manually score groundedness and citation correctness. Both must reach 95%, and no answer may invent a penalty or date.
