package sites

// Database admin (SITE-02): tables, a SQL console, export and import. Every call runs the MariaDB client inside the
// site's own db container as the site's database user. The password is read inside the container from its secret
// file into MYSQL_PWD, so it never appears in an argument list, a log or an answer.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxSQL is the longest statement text the console accepts.
	MaxSQL = 64 << 10
	// MaxImport is the largest .sql or .sql.gz file accepted for import.
	MaxImport = 512 << 20
	// QueryRows is the most rows one SELECT returns in the console.
	QueryRows = 1000
	maxCell   = 4000
	keepDumps = 3
)

// Streamer is a Runner that can send stdout to a writer (export). ExecRunner implements it.
type Streamer interface {
	DockerTo(ctx context.Context, dir string, stdin io.Reader, stdout io.Writer, args ...string) ([]byte, error)
}

// DockerTo runs docker with stdout going to w; it returns stderr.
func (r ExecRunner) DockerTo(ctx context.Context, dir string, stdin io.Reader, w io.Writer, args ...string) ([]byte, error) {
	bin := r.Bin
	if bin == "" {
		bin = "docker"
	}
	cmd := r.cmd(ctx, bin, dir, args)
	cmd.Stdin = stdin
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = w, &errb
	err := cmd.Run()
	return errb.Bytes(), err
}

// client is the shell snippet run inside the db container; "$@" are fixed arguments from this file only.
const client = `MYSQL_PWD="$(cat /run/secrets/db_password)" exec "$0" -uwordpress "$@"`

func (m *Manager) dbArgs(prog string, args ...string) []string {
	return append([]string{"exec", "-T", "db", "sh", "-c", client, prog}, args...)
}

func (m *Manager) dbSite(id string) (Site, error) {
	s, err := m.Get(id)
	if err != nil {
		return s, err
	}
	if s.Phase != "ready" {
		return s, fmt.Errorf("%w: the website is not ready", ErrBadInput)
	}
	return s, nil
}

var sqlErrRe = regexp.MustCompile(`(?m)^ERROR [0-9]+ \([0-9A-Z]+\)(?: at line [0-9]+)?: .*$`)

// dbErr turns client output into a short message; "is not running" covers a stopped database.
func dbErr(out []byte) error {
	if e := sqlErrRe.Find(out); e != nil {
		return fmt.Errorf("%w: %s", ErrBadInput, e)
	}
	t := tail(out)
	if strings.Contains(t, "is not running") || strings.Contains(t, "no container") || strings.Contains(t, "not running") {
		return fmt.Errorf("%w: the database container is not running; start the website first", ErrBadInput)
	}
	return errors.New("database call failed: " + t)
}

// Table is one table of the site database.
type Table struct {
	Name      string `json:"name"`
	Engine    string `json:"engine"`
	Rows      int64  `json:"rows"` // estimate for InnoDB
	Data      int64  `json:"data"`
	Index     int64  `json:"index"`
	Collation string `json:"collation"`
}

// DB is the overview of a site database.
type DB struct {
	Name    string  `json:"name"`
	User    string  `json:"user"`
	Host    string  `json:"host"`
	Version string  `json:"version"`
	Size    int64   `json:"size"`
	Tables  []Table `json:"tables"`
	Backups []Dump  `json:"backups"`
}

// Dump is a database copy the agent keeps (taken before an import).
type Dump struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Created int64  `json:"created"`
}

const overviewSQL = `SELECT VERSION();
SELECT table_name, IFNULL(engine,''), IFNULL(table_rows,0), IFNULL(data_length,0), IFNULL(index_length,0), IFNULL(table_collation,'')
FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_name;
`

// Database returns the tables of the site database.
func (m *Manager) Database(ctx context.Context, id string) (DB, error) {
	if _, err := m.dbSite(id); err != nil {
		return DB{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := m.compose(ctx, id, strings.NewReader(overviewSQL), m.dbArgs("mariadb", "--batch", "--skip-column-names", "wordpress")...)
	if err != nil {
		return DB{}, dbErr(out)
	}
	d := DB{Name: "wordpress", User: "wordpress", Host: "db", Tables: []Table{}}
	for i, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		f := strings.Split(line, "\t")
		if i == 0 {
			d.Version = strings.TrimSpace(line)
			continue
		}
		if len(f) != 6 {
			continue
		}
		t := Table{Name: f[0], Engine: f[1], Collation: f[5]}
		t.Rows, _ = strconv.ParseInt(f[2], 10, 64)
		t.Data, _ = strconv.ParseInt(f[3], 10, 64)
		t.Index, _ = strconv.ParseInt(f[4], 10, 64)
		d.Size += t.Data + t.Index
		d.Tables = append(d.Tables, t)
	}
	d.Backups = m.dumps(id)
	return d, nil
}

// Result is one result set of the SQL console.
type Result struct {
	Statement string      `json:"statement"`
	Columns   []string    `json:"columns"`
	Rows      [][]*string `json:"rows"` // nil = SQL NULL
	Limited   bool        `json:"limited"`
}

// QueryAnswer is what one console run returns.
type QueryAnswer struct {
	Results []Result `json:"results"`
	Millis  int64    `json:"ms"`
}

func cut(s string) string {
	if len(s) <= maxCell {
		return s
	}
	return s[:maxCell] + "…"
}

// parseXML reads the client's --xml output: one <resultset> per statement that returns rows.
func parseXML(out []byte) ([]Result, error) {
	body := bytes.ReplaceAll(out, []byte(`<?xml version="1.0"?>`), nil)
	dec := xml.NewDecoder(io.MultiReader(strings.NewReader("<all>"), bytes.NewReader(body), strings.NewReader("</all>")))
	var res []Result
	var cur *Result
	var row []*string
	var field *string
	var inField bool
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read the database answer: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "resultset":
				res = append(res, Result{Columns: []string{}, Rows: [][]*string{}})
				cur = &res[len(res)-1]
				for _, a := range t.Attr {
					if a.Name.Local == "statement" {
						cur.Statement = cut(a.Value)
					}
				}
			case "row":
				row = []*string{}
			case "field":
				inField, field = true, nil
				text.Reset()
				nul := false
				for _, a := range t.Attr {
					if a.Name.Local == "nil" && a.Value == "true" {
						nul = true
					}
					if a.Name.Local == "name" && cur != nil && len(cur.Rows) == 0 {
						cur.Columns = append(cur.Columns, a.Value)
					}
				}
				if !nul {
					s := ""
					field = &s
				}
			}
		case xml.CharData:
			if inField && field != nil {
				text.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "field":
				if field != nil {
					s := cut(text.String())
					field = &s
				}
				row = append(row, field)
				inField = false
			case "row":
				if cur != nil {
					cur.Rows = append(cur.Rows, row)
					if len(cur.Rows) >= QueryRows {
						cur.Limited = true
					}
				}
			}
		}
	}
	return res, nil
}

// Query runs SQL typed in the console. SELECTs return at most QueryRows rows each.
func (m *Manager) Query(ctx context.Context, id, sql string) (QueryAnswer, error) {
	if _, err := m.dbSite(id); err != nil {
		return QueryAnswer{}, err
	}
	sql = strings.TrimSpace(sql)
	if sql == "" || len(sql) > MaxSQL || strings.ContainsRune(sql, 0) {
		return QueryAnswer{}, fmt.Errorf("%w: SQL must be 1 to %d characters", ErrBadInput, MaxSQL)
	}
	if !strings.HasSuffix(sql, ";") {
		sql += ";"
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	in := fmt.Sprintf("SET SESSION sql_select_limit=%d;\n%s\n", QueryRows, sql)
	start := m.now()
	out, err := m.compose(ctx, id, strings.NewReader(in), m.dbArgs("mariadb", "--xml", "wordpress")...)
	ms := m.now().Sub(start).Milliseconds()
	if err != nil {
		return QueryAnswer{}, dbErr(out)
	}
	res, err := parseXML(out)
	if err != nil {
		return QueryAnswer{}, err
	}
	if res == nil {
		res = []Result{}
	}
	return QueryAnswer{Results: res, Millis: ms}, nil
}

func (m *Manager) dumpDir(id string) string { return filepath.Join(m.dir(id), "db-backups") }

var dumpRe = regexp.MustCompile(`^(before-import|export)-[0-9]{8}-[0-9]{6}\.sql\.gz$`)

func (m *Manager) dumps(id string) []Dump {
	ents, _ := os.ReadDir(m.dumpDir(id))
	out := []Dump{}
	for _, e := range ents {
		if !dumpRe.MatchString(e.Name()) || !strings.HasPrefix(e.Name(), "before-import-") {
			continue
		}
		if fi, err := e.Info(); err == nil {
			out = append(out, Dump{Name: e.Name(), Size: fi.Size(), Created: fi.ModTime().Unix()})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name > out[b].Name })
	return out
}

// dump writes a gzip'd mariadb-dump of the site database to a new file in db-backups and returns its path.
func (m *Manager) dump(ctx context.Context, id, kind string) (string, error) {
	st, ok := m.run.(Streamer)
	if !ok {
		return "", errors.New("export is not available")
	}
	if err := os.MkdirAll(m.dumpDir(id), 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s.sql.gz", kind, m.now().In(wib).Format("20060102-150405"))
	p := filepath.Join(m.dumpDir(id), name)
	f, err := os.OpenFile(p+".part", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	gz := gzip.NewWriter(f)
	bw := bufio.NewWriterSize(gz, 1<<20)
	dir := m.dir(id)
	args := append([]string{"compose", "--project-directory", dir, "-p", Project(id), "-f", filepath.Join(dir, "compose.yml")},
		m.dbArgs("mariadb-dump", "--single-transaction", "--quick", "--no-tablespaces", "--default-character-set=utf8mb4",
			"--add-drop-table", "wordpress")...)
	errOut, err := st.DockerTo(ctx, dir, nil, bw, args...)
	err = errors.Join(err, bw.Flush(), gz.Close(), f.Close())
	if err != nil {
		os.Remove(p + ".part")
		if len(errOut) > 0 {
			return "", dbErr(errOut)
		}
		return "", err
	}
	return p, os.Rename(p+".part", p)
}

var wib = time.FixedZone("WIB", 7*3600)

// Export makes a fresh .sql.gz copy of the database. The caller streams it and then calls the returned cleanup.
func (m *Manager) Export(ctx context.Context, id string) (string, func(), error) {
	if _, err := m.dbSite(id); err != nil {
		return "", nil, err
	}
	if !m.mu.TryLock() {
		return "", nil, ErrBusy
	}
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	p, err := m.dump(ctx, id, "export")
	if err != nil {
		return "", nil, err
	}
	return p, func() { os.Remove(p) }, nil
}

// OpenDump opens one of the kept before-import copies for download.
func (m *Manager) OpenDump(id, name string) (*os.File, os.FileInfo, error) {
	if _, err := m.Get(id); err != nil {
		return nil, nil, err
	}
	if !dumpRe.MatchString(name) || !strings.HasPrefix(name, "before-import-") {
		return nil, nil, fmt.Errorf("%w: unknown backup", ErrBadInput)
	}
	f, err := os.Open(filepath.Join(m.dumpDir(id), name))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: unknown backup", ErrBadInput)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

// Import replaces tables with the ones in an uploaded .sql or .sql.gz file. A copy of the current database is
// taken first and kept (the newest keepDumps), so an import can always be undone by importing that copy.
func (m *Manager) Import(ctx context.Context, id string, body io.Reader) (string, error) {
	if _, err := m.dbSite(id); err != nil {
		return "", err
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	defer m.mu.Unlock()
	if err := os.MkdirAll(m.dumpDir(id), 0o700); err != nil {
		return "", err
	}
	up := filepath.Join(m.dumpDir(id), "upload-"+randHex(6)+".part")
	f, err := os.OpenFile(up, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	defer os.Remove(up)
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(body, MaxImport+1))
	if err != nil {
		return "", err
	}
	if n > MaxImport {
		return "", ErrTooBig
	}
	if n == 0 {
		return "", fmt.Errorf("%w: the file is empty", ErrBadInput)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	var in io.Reader = f
	head := make([]byte, 2)
	if _, err := io.ReadFull(f, head); err == nil && head[0] == 0x1f && head[1] == 0x8b {
		_, _ = f.Seek(0, io.SeekStart)
		gz, err := gzip.NewReader(f)
		if err != nil {
			return "", fmt.Errorf("%w: not a valid .gz file", ErrBadInput)
		}
		in = gz
	} else {
		_, _ = f.Seek(0, io.SeekStart)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	backup, err := m.dump(ctx, id, "before-import")
	if err != nil {
		return "", fmt.Errorf("the safety copy before import failed, nothing was changed: %w", err)
	}
	m.pruneDumps(id)
	out, err := m.compose(ctx, id, in, m.dbArgs("mariadb", "--batch", "--default-character-set=utf8mb4", "wordpress")...)
	if err != nil {
		return filepath.Base(backup), dbErr(out)
	}
	return filepath.Base(backup), nil
}

func (m *Manager) pruneDumps(id string) {
	d := m.dumps(id)
	for i := keepDumps; i < len(d); i++ {
		os.Remove(filepath.Join(m.dumpDir(id), d[i].Name))
	}
}
