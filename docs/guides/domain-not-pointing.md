---
id: NP-TS-021
tier: user
keywords: dns, domain, record a, mengarah, point, resolve, preview, propagasi, nameserver
errors: dns
---

# The domain does not point to this server yet

## Symptom
Adding a name answers "does not resolve to a public internet address", the Domains page shows the name as not
pointing here, or the certificate of a name stays waiting.

## Cause
The name's DNS (its A record) does not yet point to this server's public address, or the change has not reached
every DNS server yet. A certificate can only be issued once the name points here.

## How to check
1. On the website's **Domains** page, compare the address the name points to with this server's address shown there.
2. Look the name up with an online DNS checker. A change can take from minutes up to a day.

## Fix
- At your DNS provider, set an **A record** of the name to this server's address, and remove an **AAAA** record that
  points somewhere else.
- Until then, use the website's preview address.
- Behind the Cloudflare proxy, use an own certificate (see [Certificates](certificates.md)).

## Still stuck?
Send support the article code **NP-TS-021**, the name, and where its DNS is kept.
