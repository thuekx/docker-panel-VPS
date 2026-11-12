package catalog

import (
	"regexp"
	"strings"
	"testing"
)

var pinned = regexp.MustCompile(`^[a-z0-9./-]+:[a-z0-9.-]+@sha256:[0-9a-f]{64}$`)

func TestEveryImagePinned(t *testing.T) {
	imgs := []string{CLI, MariaDB, Nginx, Composer}
	for _, p := range All() {
		imgs = append(imgs, p.Apache, p.FPM, p.CLI)
	}
	for _, i := range imgs {
		if !pinned.MatchString(i) {
			t.Errorf("image %q is not pinned by digest", i)
		}
	}
	if _, ok := PHP(Default); !ok {
		t.Fatal("default PHP not offered")
	}
	if p, _ := PHP("8.1"); !p.EOL {
		t.Error("8.1 must be marked end-of-life")
	}
}

func TestLaravelImage(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range All() {
		d := LaravelDockerfile(p)
		if !strings.Contains(d, "FROM "+p.FPM+"\n") || !strings.Contains(d, "FROM "+Composer+" AS composer") {
			t.Errorf("PHP %s: recipe must start from the pinned images:\n%s", p.Version, d)
		}
		img := LaravelImage(p)
		if !regexp.MustCompile(`^nostek-local/laravel:php[0-9.]+-[0-9a-f]{12}$`).MatchString(img) || seen[img] {
			t.Errorf("PHP %s: bad or reused image tag %q", p.Version, img)
		}
		seen[img] = true
	}
}
