package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"errors"
)

// Seal encrypts plain with a key derived from token (AES-256-GCM). The database stores only
// TokenHash(token) and the sealed bytes, so the secret is readable only by whoever holds the link.
func Seal(token string, plain []byte) ([]byte, error) {
	k := sha256.Sum256([]byte("nostek-seal:" + token))
	blk, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	nonce := RandomBytes(g.NonceSize())
	return append(nonce, g.Seal(nil, nonce, plain, nil)...), nil
}

// Open reverses Seal.
func Open(token string, sealed []byte) ([]byte, error) {
	k := sha256.Sum256([]byte("nostek-seal:" + token))
	blk, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	if len(sealed) < g.NonceSize() {
		return nil, errors.New("short")
	}
	return g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], nil)
}
