package sites

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

// ionTar is a fake loader archive with loaders for versions (and their thread-safe twins, which are skipped).
func ionTar(t *testing.T, versions ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	tw := tar.NewWriter(zw)
	add := func(name, body string) {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	add("ioncube/LICENSE.txt", "license")
	for _, v := range versions {
		add("ioncube/ioncube_loader_lin_"+v+".so", "ELF loader "+v)
		add("ioncube/ioncube_loader_lin_"+v+"_ts.so", "ELF ts "+v)
	}
	add("ioncube/../evil.so", "x")
	_ = tw.Close()
	_ = zw.Close()
	return b.Bytes()
}

func ionServer(t *testing.T, m *Manager, archive *[]byte) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(*archive) }))
	t.Cleanup(srv.Close)
	m.ionURL, m.ionHTTP = srv.URL+"/ioncube_loaders_lin_x86-64.tar.gz", srv.Client()
}

func TestIonCubeCompose(t *testing.T) {
	for _, c := range []struct {
		app, server string
		svcs        []string
	}{{spec.AppWordPress, "nginx", []string{"php"}}, {spec.AppWordPress, "apache", []string{"wordpress"}}, {spec.AppLaravel, "nginx", []string{"php"}}, {spec.AppPHP, "nginx", []string{"php"}}} {
		s := redisSite(c.app, c.server)
		s.Redis = nil
		s.IonCube = true
		cf, err := s.Compose()
		if err != nil {
			t.Fatal(err)
		}
		for _, svc := range c.svcs {
			b := svcBlock(cf, svc)
			if b == "" {
				b = svcBlock(cf, "web")
			}
			if !strings.Contains(b, "- ./ioncube/ioncube_loader_lin_"+s.PHP+".so:/usr/local/lib/nostek-ioncube.so:ro") || !strings.Contains(b, "- ./ioncube/00-ioncube.ini:/usr/local/etc/php/conf.d/00-ioncube.ini:ro") {
				t.Errorf("%s/%s: %s lacks the loader:\n%s", c.app, c.server, svc, b)
			}
		}
		if strings.Count(cf, "nostek-ioncube.so") != strings.Count(cf, "zz-nostek.ini:ro") {
			t.Errorf("%s/%s: loader not on every PHP service", c.app, c.server)
		}
		s.IonCube = false
		if plain, _ := s.Compose(); strings.Contains(plain, "ioncube") {
			t.Errorf("%s: loader while off", c.app)
		}
	}
}

func TestIonCubeOnOff(t *testing.T) {
	m, f, id := readySite(t)
	s, _ := m.Get(id)
	archive := ionTar(t, "8.1", "8.2", "8.3", s.PHP)
	ionServer(t, m, &archive)
	loaded := true
	f.hook = func(args []string) ([]byte, error, bool) {
		if strings.HasSuffix(strings.Join(args, " "), "php -v") {
			out := "PHP " + s.PHP + ".1 (cli)\nCopyright (c) The PHP Group\nZend Engine v4\n"
			if loaded {
				out += "    with the ionCube PHP Loader v14.4.0, Copyright (c) 2002-2025, by ionCube Ltd.\n"
			}
			return []byte(out), nil, true
		}
		return nil, nil, false
	}
	job, err := m.StartIonCube(id, true)
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" || strings.Join(v.Steps, ",") != "loader,restart website,check" {
		t.Fatalf("on: %+v", v)
	}
	dir := m.dir(id)
	if b, _ := os.ReadFile(filepath.Join(dir, "ioncube", "ioncube_loader_lin_"+s.PHP+".so")); string(b) != "ELF loader "+s.PHP {
		t.Fatalf("loader %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "ioncube", "00-ioncube.ini")); !strings.Contains(string(b), "zend_extension=/usr/local/lib/nostek-ioncube.so") {
		t.Fatalf("ini %q", b)
	}
	if cf, _ := os.ReadFile(filepath.Join(dir, "compose.yml")); !strings.Contains(string(cf), "nostek-ioncube.so:ro") {
		t.Fatal("compose without the loader")
	}
	v, err := m.IonCube(context.Background(), id)
	sum := sha256.Sum256(archive)
	if err != nil || !v.On || !strings.HasPrefix(v.Loaded, "the ionCube PHP Loader v14.4.0") || v.Cache == nil || v.Cache.SHA256 != hex.EncodeToString(sum[:]) || len(v.Cache.Versions) != 4 {
		t.Fatalf("view %+v %v", v, err)
	}
	if _, err := os.Stat(filepath.Join(m.ionCacheDir(), "ioncube_loader_lin_8.1_ts.so")); err == nil {
		t.Fatal("thread-safe loader kept")
	}
	if _, err := os.Stat(filepath.Join(m.cfg.Data, "cache", "evil.so")); err == nil {
		t.Fatal("path outside the cache written")
	}
	// a PHP version the archive has no loader for is not offered
	if why := m.phpWhy(Site{App: spec.AppWordPress, IonCube: true}, "8.5"); !strings.Contains(why, "no loader for PHP 8.5") || s.PHP == "8.5" {
		t.Fatalf("why %q (site PHP %s)", why, s.PHP)
	}
	if _, err := m.StartIonCube(id, true); err == nil {
		t.Fatal("on twice")
	}
	// a PHP change brings the loader of the new version and drops the old one
	to := "8.3"
	if s.PHP == to {
		to = "8.2"
	}
	job, err = m.StartPHPChange(id, to)
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("php change: %+v", v)
	}
	ents, _ := os.ReadDir(filepath.Join(dir, "ioncube"))
	if len(ents) != 2 || ents[1].Name() != "ioncube_loader_lin_"+to+".so" {
		t.Fatalf("after the PHP change: %v", ents)
	}
	if cf, _ := os.ReadFile(filepath.Join(dir, "compose.yml")); !strings.Contains(string(cf), "ioncube_loader_lin_"+to+".so:") {
		t.Fatal("compose still mounts the old loader")
	}
	s, _ = m.Get(id)
	job, _ = m.StartIonCube(id, false)
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("off: %+v", v)
	}
	if _, err := os.Stat(filepath.Join(dir, "ioncube")); err == nil {
		t.Fatal("loader folder kept after off")
	}
	// PHP does not load it: back without it
	loaded = false
	job, _ = m.StartIonCube(id, true)
	if v := waitJob(t, m, job); v.State != "failed" || !strings.Contains(v.Error, "runs without it again") {
		t.Fatalf("not loaded: %+v", v)
	}
	if s2, _ := m.Get(id); s2.IonCube {
		t.Fatal("site left on")
	}
	if cf, _ := os.ReadFile(filepath.Join(dir, "compose.yml")); strings.Contains(string(cf), "ioncube") {
		t.Fatal("compose left with the loader")
	}
}

func TestIonCubeNoLoader(t *testing.T) {
	m, _, id := readySite(t)
	archive := ionTar(t, "7.4")
	ionServer(t, m, &archive)
	job, _ := m.StartIonCube(id, true)
	if v := waitJob(t, m, job); v.State != "failed" || !strings.Contains(v.Error, "nothing was changed") || !strings.Contains(v.Error, "no loader for PHP") {
		t.Fatalf("%+v", v)
	}
	archive = []byte("not gzip")
	m.ionMu.Lock()
	_, err := m.fetchIonCube(context.Background())
	m.ionMu.Unlock()
	if err == nil {
		t.Fatal("a broken archive was accepted")
	}
	if c, ok := m.ionCache(); !ok || c.Versions[0] != "7.4" {
		t.Fatal("a failed download threw the cache away")
	}
	if ionCubeApp(Site{App: spec.AppWordPress, Engine: "native"}) || ionCubeApp(Site{App: spec.AppStatic, Server: "nginx"}) || !ionCubeApp(Site{App: spec.AppLaravel}) {
		t.Fatal("ionCube offered where it cannot run")
	}
}
