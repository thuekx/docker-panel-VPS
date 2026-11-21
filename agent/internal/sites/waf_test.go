package sites

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWAFOnOff(t *testing.T) {
	m, f, id := readySite(t)
	c := SecurityChange{Op: "waf-set", Mode: "detect", Level: 1}
	if _, err := m.ChangeSecurity(id, c); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "PANEL-01 UPDATE") {
		t.Fatalf("without the WAF service: %v", err)
	}
	f.answer = map[string]string{wafContainer: "running healthy\n"}
	for _, bad := range []SecurityChange{{Op: "waf-set", Mode: "on", Level: 1}, {Op: "waf-set", Mode: "block", Level: 3},
		{Op: "waf-exclude-add", Rule: 942100}, {Op: "waf-off"}} {
		if _, err := m.ChangeSecurity(id, bad); !errors.Is(err, ErrBadInput) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	secDo(t, m, id, c)
	cf, _ := os.ReadFile(filepath.Join(m.dir(id), "compose.yml"))
	if !strings.Contains(string(cf), "service: site-"+id+"-waf@file") || strings.Contains(string(cf), "service: site-"+id+"\n") {
		t.Fatalf("routers do not use the WAF:\n%s", cf)
	}
	edge, err := os.ReadFile(m.wafEdgeFile(id))
	if err != nil || !strings.Contains(string(edge), "site-"+id+"-waf:") || !strings.Contains(string(edge), `url: "http://nostek-core-waf-1:8090"`) {
		t.Fatalf("edge file: %s %v", edge, err)
	}
	var r wafRules
	b, _ := os.ReadFile(m.wafRulesFile(id))
	if json.Unmarshal(b, &r) != nil || r.Site != id || r.Upstream != "http://np-site-"+id+"-web-1:80" || r.Mode != "detect" || r.Paranoia != 1 ||
		len(r.Hosts) == 0 || r.Hosts[0] != "voltix.id" || r.Exclude == nil || r.Skip == nil {
		t.Fatalf("rules: %s", b)
	}
	if v, _ := m.Security(id); !v.WAF.On || v.WAF.Mode != "detect" || !v.WAFReady {
		t.Fatalf("view: %+v", v.WAF)
	}
	if _, err := m.ChangeSecurity(id, c); !errors.Is(err, ErrNoChange) {
		t.Errorf("same setting: %v", err)
	}
	// rules switched off and paths skipped reach the WAF's file
	for _, bad := range []SecurityChange{{Op: "waf-exclude-add", Rule: 949110}, {Op: "waf-exclude-add", Rule: 980170}, {Op: "waf-exclude-add", Rule: 1},
		{Op: "waf-skip-add", Path: "/"}, {Op: "waf-skip-add", Path: "wp-admin"}, {Op: "waf-skip-add", Path: "/a/../b"}, {Op: "waf-skip-add", Path: "/a b"},
		{Op: "waf-exclude-remove", Rule: 942100}, {Op: "waf-skip-remove", Path: "/x"}} {
		if _, err := m.ChangeSecurity(id, bad); !errors.Is(err, ErrBadInput) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	secDo(t, m, id, SecurityChange{Op: "waf-exclude-add", Rule: 942100})
	secDo(t, m, id, SecurityChange{Op: "waf-exclude-add", Rule: 941100})
	secDo(t, m, id, SecurityChange{Op: "waf-skip-add", Path: "/wp-admin/"})
	secDo(t, m, id, SecurityChange{Op: "waf-set", Mode: "block", Level: 2})
	b, _ = os.ReadFile(m.wafRulesFile(id))
	_ = json.Unmarshal(b, &r)
	if fmt.Sprint(r.Exclude) != "[941100 942100]" || fmt.Sprint(r.Skip) != "[/wp-admin/]" || r.Mode != "block" || r.Paranoia != 2 {
		t.Fatalf("rules after changes: %s", b)
	}
	secDo(t, m, id, SecurityChange{Op: "waf-exclude-remove", Rule: 942100})
	secDo(t, m, id, SecurityChange{Op: "waf-skip-remove", Path: "/wp-admin/"})
	if s, _ := m.Get(id); fmt.Sprint(s.WAF.Exclude) != "[941100]" || s.WAF.Skip != nil {
		t.Fatalf("site.json: %+v", s.WAF)
	}
	// security routers go through the WAF too
	secDo(t, m, id, SecurityChange{Op: "block-add", IP: "192.0.2.0/24"})
	cf, _ = os.ReadFile(filepath.Join(m.dir(id), "compose.yml"))
	if !strings.Contains(string(cf), "routers.site-"+id+"-block.service: site-"+id+"-waf@file") {
		t.Errorf("block router bypasses the WAF:\n%s", cf)
	}
	// off: the routers use the website again, the files go
	secDo(t, m, id, SecurityChange{Op: "waf-off"})
	cf, _ = os.ReadFile(filepath.Join(m.dir(id), "compose.yml"))
	if strings.Contains(string(cf), "-waf@file") {
		t.Error("labels still use the WAF")
	}
	if _, err := os.Stat(m.wafEdgeFile(id)); err == nil {
		t.Error("edge file left")
	}
	if _, err := os.Stat(m.wafRulesFile(id)); err == nil {
		t.Error("rules file left")
	}
}

func TestWAFFailedOnPutsBack(t *testing.T) {
	m, f, id := readySite(t)
	f.answer = map[string]string{wafContainer: "running healthy\n"}
	f.fail = "--wait"
	job, err := m.ChangeSecurity(id, SecurityChange{Op: "waf-set", Mode: "block", Level: 1})
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "failed" {
		t.Fatalf("%+v", v)
	}
	if _, err := os.Stat(m.wafEdgeFile(id)); err == nil {
		t.Error("edge file left after a failed turn-on")
	}
	if s, _ := m.Get(id); s.WAF != nil {
		t.Error("site.json says on")
	}
}

func TestWAFNativeAndSync(t *testing.T) {
	s := Site{ID: "n-test", Hosts: []Host{{Name: "n.test", Role: "primary", Routed: true}, {Name: "www.n.test", Role: "alias", Routed: true}},
		WAF: &WAF{Mode: "detect", Paranoia: 1}, BlockIPs: []string{"192.0.2.1"}}
	e := s.NativeEdge("172.19.0.1")
	if strings.Count(e, "service: site-n-test-waf\n") != 3 || !strings.Contains(e, "url: \"http://172.19.0.1:8088\"") {
		t.Fatalf("native edge:\n%s", e)
	}
	m, f, id := readySite(t)
	f.answer = map[string]string{wafContainer: "running healthy\n"}
	secDo(t, m, id, SecurityChange{Op: "waf-set", Mode: "detect", Level: 1})
	os.Remove(m.wafRulesFile(id))
	stale := m.wafRulesFile("gone-site")
	_ = os.WriteFile(stale, []byte("{}"), 0o644)
	if err := m.SyncWAF(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.wafRulesFile(id)); err != nil {
		t.Error("rules not written again")
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale rules kept")
	}
}

func TestWAFEvents(t *testing.T) {
	m, f, id := readySite(t)
	f.answer = map[string]string{wafContainer: "running healthy\n"}
	m.cfg.AccessLog = filepath.Join(t.TempDir(), "access.log")
	_ = os.WriteFile(m.cfg.AccessLog, nil, 0o644)
	var b strings.Builder
	now := time.Now().Unix()
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, `{"t":%d,"site":%q,"ip":"203.0.113.9","uri":"/?id=%d","rule":942100,"msg":"SQL Injection Attack Detected via libinjection","action":"detected"}`+"\n", now, id, i)
	}
	fmt.Fprintf(&b, `{"t":%d,"site":%q,"ip":"203.0.113.9","uri":"/x","rule":941100,"msg":"XSS Attack Detected via libinjection","action":"detected"}`+"\n", now, id)
	fmt.Fprintf(&b, `{"t":%d,"site":%q,"ip":"203.0.113.9","uri":"/x","rule":949110,"msg":"Inbound Anomaly Score Exceeded","action":"blocked"}`+"\n", now, id)
	b.WriteString("not json\n")
	if err := os.WriteFile(m.wafLog(), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.ReadLogins(); err != nil {
		t.Fatal(err)
	}
	secDo(t, m, id, SecurityChange{Op: "waf-set", Mode: "block", Level: 1})
	secDo(t, m, id, SecurityChange{Op: "waf-exclude-add", Rule: 941100})
	v, _ := m.Security(id)
	w := v.WAF
	if len(w.Events) != wafEventsShown || w.Events[0].Rule != 949110 || w.Blocked != 1 {
		t.Fatalf("events: %d %+v", len(w.Events), w.Events[:2])
	}
	if len(w.Rules) != 2 || w.Rules[0].Rule != 942100 || w.Rules[0].Count != wafEventsKept-2 || w.Rules[1].Rule != 941100 || !w.Rules[1].Excluded {
		t.Fatalf("rules: %+v", w.Rules)
	}
}
