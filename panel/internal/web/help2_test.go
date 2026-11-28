package web

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestHelpCheck is the guide's own check (HELP-02): every article in docs/guides has a header with a unique code,
// a known tier and known error codes, the same header in English and Indonesian, links that point to articles that
// exist, no step codes, and no server commands in articles for every user; every common error has an article.
func TestHelpCheck(t *testing.T) {
	files, _ := filepath.Glob("../../../docs/guides/*.md")
	if len(files) == 0 {
		t.Fatal("no articles")
	}
	known := map[string]bool{unknownErr: true}
	for _, e := range helpErrors {
		if known[e.Code] {
			t.Errorf("error code %s twice", e.Code)
		}
		known[e.Code] = true
	}
	slugs := map[string]bool{}
	for _, f := range files {
		s, _ := articleFile(f)
		slugs[s] = true
	}
	ids := map[string]string{}
	explained := map[string]string{}
	stepRe := regexp.MustCompile(`\b[A-Z]{2,8}-[0-9]{2}[a-z]?\b`)
	linkRe := regexp.MustCompile(`\]\(([^)#\s]+\.md)(#[^)]*)?\)`)
	fenceRe := regexp.MustCompile("(?s)```.*?```")
	banned := []string{"sudo ", "systemctl", "journalctl"}
	for _, f := range files {
		slug, lang := articleFile(f)
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		h, body, ok := parseHeader(string(b))
		name := filepath.Base(f)
		if !ok {
			t.Errorf("%s: no header between --- lines", name)
			continue
		}
		if !helpCodeRe.MatchString(h.ID) {
			t.Errorf("%s: code %q is not NP-<MN|AL|TS|FAQ|ADM>-<nnn>", name, h.ID)
		}
		if !helpTiers[h.Tier] {
			t.Errorf("%s: tier %q is not user or admin (internal notes never live in this repository)", name, h.Tier)
		}
		if lang == helpLangs[0] {
			if other, dup := ids[h.ID]; dup {
				t.Errorf("%s: code %s is also used by %s", name, h.ID, other)
			}
			ids[h.ID] = slug
			for _, l := range helpLangs[1:] {
				tr, err := os.ReadFile(filepath.Join(filepath.Dir(f), slug+"."+l+".md"))
				if err != nil {
					t.Errorf("%s: no %s version (%s.%s.md)", name, l, slug, l)
					continue
				}
				if th, _, _ := parseHeader(string(tr)); !slices.Equal([]string{th.ID, th.Tier}, []string{h.ID, h.Tier}) || th.Open != h.Open ||
					!slices.Equal(th.Keywords, h.Keywords) || !slices.Equal(th.Errors, h.Errors) {
					t.Errorf("%s: header differs from %s.%s.md", name, slug, l)
				}
			}
			for _, e := range h.Errors {
				if !known[e] {
					t.Errorf("%s: error code %q is not in helpErrors", name, e)
				}
				if other, dup := explained[e]; dup {
					t.Errorf("%s: error code %s is also explained by %s", name, e, other)
				}
				explained[e] = slug
			}
			if len(h.Keywords) == 0 {
				t.Errorf("%s: no keywords", name)
			}
		} else if _, err := os.Stat(filepath.Join(filepath.Dir(f), slug+".md")); err != nil {
			t.Errorf("%s: no English version", name)
		}
		if strings.Count(strings.TrimSpace(body), "\n") < 3 {
			t.Errorf("%s: no text below the title", name)
		}
		if !strings.HasPrefix(body, "# ") {
			t.Errorf("%s: the text does not start with a # title", name)
		}
		if m := stepTagRe.FindString(strings.SplitN(body, "\n", 2)[0]); m != "" {
			t.Errorf("%s: title carries a step code %s", name, m)
		}
		prose := fenceRe.ReplaceAllString(body, "")
		if m := stepRe.FindString(prose); m != "" {
			t.Errorf("%s: step code %s in the text; say what it is instead", name, m)
		}
		for _, m := range linkRe.FindAllStringSubmatch(body, -1) {
			if target := filepath.Base(m[1]); !strings.Contains(m[1], "/") {
				if s, _ := articleFile(target); !slugs[s] {
					t.Errorf("%s: link to %s, which is no article", name, m[1])
				}
			}
		}
		if h.Tier == "user" {
			for _, w := range banned {
				if strings.Contains(prose, w) || strings.Contains(prose, "`"+strings.TrimSpace(w)) {
					t.Errorf("%s: %q in an article for every user (server commands belong in admin articles)", name, strings.TrimSpace(w))
				}
			}
		}
	}
	for code := range known {
		if _, ok := explained[code]; !ok {
			t.Errorf("error code %s has no article (add it to an article's errors header)", code)
		}
	}
}

func TestFold(t *testing.T) {
	for in, want := range map[string]string{"Pembaruán  PANEL!": "pembaruan panel", "É-mail, ÇA": "e-mail ca", "NP-TS-010": "np-ts-010"} {
		if got := fold(in); got != want {
			t.Errorf("fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHelpSearchAndTiers(t *testing.T) {
	e := newEnv(t)
	e.withAgent()
	x := &testExt{}
	if err := e.srv.SetExtensions(x.extensions()); err != nil {
		t.Fatal(err)
	}
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		_, body, _ := e.get(e.c, "/help")
		for _, want := range []string{`href="/help/update"`, `href="/help/another-change-running"`, "NP-ADM-003", `name="q"`} {
			if !strings.Contains(body, want) {
				t.Errorf("index (%s) lacks %q", lang, want)
			}
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"id"}})
	if _, body, _ := e.get(e.c, "/help"); !strings.Contains(body, "Tombol-tombol sebuah website berwarna abu-abu") {
		t.Error("a problem article has no summary on the index")
	}
	if _, body, _ := e.get(e.c, "/help/update"); !strings.Contains(body, "Memperbarui Nostek Panel") || strings.Contains(body, "Panduan ini ditulis dalam bahasa Inggris") {
		t.Error("the Indonesian article is not shown in Indonesian")
	}
	// search: accents and capitals do not matter, error text finds the article, words must all match
	for q, want := range map[string]string{"SERTIFIKÁT wildcard": "/help/certificates", "another change is running": "/help/another-change-running", "frasa sandi": "/help/server-backup"} {
		_, body, _ := e.get(e.c, "/help?q="+url.QueryEscape(q))
		if !strings.Contains(body, `id="found"`) || !strings.Contains(body, `href="`+want+`"`) {
			t.Errorf("search %q does not find %s", q, want)
		}
	}
	if _, body, _ := e.get(e.c, "/help?q=zzqqxx+nothing"); !strings.Contains(body, "Tidak ada artikel") {
		t.Error("search without hits")
	}
	// a code opens its article
	if code, _, h := e.get(e.c, "/help?q=np-ts-010"); code != 303 || h.Get("Location") != "/help/another-change-running" {
		t.Errorf("code search: %d %s", code, h.Get("Location"))
	}
	if code, _, h := e.get(e.c, "/help/NP-ADM-003"); code != 302 || h.Get("Location") != "/help/update" {
		t.Errorf("code address: %d %s", code, h.Get("Location"))
	}

	// a hosting account sees the articles for users only
	code, out, _ := e.post(e.c, "/x/hm/make", url.Values{"email": {"cust@example.test"}, "role": {"customer"}}, "/x/hm/hello")
	m := regexp.MustCompile(`id=(\d+) link=(/reveal/\S+)`).FindStringSubmatch(out)
	if code != 200 || m == nil {
		t.Fatalf("make user: %d %s", code, out)
	}
	cust := e.client()
	e.signInNew(cust, "cust@example.test", m[2])
	_, body, _ := e.get(cust, "/help")
	if strings.Contains(body, `href="/help/update"`) || strings.Contains(body, `href="/help/change-default-user"`) || !strings.Contains(body, `href="/help/certificates"`) {
		t.Error("a hosting account sees articles for admins, or misses articles for users")
	}
	for _, p := range []string{"/help/update", "/help/install", "/help/NP-ADM-003"} {
		if code, _, _ := e.get(cust, p); code != 404 && code != 302 {
			t.Errorf("%s for a hosting account: %d", p, code)
		}
	}
	if code, _, _ := e.get(cust, "/help/update"); code != 404 {
		t.Errorf("an admin article for a hosting account: %d", code)
	}
	if _, body, _ := e.get(cust, "/help?q=passphrase"); strings.Contains(body, `href="/help/server-backup"`) {
		t.Error("the search shows a hosting account an article for admins")
	}
}

func TestHelpErrorLinks(t *testing.T) {
	e := newEnv(t)
	e.withAgent()
	// sign-in problems are readable without signing in, other articles are not
	if code, body, _ := e.get(e.c, "/help/sign-in-problems"); code != 200 || !strings.Contains(body, "NP-TS-001") {
		t.Fatalf("open article: %d", code)
	}
	if code, _, h := e.get(e.c, "/help/update"); code != 303 || h.Get("Location") != "/login" {
		t.Fatalf("closed article without login: %d", code)
	}
	e.setupAdmin(e.c, "admin@example.test")
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"id"}})
	// a common error links its article and gets an incident code
	_, body, _ := e.get(e.c, "/settings/update?err="+url.QueryEscape("another change is running; try again when it is done"))
	if !strings.Contains(body, `href="/help/another-change-running"`) || !strings.Contains(body, "Kenapa? Baca solusinya") {
		t.Error("busy error does not link its article")
	}
	inc := regexp.MustCompile(`Kode kejadian ([2-9A-HJKMNP-Z]{6}) · `).FindStringSubmatch(body)
	if inc == nil {
		t.Error("no incident code")
	}
	// an unknown error links the support article
	if _, body, _ := e.get(e.c, "/settings/update?err=something+odd"); !strings.Contains(body, `href="/help/contact-support"`) || !strings.Contains(body, "Kode kejadian") {
		t.Error("unknown error does not link the support article")
	}
	// a typing mistake links the article without an incident code
	if _, body, _ := e.get(e.c, "/settings/update?err="+url.QueryEscape("does not resolve to a public internet address")); !strings.Contains(body, `href="/help/domain-not-pointing"`) || strings.Contains(body, "Kode kejadian") {
		t.Error("dns error: wrong link or an incident code")
	}
	// the sign-in page links the open article
	c := e.client()
	e.ipLimitReset()
	_, body, _ = e.post(c, "/login", url.Values{"email": {"admin@example.test"}, "password": {"wrong-password-1"}}, "/login")
	if !strings.Contains(body, `href="/help/sign-in-problems"`) {
		t.Error("wrong password does not link the sign-in article")
	}
	// a page with no error has no help line
	if _, body, _ := e.get(e.c, "/settings/update"); strings.Contains(body, `class="err-help"`) {
		t.Error("help line without an error")
	}
}
