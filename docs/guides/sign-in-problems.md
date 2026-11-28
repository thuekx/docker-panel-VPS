---
id: NP-TS-001
tier: user
open: yes
keywords: login, masuk, sign in, password, kata sandi, 2fa, kode, authenticator, terkunci, locked, tautan, link, kode pemulihan, recovery
errors: login, linkgone
---

# Cannot sign in

## Symptom
The sign-in page says "E-mail or password is wrong", "The code is not valid", "Too many attempts", or a setup link
says "Link not valid".

## Cause
- The e-mail or password has a typo, or Caps Lock is on.
- The 2FA code is old, or the phone's clock is not right (codes change every 30 seconds).
- After many wrong tries the panel waits a minute before it accepts the next one.
- A setup link or a one-time password link works once and only for a short time.

## How to check
1. Type the password in a text editor first to see it, then paste it.
2. On the phone, turn on automatic date and time, then use the newest code.

## Fix
- Wrong password: an admin of this server can make a new setup link for you.
- Phone lost: on the 2FA page use one of your **recovery codes** instead of the code. Each works once.
- Too many attempts: wait one minute, then try once, carefully.
- Link not valid: ask the person who sent it for a new link.
- Passkey: see [Signing in with a passkey](passkeys.md).

## Still stuck?
Send support the article code **NP-TS-001**, your e-mail address on the panel, and the time you tried (with the time
zone, for example 19:40 WIB). Never send a password or a code.
