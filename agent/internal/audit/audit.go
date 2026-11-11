// Package audit writes one JSON line per agent call. It never records a secret: callers pass only ids,
// actions and results.
package audit

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Entry is one audit line.
type Entry struct {
	Time   string `json:"time"`
	Peer   int    `json:"peer_uid"`
	Action string `json:"action"`
	Site   string `json:"site,omitempty"`
	Result string `json:"result"` // ok | refused | failed
	Detail string `json:"detail,omitempty"`
}

// Log appends entries to a file (0600).
type Log struct {
	mu   sync.Mutex
	path string
}

// Open returns a log writing to path.
func Open(path string) *Log { return &Log{path: path} }

// Write appends one entry; errors go to stderr so a full disk never blocks the agent silently.
func (l *Log) Write(e Entry) {
	if l == nil || l.path == "" {
		return
	}
	e.Time = time.Now().UTC().Format(time.RFC3339)
	if len(e.Detail) > 300 {
		e.Detail = e.Detail[:300]
	}
	b, _ := json.Marshal(e)
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Stderr.WriteString("audit: " + err.Error() + "\n")
		return
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		os.Stderr.WriteString("audit: " + err.Error() + "\n")
	}
}
