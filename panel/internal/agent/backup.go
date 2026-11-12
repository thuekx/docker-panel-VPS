package agent

// Website backup calls (BAK-01).

import (
	"context"
	"net/http"
	"net/url"
)

// SiteBackup is one backup archive of a website.
type SiteBackup struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // manual | daily | before-restore
	Size    int64  `json:"size"`
	Created int64  `json:"created"`
}

// Backups is a website's backups, newest first, with its schedule.
type Backups struct {
	Backups []SiteBackup `json:"backups"`
	Daily   bool         `json:"daily"`
	Keep    int          `json:"keep"`
	Free    int64        `json:"free"`
	Job     *Job         `json:"job"`
}

func backupPath(id, name string) string { return site(id) + "/backups/" + url.PathEscape(name) }

// Backups lists a website's backups.
func (c *Client) Backups(ctx context.Context, id string) (Backups, error) {
	var b Backups
	return b, c.call(ctx, "GET", site(id)+"/backups", nil, &b)
}

// StartBackup starts a backup and returns its job.
func (c *Client) StartBackup(ctx context.Context, id string) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", site(id)+"/backups", nil, &v)
}

// SetBackupSchedule turns the daily backup on or off and sets how many are kept.
func (c *Client) SetBackupSchedule(ctx context.Context, id string, daily bool, keep int) error {
	return c.call(ctx, "POST", site(id)+"/backups/schedule", map[string]any{"daily": daily, "keep": keep}, nil)
}

// BackupFile streams one backup. The caller closes the answer body.
func (c *Client) BackupFile(ctx context.Context, id, name string) (*http.Response, error) {
	return c.stream(ctx, "GET", backupPath(id, name), nil)
}

// DeleteBackup removes one backup.
func (c *Client) DeleteBackup(ctx context.Context, id, name string) error {
	return c.call(ctx, "POST", backupPath(id, name)+"/delete", nil, nil)
}

// RestoreBackup starts a restore of "all", "files" or "database" and returns its job.
func (c *Client) RestoreBackup(ctx context.Context, id, name, what string) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", backupPath(id, name)+"/restore", map[string]string{"what": what}, &v)
}
