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
