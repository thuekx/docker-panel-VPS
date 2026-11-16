package agent

// WordPress migration calls (MIG-01).

import (
	"context"
	"io"
	"net/url"
)

// MigrateFiles is what was uploaded for a backup migration (bytes, 0 = none).
type MigrateFiles struct {
	Archive  int64 `json:"archive"`
	Database int64 `json:"database"`
	Job      *Job  `json:"job"` // the running migration, else the newest one the agent still keeps
}

// MigrateRequest starts a migration: way plugin (Code) or backup (the uploaded files or Link; Root picks one of
// several WordPress folders).
type MigrateRequest struct {
	Way  string `json:"way"`
	Code string `json:"code,omitempty"`
	Link string `json:"link,omitempty"`
	Root string `json:"root,omitempty"`
}

// MigrateStatus lists the uploaded files.
func (c *Client) MigrateStatus(ctx context.Context, id string) (MigrateFiles, error) {
	var f MigrateFiles
	return f, c.call(ctx, "GET", site(id)+"/migrate", nil, &f)
}

// MigrateUpload streams one file (part archive or database) to the agent.
func (c *Client) MigrateUpload(ctx context.Context, id, part string, body io.Reader) error {
	resp, err := c.stream(ctx, "PUT", site(id)+"/migrate/upload?part="+url.QueryEscape(part), body)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// MigrateClear removes the uploaded files.
func (c *Client) MigrateClear(ctx context.Context, id string) error {
	return c.call(ctx, "POST", site(id)+"/migrate/clear", nil, nil)
}

// StartMigrate starts a migration and returns its job.
func (c *Client) StartMigrate(ctx context.Context, id string, r MigrateRequest) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", site(id)+"/migrate", r, &v)
}
