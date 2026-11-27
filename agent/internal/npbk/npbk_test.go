package npbk

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

const pass = "correct horse battery"

func seal(t *testing.T, plain []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := newWriter(&b, pass, 1000)
	if err != nil {
		t.Fatal(err)
	}
	// write in odd pieces so chunk edges fall inside a Write
	for p := plain; len(p) > 0; {
		k := min(len(p), 10007)
		if _, err := w.Write(p[:k]); err != nil {
			t.Fatal(err)
		}
		p = p[k:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func open(sealed []byte, pw string) ([]byte, error) {
	r, err := NewReader(bytes.NewReader(sealed), pw)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func TestRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, chunk - 1, chunk, chunk + 1, 3 * chunk, 5*chunk + 123} {
		plain := make([]byte, n)
		rand.Read(plain)
		sealed := seal(t, plain)
		got, err := open(sealed, pass)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("size %d: err %v, equal %v", n, err, bytes.Equal(got, plain))
		}
		if bytes.Contains(sealed, []byte(pass)) {
			t.Fatal("passphrase in the file")
		}
	}
}

func TestRefused(t *testing.T) {
	plain := make([]byte, 3*chunk+500)
	rand.Read(plain)
	sealed := seal(t, plain)
	hdr := len(magic) + 4 + saltLen + prefixLen
	full := chunk + 16
	swap := append([]byte{}, sealed[:hdr]...)
	swap = append(swap, sealed[hdr+full:hdr+2*full]...)
	swap = append(swap, sealed[hdr:hdr+full]...)
	swap = append(swap, sealed[hdr+2*full:]...)
	flip := append([]byte{}, sealed...)
	flip[hdr+full+7] ^= 1
	hflip := append([]byte{}, sealed...)
	hflip[len(magic)+5] ^= 1
	cases := []struct {
		name string
		data []byte
		pw   string
		want error
	}{
		{"wrong passphrase", sealed, "wrong horse battery", ErrPassphrase},
		{"flipped byte", flip, pass, ErrPassphrase},
		{"flipped header", hflip, pass, ErrPassphrase},
		{"swapped chunks", swap, pass, ErrPassphrase},
		{"data appended", append(append([]byte{}, sealed...), 0), pass, ErrPassphrase},
		{"cut at a chunk edge", sealed[:hdr+2*full], pass, ErrCut},
		{"cut inside a chunk", sealed[:len(sealed)-10], pass, ErrPassphrase},
		{"header only", sealed[:hdr], pass, ErrCut},
		{"not a backup", []byte("PK\x03\x04 a zip file, not ours at all"), pass, ErrFormat},
		{"empty", nil, pass, ErrFormat},
	}
	for _, c := range cases {
		got, err := open(c.data, c.pw)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: err %v, want %v (got %d bytes)", c.name, err, c.want, len(got))
		}
	}
}

func TestShortPassphrase(t *testing.T) {
	if _, err := NewWriter(io.Discard, "elevenchars"); !errors.Is(err, ErrShort) {
		t.Fatalf("err %v", err)
	}
	// characters, not bytes: 12 multi-byte letters pass
	if _, err := newWriter(io.Discard, "ééééééééééé", 1000); !errors.Is(err, ErrShort) {
		t.Fatalf("11 letters: %v", err)
	}
	if _, err := newWriter(io.Discard, "éééééééééééé", 1000); err != nil {
		t.Fatalf("12 letters: %v", err)
	}
}

func TestUnclosedRefused(t *testing.T) {
	var b bytes.Buffer
	w, _ := newWriter(&b, pass, 1000)
	w.Write(make([]byte, 2*chunk+1))
	if _, err := open(b.Bytes(), pass); !errors.Is(err, ErrCut) {
		t.Fatalf("err %v", err)
	}
}

func TestStandardRounds(t *testing.T) {
	var b bytes.Buffer
	w, err := NewWriter(&b, pass)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("hello"))
	w.Close()
	got, err := open(b.Bytes(), pass)
	if err != nil || string(got) != "hello" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestRecipient(t *testing.T) {
	rc, err := newRecipient(pass, 1000)
	if err != nil || !rc.Valid() {
		t.Fatal(rc, err)
	}
	if _, err := newRecipient("short", 1000); !errors.Is(err, ErrShort) {
		t.Fatal(err)
	}
	plain := make([]byte, 3*chunk+123)
	_, _ = rand.Read(plain)
	var b bytes.Buffer
	w, err := NewWriterTo(&b, rc)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(plain)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	sealed := b.Bytes()
	if !bytes.HasPrefix(sealed, []byte("NPBK2\n")) || bytes.Contains(sealed, rc.Pub) {
		t.Fatal("bad header")
	}
	got, err := open(sealed, pass)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatal("round trip", err)
	}
	if _, err := open(sealed, "another passphrase!"); !errors.Is(err, ErrPassphrase) {
		t.Fatal(err)
	}
	// a second file to the same recipient uses another key
	var b2 bytes.Buffer
	w2, _ := NewWriterTo(&b2, rc)
	_, _ = w2.Write(plain)
	_ = w2.Close()
	if bytes.Equal(b2.Bytes()[:100], sealed[:100]) {
		t.Fatal("same header twice")
	}
	// cut and changed
	if _, err := open(sealed[:len(sealed)-10], pass); err == nil {
		t.Fatal("cut accepted")
	}
	c := append([]byte{}, sealed...)
	c[len(c)/2] ^= 1
	if _, err := open(c, pass); !errors.Is(err, ErrPassphrase) {
		t.Fatal(err)
	}
	// a changed ephemeral key is refused
	c = append([]byte{}, sealed...)
	c[6+4+16+3] ^= 1
	if _, err := open(c, pass); err == nil {
		t.Fatal("changed key accepted")
	}
	if (Recipient{}).Valid() {
		t.Fatal("empty recipient valid")
	}
	if _, err := NewWriterTo(&b, Recipient{}); err == nil {
		t.Fatal("bad recipient accepted")
	}
}
