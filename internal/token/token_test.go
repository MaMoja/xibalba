package token

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)

func signer(t *testing.T, fill byte) *Signer {
	t.Helper()
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = fill
	}
	s, err := NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRoundTrip(t *testing.T) {
	s := signer(t, 1)
	in := Claims{
		Expires:    now.Add(5 * time.Minute),
		NotBefore:  now.Add(3 * time.Second),
		Binding:    s.Bind("198.51.100.0/24", "Mozilla"),
		Nonce:      "00112233445566778899aabbccddeeff",
		Difficulty: 18,
	}
	tok := s.Sign(Challenge, in)
	out, err := s.Verify(Challenge, tok, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !out.Expires.Equal(in.Expires) || !out.NotBefore.Equal(in.NotBefore) || out.Binding != in.Binding ||
		out.Nonce != in.Nonce || out.Difficulty != in.Difficulty {
		t.Errorf("got %+v, want %+v", out, in)
	}
	if strings.ContainsAny(tok, "+/= \n") {
		t.Errorf("token %q is not safe to put in a cookie or form field", tok)
	}
}

func TestVerifyRejects(t *testing.T) {
	s := signer(t, 1)
	other := signer(t, 2)
	valid := s.Sign(Pass, Claims{Expires: now.Add(time.Hour), Binding: "b"})
	parts := strings.Split(valid, ".")

	// A payload that claims a far later expiry, with the old signature.
	forgedPayload := base64.RawURLEncoding.EncodeToString([]byte(`{"e":99999999999,"b":"b"}`))

	tests := []struct {
		name string
		tok  string
		at   time.Time
		want error
	}{
		{"empty", "", now, ErrMalformed},
		{"garbage", "not a token", now, ErrMalformed},
		{"two parts", parts[0] + "." + parts[1], now, ErrMalformed},
		{"four parts", valid + ".x", now, ErrMalformed},
		{"wrong version", "v2." + parts[1] + "." + parts[2], now, ErrMalformed},
		{"signature not base64", parts[0] + "." + parts[1] + ".!!!", now, ErrMalformed},
		{"huge", strings.Repeat("a", maxLength+1), now, ErrMalformed},
		{"signed with another key", other.Sign(Pass, Claims{Expires: now.Add(time.Hour)}), now, ErrSignature},
		{"payload changed, signature kept", parts[0] + "." + forgedPayload + "." + parts[2], now, ErrSignature},
		{"signature truncated", valid[:len(valid)-4], now, ErrSignature},
		{"signature of all zeros", parts[0] + "." + parts[1] + "." + strings.Repeat("A", 43), now, ErrSignature},
		{"expired", valid, now.Add(time.Hour), ErrExpired},
		{"expired long ago", valid, now.Add(1000 * time.Hour), ErrExpired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.Verify(Pass, tt.tok, tt.at); !errors.Is(err, tt.want) {
				t.Errorf("Verify = %v, want %v", err, tt.want)
			}
		})
	}
}

// A solved-challenge token must never be accepted as a pass, or the other way round.
func TestKindsAreNotInterchangeable(t *testing.T) {
	s := signer(t, 1)
	claims := Claims{Expires: now.Add(time.Hour), Binding: "b"}
	if _, err := s.Verify(Pass, s.Sign(Challenge, claims), now); !errors.Is(err, ErrSignature) {
		t.Errorf("a challenge token was accepted as a pass: %v", err)
	}
	if _, err := s.Verify(Challenge, s.Sign(Pass, claims), now); !errors.Is(err, ErrSignature) {
		t.Errorf("a pass token was accepted as a challenge: %v", err)
	}
}

func TestBind(t *testing.T) {
	s := signer(t, 1)
	a := s.Bind("198.51.100.0/24", "Mozilla/5.0")
	if a != s.Bind("198.51.100.0/24", "Mozilla/5.0") {
		t.Error("equal facts gave different bindings")
	}
	for name, b := range map[string]string{
		"other network":      s.Bind("198.51.101.0/24", "Mozilla/5.0"),
		"other user agent":   s.Bind("198.51.100.0/24", "curl/8"),
		"facts split apart":  s.Bind("198.51.100.0/2", "4Mozilla/5.0"),
		"another server key": signer(t, 2).Bind("198.51.100.0/24", "Mozilla/5.0"),
	} {
		if SameBinding(a, b) {
			t.Errorf("%s: binding is the same", name)
		}
	}
	if strings.Contains(a, "198") || strings.Contains(a, "Mozilla") {
		t.Errorf("the binding %q reveals the facts it was made from", a)
	}
}

func TestNewSignerRejectsShortKeys(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		if _, err := NewSigner(make([]byte, n)); err == nil {
			t.Errorf("a %d-byte key was accepted", n)
		}
	}
}

func TestNoncesAndKeysAreRandom(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		n, err := NewNonce()
		if err != nil || len(n) != 32 || seen[n] {
			t.Fatalf("nonce %q (err %v) is malformed or repeated", n, err)
		}
		seen[n] = true
	}
	a, _ := NewKey()
	b, _ := NewKey()
	if len(a) != KeySize || string(a) == string(b) {
		t.Error("NewKey returned a wrong-sized or repeated key")
	}
}

func TestLoadOrCreateKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "xibalba.key")

	key, created, err := LoadOrCreateKey(path)
	if err != nil || !created || len(key) != KeySize {
		t.Fatalf("first call: key %d bytes, created %v, err %v", len(key), created, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file permissions = %o, want 600", perm)
	}

	again, created, err := LoadOrCreateKey(path)
	if err != nil || created || string(again) != string(key) {
		t.Errorf("second call: created %v, err %v, same key %v; want the stored key", created, err, string(again) == string(key))
	}

	t.Run("broken file is not overwritten", func(t *testing.T) {
		bad := filepath.Join(dir, "bad.key")
		if err := os.WriteFile(bad, []byte("too short\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := LoadOrCreateKey(bad)
		if err == nil || !strings.Contains(err.Error(), "64 hexadecimal characters") {
			t.Errorf("got %v, want an explanation", err)
		}
		if data, _ := os.ReadFile(bad); string(data) != "too short\n" {
			t.Error("the broken key file was changed")
		}
	})

	t.Run("missing directory is explained", func(t *testing.T) {
		_, _, err := LoadOrCreateKey(filepath.Join(dir, "nope", "xibalba.key"))
		if err == nil || !strings.Contains(err.Error(), "does the directory") {
			t.Errorf("got %v, want a hint about the directory", err)
		}
	})

	t.Run("error does not contain the key", func(t *testing.T) {
		almost := filepath.Join(dir, "almost.key")
		secret := strings.Repeat("ab", KeySize-1) // one byte short
		if err := os.WriteFile(almost, []byte(secret), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := LoadOrCreateKey(almost)
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("error is missing or leaks the key material: %v", err)
		}
	})
}
