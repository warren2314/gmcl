# Team-entry forms and league approval

Team entries are a required question type in the existing official form builder. A club can supply up to ten named teams and age groups. Submitting always requires league review; no team, competition entry or fixture is created by submission alone.

On the response, staff open **Review team entries and choose leagues**. Choose a competition/division from the same season and matching age for every row, preview the exact create/reuse/retain effects, then approve. Returning the form for changes remains available. An appropriate competition can be created first under Competitions and fixtures.

Approval is atomic: permanent club teams and active competition entries are saved together with the completed response, immutable approval snapshot and audit event. Existing case/spacing matches are reused. Existing archived or ambiguous team records and a team already allocated to another competition at that age require staff resolution instead of silently creating or moving records. Team names include their age, for example U13 A. Existing records are not renamed or deleted by approval.

Submission ID, current team matches, participation and competition revisions bind the reviewed preview. Changed records require another preview. Assignment and team-creation locks plus unique club/name and competition/team constraints protect concurrent approvals and duplicate requests. Replaying the same approved receipt is a no-op; resubmitting the same teams through another entry form reuses them. Normal team editing and roster creation use the same team-creation lock and spacing checks.

Fixture generation and publication remain separate. Entry changes advance the competition revision so fixture checks must be reviewed again. A once/home-away format remains automatic; mixed once/twice pairings use the existing custom manual schedule.

## Sunday demonstration

The isolated demo gains **2027 Team Entry Form** for Oakfield and Riverside, plus **U15 Junior League / North Division**. Riverside has a submitted U13 A (existing, already entered) and U15 B (new) example for Joe to preview. Oakfield remains outstanding for the actual club sign-in journey. The extension is recorded once and preserves the existing demonstration records across restarts. No production seeding or real email is used.

Routine portal correspondence is shared with the club lead. Confidential safeguarding reports must stay in the existing safeguarding process; a separate restricted correspondence route is not implemented by this team-entry change.

## Validation

Database-backed HTTP/service regressions cover club submission, forced review, explicit allocation preview, staff-only approval/CSRF, stale revisions, wrong-age allocations, preview without mutation, atomic concurrent approval and replay, repeated forms reusing teams/entries, changed participation/closed seasons and permanent receipts. Existing forms and the populated demo rehearsal remain part of the full suite. Hosted acceptance and candidate CI receipts are recorded in the local handoff.
