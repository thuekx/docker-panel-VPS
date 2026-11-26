package auth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"
	"testing"
)

// a tiny CBOR writer for the test authenticator
func cbHead(major byte, v uint64) []byte {
	switch {
	case v < 24:
		return []byte{major<<5 | byte(v)}
	case v < 256:
		return []byte{major<<5 | 24, byte(v)}
	case v < 65536:
		return []byte{major<<5 | 25, byte(v >> 8), byte(v)}
	}
	b := []byte{major<<5 | 26, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(b[1:], uint32(v))
	return b
}

func cb(v any) []byte {
	switch x := v.(type) {
	case int:
		if x >= 0 {
			return cbHead(0, uint64(x))
		}
		return cbHead(1, uint64(-1-x))
	case []byte:
		return append(cbHead(2, uint64(len(x))), x...)
	case string:
		return append(cbHead(3, uint64(len(x))), x...)
	case map[int]any:
		keys := []int{}
		for k := range x {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		out := cbHead(5, uint64(len(x)))
		for _, k := range keys {
			out = append(out, cb(k)...)
			out = append(out, cb(x[k])...)
		}
		return out
	case map[string]any:
		out := cbHead(5, uint64(len(x)))
		for _, k := range []string{"fmt", "attStmt", "authData"} {
			if val, ok := x[k]; ok {
				out = append(out, cb(k)...)
				out = append(out, cb(val)...)
			}
		}
		return out
	}
	panic("cb type")
}

type testKey struct {
	ec   *ecdsa.PrivateKey
	ed   ed25519.PrivateKey
	cose []byte
	id   []byte
}

func newTestKey(t *testing.T, kind string) *testKey {
	t.Helper()
	k := &testKey{id: RandomBytes(16)}
	if kind == "ed" {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		k.ed = priv
		k.cose = cb(map[int]any{1: 1, 3: -8, -1: 6, -2: []byte(pub)})
		return k
	}
	k.ec, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	x, y := make([]byte, 32), make([]byte, 32)
	k.ec.X.FillBytes(x)
	k.ec.Y.FillBytes(y)
	k.cose = cb(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	return k
}

var rp = RP{ID: "panel.example.test", Origin: "https://panel.example.test"}

func authDataFor(rpID string, flags byte, count uint32, k *testKey) []byte {
	h := sha256.Sum256([]byte(rpID))
	b := append([]byte{}, h[:]...)
	b = append(b, flags, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(b[33:], count)
	if k != nil {
		b = append(b, make([]byte, 16)...) // AAGUID
		b = append(b, byte(len(k.id)>>8), byte(len(k.id)))
		b = append(b, k.id...)
		b = append(b, k.cose...)
	}
	return b
}

func clientJSON(typ string, ch []byte, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": base64.RawURLEncoding.EncodeToString(ch), "origin": origin})
	return b
}

func (k *testKey) sign(ad, cd []byte) []byte {
	h := sha256.Sum256(cd)
	msg := append(append([]byte{}, ad...), h[:]...)
	if k.ed != nil {
		return ed25519.Sign(k.ed, msg)
	}
	d := sha256.Sum256(msg)
	s, _ := ecdsa.SignASN1(rand.Reader, k.ec, d[:])
	return s
}

func TestPasskeyRegisterAndVerify(t *testing.T) {
	for _, kind := range []string{"ec", "ed"} {
		k := newTestKey(t, kind)
		ch := RandomBytes(32)
		att := cb(map[string]any{"fmt": "none", "attStmt": map[int]any{}, "authData": authDataFor(rp.ID, flagUP|flagUV|flagAT, 0, k)})
		c, err := Register(rp, ch, clientJSON("webauthn.create", ch, rp.Origin), att)
		if err != nil {
			t.Fatalf("%s register: %v", kind, err)
		}
		if string(c.ID) != string(k.id) || (kind == "ec" && c.Alg != -7) || (kind == "ed" && c.Alg != -8) {
			t.Fatalf("%s credential %+v", kind, c)
		}
		// refusals at registration
		for name, a := range map[string]struct {
			ch, cd, att []byte
		}{
			"other challenge": {RandomBytes(32), clientJSON("webauthn.create", ch, rp.Origin), att},
			"other origin":    {ch, clientJSON("webauthn.create", ch, "https://evil.test"), att},
			"get not create":  {ch, clientJSON("webauthn.get", ch, rp.Origin), att},
			"other rp": {ch, clientJSON("webauthn.create", ch, rp.Origin),
				cb(map[string]any{"fmt": "none", "attStmt": map[int]any{}, "authData": authDataFor("evil.test", flagUP|flagUV|flagAT, 0, k)})},
			"no verification": {ch, clientJSON("webauthn.create", ch, rp.Origin),
				cb(map[string]any{"fmt": "none", "attStmt": map[int]any{}, "authData": authDataFor(rp.ID, flagUP|flagAT, 0, k)})},
			"no credential": {ch, clientJSON("webauthn.create", ch, rp.Origin),
				cb(map[string]any{"fmt": "none", "attStmt": map[int]any{}, "authData": authDataFor(rp.ID, flagUP|flagUV, 0, nil)})},
			"not cbor": {ch, clientJSON("webauthn.create", ch, rp.Origin), []byte{0xff, 1, 2}},
		} {
			if _, err := Register(rp, a.ch, a.cd, a.att); !errors.Is(err, ErrPasskey) {
				t.Errorf("%s %s: %v", kind, name, err)
			}
		}
		// sign-in
		ch2 := RandomBytes(32)
		cd := clientJSON("webauthn.get", ch2, rp.Origin)
		ad := authDataFor(rp.ID, flagUP|flagUV, 5, nil)
		n, err := Verify(rp, c, ch2, cd, ad, k.sign(ad, cd))
		if err != nil || n != 5 {
			t.Fatalf("%s verify: %d %v", kind, n, err)
		}
		c.SignCount = n
		if _, err := Verify(rp, c, ch2, cd, ad, k.sign(ad, cd)); !errors.Is(err, ErrPasskey) {
			t.Errorf("%s: a counter that did not grow passed", kind)
		}
		ad6 := authDataFor(rp.ID, flagUP|flagUV, 6, nil)
		sig := k.sign(ad6, cd)
		sig[len(sig)-1] ^= 1
		if _, err := Verify(rp, c, ch2, cd, ad6, sig); !errors.Is(err, ErrPasskey) {
			t.Errorf("%s: a changed signature passed", kind)
		}
		other := newTestKey(t, kind)
		if _, err := Verify(rp, c, ch2, cd, ad6, other.sign(ad6, cd)); !errors.Is(err, ErrPasskey) {
			t.Errorf("%s: another key's signature passed", kind)
		}
		adNoUV := authDataFor(rp.ID, flagUP, 7, nil)
		if _, err := Verify(rp, c, ch2, cd, adNoUV, k.sign(adNoUV, cd)); !errors.Is(err, ErrPasskey) {
			t.Errorf("%s: sign-in without verification passed", kind)
		}
		// passkeys that do not count (0 every time) are fine
		c.SignCount = 0
		ad0 := authDataFor(rp.ID, flagUP|flagUV, 0, nil)
		if _, err := Verify(rp, c, ch2, cd, ad0, k.sign(ad0, cd)); err != nil {
			t.Errorf("%s counter 0: %v", kind, err)
		}
	}
}

func FuzzCBOR(f *testing.F) {
	f.Add([]byte{0xa1, 0x01, 0x02})
	f.Add([]byte{0x9f})
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _, _ = cborDecode(b)
		_, _ = parseAuthData(b, false)
		_, _, _ = publicKey(b)
	})
}
