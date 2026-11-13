package sites

// Update image (IMG-01). Every image a website runs is pinned by digest in the agent's catalogue; a new agent
// release may carry newer pins (a WordPress or PHP patch, MariaDB, PostgreSQL, Nginx, a runtime, or a new recipe
// of a built image). A website never moves to them by itself: the agent keeps the images its containers run when
// it rewrites a compose file, and the owner presses "Update image". That pulls the new images first (nothing
// changes if a pull fails), makes a "before-update" backup, then recreates the containers on the new images; if
// they do not come up healthy, the previous compose file and images are put back. Never a floating "latest".

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

// ImageUse is one service of a website: the image it runs and the one this agent would run.
type ImageUse struct {
	Service string `json:"service"`
	Current string `json:"current"`
	Wanted  string `json:"wanted"`
	Update  bool   `json:"update"`
}

// Images is the image state of a website, as the panel shows it.
type Images struct {
	Services []ImageUse `json:"services"`
	Update   bool       `json:"update"`
	Running  bool       `json:"running"` // an image update job of this website is running
}

// localImage marks an image built on the server (Laravel, PHP with PostgreSQL); it is built, never pulled.
const localImage = "nostek-local/"

type svcImage struct{ service, image string }

// composeImages reads the image of every service of a compose file the agent rendered, in file order.
func composeImages(cf string) []svcImage {
	var out []svcImage
	in, svc := false, ""
	for _, l := range strings.Split(cf, "\n") {
		switch {
		case l == "services:":
			in = true
		case l != "" && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "#"):
			in = false
		case !in:
		case strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") && strings.HasSuffix(l, ":"):
			svc = strings.TrimSuffix(strings.TrimSpace(l), ":")
		case strings.HasPrefix(l, "    image: ") && svc != "":
			out = append(out, svcImage{svc, strings.TrimPrefix(l, "    image: ")})
		}
	}
	return out
}

func imageMap(cf string) map[string]string {
	m := map[string]string{}
	for _, x := range composeImages(cf) {
		m[x.service] = x.image
	}
	return m
}

// keepImages returns cf with the image of each service set back to the one in old, so rewriting a compose file
// never moves a website to newer images; services named in except take the image of cf (a deploy builds and runs
// the app on the newest pinned runtime). A service old does not have keeps the image of cf.
func keepImages(cf, old string, except ...string) string {
	keep := imageMap(old)
	for _, e := range except {
		delete(keep, e)
	}
	if len(keep) == 0 {
		return cf
	}
	lines := strings.Split(cf, "\n")
	in, svc := false, ""
	for i, l := range lines {
		switch {
		case l == "services:":
			in = true
		case l != "" && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "#"):
			in = false
		case !in:
		case strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") && strings.HasSuffix(l, ":"):
			svc = strings.TrimSuffix(strings.TrimSpace(l), ":")
		case strings.HasPrefix(l, "    image: ") && svc != "":
			if img, ok := keep[svc]; ok {
				lines[i] = "    image: " + img
			}
		}
	}
	return strings.Join(lines, "\n")
}

// buildRecipe is the Dockerfile of a website whose image is built on the server, or "".
func buildRecipe(s Site) string {
	p, _ := catalog.PHP(s.PHP)
	switch {
	case s.IsLaravel() && s.IsPostgres():
		return catalog.LaravelPostgresDockerfile(p)
	case s.IsLaravel():
		return catalog.LaravelDockerfile(p)
	case s.AppName() == spec.AppPHP && s.IsPostgres():
		return catalog.PHPPostgresDockerfile(catalog.PHPPostgresBase(p, s.Server))
	}
	return ""
}

func (m *Manager) composePath(id string) string { return filepath.Join(m.dir(id), "compose.yml") }
func (m *Manager) recipePath(id string) string {
	return filepath.Join(m.dir(id), "build", "Dockerfile")
}

// ImageStatus compares the images a website's containers run with the ones this agent would run.
func (m *Manager) ImageStatus(ctx context.Context, id string) (Images, error) {
	s, err := m.readySite(id)
	if err != nil {
		return Images{}, err
	}
	want, err := s.Compose()
	if err != nil {
		return Images{}, fmt.Errorf("%w: this agent no longer offers the version this website runs", ErrBadInput)
	}
	disk, _ := os.ReadFile(m.composePath(id))
	cur := imageMap(string(disk))
	// a container runs the image it was created with, even after the compose file was rewritten
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := m.compose(ctx, id, nil, "ps", "--all", "--format", "json"); err == nil {
		if rows, err := parsePS(out); err == nil {
			for _, r := range rows {
				if r.Service != "" && r.Image != "" {
					cur[r.Service] = r.Image
				}
			}
		}
	}
	img := Images{Services: []ImageUse{}}
	for _, w := range composeImages(want) {
		u := ImageUse{Service: w.service, Current: cur[w.service], Wanted: w.image}
		u.Update = u.Current != u.Wanted
		img.Update = img.Update || u.Update
		img.Services = append(img.Services, u)
	}
	for _, v := range m.jobs.List() {
		if v.Site == id && v.Kind == "image" && v.State == "running" {
			img.Running = true
		}
	}
	return img, nil
}

// StartImageUpdate moves a website to the images this agent carries: pull, backup, recreate; put back on failure.
func (m *Manager) StartImageUpdate(id string) (string, error) {
	img, err := m.ImageStatus(context.Background(), id)
	if err != nil {
		return "", err
	}
	if !img.Update {
		return "", fmt.Errorf("%w: the website already runs the newest images of this panel version", ErrBadInput)
	}
	s, err := m.readySite(id)
	if err != nil {
		return "", err
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	j := m.jobs.New("image", id)
	go func() {
		defer m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
		defer cancel()
		if err := m.updateImages(ctx, j, s, img); err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done("")
	}()
	return j.ID, nil
}

func (m *Manager) updateImages(ctx context.Context, j *Job, s Site, img Images) error {
	// 1. pull the new images while the website keeps running on the old ones
	j.Step("pull new images")
	seen := map[string]bool{}
	for _, u := range img.Services {
		if !u.Update || strings.HasPrefix(u.Wanted, localImage) || seen[u.Wanted] {
			continue
		}
		seen[u.Wanted] = true
		if out, err := m.run.Docker(ctx, m.dir(s.ID), nil, "image", "inspect", "--format", "{{.Id}}", u.Wanted); err == nil &&
			strings.HasPrefix(strings.TrimSpace(string(out)), "sha256:") {
			continue // already on this server
		}
		if out, err := m.run.Docker(ctx, m.dir(s.ID), nil, "pull", "--quiet", u.Wanted); err != nil {
			return fmt.Errorf("pull %s failed, nothing was changed: %s", u.Wanted, tail(out))
		}
	}
	// 2. a backup the owner can restore if the website misbehaves on the new images
	name, err := m.backup(ctx, j, s, "before-update")
	if err != nil {
		return fmt.Errorf("the backup failed, nothing was changed: %v", err)
	}
	// 3. the new compose file (and build recipe), then build and recreate
	oldCF, err := os.ReadFile(m.composePath(s.ID))
	if err != nil {
		return err
	}
	oldRecipe, recErr := os.ReadFile(m.recipePath(s.ID))
	cf, err := s.Compose()
	if err != nil {
		return err
	}
	putBack := func(why string) error {
		_ = writeFile(m.composePath(s.ID), oldCF, 0o644)
		if recErr == nil {
			_ = writeFile(m.recipePath(s.ID), oldRecipe, 0o644)
		}
		j.Step("put back the previous images")
		if out, err := m.compose(ctx, s.ID, nil, "up", "-d", "--wait", "--wait-timeout", "300"); err != nil {
			return fmt.Errorf("%s; the previous images did not start either (%s): restore the backup %s", why, tail(out), name)
		}
		return fmt.Errorf("%s; the website runs on its previous images again", why)
	}
	if err := writeFile(m.composePath(s.ID), []byte(cf), 0o644); err != nil {
		return err
	}
	if rec := buildRecipe(s); rec != "" {
		if err := os.MkdirAll(filepath.Dir(m.recipePath(s.ID)), 0o755); err != nil {
			return putBack(err.Error())
		}
		if err := writeFile(m.recipePath(s.ID), []byte(rec), 0o644); err != nil {
			return putBack(err.Error())
		}
		j.Step("build image")
		if out, err := m.compose(ctx, s.ID, nil, "build", "--quiet", buildService(s)); err != nil {
			return putBack("the image build failed: " + tail(out))
		}
	}
	j.Step("recreate containers")
	if out, err := m.compose(ctx, s.ID, nil, "up", "-d", "--wait", "--wait-timeout", "300"); err != nil {
		return putBack("the website did not start on the new images: " + tail(out))
	}
	j.Step("done " + name) // names the backup, as a backup job does
	return nil
}
