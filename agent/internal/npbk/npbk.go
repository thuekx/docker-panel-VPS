// Package npbk seals a stream with a passphrase (server backups, BAK-02), with the Go standard library only.
//
// Layout 1 (a backup made with a passphrase typed for it):
// "NPBK1\n" | rounds (uint32, big endian) | salt (16 bytes) | nonce prefix (7 bytes) | chunks.
// The key is PBKDF2-SHA256(passphrase, salt, rounds), 32 bytes, for AES-256-GCM.
//
// Layout 2 (scheduled backups, BAK-03: the server keeps no passphrase, only a public key):
// "NPBK2\n" | rounds | salt | ephemeral X25519 public key (32 bytes) | nonce prefix | chunks.
// The passphrase gives, once, the X25519 private key PBKDF2-SHA256(passphrase, salt, rounds); the server keeps only
// its public key with the salt and rounds (a Recipient). Every file gets a fresh ephemeral key; the AES-256-GCM key
// is HKDF-SHA256(X25519(ephemeral, recipient), salt = ephemeral public | recipient public, info "nostek npbk2").
// Opening a file takes the same passphrase as layout 1.
//
// In both layouts: The plain stream is cut into
// chunks of 64 KiB; chunk i is sealed with nonce = prefix | i (uint32) | last (1 byte: 1 for the final chunk), and
// the header as additional data. Every chunk carries its own tag, so a changed, cut, reordered or extended file
// fails when it is read, and the final chunk (possibly empty) proves the end.
package npbk

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
)

const (
	magic     = "NPBK1\n"
	magic2    = "NPBK2\n"
	pubLen    = 32
	saltLen   = 16
	prefixLen = 7
	chunk     = 64 << 10
	// Rounds is the PBKDF2 work for new files.
	Rounds = 600_000
	// MinPassphrase is the shortest passphrase accepted for a new file.
	MinPassphrase = 12
	maxRounds     = 10_000_000
)

var (
	// ErrPassphrase means the passphrase is wrong or the file was changed.
	ErrPassphrase = errors.New("wrong passphrase, or the file was changed")
	// ErrFormat means the file is not a sealed server backup.
	ErrFormat = errors.New("not a Nostek server backup")
	// ErrCut means the file ends before its final chunk.
	ErrCut = errors.New("the file is incomplete")
	// ErrShort means the passphrase is too short.
	ErrShort = errors.New("the passphrase needs at least 12 characters")
)

func header(rounds uint32, salt, prefix []byte) []byte {
	h := make([]byte, 0, len(magic)+4+saltLen+prefixLen)
	h = append(h, magic...)
	h = binary.BigEndian.AppendUint32(h, rounds)
	h = append(h, salt...)
	return append(h, prefix...)
}

func aead(pass string, salt []byte, rounds uint32) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, pass, salt, int(rounds), 32)
	if err != nil {
		return nil, err
	}
	return gcm(key)
}

func gcm(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func nonce(prefix []byte, i uint32, last bool) []byte {
	n := make([]byte, 0, 12)
	n = append(n, prefix...)
	n = binary.BigEndian.AppendUint32(n, i)
	if last {
		return append(n, 1)
	}
	return append(n, 0)
}

// Writer seals what is written to it. Close writes the final chunk; without it the file is refused when read.
type Writer struct {
	w      io.Writer
	a      cipher.AEAD
	hdr    []byte
	prefix []byte
	buf    []byte
	i      uint32
	closed bool
}

// NewWriter writes the header to w and returns a Writer, with the standard number of rounds.
func NewWriter(w io.Writer, pass string) (*Writer, error) { return newWriter(w, pass, Rounds) }

func newWriter(w io.Writer, pass string, rounds uint32) (*Writer, error) {
	if len([]rune(pass)) < MinPassphrase {
		return nil, ErrShort
	}
	salt, prefix := make([]byte, saltLen), make([]byte, prefixLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(prefix); err != nil {
		return nil, err
	}
	a, err := aead(pass, salt, rounds)
	if err != nil {
		return nil, err
	}
	return start(w, a, header(rounds, salt, prefix), prefix)
}

func start(w io.Writer, a cipher.AEAD, hdr, prefix []byte) (*Writer, error) {
	if _, err := w.Write(hdr); err != nil {
		return nil, err
	}
	return &Writer{w: w, a: a, hdr: hdr, prefix: prefix, buf: make([]byte, 0, chunk)}, nil
}

// Recipient is what the server keeps for scheduled backups: the public half of the key a passphrase gives. It
// cannot open a file.
type Recipient struct {
	Rounds uint32 `json:"rounds"`
	Salt   []byte `json:"salt"`
	Pub    []byte `json:"pub"`
}

func privKey(pass string, salt []byte, rounds uint32) (*ecdh.PrivateKey, error) {
	seed, err := pbkdf2.Key(sha256.New, pass, salt, int(rounds), 32)
	if err != nil {
		return nil, err
	}
	return ecdh.X25519().NewPrivateKey(seed)
}

// NewRecipient turns a passphrase into a Recipient, with a fresh salt and the standard number of rounds.
func NewRecipient(pass string) (Recipient, error) { return newRecipient(pass, Rounds) }

func newRecipient(pass string, rounds uint32) (Recipient, error) {
	if len([]rune(pass)) < MinPassphrase {
		return Recipient{}, ErrShort
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return Recipient{}, err
	}
	k, err := privKey(pass, salt, rounds)
	if err != nil {
		return Recipient{}, err
	}
	return Recipient{Rounds: rounds, Salt: salt, Pub: k.PublicKey().Bytes()}, nil
}

// Valid tells whether the Recipient can be used.
func (rc Recipient) Valid() bool {
	if rc.Rounds < 1000 || rc.Rounds > maxRounds || len(rc.Salt) != saltLen || len(rc.Pub) != pubLen {
		return false
	}
	_, err := ecdh.X25519().NewPublicKey(rc.Pub)
	return err == nil
}

func key2(shared, eph, recip []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, shared, append(append([]byte{}, eph...), recip...), "nostek npbk2", 32)
}

// NewWriterTo writes a layout 2 header to w and returns a Writer that only the Recipient's passphrase opens.
func NewWriterTo(w io.Writer, rc Recipient) (*Writer, error) {
	if !rc.Valid() {
		return nil, errors.New("npbk: bad recipient")
	}
	pub, _ := ecdh.X25519().NewPublicKey(rc.Pub)
	e, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := e.ECDH(pub)
	if err != nil {
		return nil, err
	}
	k, err := key2(shared, e.PublicKey().Bytes(), rc.Pub)
	if err != nil {
		return nil, err
	}
	a, err := gcm(k)
	if err != nil {
		return nil, err
	}
	prefix := make([]byte, prefixLen)
	if _, err := rand.Read(prefix); err != nil {
		return nil, err
	}
	hdr := make([]byte, 0, len(magic2)+4+saltLen+pubLen+prefixLen)
	hdr = append(hdr, magic2...)
	hdr = binary.BigEndian.AppendUint32(hdr, rc.Rounds)
	hdr = append(hdr, rc.Salt...)
	hdr = append(hdr, e.PublicKey().Bytes()...)
	hdr = append(hdr, prefix...)
	return start(w, a, hdr, prefix)
}

func (s *Writer) seal(last bool) error {
	if s.i == ^uint32(0) {
		return errors.New("npbk: file too large")
	}
	out := s.a.Seal(nil, nonce(s.prefix, s.i, last), s.buf, s.hdr)
	s.i++
	s.buf = s.buf[:0]
	_, err := s.w.Write(out)
	return err
}

func (s *Writer) Write(p []byte) (int, error) {
	if s.closed {
		return 0, errors.New("npbk: write after close")
	}
	n := 0
	for len(p) > 0 {
		k := min(chunk-len(s.buf), len(p))
		s.buf = append(s.buf, p[:k]...)
		p, n = p[k:], n+k
		// a full chunk is sealed only once more data follows, so the final chunk is never empty by accident
		if len(s.buf) == chunk && len(p) > 0 {
			if err := s.seal(false); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

// Close seals the final chunk.
func (s *Writer) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.seal(true)
}

// Reader opens a sealed stream. Read returns ErrPassphrase for a wrong passphrase or a changed chunk and ErrCut
// when the stream ends before the final chunk.
type Reader struct {
	r      *bufio.Reader
	a      cipher.AEAD
	hdr    []byte
	prefix []byte
	plain  []byte
	i      uint32
	done   bool
	err    error
}

// NewReader reads the header (layout 1 or 2) and checks the passphrase on the first chunk.
func NewReader(r io.Reader, pass string) (*Reader, error) {
	br := bufio.NewReaderSize(r, chunk+64)
	hdr := make([]byte, len(magic)+4+saltLen)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return nil, ErrFormat
	}
	v2 := bytes.Equal(hdr[:len(magic2)], []byte(magic2))
	if !v2 && !bytes.Equal(hdr[:len(magic)], []byte(magic)) {
		return nil, ErrFormat
	}
	rounds := binary.BigEndian.Uint32(hdr[len(magic):])
	if rounds < 1000 || rounds > maxRounds {
		return nil, ErrFormat
	}
	salt := append([]byte{}, hdr[len(magic)+4:]...)
	rest := prefixLen
	if v2 {
		rest += pubLen
	}
	tail := make([]byte, rest)
	if _, err := io.ReadFull(br, tail); err != nil {
		return nil, ErrFormat
	}
	hdr = append(hdr, tail...)
	var a cipher.AEAD
	var err error
	if v2 {
		a, err = open2(pass, salt, rounds, tail[:pubLen])
	} else {
		a, err = aead(pass, salt, rounds)
	}
	if err != nil {
		return nil, err
	}
	rd := &Reader{r: br, a: a, hdr: hdr, prefix: hdr[len(hdr)-prefixLen:]}
	if err := rd.next(); err != nil {
		return nil, err
	}
	return rd, nil
}

// next opens the next chunk. A full-size sealed chunk is either a middle chunk or (rarely) the final one; which one
// is told by trying both nonces, so no length is stored in the clear.
func (s *Reader) next() error {
	sealed := make([]byte, chunk+s.a.Overhead())
	n, err := io.ReadFull(s.r, sealed)
	switch {
	case err == io.EOF:
		return ErrCut
	case err == io.ErrUnexpectedEOF:
		sealed = sealed[:n]
	case err != nil:
		return err
	}
	if len(sealed) < s.a.Overhead() {
		return ErrCut
	}
	if p, err := s.a.Open(nil, nonce(s.prefix, s.i, false), sealed, s.hdr); err == nil {
		if len(sealed) != chunk+s.a.Overhead() {
			return ErrPassphrase
		}
		s.plain, s.i = p, s.i+1
		return nil
	}
	p, err := s.a.Open(nil, nonce(s.prefix, s.i, true), sealed, s.hdr)
	if err != nil {
		return ErrPassphrase
	}
	// nothing may follow the final chunk
	if _, err := s.r.ReadByte(); err != io.EOF {
		return ErrPassphrase
	}
	s.plain, s.done = p, true
	return nil
}

func (s *Reader) Read(p []byte) (int, error) {
	for len(s.plain) == 0 {
		if s.err != nil {
			return 0, s.err
		}
		if s.done {
			return 0, io.EOF
		}
		if err := s.next(); err != nil {
			s.err = err
			return 0, err
		}
	}
	n := copy(p, s.plain)
	s.plain = s.plain[n:]
	return n, nil
}

func open2(pass string, salt []byte, rounds uint32, eph []byte) (cipher.AEAD, error) {
	ep, err := ecdh.X25519().NewPublicKey(eph)
	if err != nil {
		return nil, ErrFormat
	}
	k, err := privKey(pass, salt, rounds)
	if err != nil {
		return nil, err
	}
	shared, err := k.ECDH(ep)
	if err != nil {
		return nil, ErrPassphrase
	}
	key, err := key2(shared, eph, k.PublicKey().Bytes())
	if err != nil {
		return nil, err
	}
	return gcm(key)
}
