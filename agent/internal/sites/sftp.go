package sites

// SFTP accounts (SFTP-01), like cPanel's FTP Accounts: logins of the core stack's SFTP service, each locked to the
// website's folder or a folder inside it, with a password (bcrypt, hashed by the panel) and/or public keys. No
// shell. The accounts of a website live only in <data>/sftp/<id>.json (0600, root), which the service reads; the
// hash never reaches site.json, a log or an answer. Deleting a website takes the file into its trash entry, so a
// restore brings the accounts back.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	sftpContainer   = "nostek-core-sftp-1"
	SFTPPort        = 2222
	sftpMaxUsers    = 10 // accounts per website
	sftpMaxKeys     = 10 // keys per account
	sftpTrashedFile = "sftp.json"
)

var sftpNameRe = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,31}$`)

// SFTPUser is one account as stored for the service.
type SFTPUser struct {
	Name string   `json:"name"`
	Hash string   `json:"hash,omitempty"`
	Keys []string `json:"keys,omitempty"`
	Dir  string   `json:"dir"`
}

// sftpFile is <data>/sftp/<id>.json; Root is the website folder as the service mounts it.
type sftpFile struct {
	Site  string     `json:"site"`
	Root  string     `json:"root"`
	UID   int        `json:"uid"`
	GID   int        `json:"gid"`
	Users []SFTPUser `json:"users"`
}

// SFTPKey is a public key as the panel shows it.
type SFTPKey struct {
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"` // SHA256:…, like ssh-keygen -l
	Comment     string `json:"comment"`
}

// SFTPAccount is an account as the panel shows it (no hash).
type SFTPAccount struct {
	Name     string    `json:"name"`
	Dir      string    `json:"dir"`
	Password bool      `json:"password"`
	Keys     []SFTPKey `json:"keys"`
}

// SFTPView is the SFTP page of a website.
type SFTPView struct {
	Ready    bool          `json:"ready"` // the SFTP service runs
	Port     int           `json:"port"`
	Accounts []SFTPAccount `json:"accounts"`
}

// SFTPChange is one change of a website's accounts.
type SFTPChange struct {
	Op          string `json:"op"` // user-add | user-remove | password-set | password-remove | key-add | key-remove | dir-set
	Name        string `json:"name"`
	Hash        string `json:"hash,omitempty"`
	Key         string `json:"key,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Dir         string `json:"dir,omitempty"`
}

func (m *Manager) sftpDir() string             { return filepath.Join(m.cfg.Data, "sftp") }
func (m *Manager) sftpFileOf(id string) string { return filepath.Join(m.sftpDir(), id+".json") }

func (m *Manager) readSFTP(id string) (sftpFile, error) {
	var f sftpFile
	b, err := os.ReadFile(m.sftpFileOf(id))
	if errors.Is(err, fs.ErrNotExist) {
		return sftpFile{Site: id}, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("sftp accounts of %s: %w", id, err)
	}
	return f, nil
}

// writeSFTP writes a website's accounts with its current folder and file user; no accounts = no file.
func (m *Manager) writeSFTP(id string, users []SFTPUser) error {
	if len(users) == 0 {
		if err := os.Remove(m.sftpFileOf(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	uid, gid := m.owner(id)
	f := sftpFile{Site: id, Root: "/sites/" + id + "/wp", UID: uid, GID: gid, Users: users}
	if err := os.MkdirAll(m.sftpDir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	return writeFile(m.sftpFileOf(id), append(b, '\n'), 0o600)
}

// sftpReady is true when the core stack's SFTP container runs and answers its health check.
func (m *Manager) sftpReady(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := m.run.Docker(ctx, m.cfg.Data, nil, "inspect", "--format", "{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}", sftpContainer)
	return err == nil && strings.TrimSpace(string(out)) == "running healthy"
}

// SFTP returns a website's accounts.
func (m *Manager) SFTP(id string) (SFTPView, error) {
	if _, err := m.Get(id); err != nil {
		return SFTPView{}, err
	}
	f, err := m.readSFTP(id)
	if err != nil {
		return SFTPView{}, err
	}
	v := SFTPView{Ready: m.sftpReady(context.Background()), Port: SFTPPort, Accounts: []SFTPAccount{}}
	for _, u := range f.Users {
		a := SFTPAccount{Name: u.Name, Dir: u.Dir, Password: u.Hash != "", Keys: []SFTPKey{}}
		for _, k := range u.Keys {
			if pk, err := parseSSHKey(k); err == nil {
				a.Keys = append(a.Keys, pk)
			}
		}
		v.Accounts = append(v.Accounts, a)
	}
	return v, nil
}

// sftpNameTaken is true when another website has an account of that name.
func (m *Manager) sftpNameTaken(id, name string) bool {
	ents, _ := os.ReadDir(m.sftpDir())
	for _, e := range ents {
		other, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || other == id {
			continue
		}
		f, err := m.readSFTP(other)
		if err != nil {
			continue
		}
		for _, u := range f.Users {
			if u.Name == name {
				return true
			}
		}
	}
	return false
}

// checkSFTPDir checks an account's folder: an existing folder of the website ("/" = all of it).
func (m *Manager) checkSFTPDir(id, d string) error {
	if d == "" {
		d = "/"
	}
	if !strings.HasPrefix(d, "/") || len(d) > 512 || strings.ContainsAny(d, "\x00\\\n\r") || path.Clean(d) != d {
		return fmt.Errorf("%w: the folder must be a path like /wp-content/uploads (no .., no trailing /)", ErrBadInput)
	}
	if d == "/" {
		return nil
	}
	r, err := m.openWP(id)
	if err != nil {
		return err
	}
	defer r.Close()
	fi, err := r.Stat(strings.TrimPrefix(d, "/"))
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("%w: folder %s does not exist in the website", ErrBadInput, d)
	}
	return nil
}

// ChangeSFTP applies one change to a website's accounts at once (no job: only the service's file changes).
func (m *Manager) ChangeSFTP(id string, c SFTPChange) error {
	if _, err := m.Get(id); err != nil {
		return err
	}
	if !m.mu.TryLock() {
		return ErrBusy
	}
	defer m.mu.Unlock()
	f, err := m.readSFTP(id)
	if err != nil {
		return err
	}
	users := slices.Clone(f.Users)
	i := slices.IndexFunc(users, func(u SFTPUser) bool { return u.Name == c.Name })
	if c.Op != "user-add" && i < 0 {
		return fmt.Errorf("%w: no account %q on this website", ErrBadInput, c.Name)
	}
	if c.Hash != "" && !bcryptRe.MatchString(c.Hash) {
		return fmt.Errorf("%w: the password must arrive as a bcrypt hash", ErrBadInput)
	}
	key := ""
	if c.Key != "" {
		k := strings.TrimSpace(c.Key)
		if strings.ContainsAny(k, "\n\r") {
			return fmt.Errorf("%w: one key at a time (one line)", ErrBadInput)
		}
		if _, err := parseSSHKey(k); err != nil {
			return fmt.Errorf("%w: %v", ErrBadInput, err)
		}
		key = k
	}
	switch c.Op {
	case "user-add":
		if !sftpNameRe.MatchString(c.Name) {
			return fmt.Errorf("%w: the name is 3-32 small letters, digits, '.', '_' or '-', starting with a letter", ErrBadInput)
		}
		if i >= 0 || m.sftpNameTaken(id, c.Name) {
			return fmt.Errorf("%w: the name %s is taken on this server; SFTP names are unique per server", ErrBadInput, c.Name)
		}
		if len(users) >= sftpMaxUsers {
			return fmt.Errorf("%w: at most %d accounts per website", ErrBadInput, sftpMaxUsers)
		}
		if c.Hash == "" && key == "" {
			return fmt.Errorf("%w: give a password or a public key", ErrBadInput)
		}
		if c.Dir == "" {
			c.Dir = "/"
		}
		if err := m.checkSFTPDir(id, c.Dir); err != nil {
			return err
		}
		if !m.sftpReady(context.Background()) {
			return fmt.Errorf("%w: the SFTP service is not running yet; update the panel first (PANEL-01 UPDATE)", ErrBadInput)
		}
		u := SFTPUser{Name: c.Name, Hash: c.Hash, Dir: c.Dir}
		if key != "" {
			u.Keys = []string{key}
		}
		users = append(users, u)
	case "user-remove":
		users = slices.Delete(users, i, i+1)
	case "password-set":
		if c.Hash == "" {
			return fmt.Errorf("%w: give the new password", ErrBadInput)
		}
		users[i].Hash = c.Hash
	case "password-remove":
		if len(users[i].Keys) == 0 {
			return fmt.Errorf("%w: add a key first; an account needs a password or a key", ErrBadInput)
		}
		if users[i].Hash == "" {
			return ErrNoChange
		}
		users[i].Hash = ""
	case "key-add":
		if key == "" {
			return fmt.Errorf("%w: give a public key", ErrBadInput)
		}
		nk, _ := parseSSHKey(key)
		for _, k := range users[i].Keys {
			if ok, _ := parseSSHKey(k); ok.Fingerprint == nk.Fingerprint {
				return ErrNoChange
			}
		}
		if len(users[i].Keys) >= sftpMaxKeys {
			return fmt.Errorf("%w: at most %d keys per account", ErrBadInput, sftpMaxKeys)
		}
		users[i].Keys = append(slices.Clone(users[i].Keys), key)
	case "key-remove":
		keys := slices.Clone(users[i].Keys)
		j := slices.IndexFunc(keys, func(k string) bool { pk, _ := parseSSHKey(k); return pk.Fingerprint == c.Fingerprint })
		if j < 0 {
			return fmt.Errorf("%w: no such key on %s", ErrBadInput, c.Name)
		}
		if len(keys) == 1 && users[i].Hash == "" {
			return fmt.Errorf("%w: set a password first; an account needs a password or a key", ErrBadInput)
		}
		users[i].Keys = slices.Delete(keys, j, j+1)
		if len(users[i].Keys) == 0 {
			users[i].Keys = nil
		}
	case "dir-set":
		if c.Dir == "" {
			c.Dir = "/"
		}
		if c.Dir == users[i].Dir {
			return ErrNoChange
		}
		if err := m.checkSFTPDir(id, c.Dir); err != nil {
			return err
		}
		users[i].Dir = c.Dir
	default:
		return fmt.Errorf("%w: unknown change %q", ErrBadInput, c.Op)
	}
	return m.writeSFTP(id, users)
}

// SyncSFTP writes every website's accounts again with its current file user (a website moved between engines)
// and removes the files of websites that are gone (agent start).
func (m *Manager) SyncSFTP() error {
	ents, err := os.ReadDir(m.sftpDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []string
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		if _, err := m.Get(id); err != nil {
			_ = os.Remove(filepath.Join(m.sftpDir(), e.Name()))
			continue
		}
		f, err := m.readSFTP(id)
		if err == nil {
			err = m.writeSFTP(id, f.Users)
		}
		if err != nil {
			errs = append(errs, id+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// sftpToTrash moves a website's accounts into its trash entry (Delete).
func (m *Manager) sftpToTrash(id, entryDir string) {
	if _, err := os.Stat(m.sftpFileOf(id)); err == nil {
		_ = os.Rename(m.sftpFileOf(id), filepath.Join(entryDir, sftpTrashedFile))
	}
}

// sftpFromTrash brings the accounts back (Restore); names another website took meanwhile are left out.
func (m *Manager) sftpFromTrash(id string) {
	src := filepath.Join(m.dir(id), sftpTrashedFile)
	b, err := os.ReadFile(src)
	if err != nil {
		return
	}
	_ = os.Remove(src)
	var f sftpFile
	if json.Unmarshal(b, &f) != nil {
		return
	}
	var keep []SFTPUser
	for _, u := range f.Users {
		if !m.sftpNameTaken(id, u.Name) {
			keep = append(keep, u)
		}
	}
	_ = m.writeSFTP(id, keep)
}

// parseSSHKey reads one authorized_keys line ("type base64 [comment]") without an SSH library: the type inside the
// key must match, RSA needs 2048 bits or more, DSA and unknown types are refused.
func parseSSHKey(line string) (SFTPKey, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return SFTPKey{}, errors.New("not a public key (one line like ssh-ed25519 AAAA… name)")
	}
	typ := fields[0]
	switch typ {
	case "ssh-ed25519", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "ssh-rsa",
		"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
	default:
		return SFTPKey{}, fmt.Errorf("key type %s is not accepted (use ed25519, ECDSA or RSA)", typ)
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil || len(blob) > 16<<10 {
		return SFTPKey{}, errors.New("the key is not valid base64")
	}
	r := bytes.NewReader(blob)
	next := func() ([]byte, bool) {
		var n uint32
		if binary.Read(r, binary.BigEndian, &n) != nil || int(n) > r.Len() {
			return nil, false
		}
		b := make([]byte, n)
		_, _ = r.Read(b)
		return b, true
	}
	inner, ok := next()
	if !ok || string(inner) != typ {
		return SFTPKey{}, errors.New("the key does not match its type")
	}
	if typ == "ssh-rsa" {
		e, ok1 := next()
		n, ok2 := next()
		if !ok1 || !ok2 || len(e) == 0 || new(big.Int).SetBytes(n).BitLen() < 2048 {
			return SFTPKey{}, errors.New("RSA keys need at least 2048 bits")
		}
	} else if _, ok := next(); !ok {
		return SFTPKey{}, errors.New("the key is cut short")
	}
	return SFTPKey{Type: typ, Fingerprint: fingerprint(blob), Comment: strings.Join(fields[2:], " ")}, nil
}
