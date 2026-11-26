package sites

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestRemoteDBRealGate keeps a gate open for a real database (set NOSTEK_REAL_DB=ip:port and NOSTEK_REAL_ALLOW,
// optionally NOSTEK_REAL_PORT_FILE to learn the port); skipped otherwise.
func TestRemoteDBRealGate(t *testing.T) {
	target := os.Getenv("NOSTEK_REAL_DB")
	if target == "" {
		t.Skip("NOSTEK_REAL_DB not set")
	}
	m, _, id := readySite(t)
	m.UseFirewall(&fakeHost{})
	m.dbTargetFor = func(string) (string, error) { return target, nil }
	j, err := m.SetRemoteDB(context.Background(), id, []string{os.Getenv("NOSTEK_REAL_ALLOW")})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, j)
	v, _ := m.RemoteDB(id)
	_ = os.WriteFile(os.Getenv("NOSTEK_REAL_PORT_FILE"), []byte(itoa(v.Port)), 0o644)
	time.Sleep(25 * time.Second)
	v, _ = m.RemoteDB(id)
	t.Logf("refused=%d plain=%d", v.Refused, v.Plain)
}
