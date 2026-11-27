# Passkey sign-in (AUTH-02)

Status: LIVE v1.37.0 2026-11-27 13:34 WIB (built 2026-11-26). Roadmap slot 14e (RAYX-1 = A).

## What it does
- **Profile → Passkeys:** an admin adds up to 10 passkeys (a phone, a security key, the laptop's fingerprint reader).
  Adding one and removing one both ask for the account password.
- **Sign-in page:** next to the e-mail and password form, **Sign in with a passkey**. The browser offers the
  passkeys it holds for this panel (discoverable credentials), so no e-mail is typed.
- A passkey sign-in is a full sign-in. It replaces the password and the 2FA code, because the passkey always asks
  for the device's own PIN, fingerprint or face (user verification is required). Password + TOTP keep working as
  before, and the recovery codes stay the fallback.

## WebAuthn, standard library only
`panel/internal/auth/webauthn.go` checks both ceremonies without a third-party module:
- A small CBOR reader for the attestation object and the COSE key. It reads only what it needs, and has depth and
  length limits. It is fuzzed.
- Keys: ES256 (P-256), EdDSA (Ed25519) and RS256 (2048 bits or more). Attestation format `none` only: the panel does
  not need to know the device maker.
- Every check: the type in clientDataJSON, the challenge (used once, 2 minutes), the origin (`https://` + the panel's
  host), the RP ID hash (the host without the port), user present + user verified, and the signature over
  `authData || sha256(clientDataJSON)`. The sign counter must grow when the device keeps one; a passkey that goes
  back is refused as a possible clone.

## Storage and sessions
- Table `passkeys`: user, name, credential id, public key (COSE, a public key only), algorithm, sign counter, RP ID,
  created, last used. Nothing secret is stored.
- Challenges live in memory: registration per session, sign-in per `np_pk` cookie (HttpOnly, SameSite=Strict, path
  `/login/passkey`). A restart of the panel drops them; the visitor just presses the button again.
- The sign-in uses the same limits as the password: the per-IP limiter, the failure counter and the account lock.
  Every try lands in the login history (`passkey`, `passkey_failed`) and the audit log names the passkey.
- The RP ID is the host the panel is opened on (e.g. `panel.nostek.dev`). A passkey made on one name does not work on
  another one, so opening the panel by its IP address shows no passkeys.

## Tests
CBOR edge cases and fuzz, each key type with real signatures, wrong origin, wrong challenge, RP hash, missing UV,
counter going back (auth); the store (limit of 10, delete only own); the web flow end to end with a software
authenticator: add, sign in, unknown passkey, reused challenge, wrong password on add and remove, locale keys in id
and en.
