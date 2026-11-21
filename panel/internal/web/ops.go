package web

// Statistics and Cron pages (OPS-01), like cPanel's Metrics (Visitors, Errors, Bandwidth, Raw Access) and Cron
// Jobs. The agent counts visits from the edge's access log and runs the jobs inside the website's container.

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

// statRow is one bar of the day or hour chart.
type statRow struct {
	Label    string
	Visitors int
	Requests int
	Errors   int
	Bytes    string
	Pct      int
}

type metricsData struct {
	Site   agent.Site
	V      agent.Metrics
	Err    string
	Days   []statRow // newest first
	Hours  []statRow
	Bytes  string
	Errors int
	Ranges []int
}

func (s *Server) metricsPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	d := metricsData{Site: site, Ranges: []int{1, 7, 31}}
	v, err := s.ag.Metrics(ctx, site.ID, days)
	if err != nil {
		d.Err = err.Error()
	}
	d.V = v
	most := 1
	for _, x := range v.Days {
		most = max(most, x.Requests)
	}
	for i := len(v.Days) - 1; i >= 0; i-- {
		x := v.Days[i]
		d.Days = append(d.Days, statRow{Label: x.Day, Visitors: x.Visitors, Requests: x.Requests, Errors: x.S4 + x.S5, Bytes: size(x.Bytes), Pct: x.Requests * 100 / most})
	}
	most = 1
	for _, n := range v.Hours {
		most = max(most, n)
	}
	for h, n := range v.Hours {
		d.Hours = append(d.Hours, statRow{Label: strconv.Itoa(h/10) + strconv.Itoa(h%10) + ":00", Requests: n, Pct: n * 100 / most})
	}
	d.Bytes, d.Errors = size(v.Total.Bytes), v.Total.S4+v.Total.S5
	s.render(w, r, "metrics", page{Title: s.tr.T(s.lang(r), "met.title") + " · " + site.Domain, Active: "sites", Data: d})
}

// metricsRaw hands out the last raw access lines of a website as a text file.
func (s *Server) metricsRaw(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	v, err := s.ag.Metrics(ctx, site.ID, 1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+site.ID+`-access.log"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	for _, l := range v.Raw {
		_, _ = w.Write([]byte(l + "\n"))
	}
}

type cronRow struct {
	agent.CronJob
	NextText string
	Last     *agent.CronRun
	LastText string
}

type cronData struct {
	Site agent.Site
	V    agent.Cron
	Err  string
	Rows []cronRow
}

func (s *Server) cronPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	d := cronData{Site: site}
	v, err := s.ag.Cron(ctx, site.ID)
	if err != nil {
		d.Err = err.Error()
	}
	d.V = v
	for _, j := range v.Jobs {
		row := cronRow{CronJob: j, NextText: clock(j.Next)}
		if len(j.Runs) > 0 {
			row.Last = &j.Runs[0]
			row.LastText = clock(j.Runs[0].Start)
		}
		for i := range row.Runs {
			row.Runs[i].Output = strings.TrimSpace(row.Runs[i].Output)
		}
		d.Rows = append(d.Rows, row)
	}
	s.render(w, r, "cron", page{Title: "Cron · " + site.Domain, Active: "sites", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

func (s *Server) cronAction(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	f, u := r.PostForm, userOf(r)
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	base := "/cron/" + site.ID
	c := agent.CronChange{Op: f.Get("op"), ID: f.Get("id"), Schedule: strings.TrimSpace(f.Get("schedule")), Command: strings.TrimSpace(f.Get("command"))}
	if c.Op == "remove" && f.Get("confirm") != "1" {
		http.Redirect(w, r, base+"?err="+url.QueryEscape(s.tr.T(s.lang(r), "cron.confirm.need")), http.StatusSeeOther)
		return
	}
	detail := site.ID + " " + c.Op + " " + c.ID
	if c.Op == "add" || c.Op == "edit" {
		detail += " [" + c.Schedule + "]" // the command may carry a token: never in the audit log
	}
	id, changed, err := s.ag.ChangeCron(ctx, site.ID, c)
	if err != nil {
		s.audit1(u, r, "cron", detail+" failed: "+err.Error())
		http.Redirect(w, r, base+"?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	if !changed {
		http.Redirect(w, r, base+"?done=same", http.StatusSeeOther)
		return
	}
	if c.Op == "add" {
		detail = site.ID + " add " + id + " [" + c.Schedule + "]"
	}
	s.audit1(u, r, "cron", detail)
	http.Redirect(w, r, base+"?done="+url.QueryEscape(c.Op)+"#job-"+id, http.StatusSeeOther)
}
