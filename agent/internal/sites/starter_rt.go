package sites

// Starter apps of Go and Python websites built from their own files (APP-02b): no dependencies, so the first
// deploy needs no registry. The owner replaces them with the File Manager and presses Deploy.

import (
	"html"
	"os"
	"path/filepath"
	"strings"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

const goStarterMod = "module starter\n\ngo 1.26\n"

const goStarterMain = `// Starter app made by Nostek Panel. Replace this folder with your own app (upload a .zip in the File Manager and
// extract it here), then press Deploy. The app must listen on the port in the PORT environment value.
package main

import (
	"net/http"
	"os"
	"runtime"
	"strings"
)

const page = ` + "`" + `{{PAGE}}` + "`" + `

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.Replace(page, "{{RUNTIME}}", runtime.Version(), 1)))
	})
	_ = http.ListenAndServe("0.0.0.0:"+port, nil)
}
`

const pyStarterApp = `# Starter app made by Nostek Panel. Replace this folder with your own app (upload a .zip in the File Manager and
# extract it here), then press Deploy. The app must listen on the port in the PORT environment value.
import os
import platform
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PAGE = """{{PAGE}}"""


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = PAGE.replace("{{RUNTIME}}", "Python " + platform.python_version()).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


ThreadingHTTPServer(("0.0.0.0", int(os.environ.get("PORT", "3000"))), Handler).serve_forever()
`

// writeAppStarter fills the source folder of a new app built from its own files with a starter of its runtime.
func (m *Manager) writeAppStarter(s Site) error {
	if s.AppName() == spec.AppNode {
		return m.writeNodeStarter(s)
	}
	title := s.Title
	if title == "" {
		title = s.Domain
	}
	name := map[string]string{spec.AppGo: "Go", spec.AppPython: "Python"}[s.AppName()]
	page := strings.NewReplacer("{{TITLE}}", html.EscapeString(title), "{{LEAD}}", "This "+name+" app is ready (<code>{{RUNTIME}}</code>).",
		"{{HOW}}", "Put your app in the <code>source</code> folder with the File Manager in Nostek Panel (upload a .zip and extract it), then press Deploy.",
		"{{EXTRA}}", "").Replace(starterHTML)
	files := map[string]string{}
	if s.AppName() == spec.AppGo {
		files["go.mod"] = goStarterMod
		files["main.go"] = strings.Replace(goStarterMain, "{{PAGE}}", strings.ReplaceAll(page, "`", "&#96;"), 1)
	} else {
		files["requirements.txt"] = ""
		files["app.py"] = strings.Replace(pyStarterApp, "{{PAGE}}", strings.NewReplacer(`"""`, "&quot;&quot;&quot;", `\`, "&#92;").Replace(page), 1)
	}
	return m.writeSource(s, files)
}

// writeSource writes files into the source folder, owned by www-data.
func (m *Manager) writeSource(s Site, files map[string]string) error {
	src := filepath.Join(m.wpDir(s.ID), "source")
	if err := os.MkdirAll(src, 0o755); err != nil {
		return err
	}
	for name, body := range files {
		if err := writeFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			return err
		}
		if err := os.Chown(filepath.Join(src, name), m.uid(), m.uid()); err != nil {
			return err
		}
	}
	return os.Chown(src, m.uid(), m.uid())
}
