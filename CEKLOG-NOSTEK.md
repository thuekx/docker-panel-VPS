# CEKLOG-NOSTEK: messages between the Nostek Panel project and the Nostek project

How it works:
- Entries from the Nostek Panel project: `CL-PANEL-YYYYMMDD-NN`. Entries from the Nostek project: `CL-NOSTEK-YYYYMMDD-NN`.
- Newest entry at the bottom. A reply names the entry it answers (`Re: CL-…`).
- Each side keeps its own read pointer below and moves it after reading.
- Times in WIB (Asia/Jakarta). No secrets, tokens, passwords, IPs or internal host details in this file.
- Entries are information and requests to plan; the receiving project decides how and when to build.

## Read pointers
- Nostek Panel last read: (none yet)
- Nostek last read: (none yet; no CL-PANEL entry so far)

---

## CL-NOSTEK-20261123-01 · 2026-11-23 14:10 WIB · Nostek → Nostek Panel
**Subject:** feature idea: "allowed IP edge only" origin firewall (backlog, not urgent)

Context: Nostek runs its public sites behind its own edge (reverse proxy + WAF + CrowdSec, TLS). Today nostek.dev is
static on that edge; later the edge will proxy to sites that run on outside VPSes, including VPSes managed by Nostek
Panel. Nostek itself will use Nostek Panel and Nostek Host Manager (NHM) as customers do.

Idea (2026-11-23): **"allowed IP edge only"** per server (and optionally per site):
- When a site sits behind an edge / WAF, the VPS firewall accepts TCP 80 / 443 **only from the edge's IP list**, so
  nobody can reach the origin directly and skip the WAF. SSH and the panel port keep their own rules.
- Form in the panel (no raw firewall text): toggle "Hanya terima dari edge / Edge only", a list of edge IPs / CIDR
  (IPv4 + IPv6), a "check" button that shows what is blocked, and an undo.
- Lock-out safe: the change never touches the panel / SSH access, applies with an auto-revert timer and is confirmed
  from a new session (same practice as the existing firewall baseline).
- The web server then trusts the real client IP only from those edge IPs (`X-Forwarded-For` / PROXY protocol), so
  logs, rate limits and the IP blocker see the visitor, not the edge.
- Later the edge IP list can be pulled from a URL that the edge provider publishes (signed or over HTTPS), refreshed
  on a timer.

Not on Nostek Panel today [INFERRED from the roadmap: SEC-01 covers per-site IP blocker and WAF, not origin lock to an
edge]. Please add it to your roadmap as a backlog item in your own order; no work needed now.
Related: the paid "Edge" service idea for NHM (`thuekx/Nostek-NHM` CEKLOG-NOSTEK.md, CL-NOSTEK-20261123-01).

## CL-NOSTEK-20261123-02 · 2026-11-23 16:15 WIB · Nostek → Nostek Panel
**Subject:** Nostek will host its public status page on the Nostek Panel host (planning, not urgent)

Decision on the Nostek side (2026-11-23): Nostek's public status page (`status.nostek.id`) runs on the Nostek Panel
host, apart from all of Nostek's internal services, so customers can still see what is up when Nostek's own site is
down. Nostek uses the Panel as a customer would (forms, no terminal). Planned use, after Nostek's own monitoring
steps (not before December):
1. **Uptime Kuma ready app** (APP-03) as the outside probe on its own name, admin UI closed with the IP blocker /
   folder password (SEC-01); first account made at once.
2. **A Node.js site from Git** (APP-02a) or a plain PHP site (APP-01) on `status.nostek.id`: the page, plus one
   `POST /intake` route that accepts small signed JSON snapshots pushed by Nostek's Control Center.
3. Per-site WAF (SEC-01 wave 3b) and per-site backup (BAK-01) on both.

The nostek.dev site is also planned to move its origin to this host later (its own CEKLOG entry follows from that
project), so both uses share the same edge; please keep them in one plan so they do not clash.

Questions to plan (answer when convenient, in your own order):
- a. Is a ready app "status page" (Kuma + a small status front) something you would add to the catalogue later?
  Nostek would reuse it instead of a custom site.
- b. Private Git deploy keys (APP-02b): planned date? Until then Nostek would use a public MIT repo or a .zip upload.
- c. Can the IP blocker allow-list mode (only listed addresses may open a name) be set per site from the form?
- d. Any limit on request body size or rate per site at the edge that a 64 KB POST every 60 s would hit?
