package sites

// Safe update (WP-02 wave 1): a WordPress update that is checked and undone when it breaks the website. The job
// looks at the website first (WordPress loads with its plugins and theme, and the front page answers), makes a
// "before-update" backup (BAK-01), runs the update, looks again, and puts the backup back when the website worked
// before and does not now. A website that was already broken is updated but never rolled back on that ground.

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SafeUpdate is a WordPress update run as a job with a backup and a check.
type SafeUpdate struct {
	Kind  string   `json:"kind"`  // core | plugin | theme
	Do    string   `json:"do"`    // core: update | all; plugin, theme: update
	Names []string `json:"names"` // plugin, theme: what to update (or All)
	All   bool     `json:"all"`
}

// healthPHP loads WordPress with its plugins and theme (wp eval) and asks the front page through WordPress's own
// HTTP client, so a fatal error in a plugin or theme shows either way.
const healthPHP = `$r = wp_remote_get(home_url("/"), array("timeout" => 20, "sslverify" => false, "redirection" => 3)); echo "np-code:" . (is_wp_error($r) ? 0 : (int) wp_remote_retrieve_response_code($r));`

var npCodeRe = regexp.MustCompile(`np-code:([0-9]{1,3})`)

// siteHealth is what the check saw.
type siteHealth struct {
	Loads bool // WordPress loaded with its plugins and theme
	Code  int  // HTTP status of the front page (0 = no answer)
	Why   string
}

func (h siteHealth) works() bool { return h.Loads && h.Code >= 200 && h.Code < 400 }

func (h siteHealth) String() string {
	if !h.Loads {
		return "WordPress does not load: " + h.Why
	}
	if h.Code == 0 {
		return "the front page does not answer"
	}
	return "the front page answers " + strconv.Itoa(h.Code)
}

// brokeBy is true when the update broke a website that worked before it.
func brokeBy(before, after siteHealth) bool {
	if before.Loads && !after.Loads {
		return true
	}
	return before.works() && after.Code >= 500
}

func (m *Manager) wpHealth(ctx context.Context, id string) siteHealth {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	res, err := m.wp(ctx, id, nil, []string{"wp", "eval", healthPHP})
	if err != nil {
		return siteHealth{Why: err.Error()}
	}
	if len(res) != 1 {
		return siteHealth{Why: "no answer from WP-CLI"}
	}
	o := res[0]
	t := o.text()
	mm := npCodeRe.FindStringSubmatch(o.Out)
	if o.RC != 0 || mm == nil || strings.Contains(t, "Fatal error") || strings.Contains(t, "critical error") {
		return siteHealth{Why: lastLine(t)}
	}
	code, _ := strconv.Atoi(mm[1])
	return siteHealth{Loads: true, Code: code}
}

// coreCmds are the WP-CLI commands of a core update ("update") or of everything ("all").
func coreCmds(do string) ([][]string, error) {
	cmds := [][]string{{"wp", "core", "update"}, {"wp", "core", "update-db"}}
	switch do {
	case "update":
	case "all":
		cmds = append(cmds, []string{"wp", "plugin", "update", "--all"}, []string{"wp", "theme", "update", "--all"})
	default:
		return nil, fmt.Errorf("%w: unknown action %q", ErrBadInput, do)
	}
	return cmds, nil
}

func safeCmds(u SafeUpdate) ([][]string, error) {
	switch u.Kind {
	case "core":
		if len(u.Names) > 0 || u.All {
			return nil, fmt.Errorf("%w: a core update takes no names", ErrBadInput)
		}
		return coreCmds(u.Do)
	case "plugin", "theme":
		if u.Do != "update" {
			return nil, fmt.Errorf("%w: a safe update only updates", ErrBadInput)
		}
		return itemCmds(u.Kind, ItemAction{Do: "update", Names: u.Names, All: u.All})
	}
	return nil, fmt.Errorf("%w: update core, plugin or theme", ErrBadInput)
}

// StartSafeUpdate starts a safe update job and returns its id.
func (m *Manager) StartSafeUpdate(id string, u SafeUpdate) (string, error) {
	cmds, err := safeCmds(u)
	if err != nil {
		return "", err
	}
	s, err := m.wpSite(id)
	if err != nil {
		return "", err
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	j := m.jobs.New("wp-update", id)
	go func() {
		defer m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
		defer cancel()
		j.Step("check before")
		before := m.wpHealth(ctx, id)
		safe, err := m.backup(ctx, j, s, "before-update")
		if err != nil {
			j.Fail("the backup before the update failed, nothing was changed: " + err.Error())
			return
		}
		j.Step("update")
		uctx, ucancel := context.WithTimeout(ctx, 15*time.Minute)
		res, uerr := m.wp(uctx, id, nil, cmds...)
		ucancel()
		if uerr == nil {
			for _, o := range res {
				if o.RC != 0 {
					uerr = wpErr(o)
					break
				}
			}
		}
		j.Step("check after")
		after := m.wpHealth(ctx, id)
		if brokeBy(before, after) {
			j.Step("restore")
			if err := m.restore(ctx, j, s, filepath.Join(m.backupDir(id), safe), "all"); err != nil {
				j.Fail("the update broke the website (" + after.String() + ") and putting it back failed: " + err.Error() + " — restore the backup " + safe + " from the Backups tab")
				return
			}
			back := m.wpHealth(ctx, id)
			j.Fail("the update broke the website (" + after.String() + "), so it was put back as it was from the backup " + safe + " (now: " + back.String() + ")")
			return
		}
		if uerr != nil {
			j.Fail(uerr.Error() + " — the website still works (" + after.String() + "); its backup from before is " + safe)
			return
		}
		j.Step("done " + safe)
		j.Done("")
	}()
	return j.ID, nil
}
