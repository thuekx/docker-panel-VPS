package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, p string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSiteDisk(t *testing.T) {
	m, _, id := readySite(t)
	dir := m.dir(id)
	_ = os.RemoveAll(filepath.Join(dir, "wp"))
	put(t, filepath.Join(dir, "wp", "index.php"), 100)
	put(t, filepath.Join(dir, "wp", "wp-content", "uploads", "2026", "video.mp4"), 50000)
	put(t, filepath.Join(dir, "wp", "wp-content", "uploads", "2026", "a.jpg"), 3000)
	put(t, filepath.Join(dir, "wp", "wp-content", "plugins", "x", "x.php"), 7000)
	put(t, filepath.Join(dir, "db", "ibdata1"), 20000)
	put(t, filepath.Join(dir, "backups", "manual-1.tar.gz"), 9000)
	put(t, filepath.Join(dir, "files-trash", "e1", "old.zip"), 400)
	outside := filepath.Join(t.TempDir(), "secret")
	put(t, outside, 999999)
	if err := os.Symlink(outside, filepath.Join(dir, "wp", "link")); err != nil {
		t.Fatal(err)
	}
	d, err := m.Disk(context.Background(), id, "/")
	if err != nil {
		t.Fatal(err)
	}
	if d.Files != 60100 || d.Database != 20000 || d.Backups != 9000 || d.Trash != 400 || d.Incomplete {
		t.Errorf("totals %+v", d)
	}
	if d.Total < d.Files+d.Database+d.Backups+d.Trash || d.Other != d.Total-d.Files-d.Database-d.Backups-d.Trash {
		t.Errorf("total %d other %d", d.Total, d.Other)
	}
	if len(d.Rows) != 3 || d.Rows[0].Name != "wp-content" || d.Rows[0].Bytes != 60000 || d.Rows[0].Files != 3 || d.Rows[1].Name != "index.php" || d.Rows[2].Type != "link" || d.Rows[2].Bytes != 0 {
		t.Errorf("rows %+v (largest first, a link never followed)", d.Rows)
	}
	if len(d.Big) != 4 || d.Big[0].Path != "/wp-content/uploads/2026/video.mp4" || d.Big[0].Bytes != 50000 {
		t.Errorf("big %+v", d.Big)
	}
	d, err = m.Disk(context.Background(), id, "/wp-content/uploads/../uploads")
	if err != nil || d.Path != "/wp-content/uploads" || len(d.Rows) != 1 || d.Rows[0].Name != "2026" || d.Rows[0].Bytes != 53000 {
		t.Errorf("drill-down %+v %v", d, err)
	}
	for _, bad := range []string{"/index.php", "/nope", "/../../etc"} {
		if _, err := m.Disk(context.Background(), id, bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if _, err := m.Disk(context.Background(), id, "/link"); err == nil {
		t.Error("followed a link out of the website")
	}
	if _, err := m.Disk(context.Background(), "nope", "/"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown site %v", err)
	}
}

const dfJSON = `{"Active":"3","Reclaimable":"1.2GB (40%)","Size":"3GB","TotalCount":"9","Type":"Images"}
{"Active":"3","Reclaimable":"0B (0%)","Size":"12kB","TotalCount":"3","Type":"Containers"}
{"Active":"0","Reclaimable":"0B","Size":"0B","TotalCount":"0","Type":"Local Volumes"}
{"Active":"0","Reclaimable":"500MB","Size":"500MB","TotalCount":"12","Type":"Build Cache"}`

func TestServerDisk(t *testing.T) {
	m, f, id := readySite(t)
	put(t, filepath.Join(m.dir(id), "wp", "big.bin"), 123456)
	f.answer = map[string]string{"system df": dfJSON}
	d, err := m.ServerDiskUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.DiskBytes <= 0 || d.UsedBytes <= 0 || len(d.Sites) != 1 || d.Sites[0].ID != id || d.Sites[0].Bytes < 123456 || d.DockerErr != "" {
		t.Errorf("server %+v", d)
	}
	kinds := map[string]DockerUse{}
	for _, u := range d.Docker {
		kinds[u.Kind] = u
	}
	if k := kinds["images"]; k.Count != 9 || k.Bytes != 3e9 || k.Reclaimable != 1.2e9 {
		t.Errorf("images %+v", k)
	}
	if k := kinds["build-cache"]; k.Count != 12 || k.Reclaimable != 5e8 {
		t.Errorf("build cache %+v", k)
	}
	f.answer, f.fail = nil, "system df"
	if d, err := m.ServerDiskUsage(context.Background()); err != nil || d.DockerErr == "" || len(d.Sites) != 1 {
		t.Errorf("docker down hides the rest: %+v %v", d, err)
	}
}

func TestCleanImagesKeepsWhatWebsitesName(t *testing.T) {
	m, f, id := readySite(t)
	cf := mustRead(t, m.composePath(id))
	var keepRef string
	for _, x := range composeImages(cf) {
		if x.service == "cli" {
			keepRef = x.image
		}
	}
	name, digest, _ := strings.Cut(keepRef, "@")
	repo, tag, _ := strings.Cut(name, ":")
	ls := `{"ID":"sha256:aaa","Repository":"` + repo + `","Tag":"` + tag + `","Digest":"` + digest + `"}
{"ID":"sha256:bbb","Repository":"mariadb","Tag":"11.4","Digest":"sha256:0000"}
{"ID":"sha256:ccc","Repository":"<none>","Tag":"<none>","Digest":"<none>"}
{"ID":"sha256:ddd","Repository":"traefik","Tag":"v3","Digest":"sha256:1111"}
{"ID":"sha256:eee","Repository":"nostek/agent-build","Tag":"v1.19.0","Digest":"<none>"}
{"ID":"sha256:fff","Repository":"nostek/panel","Tag":"v1.18.0","Digest":"<none>"}`
	f.answer = map[string]string{"image ls": ls, "system df": dfJSON}
	f.hook = func(args []string) ([]byte, error, bool) {
		if strings.Join(args, " ") == "image rm sha256:ddd" {
			return []byte("conflict: image is being used by running container"), errors.New("exit 1"), true
		}
		return nil, nil, false
	}
	freed, err := m.CleanDocker(context.Background(), CleanImages)
	if err != nil {
		t.Fatal(err)
	}
	all := calls(f)
	if strings.Contains(all, "image rm sha256:aaa") || !strings.Contains(all, "image rm sha256:bbb") || !strings.Contains(all, "image rm sha256:ccc") || strings.Contains(all, "image rm --force") || strings.Contains(all, "sha256:eee") || strings.Contains(all, "sha256:fff") {
		t.Errorf("removed the wrong images (the panel's own images stay):\n%s", all)
	}
	if !strings.HasPrefix(freed, "2 images") {
		t.Errorf("freed %q (the image in use stays and is not counted)", freed)
	}
	if _, err := m.CleanDocker(context.Background(), "volumes"); !errors.Is(err, ErrBadInput) {
		t.Errorf("volumes clean-up offered: %v", err)
	}
	f.answer = map[string]string{"builder prune": "Deleted build cache objects:\nabc\nTotal:\t1.5GB\n"}
	if freed, err := m.CleanDocker(context.Background(), CleanBuild); err != nil || freed != "1.5GB" || !strings.Contains(calls(f), "builder prune --force --filter until=24h") {
		t.Errorf("build cache %q %v", freed, err)
	}
}

func TestNamed(t *testing.T) {
	keep := map[string]bool{"wordpress:6.9-php8.4-apache@sha256:abc": true, "nostek-local/laravel-x:13-php8.4": true}
	for _, c := range []struct {
		r    imageRow
		want bool
	}{
		{imageRow{Repository: "wordpress", Tag: "6.9-php8.4-apache", Digest: "sha256:abc"}, true},
		{imageRow{Repository: "wordpress", Tag: "<none>", Digest: "sha256:abc"}, true},
		{imageRow{Repository: "wordpress", Tag: "6.9-php8.4-apache", Digest: "sha256:old"}, false},
		{imageRow{Repository: "nostek-local/laravel-x", Tag: "13-php8.4", Digest: "<none>"}, true},
		{imageRow{Repository: "nostek-local/laravel-x", Tag: "12-php8.3", Digest: "<none>"}, false},
	} {
		if got := named(c.r, keep); got != c.want {
			t.Errorf("named(%+v) = %v", c.r, got)
		}
	}
}
