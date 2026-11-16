package api

import (
	"net/http"
	"os"
	"testing"
)

// NATIVE-01: the catalogue says whether the native engine is installed, and a native website is refused without it.
func TestCatalogNative(t *testing.T) {
	c, _ := serve(t, map[int]bool{os.Getuid(): true})
	code, body := do(t, c, http.MethodGet, "/v1/catalog", "")
	if code != 200 || body["native"] != nil {
		t.Errorf("native engine offered without NATIVE-01: %d %v", code, body["native"])
	}
	code, body = do(t, c, http.MethodPost, "/v1/sites/plan", `{"domain":"voltix-id.preview.nostek.dev","aliases":[],"primary":"domain","server":"nginx","php":"8.4","admin_email":"admin@nostek.dev","title":"Voltix","engine":"native"}`)
	if code != 422 {
		t.Errorf("native plan without the engine: %d %v", code, body)
	}
}
