# Panel API (API-01)

Scripts and CI pipelines use the panel through a small JSON API at `https://PANEL/api/v1`, signed in with an API
key. The API never accepts the panel's sign-in cookie, and the panel never shows a key a second time.

## Keys
Settings → **API keys** (`/settings/api`):
- **Name**: what the key is for (shown in the audit log).
- **Rights**: every key can read; **Operate** adds start, stop and restart; **Deploy** adds a Git deploy and
  publishing a static release.
- **Website**: every website, or only one. A key for one website gets 404 for every other one, as if it did not
  exist, and lists only its own.
- **Only from these addresses**: up to 10 addresses or networks (for example your CI runners); empty = anywhere.
- **Expires after** 30, 90 (default) or 365 days, or never.
- Your password confirms the new key. The key (`npk_…`, 47 characters) is shown once; the panel keeps only its
  sha256, the first characters (to tell keys apart), the last use and its address. At most 20 active keys.
- **Revoke** stops a key at once.

## Calls
Send `Authorization: Bearer npk_…`. Answers are JSON; errors are `{"error": "…"}`.

| Call | Right | What |
|---|---|---|
| `GET /api/v1/server` | read | Version, host name, memory, disk, load, containers, last backup, the key's own rights |
| `GET /api/v1/sites` | read | Websites: id, domain, names, URL, app, engine, status, containers up/total |
| `GET /api/v1/sites/{id}` | read | One website; a static website also lists its releases |
| `POST /api/v1/sites/{id}/start` · `/stop` · `/restart` | operate | Start, stop or restart the website |
| `POST /api/v1/sites/{id}/deploy` | deploy | Deploy a Node.js, Go or Python website from its Git branch; answers `202 {"job": …}` |
| `PUT /api/v1/sites/{id}/release?name=dist.zip` | deploy | Publish the request body (a .zip, up to 512 MB) as the new release of a static website; answers `201 {"release": …}` |
| `GET /api/v1/jobs/{job}` | read | State of a job this key's user started (`running`, `done`, `failed`) |

Status codes: 401 no or invalid key (wrong, revoked, expired), 403 a right or address the key does not have, 404
no such website or job (or one the key may not see), 429 too many calls (120 per minute per key; 10 calls per
minute without a valid key per address).

## Examples
```bash
KEY=npk_…   # from your CI secret store, never in the script itself
curl -fsS -H "Authorization: Bearer $KEY" https://panel.example.com/api/v1/sites

# publish a built static site (React, Vue, Astro …)
(cd dist && zip -qr ../dist.zip .)
curl -fsS -X PUT -H "Authorization: Bearer $KEY" --data-binary @dist.zip \
  "https://panel.example.com/api/v1/sites/my-site/release?name=dist.zip"

# deploy a Node.js website from Git and wait for the job
JOB=$(curl -fsS -X POST -H "Authorization: Bearer $KEY" https://panel.example.com/api/v1/sites/my-app/deploy | jq -r .job)
curl -fsS -H "Authorization: Bearer $KEY" https://panel.example.com/api/v1/jobs/$JOB
```

Every call that changes something is in the audit log with the key's name, never the key.
