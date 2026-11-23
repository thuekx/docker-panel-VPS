package agent

import (
	"context"
	"strconv"
)

// StatDay is one day of a website's statistics (OPS-01).
type StatDay struct {
	Day      string `json:"day"`
	Visitors int    `json:"visitors"`
	Requests int    `json:"requests"`
	Bots     int    `json:"bots"`
	Bytes    int64  `json:"bytes"`
	S2       int    `json:"s2"`
	S3       int    `json:"s3"`
	S4       int    `json:"s4"`
	S5       int    `json:"s5"`
}

// StatCount is one row of a top list.
type StatCount struct {
	Key string `json:"key"`
	N   int    `json:"n"`
}

// Metrics is a website's Statistics page.
type Metrics struct {
	Active bool        `json:"active"`
	Range  int         `json:"range"`
	Total  StatDay     `json:"total"`
	Days   []StatDay   `json:"days"`
	Hours  [24]int     `json:"hours"`
	Pages  []StatCount `json:"pages"`
	Refs   []StatCount `json:"refs"`
	Errors []StatCount `json:"errors"`
	Raw    []string    `json:"raw"`
}

// Metrics returns a website's statistics of the last 1, 7 or 31 days.
func (c *Client) Metrics(ctx context.Context, id string, days int) (Metrics, error) {
	var v Metrics
	return v, c.call(ctx, "GET", site(id)+"/metrics?days="+strconv.Itoa(days), nil, &v)
}

// CronRun is one run of a cron job.
type CronRun struct {
	Start  int64  `json:"start"`
	Millis int64  `json:"ms"`
	Code   int    `json:"code"`
	Output string `json:"output"`
	Manual bool   `json:"manual"`
}

// CronJob is one scheduled command of a website, with its next time and last runs (newest first).
type CronJob struct {
	ID       string    `json:"id"`
	Schedule string    `json:"schedule"`
	Command  string    `json:"command"`
	Off      bool      `json:"off"`
	Next     int64     `json:"next"`
	Running  bool      `json:"running"`
	Runs     []CronRun `json:"runs"`
}

// Cron is a website's Cron page.
type Cron struct {
	Supported bool      `json:"supported"`
	Where     string    `json:"where"`
	Jobs      []CronJob `json:"jobs"`
	Max       int       `json:"max"`
}

// CronChange is one change: op add | edit | remove | on | off | run.
type CronChange struct {
	Op       string `json:"op"`
	ID       string `json:"id,omitempty"`
	Schedule string `json:"schedule,omitempty"`
	Command  string `json:"command,omitempty"`
}

// Cron returns a website's cron jobs.
func (c *Client) Cron(ctx context.Context, id string) (Cron, error) {
	var v Cron
	return v, c.call(ctx, "GET", site(id)+"/cron", nil, &v)
}

// ChangeCron applies one change; changed is false when nothing changed.
func (c *Client) ChangeCron(ctx context.Context, id string, ch CronChange) (string, bool, error) {
	var v struct {
		ID      string `json:"id"`
		Changed bool   `json:"changed"`
	}
	err := c.call(ctx, "POST", site(id)+"/cron", ch, &v)
	return v.ID, v.Changed, err
}

// WatchEvent is one thing the agent's watchdog did (OPS-01 wave 2). Site is "" for core and host services.
type WatchEvent struct {
	Seq     int64  `json:"seq"`
	At      int64  `json:"at"`
	Site    string `json:"site"`
	Service string `json:"service"`
	Kind    string `json:"kind"`   // restarted | restart_failed | gave_up | recovered
	Reason  string `json:"reason"` // unhealthy | not running
}

// WatchView is the watchdog's events after a sequence number.
type WatchView struct {
	Seq    int64        `json:"seq"`
	Events []WatchEvent `json:"events"`
}

// Watchdog returns the watchdog events after since.
func (c *Client) Watchdog(ctx context.Context, since int64) (WatchView, error) {
	var v WatchView
	return v, c.call(ctx, "GET", "/v1/watchdog?since="+strconv.FormatInt(since, 10), nil, &v)
}

// UsagePoint is one sample: CPU in percent of one CPU, memory in bytes; disk (percent) and load for the server.
type UsagePoint struct {
	At   int64   `json:"at"`
	CPU  float64 `json:"cpu"`
	Mem  int64   `json:"mem"`
	Disk float64 `json:"disk"`
	Load float64 `json:"load"`
}

// Usage is the CPU and memory over time of a website or of the server.
type Usage struct {
	Recent []UsagePoint `json:"recent"`
	Hours  []UsagePoint `json:"hours"`
	CPUs   int          `json:"cpus"`
	MemMax int64        `json:"mem_max"`
}

// SiteUsage returns a website's usage.
func (c *Client) SiteUsage(ctx context.Context, id string) (Usage, error) {
	var v Usage
	return v, c.call(ctx, "GET", site(id)+"/usage", nil, &v)
}

// ServerUsage returns the server's usage.
func (c *Client) ServerUsage(ctx context.Context) (Usage, error) {
	var v Usage
	return v, c.call(ctx, "GET", "/v1/usage", nil, &v)
}
