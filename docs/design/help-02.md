# HELP-02: the panel's guide (v1.55.0, wave 1)

Roadmap 15o, decision HELP-2 (2026-11-28). Builds on HELP-01 (Help menu, articles shipped with the release).

## Articles
- One article = `docs/guides/<slug>.md` (English) + `docs/guides/<slug>.id.md` (Indonesian), copied into the panel by
  `panel/sync-docs.sh` and embedded in the binary, so the guide always matches the running release.
- Header between `---` lines (same in both files):
  `id` (fixed code `NP-<MN|AL|TS|FAQ|ADM>-<nnn>`, never reused), `tier` (`user` = everyone signed in, `admin` =
  the server's admins), `open: yes` (readable without signing in; sign-in problems and contact support only),
  `keywords`, `errors` (error codes the article explains).
- Problem articles (TS) use the headings Symptom, Cause, How to check, Fix, Still stuck? (what to send to support).
- Nothing internal and nothing about a client's own work lives here: the repository is public; NHM keeps its own
  articles in its private repository.

## In the panel
- `/help`: search box, articles for users, then (for admins) the articles for admins; the changelog card.
- `/help/<slug>` and `/help/<code>` (redirects to the slug). An admin article is 404 for other users, like the pages
  it describes. Typing a code in the search box opens it.
- Search runs on the server over the articles the user may read: code, keywords, the texts of the errors an article
  explains (both languages), title and text (both languages); lower case, accents removed; every word must match;
  hits in code, keywords, error texts or title come first.
- Error box: `panel/internal/web/helperr.go` matches the shown error to a code, by the translation key the panel used
  or by a fixed phrase of the agent's answer. A matched code links its article ("Why? Read the fix"). Anything else
  links the contact-support article and gets an incident code (6 characters without 0/O/1/I/L) written to the panel's
  log as `incident <code> user=<id> page=<path> code=<code> error=<text>`. Typing mistakes (sign-in, file names,
  DNS) get no incident code.

## Checks
`TestHelpCheck` (in `go test ./internal/web/`): header present, code format and unique, tier known, English and
Indonesian files both present with the same header, error codes known and explained once, every common error has an
article, links point to articles that exist, no step codes in the text, no `sudo` / `systemctl` / `journalctl` in
articles for users. `TestHelpSearchAndTiers` and `TestHelpErrorLinks` cover search, tiers, codes and error links.

## Next waves
The remaining menu, flow, problem and FAQ articles from the table of contents in Project files `panduan-kb/`, and
the NHM articles in the NHM repository (through a generic extension hook in the panel).
