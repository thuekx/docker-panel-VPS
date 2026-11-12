// Test app of the site-01 harness: served from a private Git repository over SSH with a deploy key (APP-02b).
package main

import (
	"net/http"
	"os"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("private-repo ok | port " + os.Getenv("PORT") + "\n"))
	})
	_ = http.ListenAndServe("0.0.0.0:"+os.Getenv("PORT"), nil)
}
