package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
)

// StaticRelease is one release of a static website (SITE-03).
type StaticRelease struct {
	Release  int    `json:"release"`
	At       int64  `json:"at"`
	Name     string `json:"name"`
	Files    int    `json:"files"`
	Bytes    int64  `json:"bytes"`
	Result   string `json:"result"` // live | ok | removed
	Rollback bool   `json:"rollback"`
}

// StaticReleases is the release history with the limits.
type StaticReleases struct {
	Releases []StaticRelease `json:"releases"`
	Keep     int             `json:"keep"`
	MaxBytes int64           `json:"max_bytes"`
}

// Releases lists the releases of a static website, newest first.
func (c *Client) Releases(ctx context.Context, id string) (StaticReleases, error) {
	var v StaticReleases
	return v, c.call(ctx, "GET", site(id)+"/releases", nil, &v)
}

// PublishStatic streams a .zip to the agent, which makes it the live release.
func (c *Client) PublishStatic(ctx context.Context, id, name string, body io.Reader) (StaticRelease, error) {
	resp, err := c.stream(ctx, "PUT", site(id)+"/releases?name="+url.QueryEscape(name), body)
	if err != nil {
		return StaticRelease{}, err
	}
	defer resp.Body.Close()
	var v StaticRelease
	return v, json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&v)
}

// RollbackStatic puts a kept release back live.
func (c *Client) RollbackStatic(ctx context.Context, id string, n int) error {
	return c.call(ctx, "POST", site(id)+"/releases/"+strconv.Itoa(n)+"/rollback", nil, nil)
}
