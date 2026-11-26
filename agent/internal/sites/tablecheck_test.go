package sites

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const checkXML = `<?xml version="1.0"?>

<resultset statement="CHECK TABLE ..." xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <row>
	<field name="Table">wordpress.wp_options</field>
	<field name="Op">check</field>
	<field name="Msg_type">status</field>
	<field name="Msg_text">OK</field>
  </row>
  <row>
	<field name="Table">wordpress.we`+"`"+`ird</field>
	<field name="Op">check</field>
	<field name="Msg_type">Error</field>
	<field name="Msg_text">Table is marked as crashed</field>
  </row>
</resultset>
`

func TestTableCheck(t *testing.T) {
	m, f, id := readySite(t)
	f.answer = map[string]string{
		"--skip-column-names": "11.8.3-MariaDB\nwp_options\tInnoDB\t1\t1\t1\tx\nwe`ird\tMyISAM\t1\t1\t1\tx\n",
		"--xml":               checkXML,
	}
	ctx := context.Background()
	for name, c := range map[string]struct {
		op     string
		tables []string
	}{"unknown op": {"drop", nil}, "unknown table": {"check", []string{"wp_users; DROP TABLE x"}}} {
		if _, err := m.Tables(ctx, id, c.op, c.tables); !errors.Is(err, ErrBadInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	a, err := m.Tables(ctx, id, "check", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.OK || len(a.Results) != 2 || a.Results[0].Table != "wp_options" || a.Results[1].Type != "error" || a.Results[1].Table != "we`ird" {
		t.Fatalf("answer %+v", a)
	}
	if sql := f.stdin[len(f.stdin)-1]; sql != "CHECK TABLE `wp_options`, `we``ird`;\n" {
		t.Fatalf("sql %q", sql)
	}
	if _, err := m.Tables(ctx, id, "repair", []string{"we`ird"}); err != nil {
		t.Fatal(err)
	}
	if sql := f.stdin[len(f.stdin)-1]; sql != "REPAIR TABLE `we``ird`;\n" {
		t.Fatalf("sql %q", sql)
	}
	m.mu.Lock()
	_, err = m.Tables(ctx, id, "optimize", nil)
	m.mu.Unlock()
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	f.fail = "--xml"
	if _, err := m.Tables(ctx, id, "check", nil); err == nil || strings.Contains(err.Error(), "MYSQL_PWD") {
		t.Fatalf("failed call: %v", err)
	}
}
