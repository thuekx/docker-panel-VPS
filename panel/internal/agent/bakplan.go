package agent

import (
	"context"
	"net/url"
)

// DestResult is the last test of, or copy to, a backup destination (BAK-03).
type DestResult struct {
	At   int64  `json:"at"`
	OK   bool   `json:"ok"`
	Msg  string `json:"msg,omitempty"`
	File string `json:"file,omitempty"`
}

// BackupDest is one destination for server backups, without its secret.
type BackupDest struct {
	ID        string      `json:"id"`
	Kind      string      `json:"kind"`
	Name      string      `json:"name"`
	On        bool        `json:"on"`
	Keep      int         `json:"keep"`
	Endpoint  string      `json:"endpoint,omitempty"`
	Region    string      `json:"region,omitempty"`
	Bucket    string      `json:"bucket,omitempty"`
	Prefix    string      `json:"prefix,omitempty"`
	AccessKey string      `json:"access_key,omitempty"`
	HasSecret bool        `json:"has_secret"`
	Host      string      `json:"host,omitempty"`
	Port      int         `json:"port,omitempty"`
	User      string      `json:"user,omitempty"`
	Path      string      `json:"path,omitempty"`
	HostKey   string      `json:"host_key,omitempty"`
	Last      *DestResult `json:"last,omitempty"`
	Test      *DestResult `json:"test,omitempty"`
}

// BackupPlan is the schedule of server backups and their destinations.
type BackupPlan struct {
	Every    string       `json:"every"`
	Hour     int          `json:"hour"`
	Weekday  int          `json:"weekday"`
	Sealed   bool         `json:"sealed"`
	LastRun  int64        `json:"last_run,omitempty"`
	LastOK   bool         `json:"last_ok"`
	LastMsg  string       `json:"last_msg,omitempty"`
	LastFile string       `json:"last_file,omitempty"`
	Due      bool         `json:"due"`
	Next     int64        `json:"next,omitempty"`
	Dests    []BackupDest `json:"dests"`
	SSHKey   string       `json:"ssh_key,omitempty"`
}

// DestInput is a new (no ID) or changed destination; an empty Secret keeps the stored one.
type DestInput struct {
	ID        string `json:"id,omitempty"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	On        bool   `json:"on"`
	Keep      int    `json:"keep"`
	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	AccessKey string `json:"access_key,omitempty"`
	Secret    string `json:"secret,omitempty"`
	Host      string `json:"host,omitempty"`
	Port      int    `json:"port,omitempty"`
	User      string `json:"user,omitempty"`
	Path      string `json:"path,omitempty"`
}

// BackupPlan reads the schedule and the destinations.
func (c *Client) BackupPlan(ctx context.Context) (BackupPlan, error) {
	var v BackupPlan
	return v, c.call(ctx, "GET", "/v1/backup-plan", nil, &v)
}

// SetServerSchedule sets the schedule; pass (when not empty) becomes the key scheduled backups are sealed to.
func (c *Client) SetServerSchedule(ctx context.Context, every string, hour, weekday int, pass string) error {
	return c.call(ctx, "POST", "/v1/backup-plan/schedule", map[string]any{"every": every, "hour": hour, "weekday": weekday, "passphrase": pass}, nil)
}

// RunScheduledBackup starts the scheduled server backup that is due (the panel wrote its copy just before).
func (c *Client) RunScheduledBackup(ctx context.Context, panelVersion string) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", "/v1/backup-plan/run", map[string]string{"panel_version": panelVersion}, &v)
}

// SaveDest adds or changes a destination and returns its id.
func (c *Client) SaveDest(ctx context.Context, in DestInput) (string, error) {
	var v struct {
		ID string `json:"id"`
	}
	return v.ID, c.call(ctx, "POST", "/v1/backup-plan/dests", in, &v)
}

// DeleteDest removes a destination.
func (c *Client) DeleteDest(ctx context.Context, id string) error {
	return c.call(ctx, "POST", "/v1/backup-plan/dests/"+url.PathEscape(id)+"/delete", nil, nil)
}

// TestDest writes and removes a small file at a destination.
func (c *Client) TestDest(ctx context.Context, id string) (DestResult, error) {
	var r DestResult
	return r, c.call(ctx, "POST", "/v1/backup-plan/dests/"+url.PathEscape(id)+"/test", nil, &r)
}

// BackupSSHKey makes the agent's SSH key when needed and returns its public half.
func (c *Client) BackupSSHKey(ctx context.Context) (string, error) {
	var v struct {
		Key string `json:"key"`
	}
	return v.Key, c.call(ctx, "POST", "/v1/backup-plan/ssh-key", nil, &v)
}

// SendServerBackup copies a listed server backup to one destination as a job.
func (c *Client) SendServerBackup(ctx context.Context, name, dest string) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", serverBackupPath(name)+"/send", map[string]string{"dest": dest}, &v)
}
