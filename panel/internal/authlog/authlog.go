// Package authlog writes one line per failed sign-in for fail2ban (SRV-01):
//
//	2026-11-26T15:00:00Z nostek-auth fail ip=203.0.113.9 service=panel
//
// Only a parsed address goes in, so a line can never carry text from the visitor. The file is emptied when it grows
// past MaxSize (fail2ban follows a truncated file).
package authlog

import (
	"net/netip"
	"os"
	"sync"
	"time"
)

// MaxSize is the size after which the file starts again.
const MaxSize = 1 << 20

// Log appends to one file. A nil Log or an empty path writes nothing.
type Log struct {
	mu      sync.Mutex
	path    string
	service string
	now     func() time.Time
}

// Open returns a log for service ("panel", "sftp") writing to path.
func Open(path, service string) *Log { return &Log{path: path, service: service, now: time.Now} }

// Fail records one failed sign-in from ip; anything that is not an address is dropped.
func (l *Log) Fail(ip string) {
	if l == nil || l.path == "" {
		return
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return
	}
	line := l.now().UTC().Format(time.RFC3339) + " nostek-auth fail ip=" + a.Unmap().String() + " service=" + l.service + "\n"
	l.mu.Lock()
	defer l.mu.Unlock()
	flag := os.O_APPEND | os.O_CREATE | os.O_WRONLY
	if fi, err := os.Stat(l.path); err == nil && fi.Size() > MaxSize {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(l.path, flag, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}
