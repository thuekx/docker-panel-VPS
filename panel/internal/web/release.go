package web

// Static releases (SITE-03): the zip goes straight from the browser to the agent; nothing is buffered here.

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const maxReleaseReq = 520 << 20 // one release zip (the agent allows 512 MB) and the form around it

func (s *Server) releaseUpload(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	if !site.IsStatic() {
		s.notFound(w, r)
		return
	}
	long(w, 2*time.Hour)
	r.Body = http.MaxBytesReader(w, r.Body, maxReleaseReq)
	mr, ok := s.multipartCSRF(r)
	if !ok {
		http.Error(w, "bad request", http.StatusForbidden)
		return
	}
	l, u := s.lang(r), userOf(r)
	ctx, cancel := s.agentCtx(r, 2*time.Hour)
	defer cancel()
	base := "/sites/" + site.ID
	var ferr error
	note := ""
	done := false
	for ferr == nil && !done {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			ferr = err
			break
		}
		if p.FormName() == "zip" && p.FileName() != "" {
			fname := path.Base(strings.ReplaceAll(p.FileName(), `\`, "/"))
			if !strings.HasSuffix(strings.ToLower(fname), ".zip") {
				ferr = errors.New(s.tr.T(l, "rel.type"))
			} else {
				rel, err := s.ag.PublishStatic(ctx, site.ID, fname, p)
				ferr = err
				note = "release " + strconv.Itoa(rel.Release) + " " + rel.Name + " (" + strconv.Itoa(rel.Files) + " files)"
				done = true
			}
		}
		p.Close()
	}
	if ferr == nil && !done {
		ferr = errors.New(s.tr.T(l, "rel.need"))
	}
	if ferr != nil {
		var mbe *http.MaxBytesError
		msg := ferr.Error()
		if errors.As(ferr, &mbe) {
			msg = s.tr.T(l, "files.err.toobig")
		}
		s.audit1(u, r, "site.static-publish", site.ID+" failed: "+msg)
		http.Redirect(w, r, base+"?err="+url.QueryEscape(msg)+"#releases", http.StatusSeeOther)
		return
	}
	s.audit1(u, r, "site.static-publish", site.ID+" "+note)
	http.Redirect(w, r, base+"?done=static-publish#releases", http.StatusSeeOther)
}
