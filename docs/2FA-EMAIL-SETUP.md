# 2FA Email Setup - AWS SES Runbook

This document replaces the earlier Postfix/OpenDKIM/Cloudflare-forwarding approach.

The recommended production path is:

- send email directly from the app via **Amazon SES SMTP**
- verify `gmcl.co.uk` in SES
- enable SES DKIM signing
- point the app at the SES SMTP endpoint

This is simpler, cheaper, and easier to operate than running your own mail server.

---

## Why This Matters

Admin login uses 2FA by email. After entering the correct password, the app sends a one-time code to the admin email address.

Production currently uses `DISABLE_2FA=1` as a temporary bypass while email delivery is being finalized. Remove that bypass only after SES delivery is confirmed working.

---

## Recommended Architecture

- **Outbound email**: Amazon SES
- **Transport from app**: SMTP
- **DNS**: Cloudflare can remain your DNS provider
- **No dependency on Cloudflare Email Routing** for outbound delivery

You may still forward the mailbox wherever you want, but outbound sending should come directly from SES.

---

## What The App Needs

The app already supports SMTP configuration through environment variables:

- `SMTP_HOST`
- `SMTP_PORT`
- `SMTP_USERNAME`
- `SMTP_PASSWORD`
- `SMTP_FROM`

The current mailer code uses plain SMTP send flow, so SES SMTP is the lowest-change option.

Relevant app file:

- [internal/email/email.go](/e:/GMCL/internal/email/email.go)

---

## AWS SES Setup

Use the SES console in your preferred AWS region. Pick one region and keep all SES settings in that same region.

Official AWS references:

- SES verified identities: https://docs.aws.amazon.com/ses/latest/dg/verify-addresses-and-domains.html
- SES Easy DKIM: https://docs.aws.amazon.com/ses/latest/dg/send-email-authentication-dkim-easy.html
- SES SMTP credentials: https://docs.aws.amazon.com/ses/latest/dg/smtp-credentials.html
- SES sandbox / production access: https://docs.aws.amazon.com/ses/latest/dg/request-production-access.html
- SES SMTP endpoints: https://docs.aws.amazon.com/general/latest/gr/ses.html

### 1. Choose an SES region

Use a region close to the app, for example:

- `eu-west-2` (London)
- `eu-west-1` (Ireland)

Keep a note of the SMTP hostname for that region.

Typical SES SMTP hostnames look like:

- `email-smtp.eu-west-2.amazonaws.com`
- `email-smtp.eu-west-1.amazonaws.com`

### 2. Verify the domain in SES

In SES:

1. Open **Verified identities**
2. Create identity
3. Choose **Domain**
4. Enter `gmcl.co.uk`
5. Enable **Easy DKIM**

SES will give you DNS records to add in Cloudflare. Usually this includes:

- one TXT record for domain verification
- three CNAME records for DKIM

Add them exactly as provided.

### 3. Wait for SES verification

In SES, wait until:

- domain status = verified
- DKIM status = successful

Do not move on until both are green.

### 4. Request production access if needed

New SES accounts may start in the **sandbox**.

If still in sandbox:

- you can only send to verified recipients
- that is not suitable for production 2FA or reminders

Request production access in the same SES region before go-live.

### 5. Create SMTP credentials

Create SES SMTP credentials in IAM using the SES flow.

You will get:

- SMTP username
- SMTP password

Store them securely. The SMTP password is not the same as your AWS console password.

### 6. Decide the sender address

Use a sender address under the verified domain, for example:

- `webmaster@gmcl.co.uk`
- `noreply@gmcl.co.uk`

Set that address as `SMTP_FROM`.

---

## Cloudflare DNS Changes

Cloudflare is still fine for DNS. You only need it to publish the SES verification and DKIM records.

Add the SES records exactly as AWS gives them:

- verification TXT
- DKIM CNAMEs

Do not proxy them.

After DNS propagates, SES should show the identity as verified.

---

## App Configuration

Set these values in `/opt/gmcl/.env` on the server:

```env
SMTP_HOST=email-smtp.eu-west-2.amazonaws.com
SMTP_PORT=587
SMTP_USERNAME=YOUR_SES_SMTP_USERNAME
SMTP_PASSWORD=YOUR_SES_SMTP_PASSWORD
SMTP_FROM=webmaster@gmcl.co.uk
```

Notes:

- Port `587` is the normal choice for SMTP with STARTTLS.
- If port `587` is blocked in your environment, SES also documents other SMTP ports.
- The app currently sends to the configured SMTP host directly.

After updating `.env`, restart the app:

```bash
cd /opt/gmcl
docker compose restart app
```

---

## App Status

The SMTP client in [internal/email/email.go](/e:/GMCL/internal/email/email.go) is expected to support:

- SMTP AUTH via `SMTP_USERNAME` and `SMTP_PASSWORD`
- STARTTLS when the server advertises it
- a display-name `SMTP_FROM` header with the correct envelope sender address

That matches the SES SMTP path described in this document.

---

## Recommended Rollout Order

1. Verify `gmcl.co.uk` in SES
2. Publish SES DKIM records in Cloudflare
3. Confirm SES identity status is verified
4. Create SES SMTP credentials
5. Set SES SMTP env vars in production
6. Restart app
7. Test 2FA email delivery
8. Remove `DISABLE_2FA=1`

---

## Production Test Checklist

### 1. Basic SMTP test from the app

Attempt admin login and confirm the app no longer logs the dev fallback.

Expected result:

- app attempts real SMTP delivery
- recipient gets the 2FA code

### 2. SES console checks

In AWS SES, verify:

- send accepted
- no auth failures
- no suppression/bounce issue

### 3. Recipient header checks

In the received email headers, look for:

- `dkim=pass`
- `spf=pass` or aligned mail path as expected

### 4. App behaviour check

Once email works:

```bash
cd /opt/gmcl
sed -i '/DISABLE_2FA/d' .env
docker compose restart app
```

Then test admin login again at `/admin/login`.

---

## Troubleshooting

### SES identity never verifies

- DNS records in Cloudflare are wrong
- records were proxied instead of DNS-only
- wrong AWS region was checked

### SMTP auth fails

- wrong SES SMTP credentials
- app is not yet using SMTP AUTH
- region hostname does not match the SES region where credentials/identity were created

### TLS / connection failures

- outbound SMTP port blocked by firewall/provider
- app mailer does not yet negotiate STARTTLS

### Email accepted but not arriving

- recipient spam folder
- SES sandbox still enabled
- SES suppression or bounce state

---

## What To Ignore From The Old Setup

These are no longer the preferred path:

- local Postfix relay
- OpenDKIM on the droplet
- Cloudflare Email Routing as the outbound delivery path

They can be removed later once SES is live and stable.

---

## Next Repo Task

Once SES is configured, the next step is operational verification:

- confirm SES identity verification is green
- set the production `SMTP_*` values
- test admin 2FA delivery end to end
