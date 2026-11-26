package main

// server-restore (BAK-02): the installer on a new server checks a server backup, then puts the websites and the
// panel's database in place. The passphrase comes from a file, never from the command line.
//
//	nostek-agent server-restore check  --file F --passphrase-file P
//	nostek-agent server-restore sites  --file F --passphrase-file P
//	nostek-agent server-restore panel  --file F --passphrase-file P   (the panel must be stopped)

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

var wib = time.FixedZone("WIB", 7*3600)

func readPassphrase(p string) (string, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if fi.Size() > 4096 {
		return "", errors.New("passphrase file is too large")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

func serverRestore(m *sites.Manager, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nostek-agent server-restore check|sites|panel --file F --passphrase-file P")
		return 2
	}
	what := args[0]
	fs := flag.NewFlagSet("server-restore", flag.ContinueOnError)
	file := fs.String("file", "", "the server backup (.npbk)")
	passFile := fs.String("passphrase-file", "", "a file with the passphrase")
	if err := fs.Parse(args[1:]); err != nil || *file == "" || *passFile == "" || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: nostek-agent server-restore check|sites|panel --file F --passphrase-file P")
		return 2
	}
	pass, err := readPassphrase(*passFile)
	if err != nil {
		fmt.Println("SERVER_BACKUP refused: passphrase file:", err)
		return 1
	}
	switch what {
	case "check":
		man, err := sites.CheckServerBackup(*file, pass)
		if err != nil {
			fmt.Println("SERVER_BACKUP refused:", err)
			return 1
		}
		for _, s := range man.Sites {
			fmt.Printf("SITE %s %s %s\n", s.ID, s.Domain, s.Engine)
		}
		for _, s := range man.Skipped {
			fmt.Println("NOT_IN_BACKUP", s)
		}
		fmt.Printf("SERVER_BACKUP ok sites=%d panel=%s host=%s made=%s\n", len(man.Sites), orDash(man.Panel), orDash(man.Host),
			time.Unix(man.Created, 0).In(wib).Format("2006-01-02_15:04_WIB"))
		return 0
	case "panel":
		if err := m.RestorePanel(*file, pass); err != nil {
			fmt.Println("PANEL refused:", err)
			return 1
		}
		fmt.Println("PANEL restored")
		return 0
	case "sites":
		res, err := m.RestoreSites(context.Background(), *file, pass, func(f string, a ...any) { fmt.Printf("STEP "+f+"\n", a...) })
		if err != nil {
			fmt.Println("SITES refused:", err)
			return 1
		}
		n := map[string]int{}
		for _, r := range res {
			n[r.Result]++
			fmt.Printf("SITE %s %s %s\n", r.ID, r.Result, r.Note)
		}
		fmt.Printf("SITES restored=%d skipped=%d failed=%d\n", n["restored"], n["skipped"], n["failed"])
		if n["failed"] > 0 {
			return 3
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "usage: nostek-agent server-restore check|sites|panel --file F --passphrase-file P")
	return 2
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
