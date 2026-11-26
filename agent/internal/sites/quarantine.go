package sites

// SEC-02: scheduled malware scans and the quarantine.
//
// Schedule. Per website: off, daily or weekly, and whether found files go to the quarantine by themselves. The scan
// loop looks every 10 minutes during the night (01:00–05:59 WIB); a website is due when its last scan finished more
// than its interval ago (minus an hour, so the time of day does not creep). One scan at a time on the server, only
// with enough free memory. A scheduled scan that finds something adds a "malware" event to the watchdog feed, which
// the panel turns into an alert.
//
// Quarantine. A found file is moved aside, never deleted: copied into <data>/quarantine/<id>/file (root only)
// with what is known about it (website, path, signature, size, sha256, mode, uid/gid, when), then removed from the
// website. It can be put back to the same path (refused when a file is there again) or removed for good. Files of a
// website that is deleted stay in the quarantine until removed.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"syscall"
	"time"
)

// ScanSchedule is a website's scheduled scan.
type ScanSchedule struct {
	Every      string `json:"every"`      // daily | weekly
	Quarantine bool   `json:"quarantine"` // move found files to the quarantine by themselves
}

const (
	scanLoopEvery    = 10 * time.Minute
	quarantineMaxB   = 256 << 20 // a file larger than this is not moved (the scan reads up to 64 MB anyway)
	quarantineMaxAll = 2000
)

// QFile is one file in the quarantine.
type QFile struct {
	ID        string `json:"id"`
	Site      string `json:"site"`
	Domain    string `json:"domain"`
	Path      string `json:"path"`
	Signature string `json:"signature"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Mode      uint32 `json:"mode"`
	UID       int    `json:"uid"`
	GID       int    `json:"gid"`
	At        int64  `json:"at"`
	Auto      bool   `json:"auto"` // moved by a scheduled scan
}

var qidRe = regexp.MustCompile(`^q[0-9a-f]{16}$`)

func (m *Manager) quarantineDir() string { return filepath.Join(m.cfg.Data, "quarantine") }

// SetScanSchedule sets or clears (every "") a website's scheduled scan.
func (m *Manager) SetScanSchedule(id, every string, quarantine bool) (Site, error) {
	if every != "" && every != "daily" && every != "weekly" {
		return Site{}, fmt.Errorf("%w: a schedule is daily or weekly", ErrBadInput)
	}
	if _, err := m.readySite(id); err != nil {
		return Site{}, err
	}
	if !m.mu.TryLock() {
		return Site{}, ErrBusy
	}
	defer m.mu.Unlock()
	s, err := m.Get(id)
	if err != nil {
		return Site{}, err
	}
	s.ScanEvery = nil
	if every != "" {
		s.ScanEvery = &ScanSchedule{Every: every, Quarantine: quarantine}
	}
	return s, writeSite(m.dir(id), s)
}

func scanInterval(every string) time.Duration {
	if every == "weekly" {
		return 7*24*time.Hour - time.Hour
	}
	return 24*time.Hour - time.Hour
}

// ScanLoop runs the scheduled scans until ctx ends.
func (m *Manager) ScanLoop(ctx context.Context) {
	t := time.NewTicker(scanLoopEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.ScanTick(ctx)
		}
	}
}

// ScanTick starts at most one due scheduled scan and waits for it.
func (m *Manager) ScanTick(ctx context.Context) {
	now := m.now()
	if h := now.In(wib).Hour(); h < 1 || h > 5 {
		return
	}
	list, err := m.List()
	if err != nil {
		return
	}
	for _, s := range list {
		if s.ScanEvery == nil || s.Phase != "ready" {
			continue
		}
		last := m.readScan(s.ID)
		if last != nil && now.Sub(time.Unix(last.Started, 0)) < scanInterval(s.ScanEvery.Every) {
			continue
		}
		if m.memAvailableMB() < scanMinFreeMB || !m.scanMu.TryLock() {
			return // not now; the next tick tries again
		}
		m.scheduledScan(ctx, s)
		return
	}
}

func (m *Manager) scheduledScan(ctx context.Context, s Site) {
	defer m.scanMu.Unlock()
	j := m.jobs.New("malware-scan", s.ID)
	sctx, cancel := context.WithTimeout(ctx, ScanTimeout)
	defer cancel()
	r := m.scan(sctx, j, s.ID)
	r.Scheduled = true
	if r.Error == "" && s.ScanEvery.Quarantine {
		for i, f := range r.Found {
			if _, err := m.QuarantineFile(s.ID, f.Path, f.Signature, true); err == nil {
				r.Found[i].Quarantined = true
			}
		}
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	_ = writeFile(filepath.Join(m.dir(s.ID), scanResultFile), b, 0o600)
	if r.Error != "" {
		j.Fail(r.Error)
		return
	}
	j.Done("")
	if n := len(r.Found) + r.More; n > 0 {
		w := m.watch()
		w.mu.Lock()
		reason := strconv.Itoa(n)
		if s.ScanEvery.Quarantine {
			reason += " quarantined"
		}
		m.addWatchEvent(w, watchTarget{site: s.ID, svc: "malware", reason: reason}, "found", m.now())
		w.mu.Unlock()
	}
}

// QuarantineFile moves one file of a website into the quarantine.
func (m *Manager) QuarantineFile(id, p, signature string, auto bool) (QFile, error) {
	rel, err := CleanPath(p)
	if err != nil {
		return QFile{}, err
	}
	if len(signature) > 200 {
		signature = signature[:200]
	}
	s, err := m.Get(id)
	if err != nil {
		return QFile{}, err
	}
	if ents, _ := os.ReadDir(m.quarantineDir()); len(ents) >= quarantineMaxAll {
		return QFile{}, fmt.Errorf("%w: the quarantine is full; remove old files first", ErrBadInput)
	}
	root, err := m.openWP(id)
	if err != nil {
		return QFile{}, err
	}
	defer root.Close()
	fi, err := root.Lstat(rel)
	if err != nil {
		return QFile{}, notExist(err)
	}
	if !fi.Mode().IsRegular() {
		return QFile{}, fmt.Errorf("%w: only a regular file can go to the quarantine", ErrBadInput)
	}
	if fi.Size() > quarantineMaxB {
		return QFile{}, fmt.Errorf("%w: the file is too large for the quarantine", ErrBadInput)
	}
	in, err := root.Open(rel)
	if err != nil {
		return QFile{}, err
	}
	defer in.Close()
	var rb [8]byte
	_, _ = rand.Read(rb[:])
	q := QFile{ID: "q" + hex.EncodeToString(rb[:]), Site: id, Domain: s.Domain, Path: "/" + rel, Signature: signature,
		Size: fi.Size(), Mode: uint32(fi.Mode().Perm()), At: m.now().Unix(), Auto: auto}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		q.UID, q.GID = int(st.Uid), int(st.Gid)
	}
	dir := filepath.Join(m.quarantineDir(), q.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return QFile{}, err
	}
	_ = os.Chmod(m.quarantineDir(), 0o700)
	out, err := os.OpenFile(filepath.Join(dir, "file"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		os.RemoveAll(dir)
		return QFile{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, quarantineMaxB+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != fi.Size() {
		err = errors.New("the file changed while it was moved")
	}
	if err != nil {
		os.RemoveAll(dir)
		return QFile{}, err
	}
	q.SHA256 = hex.EncodeToString(h.Sum(nil))
	b, _ := json.MarshalIndent(q, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o600); err != nil {
		os.RemoveAll(dir)
		return QFile{}, err
	}
	if err := root.Remove(rel); err != nil {
		os.RemoveAll(dir)
		return QFile{}, err
	}
	return q, nil
}

// Quarantine lists the quarantine, newest first.
func (m *Manager) Quarantine() ([]QFile, error) {
	ents, err := os.ReadDir(m.quarantineDir())
	if errors.Is(err, fs.ErrNotExist) {
		return []QFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []QFile{}
	for _, e := range ents {
		if q, err := m.qRead(e.Name()); err == nil {
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out, nil
}

func (m *Manager) qRead(qid string) (QFile, error) {
	if !qidRe.MatchString(qid) {
		return QFile{}, ErrNotFound
	}
	b, err := os.ReadFile(filepath.Join(m.quarantineDir(), qid, "meta.json"))
	if err != nil {
		return QFile{}, ErrNotFound
	}
	var q QFile
	if json.Unmarshal(b, &q) != nil || q.ID != qid {
		return QFile{}, ErrNotFound
	}
	return q, nil
}

// QuarantineRestore puts a file back where it was, with its mode and uid/gid.
func (m *Manager) QuarantineRestore(qid string) (QFile, error) {
	q, err := m.qRead(qid)
	if err != nil {
		return q, err
	}
	rel, err := CleanPath(q.Path)
	if err != nil {
		return q, err
	}
	root, err := m.openWP(q.Site)
	if err != nil {
		return q, fmt.Errorf("%w: the website is gone; the file can only be removed", ErrBadInput)
	}
	defer root.Close()
	if _, err := root.Lstat(rel); err == nil {
		return q, fmt.Errorf("%w: a file is at %s again; move it away first", ErrBadInput, q.Path)
	}
	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return q, err
	}
	in, err := os.Open(filepath.Join(m.quarantineDir(), qid, "file"))
	if err != nil {
		return q, err
	}
	defer in.Close()
	out, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(q.Mode)&0o777)
	if err != nil {
		return q, err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = root.Remove(rel)
		return q, err
	}
	if os.Geteuid() == 0 {
		_ = root.Lchown(rel, q.UID, q.GID)
	}
	_ = root.Chmod(rel, fs.FileMode(q.Mode)&0o777)
	return q, os.RemoveAll(filepath.Join(m.quarantineDir(), qid))
}

// QuarantineDelete removes a file from the quarantine for good.
func (m *Manager) QuarantineDelete(qid string) (QFile, error) {
	q, err := m.qRead(qid)
	if err != nil {
		return q, err
	}
	return q, os.RemoveAll(filepath.Join(m.quarantineDir(), qid))
}
