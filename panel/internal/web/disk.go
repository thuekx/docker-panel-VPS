package web

// Disk usage (DISK-01, like cPanel's Disk Usage): a website's Disk tab with its parts and a drill-down of its files
// by size, and the server's Disk page with every website, the trash and what Docker keeps, with safe clean-ups.

import (
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

type diskPart struct {
	Key   string // locale key
	Bytes int64
	Size  string
	Pct   int
}

type diskRow struct {
	agent.DiskRow
	Size string
	Pct  int
	Path string // drill-down address of a folder
}

type bigRow struct {
	agent.BigFile
	Size string
	When string
	Dir  string // the folder in the File Manager
}

type diskData struct {
	Site   agent.Site
	D      agent.SiteDisk
	Total  string
	Parts  []diskPart
	Rows   []diskRow
	Big    []bigRow
	Crumbs []crumb
	Err    string
}

func pctOf(a, b int64) int {
	if b <= 0 {
		return 0
	}
	return int(a * 100 / b)
}

func (s *Server) diskPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		p = "/"
	}
	ctx, cancel := s.agentCtx(r, 40*time.Second)
	defer cancel()
	d := diskData{Site: site, Crumbs: crumbs(p)}
	sd, err := s.ag.SiteDisk(ctx, site.ID, p)
	if err != nil {
		d.Err = err.Error()
		s.render(w, r, "disk", page{Title: site.Domain, Active: "sites", Data: d})
		return
	}
	d.D, d.Total, d.Crumbs = sd, size(sd.Total), crumbs(sd.Path)
	for _, x := range []struct {
		k string
		b int64
	}{{"files", sd.Files}, {"database", sd.Database}, {"backups", sd.Backups}, {"trash", sd.Trash}, {"other", sd.Other}} {
		if x.k == "database" && !site.HasDB() && x.b == 0 {
			continue
		}
		d.Parts = append(d.Parts, diskPart{Key: x.k, Bytes: x.b, Size: size(x.b), Pct: pctOf(x.b, sd.Total)})
	}
	var top int64
	if len(sd.Rows) > 0 {
		top = sd.Rows[0].Bytes
	}
	for _, x := range sd.Rows {
		row := diskRow{DiskRow: x, Size: size(x.Bytes), Pct: pctOf(x.Bytes, top)}
		if x.Type == "dir" {
			row.Path = path.Join(sd.Path, x.Name)
		}
		d.Rows = append(d.Rows, row)
	}
	for _, b := range sd.Big {
		d.Big = append(d.Big, bigRow{BigFile: b, Size: size(b.Bytes), When: time.Unix(b.MTime, 0).In(s.loc()).Format("02 Jan 2006"), Dir: path.Dir(b.Path)})
	}
	s.render(w, r, "disk", page{Title: site.Domain, Active: "sites", Data: d})
}

type siteBar struct {
	agent.SiteTotal
	Size string
	Pct  int
}

type dockerRow struct {
	agent.DockerUse
	Size, Free string
}

type serverDiskData struct {
	D             agent.ServerDisk
	Used, Free    string
	Disk, Trash   string
	UsedPct       int
	Sites         []siteBar
	Docker        []dockerRow
	CanImages     bool
	CanBuildCache bool
	Err           string
	Freed         string
}

func (s *Server) serverDiskPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.agentCtx(r, 40*time.Second)
	defer cancel()
	d := serverDiskData{Freed: r.URL.Query().Get("freed")}
	sd, err := s.ag.ServerDisk(ctx)
	if err != nil {
		d.Err = err.Error()
	} else {
		d.D, d.Used, d.Free, d.Disk, d.Trash, d.UsedPct = sd, size(sd.UsedBytes), size(sd.FreeBytes), size(sd.DiskBytes), size(sd.Trash), pctOf(sd.UsedBytes, sd.DiskBytes)
		var top int64
		if len(sd.Sites) > 0 {
			top = sd.Sites[0].Bytes
		}
		for _, x := range sd.Sites {
			d.Sites = append(d.Sites, siteBar{SiteTotal: x, Size: size(x.Bytes), Pct: pctOf(x.Bytes, top)})
		}
		for _, x := range sd.Docker {
			d.Docker = append(d.Docker, dockerRow{DockerUse: x, Size: size(x.Bytes), Free: size(x.Reclaimable)})
			d.CanImages = d.CanImages || (x.Kind == "images" && x.Reclaimable > 0)
			d.CanBuildCache = d.CanBuildCache || (x.Kind == "build-cache" && x.Reclaimable > 0)
		}
	}
	s.render(w, r, "diskserver", page{Title: "Disk", Active: "disk", Data: d, Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

func (s *Server) serverDiskAction(w http.ResponseWriter, r *http.Request) {
	what := r.PostForm.Get("what")
	if what != "images" && what != "build-cache" {
		http.Error(w, "bad request", 400)
		return
	}
	ctx, cancel := s.agentCtx(r, 6*time.Minute)
	defer cancel()
	freed, err := s.ag.CleanDocker(ctx, what)
	if err != nil {
		s.audit1(userOf(r), r, "disk.clean", what+" failed: "+err.Error())
		http.Redirect(w, r, "/disk?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	s.audit1(userOf(r), r, "disk.clean", what+" freed "+freed)
	http.Redirect(w, r, "/disk?done="+what+"&freed="+url.QueryEscape(freed), http.StatusSeeOther)
}
