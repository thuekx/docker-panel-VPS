package sites

// BAK-03: scheduled server backups and backup destinations.
//
// Schedule. Off, every day or once a week at a chosen hour (WIB). The passphrase is typed once when the schedule is
// set and is never kept: the agent keeps only the public half of the key it gives (npbk layout 2), so a scheduled
// backup is sealed without it and opened with the same passphrase on restore. The panel looks every few minutes;
// when a backup is due it writes its database copy and starts the scheduled run, which the agent checks again.
//
// Destinations. S3-compatible storage (path-style, Signature V4) or an SFTP server (key login: the agent makes its
// own SSH key and the page shows the public half; the server's host key is pinned at the first test). After each
// scheduled backup the file is copied to every destination that is on, the newest N kept there, and the result of
// each is kept. A listed backup can also be sent by hand. A failed backup or copy adds a "server-backup" event to the
// watchdog feed, which the panel turns into a backup alert. Secrets stay in a root-only file and never leave the
// agent; the destinations are not part of a server backup.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/npbk"
	"github.com/thuekx/docker-panel-VPS/agent/internal/s3"
)

// BackupSchedule is when scheduled server backups run.
type BackupSchedule struct {
	Every     string          `json:"every"`   // "" (off) | daily | weekly
	Hour      int             `json:"hour"`    // 0–23 WIB
	Weekday   int             `json:"weekday"` // 0 Sunday … 6 Saturday (weekly)
	Recipient *npbk.Recipient `json:"recipient,omitempty"`
	LastRun   int64           `json:"last_run,omitempty"`
	LastOK    bool            `json:"last_ok"`
	LastMsg   string          `json:"last_msg,omitempty"`
	LastFile  string          `json:"last_file,omitempty"`
}

// DestResult is the last copy to a destination.
type DestResult struct {
	At   int64  `json:"at"`
	OK   bool   `json:"ok"`
	Msg  string `json:"msg,omitempty"`
	File string `json:"file,omitempty"`
}

// BackupDest is one place server backups are copied to.
type BackupDest struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // s3 | sftp
	Name string `json:"name"`
	On   bool   `json:"on"`
	Keep int    `json:"keep"`
	// S3
	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	AccessKey string `json:"access_key,omitempty"`
	Secret    string `json:"secret,omitempty"` // never in a view
	// SFTP
	Host    string `json:"host,omitempty"`
	Port    int    `json:"port,omitempty"`
	User    string `json:"user,omitempty"`
	Path    string `json:"path,omitempty"`
	HostKey string `json:"host_key,omitempty"` // fingerprint of the pinned host key
	// results
	Last *DestResult `json:"last,omitempty"`
	Test *DestResult `json:"test,omitempty"`
}

// BackupDestView is a destination without its secret.
type BackupDestView struct {
	BackupDest
	HasSecret bool `json:"has_secret"`
}

// BackupPlan is what the panel shows: the schedule, the destinations and the agent's SSH public key.
type BackupPlan struct {
	Every    string           `json:"every"`
	Hour     int              `json:"hour"`
	Weekday  int              `json:"weekday"`
	Sealed   bool             `json:"sealed"` // a passphrase was set
	LastRun  int64            `json:"last_run,omitempty"`
	LastOK   bool             `json:"last_ok"`
	LastMsg  string           `json:"last_msg,omitempty"`
	LastFile string           `json:"last_file,omitempty"`
	Due      bool             `json:"due"`
	Next     int64            `json:"next,omitempty"`
	Dests    []BackupDestView `json:"dests"`
	SSHKey   string           `json:"ssh_key,omitempty"`
}

const (
	maxDests     = 8
	destKeepMax  = 90
	destTestName = ".nostek-test"
)

var (
	destIDRe   = regexp.MustCompile(`^d[0-9a-f]{8}$`)
	bucketRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionRe   = regexp.MustCompile(`^[a-z0-9-]{0,32}$`)
	s3PrefixRe = regexp.MustCompile(`^[A-Za-z0-9._/-]{0,200}$`)
	keyIDRe    = regexp.MustCompile(`^[A-Za-z0-9+/=._-]{3,128}$`)
	sftpUserRe = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)
	sftpPathRe = regexp.MustCompile(`^[A-Za-z0-9._/~-]{1,200}$`)
	hostNameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62})(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}))*$`)
	destNameRe = regexp.MustCompile(`^[\p{L}\p{N} ._-]{1,40}$`)
)

type planState struct {
	mu sync.Mutex
}

func (m *Manager) planDir() string { return filepath.Join(m.cfg.Data, "server-backup") }

func (m *Manager) plan() *planState {
	m.planOnce.Do(func() { m.planSt = &planState{} })
	return m.planSt
}

func (m *Manager) readJSON(name string, v any) error {
	b, err := os.ReadFile(filepath.Join(m.planDir(), name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (m *Manager) writeJSON(name string, v any) error {
	if err := os.MkdirAll(m.planDir(), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(m.planDir(), 0o700)
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(m.planDir(), name), b, 0o600)
}

func (m *Manager) schedule() (BackupSchedule, error) {
	var s BackupSchedule
	err := m.readJSON("schedule.json", &s)
	return s, err
}

func (m *Manager) dests() ([]BackupDest, error) {
	var d []BackupDest
	err := m.readJSON("dests.json", &d)
	return d, err
}

// due tells whether a scheduled backup should start at now, and when the next one is.
func (s BackupSchedule) due(now time.Time) (bool, time.Time) {
	if s.Every == "" || s.Recipient == nil {
		return false, time.Time{}
	}
	t := now.In(wib)
	start := time.Date(t.Year(), t.Month(), t.Day(), s.Hour, 0, 0, 0, wib)
	ok := func(d time.Time) bool { return s.Every == "daily" || int(d.Weekday()) == s.Weekday }
	// this slot: inside the hour, the right day, and not run in it yet
	if ok(start) && !t.Before(start) && t.Before(start.Add(time.Hour)) && s.LastRun < start.Unix() {
		return true, start
	}
	next := start
	for !next.After(t) || !ok(next) {
		next = next.AddDate(0, 0, 1)
	}
	return false, next
}

// BackupPlanView returns the schedule and the destinations, without secrets.
func (m *Manager) BackupPlanView() (BackupPlan, error) {
	p := m.plan()
	p.mu.Lock()
	defer p.mu.Unlock()
	s, err := m.schedule()
	if err != nil {
		return BackupPlan{}, err
	}
	d, err := m.dests()
	if err != nil {
		return BackupPlan{}, err
	}
	v := BackupPlan{Every: s.Every, Hour: s.Hour, Weekday: s.Weekday, Sealed: s.Recipient != nil, LastRun: s.LastRun,
		LastOK: s.LastOK, LastMsg: s.LastMsg, LastFile: s.LastFile, Dests: []BackupDestView{}}
	due, next := s.due(m.now())
	v.Due = due && m.serverJob() == nil
	if !next.IsZero() {
		v.Next = next.Unix()
	}
	for _, x := range d {
		has := x.Secret != ""
		x.Secret = ""
		v.Dests = append(v.Dests, BackupDestView{x, has})
	}
	if b, err := os.ReadFile(m.sshKey() + ".pub"); err == nil {
		v.SSHKey = strings.TrimSpace(string(b))
	}
	return v, nil
}

// SetBackupSchedule sets the schedule. A passphrase (when given) replaces the key; turning the schedule on needs one
// the first time. Off keeps the key, so it can be turned on again without a passphrase.
func (m *Manager) SetBackupSchedule(every string, hour, weekday int, pass string) error {
	if every != "" && every != "daily" && every != "weekly" {
		return fmt.Errorf("%w: every must be daily or weekly", ErrBadInput)
	}
	if hour < 0 || hour > 23 || weekday < 0 || weekday > 6 {
		return fmt.Errorf("%w: bad hour or day", ErrBadInput)
	}
	if len(pass) > 1024 {
		return fmt.Errorf("%w: the passphrase is too long", ErrBadInput)
	}
	var rc *npbk.Recipient
	if pass != "" {
		r, err := npbk.NewRecipient(pass)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrBadInput, err.Error())
		}
		rc = &r
	}
	p := m.plan()
	p.mu.Lock()
	defer p.mu.Unlock()
	s, err := m.schedule()
	if err != nil {
		return err
	}
	if rc != nil {
		s.Recipient = rc
	}
	if every != "" && s.Recipient == nil {
		return fmt.Errorf("%w: a passphrase is needed to turn the schedule on", ErrBadInput)
	}
	s.Every, s.Hour, s.Weekday = every, hour, weekday
	return m.writeJSON("schedule.json", s)
}

// DestInput is a new or changed destination. An empty Secret keeps the stored one.
type DestInput struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	On        bool   `json:"on"`
	Keep      int    `json:"keep"`
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`
	AccessKey string `json:"access_key"`
	Secret    string `json:"secret"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	User      string `json:"user"`
	Path      string `json:"path"`
}

func checkDest(in DestInput) error {
	bad := func(s string) error { return fmt.Errorf("%w: %s", ErrBadInput, s) }
	if !destNameRe.MatchString(in.Name) {
		return bad("the name needs 1–40 letters, digits, spaces, dots, dashes or underscores")
	}
	if in.Keep < 1 || in.Keep > destKeepMax {
		return bad("keep must be 1–90")
	}
	switch in.Kind {
	case "s3":
		u, err := url.Parse(in.Endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || len(in.Endpoint) > 200 {
			return bad("the endpoint must be https://host (optionally with a port)")
		}
		if !hostNameRe.MatchString(u.Hostname()) && net.ParseIP(u.Hostname()) == nil {
			return bad("the endpoint host is not valid")
		}
		if !bucketRe.MatchString(in.Bucket) {
			return bad("the bucket name is not valid")
		}
		if !regionRe.MatchString(in.Region) {
			return bad("the region is not valid")
		}
		if !s3PrefixRe.MatchString(in.Prefix) || strings.Contains(in.Prefix, "..") || strings.HasPrefix(in.Prefix, "/") {
			return bad("the prefix may hold letters, digits and . _ - /")
		}
		if !keyIDRe.MatchString(in.AccessKey) {
			return bad("the access key is not valid")
		}
		if len(in.Secret) > 256 || strings.ContainsAny(in.Secret, "\r\n") {
			return bad("the secret key is not valid")
		}
	case "sftp":
		if (!hostNameRe.MatchString(in.Host) && net.ParseIP(in.Host) == nil) || len(in.Host) > 253 {
			return bad("the host is not valid")
		}
		if in.Port < 1 || in.Port > 65535 {
			return bad("the port must be 1–65535")
		}
		if !sftpUserRe.MatchString(in.User) {
			return bad("the user name is not valid")
		}
		if !sftpPathRe.MatchString(in.Path) || strings.Contains(in.Path, "..") {
			return bad("the folder may hold letters, digits and . _ - / ~")
		}
	default:
		return bad("kind must be s3 or sftp")
	}
	return nil
}

// SaveDest adds (no ID) or changes a destination. A change of where it points clears the pinned host key.
func (m *Manager) SaveDest(in DestInput) (string, error) {
	if err := checkDest(in); err != nil {
		return "", err
	}
	p := m.plan()
	p.mu.Lock()
	defer p.mu.Unlock()
	d, err := m.dests()
	if err != nil {
		return "", err
	}
	x := BackupDest{}
	i := -1
	if in.ID != "" {
		i = slices.IndexFunc(d, func(b BackupDest) bool { return b.ID == in.ID })
		if i < 0 {
			return "", ErrNotFound
		}
		x = d[i]
		if x.Kind != in.Kind {
			return "", fmt.Errorf("%w: the kind of a destination cannot change", ErrBadInput)
		}
	} else {
		if len(d) >= maxDests {
			return "", fmt.Errorf("%w: at most %d destinations", ErrBadInput, maxDests)
		}
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		x.ID, x.Kind = "d"+hex.EncodeToString(b), in.Kind
	}
	if in.Kind == "s3" && in.Secret == "" && x.Secret == "" {
		return "", fmt.Errorf("%w: the secret key is needed", ErrBadInput)
	}
	if x.Host != in.Host || x.Port != in.Port {
		x.HostKey = ""
		_ = os.Remove(m.knownHosts(x.ID))
	}
	x.Name, x.On, x.Keep = in.Name, in.On, in.Keep
	if in.Kind == "s3" {
		x.Endpoint, x.Region, x.Bucket, x.Prefix, x.AccessKey = strings.TrimRight(in.Endpoint, "/"), in.Region, in.Bucket, strings.Trim(in.Prefix, "/"), in.AccessKey
		if in.Secret != "" {
			x.Secret = in.Secret
		}
	} else {
		x.Host, x.Port, x.User, x.Path = in.Host, in.Port, in.User, strings.TrimRight(in.Path, "/")
	}
	if i < 0 {
		d = append(d, x)
	} else {
		d[i] = x
	}
	return x.ID, m.writeJSON("dests.json", d)
}

// DeleteDest removes a destination (its copies stay where they are).
func (m *Manager) DeleteDest(id string) error {
	p := m.plan()
	p.mu.Lock()
	defer p.mu.Unlock()
	d, err := m.dests()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(d, func(b BackupDest) bool { return b.ID == id })
	if i < 0 {
		return ErrNotFound
	}
	d = slices.Delete(d, i, i+1)
	_ = os.Remove(m.knownHosts(id))
	return m.writeJSON("dests.json", d)
}

func (m *Manager) dest(id string) (BackupDest, error) {
	d, err := m.dests()
	if err != nil {
		return BackupDest{}, err
	}
	for _, x := range d {
		if x.ID == id {
			return x, nil
		}
	}
	return BackupDest{}, ErrNotFound
}

// record keeps a result on a destination (test or copy).
func (m *Manager) record(id string, test bool, r DestResult) {
	p := m.plan()
	p.mu.Lock()
	defer p.mu.Unlock()
	d, err := m.dests()
	if err != nil {
		return
	}
	for i := range d {
		if d[i].ID == id {
			if test {
				d[i].Test = &r
			} else {
				d[i].Last = &r
			}
		}
	}
	_ = m.writeJSON("dests.json", d)
}

// ---------- S3

func (m *Manager) s3Client(x BackupDest) *s3.Client {
	c := &s3.Client{Endpoint: x.Endpoint, Region: x.Region, Bucket: x.Bucket, AccessKey: x.AccessKey, SecretKey: x.Secret}
	if m.destHTTP != nil {
		c.HTTP = m.destHTTP
	}
	return c
}

func s3Key(x BackupDest, name string) string {
	if x.Prefix == "" {
		return name
	}
	return x.Prefix + "/" + name
}

// ---------- SFTP (the host's OpenSSH client)

func (m *Manager) sshKey() string { return filepath.Join(m.planDir(), "ssh", "id_ed25519") }

func (m *Manager) knownHosts(id string) string {
	return filepath.Join(m.planDir(), "ssh", "known_hosts_"+id)
}

// ensureSSHKey makes the agent's SSH key once.
func (m *Manager) ensureSSHKey(ctx context.Context) error {
	if _, err := os.Stat(m.sshKey()); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.sshKey()), 0o700); err != nil {
		return err
	}
	host, _ := os.Hostname()
	out, err := m.fw.Host(ctx, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nostek-backup@"+host, "-f", m.sshKey())
	if err != nil {
		return fmt.Errorf("ssh-keygen: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SSHKey makes the agent's SSH key when needed and returns its public half.
func (m *Manager) SSHKey(ctx context.Context) (string, error) {
	if m.fw == nil {
		return "", errors.New("the host tools are not available")
	}
	if err := m.ensureSSHKey(ctx); err != nil {
		return "", err
	}
	b, err := os.ReadFile(m.sshKey() + ".pub")
	return strings.TrimSpace(string(b)), err
}

// pinHost fetches and keeps the server's host keys at the first contact; it returns the fingerprint.
func (m *Manager) pinHost(ctx context.Context, x BackupDest) (string, error) {
	kh := m.knownHosts(x.ID)
	if _, err := os.Stat(kh); err == nil && x.HostKey != "" {
		return x.HostKey, nil
	}
	out, err := m.fw.Host(ctx, nil, "ssh-keyscan", "-T", "10", "-t", "ed25519,ecdsa,rsa", "-p", strconv.Itoa(x.Port), x.Host)
	keys := []string{}
	for l := range strings.SplitSeq(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") && len(strings.Fields(l)) == 3 {
			keys = append(keys, l)
		}
	}
	if len(keys) == 0 {
		if err == nil {
			err = errors.New("no host key")
		}
		return "", fmt.Errorf("could not reach %s:%d over SSH: %v", x.Host, x.Port, err)
	}
	if err := writeFile(kh, []byte(strings.Join(keys, "\n")+"\n"), 0o600); err != nil {
		return "", err
	}
	fp, err := m.fw.Host(ctx, nil, "ssh-keygen", "-l", "-f", kh)
	if err != nil {
		return "", fmt.Errorf("ssh-keygen: %v", err)
	}
	f := strings.Fields(strings.SplitN(string(fp), "\n", 2)[0])
	if len(f) < 2 {
		return "", errors.New("ssh-keygen: no fingerprint")
	}
	return f[1], nil
}

// sftp runs one batch of commands against the destination.
func (m *Manager) sftp(ctx context.Context, x BackupDest, batch string) (string, error) {
	out, err := m.fw.Host(ctx, strings.NewReader(batch), "sftp", "-b", "-", "-q", "-F", "/dev/null",
		"-i", m.sshKey(), "-P", strconv.Itoa(x.Port),
		"-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile="+m.knownHosts(x.ID), "-o", "ConnectTimeout=20", "-o", "ServerAliveInterval=30",
		x.User+"@"+x.Host)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return string(out), fmt.Errorf("sftp: %v: %s", err, msg)
	}
	return string(out), nil
}

// sftpQuote quotes a path for an sftp batch line.
func sftpQuote(p string) string { return `"` + strings.ReplaceAll(p, `"`, `\"`) + `"` }

func (m *Manager) sftpPut(ctx context.Context, x BackupDest, local, name string) error {
	if _, err := m.pinHost(ctx, x); err != nil {
		return err
	}
	dst := path.Join(x.Path, name)
	batch := "-mkdir " + sftpQuote(x.Path) + "\n" +
		"-rm " + sftpQuote(dst+".part") + "\n" +
		"put " + sftpQuote(local) + " " + sftpQuote(dst+".part") + "\n" +
		"-rm " + sftpQuote(dst) + "\n" +
		"rename " + sftpQuote(dst+".part") + " " + sftpQuote(dst) + "\n"
	_, err := m.sftp(ctx, x, batch)
	return err
}

func (m *Manager) sftpList(ctx context.Context, x BackupDest) ([]string, error) {
	out, err := m.sftp(ctx, x, "ls -1 "+sftpQuote(x.Path)+"\n")
	if err != nil {
		return nil, err
	}
	var names []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		n := path.Base(strings.TrimSpace(sc.Text()))
		if strings.HasPrefix(n, "nostek-server-") {
			names = append(names, n)
		}
	}
	return names, nil
}

// ---------- copy, prune, test

// isBackupName tells a server backup's file name.
func isBackupName(n string) bool { return serverBackupRe.MatchString(n) }

// sendTo copies one local backup to a destination and removes the oldest copies there beyond Keep.
func (m *Manager) sendTo(ctx context.Context, j *Job, x BackupDest, name string) error {
	local := filepath.Join(m.serverBackupDir(), name)
	progress := func(done, total int64) {
		if total > 0 {
			j.Progress(fmt.Sprintf("%s %d%%", x.Name, done*100/total))
		}
	}
	var names []string
	switch x.Kind {
	case "s3":
		c := m.s3Client(x)
		if err := c.PutFile(ctx, s3Key(x, name), local, progress); err != nil {
			return err
		}
		pfx := ""
		if x.Prefix != "" {
			pfx = x.Prefix + "/"
		}
		objs, err := c.List(ctx, pfx+"nostek-server-")
		if err != nil {
			return fmt.Errorf("copied, but the list for clean-up failed: %v", err)
		}
		for _, o := range objs {
			if n := strings.TrimPrefix(o.Key, pfx); isBackupName(n) {
				names = append(names, n)
			}
		}
		slices.Sort(names)
		for len(names) > x.Keep {
			if err := c.Delete(ctx, pfx+names[0]); err != nil {
				return fmt.Errorf("copied, but an old copy could not be removed: %v", err)
			}
			names = names[1:]
		}
	case "sftp":
		if m.fw == nil {
			return errors.New("the host tools are not available")
		}
		if err := m.ensureSSHKey(ctx); err != nil {
			return err
		}
		if err := m.sftpPut(ctx, x, local, name); err != nil {
			return err
		}
		all, err := m.sftpList(ctx, x)
		if err != nil {
			return fmt.Errorf("copied, but the list for clean-up failed: %v", err)
		}
		for _, n := range all {
			if isBackupName(n) {
				names = append(names, n)
			}
		}
		slices.Sort(names)
		if extra := len(names) - x.Keep; extra > 0 {
			var b strings.Builder
			for _, n := range names[:extra] {
				b.WriteString("rm " + sftpQuote(path.Join(x.Path, n)) + "\n")
			}
			if _, err := m.sftp(ctx, x, b.String()); err != nil {
				return fmt.Errorf("copied, but an old copy could not be removed: %v", err)
			}
		}
	}
	return nil
}

// sendAll copies a backup to every destination that is on; it returns the names that failed.
func (m *Manager) sendAll(ctx context.Context, j *Job, name string, only string) ([]string, error) {
	d, err := m.dests()
	if err != nil {
		return nil, err
	}
	var failed []string
	sent := 0
	for _, x := range d {
		if (only == "" && !x.On) || (only != "" && x.ID != only) {
			continue
		}
		if sent == 0 {
			j.Step("upload")
		}
		sent++
		err := m.sendTo(ctx, j, x, name)
		r := DestResult{At: m.now().Unix(), OK: err == nil, File: name}
		if err != nil {
			r.Msg = trimMsg(err.Error())
			failed = append(failed, x.Name)
		}
		m.record(x.ID, false, r)
	}
	if only != "" && sent == 0 {
		return nil, ErrNotFound
	}
	return failed, nil
}

func trimMsg(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// TestDest writes and removes a small file at a destination and keeps the result.
func (m *Manager) TestDest(ctx context.Context, id string) (DestResult, error) {
	x, err := m.dest(id)
	if err != nil {
		return DestResult{}, err
	}
	err = m.testDest(ctx, &x)
	r := DestResult{At: m.now().Unix(), OK: err == nil}
	if err != nil {
		r.Msg = trimMsg(err.Error())
	}
	m.record(id, true, r)
	return r, nil
}

func (m *Manager) testDest(ctx context.Context, x *BackupDest) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	body := []byte("Nostek Panel backup destination test " + m.now().UTC().Format(time.RFC3339) + "\n")
	switch x.Kind {
	case "s3":
		c := m.s3Client(*x)
		if err := c.PutBytes(ctx, s3Key(*x, destTestName), body); err != nil {
			return err
		}
		if _, err := c.List(ctx, x.Prefix); err != nil {
			return err
		}
		return c.Delete(ctx, s3Key(*x, destTestName))
	case "sftp":
		if m.fw == nil {
			return errors.New("the host tools are not available")
		}
		if err := m.ensureSSHKey(ctx); err != nil {
			return err
		}
		fp, err := m.pinHost(ctx, *x)
		if err != nil {
			return err
		}
		if fp != x.HostKey {
			x.HostKey = fp
			m.setHostKey(x.ID, fp)
		}
		tmp, err := os.CreateTemp(m.planDir(), "test-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		_, _ = tmp.Write(body)
		_ = tmp.Close()
		dst := path.Join(x.Path, destTestName)
		_, err = m.sftp(ctx, *x, "-mkdir "+sftpQuote(x.Path)+"\nput "+sftpQuote(tmp.Name())+" "+sftpQuote(dst)+"\nrm "+sftpQuote(dst)+"\n")
		return err
	}
	return ErrBadInput
}

func (m *Manager) setHostKey(id, fp string) {
	p := m.plan()
	p.mu.Lock()
	defer p.mu.Unlock()
	d, err := m.dests()
	if err != nil {
		return
	}
	for i := range d {
		if d[i].ID == id {
			d[i].HostKey = fp
		}
	}
	_ = m.writeJSON("dests.json", d)
}

// ---------- runs

// StartScheduledBackup makes the scheduled server backup when it is due (the panel wrote its copy just before),
// then copies it to the destinations.
func (m *Manager) StartScheduledBackup(panelVersion, agentVersion string) (string, error) {
	p := m.plan()
	p.mu.Lock()
	s, err := m.schedule()
	if err != nil {
		p.mu.Unlock()
		return "", err
	}
	if due, _ := s.due(m.now()); !due {
		p.mu.Unlock()
		return "", fmt.Errorf("%w: no scheduled backup is due", ErrBadInput)
	}
	rc, prev := *s.Recipient, s.LastRun
	s.LastRun = m.now().Unix()
	err = m.writeJSON("schedule.json", s)
	p.mu.Unlock()
	if err != nil {
		return "", err
	}
	seal := func(w io.Writer) (*npbk.Writer, error) { return npbk.NewWriterTo(w, rc) }
	after := func(ctx context.Context, j *Job, name string) error {
		failed, err := m.sendAll(ctx, j, name, "")
		msg := ""
		switch {
		case err != nil:
			msg = err.Error()
		case len(failed) > 0:
			msg = "copy failed: " + strings.Join(failed, ", ")
		}
		m.scheduleResult(msg == "", msg, name)
		if msg != "" {
			m.backupEvent("copy")
		}
		return nil // the backup itself is made; the page shows each destination's result
	}
	id, err := m.startServerBackup("server-backup", seal, panelVersion, agentVersion, after)
	if errors.Is(err, ErrBusy) { // another job holds the server: the next look tries again
		p.mu.Lock()
		if s, e := m.schedule(); e == nil {
			s.LastRun = prev
			_ = m.writeJSON("schedule.json", s)
		}
		p.mu.Unlock()
		return "", err
	}
	if err != nil {
		m.scheduleResult(false, err.Error(), "")
		m.backupEvent("backup")
		return "", err
	}
	go m.watchScheduled(id)
	return id, nil
}

// watchScheduled records a scheduled backup that failed before its copies.
func (m *Manager) watchScheduled(id string) {
	for {
		time.Sleep(5 * time.Second)
		j := m.jobs.Get(id)
		if j == nil {
			return
		}
		v := j.View()
		if v.State == "running" {
			continue
		}
		if v.State == "failed" {
			m.scheduleResult(false, trimMsg(v.Error), "")
			m.backupEvent("backup")
		}
		return
	}
}

func (m *Manager) scheduleResult(ok bool, msg, file string) {
	p := m.plan()
	p.mu.Lock()
	defer p.mu.Unlock()
	s, err := m.schedule()
	if err != nil {
		return
	}
	s.LastOK, s.LastMsg = ok, trimMsg(msg)
	if file != "" {
		s.LastFile = file
	}
	_ = m.writeJSON("schedule.json", s)
}

// backupEvent adds a "server-backup" event to the watchdog feed (reason: "backup" or "copy").
func (m *Manager) backupEvent(reason string) {
	w := m.watch()
	w.mu.Lock()
	m.addWatchEvent(w, watchTarget{svc: "server-backup", reason: reason}, "failed", m.now())
	w.mu.Unlock()
}

// StartServerUpload sends a listed server backup to one destination (dest) as a job.
func (m *Manager) StartServerUpload(name, dest string) (string, error) {
	if !serverBackupRe.MatchString(name) {
		return "", ErrNotFound
	}
	if _, err := os.Stat(filepath.Join(m.serverBackupDir(), name)); err != nil {
		return "", ErrNotFound
	}
	if _, err := m.dest(dest); err != nil {
		return "", err
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	j := m.jobs.New("server-upload", "")
	go func() {
		defer m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
		defer cancel()
		failed, err := m.sendAll(ctx, j, name, dest)
		if err == nil && len(failed) > 0 {
			x, _ := m.dest(dest)
			msg := "copy failed"
			if x.Last != nil && x.Last.Msg != "" {
				msg = x.Last.Msg
			}
			err = errors.New(msg)
		}
		if err != nil {
			j.Fail(err.Error())
			return
		}
		j.Step("done " + name)
		j.Done("")
	}()
	return j.ID, nil
}

// UseDestHTTP sets the HTTP client for S3 destinations (tests only).
func (m *Manager) UseDestHTTP(c *http.Client) { m.destHTTP = c }
