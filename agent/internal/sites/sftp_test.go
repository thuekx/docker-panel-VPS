package sites

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const testHash = "$2a$10$T4KbINOhRSApr6B7bAO5OuLdBPSEdrkeKV7eZfPUnFGyz17J4DR36"

func edKey(t *testing.T, comment string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blob := append(sshString(nil, []byte("ssh-ed25519")), sshString(nil, pub)...)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " " + comment
}

func rsaKey(bits int) string {
	n := new(big.Int).Lsh(big.NewInt(1), uint(bits-1))
	blob := append(sshString(nil, []byte("ssh-rsa")), sshString(nil, []byte{1, 0, 1})...)
	blob = append(blob, sshString(nil, append([]byte{0}, n.Bytes()...))...)
	return "ssh-rsa " + base64.StdEncoding.EncodeToString(blob) + " old"
}

func TestParseSSHKey(t *testing.T) {
	k := edKey(t, "ana@laptop")
	pk, err := parseSSHKey(k)
	if err != nil || pk.Type != "ssh-ed25519" || !strings.HasPrefix(pk.Fingerprint, "SHA256:") || len(pk.Fingerprint) != 50 || pk.Comment != "ana@laptop" {
		t.Fatalf("%+v %v", pk, err)
	}
	if _, err := parseSSHKey(rsaKey(3072)); err != nil {
		t.Errorf("rsa 3072: %v", err)
	}
	for _, bad := range []string{"", "ssh-ed25519", "ssh-dss AAAAB3NzaC1kc3M= x", rsaKey(1024), "ssh-ed25519 !!!", "ssh-rsa " + strings.Fields(k)[1]} {
		if _, err := parseSSHKey(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func sftpDo(t *testing.T, m *Manager, id string, c SFTPChange) {
	t.Helper()
	if err := m.ChangeSFTP(id, c); err != nil {
		t.Fatalf("%+v: %v", c, err)
	}
}

func TestSFTPAccounts(t *testing.T) {
	m, f, id := readySite(t)
	add := SFTPChange{Op: "user-add", Name: "ana", Hash: testHash, Dir: "/"}
	if err := m.ChangeSFTP(id, add); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "PANEL-01 UPDATE") {
		t.Fatalf("without the service: %v", err)
	}
	f.answer = map[string]string{sftpContainer: "running healthy\n"}
	_ = os.MkdirAll(filepath.Join(m.wpDir(id), "wp-content", "uploads"), 0o755)
	_ = os.WriteFile(filepath.Join(m.wpDir(id), "file.txt"), []byte("x"), 0o644)
	key := edKey(t, "budi")
	for _, bad := range []SFTPChange{
		{Op: "user-add", Name: "Ana", Hash: testHash},
		{Op: "user-add", Name: "ab", Hash: testHash},
		{Op: "user-add", Name: "ana"},
		{Op: "user-add", Name: "ana", Hash: "plain"},
		{Op: "user-add", Name: "ana", Hash: testHash, Dir: "/nope"},
		{Op: "user-add", Name: "ana", Hash: testHash, Dir: "/file.txt"},
		{Op: "user-add", Name: "ana", Hash: testHash, Dir: "/a/../b"},
		{Op: "user-add", Name: "ana", Key: key + "\n" + key},
		{Op: "user-add", Name: "ana", Key: rsaKey(1024)},
		{Op: "password-set", Name: "nobody", Hash: testHash},
		{Op: "launch", Name: "ana"},
	} {
		if err := m.ChangeSFTP(id, bad); !errors.Is(err, ErrBadInput) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	sftpDo(t, m, id, add)
	sftpDo(t, m, id, SFTPChange{Op: "user-add", Name: "budi.up", Key: key, Dir: "/wp-content/uploads"})
	fi, err := os.Stat(m.sftpFileOf(id))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", fi, err)
	}
	if d, _ := os.Stat(m.sftpDir()); d.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v", d.Mode())
	}
	var sf sftpFile
	b, _ := os.ReadFile(m.sftpFileOf(id))
	if json.Unmarshal(b, &sf) != nil || sf.Root != "/sites/"+id+"/wp" || sf.UID != func() int { u, _ := m.owner(id); return u }() || len(sf.Users) != 2 || sf.Users[0].Hash != testHash {
		t.Fatalf("file content: %s", b)
	}
	if _, err := os.Stat(filepath.Join(m.dir(id), "site.json")); err == nil {
		if sj, _ := os.ReadFile(filepath.Join(m.dir(id), "site.json")); strings.Contains(string(sj), "$2a$") {
			t.Error("hash in site.json")
		}
	}
	v, err := m.SFTP(id)
	if err != nil || !v.Ready || v.Port != 2222 || len(v.Accounts) != 2 || !v.Accounts[0].Password || v.Accounts[1].Password ||
		len(v.Accounts[1].Keys) != 1 || v.Accounts[1].Keys[0].Comment != "budi" || v.Accounts[1].Dir != "/wp-content/uploads" {
		t.Fatalf("view: %+v %v", v, err)
	}
	if out, _ := json.Marshal(v); strings.Contains(string(out), "$2a$") || strings.Contains(string(out), strings.Fields(key)[1]) {
		t.Error("view carries the hash or the key")
	}
	// the last way to log in cannot go
	fp := v.Accounts[1].Keys[0].Fingerprint
	if err := m.ChangeSFTP(id, SFTPChange{Op: "key-remove", Name: "budi.up", Fingerprint: fp}); !errors.Is(err, ErrBadInput) {
		t.Errorf("last key removed: %v", err)
	}
	if err := m.ChangeSFTP(id, SFTPChange{Op: "password-remove", Name: "ana"}); !errors.Is(err, ErrBadInput) {
		t.Errorf("last password removed: %v", err)
	}
	sftpDo(t, m, id, SFTPChange{Op: "password-set", Name: "budi.up", Hash: testHash})
	sftpDo(t, m, id, SFTPChange{Op: "key-remove", Name: "budi.up", Fingerprint: fp})
	sftpDo(t, m, id, SFTPChange{Op: "key-add", Name: "ana", Key: key})
	if err := m.ChangeSFTP(id, SFTPChange{Op: "key-add", Name: "ana", Key: key}); !errors.Is(err, ErrNoChange) {
		t.Errorf("same key twice: %v", err)
	}
	sftpDo(t, m, id, SFTPChange{Op: "password-remove", Name: "ana"})
	sftpDo(t, m, id, SFTPChange{Op: "dir-set", Name: "ana", Dir: "/wp-content"})
	if err := m.ChangeSFTP(id, SFTPChange{Op: "dir-set", Name: "ana", Dir: "/wp-content"}); !errors.Is(err, ErrNoChange) {
		t.Errorf("same folder: %v", err)
	}
	sf, _ = m.readSFTP(id)
	if sf.Users[0].Hash != "" || len(sf.Users[0].Keys) != 1 || sf.Users[0].Dir != "/wp-content" || sf.Users[1].Keys != nil || sf.Users[1].Hash == "" {
		t.Fatalf("after changes: %+v", sf.Users)
	}
	sftpDo(t, m, id, SFTPChange{Op: "user-remove", Name: "budi.up"})
	sftpDo(t, m, id, SFTPChange{Op: "user-remove", Name: "ana"})
	if _, err := os.Stat(m.sftpFileOf(id)); err == nil {
		t.Error("file left without accounts")
	}
}

func TestSFTPUniqueTrashSync(t *testing.T) {
	m, f, id := readySite(t)
	f.answer = map[string]string{sftpContainer: "running healthy\n"}
	sftpDo(t, m, id, SFTPChange{Op: "user-add", Name: "ana", Hash: testHash})
	// another website cannot take the name
	_ = os.WriteFile(filepath.Join(m.sftpDir(), "other-site.json"), []byte(`{"site":"other-site","users":[{"name":"dodi","hash":"`+testHash+`","dir":"/"}]}`), 0o600)
	if err := m.ChangeSFTP(id, SFTPChange{Op: "user-add", Name: "dodi", Hash: testHash}); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "taken") {
		t.Errorf("name of another website: %v", err)
	}
	// start: files of websites that are gone go, the others are written again
	_ = os.WriteFile(m.sftpFileOf(id), []byte(`{"site":"`+id+`","root":"/old","uid":1,"gid":1,"users":[{"name":"ana","hash":"`+testHash+`","dir":"/"}]}`), 0o600)
	if err := m.SyncSFTP(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.sftpDir(), "other-site.json")); err == nil {
		t.Error("stale file kept")
	}
	sf, _ := m.readSFTP(id)
	if sf.Root != "/sites/"+id+"/wp" || sf.UID != func() int { u, _ := m.owner(id); return u }() {
		t.Errorf("not written again: %+v", sf)
	}
	// delete takes the accounts into the trash, restore brings them back
	entry, err := m.Delete(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.sftpFileOf(id)); err == nil {
		t.Error("accounts of a deleted website still active")
	}
	if _, err := m.Restore(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if sf, _ := m.readSFTP(id); strings.Join(sortedSFTP(sf), ",") != "ana" {
		t.Errorf("after restore: %+v", sf)
	}
	if _, err := os.Stat(filepath.Join(m.dir(id), sftpTrashedFile)); err == nil {
		t.Error("trashed copy left in the website folder")
	}
}

// sortedSFTP is used by tests: account names of a website.
func sortedSFTP(f sftpFile) []string {
	var out []string
	for _, u := range f.Users {
		out = append(out, u.Name)
	}
	sort.Strings(out)
	return out
}
