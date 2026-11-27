package agent

// WordPress Tools calls (WP-01).

import (
	"context"
	"errors"
)

// WPItem is one plugin or theme.
type WPItem struct {
	Name          string `json:"name"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	Version       string `json:"version"`
	Update        string `json:"update"`
	UpdateVersion string `json:"update_version"`
	AutoUpdate    string `json:"auto_update"`
}

// TLS is the certificate the edge serves for the site.
type TLS struct {
	Host    string `json:"host"`
	OK      bool   `json:"ok"`
	Issuer  string `json:"issuer"`
	Expires int64  `json:"expires"`
	Error   string `json:"error"`
}

// WP is the WordPress overview of a site.
type WP struct {
	Version       string   `json:"version"`
	CoreUpdate    string   `json:"core_update"`
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	Admins        []string `json:"admins"`
	AdminUser     string   `json:"admin_user"`
	Indexing      bool     `json:"indexing"`
	Maintenance   bool     `json:"maintenance"`
	Debug         bool     `json:"debug"`
	SystemCron    bool     `json:"system_cron"`
	FileEditOff   bool     `json:"file_edit_off"`
	SSLAdmin      bool     `json:"ssl_admin"`
	PHP           string   `json:"php"`
	Server        string   `json:"server"`
	Plugins       []WPItem `json:"plugins"`
	Themes        []WPItem `json:"themes"`
	PluginUpdates int      `json:"plugin_updates"`
	ThemeUpdates  int      `json:"theme_updates"`
	TLS           TLS      `json:"tls"`
	Warnings      []string `json:"warnings"`
}

// ItemAction is a change to plugins or themes.
type ItemAction struct {
	Do       string   `json:"do"`
	Names    []string `json:"names,omitempty"`
	All      bool     `json:"all,omitempty"`
	Activate bool     `json:"activate,omitempty"`
	Safe     bool     `json:"safe,omitempty"` // update as a job: backup first, check after, put back when broken (WP-02)
}

// Check is a WordPress integrity check.
type Check struct {
	CoreOK    bool   `json:"core_ok"`
	Core      string `json:"core"`
	PluginsOK bool   `json:"plugins_ok"`
	Plugins   string `json:"plugins"`
}

// CLIResult is one WP-CLI console run.
type CLIResult struct {
	Code   int    `json:"code"`
	Output string `json:"output"`
}

// WordPress returns the overview.
func (c *Client) WordPress(ctx context.Context, id string) (WP, error) {
	var w WP
	err := c.call(ctx, "GET", site(id)+"/wp", nil, &w)
	return w, err
}

// Login returns a one-time wp-admin login link and the account it logs in.
func (c *Client) Login(ctx context.Context, id string) (string, string, error) {
	var a struct {
		URL  string `json:"url"`
		User string `json:"user"`
	}
	err := c.call(ctx, "POST", site(id)+"/wp/login", map[string]any{}, &a)
	return a.URL, a.User, err
}

// Items changes plugins ("plugins") or themes ("themes").
func (c *Client) Items(ctx context.Context, id, kind string, a ItemAction) (string, error) {
	var o struct {
		Output string `json:"output"`
	}
	err := c.call(ctx, "POST", site(id)+"/wp/"+kind, a, &o)
	return o.Output, err
}

// Core updates WordPress ("update") or everything ("all").
func (c *Client) Core(ctx context.Context, id, do string) (string, error) {
	var o struct {
		Output string `json:"output"`
	}
	err := c.call(ctx, "POST", site(id)+"/wp/core", map[string]string{"do": do}, &o)
	return o.Output, err
}

// SafeUpdate starts a safe update (WP-02) and returns its job: kind "core" (do update | all), "plugin" or "theme".
func (c *Client) SafeUpdate(ctx context.Context, id, kind string, a ItemAction) (string, error) {
	var o struct {
		Job string `json:"job"`
	}
	var err error
	if kind == "core" {
		err = c.call(ctx, "POST", site(id)+"/wp/core", map[string]any{"do": a.Do, "safe": true}, &o)
	} else {
		a.Safe = true
		err = c.call(ctx, "POST", site(id)+"/wp/"+kind+"s", a, &o)
	}
	if err == nil && o.Job == "" {
		err = errors.New("the agent started no job")
	}
	return o.Job, err
}

// StartStaging makes the staging copy of a WordPress website (WP-02 wave 2); it returns the job and the new website.
func (c *Client) StartStaging(ctx context.Context, id string) (string, Site, error) {
	var v struct {
		Job  string `json:"job"`
		Site Site   `json:"site"`
	}
	err := c.call(ctx, "POST", site(id)+"/staging", nil, &v)
	return v.Job, v.Site, err
}

// PushStaging copies the staging website from over its live website id.
func (c *Client) PushStaging(ctx context.Context, id, from string) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", site(id)+"/staging/push", map[string]string{"from": from}, &v)
}

// SetWP switches one dashboard setting.
func (c *Client) SetWP(ctx context.Context, id, key string, on bool) error {
	return c.call(ctx, "POST", site(id)+"/wp/settings", map[string]any{"key": key, "on": on}, nil)
}

// Integrity runs the checksum check.
func (c *Client) Integrity(ctx context.Context, id string) (Check, error) {
	var ch Check
	err := c.call(ctx, "POST", site(id)+"/wp/check", map[string]any{}, &ch)
	return ch, err
}

// CLI runs one WP-CLI command line.
func (c *Client) CLI(ctx context.Context, id, line string) (CLIResult, error) {
	var r CLIResult
	err := c.call(ctx, "POST", site(id)+"/wp/cli", map[string]string{"line": line}, &r)
	return r, err
}
