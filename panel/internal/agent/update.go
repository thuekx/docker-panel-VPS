package agent

import (
	"context"
	"time"
)

// Release is one published release (UPD-01).
type Release struct {
	Tag       string    `json:"tag"`
	Name      string    `json:"name"`
	Notes     string    `json:"notes"`
	Published time.Time `json:"published"`
	Newer     bool      `json:"newer"`
}

// UpdateStatus is the updater's last or current run.
type UpdateStatus struct {
	Phase   string   `json:"phase"`
	Tag     string   `json:"tag"`
	From    string   `json:"from"`
	Step    string   `json:"step"`
	Result  string   `json:"result"`
	Started int64    `json:"started"`
	Ended   int64    `json:"ended"`
	Running bool     `json:"running"`
	Lines   []string `json:"lines"`
}

// UpdateView is the release list, the last run and the backup-and-start job.
type UpdateView struct {
	Current  string        `json:"current"`
	Releases []Release     `json:"releases"`
	Error    string        `json:"error"`
	Ready    string        `json:"ready"`
	Status   *UpdateStatus `json:"status"`
	Job      *Job          `json:"job"`
}

// Update reads the release list (fresh skips the agent's cache) and the last run.
func (c *Client) Update(ctx context.Context, fresh bool) (UpdateView, error) {
	var v UpdateView
	p := "/v1/update"
	if fresh {
		p += "?fresh=1"
	}
	return v, c.call(ctx, "GET", p, nil, &v)
}

// StartUpdate starts an update to tag; a passphrase makes a server backup first.
func (c *Client) StartUpdate(ctx context.Context, tag, pass, panelVersion string) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", "/v1/update", map[string]string{"tag": tag, "passphrase": pass, "panel_version": panelVersion}, &v)
}
