package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/auth"
	"github.com/thuekx/docker-panel-VPS/panel/internal/store"
)

var apiKeyRe = regexp.MustCompile(`<code id="apikey">(npk_[A-Za-z0-9_-]{43})</code>`)

// newAPIKey makes a key on the API keys page and returns it.
func (e *env) newAPIKey(form url.Values) string {
	e.t.Helper()
	form.Set("op", "create")
	if form.Get("password") == "" {
		form.Set("password", pw)
	}
	if form.Get("expires") == "" {
		form.Set("expires", "90")
	}
	code, body, h := e.post(e.c, "/settings/api", form, "/settings/api")
	if code != 303 || h.Get("Location") != "/settings/api#newkey" {
		e.t.Fatalf("create key: %d %s", code, errText(body))
	}
	_, body, _ = e.get(e.c, "/settings/api")
	m := apiKeyRe.FindStringSubmatch(body)
	if m == nil {
		e.t.Fatal("the new key is not shown")
	}
	if _, again, _ := e.get(e.c, "/settings/api"); strings.Contains(again, m[1]) {
		e.t.Fatal("the new key is shown twice")
	}
	return m[1]
}

func errText(body string) string {
	if m := regexp.MustCompile(`class="err"[^>]*>([^<]*)<`).FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

func (e *env) apiCall(method, path, key string, body io.Reader) (int, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, body)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAPIKeysPage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		if code, body, _ := e.get(e.c, "/settings/api"); code != 200 || missingKey.MatchString(body) {
			t.Errorf("page (%s): %d %v", lang, code, missingKey.FindString(body))
		}
		if _, body, _ := e.get(e.c, "/settings"); !strings.Contains(body, `href="/settings/api"`) || missingKey.MatchString(body) {
			t.Errorf("settings (%s) has no link to the API keys", lang)
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	// the password is needed; bad input is refused
	if code, body, _ := e.post(e.c, "/settings/api", url.Values{"op": {"create"}, "name": {"ci"}, "expires": {"90"}, "password": {"wrong-password-1"}}, "/settings/api"); code != 403 || apiKeyRe.MatchString(body) {
		t.Errorf("wrong password: %d", code)
	}
	for _, bad := range []url.Values{
		{"name": {""}}, {"name": {`a"b`}}, {"name": {strings.Repeat("x", 41)}},
		{"name": {"ci"}, "site": {"no-such-site"}}, {"name": {"ci"}, "allow": {"0.0.0.0/0"}}, {"name": {"ci"}, "allow": {"example.com"}},
		{"name": {"ci"}, "expires": {"7"}},
	} {
		bad.Set("op", "create")
		bad.Set("password", pw)
		if bad.Get("expires") == "" {
			bad.Set("expires", "90")
		}
		if code, body, _ := e.post(e.c, "/settings/api", bad, "/settings/api"); code != 400 || apiKeyRe.MatchString(body) {
			t.Errorf("bad input %v: %d", bad, code)
		}
	}
	key := e.newAPIKey(url.Values{"name": {"ci-deploy"}, "right_deploy": {"on"}, "site": {"voltix-id"}, "allow": {"127.0.0.1, 10.0.0.0/8"}, "expires": {"30"}})
	// shown once: the list keeps only the prefix
	_, page, _ := e.get(e.c, "/settings/api")
	if strings.Contains(page, key) || !strings.Contains(page, key[:10]+"…") || !strings.Contains(page, "<code>voltix-id</code>") || !strings.Contains(page, "<code>10.0.0.0/8</code>") {
		t.Error("the list shows the key or misses its prefix, website or addresses")
	}
	var stored int
	e.st.DB.QueryRow(`SELECT count(*) FROM api_keys WHERE hash=? OR prefix=?`, key, key).Scan(&stored)
	if stored != 0 {
		t.Error("the key itself is stored")
	}
	// the audit log names the key, never the secret
	var audit string
	e.st.DB.QueryRow(`SELECT group_concat(action||' '||detail, '|') FROM audit WHERE action LIKE 'api.%'`).Scan(&audit)
	if !strings.Contains(audit, "api.key.create ci-deploy") || !strings.Contains(audit, "rights=read,deploy site=voltix-id from=127.0.0.1,10.0.0.0/8") || strings.Contains(audit, key) {
		t.Errorf("audit: %q", audit)
	}
	// revoke
	var id string
	e.st.DB.QueryRow(`SELECT id FROM api_keys WHERE name='ci-deploy'`).Scan(&id)
	if code, _, h := e.post(e.c, "/settings/api", url.Values{"op": {"revoke"}, "id": {id}}, "/settings/api"); code != 303 || h.Get("Location") != "/settings/api?done=revoked" {
		t.Errorf("revoke: %d %s", code, h.Get("Location"))
	}
	if code, _ := e.apiCall("GET", "/api/v1/sites", key, nil); code != 401 {
		t.Errorf("revoked key still works: %d", code)
	}
	if code, _, _ := e.post(e.c, "/settings/api", url.Values{"op": {"revoke"}, "id": {id}}, "/settings/api"); code != 404 {
		t.Errorf("revoke twice: %d", code)
	}
	if code, _ := e.post2(url.Values{"op": {"create"}, "name": {"x"}, "expires": {"90"}, "password": {pw}}); code != 403 {
		t.Errorf("create without CSRF token: %d", code)
	}
	_ = f
}

// post2 posts without a CSRF token.
func (e *env) post2(form url.Values) (int, string) {
	resp, err := e.c.PostForm(e.ts.URL+"/settings/api", form)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestAPICalls(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	read := e.newAPIKey(url.Values{"name": {"monitor"}})
	ops := e.newAPIKey(url.Values{"name": {"ops"}, "right_operate": {"on"}})
	dep := e.newAPIKey(url.Values{"name": {"ci"}, "right_deploy": {"on"}, "site": {"voltix-id"}})
	far := e.newAPIKey(url.Values{"name": {"office"}, "right_operate": {"on"}, "allow": {"203.0.113.0/24"}})

	// no key, a made-up key, a cookie session: 401
	if code, _ := e.apiCall("GET", "/api/v1/sites", "", nil); code != 401 {
		t.Errorf("no key: %d", code)
	}
	if code, _ := e.apiCall("GET", "/api/v1/sites", "npk_"+strings.Repeat("A", 43), nil); code != 401 {
		t.Errorf("made-up key: %d", code)
	}
	if resp, err := e.c.Get(e.ts.URL + "/api/v1/sites"); err != nil || resp.StatusCode != 401 {
		t.Errorf("the session cookie must not sign in to the API: %v", resp.StatusCode)
	}
	// read
	code, out := e.apiCall("GET", "/api/v1/sites", read, nil)
	if code != 200 || !strings.Contains(toJSON(out), `"id":"voltix-id"`) {
		t.Errorf("sites: %d %v", code, out)
	}
	if code, out := e.apiCall("GET", "/api/v1/server", read, nil); code != 200 || out["version"] != "test" || out["api_version"] != float64(1) {
		t.Errorf("server: %d %v", code, out)
	}
	if code, _ := e.apiCall("GET", "/api/v1/sites/voltix-id", read, nil); code != 200 {
		t.Errorf("site: %d", code)
	}
	if code, _ := e.apiCall("GET", "/api/v1/sites/Bad..id", read, nil); code != 404 {
		t.Errorf("bad id: %d", code)
	}
	if code, _ := e.apiCall("GET", "/api/v1/nothing", read, nil); code != 404 {
		t.Errorf("unknown call: %d", code)
	}
	// rights
	if code, _ := e.apiCall("POST", "/api/v1/sites/voltix-id/restart", read, nil); code != 403 {
		t.Errorf("read key restarts: %d", code)
	}
	if code, _ := e.apiCall("POST", "/api/v1/sites/voltix-id/restart", ops, nil); code != 200 || !strings.Contains(strings.Join(f.actions, "|"), "restart") {
		t.Errorf("restart: %d %v", code, f.actions)
	}
	if code, _ := e.apiCall("POST", "/api/v1/sites/voltix-id/deploy", ops, nil); code != 403 {
		t.Errorf("operate key deploys: %d", code)
	}
	if code, _ := e.apiCall("POST", "/api/v1/sites/voltix-id/restart", far, nil); code != 403 {
		t.Errorf("key used from an address outside its list: %d", code)
	}
	// a key for one website sees no other
	if code, _ := e.apiCall("GET", "/api/v1/sites/other-site", dep, nil); code != 404 {
		t.Errorf("other website: %d", code)
	}
	// a key for another website does not see this one, not even in the list
	other := "npk_" + strings.Repeat("B", 43)
	u, _ := e.st.UserByEmail("admin@example.test")
	if err := e.st.CreateAPIKey(&store.APIKey{OrgID: u.OrgID, UserID: u.ID, Name: "other", Prefix: other[:10], Rights: []string{"read", "operate"}, Site: "other-site", Created: e.clock}, auth.TokenHash(other)); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.apiCall("POST", "/api/v1/sites/voltix-id/restart", other, nil); code != 404 {
		t.Errorf("key of another website restarts this one: %d", code)
	}
	if code, out := e.apiCall("GET", "/api/v1/sites", other, nil); code != 200 || strings.Contains(toJSON(out), "voltix-id") {
		t.Errorf("key of another website lists this one: %d %v", code, out)
	}
	// deploy: only Git-built runtime websites; static release from the body
	if code, _ := e.apiCall("POST", "/api/v1/sites/voltix-id/deploy", dep, nil); code != 400 {
		t.Errorf("deploy on WordPress: %d", code)
	}
	f.app, f.repo = "node", "https://github.com/example/app.git"
	code, out = e.apiCall("POST", "/api/v1/sites/voltix-id/deploy", dep, nil)
	if code != 202 || out["job"] != "j0123456789abcdef" {
		t.Fatalf("deploy: %d %v", code, out)
	}
	if code, out := e.apiCall("GET", "/api/v1/jobs/j0123456789abcdef", dep, nil); code != 200 || out["site"] != "voltix-id" {
		t.Errorf("job: %d %v", code, out)
	}
	if code, _ := e.apiCall("GET", "/api/v1/jobs/jabcdef0123456789", dep, nil); code != 404 {
		t.Errorf("a job the key did not start: %d", code)
	}
	f.app = "static"
	if code, _ := e.apiCall("PUT", "/api/v1/sites/voltix-id/release?name=dist.tar", dep, strings.NewReader("PK")); code != 400 {
		t.Errorf("not a zip: %d", code)
	}
	code, out = e.apiCall("PUT", "/api/v1/sites/voltix-id/release?name=C:%5Cbuild%5Cdist.zip", dep, strings.NewReader("PK-zip-bytes"))
	if code != 201 || !strings.Contains(strings.Join(f.actions, "|"), "publish dist.zip PK-zip-bytes") {
		t.Errorf("release: %d %v %v", code, out, f.actions)
	}
	// audit: the key's name on every change, never the key
	var audit string
	e.st.DB.QueryRow(`SELECT group_concat(action||' '||detail, '|') FROM audit`).Scan(&audit)
	for _, want := range []string{"site.restart voltix-id", "(API key ops)", "site.deploy voltix-id", "site.static-publish voltix-id release 4 dist.zip (1 files) (API key ci)"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit lacks %q", want)
		}
	}
	for _, k := range []string{read, ops, dep, far} {
		if strings.Contains(audit, k) {
			t.Error("a key is in the audit log")
		}
	}
	// last use is recorded
	var at int64
	e.st.DB.QueryRow(`SELECT last_used_at FROM api_keys WHERE name='monitor'`).Scan(&at)
	if at == 0 {
		t.Error("last use not recorded")
	}
	// expiry
	e.clock = e.clock.Add(91 * 24 * time.Hour)
	if code, _ := e.apiCall("GET", "/api/v1/sites", read, nil); code != 401 {
		t.Errorf("expired key: %d", code)
	}
}

func TestAPILimits(t *testing.T) {
	e := newEnv(t)
	e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	key := e.newAPIKey(url.Values{"name": {"busy"}})
	// calls without a valid key: after 10 a minute the address waits, even with a good key
	for i := 0; i < apiBadPerMin; i++ {
		e.apiCall("GET", "/api/v1/sites", "npk_wrong", nil)
	}
	if code, _ := e.apiCall("GET", "/api/v1/sites", key, nil); code != 429 {
		t.Errorf("after bad calls: %d", code)
	}
	e.srv.apiBad = newTestLimiter(apiBadPerMin)
	for i := 0; i < apiPerMinute; i++ {
		if code, _ := e.apiCall("GET", "/api/v1/sites", key, nil); code != 200 {
			t.Fatalf("call %d: %d", i, code)
		}
	}
	if code, _ := e.apiCall("GET", "/api/v1/sites", key, nil); code != 429 {
		t.Errorf("call over the per-key limit: %d", code)
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func newTestLimiter(n int) *auth.Limiter { return auth.NewLimiter(n, time.Minute) }
