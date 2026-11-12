package api

import (
	"os"
	"testing"
	"time"
)

func TestArtisanRoute(t *testing.T) {
	c, _ := serve(t, map[int]bool{os.Getuid(): true})
	_, out := do(t, c, "POST", "/v1/sites", good)
	for i := 0; i < 300; i++ {
		if _, st := do(t, c, "GET", "/v1/jobs/"+out["job"].(string), ""); st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// a WordPress website has no artisan; unknown commands never run
	for _, body := range []string{`{"op":"migrate"}`, `{"op":"db:wipe"}`, `{"op":"migrate","x":1}`} {
		if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/laravel/artisan", body); code != 400 {
			t.Errorf("%s answered %d", body, code)
		}
	}
	if code, _ := do(t, c, "POST", "/v1/sites/nope/laravel/artisan", `{"op":"migrate"}`); code != 404 && code != 400 {
		t.Errorf("unknown site answered %d", code)
	}
}
