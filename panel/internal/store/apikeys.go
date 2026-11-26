package store

// API keys (API-01). A key is shown once when it is made; only its sha256 is kept. Each key has a name, rights
// (read always, plus operate and/or deploy), optionally one website and a list of addresses it may be used from,
// and an expiry.

import (
	"strings"
	"time"
)

// MaxAPIKeys is the most keys an organisation can have that are not revoked.
const MaxAPIKeys = 20

const apiKeySchema = `
CREATE TABLE IF NOT EXISTS api_keys(
  id INTEGER PRIMARY KEY, org_id INTEGER NOT NULL, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL, prefix TEXT NOT NULL, hash TEXT NOT NULL UNIQUE, rights TEXT NOT NULL, site TEXT NOT NULL DEFAULT '',
  allow_ips TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL DEFAULT 0,
  last_used_at INTEGER NOT NULL DEFAULT 0, last_ip TEXT NOT NULL DEFAULT '', revoked_at INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS api_keys_org ON api_keys(org_id, id);
`

// APIKey is one key without its secret.
type APIKey struct {
	ID, OrgID, UserID int64
	Name, Prefix      string
	Rights            []string // read, operate, deploy
	Site              string   // "" = every website
	AllowIPs          []string // "" = from anywhere
	Created, Expires  time.Time
	LastUsed          time.Time
	LastIP            string
	Revoked           time.Time
}

// Has says whether the key holds a right.
func (k APIKey) Has(right string) bool {
	for _, r := range k.Rights {
		if r == right {
			return true
		}
	}
	return false
}

// Active is true for a key that is neither revoked nor expired at t.
func (k APIKey) Active(t time.Time) bool {
	return k.Revoked.IsZero() && (k.Expires.IsZero() || t.Before(k.Expires))
}

func unixOr0(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func timeOr0(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0)
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// CreateAPIKey stores a new key by the hash of its secret.
func (s *Store) CreateAPIKey(k *APIKey, hash string) error {
	res, err := s.DB.Exec(`INSERT INTO api_keys(org_id,user_id,name,prefix,hash,rights,site,allow_ips,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		k.OrgID, k.UserID, k.Name, k.Prefix, hash, strings.Join(k.Rights, ","), k.Site, strings.Join(k.AllowIPs, ","), k.Created.Unix(), unixOr0(k.Expires))
	if err != nil {
		return err
	}
	k.ID, err = res.LastInsertId()
	return err
}

const apiKeyCols = `id,org_id,user_id,name,prefix,rights,site,allow_ips,created_at,expires_at,last_used_at,last_ip,revoked_at`

func scanAPIKey(sc interface{ Scan(...any) error }) (APIKey, error) {
	var k APIKey
	var rights, allow string
	var c, e, l, rv int64
	err := sc.Scan(&k.ID, &k.OrgID, &k.UserID, &k.Name, &k.Prefix, &rights, &k.Site, &allow, &c, &e, &l, &k.LastIP, &rv)
	k.Rights, k.AllowIPs = splitList(rights), splitList(allow)
	k.Created, k.Expires, k.LastUsed, k.Revoked = time.Unix(c, 0), timeOr0(e), timeOr0(l), timeOr0(rv)
	return k, err
}

// APIKeys lists an organisation's keys, newest first (revoked ones too, for the record).
func (s *Store) APIKeys(orgID int64) ([]APIKey, error) {
	rows, err := s.DB.Query(`SELECT `+apiKeyCols+` FROM api_keys WHERE org_id=? ORDER BY id DESC LIMIT 100`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ActiveAPIKeys counts an organisation's keys that are not revoked or expired.
func (s *Store) ActiveAPIKeys(orgID int64, now time.Time) (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT count(*) FROM api_keys WHERE org_id=? AND revoked_at=0 AND (expires_at=0 OR expires_at>?)`, orgID, now.Unix()).Scan(&n)
	return n, err
}

// APIKeyByHash finds a key by the hash of its secret (revoked and expired keys too; the caller checks Active).
func (s *Store) APIKeyByHash(hash string) (APIKey, error) {
	k, err := scanAPIKey(s.DB.QueryRow(`SELECT `+apiKeyCols+` FROM api_keys WHERE hash=?`, hash))
	if err != nil {
		return k, ErrNotFound
	}
	return k, nil
}

// TouchAPIKey records when and from where a key was last used.
func (s *Store) TouchAPIKey(id int64, at time.Time, ip string) error {
	_, err := s.DB.Exec(`UPDATE api_keys SET last_used_at=?, last_ip=? WHERE id=?`, at.Unix(), ip, id)
	return err
}

// RevokeAPIKey stops a key of the organisation; it reports the key, or ErrNotFound.
func (s *Store) RevokeAPIKey(orgID, id int64, at time.Time) (APIKey, error) {
	k, err := scanAPIKey(s.DB.QueryRow(`SELECT `+apiKeyCols+` FROM api_keys WHERE org_id=? AND id=? AND revoked_at=0`, orgID, id))
	if err != nil {
		return k, ErrNotFound
	}
	if _, err := s.DB.Exec(`UPDATE api_keys SET revoked_at=? WHERE id=?`, at.Unix(), id); err != nil {
		return k, err
	}
	k.Revoked = at
	return k, nil
}
