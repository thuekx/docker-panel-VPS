package sites

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDatabaseOverview(t *testing.T) {
	m, f, id := readySite(t)
	f.answer = map[string]string{"--skip-column-names": "11.8.3-MariaDB-ubu2404\nwp_options\tInnoDB\t412\t1589248\t65536\tutf8mb4_unicode_520_ci\nwp_posts\tInnoDB\t9\t16384\t49152\tutf8mb4_unicode_520_ci\n"}
	d, err := m.Database(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != "11.8.3-MariaDB-ubu2404" || len(d.Tables) != 2 || d.Tables[0].Rows != 412 || d.Size != 1589248+65536+16384+49152 {
		t.Fatalf("overview: %+v", d)
	}
	last := strings.Join(f.calls[len(f.calls)-1], " ")
	if !strings.Contains(last, "exec -T db sh -c") || !strings.Contains(last, `MYSQL_PWD="$(cat /run/secrets/db_password)"`) {
		t.Fatalf("client call: %s", last)
	}
	pw, _ := os.ReadFile(m.dir(id) + "/secrets/db_password")
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), string(pw)) {
			t.Fatal("database password in an argument list")
		}
	}
	f.fail = "skip-column-names"
	if _, err := m.Database(context.Background(), id); err == nil {
		t.Fatal("failed call answered")
	}
	if _, err := m.Database(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown site")
	}
}

const xmlOut = `<?xml version="1.0"?>

<resultset statement="SELECT * FROM t" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <row>
	<field name="a">1</field>
	<field name="b">x&lt;y</field>
  </row>

  <row>
	<field name="a">2</field>
	<field name="b" xsi:nil="true" />
  </row>
</resultset>
<?xml version="1.0"?>

<resultset statement="SELECT * FROM t WHERE a=99" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"></resultset>
`

func TestQueryParsesResultsAndLimits(t *testing.T) {
	m, f, id := readySite(t)
	f.answer = map[string]string{"--xml": xmlOut}
	a, err := m.Query(context.Background(), id, "SELECT * FROM t; SELECT * FROM t WHERE a=99")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Results) != 2 {
		t.Fatalf("results: %+v", a)
	}
	r := a.Results[0]
	if strings.Join(r.Columns, ",") != "a,b" || len(r.Rows) != 2 || *r.Rows[0][1] != "x<y" || r.Rows[1][1] != nil {
		t.Fatalf("first result: %+v", r)
	}
	if len(a.Results[1].Rows) != 0 {
		t.Fatal("empty result has rows")
	}
	in := f.stdin[len(f.stdin)-1]
	if !strings.HasPrefix(in, "SET SESSION sql_select_limit=1000;\n") || !strings.HasSuffix(in, "a=99;\n") {
		t.Fatalf("stdin: %q", in)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", MaxSQL+1), "SELECT 1\x00"} {
		if _, err := m.Query(context.Background(), id, bad); !errors.Is(err, ErrBadInput) {
			t.Errorf("query %.10q accepted", bad)
		}
	}
	f.answer = nil
	f.fail = "--xml"
	f.answer = map[string]string{}
	if _, err := m.Query(context.Background(), id, "SELEC 1"); err == nil {
		t.Fatal("error not reported")
	}
	if e := dbErr([]byte("--------------\nSELEC 1\n--------------\n\nERROR 1064 (42000) at line 2: You have an error in your SQL syntax\n")); !errors.Is(e, ErrBadInput) || !strings.Contains(e.Error(), "ERROR 1064") {
		t.Fatalf("dbErr: %v", e)
	}
	if e := dbErr([]byte("service \"db\" is not running")); !strings.Contains(e.Error(), "start the website first") {
		t.Fatalf("stopped db: %v", e)
	}
	// many rows are cut at QueryRows
	var b strings.Builder
	b.WriteString(`<resultset statement="x">`)
	for i := 0; i < QueryRows+5; i++ {
		b.WriteString(`<row><field name="n">1</field></row>`)
	}
	b.WriteString(`</resultset>`)
	res, err := parseXML([]byte(b.String()))
	if err != nil || len(res[0].Rows) < QueryRows || !res[0].Limited {
		t.Fatalf("limit: %v %d", err, len(res[0].Rows))
	}
}

func gzipped(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

func TestExportAndImportKeepACopyFirst(t *testing.T) {
	m, f, id := readySite(t)
	f.dumped = "-- MariaDB dump\nCREATE TABLE wp_posts (id int);\n"
	p, done, err := m.Export(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	fh, _ := os.Open(p)
	zr, err := gzip.NewReader(fh)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	fh.Close()
	if string(b) != f.dumped {
		t.Fatalf("export content %q", b)
	}
	done()
	if _, err := os.Stat(p); err == nil {
		t.Fatal("export file not cleaned up")
	}
	// import a .sql.gz: a copy is taken, then the upload is fed (unpacked) to the client
	tick := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	for i := 0; i < keepDumps+2; i++ {
		tick = tick.Add(time.Minute)
		m.now = func() time.Time { return tick }
		backup, err := m.Import(context.Background(), id, bytes.NewReader(gzipped("INSERT INTO wp_posts VALUES (1);\n")))
		if err != nil || !strings.HasPrefix(backup, "before-import-20261112-14") {
			t.Fatalf("import: %s %v", backup, err)
		}
	}
	if got := f.stdin[len(f.stdin)-1]; got != "INSERT INTO wp_posts VALUES (1);\n" {
		t.Fatalf("import fed %q", got)
	}
	if d := m.dumps(id); len(d) != keepDumps {
		t.Fatalf("kept %d copies, want %d", len(d), keepDumps)
	}
	ents, _ := os.ReadDir(m.dumpDir(id))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "upload-") || strings.HasSuffix(e.Name(), ".part") {
			t.Fatalf("left over: %s", e.Name())
		}
	}
	if fh, _, err := m.OpenDump(id, m.dumps(id)[0].Name); err != nil {
		t.Fatal(err)
	} else {
		fh.Close()
	}
	for _, bad := range []string{"../site.json", "export-20261112-140000.sql.gz", "before-import-x.sql.gz"} {
		if _, _, err := m.OpenDump(id, bad); err == nil {
			t.Errorf("dump %s served", bad)
		}
	}
	// a plain .sql works too
	if _, err := m.Import(context.Background(), id, strings.NewReader("SELECT 1;\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Import(context.Background(), id, strings.NewReader("")); !errors.Is(err, ErrBadInput) {
		t.Fatal("empty import accepted")
	}
	// when the safety copy fails nothing is imported
	n := len(f.stdin)
	f.fail = "mariadb-dump"
	if _, err := m.Import(context.Background(), id, strings.NewReader("DROP TABLE wp_posts;\n")); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("import without copy: %v", err)
	}
	if len(f.stdin) != n {
		t.Fatal("import ran although the safety copy failed")
	}
}
