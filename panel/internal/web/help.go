package web

// Help pages (HELP-01): the guides from docs/guides and the whole CHANGELOG, read inside the panel. Both come with
// the panel (a copy in web/docs, made by sync-docs.sh), so they match the running release and open without GitHub.
// They are rendered once at start by internal/md, which escapes every piece of text and lets no raw HTML through.

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

var (
	guideSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,60}$`)
	stepTagRe   = regexp.MustCompile(`\s*\([A-Z]+-[0-9]+[a-z]?\)\s*$`)
	relVerRe    = regexp.MustCompile(`^v([0-9]+\.[0-9]+\.[0-9]+)\s*[—-]*\s*(.*)$`)
	relHeadRe   = regexp.MustCompile(`(?m)^## \[([0-9]+\.[0-9]+\.[0-9]+)\]`)
)

type guide struct {
	Slug, Title, Summary string
	HTML                 template.HTML
	TOC                  []md.Heading
}

type release struct {
	Version, Date, ID string
}

type helpDocs struct {
	guides    []guide
	bySlug    map[string]int
	changelog template.HTML
	releases  []release
}

// pageGuide names the guide of a page, for the "Guide" link at its top.
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
			if _, ok := have[strings.TrimSuffix(f, ".md")]; ok {
				return "/help/" + strings.TrimSuffix(f, ".md") + frag
			}
		}
		return repoBlob + p + frag
	}
}

func loadHelp(assets fs.FS) (*helpDocs, error) {
	h := &helpDocs{bySlug: map[string]int{}}
	files, err := fs.Glob(assets, "docs/guides/*.md")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	for _, f := range files {
		slug := strings.TrimSuffix(path.Base(f), ".md")
		if guideSlugRe.MatchString(slug) {
			h.bySlug[slug] = -1
		}
	}
	link := docLink("docs/guides", h.bySlug)
	for _, f := range files {
		slug := strings.TrimSuffix(path.Base(f), ".md")
		if !guideSlugRe.MatchString(slug) {
			continue
		}
		b, err := fs.ReadFile(assets, f)
		if err != nil {
			return nil, err
		}
		g := guide{Slug: slug, Title: slug}
		src := string(b)
		// the first heading is the title (shown as the page title), the first paragraph the summary
		if first, rest, ok := strings.Cut(src, "\n"); ok && strings.HasPrefix(first, "# ") {
			g.Title = stepTagRe.ReplaceAllString(strings.TrimPrefix(strings.TrimPrefix(first, "# "), "Guide: "), "")
			if r := []rune(g.Title); len(r) > 0 {
				g.Title = strings.ToUpper(string(r[0])) + string(r[1:])
			}
			src = rest
		}
		for _, para := range strings.Split(strings.TrimSpace(src), "\n\n") {
			if p := strings.TrimSpace(para); p != "" && !strings.HasPrefix(p, "#") && !strings.HasPrefix(p, "|") && !strings.HasPrefix(p, "```") {
				g.Summary = summary(p)
				break
			}
		}
		out, heads := md.Render(src, link)
		g.HTML = template.HTML(out) // #nosec G203 -- md escapes all text and emits only its own tags
		for _, x := range heads {
			if x.Level == 2 {
				g.TOC = append(g.TOC, x)
			}
		}
		h.bySlug[slug] = len(h.guides)
		h.guides = append(h.guides, g)
	}
	for k, v := range h.bySlug {
		if v < 0 {
			delete(h.bySlug, k)
		}
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
	p = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`).ReplaceAllString(p, "$1")
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

type helpData struct {
	Guides   []guide
	Guide    *guide
	Log      template.HTML
	Releases []release
	Running  string // the running release's anchor, when the CHANGELOG has it
	Version  string
}

func (s *Server) helpIndex(w http.ResponseWriter, r *http.Request) {
	d := helpData{Guides: s.help.guides, Releases: s.help.releases}
	s.render(w, r, "help", page{Title: s.tr.T(s.lang(r), "help.title"), Active: "help", Data: d})
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

func (s *Server) helpGuide(w http.ResponseWriter, r *http.Request) {
	i, ok := s.help.bySlug[r.PathValue("slug")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	g := s.help.guides[i]
	s.render(w, r, "helpguide", page{Title: g.Title, Active: "help", Data: helpData{Guide: &g, Guides: s.help.guides}})
}
