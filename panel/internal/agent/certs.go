package agent

// Own certificate calls (CERT-01). The private key is sent once; the agent never answers it.

import "context"

// CertInfo is a website's own certificate, without key material.
type CertInfo struct {
	Own       bool     `json:"own"`
	Names     []string `json:"names"`
	Issuer    string   `json:"issuer"`
	NotBefore int64    `json:"not_before"`
	NotAfter  int64    `json:"not_after"`
	Trusted   bool     `json:"trusted"`
	Uncovered []string `json:"uncovered"`
	Uploaded  int64    `json:"uploaded"`
	SelfSign  bool     `json:"self_signed"` // CERT-02
}

// Cert returns a website's own certificate (Own = false when it uses Let's Encrypt).
func (c *Client) Cert(ctx context.Context, id string) (CertInfo, error) {
	var v CertInfo
	return v, c.call(ctx, "GET", site(id)+"/cert", nil, &v)
}

// SetCert stores and serves a website's own certificate.
func (c *Client) SetCert(ctx context.Context, id, cert, key string) (CertInfo, error) {
	var v CertInfo
	return v, c.call(ctx, "PUT", site(id)+"/cert", map[string]string{"cert": cert, "key": key}, &v)
}

// RemoveCert takes a website back to Let's Encrypt.
func (c *Client) RemoveCert(ctx context.Context, id string) error {
	return c.call(ctx, "POST", site(id)+"/cert/delete", nil, nil)
}

// CSRInput is a certificate request (CERT-02): the key is made and kept on the server.
type CSRInput struct {
	Names   []string `json:"names"`
	KeyType string   `json:"key"`
	Org     string   `json:"org"`
	Unit    string   `json:"unit"`
	City    string   `json:"city"`
	State   string   `json:"state"`
	Country string   `json:"country"`
}

// CertRequest is a waiting request: its CSR (public) and what it asks for.
type CertRequest struct {
	CSR     string   `json:"csr"`
	Names   []string `json:"names"`
	KeyType string   `json:"key"`
	Created int64    `json:"created"`
}

// CertRequest returns the waiting request of a website (CSR empty when there is none).
func (c *Client) CertRequest(ctx context.Context, id string) (CertRequest, error) {
	var v CertRequest
	return v, c.call(ctx, "GET", site(id)+"/cert/csr", nil, &v)
}

// CreateCSR makes a key on the server and a request for it; a new request replaces a waiting one.
func (c *Client) CreateCSR(ctx context.Context, id string, in CSRInput) (CertRequest, error) {
	var v CertRequest
	return v, c.call(ctx, "POST", site(id)+"/cert/csr", in, &v)
}

// DeleteCSR throws a waiting request and its key away.
func (c *Client) DeleteCSR(ctx context.Context, id string) error {
	return c.call(ctx, "POST", site(id)+"/cert/csr/delete", nil, nil)
}

// SelfSigned makes and serves a self-signed certificate for names of the website.
func (c *Client) SelfSigned(ctx context.Context, id string, names []string, key string, days int) (CertInfo, error) {
	var v CertInfo
	return v, c.call(ctx, "POST", site(id)+"/cert/self-signed", map[string]any{"names": names, "key": key, "days": days}, &v)
}

// DNSConfig is the DNS provider the edge uses for wildcard certificates (never the password).
type DNSConfig struct {
	Provider   string   `json:"provider"`
	SubAuthID  string   `json:"sub_auth_id"`
	Configured bool     `json:"configured"`
	Since      int64    `json:"since"`
	Sites      []string `json:"sites"`
	EdgeError  string   `json:"edge_error"` // the last edge restart failed with this
}

// DNS returns the DNS provider settings.
func (c *Client) DNS(ctx context.Context) (DNSConfig, error) {
	var v DNSConfig
	return v, c.call(ctx, "GET", "/v1/dns", nil, &v)
}

// SetDNS checks a ClouDNS API sub-user and gives it to the edge (the edge restarts for a few seconds).
func (c *Client) SetDNS(ctx context.Context, subAuthID, password string) (DNSConfig, error) {
	var v DNSConfig
	return v, c.call(ctx, "POST", "/v1/dns", map[string]string{"provider": "cloudns", "sub_auth_id": subAuthID, "password": password}, &v)
}

// RemoveDNS takes the sub-user away (refused while a website uses a wildcard).
func (c *Client) RemoveDNS(ctx context.Context) error {
	return c.call(ctx, "POST", "/v1/dns/delete", nil, nil)
}

// StartWildcard turns a website's wildcard certificate on (zone) or off (""); it returns the job id.
func (c *Client) StartWildcard(ctx context.Context, id, zone string) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", site(id)+"/wildcard", map[string]string{"zone": zone}, &v)
}
