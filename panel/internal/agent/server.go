package agent

import (
	"context"
	"net/url"
)

// CoreService is one core container or host unit (OPS-02).
type CoreService struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	State   string `json:"state"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	Restart bool   `json:"restart"`
}

// Proc is one process.
type Proc struct {
	PID   int     `json:"pid"`
	Name  string  `json:"name"`
	User  string  `json:"user"`
	CPU   float64 `json:"cpu"`
	RSS   int64   `json:"rss"`
	Mem   float64 `json:"mem"`
	Where string  `json:"where,omitempty"`
}

// ProcView is the top of the process list.
type ProcView struct {
	At       int64  `json:"at"`
	Cores    int    `json:"cores"`
	MemTotal int64  `json:"mem_total"`
	Count    int    `json:"count"`
	ByCPU    []Proc `json:"by_cpu"`
	ByMem    []Proc `json:"by_mem"`
}

// DriftFile is one managed file of a website.
type DriftFile struct {
	File    string `json:"file"`
	State   string `json:"state"`
	Added   int    `json:"added,omitempty"`
	Removed int    `json:"removed,omitempty"`
	Diff    string `json:"diff,omitempty"`
}

// DriftSite is the drift of one website.
type DriftSite struct {
	Site   string      `json:"site"`
	Domain string      `json:"domain"`
	Native bool        `json:"native,omitempty"`
	Drift  bool        `json:"drift"`
	Files  []DriftFile `json:"files"`
	Error  string      `json:"error,omitempty"`
}

// CoreServices lists the core containers and host units.
func (c *Client) CoreServices(ctx context.Context) ([]CoreService, error) {
	var v struct {
		Services []CoreService `json:"services"`
	}
	return v.Services, c.call(ctx, "GET", "/v1/server/services", nil, &v)
}

// RestartCore restarts one core service.
func (c *Client) RestartCore(ctx context.Context, name string) error {
	return c.call(ctx, "POST", "/v1/server/services/"+url.PathEscape(name)+"/restart", nil, nil)
}

// Processes returns the top processes.
func (c *Client) Processes(ctx context.Context) (ProcView, error) {
	var v ProcView
	return v, c.call(ctx, "GET", "/v1/server/processes", nil, &v)
}

// Drift compares the managed files of every website with what the agent would write.
func (c *Client) Drift(ctx context.Context) ([]DriftSite, error) {
	var v struct {
		Sites []DriftSite `json:"sites"`
	}
	return v.Sites, c.call(ctx, "GET", "/v1/server/drift", nil, &v)
}

// DriftFix writes a website's managed files back.
func (c *Client) DriftFix(ctx context.Context, site string) error {
	return c.call(ctx, "POST", "/v1/sites/"+url.PathEscape(site)+"/drift/fix", nil, nil)
}
