# Amazon SES Setup

This app sends mail through SMTP and can receive Amazon SES events through an SNS webhook.

## What the app already exposes

- SMTP sending is configured with `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, and `SMTP_FROM`.
- SES/SNS events are received at:

```text
https://gmcl.co.uk/webhooks/aws/ses?token=<SES_SNS_WEBHOOK_TOKEN>
```

- Events are stored in `email_events`.
- Admin UI: `/admin/email-health`. The reminder ledger shows every SMTP-accepted
  n8n/admin reminder and correlates it with the strongest SES result received
  (`Delivered`, `Bounced`, `Complaint`, or still awaiting an SES event).
- The webhook diagnostics table records subscription confirmations, rejected
  payloads, storage failures, and successfully stored SNS events. It supports
  both SNS-wrapped and raw HTTP/S delivery and both SES `eventType`
  (configuration-set publishing) and legacy `notificationType` payloads.
- Optional reminder send failures are stored in `captain_reminder_failures`.

## 1. Verify the sender domain in SES

Use the same AWS Region for all SES and SNS work. For GMCL, use the production SES region already used by the account.

1. Open Amazon SES.
2. Go to `Configuration > Verified identities`.
3. Create or open the `gmcl.co.uk` domain identity.
4. Add the SES DKIM records to DNS and wait until SES shows the identity as verified.
5. Make sure `webmaster@gmcl.co.uk` is allowed as the `SMTP_FROM` sender.

CLI alternative:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\setup-ses-events.ps1 `
  -Region eu-west-2 `
  -Domain gmcl.co.uk `
  -WebhookUrl "https://gmcl.co.uk/webhooks/aws/ses?token=<strong-random-token>" `
  -PlanOnly
```

Remove `-PlanOnly` once the output looks right:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\setup-ses-events.ps1 `
  -Region eu-west-2 `
  -Domain gmcl.co.uk `
  -WebhookUrl "https://gmcl.co.uk/webhooks/aws/ses?token=<strong-random-token>"
```

If you use a named AWS CLI profile, add `-Profile your-profile-name`.

The script creates/checks:

- SES domain identity
- SNS topic
- HTTPS SNS subscription to the app webhook
- SES configuration set
- SES event destination for delivery, bounce, complaint, delay, open, and click events

It also prints the DKIM DNS records you must add manually.

## 2. Create SES SMTP credentials

1. In Amazon SES, open `SMTP settings`.
2. Create SMTP credentials.
3. Save the SMTP username and password securely.
4. Configure production `.env`:

```bash
SMTP_HOST=email-smtp.<region>.amazonaws.com
SMTP_PORT=587
SMTP_USERNAME=<ses-smtp-username>
SMTP_PASSWORD=<ses-smtp-password>
SMTP_FROM=webmaster@gmcl.co.uk
```

Use the SMTP credentials from SES, not normal AWS access keys.

## 3. Create an SNS topic for SES events

1. Open Amazon SNS in the same Region as SES.
2. Create a Standard topic, for example `gmcl-ses-events`.
3. Create an HTTPS subscription:

```text
https://gmcl.co.uk/webhooks/aws/ses?token=<strong-random-token>
```

4. Put the same token in production `.env`:

```bash
SES_SNS_WEBHOOK_TOKEN=<strong-random-token>
SES_SNS_AUTO_CONFIRM=1
```

5. Deploy/restart the app.
6. In SNS, confirm the subscription. With `SES_SNS_AUTO_CONFIRM=1`, the app will try to auto-confirm when SNS sends the confirmation message.
7. After confirmation, set `SES_SNS_AUTO_CONFIRM=0` and restart on the next deploy.

## 4. Connect SES events to SNS

Preferred setup:

1. In SES, create a configuration set named:

```text
gmcl-captain-reports
```

2. Add an event destination to that configuration set.
3. Destination type: SNS.
4. SNS topic: `gmcl-ses-events`.
5. Event types: `Delivery`, `Bounce`, `Complaint`.
6. Optional later: add `Open` and `Click` if you want engagement tracking in AWS. The current app stores delivery/bounce/complaint events.
7. Add this to production `.env`:

```bash
SES_CONFIGURATION_SET=gmcl-captain-reports
```

The app adds `X-SES-CONFIGURATION-SET` to every outgoing SMTP message when this variable is set.

Configuration-set events use the SES `eventType` JSON field. This differs from
the `notificationType` field used by legacy identity feedback notifications;
the webhook accepts both formats.

Alternative setup:

- Configure feedback notifications directly on the `gmcl.co.uk` SES identity for bounce, complaint, and delivery.
- This is simpler, but the configuration set is cleaner because it is explicit per app/email stream.

## 5. Deploy and test

Deploy the latest code and run migrations:

```bash
cd /opt/gmcl
bash scripts/deploy.sh
```

Send a test email from the app:

```bash
curl -X POST https://gmcl.co.uk/internal/preview-email \
  -H "Authorization: Bearer <internal-secret>" \
  -H "Content-Type: application/json" \
  -d '{"type":"game_day","send_to":"your-test-email@example.com"}'
```

Then check:

- AWS SES message/event view shows the message.
- `/admin/email-health` shows a delivery event after SNS posts it back.
- The app logs show no `store failed` or `invalid ses message` errors.

### Deterministic bounce test

Send through the app to the official SES mailbox simulator address:

```text
bounce@simulator.amazonses.com
```

SES will generate a permanent bounce without adding the simulator address to
your account suppression list. The Email Health page should first show a
`send` webhook receipt and then a `bounce` event with its SMTP diagnostic. Use
`success@simulator.amazonses.com` for the equivalent delivery-path test.

Temporary delivery problems appear as `DeliveryDelay` / **Delayed / retrying**
while SES continues retrying. SES publishes a `Transient` (soft) bounce when it
eventually stops retrying, so enable both `DELIVERY_DELAY` and `BOUNCE` on the
configuration-set event destination.

If the webhook diagnostics table remains empty, the request never reached the
app: check that the SNS HTTPS subscription is `Confirmed`, its endpoint includes
the correct token, and the SES event destination is enabled on the exact
configuration set named by `SES_CONFIGURATION_SET`.

## 6. How to read the evidence

- If SES shows no message for a captain address, the app did not send to that address.
- If SES shows `DELIVERY`, SES successfully handed the message to the recipient mail provider.
- If SES shows `BOUNCE` or `COMPLAINT`, check `/admin/email-health`.
- If `/admin/email-health` shows a reminder send failure, the app failed before SES accepted the message.
- If the captain says they did not receive it but SES shows `DELIVERY`, ask them to check spam/quarantine and confirm the exact email address on the captain record.

## 7. Audit a downloaded SES messages CSV

Use the local PowerShell script:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\check-ses-messages.ps1 `
  -CsvPath C:\Users\warre\Downloads\messages.csv
```

Search specific recipients:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\check-ses-messages.ps1 `
  -CsvPath C:\Users\warre\Downloads\messages.csv `
  -Recipient andrew.pearson2593@gmail.com,andrew@kloodle.com
```

Compare against an expected recipient list:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\check-ses-messages.ps1 `
  -CsvPath C:\Users\warre\Downloads\messages.csv `
  -ExpectedRecipientsPath C:\path\to\expected-recipients.csv
```

The script writes these CSVs to `ses-audit-output/`:

- `needs-attention.csv`
- `soft-bounces.csv`
- `hard-bounces.csv`
- `unknown-bounces.csv`
- `complaints.csv`
- `not-delivered.csv`
- `missing-expected-recipients.csv` when an expected list is supplied
- `all-messages-normalized.csv`

## 8. Current Andrew finding from the export

The `messages.csv` export for 23 May 2026 does not show a captain-report send to:

```text
andrew.pearson2593@gmail.com
```

It does show a delivered and opened captain-report email to:

```text
andrew@kloodle.com
```

So this case looks like the captain record/address selected by the app was not the Gmail address, rather than SES failing delivery to Gmail.
