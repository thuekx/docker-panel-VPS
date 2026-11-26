package agent

// Server backup calls (BAK-02).

import (
	"context"
	"net/http"
	"net/url"
)

// ServerBackup is one server backup file.
type ServerBackup struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Created int64  `json:"created"`
}

// ServerBackups is the list of server backups with the disk space.
type ServerBackups struct {
	Backups []ServerBackup `json:"backups"`
	Free    int64          `json:"free"`
	Need    int64          `json:"need"`
	Job     *Job           `json:"job"`
}

func serverBackupPath(name string) string { return "/v1/server-backups/" + url.PathEscape(name) }

// ServerBackups lists the server backups.
func (c *Client) ServerBackups(ctx context.Context) (ServerBackups, error) {
	var b ServerBackups
	return b, c.call(ctx, "GET", "/v1/server-backups", nil, &b)
}

// StartServerBackup starts a server backup sealed with pass; the panel has written its database copy just before.
func (c *Client) StartServerBackup(ctx context.Context, pass, panelVersion string) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", "/v1/server-backups", map[string]string{"passphrase": pass, "panel_version": panelVersion}, &v)
}

// ServerBackupFile streams one server backup. The caller closes the answer body.
func (c *Client) ServerBackupFile(ctx context.Context, name string) (*http.Response, error) {
	return c.stream(ctx, "GET", serverBackupPath(name), nil)
}

// DeleteServerBackup removes one server backup.
func (c *Client) DeleteServerBackup(ctx context.Context, name string) error {
	return c.call(ctx, "POST", serverBackupPath(name)+"/delete", nil, nil)
}
