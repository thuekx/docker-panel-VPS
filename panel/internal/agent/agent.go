// Package agent is the panel's client for nostek-agent, the host service that is the only part allowed to
// change Docker (SITE-DOCKER-1 = A). It speaks JSON over a Unix socket; the panel never runs Docker itself.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to the agent.
type Client struct {
	hc *http.Client
}

// New returns a client for the socket path ("" = no agent configured: every call fails with ErrNoAgent).
func New(socket string) *Client {
	if socket == "" {
		return &Client{}
	}
	return &Client{hc: &http.Client{Timeout: 6 * time.Minute, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns: 2,
	}}}
}

// NewHTTP returns a client using a custom transport (tests).
func NewHTTP(hc *http.Client) *Client { return &Client{hc: hc} }

// ErrNoAgent means the agent is not installed or not reachable.
var ErrNoAgent = errors.New("agent not reachable")

// Error is an answer from the agent with its message.
type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Status returns the HTTP code of an agent error, or 0.
func Status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	if c.hc == nil {
		return ErrNoAgent
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return ErrNoAgent
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return ErrNoAgent
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("agent answered %d", resp.StatusCode)
		}
		return &Error{Code: resp.StatusCode, Msg: e.Error}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

// Host is one name of a site.
type Host struct {
	Name   string `json:"name"`
	Role   string `json:"role"`
	Routed bool   `json:"routed"`
}

// Site as the agent reports it.
type Site struct {
	ID         string `json:"id"`
	Domain     string `json:"domain"`
	Primary    string `json:"primary"`
	Server     string `json:"server"`
	PHP        string `json:"php"`
	AdminEmail string `json:"admin_email"`
	AdminUser  string `json:"admin_user"`
	Title      string `json:"title"`
	Hosts      []Host `json:"hosts"`
	URL        string `json:"url"`
	Created    int64  `json:"created"`
	Phase      string `json:"phase"`
	Status     string `json:"status"`
	Up         int    `json:"containers_up"`
	Total      int    `json:"containers_total"`
	App        string `json:"app"`       // APP-01: wordpress (empty) | php | laravel | static
	Database   bool   `json:"database"`  // php, node, go, python: own database
	DBEngine   string `json:"db_engine"` // mariadb (empty) | postgres
	Version    string `json:"version"`   // laravel: major version; node: Node.js version
	Source     string `json:"source"`    // node: git (empty) | files
	Dir        string `json:"dir"`       // node: app folder
	Repo       string `json:"repo"`      // node: Git repository
	Branch     string `json:"branch"`    // node: branch
	Build      string `json:"build"`     // node: build command ("" = default)
	Start      string `json:"start"`     // node: start command ("" = default)
	Release    int    `json:"release"`   // node: live release
}

// FromFiles is true when a Node.js app is built from the website's source/ folder.
func (s Site) FromFiles() bool { return s.Source == "files" }

// PrivateRepo is true when the app is read over SSH with its own deploy key (APP-02b).
func (s Site) PrivateRepo() bool { return !s.FromFiles() && IsSSHRepo(s.Repo) }

// IsSSHRepo is true for a repository address that needs a deploy key (the agent checks the address itself).
func IsSSHRepo(r string) bool { return strings.HasPrefix(r, "git@") || strings.HasPrefix(r, "ssh://") }

// DeployKey is the public half of a website's deploy key and the host keys pinned on the first fetch.
type DeployKey struct {
	ID          string   `json:"id,omitempty"`
	Public      string   `json:"public"`
	Fingerprint string   `json:"fingerprint"`
	HostKeys    []string `json:"host_keys,omitempty"`
}

// IsNode is true for an app built from source by the agent: Node.js, Go or Python (APP-02).
func (s Site) IsNode() bool { return s.App == "node" || s.App == "go" || s.App == "python" }

// AppName is the app the site runs: wordpress, php or static.
func (s Site) AppName() string {
	if s.App == "" {
		return "wordpress"
	}
	return s.App
}

// IsWP is true for a WordPress site.
func (s Site) IsWP() bool { return s.AppName() == "wordpress" }

// HasDB is true when the site has its own database.
func (s Site) HasDB() bool {
	return s.IsWP() || s.IsLaravel() || ((s.App == "php" || s.IsNode()) && s.Database)
}

// IsPostgres is true when the site database is PostgreSQL instead of MariaDB.
func (s Site) IsPostgres() bool { return s.HasDB() && s.DBEngine == "postgres" }

// DBEngineName is mariadb or postgres: the engine of the site database, for locale keys.
func (s Site) DBEngineName() string {
	if s.IsPostgres() {
		return "postgres"
	}
	return "mariadb"
}

// DBName is the database server the site runs, as the panel shows it.
func (s Site) DBName() string {
	if s.IsPostgres() {
		return "PostgreSQL"
	}
	return "MariaDB"
}

// IsLaravel is true for a Laravel site.
func (s Site) IsLaravel() bool { return s.App == "laravel" }

// Container is one container of a site's stack with its live usage (STACK-01).
type Container struct {
	Service  string  `json:"service"`
	Name     string  `json:"name"`
	Image    string  `json:"image"`
	State    string  `json:"state"`
	Health   string  `json:"health"`
	Status   string  `json:"status"`
	CPU      float64 `json:"cpu"`
	MemBytes int64   `json:"mem"`
	MemLimit int64   `json:"mem_limit"`
	Restarts int     `json:"restarts"`
	Started  int64   `json:"started"`
}

// Stack is a site's compose project and its containers.
type Stack struct {
	Project    string      `json:"project"`
	Containers []Container `json:"containers"`
}

// Trashed is a deleted site that can still be restored.
type Trashed struct {
	Site
	Entry   string `json:"entry"`
	Deleted int64  `json:"deleted"`
	Purge   int64  `json:"purge"`
}

// PHP is one offered version.
type PHP struct {
	Version       string `json:"version"`
	EOL           bool   `json:"eol"`
	SecurityUntil string `json:"security_until"`
	Default       bool   `json:"default"`
}

// Catalog lists what can be created.
type Catalog struct {
	PHP     []PHP     `json:"php"`
	Servers []string  `json:"servers"`
	Laravel []Laravel `json:"laravel"`
	Node    []Node    `json:"node"`
	Go      []Node    `json:"go"`
	Python  []Node    `json:"python"`
}

// Node is one offered Node.js version (APP-02).
type Node struct {
	Version       string `json:"version"`
	SecurityUntil string `json:"security_until"`
	Default       bool   `json:"default"`
}

// Deploy is one entry of a Node.js website's deploy history.
type Deploy struct {
	Release  int    `json:"release"`
	Started  int64  `json:"started"`
	Ended    int64  `json:"ended"`
	Commit   string `json:"commit"`
	Message  string `json:"message"`
	Result   string `json:"result"` // running | live | ok | failed | removed
	Error    string `json:"error"`
	Rollback bool   `json:"rollback"`
	Trigger  string `json:"trigger"` // webhook = started by a push (APP-02c)
}

// Deploys reads the deploy history of a Node.js website, newest first.
func (c *Client) Deploys(ctx context.Context, id string) ([]Deploy, error) {
	var v []Deploy
	return v, c.call(ctx, "GET", "/v1/sites/"+url.PathEscape(id)+"/deploys", nil, &v)
}

// StartDeploy builds the branch again and switches the website to it; it returns the job id.
func (c *Client) StartDeploy(ctx context.Context, id string) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", "/v1/sites/"+url.PathEscape(id)+"/deploys", nil, &v)
}

// BuildLog reads the build log of one release.
func (c *Client) BuildLog(ctx context.Context, id string, n int) (string, error) {
	var v struct {
		Log string `json:"log"`
	}
	return v.Log, c.call(ctx, "GET", fmt.Sprintf("/v1/sites/%s/deploys/%d/log", url.PathEscape(id), n), nil, &v)
}

// NewDeployKey makes a deploy key for a website about to be created from a private repository.
func (c *Client) NewDeployKey(ctx context.Context) (DeployKey, error) {
	var k DeployKey
	return k, c.call(ctx, "POST", "/v1/deploykeys", nil, &k)
}

// SiteDeployKey returns the public deploy key of a website that reads a private repository.
func (c *Client) SiteDeployKey(ctx context.Context, id string) (DeployKey, error) {
	var k DeployKey
	return k, c.call(ctx, "GET", "/v1/sites/"+url.PathEscape(id)+"/deploykey", nil, &k)
}

// EnvNames lists the names of a Node.js app's environment values (the agent never returns values).
func (c *Client) EnvNames(ctx context.Context, id string) ([]string, error) {
	var v struct {
		Names []string `json:"names"`
	}
	return v.Names, c.call(ctx, "GET", "/v1/sites/"+url.PathEscape(id)+"/env", nil, &v)
}

// SetEnv sets and deletes environment values and restarts the app.
func (c *Client) SetEnv(ctx context.Context, id string, set map[string]string, del []string) error {
	if set == nil {
		set = map[string]string{}
	}
	if del == nil {
		del = []string{}
	}
	return c.call(ctx, "POST", "/v1/sites/"+url.PathEscape(id)+"/env", map[string]any{"set": set, "delete": del}, nil)
}

// Rollback puts a kept release live again.
func (c *Client) Rollback(ctx context.Context, id string, n int) error {
	return c.call(ctx, "POST", fmt.Sprintf("/v1/sites/%s/deploys/%d/rollback", url.PathEscape(id), n), nil, nil)
}

// Laravel is one offered Laravel major version and the lowest PHP it needs.
type Laravel struct {
	Version string `json:"version"`
	MinPHP  string `json:"min_php"`
	Default bool   `json:"default"`
}

// Create is a create-site request.
type Create struct {
	App        string   `json:"app,omitempty"` // empty = WordPress
	Database   bool     `json:"database,omitempty"`
	DBEngine   string   `json:"db_engine,omitempty"` // node, go, python: mariadb | postgres
	Version    string   `json:"version,omitempty"`   // laravel: major version; node: Node.js version
	Source     string   `json:"source,omitempty"`    // node: git | files
	Dir        string   `json:"dir,omitempty"`       // node: app folder
	Repo       string   `json:"repo,omitempty"`      // node
	Branch     string   `json:"branch,omitempty"`    // node
	Build      string   `json:"build,omitempty"`     // node ("" = default)
	Start      string   `json:"start,omitempty"`     // node ("" = default)
	KeyID      string   `json:"key_id,omitempty"`    // private repository: deploy key made before the website
	Domain     string   `json:"domain"`
	Aliases    []string `json:"aliases"`
	Primary    string   `json:"primary"`
	Server     string   `json:"server"`
	PHP        string   `json:"php"`
	AdminEmail string   `json:"admin_email"`
	Title      string   `json:"title"`
}

// Job is a running or finished change.
type Job struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Site      string   `json:"site"`
	State     string   `json:"state"`
	Steps     []string `json:"steps"`
	Error     string   `json:"error"`
	HasSecret bool     `json:"has_secret"`
}

// Health checks the agent and returns its version.
func (c *Client) Health(ctx context.Context) (string, error) {
	var v struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	err := c.call(ctx, "GET", "/v1/health", nil, &v)
	return v.Version, err
}

// Catalog returns the offered versions.
func (c *Client) Catalog(ctx context.Context) (Catalog, error) {
	var v Catalog
	return v, c.call(ctx, "GET", "/v1/catalog", nil, &v)
}

// Sites lists sites with their status.
func (c *Client) Sites(ctx context.Context) ([]Site, error) {
	var v struct {
		Sites []Site `json:"sites"`
	}
	return v.Sites, c.call(ctx, "GET", "/v1/sites", nil, &v)
}

// Site returns one site.
func (c *Client) Site(ctx context.Context, id string) (Site, error) {
	var v Site
	return v, c.call(ctx, "GET", "/v1/sites/"+url.PathEscape(id), nil, &v)
}

// Plan checks a request without changing anything.
func (c *Client) Plan(ctx context.Context, r Create) (Site, error) {
	var v Site
	return v, c.call(ctx, "POST", "/v1/sites/plan", r, &v)
}

// CreateSite starts a create job.
func (c *Client) CreateSite(ctx context.Context, r Create) (string, Site, error) {
	var v struct {
		Job  string `json:"job"`
		Site Site   `json:"site"`
	}
	err := c.call(ctx, "POST", "/v1/sites", r, &v)
	return v.Job, v.Site, err
}

// Job returns a job.
func (c *Client) Job(ctx context.Context, id string) (Job, error) {
	var v Job
	return v, c.call(ctx, "GET", "/v1/jobs/"+url.PathEscape(id), nil, &v)
}

// Secret fetches a job's secret; the agent hands it out only once.
func (c *Client) Secret(ctx context.Context, id string) (string, error) {
	var v struct {
		Password string `json:"password"`
	}
	return v.Password, c.call(ctx, "POST", "/v1/jobs/"+url.PathEscape(id)+"/secret", nil, &v)
}

// Action runs start, stop or restart.
func (c *Client) Action(ctx context.Context, id, action string) error {
	return c.call(ctx, "POST", "/v1/sites/"+url.PathEscape(id)+"/"+url.PathEscape(action), nil, nil)
}

// Stack reads a site's containers with live CPU and memory.
func (c *Client) Stack(ctx context.Context, id string) (Stack, error) {
	var v Stack
	return v, c.call(ctx, "GET", "/v1/sites/"+url.PathEscape(id)+"/stack", nil, &v)
}

// RestartService restarts one container of a site (web, php or db).
func (c *Client) RestartService(ctx context.Context, id, svc string) error {
	return c.call(ctx, "POST", "/v1/sites/"+url.PathEscape(id)+"/services/"+url.PathEscape(svc)+"/restart", nil, nil)
}

// Artisan runs one listed artisan command on a Laravel site and returns its output (APP-01 Laravel).
func (c *Client) Artisan(ctx context.Context, id, op string) (string, error) {
	var v struct {
		Output string `json:"output"`
	}
	err := c.call(ctx, "POST", "/v1/sites/"+url.PathEscape(id)+"/laravel/artisan", map[string]string{"op": op}, &v)
	return v.Output, err
}

// Delete moves a site to the trash.
func (c *Client) Delete(ctx context.Context, id string) (string, error) {
	var v struct {
		Trash string `json:"trash"`
	}
	return v.Trash, c.call(ctx, "POST", "/v1/sites/"+url.PathEscape(id)+"/delete", nil, &v)
}

// Trash lists deleted sites.
func (c *Client) Trash(ctx context.Context) ([]Trashed, error) {
	var v struct {
		Trash []Trashed `json:"trash"`
	}
	return v.Trash, c.call(ctx, "GET", "/v1/trash", nil, &v)
}

// Restore brings a trash entry back.
func (c *Client) Restore(ctx context.Context, entry string) (string, error) {
	var v struct {
		Site string `json:"site"`
	}
	return v.Site, c.call(ctx, "POST", "/v1/trash/"+url.PathEscape(entry)+"/restore", nil, &v)
}

// RemoveTrash deletes a trash entry for good, so its domain is free again.
func (c *Client) RemoveTrash(ctx context.Context, entry string) error {
	return c.call(ctx, "POST", "/v1/trash/"+url.PathEscape(entry)+"/remove", nil, nil)
}

// Logs returns the last lines of one service.
func (c *Client) Logs(ctx context.Context, id, service string, lines int) (string, error) {
	var v struct {
		Logs string `json:"logs"`
	}
	q := url.Values{"service": {service}, "lines": {strconv.Itoa(lines)}}
	return v.Logs, c.call(ctx, "GET", "/v1/sites/"+url.PathEscape(id)+"/logs?"+q.Encode(), nil, &v)
}

// Delivery is what the agent did with one webhook delivery (APP-02c).
type Delivery struct {
	At     int64  `json:"at"`
	Event  string `json:"event"`
	Result string `json:"result"` // ping | deploy | queued | ignored
	Detail string `json:"detail"`
	Commit string `json:"commit"`
	Job    string `json:"job"`
}

// Webhook is the auto-deploy state of a website built from Git.
type Webhook struct {
	On         bool       `json:"on"`
	Branch     string     `json:"branch"`
	Deliveries []Delivery `json:"deliveries"`
}

// Hook is one delivery passed on from the panel's public webhook address.
type Hook struct {
	Event     string `json:"event"`
	Signature string `json:"signature,omitempty"`
	Token     string `json:"token,omitempty"`
	Body      []byte `json:"body"`
}

// Webhook reads the auto-deploy state of a website.
func (c *Client) Webhook(ctx context.Context, id string) (Webhook, error) {
	var w Webhook
	return w, c.call(ctx, "GET", "/v1/sites/"+url.PathEscape(id)+"/webhook", nil, &w)
}

// NewWebhookSecret turns auto-deploy on, or replaces its secret, and returns the secret (shown once).
func (c *Client) NewWebhookSecret(ctx context.Context, id string) (string, error) {
	var v struct {
		Secret string `json:"secret"`
	}
	err := c.call(ctx, "POST", "/v1/sites/"+url.PathEscape(id)+"/webhook/secret", nil, &v)
	return v.Secret, err
}

// RemoveWebhook turns auto-deploy off.
func (c *Client) RemoveWebhook(ctx context.Context, id string) error {
	return c.call(ctx, "POST", "/v1/sites/"+url.PathEscape(id)+"/webhook/remove", nil, nil)
}

// Deliver passes a webhook delivery to the agent, which checks its signature.
func (c *Client) Deliver(ctx context.Context, id string, h Hook) (Delivery, error) {
	var d Delivery
	return d, c.call(ctx, "POST", "/v1/sites/"+url.PathEscape(id)+"/webhook/deliver", h, &d)
}

// ImageUse is one service of a website: the image it runs and the one the agent would run (IMG-01).
type ImageUse struct {
	Service string `json:"service"`
	Current string `json:"current"`
	Wanted  string `json:"wanted"`
	Update  bool   `json:"update"`
}

// Images is the image state of a website.
type Images struct {
	Services []ImageUse `json:"services"`
	Update   bool       `json:"update"`
	Running  bool       `json:"running"`
}

// Images reads which images a website runs and whether the agent carries newer pinned ones.
func (c *Client) Images(ctx context.Context, id string) (Images, error) {
	var v Images
	return v, c.call(ctx, "GET", "/v1/sites/"+url.PathEscape(id)+"/images", nil, &v)
}

// StartImageUpdate moves a website to the newer images (pull, backup, recreate) and returns the job.
func (c *Client) StartImageUpdate(ctx context.Context, id string) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", "/v1/sites/"+url.PathEscape(id)+"/images/update", nil, &v)
}
