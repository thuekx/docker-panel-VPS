package web

// Update image (IMG-01): the Overview card that shows each container's pinned image and, when this panel version
// carries newer pins, the one button that moves the website to them (the agent pulls, backs up, then recreates).

import (
	"strings"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

type imageRow struct {
	Service string
	Current string
	Wanted  string
	Update  bool
}

type imagesView struct {
	Rows    []imageRow
	Update  bool
	Running bool
}

// shortImage shows an image the way people read it: name:tag and the first characters of its digest.
func shortImage(ref string) string {
	if ref == "" {
		return "-"
	}
	if d, ok := strings.CutPrefix(ref, "sha256:"); ok { // a container on an image that lost its name
		return "sha256:" + d[:min(12, len(d))]
	}
	name, digest, ok := strings.Cut(ref, "@sha256:")
	if !ok {
		return ref
	}
	return name + " · " + digest[:min(8, len(digest))]
}

func imagesOf(img agent.Images) *imagesView {
	v := &imagesView{Update: img.Update, Running: img.Running}
	for _, u := range img.Services {
		v.Rows = append(v.Rows, imageRow{Service: u.Service, Current: shortImage(u.Current), Wanted: shortImage(u.Wanted), Update: u.Update})
	}
	return v
}
