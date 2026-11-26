package agent

// Domain calls (DOM-01).

import "context"

// Redirect sends one path of a website elsewhere (301 permanent, 302 temporary).
type Redirect struct {
	From string `json:"from"`
	To   string `json:"to"`
	Code int    `json:"code"`
	// Prefix: the whole folder moves; /old/a/b goes to <to>/a/b.
	Prefix bool `json:"prefix,omitempty"`
}

// DomainCheck is one name of a website with what DNS and the edge say about it now. DNS: here | elsewhere | mixed
// | none | error. Cert (routed names only): valid | pending | error.
type DomainCheck struct {
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	Routed      bool     `json:"routed"`
	DNS         string   `json:"dns"`
	IPv4        []string `json:"ipv4"`
	IPv6        []string `json:"ipv6"`
	Error       string   `json:"error"`
	Cert        string   `json:"cert"`
	CertIssuer  string   `json:"cert_issuer"`
	CertExpires int64    `json:"cert_expires"`
	CertError   string   `json:"cert_error"`
}

// Domains is the Domains page of a website.
type Domains struct {
	PublicIPv4  string        `json:"public_ipv4"`
	PreviewZone string        `json:"preview_zone"`
	Canonical   string        `json:"canonical"`
	Hosts       []DomainCheck `json:"hosts"`
	Redirects   []Redirect    `json:"redirects"`
	MaxAliases  int           `json:"max_aliases"`
	Job         *Job          `json:"job"`
	// CERT-01: the wildcard zone in use (""), the zones the website may take one of, whether ClouDNS is set
	Wildcard      string   `json:"wildcard"`
	WildcardZones []string `json:"wildcard_zones"`
	DNSProvider   bool     `json:"dns_provider"`
}

// DomainChange is one change: op add | remove | refresh | redirect-add | redirect-remove.
type DomainChange struct {
	Op     string `json:"op"`
	Name   string `json:"name,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Code   int    `json:"code,omitempty"`
	Prefix bool   `json:"prefix,omitempty"`
}

// Domains checks every name of a website.
func (c *Client) Domains(ctx context.Context, id string) (Domains, error) {
	var d Domains
	return d, c.call(ctx, "GET", site(id)+"/domains", nil, &d)
}

// ChangeDomains starts the job that applies a change; "" when nothing changes.
func (c *Client) ChangeDomains(ctx context.Context, id string, ch DomainChange) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", site(id)+"/domains", ch, &v)
}
