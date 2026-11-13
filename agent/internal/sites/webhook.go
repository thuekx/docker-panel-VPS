package sites

// Auto-deploy webhook (APP-02c). A Node.js, Go or Python website built from Git can deploy on every push to its
// branch. The panel receives the delivery on its public address and passes the raw body and the signature headers
// here; the agent alone keeps the secret (deploy/webhook_secret, 0600, root only, never mounted) and checks it:
// GitHub, Gitea and Forgejo sign the body with HMAC-SHA256 (X-Hub-Signature-256), GitLab sends the secret as
// X-Gitlab-Token. Only a push to the website's own branch starts a deploy; a deploy asked while another job runs
// waits and starts after it, at most one per website (it builds the newest commit anyway).

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	MaxWebhookBody = 1 << 20          // larger deliveries are refused by the panel and here
	WebhookWait    = 20 * time.Minute // how long a deploy asked by a push waits for a running job
	webhookKeep    = 10               // deliveries kept for the panel
)

// Errors of a delivery. Both answer the same to the caller, so a delivery cannot learn which websites listen.
var (
	ErrNoWebhook = errors.New("auto-deploy is off for this website")
	ErrSignature = errors.New("the delivery is not signed with this website's secret")
)

// Hook is one delivery as the panel received it.
type Hook struct {
	Event     string `json:"event"`               // X-GitHub-Event, X-Gitea-Event or X-Gitlab-Event
	Signature string `json:"signature,omitempty"` // X-Hub-Signature-256: sha256=<hex>
	Token     string `json:"token,omitempty"`     // X-Gitlab-Token
	Body      []byte `json:"body"`
}

// Delivery is what the agent did with one delivery.
type Delivery struct {
	At     int64  `json:"at"`
	Event  string `json:"event"`
	Result string `json:"result"` // ping | deploy | queued | ignored
	Detail string `json:"detail,omitempty"`
	Commit string `json:"commit,omitempty"`
	Job    string `json:"job,omitempty"`
}

// Webhook is the auto-deploy state of a website, as the panel shows it.
type Webhook struct {
	On         bool       `json:"on"`
	Branch     string     `json:"branch"`
	Deliveries []Delivery `json:"deliveries"` // newest first
}

func (m *Manager) webhookSecretPath(id string) string {
	return filepath.Join(m.deployDir(id), "webhook_secret")
}
func (m *Manager) webhookLogPath(id string) string {
	return filepath.Join(m.deployDir(id), "webhook.json")
}

// hookSite is a website that can deploy on a push: a runtime app built from Git.
func (m *Manager) hookSite(id string) (Site, error) {
	s, err := m.Get(id)
	if err != nil {
		return Site{}, err
	}
	if !s.IsRuntime() || s.FromFiles() {
		return Site{}, fmt.Errorf("%w: auto-deploy needs a website built from a Git repository", ErrBadInput)
	}
	return s, nil
}

// NewWebhookSecret turns auto-deploy on with a new secret, or replaces the secret; the old one stops working at
// once. The secret is returned only here, to be pasted into the repository's webhook settings.
func (m *Manager) NewWebhookSecret(id string) (string, error) {
	if _, err := m.hookSite(id); err != nil {
		return "", err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	sec := hex.EncodeToString(b)
	if err := os.MkdirAll(m.deployDir(id), 0o755); err != nil {
		return "", err
	}
	if err := writeFile(m.webhookSecretPath(id), []byte(sec), 0o600); err != nil {
		return "", err
	}
	return sec, nil
}

// RemoveWebhook turns auto-deploy off; deliveries are refused from then on.
func (m *Manager) RemoveWebhook(id string) error {
	if _, err := m.Get(id); err != nil {
		return err
	}
	if err := os.Remove(m.webhookSecretPath(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// WebhookInfo returns whether auto-deploy is on and the last deliveries.
func (m *Manager) WebhookInfo(id string) (Webhook, error) {
	s, err := m.hookSite(id)
	if err != nil {
		return Webhook{}, err
	}
	w := Webhook{On: fileExists(m.webhookSecretPath(id)), Branch: s.Branch, Deliveries: []Delivery{}}
	if b, err := os.ReadFile(m.webhookLogPath(id)); err == nil {
		_ = json.Unmarshal(b, &w.Deliveries)
	}
	return w, nil
}

// pushEvent is the part of a push payload the agent reads; GitHub, Gitea and GitLab share these names.
type pushEvent struct {
	Ref     string `json:"ref"`
	After   string `json:"after"`
	Deleted bool   `json:"deleted"`
}

var zeroCommit = strings.Repeat("0", 40)

// Deliver checks a delivery and deploys on a push to the website's branch.
func (m *Manager) Deliver(id string, h Hook) (Delivery, error) {
	s, err := m.hookSite(id)
	if err != nil {
		return Delivery{}, ErrNoWebhook
	}
	sec, err := os.ReadFile(m.webhookSecretPath(id))
	if err != nil || len(sec) == 0 {
		return Delivery{}, ErrNoWebhook
	}
	if len(h.Body) > MaxWebhookBody {
		return Delivery{}, ErrTooBig
	}
	if !signed(sec, h) {
		return Delivery{}, ErrSignature
	}
	d := Delivery{At: m.now().Unix(), Event: cut(h.Event)}
	switch strings.ToLower(h.Event) {
	case "ping": // GitHub and Gitea send one when the webhook is added
		d.Result, d.Detail = "ping", "webhook reached the server"
	case "push", "push hook":
		var p pushEvent
		if err := json.Unmarshal(h.Body, &p); err != nil {
			return Delivery{}, fmt.Errorf("%w: the push payload is not JSON", ErrBadInput)
		}
		d.Commit = p.After
		if !commitRe.MatchString(d.Commit) {
			d.Commit = ""
		}
		switch {
		case p.Ref != "refs/heads/"+s.Branch:
			d.Result, d.Detail = "ignored", "push to "+cut(strings.TrimPrefix(p.Ref, "refs/heads/"))+", not to "+s.Branch
		case p.Deleted || p.After == zeroCommit:
			d.Result, d.Detail = "ignored", "the branch was deleted"
		default:
			d.Result, d.Job, d.Detail = m.hookDeploy(id)
		}
	default:
		d.Result, d.Detail = "ignored", "only push events deploy"
	}
	m.logDelivery(id, d)
	return d, nil
}

// signed checks the signature in constant time: HMAC-SHA256 of the body, or the GitLab token.
func signed(sec []byte, h Hook) bool {
	if sig, ok := strings.CutPrefix(h.Signature, "sha256="); ok {
		got, err := hex.DecodeString(sig)
		if err != nil {
			return false
		}
		mac := hmac.New(sha256.New, sec)
		mac.Write(h.Body)
		return hmac.Equal(got, mac.Sum(nil))
	}
	if h.Signature == "" && h.Token != "" {
		return subtle.ConstantTimeCompare([]byte(h.Token), sec) == 1
	}
	return false
}

// hookDeploy starts a deploy now, or lets one wait for the running job.
func (m *Manager) hookDeploy(id string) (result, job, detail string) {
	m.hookMu.Lock()
	defer m.hookMu.Unlock()
	if m.hookWait[id] {
		return "queued", "", "a deploy already waits for the running job"
	}
	job, err := m.startDeploy(id, "webhook")
	if err == nil {
		return "deploy", job, ""
	}
	if !errors.Is(err, ErrBusy) {
		return "ignored", "", cut(strings.TrimPrefix(err.Error(), ErrBadInput.Error()+": "))
	}
	m.hookWait[id] = true
	go func() {
		defer func() {
			m.hookMu.Lock()
			delete(m.hookWait, id)
			m.hookMu.Unlock()
		}()
		for end := time.Now().Add(WebhookWait); time.Now().Before(end); {
			time.Sleep(m.hookRetry)
			job, err := m.startDeploy(id, "webhook")
			if err == nil {
				m.logDelivery(id, Delivery{At: m.now().Unix(), Event: "push", Result: "deploy", Detail: "waited for the running job", Job: job})
				return
			}
			if !errors.Is(err, ErrBusy) {
				return
			}
		}
		m.logDelivery(id, Delivery{At: m.now().Unix(), Event: "push", Result: "ignored", Detail: "another job ran for too long"})
	}()
	return "queued", "", "another job is running; the deploy starts after it"
}

var hookLogMu sync.Mutex

func (m *Manager) logDelivery(id string, d Delivery) {
	hookLogMu.Lock()
	defer hookLogMu.Unlock()
	var l []Delivery
	if b, err := os.ReadFile(m.webhookLogPath(id)); err == nil {
		_ = json.Unmarshal(b, &l)
	}
	l = append([]Delivery{d}, l...)
	if len(l) > webhookKeep {
		l = l[:webhookKeep]
	}
	if b, err := json.Marshal(l); err == nil {
		_ = writeFile(m.webhookLogPath(id), b, 0o600)
	}
}
