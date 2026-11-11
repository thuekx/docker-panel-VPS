package sites

import (
	"sync"
	"time"
)

// JobView is what the panel sees of a job. It never holds the secret.
type JobView struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Site      string    `json:"site"`
	State     string    `json:"state"` // running | done | failed
	Steps     []string  `json:"steps"`
	Error     string    `json:"error,omitempty"`
	HasSecret bool      `json:"has_secret"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended,omitempty"`
}

// Job tracks one long change. The admin password of a new site lives only here, in memory, until the panel
// fetches it once or the job expires.
type Job struct {
	ID     string
	mu     sync.Mutex
	v      JobView
	secret string
}

// Step records progress.
func (j *Job) Step(s string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.v.Steps = append(j.v.Steps, s)
}

// Fail ends the job with an error.
func (j *Job) Fail(msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.v.State, j.v.Error, j.v.Ended = "failed", msg, time.Now()
}

// Done ends the job; secret may be empty.
func (j *Job) Done(secret string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.v.State, j.v.Ended, j.secret, j.v.HasSecret = "done", time.Now(), secret, secret != ""
}

// View returns a copy safe to encode.
func (j *Job) View() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := j.v
	v.Steps = append([]string(nil), j.v.Steps...)
	return v
}

// TakeSecret returns the secret once; later calls return "".
func (j *Job) TakeSecret() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.secret
	j.secret, j.v.HasSecret = "", false
	return s
}

// JobTTL is how long a finished job (and an untaken secret) is kept.
const JobTTL = 30 * time.Minute

// Jobs is the in-memory job table.
type Jobs struct {
	mu   sync.Mutex
	jobs map[string]*Job
	now  func() time.Time
}

// NewJobs returns an empty table.
func NewJobs() *Jobs { return &Jobs{jobs: map[string]*Job{}, now: time.Now} }

// New registers a running job.
func (t *Jobs) New(kind, site string) *Job {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gc()
	id := "j" + randHex(8)
	j := &Job{ID: id, v: JobView{ID: id, Kind: kind, Site: site, State: "running", Started: t.now()}}
	t.jobs[id] = j
	return j
}

// Get returns a job by id (nil when unknown or expired).
func (t *Jobs) Get(id string) *Job {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gc()
	return t.jobs[id]
}

func (t *Jobs) gc() {
	for id, j := range t.jobs {
		v := j.View()
		if v.State != "running" && t.now().Sub(v.Ended) > JobTTL {
			j.TakeSecret()
			delete(t.jobs, id)
		}
	}
}
