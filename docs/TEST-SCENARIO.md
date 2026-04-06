# Full test scenario (run locally with Docker Desktop)

This walks through the entire captain and admin flows using the seeded data.

## Prerequisites

- Docker Desktop installed and running
- No other service using port 80 (or change Caddy ports in `docker-compose.yml`)

## 1. One-time setup

1. **Create `.env` from the example**
   ```powershell
   copy .env.example .env
   ```
   Edit `.env` and set at least:
   - `SESSION_SECRET` — use a long random string (e.g. 32+ chars). Required for captain sessions.

2. **Start the stack**
   ```powershell
   docker compose up -d
   ```
   Or run without `-d` to watch logs in the foreground.

3. **Wait until the app is ready**
   In the logs you should see:
   - `migrations completed`
   - `listening on :8080`
   If the app exits with "migrations failed", ensure the `db` service is healthy (Compose will start the app after the DB healthcheck passes).

4. **Open in browser**
   - App (via Caddy): **http://localhost**
   - Health: **http://localhost/health** (should return `{"status":"ok"}`)

---

## 2. Captain flow

Seeded data: **Club ID 1**, **Team ID 1** (Test Club, First XI), one captain (captain@test.local).

### 2.1 Request magic link

1. Go to **http://localhost**
2. Enter **Club ID**: `1`, **Team ID**: `1`
3. Click **Send link**
4. You should see: *"If a captain record exists, a link has been emailed."*

### 2.2 Get the magic link (dev mode)

With `APP_ENV=dev`, the app does not send email; it prints the link to stdout.

1. View app logs:
   ```powershell
   docker compose logs app
   ```
2. Find a line like:
   ```
   Magic link for captain 1 (captain@test.local): http://localhost/magic-link/confirm?token=...
   ```
3. Copy the full URL (including the token).

### 2.3 Confirm and open the form

1. Paste the magic link into your browser (or click it if the log is clickable).
2. You should see **"Open feedback form"** and a **Continue** button.
3. Click **Continue**.
4. You should be redirected to **/captain/form**.

### 2.4 Fill and submit the form

1. Enter **Pitch rating (1–5)**: e.g. `4`
2. **Outfield rating (1–5)**: e.g. `4`
3. **Facilities rating (1–5)**: e.g. `3`
4. **Comments**: e.g. *"Pitch played well."*
5. Optionally wait a moment — autosave runs on the comments field (you may see "Saved at HH:MM:SS").
6. Click **Submit**
7. You should see **"Thank you for your submission."** and the session is cleared (you cannot go back to the form without a new magic link).

---

## 3. Admin flow

Seeded admin (when `SEED=1`): **username** `admin`, **password** `admin123` (or the value of `SEED_ADMIN_PASSWORD` in `.env`).

### 3.1 Login and 2FA

1. Go to **http://localhost/admin/login**
2. **Username**: `admin`  
   **Password**: `admin123`
3. Click **Continue**
4. You are redirected to **/admin/2fa**

### 3.2 Get the 2FA code (dev mode)

With no SMTP configured, the app logs the 2FA email to stdout.

1. Run:
   ```powershell
   docker compose logs app --tail 50
   ```
2. Find a line like:
   ```
   email send (dev fallback) to=admin@localhost subject=Your admin login code body=Your one-time code is: 123456
   ```
3. Use the 6-digit code (e.g. `123456`) on the 2FA page.
4. Click **Verify** (or the equivalent submit).
5. You should be redirected to the admin dashboard (**/admin/dashboard**).

### 3.3 Admin pages

- **Dashboard**: **http://localhost/admin/dashboard**
- **Weeks**: **http://localhost/admin/weeks**
- **Submissions**: From weeks or dashboard, open a submission to see detail.
- **Rankings**: **http://localhost/admin/rankings**
- **CSV captains**: **http://localhost/admin/csv/captains** — upload a CSV to preview/apply (see app or README for expected format).

---

## 4. Build plan for reminders, sanctions, and delegation

This section turns the new requirements into a practical delivery plan. It should be used as both scope definition and acceptance criteria for implementation.

### 4.1 Product rules to implement

#### Messaging channels

- Use **Twilio** as the outbound provider for:
  - email
  - WhatsApp
- The system should select the correct contact for the reporting window:
  - default: captain on record
  - if delegated: active delegate for that fixture/week

#### Reminder schedule

- **Saturday**: send to captains who played on that Saturday.
- **Sunday**: send to captains who played on that Sunday.
- **Monday**: send a reminder to teams still missing a report.
- **Wednesday**: send a final reminder to teams still missing a report.
- Reporting deadline is **23:59 Wednesday Europe/London**.

#### Sanctions

- If the report is still missing at **23:59 Wednesday**, issue a **yellow card**.
- After **3 yellow cards**, escalate to a **red card**.
- Every sanction must retain the fixture/week context and the chain of missed reminders that led to it.

#### Delegation

- Delegation must be easier than the current email-only invite flow.
- A captain should be able to reply with another email address or phone number.
- For WhatsApp, the experience should behave like a lightweight ongoing conversation with the app.
- Delegation must be traceable per reporting window so admins can see exactly who was responsible.

### 4.2 Recommended delivery order

Build this in four releases so the risky parts land in a controlled order.

#### Release 1: data model and reminder engine

Goal: we can determine who should be contacted, when they should be contacted, and record every attempt.

- Add contact and messaging tables.
- Extend the internal reminder job so it can calculate Saturday, Sunday, Monday, and Wednesday targets.
- Keep sending disabled or dev-only until audit logging and idempotency are in place.

#### Release 2: Twilio delivery and status capture

Goal: outbound messages are sent through Twilio and statuses flow back into the app.

- Implement a Twilio client/service layer.
- Send reminder messages by channel.
- Add webhook endpoints for delivery status callbacks where Twilio supports them.
- Persist provider identifiers, provider statuses, and provider error payloads.

#### Release 3: sanctions and templated notices

Goal: the Wednesday cutoff automatically creates sanctions and notifies the right parties.

- Implement yellow card calculation after the Wednesday cutoff job runs.
- Track cumulative yellow cards and red card escalation.
- Add sanction templates with variable insertion.
- Send sanction notices to the captain and the configured GMCL recipient.

#### Release 4: admin dashboard and conversational delegation

Goal: operations can manage and explain the system, and captains can delegate through the contact flow.

- Add dashboard, drill-down, and audit screens.
- Add admin configuration for GMCL recipients and message templates.
- Add inbound reply handling for delegation via WhatsApp and, if feasible, email reply parsing.

### 4.3 Data model changes

Recommended new tables or extensions:

- `captain_contacts`
  - captain_id
  - contact_type (`email`, `whatsapp`)
  - contact_value
  - is_primary
  - is_verified
  - created_at / updated_at
- `reporting_recipients`
  - season_id
  - week_id
  - fixture_id or team_id
  - recipient_role (`captain`, `delegate`)
  - recipient_name
  - recipient_email
  - recipient_phone
  - source (`admin`, `captain_reply`, `manual_override`)
  - active_from / active_to
- `outbound_messages`
  - season_id
  - week_id
  - fixture_id or team_id
  - message_type (`initial_sat`, `initial_sun`, `reminder_mon`, `final_reminder_wed`, `yellow_card_notice`, `red_card_notice`)
  - channel (`email`, `whatsapp`)
  - template_key
  - intended_recipient
  - resolved_recipient
  - provider (`twilio`)
  - provider_message_id
  - send_status (`queued`, `sent`, `delivered`, `failed`, `undelivered`)
  - error_code
  - error_message
  - sent_at
  - delivered_at
  - metadata JSONB
- `message_events`
  - outbound_message_id
  - event_type
  - provider_status
  - payload JSONB
  - created_at
- `sanctions`
  - season_id
  - week_id
  - fixture_id or team_id
  - captain_id
  - sanction_type (`yellow`, `red`)
  - reason_code (`missing_report_deadline`)
  - yellow_card_count_at_issue
  - issued_at
  - issued_by (`system`, admin id if manual)
  - metadata JSONB
- `sanction_recipients`
  - sanction_id
  - recipient_role (`captain`, `gmcl`)
  - channel
  - outbound_message_id
- `gmcl_notification_recipients`
  - name
  - email
  - phone
  - receives_sanctions
  - active

Likely existing tables to extend:

- `captain_delegations`
  - add phone-based delegation support if it does not already exist
  - add `source_channel` and `source_message_id`
- `audit_logs`
  - add clearer metadata conventions for outbound messaging, callbacks, sanctions, and delegation changes

### 4.4 Core services to build

#### Reminder planner

Create an internal service that resolves:

- what fixtures count as Saturday and Sunday fixtures
- whether a report already exists
- who the active recipient is
- which reminder stage applies
- whether a send has already happened for that fixture/stage/channel

This service should drive `POST /internal/send-reminders` rather than embedding the rules in the handler.

#### Outbound messaging service

Create a dedicated service layer, for example `internal/messaging`, responsible for:

- rendering templates with substitutions
- choosing the channel
- calling Twilio
- storing the outbound message row before send
- updating status after send attempt
- writing audit events

Keep Twilio-specific code behind a small interface so local/dev mode can use a logger or fake provider.

#### Sanction engine

Create a service that runs after the Wednesday deadline and:

- finds teams still missing reports
- creates yellow cards exactly once per missed report
- calculates whether the current sanction should escalate to red
- triggers sanction notices
- writes full audit history

#### Inbound reply processor

Create webhook handlers for inbound message events that can:

- link a reply to the latest open reporting conversation
- parse or present a delegation instruction
- create/update the effective delegate for that reporting window
- audit the change and preserve the source message

### 4.5 Internal endpoints and scheduled jobs

Recommended internal jobs:

- `POST /internal/send-reminders`
  - accepts a date or dry-run option
  - determines whether to run Saturday, Sunday, Monday, or Wednesday logic
  - returns counts by stage/channel/status
- `POST /internal/run-sanctions`
  - runs after Wednesday 23:59 cutoff
  - creates yellow/red cards idempotently
  - sends sanction notices
- `POST /internal/sync-message-status`
  - optional helper if webhook delivery is incomplete and polling is needed

Recommended webhook endpoints:

- `POST /webhooks/twilio/status`
  - receives delivery updates
- `POST /webhooks/twilio/inbound`
  - receives inbound WhatsApp replies and possibly email-relay events if supported

Scheduling recommendation:

- Saturday morning or early afternoon job for Saturday fixtures
- Sunday morning or early afternoon job for Sunday fixtures
- Monday reminder job
- Wednesday final reminder job before cutoff
- Wednesday 23:59 or shortly after cutoff sanction job

### 4.6 Idempotency and rules safety

This feature will create operational problems if sends or sanctions can duplicate. Build these protections in from the start.

- For each fixture/team + reminder stage + channel, allow only one active outbound message record unless a resend is explicitly triggered.
- For each missed report, allow only one sanction row per sanction event.
- If a report arrives after a reminder but before sanction processing, reminder history remains, but sanction creation must stop.
- If a report arrives after sanction creation, do not delete the sanction; instead show that the report was submitted late.
- All scheduled jobs should support dry-run output for staging/admin verification.

### 4.7 Template and content requirements

Store templates in a way that admins can manage safely later, even if initial versions live in code.

Reminder templates need substitutions for:

- captain/delegate name
- club/team
- fixture description
- submission link
- reporting deadline

Sanction templates need substitutions for:

- captain name
- club/team
- fixture/week
- sanction type
- yellow card count
- next consequence if another breach occurs
- GMCL contact/sign-off

### 4.8 Admin requirements

The admin area needs more than a single dashboard number. It needs operational visibility and explainability.

Add these screens or modules:

- **Dashboard**
  - outstanding reports
  - reminders sent today
  - failed sends
  - yellow cards this week
  - red cards this season
- **Reminder activity**
  - filter by week, team, captain, channel, status
  - open a message timeline with provider events
- **Sanctions**
  - filter by season, week, team, captain, sanction type
  - drill into why the sanction was triggered
- **Delegations**
  - current effective delegates
  - source of delegation
  - start/end window
- **Configuration**
  - GMCL recipients
  - template content
  - Twilio/test mode settings if exposed in admin

Every admin drill-down should be able to answer:

- which reminders were due
- which reminders were attempted
- whether Twilio accepted them
- whether Twilio later reported delivery failure
- who the effective recipient was at the time
- why a yellow or red card was issued

### 4.9 Audit and compliance expectations

Capture an audit trail for:

- reminder planning
- reminder send attempts
- reminder provider callbacks
- sanction creation
- sanction notice delivery
- delegation creation/change/removal
- admin edits to GMCL recipients or templates

Read/seen state should be treated as optional. If Twilio does not provide reliable read-state for a channel, the product must still function without it.

### 4.10 Technical decisions still to make

These are the main open items before implementation starts:

- Should yellow/red cards attach to the **team**, the **captain**, or both?
- Does a red card replace the third yellow card event, or is it issued as the consequence of reaching three yellows?
- Is delegation valid per fixture, per week, until revoked, or selectable by the captain each time?
- Do we support inbound email parsing now, or deliver WhatsApp reply handling first and keep email delegation manual for phase one?
- Should the GMCL sanction recipient be one global contact, multiple contacts, or configurable by division/competition?

Recommended default decisions for phase one:

- Sanctions attach to both the team context and the captain-on-record at the time of breach.
- The third yellow creates a red sanction event while preserving the previous yellow history.
- Delegation is valid per reporting week unless explicitly changed.
- Ship WhatsApp inbound delegation first; keep email delegation as admin-assisted until proven necessary.
- GMCL recipients are configured in admin and can support more than one active recipient.

### 4.11 Suggested sprint breakdown

#### Sprint 1

- schema for contacts, outbound messages, message events, sanctions, GMCL recipients
- reminder planner service
- dry-run internal reminder endpoint

#### Sprint 2

- Twilio outbound integration
- Twilio status webhook
- audit/event persistence
- dev/test provider fallback

#### Sprint 3

- sanction engine
- sanction templates
- GMCL recipient admin configuration
- sanction notices

#### Sprint 4

- dashboard and drill-down screens
- delegation admin views
- conversational delegation via inbound WhatsApp

#### Sprint 5

- hardening
- retries/resend controls
- end-to-end test scenarios
- reporting polish and operational docs

### 4.12 Definition of done

This feature set is ready when:

- Saturday, Sunday, Monday, and Wednesday reminder jobs target the correct teams.
- No duplicate reminder is created for the same stage unless an admin explicitly resends.
- Twilio send attempts and callback outcomes are visible in admin.
- Missing Wednesday reports create yellow cards automatically.
- The third yellow produces the expected red-card escalation behaviour.
- Sanction notices go to both the captain/delegate path and the configured GMCL recipient.
- Admin users can drill into any yellow/red card and explain exactly why it happened.
- Delegation by WhatsApp reply works for at least one supported happy path.
- Audit history is complete enough to troubleshoot failed sends and sanction disputes.

---

## 5. GMCL seeded teams

When `SEED=1` the following clubs, teams, and captains are created automatically.
They use fictional Play-Cricket team IDs so that fixture-sync and umpire-prefill
can be tested locally without the real API.

### 5.1 Seeded club and team reference

| Club | Short | Team | PC Team ID | Captain | Captain email |
|------|-------|------|-----------|---------|--------------|
| Test Club | TC | First XI | *(none)* | Test Captain | captain@test.local |
| Heaton Mersey CC | HM | First XI | 10011 | James Holden | hm1@gmcl.test |
| Heaton Mersey CC | HM | Second XI | 10012 | Paul Whitworth | hm2@gmcl.test |
| Denton St Lawrence CC | DSL | First XI | 10021 | Amir Rashid | dsl1@gmcl.test |
| Denton St Lawrence CC | DSL | Second XI | 10022 | Connor Finney | dsl2@gmcl.test |
| Woodhouses CC | WH | First XI | 10031 | Thomas Briggs | wh1@gmcl.test |
| Woodhouses CC | WH | Second XI | 10032 | Declan Walsh | wh2@gmcl.test |
| Prestwich CC | PRE | First XI | 10041 | Ravi Patel | pre1@gmcl.test |
| Prestwich CC | PRE | Second XI | 10042 | Simon Clarke | pre2@gmcl.test |
| Swinton Moorside CC | SM | First XI | 10051 | Mark Thornton | sm1@gmcl.test |
| Royton CC | ROY | First XI | 10061 | Nathan Webb | roy1@gmcl.test |
| Royton CC | ROY | Second XI | 10062 | Craig Doyle | roy2@gmcl.test |
| Norden CC | NOR | First XI | 10071 | Oliver Ramshaw | nor1@gmcl.test |
| Hyde CC | HYD | First XI | 10081 | Darren Lees | hyd1@gmcl.test |
| Hyde CC | HYD | Second XI | 10082 | Bhav Sharma | hyd2@gmcl.test |

To look up IDs after seeding:
```sql
SELECT cl.name, t.name, t.play_cricket_team_id, c.email
FROM captains c
JOIN teams t ON c.team_id = t.id
JOIN clubs cl ON t.club_id = cl.id
ORDER BY cl.name, t.name;
```

### 5.2 Request a magic link for a GMCL captain

The captain flow works for any seeded team. You need the **Club ID** and **Team ID**,
not the Play-Cricket ID. Look them up with:
```sql
SELECT cl.id AS club_id, t.id AS team_id, cl.name, t.name
FROM teams t JOIN clubs cl ON t.club_id = cl.id ORDER BY cl.name, t.name;
```

Then follow the same steps as Section 2, substituting those IDs.

Example: to test as Heaton Mersey CC First XI captain (hm1@gmcl.test):
1. Find Club ID for "Heaton Mersey CC" and Team ID for their "First XI" from the query above.
2. Go to **http://localhost**, enter those IDs, click **Send link**.
3. Check `docker compose logs app` for the magic link.
4. Complete the form as described in Section 2.4.

---

## 6. Fixture sync testing

These steps test the `POST /internal/sync-league-fixtures` endpoint using raw JSON
(no real Play-Cricket API needed). The HMAC signature must match `N8N_HMAC_SECRET` in `.env`.

### 6.1 Helper script: sign and send

Save the following as a local PowerShell helper (adjust `$secret` to match your `.env`):

```powershell
$secret = "your-hmac-secret-here"
$body = @'
{
  "raw_body": {
    "match_details": [
      {
        "match_id": "90001",
        "league_id": "5501",
        "competition_id": "88101",
        "match_date": "05/04/2025",
        "ground_name": "Heaton Mersey CC Ground",
        "home_team_name": "Heaton Mersey CC - 1st XI",
        "home_team_id": "10011",
        "home_club_name": "Heaton Mersey CC",
        "home_club_id": "2001",
        "away_team_name": "Denton St Lawrence CC - 1st XI",
        "away_team_id": "10021",
        "away_club_name": "Denton St Lawrence CC",
        "away_club_id": "2002",
        "umpire_1_name": "R. Patel",
        "umpire_2_name": "S. Khan",
        "umpire_3_name": ""
      },
      {
        "match_id": "90002",
        "league_id": "5501",
        "competition_id": "88102",
        "match_date": "05/04/2025",
        "ground_name": "Woodhouses CC Ground",
        "home_team_name": "Woodhouses CC - 1st XI",
        "home_team_id": "10031",
        "home_club_name": "Woodhouses CC",
        "home_club_id": "2003",
        "away_team_name": "Prestwich CC - 1st XI",
        "away_team_id": "10041",
        "away_club_name": "Prestwich CC",
        "away_club_id": "2004",
        "umpire_1_name": "T. Briggs",
        "umpire_2_name": "D. Walsh",
        "umpire_3_name": ""
      }
    ]
  }
}
'@
$ts = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds().ToString()
$nonce = [System.Guid]::NewGuid().ToString("N").Substring(0,12)
$method = "POST"
$path = "/internal/sync-league-fixtures"
$ct = ""
$msg = "$ts|$nonce|$method|$path|$ct|$body"
$hmac = New-Object System.Security.Cryptography.HMACSHA256
$hmac.Key = [System.Text.Encoding]::UTF8.GetBytes($secret)
$sig = [System.BitConverter]::ToString($hmac.ComputeHash(
    [System.Text.Encoding]::UTF8.GetBytes($msg)
)).Replace("-","").ToLower()

Invoke-RestMethod `
  -Uri "http://localhost/internal/sync-league-fixtures" `
  -Method POST `
  -Body $body `
  -ContentType "application/json" `
  -Headers @{ "X-Signature" = $sig; "X-Timestamp" = $ts; "X-Nonce" = $nonce }
```

Expected response:
```json
{"status":"ok","count":2}
```

### 6.2 Verify fixtures were stored

```sql
SELECT play_cricket_match_id, match_date, home_team_name, away_team_name,
       umpire_1_name, umpire_2_name
FROM league_fixtures
WHERE play_cricket_match_id IN (90001, 90002)
ORDER BY play_cricket_match_id;
```

Expected: two rows with correct umpire names and date `2025-04-05`.

### 6.3 Verify umpire prefill on the captain form

1. Find the Team ID for Heaton Mersey CC First XI:
   ```sql
   SELECT t.id FROM teams t JOIN clubs c ON c.id = t.club_id
   WHERE c.name = 'Heaton Mersey CC' AND t.name = 'First XI';
   ```
2. Request a magic link for that captain (Section 5.2).
3. Open the captain form at **/captain/form**.
4. Set the **match date** to `2025-04-05`.
5. The umpire fields should auto-populate with **R. Patel** and **S. Khan**.

If they do not prefill, check that `teams.play_cricket_team_id = '10011'` for that row:
```sql
SELECT id, name, play_cricket_team_id FROM teams WHERE id = <team_id>;
```

### 6.4 Full Saturday fixture set for testing

The following JSON block covers all seeded GMCL teams for a Saturday round.
Use it as the value of `raw_body` in the sync request from Section 6.1.

```json
{
  "match_details": [
    {
      "match_id": "90001", "league_id": "5501", "competition_id": "88101",
      "match_date": "05/04/2025", "ground_name": "Heaton Mersey CC Ground",
      "home_team_name": "Heaton Mersey CC - 1st XI", "home_team_id": "10011",
      "home_club_name": "Heaton Mersey CC", "home_club_id": "2001",
      "away_team_name": "Denton St Lawrence CC - 1st XI", "away_team_id": "10021",
      "away_club_name": "Denton St Lawrence CC", "away_club_id": "2002",
      "umpire_1_name": "R. Patel", "umpire_2_name": "S. Khan", "umpire_3_name": ""
    },
    {
      "match_id": "90002", "league_id": "5501", "competition_id": "88102",
      "match_date": "05/04/2025", "ground_name": "Woodhouses CC Ground",
      "home_team_name": "Woodhouses CC - 1st XI", "home_team_id": "10031",
      "home_club_name": "Woodhouses CC", "home_club_id": "2003",
      "away_team_name": "Prestwich CC - 1st XI", "away_team_id": "10041",
      "away_club_name": "Prestwich CC", "away_club_id": "2004",
      "umpire_1_name": "T. Briggs", "umpire_2_name": "D. Walsh", "umpire_3_name": ""
    },
    {
      "match_id": "90003", "league_id": "5501", "competition_id": "88103",
      "match_date": "05/04/2025", "ground_name": "Royton CC Ground",
      "home_team_name": "Royton CC - 1st XI", "home_team_id": "10061",
      "home_club_name": "Royton CC", "home_club_id": "2006",
      "away_team_name": "Norden CC - 1st XI", "away_team_id": "10071",
      "away_club_name": "Norden CC", "away_club_id": "2007",
      "umpire_1_name": "M. Thornton", "umpire_2_name": "J. Clarke", "umpire_3_name": ""
    },
    {
      "match_id": "90004", "league_id": "5501", "competition_id": "88104",
      "match_date": "05/04/2025", "ground_name": "Swinton Moorside CC Ground",
      "home_team_name": "Swinton Moorside CC - 1st XI", "home_team_id": "10051",
      "home_club_name": "Swinton Moorside CC", "home_club_id": "2005",
      "away_team_name": "Hyde CC - 1st XI", "away_team_id": "10081",
      "away_club_name": "Hyde CC", "away_club_id": "2008",
      "umpire_1_name": "N. Webb", "umpire_2_name": "O. Ramshaw", "umpire_3_name": ""
    }
  ]
}
```

### 6.5 Sunday 2nd XI fixture set

```json
{
  "match_details": [
    {
      "match_id": "90101", "league_id": "5501", "competition_id": "88201",
      "match_date": "06/04/2025", "ground_name": "Heaton Mersey CC Ground",
      "home_team_name": "Heaton Mersey CC - 2nd XI", "home_team_id": "10012",
      "home_club_name": "Heaton Mersey CC", "home_club_id": "2001",
      "away_team_name": "Prestwich CC - 2nd XI", "away_team_id": "10042",
      "away_club_name": "Prestwich CC", "away_club_id": "2004",
      "umpire_1_name": "N. Webb", "umpire_2_name": "O. Ramshaw", "umpire_3_name": ""
    },
    {
      "match_id": "90102", "league_id": "5501", "competition_id": "88202",
      "match_date": "06/04/2025", "ground_name": "Hyde CC Ground",
      "home_team_name": "Hyde CC - 2nd XI", "home_team_id": "10082",
      "home_club_name": "Hyde CC", "home_club_id": "2008",
      "away_team_name": "Royton CC - 2nd XI", "away_team_id": "10062",
      "away_club_name": "Royton CC", "away_club_id": "2006",
      "umpire_1_name": "B. Sharma", "umpire_2_name": "C. Doyle", "umpire_3_name": ""
    },
    {
      "match_id": "90103", "league_id": "5501", "competition_id": "88203",
      "match_date": "06/04/2025", "ground_name": "Denton St Lawrence CC Ground",
      "home_team_name": "Denton St Lawrence CC - 2nd XI", "home_team_id": "10022",
      "home_club_name": "Denton St Lawrence CC", "home_club_id": "2002",
      "away_team_name": "Woodhouses CC - 2nd XI", "away_team_id": "10032",
      "away_club_name": "Woodhouses CC", "away_club_id": "2003",
      "umpire_1_name": "", "umpire_2_name": "", "umpire_3_name": ""
    }
  ]
}
```

Note: match 90103 has no umpires assigned yet — this tests that the form shows empty
umpire fields and does not prefill when the API returns blanks.

---

## 7. Internal endpoints (n8n)

These require HMAC signing. For a quick local test you can skip them or use a tool that signs requests with the shared secret. See `internal/middleware/hmac.go` and `docs/DESIGN.md` for the signature format.

Current and planned endpoints for this area:

- `POST /internal/send-reminders`
- `POST /internal/generate-weekly-report`
- `POST /internal/run-sanctions` (planned)
- `POST /webhooks/twilio/status` (planned)
- `POST /webhooks/twilio/inbound` (planned)

---

## 8. Resetting and re-running

- **Reset database (re-run migrations and seed)**  
  Remove the DB volume and start again:
  ```powershell
  docker compose down -v
  docker compose up -d
  ```
  Then wait for "migrations completed" and "listening on :8080".

- **Stop the stack**
  ```powershell
  docker compose down
  ```

---

## 9. Troubleshooting

| Issue | What to check |
|-------|----------------|
| "No active match week" | Seed creates a week containing `CURRENT_DATE`. Ensure `SEED=1` ran and the app has been restarted after seed. |
| "If a captain record exists..." but no link in logs | Confirm club 1 / team 1 and that seed created the captain. Check `docker compose logs app` for errors. |
| Captain form redirects to `/` | Session cookie requires `SESSION_SECRET` in `.env`. |
| Admin 2FA "invalid code" | Use the **latest** code from logs; codes expire after 10 minutes. |
| App exits with "migrations failed" | DB might not be ready. Ensure `db` has a healthcheck and app has `depends_on: db: condition: service_healthy`. |
| Port 80 in use | Change Caddy ports in `docker-compose.yml` (e.g. `"8081:80"`) and use **http://localhost:8081**. |
| Fixture sync returns 401 | HMAC signature mismatch. Regenerate the signature using the exact body string (no extra whitespace). Check `N8N_HMAC_SECRET` matches between `.env` and your script. |
| Umpire prefill not working | Confirm `teams.play_cricket_team_id` is set for the team (use the SQL in Section 6.3). Check that the fixture was stored in `league_fixtures` for the correct date. |
| GMCL captain "record not found" | The magic-link lookup uses club_id + team_id (not Play-Cricket IDs). Use the SQL in Section 5.2 to find the correct numeric IDs. |
