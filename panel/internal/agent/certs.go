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
