package web

// Alerts by Telegram and e-mail, and the login history (OPS-01 wave 2). The panel checks the server once a minute
// (disk, memory, nightly backup), reads the agent's watchdog events, and hears of every sign-in. Each kind can be
// switched off; a condition is reported once when it starts and once when it is over.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/alert"
	"github.com/thuekx/docker-panel-VPS/panel/internal/store"
	"github.com/thuekx/docker-panel-VPS/panel/internal/sysinfo"
)

// AlertKinds are the alerts that can be switched on and off.
var AlertKinds = []string{"login", "watchdog", "disk", "memory", "backup", "certificate"}

const (
	kTgChat   = "alert.tg.chat"
	kTgToken  = "alert.tg.token" // sealed
	kMailHost = "alert.mail.host"
	kMailPort = "alert.mail.port"
	kMailUser = "alert.mail.user"
	kMailPass = "alert.mail.pass" // sealed
	kMailFrom = "alert.mail.from"
	kMailTo   = "alert.mail.to"
	kKindsOff = "alert.off" // kinds switched off, comma separated
	kWatchSeq = "alert.watch.seq"
	memBadFor = 5 // minutes of memory at its red level before an alert
)

type alertState struct {
	mu     sync.Mutex
	active map[string]bool
	memBad int
	// CERT-01: own certificates are looked at once an hour; one alert per certificate (website + end)
	certAt   time.Time
	certSent map[string]bool
}

// certWarnDays is how long before its end an own certificate is reported (Let's Encrypt renews by itself).
const certWarnDays = 14

// SetAlertKey gives the server the key that seals the alert secrets.
func (s *Server) SetAlertKey(k *alert.Key) { s.akey = k }

func (s *Server) kindOn(kind string) bool {
	for _, k := range strings.Split(s.st.Setting(kKindsOff, ""), ",") {
		if k == kind {
			return false
		}
	}
	return true
}

func (s *Server) telegram() (alert.Telegram, bool) {
	t := alert.Telegram{Chat: s.st.Setting(kTgChat, "")}
	if t.Chat == "" || s.akey == nil {
		return t, false
	}
	tok, err := s.akey.Open(s.st.Setting(kTgToken, ""))
	t.Token = tok
	return t, err == nil && tok != ""
}

func (s *Server) mailCfg() (alert.Mail, bool) {
	m := alert.Mail{Host: s.st.Setting(kMailHost, ""), User: s.st.Setting(kMailUser, ""), From: s.st.Setting(kMailFrom, "")}
	if m.Host == "" {
		return m, false
	}
	m.Port, _ = strconv.Atoi(s.st.Setting(kMailPort, "587"))
	for _, t := range strings.Split(s.st.Setting(kMailTo, ""), ",") {
		if t = strings.TrimSpace(t); t != "" {
			m.To = append(m.To, t)
		}
	}
	if s.akey != nil {
		m.Pass, _ = s.akey.Open(s.st.Setting(kMailPass, ""))
	}
	return m, true
}

// send delivers one alert on every channel that is set up and records it with the outcome per channel.
func (s *Server) send(ctx context.Context, orgID int64, kind, text string) string {
	lang := s.cfg.DefaultLang
	host := s.sys.Hostname
	full := "Nostek Panel · " + host + "\n" + text
	var res []string
	if t, ok := s.telegram(); ok {
		if err := alert.SendTelegram(ctx, t, full); err != nil {
			res = append(res, "Telegram: "+err.Error())
		} else {
			res = append(res, "Telegram: OK")
		}
	}
	if m, ok := s.mailCfg(); ok {
		subj := "Nostek Panel " + host + ": " + s.tr.T(lang, "alert.kind."+kind)
		if err := alert.SendMail(ctx, m, subj, full); err != nil {
			res = append(res, "e-mail: "+err.Error())
		} else {
			res = append(res, "e-mail: OK")
		}
	}
	sent := strings.Join(res, " · ")
	if sent == "" {
		sent = s.tr.T(lang, "alert.nochannel")
	}
	_ = s.st.AddAlert(orgID, kind, text, sent, s.now())
	return sent
}

// notify sends an alert of a kind that is switched on, in the background so a sign-in never waits for it.
func (s *Server) notify(orgID int64, kind, text string) {
	if !s.kindOn(kind) {
		return
	}
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s.send(ctx, orgID, kind, text)
	}
	if s.alertSync { // tests
		run()
		return
	}
	go run()
}

// recordLogin keeps one sign-in attempt and reports a successful one from a new address.
func (s *Server) recordLogin(u *store.User, r *http.Request, result string) {
	if u == nil {
		return
	}
	ip := s.clientIP(r)
	newIP, _ := s.st.RecordLogin(u.OrgID, u.ID, ip, r.UserAgent(), result, s.now())
	if newIP {
		s.notify(u.OrgID, "login", s.tr.T(s.cfg.DefaultLang, "alert.login", u.Email, ip, browser(r.UserAgent())))
	}
}

// browser names the browser and system of a User-Agent in a few words.
func browser(ua string) string {
	b, o := "", ""
	for _, x := range []struct{ k, n string }{{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"Firefox/", "Firefox"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"}, {"curl/", "curl"}} {
		if strings.Contains(ua, x.k) {
			b = x.n
			break
		}
	}
	for _, x := range []struct{ k, n string }{{"Android", "Android"}, {"iPhone", "iPhone"}, {"iPad", "iPad"}, {"Windows", "Windows"}, {"Mac OS X", "macOS"}, {"Linux", "Linux"}} {
		if strings.Contains(ua, x.k) {
			o = x.n
			break
		}
	}
	switch {
	case b != "" && o != "":
		return b + ", " + o
	case b != "" || o != "":
		return b + o
	}
	return "?"
}

// AlertTick checks the server and the watchdog once; AlertLoop runs it every minute.
func (s *Server) AlertTick(ctx context.Context) {
	org, err := s.st.FirstOrg()
	if err != nil || org == 0 {
		return
	}
	lang := s.cfg.DefaultLang
	snap := s.sys.Read(ctx)
	st := &s.alerts
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.active == nil {
		st.active = map[string]bool{}
	}
	flip := func(kind string, bad, good bool, onText, offText string) {
		switch {
		case bad && !st.active[kind]:
			st.active[kind] = true
			s.notify(org, kind, onText)
		case good && st.active[kind]:
			st.active[kind] = false
			s.notify(org, kind, offText)
		}
	}
	if snap.DiskStatus != sysinfo.Unknown && snap.DiskTotalGB > 0 {
		pctUsed := int(snap.DiskUsedGB * 100 / snap.DiskTotalGB)
		flip("disk", snap.DiskStatus == sysinfo.Bad, snap.DiskStatus == sysinfo.OK,
			s.tr.T(lang, "alert.disk", pctUsed, strconv.FormatFloat(snap.DiskUsedGB, 'f', 1, 64), strconv.FormatFloat(snap.DiskTotalGB, 'f', 1, 64)),
			s.tr.T(lang, "alert.disk.ok", pctUsed))
	}
	if snap.MemStatus == sysinfo.Bad {
		st.memBad++
	} else if snap.MemStatus != sysinfo.Unknown {
		st.memBad = 0
	}
	if snap.MemTotalMB > 0 {
		flip("memory", st.memBad >= memBadFor, snap.MemStatus == sysinfo.OK,
			s.tr.T(lang, "alert.memory", snap.MemUsedMB, snap.MemTotalMB, memBadFor), s.tr.T(lang, "alert.memory.ok"))
	}
	if snap.BackupStatus != sysinfo.Unknown {
		last := snap.BackupLast.In(s.loc()).Format("02 Jan 2006 15:04 MST")
		on := s.tr.T(lang, "alert.backup.failed", last)
		if snap.BackupStatus == sysinfo.Warn {
			on = s.tr.T(lang, "alert.backup.late", last)
		}
		flip("backup", snap.BackupStatus != sysinfo.OK, snap.BackupStatus == sysinfo.OK, on, s.tr.T(lang, "alert.backup.ok", last))
	}
	if s.ag == nil {
		return
	}
	actx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if now := s.now(); now.Sub(st.certAt) >= time.Hour {
		st.certAt = now
		s.certAlerts(actx, org, lang, now)
	}
	seqS := s.st.Setting(kWatchSeq, "")
	since, _ := strconv.ParseInt(seqS, 10, 64)
	v, err := s.ag.Watchdog(actx, since)
	if err != nil {
		return
	}
	if seqS != "" && v.Seq >= since { // the first look (or a reset agent) only takes the position: no old news
		for _, e := range v.Events {
			what := e.Service
			if e.Site != "" {
				what = e.Site + " (" + e.Service + ")"
			}
			text := s.tr.T(lang, "alert.watch.recovered", what)
			if e.Kind != "recovered" {
				text = s.tr.T(lang, "alert.watch."+e.Kind, what, s.tr.T(lang, "alert.reason."+strings.ReplaceAll(e.Reason, " ", "_")))
			}
			s.notify(org, "watchdog", text)
		}
	}
	_ = s.st.SetSetting(kWatchSeq, strconv.FormatInt(v.Seq, 10))
}

// certAlerts reports own certificates that end within certWarnDays (caller holds s.alerts.mu).
func (s *Server) certAlerts(ctx context.Context, org int64, lang string, now time.Time) {
	list, err := s.ag.Sites(ctx)
	if err != nil {
		return
	}
	st := &s.alerts
	if st.certSent == nil {
		st.certSent = map[string]bool{}
	}
	for _, site := range list {
		ci, err := s.ag.Cert(ctx, site.ID)
		if err != nil || !ci.Own || ci.NotAfter == 0 {
			continue
		}
		end := time.Unix(ci.NotAfter, 0)
		days := int(end.Sub(now).Hours() / 24)
		key := site.ID + "@" + strconv.FormatInt(ci.NotAfter, 10)
		if days > certWarnDays || st.certSent[key] {
			continue
		}
		st.certSent[key] = true
		text := s.tr.T(lang, "alert.cert", site.Domain, end.In(s.loc()).Format("02 Jan 2006"), days)
		if !end.After(now) {
			text = s.tr.T(lang, "alert.cert.ended", site.Domain, end.In(s.loc()).Format("02 Jan 2006"))
		}
		s.notify(org, "certificate", text)
	}
}

// AlertLoop runs AlertTick every minute until ctx ends.
func (s *Server) AlertLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.AlertTick(ctx)
		}
	}
}

// ---------- settings page: the alert part

type alertsView struct {
	TgChat   string
	TgSet    bool
	MailHost string
	MailPort int
	MailUser string
	MailPass bool
	MailFrom string
	MailTo   string
	Kinds    []kindOn
	Ports    []int
	Sent     []sentRow
	KeyOK    bool
}

type kindOn struct {
	Kind string
	On   bool
}

type sentRow struct{ At, Kind, Text, Sent string }

func (s *Server) alertsView(orgID int64) alertsView {
	v := alertsView{TgChat: s.st.Setting(kTgChat, ""), TgSet: s.st.Setting(kTgToken, "") != "", MailHost: s.st.Setting(kMailHost, ""),
		MailUser: s.st.Setting(kMailUser, ""), MailPass: s.st.Setting(kMailPass, "") != "", MailFrom: s.st.Setting(kMailFrom, ""),
		MailTo: s.st.Setting(kMailTo, ""), Ports: []int{587, 465, 2525}, KeyOK: s.akey != nil}
	v.MailPort, _ = strconv.Atoi(s.st.Setting(kMailPort, "587"))
	for _, k := range AlertKinds {
		v.Kinds = append(v.Kinds, kindOn{k, s.kindOn(k)})
	}
	list, _ := s.st.Alerts(orgID, 30)
	for _, a := range list {
		v.Sent = append(v.Sent, sentRow{a.At.In(s.loc()).Format("02 Jan 15:04"), a.Kind, a.Text, a.Sent})
	}
	return v
}

// alertsSubmit handles the alert forms of the settings page.
func (s *Server) alertsSubmit(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	lang := s.lang(r)
	f := r.PostForm
	back := func(flash, errMsg string) {
		if errMsg != "" {
			s.renderSettings(w, r, http.StatusUnprocessableEntity, errMsg)
			return
		}
		http.Redirect(w, r, "/settings?saved="+flash+"#alerts", http.StatusSeeOther)
	}
	if s.akey == nil {
		back("", s.tr.T(lang, "alert.nokey"))
		return
	}
	switch f.Get("op") {
	case "telegram":
		t := alert.Telegram{Chat: strings.TrimSpace(f.Get("chat")), Token: strings.TrimSpace(f.Get("token"))}
		if t.Token == "" { // keep the stored token
			old, ok := s.telegram()
			if !ok {
				back("", s.tr.T(lang, "alert.tg.need"))
				return
			}
			t.Token = old.Token
		}
		if err := alert.CheckTelegram(t); err != nil {
			back("", err.Error())
			return
		}
		sealed, err := s.akey.Seal(t.Token)
		if err != nil {
			http.Error(w, "internal error", 500)
			return
		}
		_ = s.st.SetSetting(kTgChat, t.Chat)
		_ = s.st.SetSetting(kTgToken, sealed)
		s.audit1(u, r, "alerts.telegram", "chat "+t.Chat)
		back("1", "")
	case "telegram-off":
		_ = s.st.SetSetting(kTgChat, "")
		_ = s.st.SetSetting(kTgToken, "")
		s.audit1(u, r, "alerts.telegram_off", "")
		back("1", "")
	case "mail":
		port, _ := strconv.Atoi(f.Get("port"))
		m := alert.Mail{Host: strings.ToLower(strings.TrimSpace(f.Get("host"))), Port: port, User: strings.TrimSpace(f.Get("user")),
			Pass: f.Get("pass"), From: strings.TrimSpace(f.Get("from"))}
		for _, t := range strings.Split(f.Get("to"), ",") {
			if t = strings.TrimSpace(t); t != "" {
				m.To = append(m.To, t)
			}
		}
		if m.Pass == "" { // keep the stored password
			old, _ := s.mailCfg()
			m.Pass = old.Pass
		}
		if err := alert.CheckMail(m); err != nil {
			back("", err.Error())
			return
		}
		sealed, err := s.akey.Seal(m.Pass)
		if err != nil {
			http.Error(w, "internal error", 500)
			return
		}
		for k, v := range map[string]string{kMailHost: m.Host, kMailPort: strconv.Itoa(m.Port), kMailUser: m.User, kMailPass: sealed, kMailFrom: m.From, kMailTo: strings.Join(m.To, ", ")} {
			_ = s.st.SetSetting(k, v)
		}
		s.audit1(u, r, "alerts.mail", m.Host+":"+strconv.Itoa(m.Port)+" to "+strings.Join(m.To, ", "))
		back("1", "")
	case "mail-off":
		for _, k := range []string{kMailHost, kMailPort, kMailUser, kMailPass, kMailFrom, kMailTo} {
			_ = s.st.SetSetting(k, "")
		}
		s.audit1(u, r, "alerts.mail_off", "")
		back("1", "")
	case "kinds":
		var off []string
		for _, k := range AlertKinds {
			if f.Get("k_"+k) != "1" {
				off = append(off, k)
			}
		}
		_ = s.st.SetSetting(kKindsOff, strings.Join(off, ","))
		s.audit1(u, r, "alerts.kinds", "off: "+strings.Join(off, ","))
		back("1", "")
	case "test":
		ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
		defer cancel()
		sent := s.send(ctx, u.OrgID, "test", s.tr.T(s.cfg.DefaultLang, "alert.test", u.Email))
		s.audit1(u, r, "alerts.test", sent)
		back("test", "")
	default:
		http.Error(w, "bad request", 400)
	}
}
