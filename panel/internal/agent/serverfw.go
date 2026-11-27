package agent

import "context"

// FWRule is one ufw rule (SRV-01).
type FWRule struct {
	N       int    `json:"n"`
	To      string `json:"to"`
	Action  string `json:"action"`
	From    string `json:"from"`
	Comment string `json:"comment"`
	V6      bool   `json:"v6"`
	Locked  string `json:"locked"`
}

// FWTrial is a firewall change waiting for its confirmation.
type FWTrial struct {
	Started  int64  `json:"started"`
	Deadline int64  `json:"deadline"`
	What     string `json:"what"`
}

// Jail is one fail2ban jail.
type Jail struct {
	Name   string   `json:"name"`
	Banned []string `json:"banned"`
	Failed int      `json:"failed"`
	Total  int      `json:"total"`
}

// ServerSecurity is the firewall and fail2ban.
type ServerSecurity struct {
	Active   bool     `json:"active"`
	Rules    []FWRule `json:"rules"`
	SSHPorts []int    `json:"ssh_ports"`
	Trial    *FWTrial `json:"trial"`
	FWError  string   `json:"fw_error"`
	Jails    []Jail   `json:"jails"`
	Allow    []string `json:"allow"`
	F2BError string   `json:"f2b_error"`
}

// FWChange is one firewall change.
type FWChange struct {
	Op    string `json:"op"`
	Port  string `json:"port"`
	Proto string `json:"proto"`
	From  string `json:"from"`
	Rule  int    `json:"rule"`
	Match string `json:"match"`
}

// ServerSecurity reads the firewall and fail2ban.
func (c *Client) ServerSecurity(ctx context.Context) (ServerSecurity, error) {
	var v ServerSecurity
	return v, c.call(ctx, "GET", "/v1/server-security", nil, &v)
}

// FirewallChange applies one change as a trial.
func (c *Client) FirewallChange(ctx context.Context, ch FWChange) (FWTrial, error) {
	var t FWTrial
	return t, c.call(ctx, "POST", "/v1/firewall", ch, &t)
}

// FirewallConfirm keeps the change on trial; FirewallUndo puts the rules from before back.
func (c *Client) FirewallConfirm(ctx context.Context) error {
	return c.call(ctx, "POST", "/v1/firewall/confirm", nil, nil)
}

// FirewallUndo puts the rules from before the change back.
func (c *Client) FirewallUndo(ctx context.Context) error {
	return c.call(ctx, "POST", "/v1/firewall/undo", nil, nil)
}

// Unban lifts one fail2ban ban.
func (c *Client) Unban(ctx context.Context, jail, ip string) error {
	return c.call(ctx, "POST", "/v1/fail2ban/unban", map[string]string{"jail": jail, "ip": ip}, nil)
}

// SetFail2banAllow writes the allow-list.
func (c *Client) SetFail2banAllow(ctx context.Context, list []string) ([]string, error) {
	var v struct {
		Allow []string `json:"allow"`
	}
	return v.Allow, c.call(ctx, "POST", "/v1/fail2ban/allow", map[string][]string{"allow": list}, &v)
}
