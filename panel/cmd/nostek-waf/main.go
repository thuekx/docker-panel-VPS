// Command nostek-waf is the edge's web application firewall (SEC-01 wave 3b). It runs from the panel image as its
// own container of the core stack, reachable only on the edge network; see internal/waf.
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

	"github.com/thuekx/docker-panel-VPS/panel/internal/waf"
)

var version = "dev"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		c := http.Client{Timeout: 3 * time.Second}
		r, err := c.Get("http://127.0.0.1" + env("WAF_HEALTH", ":8091") + "/healthz")
		if err != nil || r.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	f, err := os.OpenFile(env("WAF_LOG", "/logs/waf.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	sv := waf.New(env("WAF_CONFIG", "/config"), f)
	sv.Proxies = waf.ParseProxies(os.Getenv("EDGE_TRUSTED_PROXIES"))
	if err := sv.Load(); err != nil {
		log.Printf("waf: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go sv.Watch(ctx, 3*time.Second)
	health := &http.Server{Addr: env("WAF_HEALTH", ":8091"), ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "ok %s sites=%s\n", version, strings.Join(sv.Sites(), ","))
		})}
	srv := &http.Server{Addr: env("WAF_LISTEN", ":8090"), Handler: sv, ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sh)
		_ = health.Shutdown(sh)
	}()
	go func() {
		if err := health.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("health: %v", err)
		}
	}()
	log.Printf("nostek-waf %s listening on %s", version, srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
