package sites

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/npbk"
)

func TestScheduleDue(t *testing.T) {
	at := func(d, h, mi int) time.Time { return time.Date(2026, 11, d, h, mi, 0, 0, wib) } // 2026-11-26 is a Thursday
	rc := &BackupSchedule{}
	daily := BackupSchedule{Every: "daily", Hour: 2, Recipient: rc.Recipient}
	if due, _ := daily.due(at(26, 2, 5)); due {
		t.Fatal("due without a key")
	}
	k := npbkRecipient(t)
	daily.Recipient = &k
	cases := []struct {
		s    BackupSchedule
		now  time.Time
		due  bool
		next time.Time
	}{
		{daily, at(26, 2, 5), true, at(26, 2, 0)},
		{daily, at(26, 1, 59), false, at(26, 2, 0)},
		{daily, at(26, 3, 0), false, at(27, 2, 0)},
		{BackupSchedule{Every: "daily", Hour: 2, Recipient: &k, LastRun: at(26, 2, 1).Unix()}, at(26, 2, 30), false, at(27, 2, 0)},
		{BackupSchedule{Every: "daily", Hour: 2, Recipient: &k, LastRun: at(25, 2, 1).Unix()}, at(26, 2, 30), true, at(26, 2, 0)},
		{BackupSchedule{Every: "weekly", Hour: 2, Weekday: 4, Recipient: &k}, at(26, 2, 10), true, at(26, 2, 0)},
		{BackupSchedule{Every: "weekly", Hour: 2, Weekday: 0, Recipient: &k}, at(26, 2, 10), false, at(29, 2, 0)},
		{BackupSchedule{Hour: 2, Recipient: &k}, at(26, 2, 10), false, time.Time{}},
	}
	for i, c := range cases {
		due, next := c.s.due(c.now)
		if due != c.due || !next.Equal(c.next) {
			t.Errorf("case %d: due %v next %v, want %v %v", i, due, next, c.due, c.next)
		}
	}
}

// s3Fake keeps objects in memory and checks that requests are signed.
type s3Fake struct {
	mu   sync.Mutex
	objs map[string][]byte
	fail bool
}

func (f *s3Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(r.Header.Get("Authorization"), "Credential=AKTEST/") || f.fail {
		w.WriteHeader(403)
		fmt.Fprint(w, "<Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>")
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/bkt/")
	switch {
	case r.Method == "GET" && r.URL.Query().Get("list-type") == "2":
		var keys []string
		for k := range f.objs {
			if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		fmt.Fprint(w, "<ListBucketResult>")
		for _, k := range keys {
			fmt.Fprintf(w, "<Contents><Key>%s</Key><Size>%d</Size></Contents>", k, len(f.objs[k]))
		}
		fmt.Fprint(w, "<IsTruncated>false</IsTruncated></ListBucketResult>")
	case r.Method == "PUT":
		b, _ := io.ReadAll(r.Body)
		f.objs[key] = b
	case r.Method == "DELETE":
		delete(f.objs, key)
		w.WriteHeader(204)
	default:
		w.WriteHeader(400)
	}
}

func s3Env(t *testing.T, m *Manager) (*s3Fake, string) {
	t.Helper()
	f := &s3Fake{objs: map[string][]byte{}}
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	m.UseDestHTTP(srv.Client())
	return f, srv.URL
}

func TestBackupDests(t *testing.T) {
	m, _ := newTest(t, nil)
	_, url := s3Env(t, m)
	good := DestInput{Kind: "s3", Name: "IDCloudHost", On: true, Keep: 7, Endpoint: url, Bucket: "bkt", Prefix: "srv/", AccessKey: "AKTEST", Secret: "s3cret-value"}
	for _, bad := range []func(*DestInput){
		func(d *DestInput) { d.Kind = "ftp" },
		func(d *DestInput) { d.Endpoint = "http://is3.cloudhost.id" },
		func(d *DestInput) { d.Endpoint = "https://user:pw@is3.cloudhost.id" },
		func(d *DestInput) { d.Bucket = "Bad_Bucket" },
		func(d *DestInput) { d.Prefix = "../x" },
		func(d *DestInput) { d.Keep = 0 },
		func(d *DestInput) { d.Secret = "" },
		func(d *DestInput) { d.Name = "" },
		func(d *DestInput) { d.Secret = "a\nb" },
	} {
		in := good
		bad(&in)
		if _, err := m.SaveDest(in); !errors.Is(err, ErrBadInput) {
			t.Fatalf("%+v accepted: %v", in, err)
		}
	}
	id, err := m.SaveDest(good)
	if err != nil || !destIDRe.MatchString(id) {
		t.Fatal(id, err)
	}
	v, _ := m.BackupPlanView()
	if len(v.Dests) != 1 || v.Dests[0].Secret != "" || !v.Dests[0].HasSecret || v.Dests[0].Prefix != "srv" {
		t.Fatalf("%+v", v.Dests)
	}
	// a change without a secret keeps the stored one; the kind cannot change
	good.ID, good.Secret, good.Keep = id, "", 3
	if _, err := m.SaveDest(good); err != nil {
		t.Fatal(err)
	}
	if x, _ := m.dest(id); x.Secret != "s3cret-value" || x.Keep != 3 {
		t.Fatalf("%+v", x)
	}
	sf := good
	sf.Kind, sf.Host, sf.Port, sf.User, sf.Path = "sftp", "backup.example.net", 22, "nostek", "backups"
	if _, err := m.SaveDest(sf); !errors.Is(err, ErrBadInput) {
		t.Fatal("kind changed", err)
	}
	r, err := m.TestDest(context.Background(), id)
	if err != nil || !r.OK {
		t.Fatalf("%+v %v", r, err)
	}
	fi, _ := os.Stat(filepath.Join(m.planDir(), "dests.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatal(fi.Mode())
	}
	if err := m.DeleteDest(id); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteDest(id); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestScheduledBackup(t *testing.T) {
	m, _, _ := serverSite(t)
	now := time.Now().Truncate(time.Hour).Add(10 * time.Minute) // the panel's copy must be fresh
	ticking(m, now)
	hr := now.In(wib).Hour()
	f, url := s3Env(t, m)
	if err := m.SetBackupSchedule("daily", hr, 0, ""); !errors.Is(err, ErrBadInput) {
		t.Fatal("on without a passphrase", err)
	}
	if err := m.SetBackupSchedule("daily", hr, 0, "short"); !errors.Is(err, ErrBadInput) {
		t.Fatal("short passphrase", err)
	}
	if err := m.SetBackupSchedule("daily", hr, 0, serverPass); err != nil {
		t.Fatal(err)
	}
	// the passphrase is not kept anywhere
	_ = filepath.WalkDir(m.planDir(), func(p string, d os.DirEntry, _ error) error {
		if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte(serverPass)) {
			t.Fatalf("passphrase in %s", p)
		}
		return nil
	})
	// three old copies, keep two
	f.objs["srv/nostek-server-20261101-020000.npbk"] = []byte("old1")
	f.objs["srv/nostek-server-20261102-020000.npbk"] = []byte("old2")
	f.objs["srv/notes.txt"] = []byte("not ours")
	id, _ := m.SaveDest(DestInput{Kind: "s3", Name: "S3", On: true, Keep: 2, Endpoint: url, Bucket: "bkt", Prefix: "srv", AccessKey: "AKTEST", Secret: "x"})
	_, _ = m.SaveDest(DestInput{Kind: "s3", Name: "Off", On: false, Keep: 2, Endpoint: url, Bucket: "bkt", Prefix: "off", AccessKey: "AKTEST", Secret: "x"})
	v, _ := m.BackupPlanView()
	if !v.Due || !v.Sealed || v.Every != "daily" {
		t.Fatalf("%+v", v)
	}
	job, err := m.StartScheduledBackup("v1.41.0", "v1.41.0")
	if err != nil {
		t.Fatal(err)
	}
	if jv := waitJob(t, m, job); jv.State != "done" {
		t.Fatalf("%+v", jv)
	}
	if _, err := m.StartScheduledBackup("", ""); !errors.Is(err, ErrBadInput) {
		t.Fatal("ran twice in one slot", err)
	}
	b, _ := m.ListServerBackups()
	name := b.Backups[0].Name
	got := f.objs["srv/"+name]
	if !bytes.HasPrefix(got, []byte("NPBK2\n")) {
		t.Fatalf("not sealed to the key: %q", got[:min(10, len(got))])
	}
	if _, ok := f.objs["srv/nostek-server-20261101-020000.npbk"]; ok {
		t.Fatal("oldest copy kept")
	}
	if _, ok := f.objs["srv/nostek-server-20261102-020000.npbk"]; !ok || f.objs["srv/notes.txt"] == nil {
		t.Fatal("too much removed")
	}
	for k := range f.objs {
		if strings.HasPrefix(k, "off/") {
			t.Fatal("a destination that is off got a copy")
		}
	}
	// the same passphrase opens it, as on restore
	if _, err := CheckServerBackup(filepath.Join(m.serverBackupDir(), name), serverPass); err != nil {
		t.Fatal(err)
	}
	v, _ = m.BackupPlanView()
	if !v.LastOK || v.LastFile != name || v.Due {
		t.Fatalf("%+v", v)
	}
	if x, _ := m.dest(id); x.Last == nil || !x.Last.OK || x.Last.File != name {
		t.Fatalf("%+v", x.Last)
	}

	// a failing destination: the backup is kept, the result says so and an event is added
	f.fail = true
	writePanelCopy(t, m)
	_, err = m.StartServerUpload(name, id)
	if err != nil {
		t.Fatal(err)
	}
	// wait for the upload's result (up to 10 s: a busy CI runner can be slow)
	for i := 0; i < 1000; i++ {
		if x, _ := m.dest(id); m.serverJob() == nil && x.Last != nil && !x.Last.OK {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if x, _ := m.dest(id); x.Last == nil || x.Last.OK || !strings.Contains(x.Last.Msg, "AccessDenied") {
		t.Fatalf("%+v", x.Last)
	}
	s, _ := m.schedule()
	s.LastRun = 0
	_ = m.writeJSON("schedule.json", s)
	job, err = m.StartScheduledBackup("", "")
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, job)
	v, _ = m.BackupPlanView()
	if v.LastOK || !strings.Contains(v.LastMsg, "S3") {
		t.Fatalf("%+v", v)
	}
	w := m.Watchdog(0)
	found := false
	for _, e := range w.Events {
		if e.Service == "server-backup" && e.Kind == "failed" && e.Reason == "copy" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no event: %+v", w.Events)
	}
	// off keeps the key
	if err := m.SetBackupSchedule("", hr, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetBackupSchedule("weekly", 3, 1, ""); err != nil {
		t.Fatal("on again without a passphrase", err)
	}
}

// sshHost fakes the OpenSSH tools.
type sshHost struct {
	mu      sync.Mutex
	calls   []string
	batches []string
	ls      string
}

func (h *sshHost) Host(_ context.Context, stdin io.Reader, bin string, args ...string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := hostBins[bin]; !ok {
		return nil, errors.New("not allowed")
	}
	h.calls = append(h.calls, bin+" "+strings.Join(args, " "))
	switch bin {
	case "ssh-keygen":
		if args[0] == "-l" {
			return []byte("256 SHA256:abcDEF backup.example.net (ED25519)\n"), nil
		}
		f := args[len(args)-1]
		_ = os.WriteFile(f, []byte("PRIVATE"), 0o600)
		_ = os.WriteFile(f+".pub", []byte("ssh-ed25519 AAAAC3Nza nostek-backup@test\n"), 0o644)
	case "ssh-keyscan":
		return []byte("# backup.example.net:2222 SSH-2.0-OpenSSH_9.6\n[backup.example.net]:2222 ssh-ed25519 AAAAC3NzaHOST\n"), nil
	case "sftp":
		b, _ := io.ReadAll(stdin)
		h.batches = append(h.batches, string(b))
		if strings.HasPrefix(string(b), "ls -1") {
			return []byte(h.ls), nil
		}
	}
	return nil, nil
}

func TestSFTPDest(t *testing.T) {
	m, _, _ := serverSite(t)
	h := &sshHost{}
	m.UseFirewall(h)
	ctx := context.Background()
	for _, bad := range []DestInput{
		{Kind: "sftp", Name: "x", Keep: 3, Host: "bad host", Port: 22, User: "nostek", Path: "b"},
		{Kind: "sftp", Name: "x", Keep: 3, Host: "h.example", Port: 0, User: "nostek", Path: "b"},
		{Kind: "sftp", Name: "x", Keep: 3, Host: "h.example", Port: 22, User: "-oProxy", Path: "b"},
		{Kind: "sftp", Name: "x", Keep: 3, Host: "h.example", Port: 22, User: "nostek", Path: "a/../b"},
		{Kind: "sftp", Name: "x", Keep: 3, Host: "h.example", Port: 22, User: "nostek", Path: `b"; rm`},
	} {
		if _, err := m.SaveDest(bad); !errors.Is(err, ErrBadInput) {
			t.Fatalf("%+v accepted", bad)
		}
	}
	id, err := m.SaveDest(DestInput{Kind: "sftp", Name: "Box", On: true, Keep: 1, Host: "backup.example.net", Port: 2222, User: "nostek", Path: "backups/panel/"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := m.SSHKey(ctx)
	if err != nil || !strings.HasPrefix(key, "ssh-ed25519 ") {
		t.Fatal(key, err)
	}
	r, _ := m.TestDest(ctx, id)
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	x, _ := m.dest(id)
	if x.HostKey != "SHA256:abcDEF" {
		t.Fatalf("%+v", x)
	}
	kh, _ := os.ReadFile(m.knownHosts(id))
	if !strings.Contains(string(kh), "AAAAC3NzaHOST") || strings.Contains(string(kh), "#") {
		t.Fatalf("%q", kh)
	}
	file := makeServerBackup(t, m)
	name := filepath.Base(file)
	h.ls = "backups/panel/nostek-server-20261101-020000.npbk\nbackups/panel/" + name + "\nbackups/panel/other.txt\n"
	job, err := m.StartServerUpload(name, id)
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("%+v", v)
	}
	all := strings.Join(h.batches, "\n")
	for _, want := range []string{
		`put "` + file + `" "backups/panel/` + name + `.part"`,
		`rename "backups/panel/` + name + `.part" "backups/panel/` + name + `"`,
		`rm "backups/panel/nostek-server-20261101-020000.npbk"`,
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %s in\n%s", want, all)
		}
	}
	if strings.Contains(all, "other.txt") {
		t.Fatal("removed a file that is not a backup")
	}
	for _, c := range h.calls {
		if strings.HasPrefix(c, "sftp ") && (!strings.Contains(c, "StrictHostKeyChecking=yes") || !strings.Contains(c, "BatchMode=yes") || !strings.HasSuffix(c, "nostek@backup.example.net")) {
			t.Fatal(c)
		}
	}
	// moving the destination to another host forgets the pinned key
	_, _ = m.SaveDest(DestInput{ID: id, Kind: "sftp", Name: "Box", On: true, Keep: 1, Host: "other.example.net", Port: 2222, User: "nostek", Path: "backups"})
	if x, _ := m.dest(id); x.HostKey != "" {
		t.Fatal("host key kept")
	}
	if _, err := os.Stat(m.knownHosts(id)); err == nil {
		t.Fatal("known_hosts kept")
	}
}

func npbkRecipient(t *testing.T) npbk.Recipient {
	t.Helper()
	r, err := npbk.NewRecipient(serverPass)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
