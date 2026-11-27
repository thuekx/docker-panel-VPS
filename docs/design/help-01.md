# HELP-01: changelog and guides in the panel (v1.50.0)

Roadmap 15j, decision HELP-1 (2026-11-27). Status: BUILT 2026-11-27 (v1.50.0).

## What it shows
- A **Help** item in the menu (`/help`) with two parts:
  - **Changelog** (`/help/changelog`): the whole CHANGELOG.md, every release and every feature of the panel, not one
    area. A list of releases on the side jumps to each one; the release this server runs is marked "running" and
    linked from a banner at the top. The footer of every page and the Update page link to it.
  - **Guides** (`/help/<name>`): every file in `docs/guides`, with its title, its first paragraph as a summary and a
    list of its sections. The guides are in English; the Indonesian page says so.
- Pages that have a guide show a **Guide** button at the top: Update, Server backup, Certificates, API keys, Server
  security, Quarantine, System, Terminal, Database and Profile (passkeys).
- Links between documents stay in the panel: a link to a guide opens its Help page, a link to the CHANGELOG opens
  the Changelog page, links to other files of the repository open on GitHub in a new tab.

## Why it comes with the panel
- The panel image is built from `panel/` only, so `panel/sync-docs.sh` copies `docs/guides/*.md` and
  `CHANGELOG.md` into `panel/web/docs`, which is embedded in the binary. `TestDocsCopy` fails when the copy differs
  from the repository's files, so a release can never ship an old copy.
- The pages therefore match the running release exactly, work when GitHub is not reachable, and need no call out.
  The Update page keeps showing the notes of newer releases from GitHub (UPD-01).

## Safety
- `panel/internal/md` renders the small part of Markdown the files use (headings, paragraphs, nested lists, fenced
  code, tables, quotes, code, bold, italic, links). Every piece of text is escaped first and no raw HTML from a file
  passes; link targets with a scheme other than http(s) (`javascript:`, `data:` …) keep only their text.
- The pages need a signed-in account, like every other page. Names are matched against the embedded list only;
  anything else is 404.

## Tests
- `internal/md`: the renderer on a sample with every construct and on every real guide and the CHANGELOG (tags
  balanced, no markup left, nothing unsafe).
- `internal/web`: the copy is current; the pages need a login, render in both languages without missing keys,
  link guides and anchors, mark the running release, 404 for unknown names; the Guide button shows only on pages
  that have one.
