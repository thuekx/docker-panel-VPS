package sites

// Starter pages of the apps (APP-01): one file that shows the website works and says how to put the real site
// in place. The owner replaces it with the File Manager (upload a .zip and extract it) or overwrites it.

import (
	"html"
	"os"
	"path/filepath"
	"strings"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

const starterHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{TITLE}}</title>
<style>body{margin:0;min-height:100vh;display:grid;place-items:center;font:16px/1.6 system-ui,sans-serif;background:#0b1220;color:#e6edf6}
main{max-width:560px;padding:32px}h1{margin:0 0 8px;font-size:28px}p{color:#9fb0c8}code{background:#16213a;padding:2px 6px;border-radius:6px}
.tag{display:inline-block;margin-bottom:16px;padding:2px 10px;border-radius:99px;background:#0d9488;color:#fff;font-size:13px}</style></head>
<body><main><span class="tag">Nostek Panel</span><h1>{{TITLE}}</h1>
<p>{{LEAD}}</p>
<p>{{HOW}}</p>
{{EXTRA}}</main></body></html>
`

// phpExtra checks the database connection without showing any setting; the password is read from its file.
const phpExtra = `<?php
$v = PHP_VERSION;
echo "<p>PHP <code>" . htmlspecialchars($v) . "</code></p>";
if (getenv('DB_HOST')) {
    $pw = @file_get_contents(getenv('DB_PASSWORD_FILE') ?: '/run/secrets/db_password');
    mysqli_report(MYSQLI_REPORT_OFF);
    $db = @mysqli_connect(getenv('DB_HOST'), getenv('DB_USER'), trim((string) $pw), getenv('DB_NAME'));
    echo $db ? "<p>Database: connected</p>" : "<p>Database: not reachable yet</p>";
    if ($db) { mysqli_close($db); }
}
?>`

// writeStarter puts the starter page into the new website folder, owned by www-data.
func (m *Manager) writeStarter(s Site) error {
	title := s.Title
	if title == "" {
		title = s.Domain
	}
	name, extra := "index.html", ""
	lead := "This static website is ready."
	how := "Upload your site (for example the <code>dist</code> folder of a React, Vue or Astro build) with the File Manager in Nostek Panel: upload a .zip and extract it here, replacing this page."
	if s.AppName() == spec.AppPHP {
		name, extra = "index.php", phpExtra
		lead = "This PHP website is ready."
		how = "Upload your code with the File Manager in Nostek Panel: upload a .zip and extract it here, replacing this file."
		if s.HasDB() {
			how += " The database address, name and user are in the environment values <code>DB_HOST</code>, <code>DB_NAME</code>, <code>DB_USER</code>; the password is in the file named by <code>DB_PASSWORD_FILE</code>."
		}
	}
	body := strings.NewReplacer("{{TITLE}}", html.EscapeString(title), "{{LEAD}}", lead, "{{HOW}}", how, "{{EXTRA}}", extra).Replace(starterHTML)
	r, err := os.OpenRoot(m.wpDir(s.ID))
	if err != nil {
		return err
	}
	defer r.Close()
	f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := r.Chown(name, m.uid(), m.uid()); err != nil {
		return err
	}
	if err := r.Chown(".", m.uid(), m.uid()); err != nil {
		return err
	}
	return r.Chmod(name, 0o644)
}

// Starter Node.js app (APP-02, source "files"): no dependencies, so the first deploy needs no registry.
const nodeStarterPackage = `{
  "name": "nostek-starter",
  "version": "1.0.0",
  "private": true,
  "scripts": { "start": "node server.js" }
}
`

const nodeStarterServer = `// Starter app made by Nostek Panel. Replace this folder with your own app (upload a .zip in the File Manager
// and extract it here), then press Deploy. The app must listen on the port in the PORT environment value.
const http = require('http');
const page = ` + "`" + `{{PAGE}}` + "`" + `;
http.createServer((req, res) => {
  res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
  res.end(page.replace('{{NODE}}', process.version));
}).listen(Number(process.env.PORT) || 3000, process.env.HOST || '0.0.0.0');
`

// writeNodeStarter fills the source folder of a new Node.js website built from its own files.
func (m *Manager) writeNodeStarter(s Site) error {
	title := s.Title
	if title == "" {
		title = s.Domain
	}
	page := strings.NewReplacer("{{TITLE}}", html.EscapeString(title), "{{LEAD}}", "This Node.js app is ready (Node.js <code>{{NODE}}</code>).",
		"{{HOW}}", "Put your app in the <code>source</code> folder with the File Manager in Nostek Panel (upload a .zip and extract it), then press Deploy.",
		"{{EXTRA}}", "").Replace(starterHTML)
	page = strings.NewReplacer("`", "&#96;", "\\", "&#92;", "${", "&#36;{").Replace(page)
	src := filepath.Join(m.wpDir(s.ID), "source")
	if err := os.MkdirAll(src, 0o755); err != nil {
		return err
	}
	for name, body := range map[string]string{"package.json": nodeStarterPackage, "server.js": strings.Replace(nodeStarterServer, "{{PAGE}}", page, 1)} {
		if err := writeFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			return err
		}
		if err := os.Chown(filepath.Join(src, name), m.uid(), m.uid()); err != nil {
			return err
		}
	}
	return os.Chown(src, m.uid(), m.uid())
}
