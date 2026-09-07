# Case dropped notifications

On an open case assigned to you, use **Close with no action**. Tick the clubs
that should receive a closure notice, enter the private closure reason, confirm
that no sanction or approval request is required, and submit.

The selector shows the home and away clubs where the linked Play-Cricket fixture
has mapped teams. Other linked case clubs are shown by name as **Case club**;
staff should select the intended club by name. Either club or both can be
selected. Leaving every box unticked closes without email.

Each selected club receives a separate notice through its active, verified
official mailbox. Shared addresses receive one copy. The subject identifies the
case reference. The message says the case has been dropped and closed with no
further action, and no response is required. Private reasons, reporter details,
evidence and response links are not included.

Closure still cancels pending response requests, reminders, unsent messages and
open follow-up tasks. The new notices are then queued in the same transaction,
with recipient and outbox records in the case history. Invalid recipients or
disabled outbound email prevent a requested notification and roll back closure.
The existing outbox worker handles delivery and failures; queued does not mean
delivered. Existing sanctions and ineligible-player email switches apply.

Historic batch closure continues to close without email. Open individual cases
when notices are needed.

Validation: `go test ./internal/httpserver ./internal/sanctions` passes. Database
integration tests require `TEST_DB_DSN`; live SMTP delivery has not been tested
as part of this change. No new migration is required.
