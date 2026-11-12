package sites

// Deploy keys for private Git repositories (APP-02b). The panel asks for a key before the website exists, so the
// owner can add the public half to the repository (GitHub: Settings → Deploy keys, read only) before the first
// build. The private half never leaves the server: it is written 0600 and only mounted, read only, into the
// website's own build container. The repository's host key is learnt on the first fetch and pinned in
// deploy/known_hosts; a changed host key stops later builds.

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

// MaxPendingKeys bounds deploy keys made for websites not created yet; they are removed after PendingKeyTTL.
const (
	MaxPendingKeys = 20
	PendingKeyTTL  = 24 * time.Hour
)

// DeployKey is the public half of a deploy key, as the panel shows it.
type DeployKey struct {
	ID          string   `json:"id,omitempty"`
	Public      string   `json:"public"`
	Fingerprint string   `json:"fingerprint"`
	HostKeys    []string `json:"host_keys,omitempty"` // fingerprints of the pinned repository host keys
}

func (m *Manager) pendingKeyDir() string { return filepath.Join(m.cfg.Data, "deploy-keys") }

// deployKeyPath is the private key of a website; the public half sits next to it with .pub.
func (m *Manager) deployKeyPath(id string) string {
	return filepath.Join(m.deployDir(id), "deploy_key")
}
func (m *Manager) knownHostsPath(id string) string {
	return filepath.Join(m.deployDir(id), "known_hosts")
}

// NewDeployKey makes a key for a website that is about to be created from a private repository.
func (m *Manager) NewDeployKey() (DeployKey, error) {
	dir := m.pendingKeyDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return DeployKey{}, err
	}
	m.purgePendingKeys()
	if ents, _ := os.ReadDir(dir); pendingCount(ents) >= MaxPendingKeys {
		return DeployKey{}, fmt.Errorf("%w: too many unused deploy keys; try again tomorrow", ErrBadInput)
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return DeployKey{}, err
	}
	id := hex.EncodeToString(b)
	priv, pub, fp, err := newSSHKey("nostek-panel-deploy-" + id)
	if err != nil {
		return DeployKey{}, err
	}
	p := filepath.Join(dir, id)
	if err := writeFile(p+".pub", []byte(pub+"\n"), 0o600); err != nil {
		return DeployKey{}, err
	}
	if err := writeFile(p, priv, 0o600); err != nil {
		_ = os.Remove(p + ".pub")
		return DeployKey{}, err
	}
	return DeployKey{ID: id, Public: pub, Fingerprint: fp}, nil
}

func pendingCount(ents []os.DirEntry) int {
	n := 0
	for _, e := range ents {
		if spec.KeyID(e.Name()) == nil {
			n++
		}
	}
	return n
}

// pendingKey checks that a key made by NewDeployKey is still there.
func (m *Manager) pendingKey(keyID string) error {
	if spec.KeyID(keyID) != nil {
		return fmt.Errorf("%w: deploy key id is not valid", ErrBadInput)
	}
	if !fileExists(filepath.Join(m.pendingKeyDir(), keyID)) {
		return fmt.Errorf("%w: the deploy key is gone (unused keys are removed after a day); make a new one", ErrBadInput)
	}
	return nil
}

// adoptDeployKey moves a pending key into the website, owned by the user of the build container.
func (m *Manager) adoptDeployKey(s Site, keyID string) error {
	if err := m.pendingKey(keyID); err != nil {
		return err
	}
	if err := os.MkdirAll(m.deployDir(s.ID), 0o755); err != nil {
		return err
	}
	src := filepath.Join(m.pendingKeyDir(), keyID)
	for _, f := range []struct{ from, to string }{{src, m.deployKeyPath(s.ID)}, {src + ".pub", m.deployKeyPath(s.ID) + ".pub"}} {
		if err := os.Rename(f.from, f.to); err != nil {
			return err
		}
		if err := os.Chown(f.to, m.uid(), m.uid()); err != nil {
			return err
		}
	}
	if err := writeFile(m.knownHostsPath(s.ID), nil, 0o600); err != nil {
		return err
	}
	return os.Chown(m.knownHostsPath(s.ID), m.uid(), m.uid())
}

// SiteDeployKey returns the public deploy key of a website and the host keys pinned so far.
func (m *Manager) SiteDeployKey(id string) (DeployKey, error) {
	s, err := m.Get(id)
	if err != nil {
		return DeployKey{}, err
	}
	if !s.IsRuntime() || !spec.IsSSHRepo(s.Repo) {
		return DeployKey{}, ErrNoDeployKey
	}
	pub, err := os.ReadFile(m.deployKeyPath(id) + ".pub")
	if err != nil {
		return DeployKey{}, ErrNoDeployKey
	}
	k := DeployKey{Public: strings.TrimSpace(string(pub))}
	if f := strings.Fields(k.Public); len(f) >= 2 {
		if raw, err := base64.StdEncoding.DecodeString(f[1]); err == nil {
			k.Fingerprint = fingerprint(raw)
		}
	}
	if kh, err := os.ReadFile(m.knownHostsPath(id)); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(kh))
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) < 3 {
				continue
			}
			if raw, err := base64.StdEncoding.DecodeString(f[2]); err == nil {
				k.HostKeys = append(k.HostKeys, f[1]+" "+fingerprint(raw))
			}
		}
	}
	return k, nil
}

// ErrNoDeployKey: the website does not read a private repository.
var ErrNoDeployKey = errors.New("this website has no deploy key (its repository is public or it is built from its own files)")

// purgePendingKeys removes deploy keys that were never used for a website.
func (m *Manager) purgePendingKeys() []string {
	ents, err := os.ReadDir(m.pendingKeyDir())
	if err != nil {
		return nil
	}
	var gone []string
	for _, e := range ents {
		name := strings.TrimSuffix(e.Name(), ".pub")
		if spec.KeyID(name) != nil {
			continue
		}
		if info, err := e.Info(); err == nil && m.now().Sub(info.ModTime()) > PendingKeyTTL {
			if os.Remove(filepath.Join(m.pendingKeyDir(), e.Name())) == nil && !strings.HasSuffix(e.Name(), ".pub") {
				gone = append(gone, "deploy-key "+name)
			}
		}
	}
	return gone
}

// newSSHKey makes an Ed25519 key: the private key in OpenSSH format, the public key as one authorized_keys line
// and its SHA256 fingerprint as ssh-keygen prints it.
func newSSHKey(comment string) ([]byte, string, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", "", err
	}
	blob := sshString(nil, []byte("ssh-ed25519"))
	blob = sshString(blob, pub)
	check := make([]byte, 4)
	if _, err := rand.Read(check); err != nil {
		return nil, "", "", err
	}
	var sec []byte
	sec = append(sec, check...)
	sec = append(sec, check...)
	sec = sshString(sec, []byte("ssh-ed25519"))
	sec = sshString(sec, pub)
	sec = sshString(sec, priv) // 64 bytes: seed then public key
	sec = sshString(sec, []byte(comment))
	for i := byte(1); len(sec)%8 != 0; i++ {
		sec = append(sec, i)
	}
	var body []byte
	body = append(body, []byte("openssh-key-v1\x00")...)
	body = sshString(body, []byte("none"))
	body = sshString(body, []byte("none"))
	body = sshString(body, nil)
	body = binary.BigEndian.AppendUint32(body, 1)
	body = sshString(body, blob)
	body = sshString(body, sec)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: body})
	return privPEM, "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " " + comment, fingerprint(blob), nil
}

func sshString(b, s []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}

func fingerprint(blob []byte) string {
	h := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(h[:])
}
