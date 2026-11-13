package sites

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

func sign(sec, body string) string {
	mac := hmac.New(sha256.New, []byte(sec))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func push(ref string) string {
	return `{"ref":"` + ref + `","after":"` + commitA + `","deleted":false,"repository":{"full_name":"owner/shop"}}`
}

func TestWebhookDeploy(t *testing.T) {
	m, f, s, _ := nodeSite(t, okBuild, nil)
	m.hookRetry = 10 * time.Millisecond
	main := push("refs/heads/main")
	if _, err := m.Deliver(s.ID, Hook{Event: "push", Signature: sign("x", main), Body: []byte(main)}); !errors.Is(err, ErrNoWebhook) {
		t.Fatalf("delivery while off: %v", err)
	}
	sec, err := m.NewWebhookSecret(s.ID)
	if err != nil || len(sec) != 64 {
		t.Fatalf("secret %q %v", sec, err)
	}
	if st, _ := os.Stat(m.webhookSecretPath(s.ID)); st == nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("secret file %v", st)
	}
	if b, _ := os.ReadFile(m.dir(s.ID) + "/compose.yml"); strings.Contains(string(b), "webhook") {
		t.Error("secret mounted into the app")
	}
	// wrong or missing signatures
	for _, h := range []Hook{
		{Event: "push", Signature: sign("wrong", main), Body: []byte(main)},
		{Event: "push", Signature: sign(sec, main+" "), Body: []byte(main)},
		{Event: "push", Signature: "sha256=zz", Body: []byte(main)},
		{Event: "push", Signature: sec, Body: []byte(main)},
		{Event: "Push Hook", Token: "nope", Body: []byte(main)},
		{Event: "push", Body: []byte(main)},
		{Event: "push", Signature: sign("wrong", main), Token: sec, Body: []byte(main)},
	} {
		if _, err := m.Deliver(s.ID, h); !errors.Is(err, ErrSignature) {
			t.Errorf("accepted %+v: %v", h, err)
		}
	}
	builds := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, c := range f.calls {
			if len(c) > 0 && c[0] == "run" && strings.Contains(strings.Join(c, " "), "np-build-") {
				n++
			}
		}
		return n
	}
	before := builds()
	// ping, another branch, a deleted branch, another event: nothing is built
	for _, c := range []struct {
		h    Hook
		want string
	}{
		{Hook{Event: "ping", Body: []byte(`{"zen":"hi"}`)}, "ping"},
		{Hook{Event: "push", Body: []byte(push("refs/heads/dev"))}, "ignored"},
		{Hook{Event: "push", Body: []byte(push("refs/tags/main"))}, "ignored"},
		{Hook{Event: "push", Body: []byte(`{"ref":"refs/heads/main","after":"` + zeroCommit + `"}`)}, "ignored"},
		{Hook{Event: "push", Body: []byte(`{"ref":"refs/heads/main","deleted":true}`)}, "ignored"},
		{Hook{Event: "issues", Body: []byte(main)}, "ignored"},
	} {
		c.h.Signature = sign(sec, string(c.h.Body))
		d, err := m.Deliver(s.ID, c.h)
		if err != nil || d.Result != c.want || d.Job != "" {
			t.Errorf("%s %s: %+v %v", c.h.Event, c.h.Body, d, err)
		}
	}
	if builds() != before {
		t.Fatal("built without a push to the branch")
	}
	// a signed push to the branch deploys, marked as started by the webhook
	d, err := m.Deliver(s.ID, Hook{Event: "push", Signature: sign(sec, main), Body: []byte(main)})
	if err != nil || d.Result != "deploy" || d.Job == "" || d.Commit != commitA {
		t.Fatalf("push %+v %v", d, err)
	}
	if v := waitJob(t, m, d.Job); v.State != "done" {
		t.Fatalf("job %+v", v)
	}
	h, _ := m.History(s.ID)
	if len(h) != 2 || h[0].Trigger != "webhook" || h[1].Trigger != "" {
		t.Fatalf("history %+v", h)
	}
	// GitLab sends the secret as a token
	gl := push("refs/heads/main")
	d, err = m.Deliver(s.ID, Hook{Event: "Push Hook", Token: sec, Body: []byte(gl)})
	if err != nil || d.Result != "deploy" {
		t.Fatalf("gitlab %+v %v", d, err)
	}
	waitJob(t, m, d.Job)
	// a push while another job runs waits for it, once
	m.mu.Lock()
	d1, _ := m.Deliver(s.ID, Hook{Event: "push", Signature: sign(sec, main), Body: []byte(main)})
	d2, _ := m.Deliver(s.ID, Hook{Event: "push", Signature: sign(sec, main), Body: []byte(main)})
	if d1.Result != "queued" || d2.Result != "queued" || d1.Job != "" {
		t.Fatalf("busy %+v %+v", d1, d2)
	}
	n := builds()
	m.mu.Unlock()
	for i := 0; i < 300 && builds() == n; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	for i := 0; i < 300; i++ {
		if w, _ := m.WebhookInfo(s.ID); len(w.Deliveries) > 0 && w.Deliveries[0].Result == "deploy" && w.Deliveries[0].Job != "" {
			waitJob(t, m, w.Deliveries[0].Job)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if got := builds(); got != n+1 {
		t.Fatalf("queued pushes built %d times", got-n)
	}
	w, err := m.WebhookInfo(s.ID)
	if err != nil || !w.On || w.Branch != "main" || len(w.Deliveries) != webhookKeep || w.Deliveries[0].Detail != "waited for the running job" {
		t.Fatalf("info %+v %v", w, err)
	}
	// a new secret replaces the old one; off refuses everything
	sec2, _ := m.NewWebhookSecret(s.ID)
	if _, err := m.Deliver(s.ID, Hook{Event: "ping", Signature: sign(sec, "{}"), Body: []byte("{}")}); !errors.Is(err, ErrSignature) || sec2 == sec {
		t.Errorf("old secret still works: %v", err)
	}
	if err := m.RemoveWebhook(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Deliver(s.ID, Hook{Event: "ping", Signature: sign(sec2, "{}"), Body: []byte("{}")}); !errors.Is(err, ErrNoWebhook) {
		t.Errorf("delivery after off: %v", err)
	}
	if w, _ := m.WebhookInfo(s.ID); w.On {
		t.Error("still on")
	}
	big := strings.Repeat("x", MaxWebhookBody+1)
	sec3, _ := m.NewWebhookSecret(s.ID)
	if _, err := m.Deliver(s.ID, Hook{Event: "push", Signature: sign(sec3, big), Body: []byte(big)}); !errors.Is(err, ErrTooBig) {
		t.Errorf("big body: %v", err)
	}
	if _, err := m.Deliver("nope", Hook{Event: "ping"}); !errors.Is(err, ErrNoWebhook) {
		t.Errorf("unknown site: %v", err)
	}
}

func TestWebhookOnlyForGitApps(t *testing.T) {
	m, _, s := appDBSite(t, spec.Create{App: "go", Domain: "voltix.id", Primary: "domain", Version: "1.27", Source: "files"})
	if _, err := m.NewWebhookSecret(s.ID); !errors.Is(err, ErrBadInput) {
		t.Errorf("own files: %v", err)
	}
	if _, err := m.WebhookInfo(s.ID); !errors.Is(err, ErrBadInput) {
		t.Errorf("info own files: %v", err)
	}
	wm, _, wid := readySite(t)
	if _, err := wm.NewWebhookSecret(wid); !errors.Is(err, ErrBadInput) {
		t.Errorf("WordPress: %v", err)
	}
}
