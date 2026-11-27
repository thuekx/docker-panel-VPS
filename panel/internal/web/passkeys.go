package web

// Passkeys (AUTH-02): an admin adds passkeys on the profile page (with the account password) and can then sign in
// with one instead of the password and the 2FA code. A passkey with user verification (PIN, fingerprint, face) is a
// login of its own; password + TOTP stay as they are. Only public keys are stored.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/thuekx/docker-panel-VPS/panel/internal/auth"
	"github.com/thuekx/docker-panel-VPS/panel/internal/store"
)

const (
	cookiePasskey  = "np_pk"
	passkeyTimeout = 2 * time.Minute
)

// challenges keeps each ceremony's challenge for passkeyTimeout; a challenge is used once.
type challenges struct {
	mu sync.Mutex
	m  map[string]challenge
}

type challenge struct {
	b      []byte
	userID int64 // registration: the admin it belongs to
	exp    time.Time
}

func (c *challenges) put(key string, ch challenge) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]challenge{}
	}
	now := time.Now()
	for k, v := range c.m {
		if now.After(v.exp) {
			delete(c.m, k)
		}
	}
	if len(c.m) > 10000 {
		return // a flood: refuse new ceremonies until old ones expire
	}
	c.m[key] = ch
}

func (c *challenges) take(key string) (challenge, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.m[key]
	delete(c.m, key)
	if !ok || time.Now().After(ch.exp) {
		return challenge{}, false
	}
	return ch, true
}

// rpOf is the relying party of a request: the panel's own name, as the browser reached it.
func (s *Server) rpOf(r *http.Request) auth.RP {
	host := r.Host
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	scheme := "https://"
	if !s.cfg.SecureCookies {
		scheme = "http://" // tests and plain local runs
	}
	return auth.RP{ID: strings.ToLower(name), Origin: scheme + host}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(s string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return nil
	}
	return b
}

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// userHandle is the stable id the authenticator keeps for an admin (not secret, not the e-mail).
func userHandle(id int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(id))
	h := sha256.Sum256(append([]byte("nostek-panel-user:"), b[:]...))
	return h[:16]
}

type passkeyRow struct {
	ID            int64
	Name          string
	Created, Used string
}

func (s *Server) passkeyRows(userID int64) []passkeyRow {
	list, _ := s.st.Passkeys(userID)
	var out []passkeyRow
	for _, p := range list {
		row := passkeyRow{ID: p.ID, Name: p.Name, Created: p.Created.In(s.loc()).Format("02 Jan 2006")}
		if !p.LastUsed.IsZero() {
			row.Used = p.LastUsed.In(s.loc()).Format("02 Jan 2006 15:04")
		}
		out = append(out, row)
	}
	return out
}

// passkeyBegin (profile) checks the password and answers the options for navigator.credentials.create().
func (s *Server) passkeyBegin(w http.ResponseWriter, r *http.Request) {
	u, l := userOf(r), s.lang(r)
	if !s.ipLimit.Allow("pw:" + s.clientIP(r)) {
		writeJSONStatus(w, 429, map[string]string{"error": s.tr.T(l, "err.toomany")})
		return
	}
	if !auth.CheckPassword(u.PwHash, r.PostForm.Get("password")) {
		s.audit1(u, r, "passkey.add", "refused: wrong password")
		writeJSONStatus(w, 401, map[string]string{"error": s.tr.T(l, "pk.err.password")})
		return
	}
	have, _ := s.st.Passkeys(u.ID)
	if len(have) >= store.MaxPasskeys {
		writeJSONStatus(w, 400, map[string]string{"error": s.tr.T(l, "pk.err.many", store.MaxPasskeys)})
		return
	}
	rp := s.rpOf(r)
	ch := auth.RandomBytes(32)
	s.pkCh.put("reg:"+sessOf(r).ID, challenge{b: ch, userID: u.ID, exp: time.Now().Add(passkeyTimeout)})
	exclude := []map[string]string{}
	for _, p := range have {
		exclude = append(exclude, map[string]string{"type": "public-key", "id": b64(p.CredID)})
	}
	writeJSONStatus(w, 200, map[string]any{
		"challenge": b64(ch),
		"rp":        map[string]string{"id": rp.ID, "name": "Nostek Panel"},
		"user":      map[string]string{"id": b64(userHandle(u.ID)), "name": u.Email, "displayName": u.Email},
		"pubKeyCredParams": []map[string]any{
			{"type": "public-key", "alg": -8}, {"type": "public-key", "alg": -7}, {"type": "public-key", "alg": -257}},
		"authenticatorSelection": map[string]string{"residentKey": "required", "userVerification": "required"},
		"attestation":            "none",
		"excludeCredentials":     exclude,
		"timeout":                int(passkeyTimeout / time.Millisecond),
	})
}

// passkeyFinish (profile) checks the new credential and stores it.
func (s *Server) passkeyFinish(w http.ResponseWriter, r *http.Request) {
	u, l, f := userOf(r), s.lang(r), r.PostForm
	ch, ok := s.pkCh.take("reg:" + sessOf(r).ID)
	if !ok || ch.userID != u.ID {
		writeJSONStatus(w, 400, map[string]string{"error": s.tr.T(l, "pk.err.expired")})
		return
	}
	name := strings.TrimSpace(f.Get("name"))
	if name == "" {
		name = "Passkey"
	}
	if utf8.RuneCountInString(name) > 60 {
		name = string([]rune(name)[:60])
	}
	rp := s.rpOf(r)
	c, err := auth.Register(rp, ch.b, unb64(f.Get("client")), unb64(f.Get("att")))
	if err != nil {
		s.audit1(u, r, "passkey.add", "refused: "+err.Error())
		writeJSONStatus(w, 400, map[string]string{"error": s.tr.T(l, "pk.err.refused")})
		return
	}
	if _, err := s.st.AddPasskey(store.Passkey{UserID: u.ID, Name: name, CredID: c.ID, PublicKey: c.PublicKey, Alg: c.Alg,
		SignCount: c.SignCount, RPID: rp.ID, Created: s.now()}); err != nil {
		writeJSONStatus(w, 400, map[string]string{"error": s.tr.T(l, "pk.err.refused")})
		return
	}
	s.audit1(u, r, "passkey.add", name+" (alg "+strconv.FormatInt(c.Alg, 10)+")")
	writeJSONStatus(w, 200, map[string]string{"go": "/profile?saved=passkey#passkeys"})
}

// passkeyDelete (profile) removes a passkey; it asks for the password like adding one.
func (s *Server) passkeyDelete(w http.ResponseWriter, r *http.Request) {
	u, f := userOf(r), r.PostForm
	if !s.ipLimit.Allow("pw:" + s.clientIP(r)) {
		s.renderProfile(w, r, http.StatusTooManyRequests, "err.toomany")
		return
	}
	if !auth.CheckPassword(u.PwHash, f.Get("password")) {
		s.audit1(u, r, "passkey.remove", "refused: wrong password")
		s.renderProfile(w, r, http.StatusUnauthorized, "pk.err.password")
		return
	}
	id, _ := strconv.ParseInt(f.Get("id"), 10, 64)
	name := ""
	for _, p := range s.passkeyRows(u.ID) {
		if p.ID == id {
			name = p.Name
		}
	}
	if err := s.st.DeletePasskey(u.ID, id); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	s.audit1(u, r, "passkey.remove", name)
	http.Redirect(w, r, "/profile?saved=passkeyremoved#passkeys", http.StatusSeeOther)
}

// loginPasskeyBegin answers the options for navigator.credentials.get() (any passkey of this panel).
func (s *Server) loginPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	l := s.lang(r)
	if !s.preCSRFOK(r) {
		http.Error(w, "bad request", http.StatusForbidden)
		return
	}
	if !s.ipLimit.Allow("login:" + s.clientIP(r)) {
		writeJSONStatus(w, 429, map[string]string{"error": s.tr.T(l, "err.toomany")})
		return
	}
	tok := auth.NewToken()
	ch := auth.RandomBytes(32)
	s.pkCh.put("login:"+auth.TokenHash(tok), challenge{b: ch, exp: time.Now().Add(passkeyTimeout)})
	http.SetCookie(w, &http.Cookie{Name: cookiePasskey, Value: tok, Path: "/login/passkey", HttpOnly: true, Secure: s.cfg.SecureCookies,
		SameSite: http.SameSiteStrictMode, MaxAge: int(passkeyTimeout / time.Second)})
	writeJSONStatus(w, 200, map[string]any{"challenge": b64(ch), "rpId": s.rpOf(r).ID, "userVerification": "required",
		"timeout": int(passkeyTimeout / time.Millisecond)})
}

// loginPasskeyFinish checks the signature and signs the admin in with a full session.
func (s *Server) loginPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	l := s.lang(r)
	if !s.preCSRFOK(r) {
		http.Error(w, "bad request", http.StatusForbidden)
		return
	}
	f := r.PostForm
	refuse := func(code int, key string) { writeJSONStatus(w, code, map[string]string{"error": s.tr.T(l, key)}) }
	if !s.ipLimit.Allow("login:" + s.clientIP(r)) {
		refuse(429, "err.toomany")
		return
	}
	c, err := r.Cookie(cookiePasskey)
	if err != nil {
		refuse(400, "pk.err.expired")
		return
	}
	ch, ok := s.pkCh.take("login:" + auth.TokenHash(c.Value))
	http.SetCookie(w, &http.Cookie{Name: cookiePasskey, Value: "", Path: "/login/passkey", MaxAge: -1, HttpOnly: true, Secure: s.cfg.SecureCookies})
	if !ok {
		refuse(400, "pk.err.expired")
		return
	}
	cred := unb64(f.Get("id"))
	if len(cred) == 0 {
		refuse(400, "pk.err.refused")
		return
	}
	p, err := s.st.PasskeyByCred(cred)
	if errors.Is(err, sql.ErrNoRows) || p == nil {
		refuse(401, "pk.err.unknown")
		return
	}
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	u, err := s.st.UserByID(p.UserID)
	if err != nil {
		refuse(401, "pk.err.unknown")
		return
	}
	if u.LockedUntil.After(s.now()) {
		s.audit1(u, r, "login.locked", "passkey")
		s.recordLogin(u, r, "locked")
		refuse(401, "err.login")
		return
	}
	if h := unb64(f.Get("user")); len(h) > 0 && string(h) != string(userHandle(u.ID)) {
		refuse(401, "pk.err.refused")
		return
	}
	rp := s.rpOf(r)
	if rp.ID != p.RPID {
		refuse(401, "pk.err.unknown")
		return
	}
	n, err := auth.Verify(rp, auth.Credential{ID: p.CredID, PublicKey: p.PublicKey, Alg: p.Alg, SignCount: p.SignCount},
		ch.b, unb64(f.Get("client")), unb64(f.Get("auth")), unb64(f.Get("sig")))
	if err != nil {
		_ = s.st.RecordLoginFailure(u.ID, maxFails, s.now().Add(lockFor))
		s.audit1(u, r, "login.passkey_failed", p.Name+": "+err.Error())
		s.recordLogin(u, r, "passkey_failed")
		refuse(401, "pk.err.refused")
		return
	}
	_ = s.st.UsePasskey(p.ID, n, s.now())
	_ = s.st.ResetLoginFailures(u.ID)
	if old, _ := s.currentSession(r); old != nil {
		_ = s.st.DeleteSession(old.ID)
	}
	if _, err := s.startSession(w, u, true); err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	s.audit1(u, r, "login.ok", "passkey "+p.Name)
	s.recordLogin(u, r, "passkey")
	writeJSONStatus(w, 200, map[string]string{"go": "/"})
}
