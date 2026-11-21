# Code editor bundle (EDITOR-1)

`../static/vendor/codemirror.js` is CodeMirror 6 (MIT) with the languages of website files, built from `entry.js`
into one file the panel serves itself (no CDN; the panel's CSP allows only its own scripts). The page
`fileedit.html` loads it; without JavaScript the plain textarea still works.

Rebuild (versions pinned in `package-lock.json`), then update the checksum:
```bash
cd panel/web/editor && npm ci && npm run build
cd ../static/vendor && sha256sum codemirror.js   # replace its line in VERSIONS.sha256
```
