# Panel API with API keys (API-01)

Status: LIVE v1.33.0 2026-11-27 13:34 WIB (built 2026-11-26). Roadmap slot 14a (RAYX-1 = A). User guide: [docs/guides/api.md](../guides/api.md).

## Goal
Scripts and CI can read the panel and do the few things a pipeline needs (restart, Git deploy, publish a static
release) without a person's password or session, with the least rights possible and a clear record.

## Keys
- Table `api_keys` in the panel database: name, prefix (`npk_` + 6 characters), sha256 of the key, rights,
  website, allowed addresses, created, expires, last use and its address, revoked. The key itself is never stored,
  logged or shown again; it is 32 random bytes (base64url), so a plain sha256 is enough (no password hash needed).
- Rights: `read` always; `operate` (start/stop/restart); `deploy` (Git deploy, static release). A key can be bound to
  one website and to at most 10 addresses or networks (`0.0.0.0/0` refused). Lifetime 30/90/365 days or none.
- Made on `/settings/api` with the user's password (and the sign-in rate limit), at most 20 active per
  organisation. After the form, the key waits in memory (2 minutes) for that session's next page view and is shown once; a reload shows it no more and makes no second key. Revoke is
  immediate. The audit log records create and revoke with name, prefix, rights, website, addresses and expiry.

## The API
- Routes under `/api/v1`, outside the cookie session: no CSRF and no CORS headers, so a web page in a browser cannot
  use them; the session cookie does not sign in to the API.
- A call is checked in this order: calls without a valid key per address (10 a minute, then 429 for that address),
  `Authorization: Bearer npk_…` of the right length, the key by its hash, not revoked or expired (401), the
  address list (403), 120 calls a minute per key (429), the right (403), the key's user still in its organisation.
- A key bound to one website gets 404 for others and lists only its own. Jobs are shown only to the user that
  started them and, for a bound key, only for its website.
- `PUT …/release` streams the body to the agent like the browser upload (no buffering), 512 MB limit.
- Every change is audited as the key's user with "(API key NAME)" and the caller's address.

## Not in this step
Creating or deleting websites, files, databases, users, domains and security settings over the API; webhooks out.
They can follow when there is a need, each with its own right.

## Tests
`panel/internal/web/api_test.go`: page in both languages, password needed, bad names/websites/addresses/expiry
refused, the key shown once and stored only as a hash, audit without the key, revoke, CSRF; calls without/with a
made-up key, the cookie refused, read calls, rights, address list, a key bound to another website (404 and an empty
list), deploy only for Git-built runtime websites, jobs of other users hidden, static release streamed with the file
name cleaned, audit with the key's name, last use, expiry; the limit without a valid key and the per-key limit.
