package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

// fakeAgent answers like nostek-agent for the routes the panel uses.
type fakeAgent struct {
	mu         sync.Mutex
	created    []agent.Create
	secret     string
	deleted    []string
	actions    []string
	stackDown  bool
	puts       []string // "path overwrite body" of every file write
	ops        []string // file operations
	sqls       []string
	remote     agent.RemoteDB // DB-02
	remoteSet  []string
	imported   string
	wpCalls    []string // "path body" of every WordPress call
	wpDown     bool
	wpJob      agent.Job // WP-02: the safe update job
	stgCopy    bool      // WP-02 wave 2: voltix-id has a staging copy in the list
	stgOf      string    // WP-02 wave 2: voltix-id is the staging copy of this website
	bakCalls   []string  // "method path body" of every backup call
	bakDown    bool
	bakJob     agent.Job
	sbkCalls   []string // BAK-02: "method path body" of every server backup call
	sbkCopy    string   // BAK-02: where the panel writes its database copy
	sbkSeen    []byte   // BAK-02: the start of that copy when the backup was asked
	sbkJob     agent.Job
	upd        agent.UpdateView // UPD-01
	updCalls   []string         // POST bodies
	updGets    []string
	srv        agent.ServerSecurity // SRV-01
	srvCalls   []string
	qFiles     []agent.QFile    // SEC-02
	plan       agent.BackupPlan // BAK-03
	sysCalls   []string         // OPS-02
	planCalls  []string
	qCalls     []string
	cert       agent.CertInfo // CERT-01
	certCalls  []string
	csr        agent.CertRequest // CERT-02: the waiting request
	dns        agent.DNSConfig   // CERT-01 wildcard: the ClouDNS sub-user
	dnsCalls   []string
	wcZones    []string // zones the fake site may take a wildcard of
	wcOn       string
	app        string               // APP-01: app of the fake site (empty = WordPress)
	protected  []agent.ProtectedDir // SEC-01 protected folders of the fake site (APP-03 wave 3 setup lock)
	appDB      bool
	trash      []agent.Trashed
	removed    []string
	keys       int
	repo       string // APP-02b: repository of the fake runtime site
	dbEngine   string // APP-02b: engine of the fake site database
	hookOn     bool   // APP-02c
	hooks      []agent.Hook
	hookCalls  []string
	imgOld     bool // IMG-01: the database runs an older image
	imgCalls   int
	limCalls   []string // RES-01: bodies of the limit changes
	grpCalls   []string // GRP-01: "method path body" of every group call
	cleans     []string // DISK-01: clean-up bodies
	diskPaths  []string // DISK-01: drill-down paths asked
	native     bool     // NATIVE-01: the server has the native engine
	engine     string   // NATIVE-01: engine of the fake site
	migCalls   []string // MIG-01: "method path body" of every migrate call
	migJob     *agent.Job
	domCalls   []string // DOM-01: bodies of the domain changes
	domJob     *agent.Job
	domSame    bool     // DOM-01: the next change changes nothing
	secCalls   []string // SEC-01: bodies of the security changes
	secJob     *agent.Job
	secScan    agent.Scan   // SEC-01 wave 3
	secLogins  agent.Logins // SEC-01 wave 3
	secWAF     agent.WAF    // SEC-01 wave 3b
	secWAFOK   bool
	sftp       agent.SFTP // SFTP-01
	sftpCalls  []string
	perf       agent.Perf // PHP-01
	perfCalls  []string
	perfJob    *agent.Job
	redis      *agent.RedisView // REDIS-01; nil = an agent without Redis routes
	redisCalls []string
	ion        *agent.IonCubeView // PHP-02; nil = an agent without ionCube routes
	ionCalls   []string
	term       agent.TerminalView // TERM-01
	termCalls  []string
	termSeen   chan string   // what the fake session got: query, then each message
	metrics    agent.Metrics // OPS-01
	cron       agent.Cron
	cronCalls  []string
	watch      agent.WatchView // OPS-01 wave 2
	watchAsk   []string
	usage      agent.Usage
}

func (f *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/sites/voltix-id/terminal/session" { // a long session must not hold the lock
		f.termSession(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	site := agent.Site{ID: "voltix-id", Domain: "voltix.id", Server: "nginx", PHP: "8.4", AdminUser: "admin-abc123",
		URL: "https://voltix-id.preview.nostek.dev", Phase: "ready", Status: "running",
		Hosts: []agent.Host{{Name: "voltix.id", Role: "primary"}, {Name: "voltix-id.preview.nostek.dev", Role: "preview", Routed: true}}}
	site.App, site.Database, site.DBEngine, site.Engine = f.app, f.appDB, f.dbEngine, f.engine
	site.StagingOf = f.stgOf
	if f.app == "laravel" {
		site.Version = "13"
	}
	if f.app == "static" {
		site.PHP, site.AdminUser = "", ""
	}
	if f.app == "go" || f.app == "python" {
		site.PHP, site.Server, site.AdminUser, site.Version, site.Source, site.Release = "", "", "", "1.27", "files", 1
	}
	if f.app == "n8n" || f.app == "uptime-kuma" || f.app == "ghost" {
		site.PHP, site.Server, site.AdminUser = "", "", ""
	}
	site.Protected = f.protected
	if f.app == "ghost" {
		site.DBEngine = "mysql"
	}
	if f.app == "node" {
		site.PHP, site.Server, site.AdminUser, site.Version = "", "", "", "24"
		site.Repo, site.Branch, site.Start, site.Release = "https://github.com/user/shop.git", "main", "node server.js", 3
		if f.repo != "" {
			site.Repo = f.repo
		}
	}
	write := func(code int, v any) { w.WriteHeader(code); json.NewEncoder(w).Encode(v) }
	if strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/files") || strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/db") {
		f.filesDB(w, r, write)
		return
	}
	if r.URL.Path == "/v1/dns" || r.URL.Path == "/v1/dns/delete" || r.URL.Path == "/v1/sites/voltix-id/wildcard" {
		f.dnsRoutes(w, r, write)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/cert") {
		b, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/cert/csr") && r.Method == "GET":
			write(200, f.csr)
		case strings.HasSuffix(r.URL.Path, "/cert/csr"):
			f.certCalls = append(f.certCalls, "CSR "+string(b))
			f.csr = agent.CertRequest{CSR: "-----BEGIN CERTIFICATE REQUEST-----\nMIIC\n-----END CERTIFICATE REQUEST-----\n", Names: []string{"voltix.id"}, KeyType: "ec256", Created: 1795700000}
			write(200, f.csr)
		case strings.HasSuffix(r.URL.Path, "/cert/csr/delete"):
			f.certCalls = append(f.certCalls, "CSR-DELETE")
			f.csr = agent.CertRequest{}
			write(200, map[string]bool{"ok": true})
		case strings.HasSuffix(r.URL.Path, "/cert/self-signed"):
			f.certCalls = append(f.certCalls, "SELF "+string(b))
			f.cert = agent.CertInfo{Own: true, SelfSign: true, Names: []string{"voltix.id"}, Issuer: "voltix.id", NotAfter: 1900000000}
			write(200, f.cert)
		case r.Method == "GET":
			write(200, f.cert)
		case r.Method == "PUT" && strings.Contains(string(b), `"key":""`) && f.csr.CSR == "":
			write(400, map[string]string{"error": "bad input: the private key is needed: this certificate does not belong to the waiting request"})
		case r.Method == "PUT" && strings.Contains(string(b), "BAD"):
			write(400, map[string]string{"error": "bad input: the private key does not belong to the certificate"})
		case r.Method == "PUT":
			f.certCalls = append(f.certCalls, "PUT "+string(b))
			f.cert = agent.CertInfo{Own: true, Names: []string{"voltix.id"}, Issuer: "Origin CA", NotAfter: 1900000000}
			write(200, f.cert)
		default:
			f.certCalls = append(f.certCalls, "POST "+r.URL.Path)
			f.cert = agent.CertInfo{}
			write(200, map[string]bool{"ok": true})
		}
		return
	}
	if r.URL.Path == "/v1/server-security" || strings.HasPrefix(r.URL.Path, "/v1/firewall") || strings.HasPrefix(r.URL.Path, "/v1/fail2ban") {
		f.serverSec(w, r, write)
		return
	}
	if r.URL.Path == "/v1/update" {
		f.update(w, r, write)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/server/") || strings.HasSuffix(r.URL.Path, "/drift/fix") {
		f.system(r, write)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/backup-plan") || strings.HasSuffix(r.URL.Path, "/send") {
		f.backupPlan(r, write)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/server-backups") || strings.HasPrefix(r.URL.Path, "/v1/jobs/j5bc") {
		f.serverBackups(w, r, write)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/backups") || strings.HasPrefix(r.URL.Path, "/v1/jobs/jbac") {
		f.backups(w, r, write)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/migrate") || strings.HasPrefix(r.URL.Path, "/v1/jobs/jmig") {
		f.migrate(w, r, write)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/domains") || strings.HasPrefix(r.URL.Path, "/v1/jobs/jddd") {
		f.domains(w, r, write)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/sftp") { // SFTP-01
		if r.Method == "POST" {
			b, _ := io.ReadAll(r.Body)
			f.sftpCalls = append(f.sftpCalls, strings.TrimSpace(string(b)))
			if strings.Contains(string(b), `"name":"taken"`) {
				write(400, map[string]string{"error": "bad input: the name taken is taken on this server"})
				return
			}
			write(200, map[string]bool{"changed": !strings.Contains(string(b), `"op":"dir-set","name":"ana","dir":"/"`)})
			return
		}
		write(200, f.sftp)
		return
	}
	if r.URL.Path == "/v1/watchdog" {
		f.watchAsk = append(f.watchAsk, r.URL.RawQuery)
		write(200, f.watch)
		return
	}
	if r.URL.Path == "/v1/usage" || r.URL.Path == "/v1/sites/voltix-id/usage" {
		write(200, f.usage)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/metrics") || strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/cron") {
		f.opsAPI(r, write)
		return
	}
	if r.URL.Path == "/v1/sites/voltix-id/terminal" {
		if r.Method == "POST" {
			b, _ := io.ReadAll(r.Body)
			f.termCalls = append(f.termCalls, strings.TrimSpace(string(b)))
			f.term.On = strings.Contains(string(b), "true")
			write(200, map[string]bool{"ok": true})
			return
		}
		write(200, f.term)
		return
	}
	if r.URL.Path == "/v1/sites/voltix-id/ioncube" {
		f.ionAPI(r, write)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/redis") {
		f.redisAPI(r, write)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/perf") || strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/php") || strings.HasPrefix(r.URL.Path, "/v1/jobs/jfeed") {
		f.perfAPI(r, write)
		return
	}
	if r.URL.Path == "/v1/sites/voltix-id/quarantine" || strings.HasPrefix(r.URL.Path, "/v1/quarantine") {
		f.quarantine(w, r, write)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/security") || strings.HasPrefix(r.URL.Path, "/v1/jobs/jsec") {
		f.security(w, r, write)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/wp") || strings.HasPrefix(r.URL.Path, "/v1/jobs/jc0de") {
		f.wordpress(w, r, write)
		return
	}
	switch {
	case r.URL.Path == "/v1/catalog":
		write(200, agent.Catalog{PHP: []agent.PHP{{Version: "8.1", EOL: true}, {Version: "8.4", Default: true, SecurityUntil: "2028-12-31"}},
			Laravel: []agent.Laravel{{Version: "13", MinPHP: "8.3", Default: true}, {Version: "12", MinPHP: "8.2"}},
			Node:    []agent.Node{{Version: "26", SecurityUntil: "2029-04-30"}, {Version: "24", SecurityUntil: "2028-04-30", Default: true}},
			Go:      []agent.Node{{Version: "1.27", SecurityUntil: "2027-08-31", Default: true}},
			Python:  []agent.Node{{Version: "3.14", SecurityUntil: "2030-10-31", Default: true}},
			Ready:   []agent.Ready{{ID: "uptime-kuma", Name: "Uptime Kuma", Version: "2.5.11-slim-rootless"}, {ID: "n8n", Name: "n8n", Version: "2.47.7"}, {ID: "ghost", Name: "Ghost", Version: "6.79.0-alpine"}},
			Native:  map[bool]*agent.Native{true: {PHP: []string{"8.3", "8.4"}}}[f.native]})
	case r.URL.Path == "/v1/sites" && r.Method == "GET":
		list := []agent.Site{site}
		if f.stgCopy {
			list = append(list, agent.Site{ID: "staging-voltix-id", Domain: "staging.voltix.id", URL: "https://staging-voltix-id.preview.nostek.dev", Phase: "ready", StagingOf: "voltix-id"})
		}
		write(200, map[string]any{"sites": list})
	case r.URL.Path == "/v1/sites/shop-id" && f.stgOf == "shop-id":
		write(200, agent.Site{ID: "shop-id", Domain: "shop.id", Phase: "ready"})
	case r.URL.Path == "/v1/sites/voltix-id/staging":
		write(202, map[string]any{"job": "jc0de000000000002", "site": agent.Site{ID: "staging-voltix-id", StagingOf: "voltix-id"}})
	case strings.HasPrefix(r.URL.Path, "/v1/sites/") && strings.HasSuffix(r.URL.Path, "/staging/push"):
		b, _ := io.ReadAll(r.Body)
		f.wpCalls = append(f.wpCalls, r.URL.Path+" "+strings.TrimSpace(string(b)))
		write(202, map[string]string{"job": "jc0de000000000003"})
	case r.URL.Path == "/v1/trash":
		t := f.trash
		if t == nil {
			t = []agent.Trashed{}
		}
		write(200, map[string]any{"trash": t})
	case r.URL.Path == "/v1/deploykeys" && r.Method == "POST":
		f.keys++
		write(200, agent.DeployKey{ID: "0123456789abcdef", Public: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey nostek-panel-deploy-0123456789abcdef", Fingerprint: "SHA256:testfp"})
	case r.URL.Path == "/v1/sites/voltix-id/images":
		cur := "mariadb:11.8@sha256:c4124d1afa875e47f2892edf26b482fa54f796e2f342d3717a04ca091a22a9bd"
		img := agent.Images{Services: []agent.ImageUse{
			{Service: "db", Current: cur, Wanted: cur},
			{Service: "web", Current: "nginx:1.30-alpine@sha256:583b7f2fce1cdb8da5ad5c39ebd3c33b98b87d493190e548bdee1fa8c619c311", Wanted: "nginx:1.30-alpine@sha256:583b7f2fce1cdb8da5ad5c39ebd3c33b98b87d493190e548bdee1fa8c619c311"}}}
		if f.imgOld {
			img.Services[0].Current, img.Services[0].Update, img.Update = "sha256:0ld0ld0ld0ld0ld0ld", true, true
		}
		write(200, img)
	case r.URL.Path == "/v1/sites/voltix-id/disk":
		p := r.URL.Query().Get("path")
		f.diskPaths = append(f.diskPaths, p)
		if p == "/nope" {
			write(404, map[string]string{"error": "no such file or folder"})
			return
		}
		write(200, agent.SiteDisk{Files: 60100, Database: 20000, Backups: 9000, Trash: 400, Other: 500, Total: 90000, Path: p,
			Rows: []agent.DiskRow{{Name: "wp-content", Type: "dir", Bytes: 60000, Files: 3}, {Name: "index.php", Type: "file", Bytes: 100, Files: 1}, {Name: "link", Type: "link"}},
			Big:  []agent.BigFile{{Path: "/wp-content/uploads/2026/video.mp4", Bytes: 50000, MTime: 1763251200}}})
	case r.URL.Path == "/v1/disk" && r.Method == "GET":
		write(200, agent.ServerDisk{DiskBytes: 90 << 30, UsedBytes: 20 << 30, FreeBytes: 70 << 30, Trash: 1 << 20,
			Sites:  []agent.SiteTotal{{ID: "voltix-id", Domain: "voltix.id", Bytes: 3 << 30}},
			Docker: []agent.DockerUse{{Kind: "images", Count: 9, Bytes: 3e9, Reclaimable: 1.2e9}, {Kind: "build-cache", Count: 0}}})
	case r.URL.Path == "/v1/disk/clean":
		b, _ := io.ReadAll(r.Body)
		f.cleans = append(f.cleans, string(b))
		write(200, map[string]string{"freed": "2 images, 1.2 GB"})
	case r.URL.Path == "/v1/groups":
		write(200, map[string]any{"groups": []agent.Group{{Name: "a1", GroupLimits: agent.GroupLimits{CPU: 100, Memory: 1024, Tasks: 256}, Sites: []string{"voltix-id"}, MemUsed: 300}}})
	case strings.HasPrefix(r.URL.Path, "/v1/groups/") || r.URL.Path == "/v1/sites/voltix-id/group":
		b, _ := io.ReadAll(r.Body)
		f.grpCalls = append(f.grpCalls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(b))))
		write(200, map[string]bool{"ok": true})
	case r.URL.Path == "/v1/sites/voltix-id/limits" && r.Method == "GET":
		write(200, agent.Limits{Services: []agent.ServiceRAM{{Service: "db", Standard: 512, Current: 1024}, {Service: "php", Standard: 512, Current: 512, PHP: true}, {Service: "web", Standard: 128, Current: 128}},
			RAMChoice: []int{768, 1024, 1536, 2048}, PHP: &agent.PHPSettings{Memory: 256, Upload: 512, ExecTime: 120, InputVars: 1000},
			PHPStd:    agent.PHPSettings{Memory: 256, Upload: 128, ExecTime: 120, InputVars: 1000},
			PHPChoice: map[string][]int{"memory_mb": {256, 512}, "upload_mb": {128, 512}, "exec_seconds": {120, 300}, "max_input_vars": {1000, 3000}},
			HostMB:    1024, TotalMB: 1664})
	case r.URL.Path == "/v1/sites/voltix-id/limits":
		b, _ := io.ReadAll(r.Body)
		f.limCalls = append(f.limCalls, string(b))
		if strings.Contains(string(b), `"db":256`) {
			write(400, map[string]string{"error": "memory of db can be the standard 512 MB or one of [768 1024 1536 2048] MB above it"})
			return
		}
		write(200, map[string]bool{"ok": true})
	case r.URL.Path == "/v1/sites/voltix-id/images/update":
		f.imgCalls++
		if !f.imgOld {
			write(400, map[string]string{"error": "the website already runs the newest images of this panel version"})
			return
		}
		write(202, map[string]string{"job": "jabcdef0123456789"})
	case r.URL.Path == "/v1/jobs/jabcdef0123456789":
		write(200, agent.Job{ID: "jabcdef0123456789", Kind: "image", Site: "voltix-id", State: "done", Steps: []string{"pull new images", "copy database", "copy files", "recreate containers", "done before-update-20261114-080000.tar.gz"}})
	case r.URL.Path == "/v1/sites/voltix-id/webhook":
		write(200, agent.Webhook{On: f.hookOn, Branch: "main", Deliveries: []agent.Delivery{{At: 1700000000, Event: "push", Result: "deploy", Commit: "0123456789abcdef0123456789abcdef01234567", Job: "j0123456789abcdef"}}})
	case r.URL.Path == "/v1/sites/voltix-id/webhook/secret":
		f.hookOn = true
		f.hookCalls = append(f.hookCalls, "secret")
		write(200, map[string]string{"secret": "5ec2e75ec2e75ec2e75ec2e75ec2e75ec2e75ec2e75ec2e75ec2e75ec2e75ec2"})
	case r.URL.Path == "/v1/sites/voltix-id/webhook/remove":
		f.hookOn = false
		f.hookCalls = append(f.hookCalls, "remove")
		write(200, map[string]any{"ok": true})
	case r.URL.Path == "/v1/sites/voltix-id/webhook/deliver":
		var h agent.Hook
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &h)
		f.hooks = append(f.hooks, h)
		if h.Signature == "sha256=bad" {
			write(401, map[string]string{"error": "the delivery is not signed with this website's secret"})
			return
		}
		write(200, agent.Delivery{Event: h.Event, Result: "deploy", Job: "j0123456789abcdef"})
	case strings.HasPrefix(r.URL.Path, "/v1/sites/") && strings.HasSuffix(r.URL.Path, "/webhook/deliver"):
		write(404, map[string]string{"error": "auto-deploy is off for this website"})
	case r.URL.Path == "/v1/sites/voltix-id/deploykey":
		write(200, agent.DeployKey{Public: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey nostek-panel-deploy-0123456789abcdef", Fingerprint: "SHA256:testfp", HostKeys: []string{"ssh-ed25519 SHA256:hostfp"}})
	case strings.HasPrefix(r.URL.Path, "/v1/trash/") && strings.HasSuffix(r.URL.Path, "/remove"):
		f.removed = append(f.removed, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/trash/"), "/remove"))
		write(200, map[string]any{"ok": true})
	case r.URL.Path == "/v1/sites/plan", r.URL.Path == "/v1/sites" && r.Method == "POST":
		var c agent.Create
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &c)
		if c.Server != "apache" && c.Server != "nginx" && c.App != "node" && c.App != "go" && c.App != "python" && c.App != "n8n" && c.App != "uptime-kuma" && c.App != "ghost" {
			write(422, map[string]string{"error": "web server must be apache or nginx"})
			return
		}
		if r.URL.Path == "/v1/sites/plan" {
			site.App, site.Engine = c.App, c.Engine
			if site.IsNode() || c.App == "php" || c.App == "laravel" {
				site.Database, site.DBEngine = c.Database, c.DBEngine
			}
			write(200, site)
			return
		}
		f.created = append(f.created, c)
		write(202, map[string]any{"job": "j0123456789abcdef", "site": site})
	case r.URL.Path == "/v1/jobs/j0123456789abcdef":
		write(200, agent.Job{ID: "j0123456789abcdef", Site: "voltix-id", State: "done", Steps: []string{"files", "install WordPress"}, HasSecret: f.secret != ""})
	case r.URL.Path == "/v1/jobs/j0123456789abcdef/secret":
		if f.secret == "" {
			write(410, map[string]string{"error": "secret already shown"})
			return
		}
		s := f.secret
		f.secret = ""
		write(200, map[string]string{"password": s})
	case r.URL.Path == "/v1/sites/voltix-id/stack":
		if f.stackDown {
			write(502, map[string]string{"error": "cannot read containers: docker not reachable"})
			return
		}
		write(200, agent.Stack{Project: "np-site-voltix-id", Containers: []agent.Container{
			{Service: "web", Name: "np-site-voltix-id-web-1", Image: "nginx:1.30-alpine", State: "running", CPU: 0.4, MemBytes: 8 << 20, MemLimit: 256 << 20, Started: 1700000000 - 7200},
			{Service: "php", Name: "np-site-voltix-id-php-1", Image: "wordpress:7.1.4-php8.4-fpm", State: "exited", CPU: -1, MemBytes: -1, Restarts: 3},
			{Service: "db", Name: "np-site-voltix-id-db-1", Image: "mariadb:11.8", State: "running", Health: "unhealthy", CPU: 1.5, MemBytes: 120 << 20, MemLimit: 512 << 20, Started: 1700000000 - 600}}})
	case r.URL.Path == "/v1/sites/voltix-id" && r.Method == "GET":
		write(200, site)
	case r.URL.Path == "/v1/sites/voltix-id/delete":
		f.deleted = append(f.deleted, "voltix-id")
		write(200, map[string]any{"trash": "voltix-id.1700000000", "days": 7})
	case r.URL.Path == "/v1/sites/voltix-id/deploys" && r.Method == "GET":
		write(200, []agent.Deploy{
			{Release: 3, Started: 1700000000, Ended: 1700000095, Commit: "0123456789abcdef0123456789abcdef01234567", Message: "Checkout <b>v2</b>", Result: "live"},
			{Release: 2, Started: 1699990000, Ended: 1699990030, Result: "failed", Error: "build failed: see the build log"},
			{Release: 1, Started: 1699980000, Ended: 1699980090, Commit: "89abcdef0123456789abcdef0123456789abcdef", Message: "First", Result: "ok"}})
	case r.URL.Path == "/v1/sites/voltix-id/deploys" && r.Method == "POST":
		f.actions = append(f.actions, "deploy")
		write(202, map[string]string{"job": "j0123456789abcdef"})
	case r.URL.Path == "/v1/sites/voltix-id/releases" && r.Method == "GET":
		write(200, agent.StaticReleases{Keep: 3, MaxBytes: 512 << 20, Releases: []agent.StaticRelease{
			{Release: 3, At: 1700000000, Name: "dist <b>.zip", Files: 12, Bytes: 3 << 20, Result: "live"},
			{Release: 2, At: 1699990000, Name: "old.zip", Files: 10, Bytes: 2 << 20, Result: "ok"},
			{Release: 0, At: 1699980000, Result: "removed"}}})
	case r.URL.Path == "/v1/sites/voltix-id/releases" && r.Method == "PUT":
		b, _ := io.ReadAll(r.Body)
		f.actions = append(f.actions, "publish "+r.URL.Query().Get("name")+" "+string(b))
		write(200, agent.StaticRelease{Release: 4, Name: r.URL.Query().Get("name"), Files: 1, Result: "live"})
	case r.URL.Path == "/v1/sites/voltix-id/releases/2/rollback":
		f.actions = append(f.actions, "static-rollback 2")
		write(200, agent.StaticRelease{Release: 2, Result: "live"})
	case r.URL.Path == "/v1/sites/voltix-id/deploys/2/log":
		write(200, map[string]string{"log": "npm ERR! missing script: build <script>"})
	case r.URL.Path == "/v1/sites/voltix-id/env" && r.Method == "GET":
		write(200, map[string][]string{"names": {"API_KEY", "DATABASE_URL"}})
	case r.URL.Path == "/v1/sites/voltix-id/env" && r.Method == "POST":
		b, _ := io.ReadAll(r.Body)
		f.actions = append(f.actions, "env "+string(b))
		write(200, map[string]bool{"ok": true})
	case r.URL.Path == "/v1/sites/voltix-id/laravel/artisan":
		b, _ := io.ReadAll(r.Body)
		f.actions = append(f.actions, "artisan "+string(b))
		if f.app != "laravel" {
			write(400, map[string]string{"error": "this website does not run Laravel"})
			return
		}
		write(200, map[string]string{"output": "INFO  Nothing to migrate. <b>x</b>"})
	case strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/") && r.Method == "POST":
		f.actions = append(f.actions, strings.TrimPrefix(r.URL.Path, "/v1/sites/voltix-id/"))
		write(200, map[string]bool{"ok": true})
	default:
		write(404, map[string]string{"error": "not found"})
	}
}

func (e *env) withAgent() *fakeAgent {
	f := &fakeAgent{secret: "Pw-For-Test-Only-0123456"}
	ts := httptest.NewServer(f)
	e.t.Cleanup(ts.Close)
	u, _ := url.Parse(ts.URL)
	e.srv.SetAgent(agent.NewHTTP(&http.Client{Transport: rewrite{u}}))
	return f
}

type rewrite struct{ u *url.URL }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme, req.URL.Host = r.u.Scheme, r.u.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestSitesNeedLoginAndShowNoAgent(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/sites", "/sites/new", "/sites/voltix-id", "/sites/job/j0123456789abcdef"} {
		if code, _, h := e.get(e.c, p); code != 303 || h.Get("Location") != "/login" {
			t.Errorf("%s reachable without login: %d", p, code)
		}
	}
	e.setupAdmin(e.c, "admin@example.test")
	code, body, _ := e.get(e.c, "/sites")
	if code != 200 || !strings.Contains(body, "not reachable") || strings.Contains(body, "s-ok") {
		t.Fatalf("no agent must show a warning and never green: %d", code)
	}
}

func TestSiteCreateApproveAndPasswordOnce(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")

	_, body, _ := e.get(e.c, "/sites/new")
	if !strings.Contains(body, "admin@example.test") || !strings.Contains(body, "end of life") {
		t.Fatal("form lacks default e-mail or EOL label")
	}
	form := url.Values{"step": {"plan"}, "domain": {" Voltix.ID "}, "primary": {"domain"}, "server": {"nginx"}, "php": {"8.4"},
		"admin_email": {"admin@example.test"}, "title": {"Voltix"}, "aliases": {"voltix.co.id, VOLTIX.net"}}
	code, body, _ := e.postWith(e.c, "/sites/new", form, body)
	if code != 200 || !strings.Contains(body, "Approve and create") || !strings.Contains(body, "waiting for DNS") {
		t.Fatalf("approve card: %d", code)
	}
	if len(f.created) != 0 {
		t.Fatal("plan step created a site")
	}
	form.Set("step", "create")
	code, _, h := e.postWith(e.c, "/sites/new", form, body)
	if code != 303 || h.Get("Location") != "/sites/job/j0123456789abcdef" {
		t.Fatalf("create: %d %s", code, h.Get("Location"))
	}
	c := f.created[0]
	if c.Domain != "voltix.id" || len(c.Aliases) != 2 || c.Aliases[1] != "voltix.net" {
		t.Fatalf("request sent = %+v", c)
	}

	_, body, _ = e.get(e.c, "/sites/job/j0123456789abcdef")
	if !strings.Contains(body, "Show admin password") || strings.Contains(body, "Pw-For-Test-Only") {
		t.Fatal("job page must offer the password without showing it")
	}
	// the starter sees the password exactly once
	_, body2, _ := e.post(e.c, "/sites/job/j0123456789abcdef/secret", url.Values{}, "/sites/job/j0123456789abcdef")
	if !strings.Contains(body2, "Pw-For-Test-Only-0123456") {
		t.Fatal("password not shown on the first request")
	}
	_, body3, _ := e.post(e.c, "/sites/job/j0123456789abcdef/secret", url.Values{}, "/sites/job/j0123456789abcdef")
	if strings.Contains(body3, "Pw-For-Test-Only") {
		t.Fatal("password shown twice")
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "site.create") || !strings.Contains(audit, "site.password_shown") || strings.Contains(audit, "Pw-For-Test-Only") {
		t.Fatal("audit wrong or holds the password")
	}
}

func TestSiteBadInputAndForeignJob(t *testing.T) {
	e := newEnv(t)
	e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	form := url.Values{"step": {"plan"}, "domain": {"voltix.id"}, "primary": {"domain"}, "server": {"caddy"}, "php": {"8.4"},
		"admin_email": {"admin@example.test"}, "title": {"Voltix"}}
	code, body, _ := e.post(e.c, "/sites/new", form, "/sites/new")
	if code != 422 || !strings.Contains(body, "apache or nginx") {
		t.Fatalf("bad server: %d", code)
	}
	// a job the user did not start: no password button and the secret route answers 404
	_, body, _ = e.get(e.c, "/sites/job/j0123456789abcdef")
	if strings.Contains(body, "Show admin password") {
		t.Fatal("foreign job offers the password")
	}
	if code, _, _ := e.post(e.c, "/sites/job/j0123456789abcdef/secret", url.Values{}, "/sites"); code != 404 {
		t.Fatalf("foreign secret: %d", code)
	}
	for _, p := range []string{"/sites/job/..%2fetc", "/sites/job/jXYZ", "/sites/UPPER", "/sites/a--b.c"} {
		if code, _, _ := e.get(e.c, p); code != 404 {
			t.Errorf("%s answered %d", p, code)
		}
	}
}

func TestSiteActionsAndDeleteConfirm(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	code, _, h := e.post(e.c, "/sites/voltix-id", url.Values{"action": {"restart"}}, "/sites/voltix-id")
	if code != 303 || !strings.Contains(h.Get("Location"), "done=restart") || f.actions[0] != "restart" {
		t.Fatalf("restart: %d %s %v", code, h.Get("Location"), f.actions)
	}
	if code, _, _ := e.post(e.c, "/sites/voltix-id", url.Values{"action": {"exec"}}, "/sites/voltix-id"); code != 400 {
		t.Fatalf("unknown action: %d", code)
	}
	e.post(e.c, "/sites/voltix-id", url.Values{"action": {"delete"}, "confirm": {"nostek.id"}}, "/sites/voltix-id")
	if len(f.deleted) != 0 {
		t.Fatal("delete ran with a wrong confirmation")
	}
	code, _, h = e.post(e.c, "/sites/voltix-id", url.Values{"action": {"delete"}, "confirm": {"Voltix.id"}}, "/sites/voltix-id")
	if code != 303 || h.Get("Location") != "/sites?done=delete" || len(f.deleted) != 1 {
		t.Fatalf("delete: %d %s", code, h.Get("Location"))
	}
	// POST without CSRF is refused
	resp, _ := e.c.PostForm(e.ts.URL+"/sites/voltix-id", url.Values{"action": {"stop"}})
	if resp.StatusCode != 403 {
		t.Fatalf("no csrf: %d", resp.StatusCode)
	}
	_, body, _ := e.get(e.c, "/sites")
	if !strings.Contains(body, "voltix.id") || !strings.Contains(body, "Running") {
		t.Fatal("list does not show the site")
	}
}

func TestSiteStackShownHonestly(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	if code, _, _ := e.get(e.c, "/stack/voltix-id"); code != 303 {
		t.Fatal("stack JSON reachable without login")
	}
	e.setupAdmin(e.c, "admin@example.test")
	code, body, _ := e.get(e.c, "/sites/voltix-id")
	if code != 200 || !strings.Contains(body, "np-site-voltix-id-php-1") || !strings.Contains(body, "1/3 running") {
		t.Fatalf("site page: %d", code)
	}
	// php stopped and db unhealthy are never green; only web is
	if strings.Count(body, `class="pill s-ok" data-f="state"`) != 1 || !strings.Contains(body, `class="pill s-bad" data-f="state">Stopped`) || !strings.Contains(body, "Running · unhealthy") {
		t.Fatal("container states not shown honestly")
	}
	if !strings.Contains(body, "8 MB / 256 MB") || !strings.Contains(body, "2h 0m") {
		t.Fatal("usage or uptime missing")
	}
	code, js, h := e.get(e.c, "/stack/voltix-id")
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "application/json") || !strings.Contains(js, `"mem_pct":23`) {
		t.Fatalf("stack JSON: %d %s", code, js)
	}
	_, list, _ := e.get(e.c, "/sites")
	if !strings.Contains(list, "Containers") {
		t.Fatal("list lacks the containers column")
	}
	// restart one container: only known services, audited
	_, page, _ := e.get(e.c, "/sites/voltix-id")
	for _, bad := range []string{"cli", "edge", ""} {
		if code, _, _ := e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"restart-service"}, "service": {bad}}, page); code != 400 {
			t.Errorf("service %q accepted: %d", bad, code)
		}
	}
	code, _, h = e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"restart-service"}, "service": {"php"}}, page)
	if code != 303 || h.Get("Location") != "/sites/voltix-id?done=restart_php" {
		t.Fatalf("restart php: %d %s", code, h.Get("Location"))
	}
	if f.actions[len(f.actions)-1] != "services/php/restart" {
		t.Fatalf("agent call: %v", f.actions)
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "site.restart_php") {
		t.Fatal("restart not audited")
	}
	// agent cannot read docker: unknown, never green
	f.stackDown = true
	_, body, _ = e.get(e.c, "/sites/voltix-id")
	if !strings.Contains(body, "cannot be read") || strings.Contains(body, `data-stack-count`) {
		t.Fatal("docker failure must show unknown")
	}
	if code, _, _ := e.get(e.c, "/stack/voltix-id"); code != 502 {
		t.Fatalf("stack JSON on failure: %d", code)
	}
}
