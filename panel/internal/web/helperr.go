package web

// HELP-02: common errors link the article that explains them. An error the panel shows is matched to an error
// code here: by the translation key the panel itself used, or by a fixed phrase of the agent's answer. The article
// that lists the code in its "errors" header is linked under the error box as "Why? Read the fix". An error that
// matches no code links the "contact support" article (code "unknown") and gets an incident code: a short id
// written to the panel's log with the page and the full error, so support can find exactly what happened.

import (
	"crypto/rand"
	"log"
	"net/http"
	"strings"
)

// helpErr is one common error.
type helpErr struct {
	Code    string
	Keys    []string // translation keys whose text the panel shows for it
	Phrases []string // fixed parts of the agent's answer (compared without case)
	Quiet   bool     // a typing mistake of the user: no incident code
}

// helpErrors are the common errors of the panel. A new common error gets a code here and an article that lists it
// (TestHelpCheck fails otherwise).
var helpErrors = []helpErr{
	{Code: "login", Keys: []string{"err.login", "err.toomany", "err.code", "err.pwcurrent"}, Quiet: true},
	{Code: "linkgone", Keys: []string{"linkgone"}, Quiet: true},
	{Code: "notfound", Keys: []string{"notfound"}, Quiet: true},
	{Code: "busy", Phrases: []string{"another change is running"}},
	{Code: "noagent", Phrases: []string{"agent not reachable", "agent answered"}},
	{Code: "dns", Phrases: []string{"does not resolve to"}, Quiet: true},
	{Code: "cloudns", Phrases: []string{"cloudns"}},
	{Code: "file", Keys: []string{"files.err.toobig", "files.err.name", "files.err.nofile"},
		Phrases: []string{"file is too large", "already exists", "name is not valid", "a file name is needed", "choose another name"}, Quiet: true},
}

// unknownErr is the code of every error that matches no other.
const unknownErr = "unknown"

// errCode finds the code of an error text shown in lang.
func (s *Server) errCode(text string) (helpErr, bool) {
	low := strings.ToLower(text)
	for _, e := range helpErrors {
		for _, k := range e.Keys {
			for _, l := range helpLangs {
				if t := s.tr.T(l, k); t == text || s.tr.T(l, k+".lead") == text {
					return e, true
				}
			}
		}
		for _, p := range e.Phrases {
			if strings.Contains(low, p) {
				return e, true
			}
		}
	}
	return helpErr{Code: unknownErr}, false
}

// errSearch is the folded text of the errors an article explains, in every language, for the search.
func (s *Server) errSearch(g *guide) string {
	var b strings.Builder
	for _, code := range g.Errors {
		for _, e := range helpErrors {
			if e.Code != code {
				continue
			}
			for _, k := range e.Keys {
				for _, l := range helpLangs {
					b.WriteString(s.tr.T(l, k) + " ")
				}
			}
			b.WriteString(strings.Join(e.Phrases, " ") + " ")
		}
	}
	return fold(b.String())
}

// incidentAlphabet has no 0/O or 1/I/L, so a code read out over the phone is not mistaken.
const incidentAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

func newIncident() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	for i := range b {
		b[i] = incidentAlphabet[int(b[i])%len(incidentAlphabet)]
	}
	return string(b[:])
}

// errInfo fills the article link and the incident code of a page that shows an error.
func (s *Server) errInfo(r *http.Request, name string, p *page) {
	if s.help == nil {
		return
	}
	text := p.Error
	if name == "message" {
		key, _ := p.Data.(string)
		text = s.tr.T(helpLangs[0], key+".lead")
	}
	if text == "" {
		return
	}
	e, _ := s.errCode(text)
	if i, ok := s.help.byErr[e.Code]; ok && (helpVisible(r, s.help.guides[i]) || s.help.guides[i].Open) {
		p.ErrHelp = s.help.guides[i].Slug
	}
	if e.Quiet || name == "message" {
		return
	}
	p.Incident = newIncident()
	var uid int64
	if p.User != nil {
		uid = p.User.ID
	}
	log.Printf("incident %s user=%d page=%s code=%s error=%q", p.Incident, uid, r.URL.Path, e.Code, text)
}
