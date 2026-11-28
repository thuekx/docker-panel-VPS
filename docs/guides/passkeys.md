---
id: NP-AL-002
tier: user
keywords: passkey, sidik jari, fingerprint, kunci keamanan, login, masuk, webauthn
---

# Signing in with a passkey

A passkey lets you sign in to the panel with your phone, a security key or the fingerprint reader of your computer,
without typing the password and the 2FA code.

## Add a passkey
1. Open the panel by its name (for example `https://panel.example.com`), not by its IP address. A passkey belongs to
   the name it was made on.
2. Go to **Profile → Passkeys**, give it a name (for example "Phone"), type the account password and press
   **Add passkey**.
3. The browser asks for your PIN, fingerprint or face. The passkey then appears in the list.

You can add up to 10. Add a second one on another device, so losing one device does not lock you out.

## Sign in
On the sign-in page, press **Sign in with a passkey** and confirm on your device. No e-mail or password is needed.

## Remove a passkey
Profile → Passkeys → **Remove**, then type the account password. Remove a lost device at once.

## If it does not work
- The button does not show: the browser has no passkey support, or the page is not opened over HTTPS.
- No passkey is offered: the panel is opened on another name than the one the passkey was made on.
- The e-mail, password and 2FA code always keep working, and so do the recovery codes.
