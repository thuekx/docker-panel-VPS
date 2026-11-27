package agent

import "context"

// IonCubeCache is what the agent keeps of ionCube's loader archive (PHP-02).
type IonCubeCache struct {
	Fetched  int64    `json:"fetched"`
	SHA256   string   `json:"sha256"`
	URL      string   `json:"url"`
	Versions []string `json:"versions"`
}

// IonCubeView is the ionCube Loader card of a website.
type IonCubeView struct {
	Possible bool          `json:"possible"`
	Why      string        `json:"why"`
	On       bool          `json:"on"`
	PHP      string        `json:"php"`
	Loaded   string        `json:"loaded"`
	Cache    *IonCubeCache `json:"cache"`
	Job      *Job          `json:"job"`
}

// IonCube returns the ionCube Loader card of a website.
func (c *Client) IonCube(ctx context.Context, id string) (IonCubeView, error) {
	var v IonCubeView
	return v, c.call(ctx, "GET", site(id)+"/ioncube", nil, &v)
}

// SetIonCube turns the ionCube Loader of a website on or off and returns the job.
func (c *Client) SetIonCube(ctx context.Context, id string, on bool) (string, error) {
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", site(id)+"/ioncube", map[string]bool{"on": on}, &v)
}
