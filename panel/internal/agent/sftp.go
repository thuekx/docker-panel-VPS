package agent

import "context"

// SFTPKey is a public key of an SFTP account (SFTP-01).
type SFTPKey struct {
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	Comment     string `json:"comment"`
}

// SFTPAccount is one SFTP login of a website; the password hash never leaves the agent.
type SFTPAccount struct {
	Name     string    `json:"name"`
	Dir      string    `json:"dir"`
	Password bool      `json:"password"`
	Keys     []SFTPKey `json:"keys"`
}

// SFTP is a website's SFTP page.
type SFTP struct {
	Ready    bool          `json:"ready"`
	Port     int           `json:"port"`
	Accounts []SFTPAccount `json:"accounts"`
}

// SFTPChange is one change of a website's SFTP accounts; Hash is bcrypt, made by the panel.
type SFTPChange struct {
	Op          string `json:"op"`
	Name        string `json:"name"`
	Hash        string `json:"hash,omitempty"`
	Key         string `json:"key,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Dir         string `json:"dir,omitempty"`
}

// SFTP returns a website's SFTP accounts.
func (c *Client) SFTP(ctx context.Context, id string) (SFTP, error) {
	var v SFTP
	return v, c.call(ctx, "GET", site(id)+"/sftp", nil, &v)
}

// ChangeSFTP applies one change at once; changed is false when nothing changed.
func (c *Client) ChangeSFTP(ctx context.Context, id string, ch SFTPChange) (bool, error) {
	var v struct {
		Changed bool `json:"changed"`
	}
	return v.Changed, c.call(ctx, "POST", site(id)+"/sftp", ch, &v)
}
