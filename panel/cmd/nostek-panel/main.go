// Command nostek-panel runs the Nostek Panel web UI and its admin helpers.
//
//	nostek-panel serve
//	nostek-panel setup-link      (first admin, one-time link, 30 min)
//	nostek-panel admin-rotate EMAIL  (new random password via one-time link, 15 min)
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/thuekx/docker-panel-VPS/panel/internal/store"
	"github.com/thuekx/docker-panel-VPS/panel/internal/sysinfo"
	"github.com/thuekx/docker-panel-VPS/panel/internal/web"
	webassets "github.com/thuekx/docker-panel-VPS/panel/web"
)

var version = "dev"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: nostek-panel serve | setup-link | admin-rotate EMAIL")
		os.Exit(2)
	}
	st, err := store.Open(env("NP_DB", "/data/panel.db"))
	if err != nil {
		log.Fatalf("FAIL open database: %v", err)
	}
	defer st.Close()
	base := strings.TrimRight(env("NP_BASE_URL", "https://localhost"), "/")
	switch os.Args[1] {
	case "setup-link":
		link, err := web.SetupLink(st, base, time.Now())
		if err != nil {
			fmt.Println("REFUSE", err)
			os.Exit(3)
		}
		fmt.Println("SETUP_LINK", link)
		fmt.Println("INFO valid 30 minutes, one use only")
	case "admin-rotate":
		if len(os.Args) < 3 {
			fmt.Println("REFUSE admin-rotate needs the admin e-mail")
			os.Exit(3)
		}
		link, err := web.AdminRotate(st, strings.ToLower(os.Args[2]), base, time.Now())
		if err != nil {
			fmt.Println("REFUSE", err)
			os.Exit(3)
		}
		fmt.Println("REVEAL_LINK", link)
		fmt.Println("INFO valid 15 minutes, shows the new password once; all sessions were signed out")
	case "serve":
		serve(st)
	default:
		fmt.Fprintln(os.Stderr, "unknown command")
		os.Exit(2)
	}
}

func serve(st *store.Store) {
	host, _ := os.Hostname()
	srv, err := web.New(web.Config{
		SecureCookies: env("NP_INSECURE_COOKIES", "") == "",
		TrustProxy:    env("NP_TRUST_PROXY", "1") == "1",
		DefaultLang:   env("NP_LANG", "id"),
		Version:       version,
	}, st, webassets.FS, sysinfo.Reader{
		ProcDir:    env("NP_PROC", "/host/proc"),
		DiskPath:   env("NP_DISK", "/data"),
		DockerURL:  env("NP_DOCKER_URL", "http://docker-api:2375"),
		BackupFile: env("NP_BACKUP_STATUS", "/host/backup/last"),
		Hostname:   env("NP_HOSTNAME", host),
	})
	if err != nil {
		log.Fatalf("FAIL init: %v", err)
	}
	hs := &http.Server{Addr: env("NP_LISTEN", ":8080"), Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10}
	go func() {
		log.Printf("INFO nostek-panel %s listening on %s", version, hs.Addr)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("FAIL listen: %v", err)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = hs.Shutdown(ctx)
}
