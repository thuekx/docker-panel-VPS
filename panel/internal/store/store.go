// Package store keeps panel data in SQLite. Every org-scoped read takes an org id.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct{ DB *sql.DB }

type User struct {
	ID, OrgID           int64
	Email, PwHash, Role string
	TOTPSecret          string
	TOTPEnabled         bool
	TOTPLastStep        int64
	FailedCount         int
	LockedUntil         time.Time
}

type Session struct {
	ID        string // hash of the cookie value
	UserID    int64
	CSRF      string
	MFAOK     bool
	CreatedAt time.Time
	LastSeen  time.Time
}

type AuditEntry struct {
	ID     int64
	OrgID  int64
	Email  string
	Action string
	Detail string
	IP     string
	At     time.Time
}

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS orgs(id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS users(
  id INTEGER PRIMARY KEY, org_id INTEGER NOT NULL REFERENCES orgs(id), email TEXT NOT NULL UNIQUE,
  pw_hash TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'admin', totp_secret TEXT NOT NULL DEFAULT '',
  totp_enabled INTEGER NOT NULL DEFAULT 0, totp_last_step INTEGER NOT NULL DEFAULT 0,
  failed_count INTEGER NOT NULL DEFAULT 0, locked_until INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS sessions(
  id TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, csrf TEXT NOT NULL,
  mfa_ok INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, last_seen INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS tokens(
  hash TEXT PRIMARY KEY, kind TEXT NOT NULL, user_id INTEGER, sealed BLOB, expires_at INTEGER NOT NULL, used INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS recovery_codes(
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, code_hash TEXT NOT NULL, used INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS audit(
  id INTEGER PRIMARY KEY, org_id INTEGER NOT NULL, user_id INTEGER, email TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', ip TEXT NOT NULL DEFAULT '', at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{DB: db}, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) UserCount() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// CreateFirstAdmin creates the first organisation and its admin in one transaction.
func (s *Store) CreateFirstAdmin(orgName, email, pwHash string) (*User, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, errors.New("an admin already exists")
	}
	res, err := tx.Exec(`INSERT INTO orgs(name) VALUES(?)`, orgName)
	if err != nil {
		return nil, err
	}
	orgID, _ := res.LastInsertId()
	res, err = tx.Exec(`INSERT INTO users(org_id,email,pw_hash,role,created_at) VALUES(?,?,?,?,?)`, orgID, email, pwHash, "admin", time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.UserByID(id)
}

// CreateOrgWithAdmin is used for additional tenants (and by isolation tests).
func (s *Store) CreateOrgWithAdmin(orgName, email, pwHash string) (*User, error) {
	res, err := s.DB.Exec(`INSERT INTO orgs(name) VALUES(?)`, orgName)
	if err != nil {
		return nil, err
	}
	orgID, _ := res.LastInsertId()
	res, err = s.DB.Exec(`INSERT INTO users(org_id,email,pw_hash,role,created_at) VALUES(?,?,?,?,?)`, orgID, email, pwHash, "admin", time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.UserByID(id)
}

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var en int
	var locked int64
	err := row.Scan(&u.ID, &u.OrgID, &u.Email, &u.PwHash, &u.Role, &u.TOTPSecret, &en, &u.TOTPLastStep, &u.FailedCount, &locked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.TOTPEnabled = en == 1
	u.LockedUntil = time.Unix(locked, 0)
	return &u, nil
}

const userCols = `id,org_id,email,pw_hash,role,totp_secret,totp_enabled,totp_last_step,failed_count,locked_until`

func (s *Store) UserByID(id int64) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id))
}

func (s *Store) UserByEmail(email string) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE email=?`, email))
}

func (s *Store) SetPassword(userID int64, hash string) error {
	_, err := s.DB.Exec(`UPDATE users SET pw_hash=?, failed_count=0, locked_until=0 WHERE id=?`, hash, userID)
	return err
}

func (s *Store) SetTOTPSecret(userID int64, secret string) error {
	_, err := s.DB.Exec(`UPDATE users SET totp_secret=?, totp_enabled=0 WHERE id=?`, secret, userID)
	return err
}

func (s *Store) EnableTOTP(userID int64, step int64) error {
	_, err := s.DB.Exec(`UPDATE users SET totp_enabled=1, totp_last_step=? WHERE id=?`, step, userID)
	return err
}

// UseTOTPStep stores the step so the same code cannot be used twice.
func (s *Store) UseTOTPStep(userID, step int64) (bool, error) {
	res, err := s.DB.Exec(`UPDATE users SET totp_last_step=? WHERE id=? AND totp_last_step < ?`, step, userID, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) RecordLoginFailure(userID int64, maxFails int, until time.Time) error {
	_, err := s.DB.Exec(`UPDATE users SET failed_count=failed_count+1,
	  locked_until=CASE WHEN failed_count+1>=? THEN ? ELSE locked_until END WHERE id=?`, maxFails, until.Unix(), userID)
	return err
}

func (s *Store) ResetLoginFailures(userID int64) error {
	_, err := s.DB.Exec(`UPDATE users SET failed_count=0, locked_until=0 WHERE id=?`, userID)
	return err
}

// ---- sessions

func (s *Store) CreateSession(sess *Session) error {
	_, err := s.DB.Exec(`INSERT INTO sessions(id,user_id,csrf,mfa_ok,created_at,last_seen) VALUES(?,?,?,?,?,?)`,
		sess.ID, sess.UserID, sess.CSRF, b2i(sess.MFAOK), sess.CreatedAt.Unix(), sess.LastSeen.Unix())
	return err
}

func (s *Store) SessionByID(id string) (*Session, error) {
	var sess Session
	var mfa int
	var c, l int64
	err := s.DB.QueryRow(`SELECT id,user_id,csrf,mfa_ok,created_at,last_seen FROM sessions WHERE id=?`, id).Scan(&sess.ID, &sess.UserID, &sess.CSRF, &mfa, &c, &l)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sess.MFAOK = mfa == 1
	sess.CreatedAt, sess.LastSeen = time.Unix(c, 0), time.Unix(l, 0)
	return &sess, nil
}

func (s *Store) TouchSession(id string, t time.Time) error {
	_, err := s.DB.Exec(`UPDATE sessions SET last_seen=? WHERE id=?`, t.Unix(), id)
	return err
}

func (s *Store) DeleteSession(id string) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE id=?`, id)
	return err
}

func (s *Store) DeleteUserSessions(userID int64) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE user_id=?`, userID)
	return err
}

// ---- one-time tokens

func (s *Store) CreateToken(hash, kind string, userID int64, sealed []byte, exp time.Time) error {
	_, err := s.DB.Exec(`INSERT INTO tokens(hash,kind,user_id,sealed,expires_at) VALUES(?,?,?,?,?)`, hash, kind, userID, sealed, exp.Unix())
	return err
}

// PeekToken returns a valid (unused, unexpired) token without using it.
func (s *Store) PeekToken(hash, kind string, now time.Time) (int64, []byte, error) {
	var uid sql.NullInt64
	var sealed []byte
	err := s.DB.QueryRow(`SELECT user_id,sealed FROM tokens WHERE hash=? AND kind=? AND used=0 AND expires_at>?`, hash, kind, now.Unix()).Scan(&uid, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, ErrNotFound
	}
	return uid.Int64, sealed, err
}

// UseToken marks a token used; it fails if it was already used or expired.
func (s *Store) UseToken(hash, kind string, now time.Time) error {
	res, err := s.DB.Exec(`UPDATE tokens SET used=1, sealed=NULL WHERE hash=? AND kind=? AND used=0 AND expires_at>?`, hash, kind, now.Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}

// ---- recovery codes

func (s *Store) ReplaceRecoveryCodes(userID int64, hashes []string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM recovery_codes WHERE user_id=?`, userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.Exec(`INSERT INTO recovery_codes(user_id,code_hash) VALUES(?,?)`, userID, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UseRecoveryCode(userID int64, hash string) (bool, error) {
	res, err := s.DB.Exec(`UPDATE recovery_codes SET used=1 WHERE user_id=? AND code_hash=? AND used=0`, userID, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) RecoveryCodesLeft(userID int64) (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT count(*) FROM recovery_codes WHERE user_id=? AND used=0`, userID).Scan(&n)
	return n, err
}

// ---- audit (org scoped)

func (s *Store) Audit(orgID, userID int64, email, action, detail, ip string) error {
	_, err := s.DB.Exec(`INSERT INTO audit(org_id,user_id,email,action,detail,ip,at) VALUES(?,?,?,?,?,?,?)`, orgID, userID, email, action, detail, ip, time.Now().Unix())
	return err
}

func (s *Store) AuditList(orgID int64, limit int) ([]AuditEntry, error) {
	rows, err := s.DB.Query(`SELECT id,org_id,email,action,detail,ip,at FROM audit WHERE org_id=? ORDER BY id DESC LIMIT ?`, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var a AuditEntry
		var at int64
		if err := rows.Scan(&a.ID, &a.OrgID, &a.Email, &a.Action, &a.Detail, &a.IP, &at); err != nil {
			return nil, err
		}
		a.At = time.Unix(at, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// AuditEntryFor returns one entry only if it belongs to orgID (other org = not found).
func (s *Store) AuditEntryFor(orgID, id int64) (*AuditEntry, error) {
	var a AuditEntry
	var at int64
	err := s.DB.QueryRow(`SELECT id,org_id,email,action,detail,ip,at FROM audit WHERE id=? AND org_id=?`, id, orgID).Scan(&a.ID, &a.OrgID, &a.Email, &a.Action, &a.Detail, &a.IP, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	a.At = time.Unix(at, 0)
	return &a, err
}

// ---- settings

func (s *Store) Setting(key, def string) string {
	var v string
	if err := s.DB.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v); err != nil {
		return def
	}
	return v
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
