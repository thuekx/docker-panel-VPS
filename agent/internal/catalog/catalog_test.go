package catalog

import (
	"regexp"
	"testing"
)

var pinned = regexp.MustCompile(`^[a-z0-9./-]+:[a-z0-9.-]+@sha256:[0-9a-f]{64}$`)

func TestEveryImagePinned(t *testing.T) {
	imgs := []string{CLI, MariaDB, Nginx}
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
