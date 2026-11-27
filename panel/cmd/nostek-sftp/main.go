// Command nostek-sftp is the SFTP service of the core stack (SFTP-01). It runs from the panel image as its own
// container; the edge is not involved. See internal/sftpd.
package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/authlog"
	"github.com/thuekx/docker-panel-VPS/panel/internal/sftpd"
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
		c, err := net.DialTimeout("tcp", "127.0.0.1"+env("SFTP_LISTEN", ":2222"), 3*time.Second)
		if err != nil {
			os.Exit(1)
		}
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		line, err := bufio.NewReader(c).ReadString('\n')
		c.Close()
		if err != nil || !strings.HasPrefix(line, "SSH-2.0-") {
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
	sv := sftpd.New(env("SFTP_CONFIG", "/config"), os.Stdout)
	sv.AuthFail = authlog.Open(filepath.Join(env("SFTP_STATE", "/state"), "auth-fail.log"), "sftp") // SRV-01: fail2ban
	if err := sv.Load(); err != nil {
		log.Printf("sftp: %v", err)
	}
	hk, err := sftpd.HostKey(filepath.Join(env("SFTP_STATE", "/state"), "host_ed25519_key"))
	if err != nil {
		return fmt.Errorf("host key: %w", err)
	}
	l, err := net.Listen("tcp", env("SFTP_LISTEN", ":2222"))
	if err != nil {
		return err
	}
	stop := make(chan struct{})
	go sv.Watch(stop, 3*time.Second)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sig; close(stop); l.Close() }()
	log.Printf("nostek-sftp %s listening on %s, accounts %d", version, l.Addr(), len(sv.Accounts()))
	return sv.Serve(l, hk)
}
