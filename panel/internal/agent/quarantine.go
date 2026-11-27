package agent

import (
	"context"
	"net/url"
)

// ScanSchedule is a website's scheduled malware scan (SEC-02).
type ScanSchedule struct {
	Every      string `json:"every"`
	Quarantine bool   `json:"quarantine"`
}

// QFile is one file in the quarantine.
type QFile struct {
	ID        string `json:"id"`
	Site      string `json:"site"`
	Domain    string `json:"domain"`
	Path      string `json:"path"`
	Signature string `json:"signature"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	At        int64  `json:"at"`
	Auto      bool   `json:"auto"`
}

// SetScanSchedule sets a website's scheduled scan ("" = off).
func (c *Client) SetScanSchedule(ctx context.Context, site, every string, quarantine bool) error {
	return c.call(ctx, "POST", "/v1/sites/"+url.PathEscape(site)+"/security/scan-schedule", map[string]any{"every": every, "quarantine": quarantine}, nil)
}

// QuarantineFile moves one file of a website to the quarantine.
func (c *Client) QuarantineFile(ctx context.Context, site, path, signature string) (QFile, error) {
	var q QFile
	return q, c.call(ctx, "POST", "/v1/sites/"+url.PathEscape(site)+"/quarantine", map[string]string{"path": path, "signature": signature}, &q)
}

// Quarantine lists the quarantine.
func (c *Client) Quarantine(ctx context.Context) ([]QFile, error) {
	var v struct {
		Files []QFile `json:"files"`
	}
	return v.Files, c.call(ctx, "GET", "/v1/quarantine", nil, &v)
}

// QuarantineRestore puts a file back; QuarantineDelete removes it for good.
func (c *Client) QuarantineRestore(ctx context.Context, id string) (QFile, error) {
	var q QFile
	return q, c.call(ctx, "POST", "/v1/quarantine/"+url.PathEscape(id)+"/restore", nil, &q)
}

// QuarantineDelete removes a file from the quarantine for good.
func (c *Client) QuarantineDelete(ctx context.Context, id string) (QFile, error) {
	var q QFile
	return q, c.call(ctx, "POST", "/v1/quarantine/"+url.PathEscape(id)+"/delete", nil, &q)
}
