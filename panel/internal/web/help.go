package web

// Help pages (HELP-01, HELP-02): the articles from docs/guides and the whole CHANGELOG, read inside the panel. Both
// come with the panel (a copy in web/docs, made by sync-docs.sh), so they match the running release and open without
// GitHub. They are rendered once at start by internal/md, which escapes every piece of text and lets no raw HTML
// through.
//
// HELP-02: every article has a fixed code (NP-<kind>-<nnn>), a tier (user: everyone signed in; admin: the server's
// admins only), keywords and the error codes it explains, in a small header between "---" lines. Each article is
// written twice: <slug>.md in English and <slug>.id.md in Indonesian. The Help page searches the articles the
// signed-in user may see, and an error box links the article that explains the error.

import (
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/thuekx/docker-panel-VPS/panel/internal/md"
)

const repoBlob = "https://github.com/thuekx/docker-panel-VPS/blob/main/"

// helpLangs are the languages every article is written in; the first is the fallback.
var helpLangs = []string{"en", "id"}

var (
	guideSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,60}$`)
	helpCodeRe  = regexp.MustCompile(`^NP-(MN|AL|TS|FAQ|ADM)-[0-9]{3}$`)
	stepTagRe   = regexp.MustCompile(`\s*\([A-Z]+-[0-9]+[a-z]?\)\s*$`)
	relVerRe    = regexp.MustCompile(`^v([0-9]+\.[0-9]+\.[0-9]+)\s*[—-]*\s*(.*)$`)
	relHeadRe   = regexp.MustCompile(`(?m)^## \[([0-9]+\.[0-9]+\.[0-9]+)\]`)
	mdLinkRe    = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
)

// helpTiers are the readers an article is for.
var helpTiers = map[string]bool{"user": true, "admin": true}

// guideText is an article in one language.
type guideText struct {
	Title, Summary string
	HTML           template.HTML
	TOC            []md.Heading
	search         string // folded title + body, for the search
}

type guide struct {
	Slug, ID, Tier   string
	Open             bool // readable without signing in (sign-in problems)
	Keywords, Errors []string
	text             map[string]*guideText
	search           string // folded keywords, code and error texts (both languages)
}

// guideView is an article as one page shows it, in the reader's language.
type guideView struct {
	Slug, ID, Tier string
	Title, Summary string
	HTML           template.HTML
	TOC            []md.Heading
	English        bool // shown in English because the reader's language has no text yet
}

func (g *guide) in(lang string) *guideText {
	if t, ok := g.text[lang]; ok {
		return t
	}
	return g.text[helpLangs[0]]
}

func (g *guide) view(lang string) guideView {
	t := g.in(lang)
	_, own := g.text[lang]
	return guideView{Slug: g.Slug, ID: g.ID, Tier: g.Tier, Title: t.Title, Summary: t.Summary, HTML: t.HTML, TOC: t.TOC, English: !own}
}

type release struct {
	Version, Date, ID string
}

type helpDocs struct {
	guides    []*guide
	bySlug    map[string]int
	byID      map[string]int
	byErr     map[string]int // error code -> article
	changelog template.HTML
	releases  []release
}

// pageGuide names the article of a page, for the "Guide" link at its top.
var pageGuide = map[string]string{
	"update":       "update",
	"serverbackup": "server-backup",
	"certsettings": "certificates",
	"apikeys":      "api",
	"serversec":    "server-security",
	"quarantine":   "malware-scan",
	"system":       "system",
	"terminal":     "terminal",
	"db":           "database-access",
	"profile":      "passkeys",
	"domains":      "certificates",
}

// docLink maps a link in a document under dir (a path in the repository) to the panel's own page, or to the file
// on GitHub for other files of the repository.
func docLink(dir string, have map[string]int) md.Link {
	return func(t string) string {
		if strings.HasPrefix(t, "https://") || strings.HasPrefix(t, "http://") {
			return t
		}
		if strings.HasPrefix(t, "#") {
			return t
		}
		if strings.Contains(t, ":") { // javascript:, mailto:, data: …
			return ""
		}
		file, frag, _ := strings.Cut(t, "#")
		p := path.Clean(path.Join(dir, file))
		if p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
			return ""
		}
		if frag != "" {
			frag = "#" + frag
		}
		if p == "CHANGELOG.md" {
			return "/help/changelog" + frag
		}
		if d, f := path.Split(p); d == "docs/guides/" && strings.HasSuffix(f, ".md") {
			slug := strings.TrimSuffix(strings.TrimSuffix(f, ".md"), ".id")
			if _, ok := have[slug]; ok {
				return "/help/" + slug + frag
			}
		}
		return repoBlob + p + frag
	}
}

// helpHeader is the header of an article.
type helpHeader struct {
	ID, Tier         string
	Open             bool
	Keywords, Errors []string
}

// parseHeader reads the header between "---" lines at the top of an article: one "key: value" per line, lists
// separated by commas. It returns the header and the rest of the file.
func parseHeader(src string) (helpHeader, string, bool) {
	var h helpHeader
	if !strings.HasPrefix(src, "---\n") {
		return h, src, false
	}
	head, rest, ok := strings.Cut(src[4:], "\n---\n")
	if !ok {
		return h, src, false
	}
	list := func(v string) []string {
		var out []string
		for _, x := range strings.Split(v, ",") {
			if x = strings.TrimSpace(x); x != "" {
				out = append(out, x)
			}
		}
		return out
	}
	for _, line := range strings.Split(head, "\n") {
		k, v, _ := strings.Cut(line, ":")
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "id":
			h.ID = v
		case "tier":
			h.Tier = v
		case "open":
			h.Open = v == "yes"
		case "keywords":
			h.Keywords = list(v)
		case "errors":
			h.Errors = list(v)
		}
	}
	return h, strings.TrimLeft(rest, "\n"), true
}

// articleFile splits "update.id.md" into its slug and language.
func articleFile(f string) (slug, lang string) {
	base := strings.TrimSuffix(path.Base(f), ".md")
	for _, l := range helpLangs[1:] {
		if s, ok := strings.CutSuffix(base, "."+l); ok {
			return s, l
		}
	}
	return base, helpLangs[0]
}

func renderText(src string, link md.Link) *guideText {
	t := &guideText{}
	// the first heading is the title (shown as the page title), the first paragraph the summary
	if first, rest, ok := strings.Cut(src, "\n"); ok && strings.HasPrefix(first, "# ") {
		t.Title = stepTagRe.ReplaceAllString(strings.TrimPrefix(strings.TrimPrefix(first, "# "), "Guide: "), "")
		if r := []rune(t.Title); len(r) > 0 {
			t.Title = strings.ToUpper(string(r[0])) + string(r[1:])
		}
		src = rest
	}
	for _, para := range strings.Split(strings.TrimSpace(src), "\n\n") {
		p := strings.TrimSpace(para)
		if h, rest, ok := strings.Cut(p, "\n"); ok && strings.HasPrefix(h, "#") { // a heading straight above its text
			p = strings.TrimSpace(rest)
		}
		if p != "" && !strings.HasPrefix(p, "#") && !strings.HasPrefix(p, "|") && !strings.HasPrefix(p, "```") {
			t.Summary = summary(p)
			break
		}
	}
	out, heads := md.Render(src, link)
	t.HTML = template.HTML(out) // #nosec G203 -- md escapes all text and emits only its own tags
	for _, x := range heads {
		if x.Level == 2 {
			t.TOC = append(t.TOC, x)
		}
	}
	t.search = fold(t.Title + "\n" + mdLinkRe.ReplaceAllString(src, "$1"))
	return t
}

func loadHelp(assets fs.FS) (*helpDocs, error) {
	h := &helpDocs{bySlug: map[string]int{}, byID: map[string]int{}, byErr: map[string]int{}}
	files, err := fs.Glob(assets, "docs/guides/*.md")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	for _, f := range files {
		if slug, _ := articleFile(f); guideSlugRe.MatchString(slug) {
			h.bySlug[slug] = -1
		}
	}
	link := docLink("docs/guides", h.bySlug)
	for _, f := range files {
		slug, lang := articleFile(f)
		if !guideSlugRe.MatchString(slug) {
			continue
		}
		b, err := fs.ReadFile(assets, f)
		if err != nil {
			return nil, err
		}
		head, src, _ := parseHeader(string(b))
		i := h.bySlug[slug]
		if i < 0 {
			i = len(h.guides)
			h.bySlug[slug] = i
			h.guides = append(h.guides, &guide{Slug: slug, Tier: "user", text: map[string]*guideText{}})
		}
		g := h.guides[i]
		if lang == helpLangs[0] { // the English file's header is the article's
			g.ID, g.Open, g.Keywords, g.Errors = head.ID, head.Open, head.Keywords, head.Errors
			if head.Tier != "" {
				g.Tier = head.Tier
			}
		}
		g.text[lang] = renderText(src, link)
		if g.text[lang].Title == "" {
			g.text[lang].Title = slug
		}
	}
	for k, v := range h.bySlug {
		if v < 0 {
			delete(h.bySlug, k)
		}
	}
	for i, g := range h.guides {
		if g.ID != "" {
			h.byID[g.ID] = i
		}
		for _, e := range g.Errors {
			h.byErr[e] = i
		}
		g.search = fold(strings.Join(g.Keywords, " ") + " " + g.ID)
	}
	b, err := fs.ReadFile(assets, "docs/CHANGELOG.md")
	if err != nil {
		return nil, err
	}
	src := string(b)
	if first, rest, ok := strings.Cut(src, "\n"); ok && strings.HasPrefix(first, "# ") {
		src = rest
	}
	src = relHeadRe.ReplaceAllString(src, "## v$1") // "## [1.49.0] — date" reads "v1.49.0 — date"
	out, heads := md.Render(src, docLink(".", h.bySlug))
	h.changelog = template.HTML(out) // #nosec G203 -- as above
	for _, x := range heads {
		if m := relVerRe.FindStringSubmatch(x.Text); x.Level == 2 && m != nil {
			h.releases = append(h.releases, release{Version: m[1], Date: m[2], ID: x.ID})
		}
	}
	return h, nil
}

// summary is the first paragraph as plain text, cut at about 200 characters.
func summary(p string) string {
	p = strings.Join(strings.Fields(p), " ")
	p = mdLinkRe.ReplaceAllString(p, "$1")
	p = strings.NewReplacer("**", "", "`", "", "> ", "").Replace(p)
	if r := []rune(p); len(r) > 200 {
		cut := string(r[:200])
		if i := strings.LastIndex(cut, " "); i > 120 {
			cut = cut[:i]
		}
		p = cut + " …"
	}
	return p
}

// foldMap takes the accents off Latin letters, so "pembaruan" finds "pembaruán" and "e-mail" finds "é-mail".
var foldMap = func() map[rune]rune {
	m := map[rune]rune{}
	for base, list := range map[rune]string{
		'a': "àáâãäåāăą", 'c': "çćĉċč", 'd': "ďđ", 'e': "èéêëēĕėęě", 'g': "ĝğġģ", 'h': "ĥħ", 'i': "ìíîïĩīĭįı",
		'j': "ĵ", 'k': "ķ", 'l': "ĺļľŀł", 'n': "ñńņňŉ", 'o': "òóôõöøōŏő", 'r': "ŕŗř", 's': "śŝşšß", 't': "ţťŧ",
		'u': "ùúûüũūŭůűų", 'w': "ŵ", 'y': "ýÿŷ", 'z': "źżž",
	} {
		for _, r := range list {
			m[r] = base
		}
	}
	return m
}()

// fold makes text comparable for the search: lower case, no accents, one space between words.
func fold(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		if f, ok := foldMap[r]; ok {
			r = f
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r > 127:
			b.WriteRune(r)
			space = false
		case r == '.' || r == '-' || r == '_' || r == '/':
			b.WriteRune(r)
			space = false
		default:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// helpSearch finds the articles of list whose code, keywords, error texts, title or text hold every word of q; the
// ones that hold them in their title or keywords come first.
func (s *Server) helpSearch(list []*guide, q, lang string) []*guide {
	words := strings.Fields(fold(q))
	if len(words) == 0 {
		return nil
	}
	type hit struct {
		g    *guide
		rank int
	}
	var hits []hit
	for _, g := range list {
		head := g.search + " " + fold(g.in(lang).Title) + " " + s.errSearch(g)
		all := head
		for _, t := range g.text {
			all += " " + t.search
		}
		ok, inHead := true, true
		for _, w := range words {
			if !strings.Contains(all, w) {
				ok = false
				break
			}
			if !strings.Contains(head, w) {
				inHead = false
			}
		}
		if ok {
			r := 1
			if inHead {
				r = 0
			}
			hits = append(hits, hit{g, r})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })
	out := make([]*guide, 0, len(hits))
	for _, x := range hits {
		out = append(out, x.g)
	}
	return out
}

// helpVisible reports whether the signed-in user of r may read g.
func helpVisible(r *http.Request, g *guide) bool {
	if g.Tier == "user" {
		return true
	}
	u := userOf(r)
	return u != nil && u.Role == "admin"
}

func (s *Server) visibleGuides(r *http.Request) []*guide {
	var out []*guide
	for _, g := range s.help.guides {
		if helpVisible(r, g) {
			out = append(out, g)
		}
	}
	return out
}

func views(list []*guide, lang string) []guideView {
	out := make([]guideView, 0, len(list))
	for _, g := range list {
		out = append(out, g.view(lang))
	}
	return out
}

type helpData struct {
	Guides   []guideView
	Admin    []guideView // the articles for admins only
	Guide    *guideView
	Log      template.HTML
	Releases []release
	Running  string // the running release's anchor, when the CHANGELOG has it
	Version  string
	Query    string
	Found    []guideView
	Searched bool
}

func (s *Server) helpIndex(w http.ResponseWriter, r *http.Request) {
	l := s.lang(r)
	d := helpData{Releases: s.help.releases}
	list := s.visibleGuides(r)
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		if len(q) > 200 {
			q = q[:200]
		}
		d.Query, d.Searched = q, true
		if i, ok := s.help.byID[strings.ToUpper(q)]; ok && helpVisible(r, s.help.guides[i]) { // a code opens its article
			http.Redirect(w, r, "/help/"+s.help.guides[i].Slug, http.StatusSeeOther)
			return
		}
		d.Found = views(s.helpSearch(list, q, l), l)
	}
	for _, g := range list {
		if g.Tier == "admin" {
			d.Admin = append(d.Admin, g.view(l))
		} else {
			d.Guides = append(d.Guides, g.view(l))
		}
	}
	s.render(w, r, "help", page{Title: s.tr.T(l, "help.title"), Active: "help", Data: d})
}

func (s *Server) helpChangelog(w http.ResponseWriter, r *http.Request) {
	d := helpData{Log: s.help.changelog, Releases: s.help.releases, Version: strings.TrimPrefix(s.cfg.Version, "v")}
	for _, x := range s.help.releases {
		if x.Version == d.Version {
			d.Running = x.ID
		}
	}
	s.render(w, r, "helplog", page{Title: s.tr.T(s.lang(r), "help.log"), Active: "help", Data: d})
}

// helpArticle finds the article of the path's name: its slug, or its code (which then redirects to the slug).
func (s *Server) helpArticle(w http.ResponseWriter, r *http.Request) (*guide, bool) {
	name := r.PathValue("slug")
	if helpCodeRe.MatchString(name) {
		if i, ok := s.help.byID[name]; ok {
			http.Redirect(w, r, "/help/"+s.help.guides[i].Slug, http.StatusFound)
			return nil, false
		}
	}
	i, ok := s.help.bySlug[name]
	if !ok {
		s.notFound(w, r)
		return nil, false
	}
	return s.help.guides[i], true
}

func (s *Server) helpGuide(w http.ResponseWriter, r *http.Request) {
	g, ok := s.helpArticle(w, r)
	if !ok {
		return
	}
	if !helpVisible(r, g) { // an article for admins is 404 for everyone else, as their pages are
		s.notFound(w, r)
		return
	}
	v := g.view(s.lang(r))
	s.render(w, r, "helpguide", page{Title: v.Title, Active: "help", Data: helpData{Guide: &v}})
}

// helpOpen shows an article marked "open" (sign-in problems) to someone who is not signed in, on the sign-in
// layout; everything else goes to next, which asks for a sign-in.
func (s *Server) helpOpen(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sess, _ := s.currentSession(r); sess != nil {
			next(w, r)
			return
		}
		i, ok := s.help.bySlug[r.PathValue("slug")]
		if !ok || !s.help.guides[i].Open {
			next(w, r)
			return
		}
		v := s.help.guides[i].view(s.lang(r))
		s.render(w, r, "helpguide", page{Title: v.Title, Data: helpData{Guide: &v}})
	}
}
