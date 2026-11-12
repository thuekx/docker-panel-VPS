package web

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

// filesDB answers the file manager and database routes of the fake agent.
func (f *fakeAgent) filesDB(w http.ResponseWriter, r *http.Request, write func(int, any)) {
	p := strings.TrimPrefix(r.URL.Path, "/v1/sites/voltix-id")
	q := r.URL.Query()
	switch {
	case p == "/files" && r.Method == "GET":
		if q.Get("path") == "/nope" {
			write(400, map[string]string{"error": "no such file or folder"})
			return
		}
		write(200, agent.Listing{Path: q.Get("path"), Entries: []agent.Entry{
			{Name: "wp-content", Type: "dir", Mode: "0755", Modified: 1700000000},
			{Name: "index.php", Type: "file", Size: 405, Mode: "0644", Modified: 1700000000},
			{Name: "theme.zip", Type: "file", Size: 3 << 20, Mode: "0644", Modified: 1700000000},
			{Name: "big.sql", Type: "file", Size: 9 << 20, Mode: "0644", Modified: 1700000000},
			{Name: "<b>x</b>.txt", Type: "file", Size: 1, Mode: "0644", Modified: 1700000000}}})
	case p == "/files/content" && r.Method == "GET":
		switch q.Get("path") {
		case "/index.php":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="index.php"`)
			io.WriteString(w, "<?php\r\nrequire __DIR__ . '/wp-blog-header.php';\r\n")
		case "/logo.png":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte{0x89, 'P', 'N', 'G', 0, 0, 1})
		default:
			write(400, map[string]string{"error": "no such file or folder"})
		}
	case p == "/files/content" && r.Method == "PUT":
		b, _ := io.ReadAll(r.Body)
		f.puts = append(f.puts, q.Get("path")+" "+q.Get("overwrite")+" "+string(b))
		write(200, agent.Entry{Name: "x", Type: "file", Size: int64(len(b))})
	case p == "/files/zip":
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="wp-content.zip"`)
		io.WriteString(w, "PK\x03\x04fake")
	case p == "/files/trash":
		write(200, map[string]any{"trash": []agent.FileTrash{{Entry: "1700000000-ab12cd34", Path: "/old-theme", Type: "dir", Deleted: 1700000000, Purge: 1700604800}}})
	case strings.HasPrefix(p, "/files/trash/"):
		f.ops = append(f.ops, "restore "+strings.Split(p, "/")[3])
		write(200, map[string]string{"path": "/old-theme"})
	case strings.HasPrefix(p, "/files/") && r.Method == "POST":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		op := strings.TrimPrefix(p, "/files/")
		f.ops = append(f.ops, strings.TrimSpace(op+" "+body["path"]+" "+body["to"]+" "+body["mode"]))
		if body["mode"] == "4755" {
			write(400, map[string]string{"error": "permissions must be like 644 or 755"})
			return
		}
		write(200, map[string]any{"ok": true, "files": 12})
	case p == "/db":
		write(200, agent.DB{Name: "wordpress", User: "wordpress", Host: "db", Version: "11.8.3-MariaDB", Size: 2 << 20,
			Tables:  []agent.Table{{Name: "wp_options", Engine: "InnoDB", Rows: 412, Data: 1 << 20, Index: 65536}},
			Backups: []agent.Dump{{Name: "before-import-20261112-140000.sql.gz", Size: 40000, Created: 1700000000}}})
	case p == "/db/query":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		f.sqls = append(f.sqls, body["sql"])
		if strings.Contains(body["sql"], "SELEC ") {
			write(400, map[string]string{"error": "ERROR 1064 (42000) at line 2: You have an error in your SQL syntax"})
			return
		}
		v, nul := "<script>alert(1)</script>", (*string)(nil)
		write(200, agent.QueryAnswer{Millis: 7, Results: []agent.Result{{Statement: "SELECT 1", Columns: []string{"option_name", "option_value"}, Rows: [][]*string{{&v, nul}}}}})
	case p == "/db/export":
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="voltix-id-20261112-140000.sql.gz"`)
		w.Write([]byte{0x1f, 0x8b, 1, 2})
	case p == "/db/import":
		b, _ := io.ReadAll(r.Body)
		f.imported = string(b)
		write(200, map[string]string{"backup": "before-import-20261112-140100.sql.gz"})
	case strings.HasPrefix(p, "/db/backups/"):
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="x.sql.gz"`)
		w.Write([]byte{0x1f, 0x8b})
	default:
		write(404, map[string]string{"error": "not found"})
	}
}

// multipartPost sends a multipart form; fields are written in order, a "@name" value is a file part.
func (e *env) multipartPost(c *http.Client, path string, fields [][2]string) (int, http.Header) {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for _, kv := range fields {
		if strings.HasPrefix(kv[0], "@") {
			parts := strings.SplitN(kv[1], "=", 2)
			fw, _ := mw.CreateFormFile(strings.TrimPrefix(kv[0], "@"), parts[0])
			fw.Write([]byte(parts[1]))
			continue
		}
		mw.WriteField(kv[0], kv[1])
	}
	mw.Close()
	req, _ := http.NewRequest("POST", e.ts.URL+path, &b)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header
}

func TestFilesAndDBNeedLogin(t *testing.T) {
	e := newEnv(t)
	e.withAgent()
	for _, p := range []string{"/files/voltix-id", "/files/voltix-id/edit?path=/index.php", "/files/voltix-id/download?path=/index.php", "/files/voltix-id/zip?path=/", "/db/voltix-id", "/db/voltix-id/backups/before-import-20261112-140000.sql.gz"} {
		if code, _, h := e.get(e.c, p); code != 303 || h.Get("Location") != "/login" {
			t.Errorf("%s reachable without login: %d", p, code)
		}
	}
	if code, _ := e.multipartPost(e.c, "/files/voltix-id/upload?path=/", [][2]string{{"@files", "a.txt=hi"}}); code != 303 {
		t.Errorf("upload without login: %d", code)
	}
}

func TestFilesAndDBPagesHaveNoMissingKeys(t *testing.T) {
	e := newEnv(t)
	e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, p := range []string{"/files/voltix-id", "/files/voltix-id/edit?path=/index.php", "/files/voltix-id/edit?path=/logo.png", "/db/voltix-id", "/sites/voltix-id"} {
			code, body, _ := e.get(e.c, p)
			if code != 200 || missingKey.MatchString(body) {
				t.Errorf("%s (%s): %d or missing key %v", p, lang, code, missingKey.FindString(body))
			}
		}
	}
	if got := []string{size(405), size(7500), size(4718592), size(3 << 30)}; strings.Join(got, ",") != "405 B,7.3 KB,4.5 MB,3.0 GB" {
		t.Errorf("sizes: %v", got)
	}
}

func TestFileManagerPages(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	code, body, _ := e.get(e.c, "/files/voltix-id?path=/")
	if code != 200 || !strings.Contains(body, "wp-content") || !strings.Contains(body, "Extract here") || !strings.Contains(body, "/old-theme") {
		t.Fatalf("files page: %d", code)
	}
	if strings.Contains(body, "<b>x</b>") || !strings.Contains(body, "&lt;b&gt;x&lt;/b&gt;.txt") {
		t.Fatal("file names must be escaped")
	}
	// a 9 MB file gets no editor link
	if strings.Contains(body, "/edit?path=%2fbig.sql") || !strings.Contains(body, "/edit?path=%2findex.php") {
		t.Fatal("editor link rules wrong")
	}
	if code, _, _ := e.get(e.c, "/files/bad..id"); code != 404 {
		t.Errorf("bad site id: %d", code)
	}
	if code, _, _ := e.get(e.c, "/files/voltix-id?path=/nope"); code != 200 {
		t.Errorf("missing folder must show the error on the page: %d", code)
	}
	// without the form token nothing changes
	if code, _, _ := e.postWith(e.c, "/files/voltix-id", url.Values{"op": {"delete"}, "path": {"/index.php"}}, ""); code != 403 {
		t.Fatalf("delete without CSRF: %d", code)
	}
	steps := []struct {
		form url.Values
		want string
		loc  string
	}{
		{url.Values{"op": {"mkdir"}, "dir": {"/wp-content"}, "name": {"uploads"}}, "mkdir /wp-content/uploads", "done=mkdir"},
		{url.Values{"op": {"rename"}, "dir": {"/"}, "path": {"/index.php"}, "name": {"old.php"}}, "rename /index.php /old.php", "done=rename"},
		{url.Values{"op": {"rename"}, "dir": {"/"}, "path": {"/a.txt"}, "name": {"/wp-content/a.txt"}}, "rename /a.txt /wp-content/a.txt", "done=rename"},
		{url.Values{"op": {"chmod"}, "dir": {"/"}, "path": {"/old.php"}, "mode": {"4755"}}, "chmod /old.php  4755", "err="},
		{url.Values{"op": {"delete"}, "dir": {"/"}, "path": {"/old.php"}}, "delete /old.php", "done=delete"},
		{url.Values{"op": {"unzip"}, "dir": {"/"}, "path": {"/theme.zip"}}, "unzip /theme.zip", "done=unzip"},
		{url.Values{"op": {"restore"}, "dir": {"/"}, "entry": {"1700000000-ab12cd34"}}, "restore 1700000000-ab12cd34", "done=restore"},
	}
	for _, s := range steps {
		_, page, _ := e.get(e.c, "/files/voltix-id")
		code, _, h := e.postWith(e.c, "/files/voltix-id", s.form, page)
		if code != 303 || !strings.Contains(h.Get("Location"), s.loc) {
			t.Errorf("%v: %d %s", s.form, code, h.Get("Location"))
		}
		if got := f.ops[len(f.ops)-1]; got != s.want {
			t.Errorf("agent got %q, want %q", got, s.want)
		}
	}
	_, page, _ := e.get(e.c, "/files/voltix-id")
	if code, _, h := e.postWith(e.c, "/files/voltix-id", url.Values{"op": {"mkdir"}, "dir": {"/"}, "name": {"../x"}}, page); code != 303 || !strings.Contains(h.Get("Location"), "err=") {
		t.Errorf("name with / accepted: %s", h.Get("Location"))
	}
	// new file -> editor
	code, _, h := e.postWith(e.c, "/files/voltix-id", url.Values{"op": {"newfile"}, "dir": {"/"}, "name": {"hello.html"}}, page)
	if code != 303 || h.Get("Location") != "/files/voltix-id/edit?path=%2Fhello.html" || f.puts[len(f.puts)-1] != "/hello.html  " {
		t.Fatalf("new file: %s %v", h.Get("Location"), f.puts)
	}
	// editor keeps CRLF files CRLF and turns the browser's CRLF back into LF otherwise
	code, ed, _ := e.get(e.c, "/files/voltix-id/edit?path=/index.php")
	if code != 200 || !strings.Contains(ed, "wp-blog-header.php") || !strings.Contains(ed, `name="eol" value="crlf"`) {
		t.Fatalf("editor: %d", code)
	}
	e.postWith(e.c, "/files/voltix-id", url.Values{"op": {"save"}, "path": {"/index.php"}, "eol": {"crlf"}, "content": {"a\r\nb"}}, ed)
	e.postWith(e.c, "/files/voltix-id", url.Values{"op": {"save"}, "path": {"/style.css"}, "eol": {"lf"}, "content": {"a\r\nb"}}, ed)
	if got := f.puts[len(f.puts)-2:]; got[0] != "/index.php 1 a\r\nb" || got[1] != "/style.css 1 a\nb" {
		t.Fatalf("saves: %q", got)
	}
	if _, ed, _ := e.get(e.c, "/files/voltix-id/edit?path=/logo.png"); !strings.Contains(ed, "not a text file") || strings.Contains(ed, "<textarea") {
		t.Fatal("binary file opened in the editor")
	}
	// downloads stream through with an attachment header
	resp, _ := e.c.Get(e.ts.URL + "/files/voltix-id/download?path=/index.php")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") || resp.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(string(b), "<?php") {
		t.Fatalf("download: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = e.c.Get(e.ts.URL + "/files/voltix-id/zip?path=/wp-content")
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("zip: %d %v", resp.StatusCode, resp.Header)
	}
	// upload: the token must be the first part
	tok := csrfRe.FindStringSubmatch(page)[1]
	if code, _ := e.multipartPost(e.c, "/files/voltix-id/upload?path=/wp-content", [][2]string{{"@files", "a.txt=hi"}, {"csrf", tok}}); code != 403 {
		t.Errorf("upload with the token after the file: %d", code)
	}
	if code, _ := e.multipartPost(e.c, "/files/voltix-id/upload?path=/wp-content", [][2]string{{"csrf", "wrong"}, {"@files", "a.txt=hi"}}); code != 403 {
		t.Errorf("upload with a wrong token: %d", code)
	}
	n := len(f.puts)
	code, h = e.multipartPost(e.c, "/files/voltix-id/upload?path=/wp-content", [][2]string{{"csrf", tok}, {"overwrite", "1"}, {"@files", `..\..\evil.php=<?php`}, {"@files", "b.txt=second"}})
	if code != 303 || !strings.Contains(h.Get("Location"), "done=upload") {
		t.Fatalf("upload: %d %s", code, h.Get("Location"))
	}
	if got := f.puts[n:]; len(got) != 2 || got[0] != "/wp-content/evil.php 1 <?php" || got[1] != "/wp-content/b.txt 1 second" {
		t.Fatalf("uploaded: %q", got)
	}
	_, audit, _ := e.get(e.c, "/audit")
	for _, want := range []string{"files.upload", "files.delete", "files.save", "files.rename"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit lacks %s", want)
		}
	}
	if strings.Contains(audit, "second") {
		t.Error("audit holds file content")
	}
}

func TestDatabasePage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	code, page, _ := e.get(e.c, "/db/voltix-id")
	if code != 200 || !strings.Contains(page, "wp_options") || !strings.Contains(page, "11.8.3-MariaDB") || !strings.Contains(page, "before-import-20261112-140000.sql.gz") {
		t.Fatalf("db page: %d", code)
	}
	code, body, _ := e.postWith(e.c, "/db/voltix-id", url.Values{"op": {"query"}, "sql": {"SELECT option_name, option_value FROM wp_options"}}, page)
	if code != 200 || !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") || strings.Contains(body, "<script>alert(1)") || !strings.Contains(body, `class="null"`) {
		t.Fatalf("query result: %d", code)
	}
	code, body, _ = e.postWith(e.c, "/db/voltix-id", url.Values{"op": {"query"}, "sql": {"SELEC x"}}, page)
	if code != 422 || !strings.Contains(body, "ERROR 1064") || !strings.Contains(body, "SELEC x") {
		t.Fatalf("query error: %d", code)
	}
	resp, err := e.c.PostForm(e.ts.URL+"/db/voltix-id", url.Values{"op": {"export"}, "csrf": {csrfRe.FindStringSubmatch(page)[1]}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/gzip" || !bytes.HasPrefix(b, []byte{0x1f, 0x8b}) {
		t.Fatalf("export: %d %v", resp.StatusCode, resp.Header)
	}
	if code, _, _ := e.get(e.c, "/db/voltix-id/backups/..%2f..%2fsite.json"); code != 404 {
		t.Errorf("backup name trick: %d", code)
	}
	tok := csrfRe.FindStringSubmatch(page)[1]
	if code, h := e.multipartPost(e.c, "/db/voltix-id/import", [][2]string{{"csrf", tok}, {"@file", "dump.sql=DROP TABLE x;"}}); code != 303 || !strings.Contains(h.Get("Location"), "err=") || f.imported != "" {
		t.Fatalf("import without confirmation: %d %s", code, h.Get("Location"))
	}
	if code, h := e.multipartPost(e.c, "/db/voltix-id/import", [][2]string{{"csrf", tok}, {"confirm", "1"}, {"@file", "dump.exe=MZ"}}); code != 303 || !strings.Contains(h.Get("Location"), "err=") || f.imported != "" {
		t.Fatalf("import of a wrong type: %d %s", code, h.Get("Location"))
	}
	if code, h := e.multipartPost(e.c, "/db/voltix-id/import", [][2]string{{"csrf", tok}, {"confirm", "1"}, {"@file", "dump.sql=CREATE TABLE t (a int);"}}); code != 303 || !strings.Contains(h.Get("Location"), "done=import") || f.imported != "CREATE TABLE t (a int);" {
		t.Fatalf("import: %d %s %q", code, h.Get("Location"), f.imported)
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "db.import") || !strings.Contains(audit, "db.query") || !strings.Contains(audit, "db.export") || strings.Contains(audit, "option_value FROM") {
		t.Fatal("audit must record database actions without the SQL text")
	}
	// tabs on the site page
	if _, site, _ := e.get(e.c, "/sites/voltix-id"); !strings.Contains(site, `href="/files/voltix-id"`) || !strings.Contains(site, `href="/db/voltix-id"`) {
		t.Fatal("site page lacks the File Manager and Database tabs")
	}
}
