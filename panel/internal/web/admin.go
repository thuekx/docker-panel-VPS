package web

import (
	"errors"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/auth"
	"github.com/thuekx/docker-panel-VPS/panel/internal/store"
)

// SetupLink creates a one-time link (30 min) to create the first admin. It refuses when an admin exists.
func SetupLink(st *store.Store, base string, now time.Time) (string, error) {
	n, err := st.UserCount()
	if err != nil {
		return "", err
	}
	if n > 0 {
		return "", errors.New("an admin already exists; use admin-rotate")
	}
	tok := auth.NewToken()
	if err := st.CreateToken(auth.TokenHash(tok), "setup", 0, nil, now.Add(30*time.Minute)); err != nil {
		return "", err
	}
	return base + "/setup/" + tok, nil
}

// AdminRotate sets a new random password for the admin, signs out all sessions, and returns a
// one-time link (15 min) that shows the password once. The password is never printed or logged.
func AdminRotate(st *store.Store, email, base string, now time.Time) (string, error) {
	u, err := st.UserByEmail(email)
	if err != nil {
		return "", errors.New("no such admin")
	}
	pw := auth.RandomPassword(20)
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return "", err
	}
	tok := auth.NewToken()
	sealed, err := auth.Seal(tok, []byte(pw))
	if err != nil {
		return "", err
	}
	if err := st.SetPassword(u.ID, hash); err != nil {
		return "", err
	}
	_ = st.DeleteUserSessions(u.ID)
	if err := st.CreateToken(auth.TokenHash(tok), "reveal", u.ID, sealed, now.Add(15*time.Minute)); err != nil {
		return "", err
	}
	_ = st.Audit(u.OrgID, u.ID, u.Email, "admin.password_rotated", "break-glass rotate from host", "host")
	return base + "/reveal/" + tok, nil
}
