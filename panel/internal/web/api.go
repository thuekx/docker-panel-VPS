package web

// API-01: a small JSON API under /api/v1 for scripts and CI, signed in with an API key (Authorization: Bearer).
// Keys are made on the API keys page, shown once and kept only as a sha256. Every key can read; "operate" adds
// start/stop/restart, "deploy" adds a Git deploy and publishing a static release. A key can be limited to one
// website and to a list of addresses, and it expires. Every call that changes something is in the audit log with
// the key's name, never the key.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
	"github.com/thuekx/docker-panel-VPS/panel/internal/auth"
	"github.com/thuekx/docker-panel-VPS/panel/internal/store"
)

const (
	apiKeyPrefix   = "npk_"
	apiMaxAllow    = 10
	apiPerMinute   = 120 // calls per key
	apiBadPerMin   = 10  // calls without a valid key per address before 429
	apiMaxNameLen  = 40
	apiReleaseBody = 512<<20 + 1
)

// APIRights are the rights a key can hold besides read.
var APIRights = []string{"operate", "deploy"}

// apiExpiry are the offered lifetimes in days (0 = does not expire).
var apiExpiry = []int{30, 90, 365, 0}

func newAPISecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return apiKeyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// apiAllowed says whether ip is in the key's list (an empty list allows every address).
func apiAllowed(list []string, ip string) bool {
	if len(list) == 0 {
		return true
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, x := range list {
		if p, err := netip.ParsePrefix(x); err == nil && p.Contains(a) {
			return true
		}
		if b, err := netip.ParseAddr(x); err == nil && b.Unmap() == a {
			return true
		}
	}
	return false
}

// parseAllowList checks a list of addresses or networks for a key.
func parseAllowList(in string) ([]string, error) {
	var out []string
	for _, f := range splitRe.Split(strings.TrimSpace(in), -1) {
		if f == "" {
			continue
		}
		if p, err := netip.ParsePrefix(f); err == nil {
			if p.Bits() == 0 {
				return nil, errors.New(f)
			}
			out = append(out, p.Masked().String())
		} else if a, err := netip.ParseAddr(f); err == nil {
			out = append(out, a.Unmap().String())
		} else {
			return nil, errors.New(f)
		}
	}
	if len(out) > apiMaxAllow {
		return nil, errors.New(strconv.Itoa(len(out)))
	}
	return out, nil
}

// ---------- the API keys page

type apiKeysData struct {
	Keys     []apiKeyRow
	Sites    []agent.Site
	Rights   []string
	Expiry   []int
	NewKey   string // shown once, right after it is made
	NewName  string
	Max      int
	Active   int
	Base     string
	Examples bool
}

type apiKeyRow struct {
	store.APIKey
	State                    string // active | expired | revoked
	CreatedAt, ExpiresAt, At string
}

func (s *Server) apiKeysPage(w http.ResponseWriter, r *http.Request) {
	if fk, ok := s.freshKeys.take(sessOf(r).ID, s.now()); ok {
		s.renderAPIKeys(w, r, http.StatusOK, "", fk.secret, fk.name)
		return
	}
	s.renderAPIKeys(w, r, http.StatusOK, "", "", "")
}

// freshKey is a key just made, held until the page shows it once.
type freshKey struct {
	secret, name string
	until        time.Time
}

type freshKeys struct {
	mu sync.Mutex
	m  map[string]freshKey // by session
}

func (f *freshKeys) put(sess string, k freshKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]freshKey{}
	}
	f.m[sess] = k
}

func (f *freshKeys) take(sess string, now time.Time) (freshKey, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.m[sess]
	delete(f.m, sess)
	for id, x := range f.m { // drop keys nobody came back for
		if now.After(x.until) {
			delete(f.m, id)
		}
	}
	return k, ok && now.Before(k.until)
}

func (s *Server) renderAPIKeys(w http.ResponseWriter, r *http.Request, code int, errMsg, newKey, newName string) {
	u, l := userOf(r), s.lang(r)
	keys, _ := s.st.APIKeys(u.OrgID)
	now := s.now()
	d := apiKeysData{Rights: APIRights, Expiry: apiExpiry, NewKey: newKey, NewName: newName, Max: store.MaxAPIKeys}
	d.Active, _ = s.st.ActiveAPIKeys(u.OrgID, now)
	f := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.In(s.loc()).Format("02 Jan 2006 15:04")
	}
	for _, k := range keys {
		row := apiKeyRow{APIKey: k, CreatedAt: f(k.Created), ExpiresAt: f(k.Expires), At: f(k.LastUsed), State: "active"}
		switch {
		case !k.Revoked.IsZero():
			row.State = "revoked"
		case !k.Active(now):
			row.State = "expired"
		}
		d.Keys = append(d.Keys, row)
	}
	if s.ag != nil {
		ctx, cancel := s.agentCtx(r, 5*time.Second)
		d.Sites, _ = s.ag.Sites(ctx)
		cancel()
	}
	d.Base = "https://" + r.Host + "/api/v1"
	p := page{Title: s.tr.T(l, "api.title"), Active: "settings", Data: d, Flash: r.URL.Query().Get("done"), Error: errMsg}
	w.Header().Set("Cache-Control", "no-store")
	if code != http.StatusOK {
		p.Flash = ""
		w.WriteHeader(code)
	}
	s.render(w, r, "apikeys", p)
}

func (s *Server) apiKeysAction(w http.ResponseWriter, r *http.Request) {
	u, l := userOf(r), s.lang(r)
	switch r.PostForm.Get("op") {
	case "create":
		if !s.ipLimit.Allow("pw:" + s.clientIP(r)) {
			s.renderAPIKeys(w, r, http.StatusTooManyRequests, s.tr.T(l, "err.toomany"), "", "")
			return
		}
		if !auth.CheckPassword(u.PwHash, r.PostForm.Get("password")) {
			s.audit1(u, r, "api.key.create", "refused: wrong password")
			s.renderAPIKeys(w, r, http.StatusForbidden, s.tr.T(l, "api.err.password"), "", "")
			return
		}
		k, err := s.apiKeyFromForm(r, u)
		if err != nil {
			s.renderAPIKeys(w, r, http.StatusBadRequest, err.Error(), "", "")
			return
		}
		if n, _ := s.st.ActiveAPIKeys(u.OrgID, s.now()); n >= store.MaxAPIKeys {
			s.renderAPIKeys(w, r, http.StatusBadRequest, s.tr.T(l, "api.err.max", store.MaxAPIKeys), "", "")
			return
		}
		secret, err := newAPISecret()
		if err != nil {
			http.Error(w, "internal error", 500)
			return
		}
		k.Prefix = secret[:len(apiKeyPrefix)+6]
		if err := s.st.CreateAPIKey(&k, auth.TokenHash(secret)); err != nil {
			http.Error(w, "internal error", 500)
			return
		}
		s.audit1(u, r, "api.key.create", apiKeyNote(k))
		// the key waits in memory for this session's next page view only (2 minutes), so a reload of the
		// answer neither shows it again nor makes a second key
		s.freshKeys.put(sessOf(r).ID, freshKey{secret: secret, name: k.Name, until: s.now().Add(2 * time.Minute)})
		http.Redirect(w, r, "/settings/api#newkey", http.StatusSeeOther)
	case "revoke":
		id, err := strconv.ParseInt(r.PostForm.Get("id"), 10, 64)
		if err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		k, err := s.st.RevokeAPIKey(u.OrgID, id, s.now())
		if err != nil {
			s.notFound(w, r)
			return
		}
		s.audit1(u, r, "api.key.revoke", k.Name+" ("+k.Prefix+"…)")
		http.Redirect(w, r, "/settings/api?done=revoked", http.StatusSeeOther)
	default:
		http.Error(w, "bad request", 400)
	}
}

func apiKeyNote(k store.APIKey) string {
	n := k.Name + " (" + k.Prefix + "…) rights=" + strings.Join(k.Rights, ",")
	if k.Site != "" {
		n += " site=" + k.Site
	}
	if len(k.AllowIPs) > 0 {
		n += " from=" + strings.Join(k.AllowIPs, ",")
	}
	if !k.Expires.IsZero() {
		n += " expires=" + k.Expires.UTC().Format("2006-01-02")
	}
	return n
}

func (s *Server) apiKeyFromForm(r *http.Request, u *store.User) (store.APIKey, error) {
	l, f := s.lang(r), r.PostForm
	now := s.now()
	k := store.APIKey{OrgID: u.OrgID, UserID: u.ID, Created: now, Rights: []string{"read"}}
	k.Name = strings.TrimSpace(f.Get("name"))
	if k.Name == "" || len(k.Name) > apiMaxNameLen || strings.ContainsAny(k.Name, "<>\"'`\\\r\n\t") {
		return k, errors.New(s.tr.T(l, "api.err.name", apiMaxNameLen))
	}
	for _, x := range APIRights {
		if f.Get("right_"+x) == "on" {
			k.Rights = append(k.Rights, x)
		}
	}
	if site := f.Get("site"); site != "" {
		if !siteRe.MatchString(site) {
			return k, errors.New(s.tr.T(l, "api.err.site"))
		}
		ctx, cancel := s.agentCtx(r, 5*time.Second)
		_, err := s.ag.Site(ctx, site)
		cancel()
		if err != nil {
			return k, errors.New(s.tr.T(l, "api.err.site"))
		}
		k.Site = site
	}
	allow, err := parseAllowList(f.Get("allow"))
	if err != nil {
		return k, errors.New(s.tr.T(l, "api.err.allow", apiMaxAllow))
	}
	k.AllowIPs = allow
	days, err := strconv.Atoi(f.Get("expires"))
	ok := err == nil
	if ok {
		ok = false
		for _, d := range apiExpiry {
			ok = ok || d == days
		}
	}
	if !ok {
		return k, errors.New(s.tr.T(l, "api.err.expires"))
	}
	if days > 0 {
		k.Expires = now.Add(time.Duration(days) * 24 * time.Hour)
	}
	return k, nil
}

// ---------- the API itself

type apiCall struct {
	key  store.APIKey
	user *store.User
	ip   string
}

type apiHandler func(w http.ResponseWriter, r *http.Request, c apiCall)

func apiJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func apiErr(w http.ResponseWriter, code int, msg string) {
	apiJSON(w, code, map[string]string{"error": msg})
}

// api signs a call in with its key and checks the right it needs ("read", "operate" or "deploy").
func (s *Server) api(right string, h apiHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := s.clientIP(r)
		if s.apiBad.Blocked(ip) {
			apiErr(w, http.StatusTooManyRequests, "too many calls without a valid key; wait a minute")
			return
		}
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		tok = strings.TrimSpace(tok)
		if !ok || !strings.HasPrefix(tok, apiKeyPrefix) || len(tok) != len(apiKeyPrefix)+43 {
			s.apiBad.Allow(ip)
			w.Header().Set("WWW-Authenticate", `Bearer realm="nostek-panel"`)
			apiErr(w, http.StatusUnauthorized, "an API key is needed: Authorization: Bearer npk_…")
			return
		}
		hash := auth.TokenHash(tok)
		k, err := s.st.APIKeyByHash(hash)
		if err != nil || subtle.ConstantTimeCompare([]byte(k.Prefix), []byte(tok[:len(k.Prefix)])) != 1 || !k.Active(s.now()) {
			s.apiBad.Allow(ip)
			w.Header().Set("WWW-Authenticate", `Bearer realm="nostek-panel", error="invalid_token"`)
			apiErr(w, http.StatusUnauthorized, "this API key is not valid (wrong, revoked or expired)")
			return
		}
		if !apiAllowed(k.AllowIPs, ip) {
			apiErr(w, http.StatusForbidden, "this API key may not be used from "+ip)
			return
		}
		if !s.apiLimit.Allow(strconv.FormatInt(k.ID, 10)) {
			apiErr(w, http.StatusTooManyRequests, "at most "+strconv.Itoa(apiPerMinute)+" calls per minute per key")
			return
		}
		if !k.Has(right) {
			apiErr(w, http.StatusForbidden, "this API key has no "+right+" right")
			return
		}
		u, err := s.st.UserByID(k.UserID)
		if err != nil || u.OrgID != k.OrgID {
			apiErr(w, http.StatusUnauthorized, "this API key is not valid")
			return
		}
		_ = s.st.TouchAPIKey(k.ID, s.now(), ip)
		h(w, r, apiCall{key: k, user: u, ip: ip})
	}
}

func (s *Server) apiAudit(c apiCall, action, detail string) {
	_ = s.st.Audit(c.user.OrgID, c.user.ID, c.user.Email, action, detail+" (API key "+c.key.Name+")", c.ip)
}

// apiSite finds the website of the call; a key for one website sees no other (404, like one that does not exist).
func (s *Server) apiSite(w http.ResponseWriter, r *http.Request, c apiCall) (agent.Site, bool) {
	id := r.PathValue("id")
	if !siteRe.MatchString(id) || (c.key.Site != "" && c.key.Site != id) {
		apiErr(w, http.StatusNotFound, "no such website")
		return agent.Site{}, false
	}
	ctx, cancel := s.agentCtx(r, 10*time.Second)
	defer cancel()
	site, err := s.ag.Site(ctx, id)
	if err != nil {
		apiErr(w, http.StatusNotFound, "no such website")
		return site, false
	}
	return site, true
}

type apiSiteView struct {
	ID      string   `json:"id"`
	Domain  string   `json:"domain"`
	Names   []string `json:"names"`
	URL     string   `json:"url"`
	App     string   `json:"app"`
	Engine  string   `json:"engine"`
	Status  string   `json:"status"`
	Up      int      `json:"containers_up"`
	Total   int      `json:"containers_total"`
	Release int      `json:"release,omitempty"`
	Created int64    `json:"created"`
}

func apiSiteOf(x agent.Site) apiSiteView {
	v := apiSiteView{ID: x.ID, Domain: x.Domain, URL: x.URL, App: x.AppName(), Engine: x.Engine, Status: x.Status,
		Up: x.Up, Total: x.Total, Release: x.Release, Created: x.Created}
	if v.Engine == "" {
		v.Engine = "docker"
	}
	if x.App != "" && x.AppName() != x.App {
		v.App = x.App
	}
	for _, h := range x.Hosts {
		v.Names = append(v.Names, h.Name)
	}
	return v
}

func (s *Server) apiServer(w http.ResponseWriter, r *http.Request, c apiCall) {
	snap := s.sys.Read(r.Context())
	out := map[string]any{
		"version": s.cfg.Version, "hostname": snap.Hostname, "time_zone": s.loc().String(),
		"memory_mb":   map[string]any{"total": snap.MemTotalMB, "used": snap.MemUsedMB, "status": snap.MemStatus},
		"disk_gb":     map[string]any{"total": snap.DiskTotalGB, "used": snap.DiskUsedGB, "status": snap.DiskStatus},
		"load":        map[string]any{"load1": snap.Load1, "cpus": snap.CPUs, "status": snap.LoadStatus},
		"uptime_h":    snap.UptimeHours,
		"containers":  map[string]any{"running": snap.ContainersRunning, "total": snap.ContainersTotal},
		"backup":      map[string]any{"status": snap.BackupStatus, "last": unixOrNil(snap.BackupLast)},
		"key":         map[string]any{"name": c.key.Name, "rights": c.key.Rights, "site": c.key.Site},
		"api_version": 1,
	}
	apiJSON(w, http.StatusOK, out)
}

func unixOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

func (s *Server) apiSites(w http.ResponseWriter, r *http.Request, c apiCall) {
	ctx, cancel := s.agentCtx(r, 10*time.Second)
	defer cancel()
	sites, err := s.ag.Sites(ctx)
	if err != nil {
		apiErr(w, http.StatusBadGateway, "the agent does not answer")
		return
	}
	out := []apiSiteView{}
	for _, x := range sites {
		if c.key.Site == "" || c.key.Site == x.ID {
			out = append(out, apiSiteOf(x))
		}
	}
	apiJSON(w, http.StatusOK, map[string]any{"sites": out})
}

func (s *Server) apiSiteShow(w http.ResponseWriter, r *http.Request, c apiCall) {
	site, ok := s.apiSite(w, r, c)
	if !ok {
		return
	}
	v := map[string]any{"site": apiSiteOf(site)}
	if site.IsStatic() {
		ctx, cancel := s.agentCtx(r, 10*time.Second)
		if rels, err := s.ag.Releases(ctx, site.ID); err == nil {
			v["releases"] = rels
		}
		cancel()
	}
	apiJSON(w, http.StatusOK, v)
}

func (s *Server) apiSiteAction(act string) apiHandler {
	return func(w http.ResponseWriter, r *http.Request, c apiCall) {
		site, ok := s.apiSite(w, r, c)
		if !ok {
			return
		}
		ctx, cancel := s.agentCtx(r, 6*time.Minute)
		defer cancel()
		if err := s.ag.Action(ctx, site.ID, act); err != nil {
			s.apiAudit(c, "site."+act, site.ID+" failed: "+err.Error())
			apiErr(w, http.StatusConflict, err.Error())
			return
		}
		s.apiAudit(c, "site."+act, site.ID+" "+site.Domain)
		apiJSON(w, http.StatusOK, map[string]any{"ok": true, "site": site.ID, "action": act})
	}
}

func (s *Server) apiDeploy(w http.ResponseWriter, r *http.Request, c apiCall) {
	site, ok := s.apiSite(w, r, c)
	if !ok {
		return
	}
	if !site.IsNode() || site.FromFiles() {
		apiErr(w, http.StatusBadRequest, "only a Node.js, Go or Python website built from Git deploys this way")
		return
	}
	ctx, cancel := s.agentCtx(r, time.Minute)
	defer cancel()
	job, err := s.ag.StartDeploy(ctx, site.ID)
	if err != nil {
		s.apiAudit(c, "site.deploy", site.ID+" failed: "+err.Error())
		apiErr(w, http.StatusConflict, err.Error())
		return
	}
	s.owners.set(job, c.user.ID)
	s.apiAudit(c, "site.deploy", site.ID+" "+site.Domain)
	apiJSON(w, http.StatusAccepted, map[string]any{"job": job, "status_url": "/api/v1/jobs/" + job})
}

// apiRelease publishes the request body (a .zip) as a new release of a static website, streamed to the agent.
func (s *Server) apiRelease(w http.ResponseWriter, r *http.Request, c apiCall) {
	site, ok := s.apiSite(w, r, c)
	if !ok {
		return
	}
	if !site.IsStatic() {
		apiErr(w, http.StatusBadRequest, "only a static website takes a release")
		return
	}
	name := path.Base(strings.ReplaceAll(r.URL.Query().Get("name"), `\`, "/"))
	if name == "." || name == "/" || name == "" {
		name = "release.zip"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".zip") {
		apiErr(w, http.StatusBadRequest, "name must end in .zip")
		return
	}
	long(w, 2*time.Hour)
	r.Body = http.MaxBytesReader(w, r.Body, apiReleaseBody)
	ctx, cancel := s.agentCtx(r, 2*time.Hour)
	defer cancel()
	rel, err := s.ag.PublishStatic(ctx, site.ID, name, r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		msg := err.Error()
		code := http.StatusBadRequest
		if errors.As(err, &mbe) {
			msg, code = "the zip is larger than 512 MB", http.StatusRequestEntityTooLarge
		}
		s.apiAudit(c, "site.static-publish", site.ID+" failed: "+msg)
		apiErr(w, code, msg)
		return
	}
	s.apiAudit(c, "site.static-publish", site.ID+" release "+strconv.Itoa(rel.Release)+" "+rel.Name+" ("+strconv.Itoa(rel.Files)+" files)")
	apiJSON(w, http.StatusCreated, map[string]any{"release": rel})
}

func (s *Server) apiJob(w http.ResponseWriter, r *http.Request, c apiCall) {
	id := r.PathValue("job")
	if !jobRe.MatchString(id) {
		apiErr(w, http.StatusNotFound, "no such job")
		return
	}
	// a key sees only the jobs of its own user, and of its own website
	if uid, ok := s.owners.owner(id); !ok || uid != c.user.ID {
		apiErr(w, http.StatusNotFound, "no such job")
		return
	}
	ctx, cancel := s.agentCtx(r, 10*time.Second)
	defer cancel()
	j, err := s.ag.Job(ctx, id)
	if err != nil || (c.key.Site != "" && j.Site != c.key.Site) {
		apiErr(w, http.StatusNotFound, "no such job")
		return
	}
	apiJSON(w, http.StatusOK, map[string]any{"id": j.ID, "kind": j.Kind, "site": j.Site, "state": j.State,
		"steps": j.Steps, "error": j.Error, "progress": j.Progress})
}
