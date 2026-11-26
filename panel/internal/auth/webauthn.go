package auth

// Passkeys (AUTH-02): the server side of WebAuthn Level 2, standard library only. Registration takes attestation
// "none" (the panel trusts the account that is signed in, not the maker of the key); sign-in checks the challenge,
// the origin, the relying party, user presence and verification, the signature and the counter. Algorithms:
// ES256 (-7), EdDSA (-8), RS256 (-257).

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// ErrPasskey is any refused registration or sign-in; the text says why, for the audit log.
var ErrPasskey = errors.New("passkey refused")

func pkErr(f string, a ...any) error { return fmt.Errorf("%w: "+f, append([]any{ErrPasskey}, a...)...) }

// Credential is a registered passkey (public data only).
type Credential struct {
	ID        []byte
	PublicKey []byte // COSE_Key as the authenticator sent it
	Alg       int64
	SignCount uint32
}

// RP is the relying party: the panel's name (RP ID) and the origin the browser must report.
type RP struct {
	ID     string // panel.example.com
	Origin string // https://panel.example.com
}

const (
	flagUP = 0x01
	flagUV = 0x04
	flagAT = 0x40
	flagED = 0x80
)

// ---------- a small CBOR reader (RFC 8949): what WebAuthn uses, nothing more

type cborReader struct {
	b     []byte
	depth int
}

func (r *cborReader) head() (major byte, val uint64, err error) {
	if len(r.b) == 0 {
		return 0, 0, errors.New("cbor: short")
	}
	ib := r.b[0]
	r.b = r.b[1:]
	major, info := ib>>5, ib&0x1f
	switch {
	case info < 24:
		return major, uint64(info), nil
	case info == 24, info == 25, info == 26, info == 27:
		n := 1 << (info - 24)
		if len(r.b) < n {
			return 0, 0, errors.New("cbor: short")
		}
		var v uint64
		for _, c := range r.b[:n] {
			v = v<<8 | uint64(c)
		}
		r.b = r.b[n:]
		return major, v, nil
	}
	return 0, 0, errors.New("cbor: indefinite or reserved length")
}

// value decodes one item: uint64, int64 (negative), []byte, string, []any, map[any]any, bool or nil.
func (r *cborReader) value() (any, error) {
	if r.depth > 16 {
		return nil, errors.New("cbor: too deep")
	}
	major, v, err := r.head()
	if err != nil {
		return nil, err
	}
	switch major {
	case 0:
		if v > 1<<62 {
			return nil, errors.New("cbor: integer too large")
		}
		return int64(v), nil
	case 1:
		if v > 1<<62 {
			return nil, errors.New("cbor: integer too large")
		}
		return -1 - int64(v), nil
	case 2, 3:
		if v > uint64(len(r.b)) {
			return nil, errors.New("cbor: short")
		}
		s := r.b[:v]
		r.b = r.b[v:]
		if major == 2 {
			return append([]byte(nil), s...), nil
		}
		return string(s), nil
	case 4:
		if v > 1024 {
			return nil, errors.New("cbor: array too long")
		}
		r.depth++
		out := make([]any, 0, v)
		for i := uint64(0); i < v; i++ {
			x, err := r.value()
			if err != nil {
				return nil, err
			}
			out = append(out, x)
		}
		r.depth--
		return out, nil
	case 5:
		if v > 256 {
			return nil, errors.New("cbor: map too long")
		}
		r.depth++
		out := make(map[any]any, v)
		for i := uint64(0); i < v; i++ {
			k, err := r.value()
			if err != nil {
				return nil, err
			}
			switch k.(type) {
			case int64, string:
			default:
				return nil, errors.New("cbor: map key type")
			}
			x, err := r.value()
			if err != nil {
				return nil, err
			}
			out[k] = x
		}
		r.depth--
		return out, nil
	case 7:
		switch v {
		case 20:
			return false, nil
		case 21:
			return true, nil
		case 22:
			return nil, nil
		}
		return nil, errors.New("cbor: float or simple value")
	}
	return nil, errors.New("cbor: tag not supported")
}

func cborDecode(b []byte) (any, []byte, error) {
	r := &cborReader{b: b}
	v, err := r.value()
	return v, r.b, err
}

// ---------- client data and authenticator data

type clientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin bool   `json:"crossOrigin"`
}

func checkClient(raw []byte, typ string, challenge []byte, rp RP) error {
	var c clientData
	if err := json.Unmarshal(raw, &c); err != nil {
		return pkErr("client data is not JSON")
	}
	if c.Type != typ {
		return pkErr("client data type %q", c.Type)
	}
	got, err := base64.RawURLEncoding.DecodeString(c.Challenge)
	if err != nil || len(challenge) == 0 || subtle.ConstantTimeCompare(got, challenge) != 1 {
		return pkErr("challenge does not match")
	}
	if c.Origin != rp.Origin || c.CrossOrigin {
		return pkErr("origin %q is not %q", c.Origin, rp.Origin)
	}
	return nil
}

type authData struct {
	rpHash  []byte
	flags   byte
	count   uint32
	credID  []byte
	credKey []byte
}

func parseAuthData(b []byte, wantCred bool) (authData, error) {
	var a authData
	if len(b) < 37 {
		return a, pkErr("authenticator data too short")
	}
	a.rpHash, a.flags, a.count = b[:32], b[32], binary.BigEndian.Uint32(b[33:37])
	rest := b[37:]
	if a.flags&flagAT != 0 {
		if len(rest) < 18 {
			return a, pkErr("credential data too short")
		}
		n := int(binary.BigEndian.Uint16(rest[16:18]))
		rest = rest[18:]
		if n == 0 || n > 1023 || len(rest) < n {
			return a, pkErr("credential id length")
		}
		a.credID, rest = rest[:n], rest[n:]
		_, after, err := cborDecode(rest)
		if err != nil {
			return a, pkErr("credential key: %v", err)
		}
		a.credKey, rest = rest[:len(rest)-len(after)], after
	} else if wantCred {
		return a, pkErr("no credential in the authenticator data")
	}
	if a.flags&flagED != 0 {
		_, after, err := cborDecode(rest)
		if err != nil {
			return a, pkErr("extensions: %v", err)
		}
		rest = after
	}
	if len(rest) != 0 {
		return a, pkErr("trailing bytes in the authenticator data")
	}
	return a, nil
}

func checkFlags(a authData, rp RP) error {
	h := sha256.Sum256([]byte(rp.ID))
	if subtle.ConstantTimeCompare(a.rpHash, h[:]) != 1 {
		return pkErr("relying party does not match")
	}
	if a.flags&flagUP == 0 {
		return pkErr("user not present")
	}
	if a.flags&flagUV == 0 {
		return pkErr("user not verified (PIN, fingerprint or face needed)")
	}
	return nil
}

// ---------- COSE keys

func coseInt(m map[any]any, k int64) (int64, bool) {
	v, ok := m[k].(int64)
	return v, ok
}

func coseBytes(m map[any]any, k int64) []byte {
	v, _ := m[k].([]byte)
	return v
}

// publicKey turns a COSE_Key into a Go key and its algorithm.
func publicKey(cose []byte) (crypto.PublicKey, int64, error) {
	v, rest, err := cborDecode(cose)
	if err != nil || len(rest) != 0 {
		return nil, 0, pkErr("public key is not CBOR")
	}
	m, ok := v.(map[any]any)
	if !ok {
		return nil, 0, pkErr("public key is not a map")
	}
	kty, _ := coseInt(m, 1)
	alg, _ := coseInt(m, 3)
	switch {
	case kty == 2 && alg == -7: // EC2, ES256
		crv, _ := coseInt(m, -1)
		x, y := coseBytes(m, -2), coseBytes(m, -3)
		if crv != 1 || len(x) != 32 || len(y) != 32 {
			return nil, 0, pkErr("ES256 key is not P-256")
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if _, err := pub.ECDH(); err != nil {
			return nil, 0, pkErr("ES256 key is not on the curve")
		}
		return pub, alg, nil
	case kty == 1 && alg == -8: // OKP, EdDSA
		crv, _ := coseInt(m, -1)
		x := coseBytes(m, -2)
		if crv != 6 || len(x) != ed25519.PublicKeySize {
			return nil, 0, pkErr("EdDSA key is not Ed25519")
		}
		return ed25519.PublicKey(x), alg, nil
	case kty == 3 && alg == -257: // RSA, RS256
		n, e := coseBytes(m, -1), coseBytes(m, -2)
		if len(n) < 256 || len(e) == 0 || len(e) > 4 {
			return nil, 0, pkErr("RS256 key shorter than 2048 bits")
		}
		ev := 0
		for _, c := range e {
			ev = ev<<8 | int(c)
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: ev}, alg, nil
	}
	return nil, 0, pkErr("key type %d / algorithm %d not supported", kty, alg)
}

func verifySig(pub crypto.PublicKey, signed, sig []byte) bool {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		h := sha256.Sum256(signed)
		return ecdsa.VerifyASN1(k, h[:], sig)
	case ed25519.PublicKey:
		return ed25519.Verify(k, signed, sig)
	case *rsa.PublicKey:
		h := sha256.Sum256(signed)
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, h[:], sig) == nil
	}
	return false
}

// ---------- the two ceremonies

// Register checks a navigator.credentials.create() answer and returns the new passkey.
func Register(rp RP, challenge, clientDataJSON, attestationObject []byte) (Credential, error) {
	if err := checkClient(clientDataJSON, "webauthn.create", challenge, rp); err != nil {
		return Credential{}, err
	}
	v, rest, err := cborDecode(attestationObject)
	if err != nil || len(rest) != 0 {
		return Credential{}, pkErr("attestation is not CBOR")
	}
	m, ok := v.(map[any]any)
	if !ok {
		return Credential{}, pkErr("attestation is not a map")
	}
	raw, _ := m["authData"].([]byte)
	a, err := parseAuthData(raw, true)
	if err != nil {
		return Credential{}, err
	}
	if err := checkFlags(a, rp); err != nil {
		return Credential{}, err
	}
	_, alg, err := publicKey(a.credKey)
	if err != nil {
		return Credential{}, err
	}
	return Credential{ID: bytes.Clone(a.credID), PublicKey: bytes.Clone(a.credKey), Alg: alg, SignCount: a.count}, nil
}

// Verify checks a navigator.credentials.get() answer against a stored passkey and returns its new counter.
func Verify(rp RP, c Credential, challenge, clientDataJSON, authenticatorData, signature []byte) (uint32, error) {
	if err := checkClient(clientDataJSON, "webauthn.get", challenge, rp); err != nil {
		return 0, err
	}
	a, err := parseAuthData(authenticatorData, false)
	if err != nil {
		return 0, err
	}
	if err := checkFlags(a, rp); err != nil {
		return 0, err
	}
	pub, _, err := publicKey(c.PublicKey)
	if err != nil {
		return 0, err
	}
	h := sha256.Sum256(clientDataJSON)
	signed := append(bytes.Clone(authenticatorData), h[:]...)
	if !verifySig(pub, signed, signature) {
		return 0, pkErr("signature does not match")
	}
	// a counter that does not grow means a copied key (counters of 0 are passkeys that do not count)
	if (a.count != 0 || c.SignCount != 0) && a.count <= c.SignCount {
		return 0, pkErr("signature counter went back (%d after %d): the key may be copied", a.count, c.SignCount)
	}
	return a.count, nil
}
