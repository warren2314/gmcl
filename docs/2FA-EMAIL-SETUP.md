# 2FA Email Setup — Status & Resumption Guide

## Why This Matters

Admin login uses two-factor authentication (2FA). After entering the correct
password, the system emails a one-time 6-digit code to the admin's email address.
The admin must enter this code to complete login. This was a client requirement.

Currently `DISABLE_2FA=1` is set in `.env` on the production droplet as a
temporary bypass so the admin dashboard is accessible while email is being
configured. **This must be removed once email is working.**

---

## Current State (as of 2026-04-07)

### What works
- Postfix is installed and running on the droplet
- The Docker app container can reach Postfix via `host.docker.internal:25`
- UFW allows Docker network (`172.16.0.0/12`) to connect to port 25
- The Go SMTP client correctly sends `EHLO gmcl.co.uk`
- SPF record is set: `v=spf1 ip4:104.248.172.185 include:_spf.mx.cloudflare.net ~all`

### What is blocking
Cloudflare Email Routing rejects mail with:
> "Cannot forward emails that are not authenticated"

Cloudflare requires **DKIM** (or ARC) authentication on top of SPF. SPF alone
is not enough for Cloudflare Email Routing to forward the message.

---

## The Fix — OpenDKIM

We need to install and configure **OpenDKIM** so Postfix signs outgoing mail
with a DKIM signature. Once the DNS TXT record is added, Cloudflare will see
`dkim=pass` and forward the email successfully.

### Steps completed
- [x] `sudo apt-get install -y opendkim opendkim-tools`
- [x] Key pair generated at `/etc/opendkim/keys/gmcl.co.uk/mail.private`
- [x] `/etc/opendkim.conf` written with Unix socket config
- [x] `/etc/opendkim/KeyTable`, `SigningTable`, `TrustedHosts` written
- [x] Postfix configured to use milter: `unix:/run/opendkim/opendkim.sock`

### Steps remaining
- [ ] Get OpenDKIM service running (currently failing — see below)
- [ ] Get DNS TXT record from `/etc/opendkim/keys/gmcl.co.uk/mail.txt`
- [ ] Add DKIM TXT record to Cloudflare DNS (`mail._domainkey.gmcl.co.uk`)
- [ ] Test: `echo "test" | mail -s "DKIM test" webmaster@gmcl.co.uk`
- [ ] Verify in Cloudflare Email Routing activity log that DKIM shows `pass`
- [ ] Remove `DISABLE_2FA=1` from `/opt/gmcl/.env`
- [ ] Restart app: `docker compose restart app`

---

## Resuming OpenDKIM Setup

### Current failure
OpenDKIM starts but exits immediately. Last attempt:

```
Process: ExecStart=/usr/sbin/opendkim -f (code=exited, status=0/SUCCESS)
```

The systemd override at `/etc/systemd/system/opendkim.service.d/override.conf`
was interfering. To continue:

```bash
# Remove bad override if still present
sudo rm -f /etc/systemd/system/opendkim.service.d/override.conf
sudo systemctl daemon-reload
sudo systemctl restart opendkim
sudo systemctl status opendkim --no-pager
```

If it still fails, run manually to see the real error:
```bash
sudo -u opendkim opendkim -f -x /etc/opendkim.conf
```

### Config files on the server

**`/etc/opendkim.conf`**
```
Mode                  sv
SignatureAlgorithm    rsa-sha256
Canonicalization      relaxed/relaxed
KeyTable              /etc/opendkim/KeyTable
SigningTable          /etc/opendkim/SigningTable
ExternalIgnoreList    /etc/opendkim/TrustedHosts
InternalHosts         /etc/opendkim/TrustedHosts
Socket                local:/run/opendkim/opendkim.sock
UMask                 002
UserID                opendkim
```

**`/etc/opendkim/KeyTable`**
```
mail._domainkey.gmcl.co.uk gmcl.co.uk:mail:/etc/opendkim/keys/gmcl.co.uk/mail.private
```

**`/etc/opendkim/SigningTable`**
```
*@gmcl.co.uk mail._domainkey.gmcl.co.uk
```

**`/etc/opendkim/TrustedHosts`**
```
127.0.0.1
::1
localhost
172.16.0.0/12
```

### Postfix milter config (already set)
```bash
smtpd_milters = unix:/run/opendkim/opendkim.sock
non_smtpd_milters = unix:/run/opendkim/opendkim.sock
milter_default_action = accept
milter_protocol = 6
```

---

## Once DKIM is Working — Cloudflare DNS Record

```bash
sudo cat /etc/opendkim/keys/gmcl.co.uk/mail.txt
```

Add the output as a TXT record in Cloudflare DNS:
- **Type**: TXT
- **Name**: `mail._domainkey`
- **Content**: the `p=...` value from the file (everything inside the quotes, joined together)

---

## Removing the Bypass

Once email is confirmed working:

```bash
sed -i '/DISABLE_2FA/d' /opt/gmcl/.env
docker compose restart app
```

Then test login at `gmcl.co.uk/admin/login` — the 2FA code should arrive at
`webmaster@gmcl.co.uk` (forwarded by Cloudflare to your personal inbox).
