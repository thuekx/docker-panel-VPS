package store

import (
	"errors"
	"time"
)

// EXT-01: users other than the server's admins, made and locked by an extension. A lock is its own table (not
// locked_until) so a password change or a good sign-in never lifts it.
const userSchema = `
CREATE TABLE IF NOT EXISTS user_locks(user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE, at INTEGER NOT NULL);
`

// ErrAdmin refuses a change an extension may not make to an admin.
var ErrAdmin = errors.New("not for an admin")

// CreateUser adds a user to an organisation with a role other than admin.
func (s *Store) CreateUser(orgID int64, email, pwHash, role string) (*User, error) {
	if role == "" || role == "admin" {
		return nil, ErrAdmin
	}
	res, err := s.DB.Exec(`INSERT INTO users(org_id,email,pw_hash,role,created_at) VALUES(?,?,?,?,?)`, orgID, email, pwHash, role, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.UserByID(id)
}

// notAdmin returns the user when it exists and is not an admin.
func (s *Store) notAdmin(id int64) (*User, error) {
	u, err := s.UserByID(id)
	if err != nil {
		return nil, err
	}
	if u.Role == "admin" {
		return nil, ErrAdmin
	}
	return u, nil
}

// SetUserLocked locks (and signs out) or unlocks a user that is not an admin.
func (s *Store) SetUserLocked(id int64, locked bool, now time.Time) error {
	if _, err := s.notAdmin(id); err != nil {
		return err
	}
	if !locked {
		_, err := s.DB.Exec(`DELETE FROM user_locks WHERE user_id=?`, id)
		return err
	}
	if _, err := s.DB.Exec(`INSERT OR IGNORE INTO user_locks(user_id,at) VALUES(?,?)`, id, now.Unix()); err != nil {
		return err
	}
	return s.DeleteUserSessions(id)
}

// DeleteUser removes a user that is not an admin, with its sessions, tokens and codes.
func (s *Store) DeleteUser(id int64) error {
	if _, err := s.notAdmin(id); err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM tokens WHERE user_id=?`, `DELETE FROM sessions WHERE user_id=?`,
		`DELETE FROM recovery_codes WHERE user_id=?`, `DELETE FROM user_locks WHERE user_id=?`, `DELETE FROM users WHERE id=?`} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
