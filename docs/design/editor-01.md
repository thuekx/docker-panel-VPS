# Code editor in the File Manager (EDITOR-1)

Status: [BUILT] v1.24.0, 2026-11-21. Requested for the next release on 2026-11-21 00:16 WIB: "editor file …
modern dan menarik … ramah untuk coding" (most files in a website are program files).

## Choice
| Option | Size (min / gzip) | Phones | Notes |
|---|---|---|---|
| **CodeMirror 6** (chosen) | ~860 KB / ~295 KB with every language below | good (native selection, touch) | MIT, modular, used by Chrome DevTools, Replit, Obsidian |
| Monaco (VS Code) | several MB plus web workers | weak | heavy, needs workers and its own loader |
| Ace | ~500 KB+ | fair | older design |

## What the editor does
- Language from the file name: PHP (also `.blade.php`, `.phtml`, `.inc`), JavaScript/TypeScript (JSX/TSX), HTML
  (also `.vue`, `.twig`), CSS/SCSS/LESS, JSON, SQL, Python, Go (`go.mod`), YAML, Markdown, XML/SVG, shell, Nginx
  `.conf`, Dockerfile, TOML, `.env`, `.htaccess`, `.ini`. Anything else is plain text.
- Line numbers, folding, bracket matching and closing, auto-indent (tabs when the file uses tabs, else 4 spaces),
  Tab indents, several cursors (Alt+click, rectangular selection), find and replace (Ctrl+F), word completion
  (Ctrl+Space), comment toggle (Ctrl+/), history, Ctrl+S saves.
- Toolbar: language, find/replace, line wrap, full screen (the browser's own; Esc leaves it).
- Theme: One Dark when the panel is dark, the default light theme otherwise.
- The textarea stays the form field (kept in step on every change) and the fallback without JavaScript. Line ends:
  the editor works with LF; the panel writes CRLF back for files that had it (existing `eol` field).

## Security
- The bundle is served from `/static/vendor/codemirror.js` (script-src 'self' unchanged). Its checksum is in
  `panel/web/static/vendor/VERSIONS.sha256`, checked by CI.
- CodeMirror adds `<style>` elements. The edit page (and only it, only when the editor loads) sends
  `style-src 'self' 'nonce-…'` with a fresh 144-bit nonce per visit and passes it to the editor
  (`EditorView.cspNonce`); every other page keeps `style-src 'self'`.

## Build
`panel/web/editor`: `entry.js` (the editor setup), `build.mjs` (esbuild, one IIFE `NostekEditor`),
`package-lock.json` (pinned versions). `npm ci && npm run build`, then update the checksum line.
Licenses of every bundled package: `panel/web/static/vendor/LICENSE-codemirror.md` (all MIT).

## Tests
- Panel test: the edit page loads the bundle with a fresh nonce matching its CSP, a binary file and other pages get
  no nonce.
- Checked in Chromium with the real panel handler and CSP: no CSP error, PHP highlighted, typing keeps the form
  field in step and shows "unsaved", find panel, wrap and full screen work, light and dark.
