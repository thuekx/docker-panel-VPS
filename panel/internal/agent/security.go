package agent

// Security calls (SEC-01).

import "context"

// Protected is a folder that asks for a login; Users holds the user names.
type Protected struct {
	Path  string   `json:"path"`
	Users []string `json:"users"`
}

// Hotlink protection: Allow lists the extra sites that may embed the website's images and media.
type Hotlink struct {
	Allow []string `json:"allow"`
}

// MIME maps file extensions to a content type (SEC-01 wave 2).
type MIME struct {
	Type string   `json:"type"`
	Exts []string `json:"exts"`
}

// Security is the Security page of a website.
type Security struct {
	Blocked     []string    `json:"blocked"`
	Protected   []Protected `json:"protected"`
	Hotlink     *Hotlink    `json:"hotlink"`
	HotlinkExts []string    `json:"hotlink_exts"`
	WebServer   bool        `json:"web_server"`
	ErrorPages  []int       `json:"error_pages"`
	ErrorCodes  []int       `json:"error_codes"`
	ErrorDir    string      `json:"error_dir"`
	MIME        []MIME      `json:"mime"`
	Job         *Job        `json:"job"`
	Logins      Logins      `json:"logins"`
	Scan        Scan        `json:"scan"`
	WAF         WAF         `json:"waf"`
	WAFReady    bool        `json:"waf_ready"`
	Allowed     []string    `json:"allowed"` // SITE-03: only these reach the website (empty = everyone)
}

// WAFEvent is one rule the firewall matched on a visit (SEC-01 wave 3b).
type WAFEvent struct {
	Time   int64  `json:"t"`
	IP     string `json:"ip"`
	URI    string `json:"uri"`
	Rule   int    `json:"rule"`
	Msg    string `json:"msg"`
	Data   string `json:"data"`
	Action string `json:"action"`
}

// WAFRule is how often a rule matched.
type WAFRule struct {
	Rule     int    `json:"rule"`
	Msg      string `json:"msg"`
	Count    int    `json:"count"`
	Excluded bool   `json:"excluded"`
}

// WAF is a website's firewall: its setting and latest events.
type WAF struct {
	On       bool       `json:"on"`
	Mode     string     `json:"mode"`
	Paranoia int        `json:"paranoia"`
	Exclude  []int      `json:"exclude"`
	Skip     []string   `json:"skip"`
	Blocked  int        `json:"blocked"`
	Events   []WAFEvent `json:"events"`
	Rules    []WAFRule  `json:"rules"`
}

// LoginIP is one address that tried to log in to the website (SEC-01 wave 3).
type LoginIP struct {
	IP       string `json:"ip"`
	Day      int    `json:"day"`
	DayFail  int    `json:"day_fail"`
	Week     int    `json:"week"`
	LastSeen int64  `json:"last_seen"`
	LastPath string `json:"last_path"`
	Blocked  bool   `json:"blocked"`
}

// Logins is the login attempts of the last 24 hours and 7 days; Active is false until the edge writes its log.
type Logins struct {
	Active  bool      `json:"active"`
	Since   int64     `json:"since"`
	Day     int       `json:"day"`
	DayFail int       `json:"day_fail"`
	Week    int       `json:"week"`
	IPs     []LoginIP `json:"ips"`
}

// ScanFound is one file the malware scan flagged.
type ScanFound struct {
	Path      string `json:"path"`
	Signature string `json:"signature"`
	Gone      bool   `json:"gone"`
}

// ScanResult is the last malware scan of a website.
type ScanResult struct {
	Started    int64       `json:"started"`
	Finished   int64       `json:"finished"`
	Files      int         `json:"files"`
	Found      []ScanFound `json:"found"`
	More       int         `json:"more"`
	Signatures string      `json:"signatures"`
	Warning    string      `json:"warning"`
	Error      string      `json:"error"`
}

// Scan is the malware card: the last result and the scan that runs now.
type Scan struct {
	Last  *ScanResult `json:"last"`
	Job   *Job        `json:"job"`
	MinMB int         `json:"min_mb"`
}

// SecurityChange is one change: op block-add | block-remove | protect-set | protect-user-remove | protect-remove |
// hotlink-on | hotlink-off. Hash is a bcrypt hash made by the panel: the password never leaves the panel.
type SecurityChange struct {
	Op    string   `json:"op"`
	IP    string   `json:"ip,omitempty"`
	Path  string   `json:"path,omitempty"`
	User  string   `json:"user,omitempty"`
	Hash  string   `json:"hash,omitempty"`
	Allow []string `json:"allow,omitempty"`
	Code  int      `json:"code,omitempty"`
	Type  string   `json:"type,omitempty"`
	Exts  string   `json:"exts,omitempty"`
	Mode  string   `json:"mode,omitempty"`
	Level int      `json:"level,omitempty"`
	Rule  int      `json:"rule,omitempty"`
	// optimize-set (PHP-01)
	Compress bool `json:"compress,omitempty"`
	Days     int  `json:"days,omitempty"`
	// header-set | header-remove (SITE-03)
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
}

// Security returns a website's edge rules.
func (c *Client) Security(ctx context.Context, id string) (Security, error) {
	var v Security
	return v, c.call(ctx, "GET", site(id)+"/security", nil, &v)
}

// ChangeSecurity starts the job that applies a change; "" when nothing changes.
func (c *Client) ChangeSecurity(ctx context.Context, id string, ch SecurityChange) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", site(id)+"/security", ch, &v)
}

// StartScan starts a malware scan of the website's files.
func (c *Client) StartScan(ctx context.Context, id string) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", site(id)+"/security/scan", nil, &v)
}
