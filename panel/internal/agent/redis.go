package agent

import "context"

// RedisConf is the Redis of a website (REDIS-01): cache (memory only, old keys go first) or store (kept on disk,
// nothing is thrown away) and its memory in MB.
type RedisConf struct {
	Mode string `json:"mode"`
	MB   int    `json:"mb"`
}

// RedisView is the Redis card of a website: whether it can have one, its settings and its figures.
type RedisView struct {
	Possible bool       `json:"possible"`
	Why      string     `json:"why"`
	Conf     *RedisConf `json:"conf"`
	Sizes    []int      `json:"sizes"`
	Kind     string     `json:"kind"` // wordpress | laravel | php | runtime
	Running  bool       `json:"running"`
	Used     int64      `json:"used"`
	Keys     int64      `json:"keys"`
	Hits     int64      `json:"hits"`
	Misses   int64      `json:"misses"`
	Job      *Job       `json:"job"`
}

// HitRate is the share of reads Redis answered, in percent (-1 = no reads yet).
func (v RedisView) HitRate() int {
	if v.Hits+v.Misses == 0 {
		return -1
	}
	return int(v.Hits * 100 / (v.Hits + v.Misses))
}

// Redis returns the Redis card of a website.
func (c *Client) Redis(ctx context.Context, id string) (RedisView, error) {
	var v RedisView
	return v, c.call(ctx, "GET", site(id)+"/redis", nil, &v)
}

// SetRedis turns a website's Redis on (with these settings) or off (nil) and returns the job.
func (c *Client) SetRedis(ctx context.Context, id string, conf *RedisConf) (string, error) {
	body := map[string]any{"on": false}
	if conf != nil {
		body = map[string]any{"on": true, "mode": conf.Mode, "mb": conf.MB}
	}
	var v struct {
		Job string `json:"job"`
	}
	return v.Job, c.call(ctx, "POST", site(id)+"/redis", body, &v)
}

// FlushRedis empties a website's Redis.
func (c *Client) FlushRedis(ctx context.Context, id string) error {
	return c.call(ctx, "POST", site(id)+"/redis/flush", nil, nil)
}

// UsedMB is the memory Redis uses now, in whole MB (at least 1 once it holds anything).
func (v RedisView) UsedMB() int64 {
	if v.Used > 0 && v.Used < 1<<20 {
		return 1
	}
	return v.Used >> 20
}
