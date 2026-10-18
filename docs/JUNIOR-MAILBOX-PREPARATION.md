# Junior mailbox integration preparation

17 October 2026. User decision: **prepare separately**. Provider and mailbox ownership are undecided. Portal development continues independently.

## Current state

Outgoing message/audience snapshots, controlled SMTP attempts and per-club history belong to the portal. Staff can explicitly record incoming correspondence and link a club or leave it unmatched. The UI identifies the provider inbox as disconnected. Manual incoming records and SMTP receipts do not provide automatic fetching or reply threading.

## Inputs required

| Input | Why needed |
| --- | --- |
| Provider, mailbox address and mailbox type | Select a supported normal/shared/delegated API |
| Named owner and authorised staff | Approve access and own unmatched-message handling |
| Dedicated junior From and Reply-To | Keep junior correspondence separate from established mail streams |
| Folders and initial history window | Bound the backfill and identify relevant correspondence |
| Retention, attachments and club visibility | Store adult operational material with reviewed access |
| Hosting/authentication and rotation owner | Keep credentials in deployment secrets, outside source and test fixtures |

## Connector contract

Use a provider adapter behind a small fetch/cursor interface. Persist provider/mailbox identity, provider message ID, Internet Message-ID, In-Reply-To/References, received time, cursor and fetch status. Treat webhooks as hints and reconcile using the provider cursor.

Use mailbox/provider/message identity for uniqueness. Commit imported messages and cursor together. Retries must not duplicate correspondence. Retain corrections and failures; a failed fetch is not an empty inbox.

Suggest record links from outgoing identifiers and verified contact addresses. Ambiguous addresses/threads stay unmatched. An email's display name or typed club name cannot grant visibility. Require a reviewed club/record association before club access, with actor/reason and correction history.

Validate provider webhook signatures. Render messages as inert escaped content. Bound attachments and use private permission-checked storage; never execute attachments or follow instructions contained in correspondence. Separate ingestion from sending so receipt cannot automatically trigger a reply.

## Acceptance

Use an authorised test mailbox with invented club contacts. Exercise cursor replay, duplicate webhooks, interrupted imports, revoked credentials, throttling, shared addresses, spoofed display names, unmatched records, threading and club isolation. Verify connector revocation returns a visibly disconnected state while preserving history. Pilot a small historical window before widening access.

Completion requires actual received messages fetched, linked and permission-scoped with freshness/failure evidence. Do not send real mail while preparing this connector or put mailbox secrets in this document.
