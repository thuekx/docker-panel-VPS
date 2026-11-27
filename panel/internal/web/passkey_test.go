package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// a test authenticator: one P-256 passkey, CBOR written by hand
type fakeAuthn struct {
	key   *ecdsa.PrivateKey
	id    []byte
	count uint32
}

func cbor(major byte, n int) []byte {
	if n < 24 {
		return []byte{major<<5 | byte(n)}
	}
	if n < 256 {
		return []byte{major<<5 | 24, byte(n)}
	}
	return []byte{major<<5 | 25, byte(n >> 8), byte(n)}
}

func cborBytes(b []byte) []byte { return append(cbor(2, len(b)), b...) }
func cborText(s string) []byte  { return append(cbor(3, len(s)), s...) }
func cborNeg(n int) []byte      { return cbor(1, -1-n) }
func cborUint(n int) []byte     { return cbor(0, n) }
func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func (a *fakeAuthn) cose() []byte {
	x, y := make([]byte, 32), make([]byte, 32)
	a.key.X.FillBytes(x)
	a.key.Y.FillBytes(y)
	return cat(cbor(5, 5), cborUint(1), cborUint(2), cborUint(3), cborNeg(-7), cborNeg(-1), cborUint(1), cborNeg(-2), cborBytes(x), cborNeg(-3), cborBytes(y))
}

func (a *fakeAuthn) authData(rpID string, withCred bool) []byte {
	h := sha256.Sum256([]byte(rpID))
	flags := byte(0x01 | 0x04)
	if withCred {
		flags |= 0x40
	}
	b := append(append([]byte{}, h[:]...), flags, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(b[33:], a.count)
	if withCred {
		b = append(b, make([]byte, 16)...)
		b = append(b, 0, byte(len(a.id)))
		b = append(b, a.id...)
		b = append(b, a.cose()...)
	}
	return b
}

func clientData(typ, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]string{"type": typ, "challenge": challenge, "origin": origin})
	return b
}

func enc(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func TestPasskeys(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin(e.c, "admin@example.test")
	u, _ := url.Parse(e.ts.URL)
	rpID, origin := u.Hostname(), e.ts.URL
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	a := &fakeAuthn{key: k, id: []byte("credential-0001")}

	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, p := range []string{"/profile", "/profile?saved=passkey", "/profile?saved=passkeyremoved"} {
			if code, body, _ := e.get(e.c, p); code != 200 || missingKey.MatchString(body) || !strings.Contains(body, `id="passkeys"`) {
				t.Errorf("%s (%s): %d %v", p, lang, code, missingKey.FindString(body))
			}
		}
		lc := e.client()
		if code, body, _ := e.get(lc, "/login"); code != 200 || missingKey.MatchString(body) || !strings.Contains(body, "data-passkey-login") {
			t.Errorf("login (%s): %d", lang, code)
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, prof, _ := e.get(e.c, "/profile")
	jsonPost := func(cl *http.Client, path string, form url.Values) (int, map[string]any) {
		resp, err := cl.PostForm(e.ts.URL+path, form)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out map[string]any
		json.Unmarshal(b, &out)
		return resp.StatusCode, out
	}
	csrf := csrfRe.FindStringSubmatch(prof)[1]

	// add: the password is needed
	if code, out := jsonPost(e.c, "/profile/passkeys/begin", url.Values{"csrf": {csrf}, "password": {"wrong"}}); code != 401 || out["error"] != "Wrong password." {
		t.Fatalf("begin wrong password %d %v", code, out)
	}
	code, opts := jsonPost(e.c, "/profile/passkeys/begin", url.Values{"csrf": {csrf}, "password": {pw}})
	if code != 200 || opts["rp"].(map[string]any)["id"] != rpID || opts["authenticatorSelection"].(map[string]any)["userVerification"] != "required" {
		t.Fatalf("begin %d %v", code, opts)
	}
	ch := opts["challenge"].(string)
	att := cat(cbor(5, 3), cborText("fmt"), cborText("none"), cborText("attStmt"), cbor(5, 0), cborText("authData"), cborBytes(a.authData(rpID, true)))
	// a wrong origin is refused, and the challenge is gone after one try
	if code, _ := jsonPost(e.c, "/profile/passkeys/finish", url.Values{"csrf": {csrf}, "name": {"Laptop"}, "client": {enc(clientData("webauthn.create", ch, "https://evil.test"))}, "att": {enc(att)}}); code != 400 {
		t.Fatalf("evil origin %d", code)
	}
	_, opts = jsonPost(e.c, "/profile/passkeys/begin", url.Values{"csrf": {csrf}, "password": {pw}})
	ch = opts["challenge"].(string)
	if code, out := jsonPost(e.c, "/profile/passkeys/finish", url.Values{"csrf": {csrf}, "name": {"Office <laptop>"}, "client": {enc(clientData("webauthn.create", ch, origin))}, "att": {enc(att)}}); code != 200 || out["go"] != "/profile?saved=passkey#passkeys" {
		t.Fatalf("finish %d %v", code, out)
	}
	_, prof, _ = e.get(e.c, "/profile")
	if !strings.Contains(prof, "Office &lt;laptop&gt;") {
		t.Fatal("passkey not listed")
	}

	// sign in with it from a fresh browser
	lc := e.client()
	_, lp, _ := e.get(lc, "/login")
	lcsrf := csrfRe.FindStringSubmatch(lp)[1]
	signIn := func(cl *http.Client, cred []byte) (int, map[string]any) {
		code, o := jsonPost(cl, "/login/passkey/begin", url.Values{"csrf": {lcsrf}})
		if code != 200 {
			t.Fatalf("login begin %d %v", code, o)
		}
		cd := clientData("webauthn.get", o["challenge"].(string), origin)
		a.count++
		ad := a.authData(rpID, false)
		h := sha256.Sum256(cd)
		d := sha256.Sum256(append(append([]byte{}, ad...), h[:]...))
		sig, _ := ecdsa.SignASN1(rand.Reader, a.key, d[:])
		return jsonPost(cl, "/login/passkey/finish", url.Values{"csrf": {lcsrf}, "id": {enc(cred)}, "client": {enc(cd)}, "auth": {enc(ad)}, "sig": {enc(sig)}})
	}
	if code, out := signIn(lc, []byte("unknown-cred")); code != 401 || out["error"] != "This passkey is not known to this panel." {
		t.Fatalf("unknown %d %v", code, out)
	}
	if code, out := signIn(lc, a.id); code != 200 || out["go"] != "/" {
		t.Fatalf("sign in %d %v", code, out)
	}
	if code, body, _ := e.get(lc, "/"); code != 200 || strings.Contains(body, `action="/login"`) {
		t.Fatalf("not signed in after the passkey: %d", code)
	}
	// without the challenge cookie (or a second use) nothing happens
	if code, _ := jsonPost(e.client(), "/login/passkey/finish", url.Values{"csrf": {lcsrf}, "id": {enc(a.id)}}); code != 403 && code != 400 {
		t.Fatalf("no ceremony %d", code)
	}
	// a counter that goes back is refused (a copied key)
	a.count = 0
	lc2 := e.client()
	_, lp, _ = e.get(lc2, "/login")
	lcsrf = csrfRe.FindStringSubmatch(lp)[1]
	if code, _ := signIn(lc2, a.id); code != 401 {
		t.Fatalf("counter back %d", code)
	}

	// remove with the password
	_, prof, _ = e.get(e.c, "/profile")
	id := strings.Split(strings.Split(prof, `name="id" value="`)[1], `"`)[0]
	if code, _, _ := e.postWith(e.c, "/profile/passkeys/delete", url.Values{"id": {id}, "password": {"wrong"}}, prof); code != 401 {
		t.Fatalf("remove wrong password %d", code)
	}
	if code, _, h := e.postWith(e.c, "/profile/passkeys/delete", url.Values{"id": {id}, "password": {pw}}, prof); code != 303 || h.Get("Location") != "/profile?saved=passkeyremoved#passkeys" {
		t.Fatalf("remove %d %s", code, h.Get("Location"))
	}
	var audit string
	rows, _ := e.st.DB.Query(`SELECT action || ' ' || detail FROM audit`)
	for rows.Next() {
		var s string
		rows.Scan(&s)
		audit += s + "\n"
	}
	rows.Close()
	for _, want := range []string{"passkey.add Office <laptop> (alg -7)", "login.ok passkey Office <laptop>", "login.passkey_failed", "passkey.remove Office <laptop>", "passkey.add refused: wrong password"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit lacks %q\n%s", want, audit)
		}
	}
	if strings.Contains(audit, pw) {
		t.Fatal("password in the audit log")
	}
}
