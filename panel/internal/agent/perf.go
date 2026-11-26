package agent

import "context"

// PHPChoice is one PHP version a website can move to (PHP-01); Why says why it is not offered.
type PHPChoice struct {
	Version string `json:"version"`
	EOL     bool   `json:"eol"`
	Why     string `json:"why"`
}

// PHPState is the PHP version of a website, the versions it can move to and the one it came from.
type PHPState struct {
	Current  string      `json:"current"`
	Previous string      `json:"previous"`
	Choices  []PHPChoice `json:"choices"`
	Running  bool        `json:"running"`
}

// Optimize is what the edge does for a website's answers: compression and browser cache days of static files.
type Optimize struct {
	Compress  bool `json:"compress"`
	CacheDays int  `json:"cache_days"`
}

// Perf is the Performance page of a website; PHP is nil when the website runs no PHP.
type Perf struct {
	PHP       *PHPState `json:"php"`
	Optimize  Optimize  `json:"optimize"`
	CacheDays []int     `json:"cache_days"`
	Job       *Job      `json:"job"`
	// SITE-03
	Headers       []HeaderRule `json:"headers"`
	HeaderPresets []string     `json:"header_presets"`
}

// HeaderRule is one response header of a website at the edge (Path "/" = every answer; Value "" removes it).
type HeaderRule struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Perf returns a website's Performance page.
func (c *Client) Perf(ctx context.Context, id string) (Perf, error) {
	var v Perf
	return v, c.call(ctx, "GET", site(id)+"/perf", nil, &v)
}

// StartPHPChange moves a website to another PHP version (backup first) and returns the job.
func (c *Client) StartPHPChange(ctx context.Context, id, php string) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", site(id)+"/php", map[string]string{"php": php}, &v)
}
