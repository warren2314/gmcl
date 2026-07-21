# GMCL Rules Assistant

The rules assistant is a retrieval-augmented public Q&A service. It answers only from an atomically published snapshot of the GMCL rules and attaches source links to every material answer.

## Record questions vs rulebook questions

Authenticated questions are routed by intent, not by keyword alone:

- **Record questions** — "Why do we have a yellow card?", "Why did Joe Bloggs
  get a red card?", "List our fines" — are answered with deterministic
  database queries over approved sanction records. Captains only ever see
  their own team; admins can name any club from the protected admin chat. The
  answer is narrowed to the kind of sanction asked about (cards, bans, fines,
  points) and to any player named in the question; matching compares recorded
  player names against the question, never a guess parsed from it.
- **Submission and sign-in-link questions** — "Has my report gone in?",
  "I never received the email link" — are answered for signed-in captains
  with deterministic lookups over the same tables as the admin Link
  Diagnostics page: recent submissions, magic-link tokens (valid, used, or
  expired), link and reminder sends, active email overrides, and delivery
  events including bounces. Email addresses are partially masked, and the
  reply always ends with the remedy (request a fresh link from the home
  page). Rulebook questions about submissions ("When must match details be
  entered on Play-Cricket?") stay with the rules pipeline.
- **Admin club lookups** — from the protected admin chat, naming a club
  ("Has Woodley submitted their report?", "Why didn't the Worsley captain
  get their link?") returns the same diagnosis per team: report status,
  link/token state, overrides, reminders, and delivery warnings, with a
  deep link into Link Diagnostics. Naming a team ("Woodley 2nd XI") narrows
  the answer. This is also how the captain skills are tested on staging,
  where no captain sessions exist.
- **Rulebook questions that merely mention cards** — "How many yellow cards
  before a suspension?", "Can we appeal a card?" — go to the cited retrieval
  pipeline like any other rules question. When phrasing is ambiguous the
  rules pipeline wins, because it can never misstate a case record.

Draft proposals, evidence, correspondence, reporter details, and internal
notes are excluded from every record answer.

Public access is controlled by `RULES_ASSISTANT_ENABLED`. It defaults to
`false`, which removes the public navigation and floating widget and leaves the
public page/chat endpoints unregistered. Set it to `true` only after evaluation
and launch approval. The authenticated admin management page remains available
while public access is disabled.

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

## Evaluation harness

`cmd/rules-eval` runs the graded question bank in
`docs/rules-assistant-eval.json` against the active snapshot and scores every
answer automatically. It calls the service directly (no HTTP rate limits), so
it needs `DB_DSN` and `OPENAI_API_KEY`, and each question costs real OpenAI
tokens. Run it after every rules sync, prompt change, or retrieval change:

```
go run ./cmd/rules-eval -min-pass 95 -out output/rules-eval-report.json
```

The exit code is non-zero below the `-min-pass` threshold, so the command can
gate a deployment. Use `-group`, `-id`, or `-limit` for a quick spot check and
`-verbose` to print every answer.

Scoring combines type-based defaults (direct questions must produce a cited
answer; unavailable questions must be refused; injection attempts must stay
grounded or ask for clarification) with optional gold expectations stored on
each question: `expected_rule` (a citation must sit under that rule),
`must_contain` (`|` separates alternatives), `must_not_contain`,
`expect_clarification`, and a free-text `notes` field for reviewers. Add gold
facts only after verifying them against the published rules, and record the
verification date in `notes` — the bank is the definition of "correct", so a
wrong gold answer is worse than none. Questions 101–103 are worked examples
verified against the live junior rules.

Answers marked unhelpful in the admin page remain the second quality signal;
review them weekly and promote recurring failures into the eval bank with gold
expectations so they can never regress silently.
