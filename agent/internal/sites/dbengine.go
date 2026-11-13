package sites

// PostgreSQL support of the database tools (APP-02b): the same overview, console, export, import, backup and
// restore as MariaDB, run with the PostgreSQL clients inside the site's own db container. As with MariaDB, the
// password is read inside the container from its secret file, never passed as an argument.

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
)

// pgClient is the shell snippet run inside a PostgreSQL db container; "$@" are fixed arguments from this package.
const pgClient = `PGPASSWORD="$(cat /run/secrets/db_password)" exec "$0" -h 127.0.0.1 -U "$POSTGRES_USER" "$@"`

func pgArgs(prog string, args ...string) []string {
	return append([]string{"exec", "-T", "db", "sh", "-c", pgClient, prog}, args...)
}

// loadArgs runs an SQL script (an import or a restore) into the site database.
func (m *Manager) loadArgs(s Site) []string {
	if s.IsPostgres() {
		return pgArgs("psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", s.DBName())
	}
	return m.dbArgs("mariadb", "--batch", "--default-character-set=utf8mb4", s.DBName())
}

// dumpArgs writes the whole site database as SQL to stdout; full adds routines and events (backups).
func (m *Manager) dumpArgs(s Site, full bool) []string {
	if s.IsPostgres() {
		return pgArgs("pg_dump", "--no-owner", "--no-acl", "--clean", "--if-exists", "-d", s.DBName())
	}
	args := []string{"--single-transaction", "--quick", "--no-tablespaces", "--default-character-set=utf8mb4", "--add-drop-table"}
	if full {
		args = append(args, "--routines", "--events")
	}
	return m.dbArgs("mariadb-dump", append(args, s.DBName())...)
}

// resetArgs empties the site database before a restore, so tables made after the backup do not stay behind.
func (m *Manager) resetArgs(s Site) (string, []string) {
	if s.IsPostgres() {
		return "DROP SCHEMA IF EXISTS public CASCADE;\nCREATE SCHEMA public;\n", pgArgs("psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", s.DBName())
	}
	return resetSQL(s.DBName()), m.dbArgs("mariadb", "--batch")
}

var pgErrRe = regexp.MustCompile(`(?m)^(?:psql:[^\n]*?)?(?:ERROR|FATAL):  .*$`)

const pgOverviewSQL = `SHOW server_version;
SELECT c.relname, 'PostgreSQL', GREATEST(c.reltuples, 0)::bigint, pg_relation_size(c.oid), pg_indexes_size(c.oid), ''
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') ORDER BY c.relname;
`

func (m *Manager) pgDatabase(ctx context.Context, s Site) (DB, error) {
	out, err := m.compose(ctx, s.ID, strings.NewReader(pgOverviewSQL), pgArgs("psql", "-X", "-q", "-A", "-t", "-F", "\t", "-v", "ON_ERROR_STOP=1", "-d", s.DBName())...)
	if err != nil {
		return DB{}, pgErr(out)
	}
	d := DB{Name: s.DBName(), User: s.DBName(), Host: "db", Tables: []Table{}}
	for i, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if i == 0 {
			d.Version = "PostgreSQL " + strings.TrimSpace(line)
			continue
		}
		f := strings.Split(line, "\t")
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
	d.Backups = m.dumps(s.ID)
	return d, nil
}

func pgErr(out []byte) error {
	if e := pgErrRe.Find(out); e != nil {
		return fmt.Errorf("%w: %s", ErrBadInput, strings.TrimSpace(string(e)))
	}
	return dbErr(out)
}

// pgQuery runs console SQL with psql's HTML output. Every result table carries a random marker that typed SQL
// cannot know, so echoed text is never taken for a result; SQL NULL is printed as another random marker.
func (m *Manager) pgQuery(ctx context.Context, s Site, sql string) ([]Result, error) {
	mark := randHex(8)
	null := "NP-NULL-" + randHex(8)
	in := fmt.Sprintf("\\pset format html\n\\pset tableattr 'class=\"np-%s\"'\n\\pset null '%s'\n\\pset footer off\nSET statement_timeout = '55s';\n%s\n", mark, null, sql)
	out, err := m.compose(ctx, s.ID, strings.NewReader(in), pgArgs("psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", s.DBName())...)
	if err != nil {
		return nil, pgErr(out)
	}
	return parsePGHTML(string(out), mark, null), nil
}

var (
	pgCellRe = regexp.MustCompile(`(?s)<(th|td)[^>]*>(.*?)</(?:th|td)>`)
	pgRowRe  = regexp.MustCompile(`(?s)<tr[^>]*>(.*?)</tr>`)
)

// parsePGHTML reads the result tables psql printed with the given marker.
func parsePGHTML(out, mark, null string) []Result {
	res := []Result{}
	open := ` class="np-` + mark + `">` // psql escapes typed text, so a quote cannot appear outside its own tags
	for {
		i := strings.Index(out, open)
		if i < 0 {
			break
		}
		out = out[i+len(open):]
		j := strings.Index(out, "</table>")
		if j < 0 {
			break
		}
		table := out[:j]
		out = out[j:]
		r := Result{Columns: []string{}, Rows: [][]*string{}}
		for _, tr := range pgRowRe.FindAllStringSubmatch(table, -1) {
			var row []*string
			header := false
			for _, c := range pgCellRe.FindAllStringSubmatch(tr[1], -1) {
				raw := strings.TrimSpace(c[2])
				if raw == "&nbsp;" { // psql's empty cell
					raw = ""
				}
				v := html.UnescapeString(strings.ReplaceAll(raw, "<br />\n", "\n"))
				v = strings.ReplaceAll(v, "<br />", "\n")
				if c[1] == "th" {
					header = true
					r.Columns = append(r.Columns, strings.TrimSpace(v))
					continue
				}
				if strings.TrimSpace(v) == null {
					row = append(row, nil)
					continue
				}
				v = cut(v)
				row = append(row, &v)
			}
			if header || row == nil {
				continue
			}
			if len(r.Rows) >= QueryRows {
				r.Limited = true
				break
			}
			r.Rows = append(r.Rows, row)
		}
		res = append(res, r)
	}
	return res
}

// sqlErr reads the error of the site's database client.
func sqlErr(s Site, out []byte) error {
	if s.IsPostgres() {
		return pgErr(out)
	}
	return dbErr(out)
}
