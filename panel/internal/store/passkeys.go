package store

// Passkeys (AUTH-02): WebAuthn credentials of an admin. Only public data is kept: the credential id, its COSE public
// key, the algorithm, the signature counter and the panel name it was made for.

import (
	"database/sql"
	"errors"
	"time"
)

// MaxPasskeys is the most passkeys one admin can have.
const MaxPasskeys = 10

const passkeySchema = `
CREATE TABLE IF NOT EXISTS passkeys(
  id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, name TEXT NOT NULL,
  cred_id BLOB NOT NULL UNIQUE, public_key BLOB NOT NULL, alg INTEGER NOT NULL, sign_count INTEGER NOT NULL DEFAULT 0,
  rp_id TEXT NOT NULL, created_at INTEGER NOT NULL, last_used_at INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS passkeys_user ON passkeys(user_id, id);
`

// Passkey is one stored credential.
type Passkey struct {
	ID, UserID int64
	Name       string
	CredID     []byte
	PublicKey  []byte
	Alg        int64
	SignCount  uint32
	RPID       string
	Created    time.Time
	LastUsed   time.Time
}

// AddPasskey stores a new passkey; it refuses past MaxPasskeys.
func (s *Store) AddPasskey(p Passkey) (int64, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT count(*) FROM passkeys WHERE user_id = ?`, p.UserID).Scan(&n); err != nil {
		return 0, err
	}
	if n >= MaxPasskeys {
		return 0, errors.New("too many passkeys")
	}
	res, err := s.DB.Exec(`INSERT INTO passkeys(user_id, name, cred_id, public_key, alg, sign_count, rp_id, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		p.UserID, p.Name, p.CredID, p.PublicKey, p.Alg, p.SignCount, p.RPID, p.Created.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func scanPasskey(sc interface{ Scan(...any) error }) (*Passkey, error) {
	var p Passkey
	var created, used int64
	var count int64
	if err := sc.Scan(&p.ID, &p.UserID, &p.Name, &p.CredID, &p.PublicKey, &p.Alg, &count, &p.RPID, &created, &used); err != nil {
		return nil, err
	}
	p.SignCount = uint32(count)
	p.Created = time.Unix(created, 0)
	if used > 0 {
		p.LastUsed = time.Unix(used, 0)
	}
	return &p, nil
}

const passkeyCols = `id, user_id, name, cred_id, public_key, alg, sign_count, rp_id, created_at, last_used_at`

// Passkeys lists an admin's passkeys, oldest first.
func (s *Store) Passkeys(userID int64) ([]Passkey, error) {
	rows, err := s.DB.Query(`SELECT `+passkeyCols+` FROM passkeys WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Passkey
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// PasskeyByCred finds a passkey by its credential id (sql.ErrNoRows when none).
func (s *Store) PasskeyByCred(cred []byte) (*Passkey, error) {
	return scanPasskey(s.DB.QueryRow(`SELECT `+passkeyCols+` FROM passkeys WHERE cred_id = ?`, cred))
}

// UsePasskey records a sign-in with its new counter.
func (s *Store) UsePasskey(id int64, count uint32, t time.Time) error {
	_, err := s.DB.Exec(`UPDATE passkeys SET sign_count = ?, last_used_at = ? WHERE id = ?`, count, t.Unix(), id)
	return err
}

// DeletePasskey removes one of an admin's passkeys.
func (s *Store) DeletePasskey(userID, id int64) error {
	res, err := s.DB.Exec(`DELETE FROM passkeys WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
