// Command nostek-agent runs on the host as root and is the only part of Nostek Panel that may change Docker.
// It accepts a fixed set of site actions on a Unix socket that only the panel's user can open
// (SITE-DOCKER-1 = A). Subcommands: serve | purge | check-config | version | server-restore (BAK-02).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/api"
	"github.com/thuekx/docker-panel-VPS/agent/internal/audit"
	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

// Version is set at build time (-ldflags "-X main.Version=v1.2.0").
var Version = "dev"

// Config is /etc/nostek-agent/config.json.
type Config struct {
	Data        string `json:"data"`
	PublicIPv4  string `json:"public_ipv4"`
	PanelDomain string `json:"panel_domain"`
	PreviewZone string `json:"preview_zone"`
	Socket      string `json:"socket"`
	SocketGID   int    `json:"socket_gid"`
	AllowedUIDs []int  `json:"allowed_uids"`
	Audit       string `json:"audit"`
	// MIG-01 test harness only: networks a migration may reach although they are private, and a CA it trusts.
	// A real server never sets them.
	MigrateTestAllow []string `json:"migrate_test_allow,omitempty"`
	MigrateTestCA    string   `json:"migrate_test_ca,omitempty"`
	// SEC-01 test harness only: a folder of test signatures for the malware scan (no freshclam).
	ClamTestDB string `json:"clam_test_db,omitempty"`
}

func loadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	if !filepath.IsAbs(c.Data) || !filepath.IsAbs(c.Socket) || (c.Audit != "" && !filepath.IsAbs(c.Audit)) {
		return c, errors.New("config: data, socket and audit must be absolute paths")
	}
	if ip := net.ParseIP(c.PublicIPv4); ip == nil || ip.To4() == nil {
		return c, errors.New("config: public_ipv4 must be an IPv4 address")
	}
	if err := spec.Domain(c.PanelDomain); err != nil {
		return c, fmt.Errorf("config: panel_domain: %w", err)
	}
	if c.PreviewZone != "" {
		if err := spec.Domain(c.PreviewZone); err != nil {
			return c, fmt.Errorf("config: preview_zone: %w", err)
		}
	}
	if len(c.AllowedUIDs) == 0 {
		return c, errors.New("config: allowed_uids is empty")
	}
	for _, p := range c.MigrateTestAllow {
		if _, err := netip.ParsePrefix(p); err != nil {
			return c, fmt.Errorf("config: migrate_test_allow: %w", err)
		}
	}
	if c.MigrateTestCA != "" && !filepath.IsAbs(c.MigrateTestCA) {
		return c, errors.New("config: migrate_test_ca must be an absolute path")
	}
	if c.ClamTestDB != "" && !filepath.IsAbs(c.ClamTestDB) {
		return c, errors.New("config: clam_test_db must be an absolute path")
	}
	return c, nil
}

func main() {
	cfgPath := "/etc/nostek-agent/config.json"
	if v := os.Getenv("NOSTEK_AGENT_CONFIG"); v != "" {
		cfgPath = v
	}
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if cmd == "version" {
		fmt.Println(Version)
		return
	}
	// BAK-02: checking a server backup needs no configuration, so the installer can run it before anything is set up
	if cmd == "server-restore" && len(os.Args) > 2 && os.Args[2] == "check" {
		os.Exit(serverRestore(nil, os.Args[2:]))
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	sc := sites.Config{Data: cfg.Data, PublicIPv4: cfg.PublicIPv4, PanelDomain: cfg.PanelDomain,
		PreviewZone: cfg.PreviewZone, MigrateTestCA: cfg.MigrateTestCA, ClamTestDB: cfg.ClamTestDB}
	for _, p := range cfg.MigrateTestAllow {
		sc.MigrateTestAllow = append(sc.MigrateTestAllow, netip.MustParsePrefix(p))
	}
	if len(sc.MigrateTestAllow) > 0 || sc.MigrateTestCA != "" || sc.ClamTestDB != "" {
		fmt.Fprintln(os.Stderr, "warning: migrate_test_allow / migrate_test_ca / clam_test_db are set: test harness only")
	}
	m := sites.New(sc, sites.ExecRunner{}, nil)
	m.UseFirewall(sites.ExecHost{}) // DB-02: ufw rules of the database ports
	// NATIVE-01: native.json next to the config turns the native engine on; a broken one leaves Docker only
	if nc, err := sites.LoadNative(filepath.Join(filepath.Dir(cfgPath), "native.json")); err != nil {
		fmt.Fprintln(os.Stderr, "native engine off:", err)
	} else if nc != nil {
		m.EnableNative(nc, sites.ExecHost{}, sites.NativePaths{Etc: "/etc", Run: "/run", Data: cfg.Data})
	}
	switch cmd {
	case "check-config":
		fmt.Println("config OK")
	case "purge":
		gone, err := m.Purge()
		for _, g := range gone {
			fmt.Println("purged", g)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "server-restore":
		os.Exit(serverRestore(m, os.Args[2:]))
	case "serve":
		if err := serve(cfg, m); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: nostek-agent serve|purge|check-config|version|server-restore")
		os.Exit(2)
	}
}

func serve(cfg Config, m *sites.Manager) error {
	if err := os.MkdirAll(filepath.Dir(cfg.Socket), 0o750); err != nil {
		return err
	}
	if err := os.Chown(filepath.Dir(cfg.Socket), 0, cfg.SocketGID); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(cfg.Socket), 0o750); err != nil {
		return err
	}
	_ = os.Remove(cfg.Socket)
	old := syscall.Umask(0o117)
	ln, err := net.Listen("unix", cfg.Socket)
	syscall.Umask(old)
	if err != nil {
		return err
	}
	defer ln.Close()
	if err := os.Chown(cfg.Socket, 0, cfg.SocketGID); err != nil {
		return err
	}
	if err := os.Chmod(cfg.Socket, 0o660); err != nil {
		return err
	}
	if cfg.Audit != "" {
		_ = os.MkdirAll(filepath.Dir(cfg.Audit), 0o700)
	}
	allowed := map[int]bool{}
	for _, u := range cfg.AllowedUIDs {
		allowed[u] = true
	}
	s := &api.Server{M: m, Audit: audit.Open(cfg.Audit), Allowed: allowed, Version: Version}
	srv := &http.Server{
		Handler:           s.Handler(),
		ConnContext:       api.ConnContext,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      6 * time.Minute,
		MaxHeaderBytes:    8 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sh)
	}()
	go m.BackupLoop(ctx, func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })
	go m.CronLoop(ctx, func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })
	go m.LoginLoop(ctx, func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })
	go m.UserCronLoop(ctx)              // OPS-01: the websites' own cron jobs
	go m.WatchLoop(ctx)                 // OPS-01 wave 2: watchdog and usage samples
	go m.ScanLoop(ctx)                  // SEC-02: scheduled malware scans at night
	if err := m.SyncWAF(); err != nil { // SEC-01 wave 3b: the WAF files follow site.json
		fmt.Fprintln(os.Stderr, "waf files:", err)
	}
	if err := m.SyncCerts(); err != nil { // CERT-01: own certificates follow the website folders
		fmt.Fprintln(os.Stderr, "certificate files:", err)
	}
	if err := m.SyncRemoteDB(ctx); err != nil { // DB-02: database ports from outside
		fmt.Fprintln(os.Stderr, "database ports:", err)
	}
	m.OnFirewallUndo(func(why string, err error) { // SRV-01: an undone firewall trial is in the audit log
		res := "ok"
		if err != nil {
			res, why = "failed", why+": "+err.Error()
		}
		s.Audit.Write(audit.Entry{Action: "firewall-undo", Result: res, Detail: why})
	})
	m.ResumeFirewallTrial(ctx)
	if err := m.SyncFail2ban(ctx); err != nil { // SRV-01: the panel's and SFTP's jails
		fmt.Fprintln(os.Stderr, "fail2ban:", err)
	}
	if err := m.SyncSFTP(); err != nil { // SFTP-01: accounts follow the website's file user
		fmt.Fprintln(os.Stderr, "sftp files:", err)
	}
	fmt.Println("nostek-agent", Version, "listening on", cfg.Socket)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
