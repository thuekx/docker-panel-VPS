package sites

// Check, repair and optimize of a website's tables (DB-02). MariaDB runs CHECK / REPAIR / OPTIMIZE TABLE on the
// tables the database itself lists (names are quoted, never taken as SQL); PostgreSQL has no table check or repair,
// so only "optimize" runs there, as VACUUM (ANALYZE) on the whole database.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TableOps are the operations a website's tables can get.
var TableOps = []string{"check", "repair", "optimize"}

// TableResult is the database's own answer for one table (MariaDB: Msg_type status, note, info, warning or error).
type TableResult struct {
	Table string `json:"table"`
	Type  string `json:"type"`
	Text  string `json:"text"`
}

// TableAnswer is what one run returns; OK is false when any table reports an error.
type TableAnswer struct {
	Op      string        `json:"op"`
	OK      bool          `json:"ok"`
	Results []TableResult `json:"results"`
	Millis  int64         `json:"ms"`
}

func quoteIdent(n string) string { return "`" + strings.ReplaceAll(n, "`", "``") + "`" }

// Tables runs op on the given tables of a website's database (none = all of them).
func (m *Manager) Tables(ctx context.Context, id, op string, tables []string) (TableAnswer, error) {
	known := false
	for _, o := range TableOps {
		known = known || o == op
	}
	if !known {
		return TableAnswer{}, fmt.Errorf("%w: op must be check, repair or optimize", ErrBadInput)
	}
	if len(tables) > 1000 {
		return TableAnswer{}, fmt.Errorf("%w: too many tables", ErrBadInput)
	}
	site, err := m.dbSite(id)
	if err != nil {
		return TableAnswer{}, err
	}
	if site.IsPostgres() && op != "optimize" {
		return TableAnswer{}, fmt.Errorf("%w: PostgreSQL has no table %s; use optimize (VACUUM ANALYZE)", ErrBadInput, op)
	}
	db, err := m.Database(ctx, id)
	if err != nil {
		return TableAnswer{}, err
	}
	have := map[string]bool{}
	for _, t := range db.Tables {
		have[t.Name] = true
	}
	if len(tables) == 0 {
		for _, t := range db.Tables {
			tables = append(tables, t.Name)
		}
	}
	for _, t := range tables {
		if !have[t] {
			return TableAnswer{}, fmt.Errorf("%w: no table %q in the database", ErrBadInput, t)
		}
	}
	ans := TableAnswer{Op: op, OK: true, Results: []TableResult{}}
	if len(tables) == 0 {
		return ans, nil
	}
	if !m.mu.TryLock() {
		return TableAnswer{}, ErrBusy
	}
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	start := m.now()
	if site.IsPostgres() {
		out, err := m.compose(ctx, id, strings.NewReader("VACUUM (ANALYZE);\n"), pgArgs("psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", site.DBName())...)
		if err != nil {
			if e := pgErrRe.Find(out); e != nil {
				return TableAnswer{}, fmt.Errorf("%w: %s", ErrBadInput, e)
			}
			return TableAnswer{}, dbErr(out)
		}
		ans.Results = append(ans.Results, TableResult{Table: "*", Type: "status", Text: "VACUUM (ANALYZE) done"})
		ans.Millis = m.now().Sub(start).Milliseconds()
		return ans, nil
	}
	q := make([]string, len(tables))
	for i, t := range tables {
		q[i] = quoteIdent(t)
	}
	sql := strings.ToUpper(op) + " TABLE " + strings.Join(q, ", ") + ";\n"
	var out []byte
	if site.IsNative() {
		out, err = m.nativeClient(ctx, site, strings.NewReader(sql), "--xml")
	} else {
		out, err = m.compose(ctx, id, strings.NewReader(sql), m.dbArgs("mariadb", "--xml", site.DBName())...)
	}
	ans.Millis = m.now().Sub(start).Milliseconds()
	if err != nil {
		return TableAnswer{}, dbErr(out)
	}
	res, err := parseXML(out)
	if err != nil {
		return TableAnswer{}, err
	}
	for _, r := range res {
		col := map[string]int{}
		for i, c := range r.Columns {
			col[c] = i
		}
		ti, ok1 := col["Table"]
		yi, ok2 := col["Msg_type"]
		xi, ok3 := col["Msg_text"]
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		for _, row := range r.Rows {
			get := func(i int) string {
				if i < len(row) && row[i] != nil {
					return *row[i]
				}
				return ""
			}
			tr := TableResult{Table: get(ti), Type: strings.ToLower(get(yi)), Text: get(xi)}
			// "db.table": the database name is the same for every row
			if _, after, ok := strings.Cut(tr.Table, "."); ok {
				tr.Table = after
			}
			if tr.Type == "error" {
				ans.OK = false
			}
			ans.Results = append(ans.Results, tr)
		}
	}
	return ans, nil
}
