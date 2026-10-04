// Package token signs and verifies the small tokens Xibalba hands to clients:
// the challenge it asks a client to solve and the pass that says a client
// has solved one.
//
// A token is a statement by Xibalba, protected by a keyed signature
// (HMAC-SHA-256). Nothing is stored on the server: whoever presents a token
// with a valid signature presents something Xibalba issued. Everything a
// client sends back is therefore treated as untrusted until Verify has
// checked signature, kind and expiry, in that order.
//
// Tokens carry no personal data. A token is tied to a client through a
// binding, which is itself a keyed hash and cannot be turned back into the
// address or user agent it was made from.
package token

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// KeySize is the length of the signing key in bytes.
const KeySize = 32

// maxLength bounds the tokens Verify is willing to look at.
const maxLength = 1024

const version = "v1"

// Kind says what a token is for. A token of one kind is never accepted as
// another: each kind is signed with its own key derived from the main key.
type Kind string

const (
	// Challenge is a task handed to a client.
	Challenge Kind = "challenge"
	// Pass states that a client has solved a challenge.
	Pass Kind = "pass"
)

// Reasons a token is rejected. Callers treat them all the same way (the
// client gets a new challenge); they are distinct for tests and statistics.
var (
	ErrMalformed = errors.New("token: malformed")
	ErrSignature = errors.New("token: signature does not match")
	ErrExpired   = errors.New("token: expired")
)

// Claims is what a token states.
type Claims struct {
	// Expires is when the token stops being valid.
	Expires time.Time
	// NotBefore, if set, is the earliest moment an answer is accepted.
	NotBefore time.Time
	// Binding ties the token to one client (see Signer.Bind).
	Binding string
	// Nonce is a random value that makes each challenge different.
	Nonce string
	// Difficulty is the amount of work a challenge asks for.
	Difficulty int
	// Method names the kind of check a challenge asks for.
	Method string
	// Checks holds the extra checks a challenge asks for, or a pass has
	// met, one bit each.
	Checks uint8
	// Level says how demanding the check was that a pass was earned with.
	Level int
}

// wire is the encoded form of Claims. Times are Unix seconds.
type wire struct {
	Expires    int64  `json:"e"`
	NotBefore  int64  `json:"s,omitempty"`
	Binding    string `json:"b,omitempty"`
	Nonce      string `json:"n,omitempty"`
	Difficulty int    `json:"d,omitempty"`
	Method     string `json:"m,omitempty"`
	Checks     uint8  `json:"c,omitempty"`
	Level      int    `json:"l,omitempty"`
}

// Signer signs and verifies tokens with one key. It is safe for concurrent use.
type Signer struct {
	keys    map[Kind][]byte
	bindKey []byte
	macKey  []byte
}

// NewSigner returns a Signer for key, which must be KeySize bytes long.
func NewSigner(key []byte) (*Signer, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("token: the key has %d bytes, it must have %d", len(key), KeySize)
	}
	derive := func(purpose string) []byte {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte("xibalba/" + version + "/" + purpose))
		return mac.Sum(nil)
	}
	return &Signer{
		keys:    map[Kind][]byte{Challenge: derive("challenge"), Pass: derive("pass")},
		bindKey: derive("binding"),
		macKey:  derive("mac"),
	}, nil
}

// MAC returns a short value only this Signer can compute for data, for a
// purpose of its own. It is for values handed to a client that the client
// must show again, where a whole token would be too much.
func (s *Signer) MAC(purpose, data string) string {
	mac := hmac.New(sha256.New, s.macKey)
	mac.Write([]byte("mac/" + purpose + "\x00" + data))
	return hex.EncodeToString(mac.Sum(nil)[:12])
}

// Sign returns a token of the given kind stating c.
func (s *Signer) Sign(kind Kind, c Claims) string {
	w := wire{Expires: c.Expires.Unix(), Binding: c.Binding, Nonce: c.Nonce, Difficulty: c.Difficulty,
		Method: c.Method, Checks: c.Checks, Level: c.Level}
	if !c.NotBefore.IsZero() {
		w.NotBefore = c.NotBefore.Unix()
	}
	payload, _ := json.Marshal(w) // a struct of strings and numbers cannot fail to encode
	body := version + "." + base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + base64.RawURLEncoding.EncodeToString(s.mac(kind, body))
}

// Verify checks that tok is a token of the given kind, signed by this Signer
// and not expired at now, and returns what it states.
func (s *Signer) Verify(kind Kind, tok string, now time.Time) (Claims, error) {
	if len(tok) == 0 || len(tok) > maxLength {
		return Claims{}, ErrMalformed
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || parts[0] != version {
		return Claims{}, ErrMalformed
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, ErrMalformed
	}
	// The signature is checked before the payload is even decoded, and in
	// constant time, so nothing about a forged token is trusted or leaked.
	if !hmac.Equal(signature, s.mac(kind, parts[0]+"."+parts[1])) {
		return Claims{}, ErrSignature
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrMalformed
	}
	var w wire
	if err := json.Unmarshal(payload, &w); err != nil {
		return Claims{}, ErrMalformed
	}
	c := Claims{Expires: time.Unix(w.Expires, 0), Binding: w.Binding, Nonce: w.Nonce, Difficulty: w.Difficulty,
		Method: w.Method, Checks: w.Checks, Level: w.Level}
	if w.NotBefore != 0 {
		c.NotBefore = time.Unix(w.NotBefore, 0)
	}
	if !now.Before(c.Expires) {
		return Claims{}, ErrExpired
	}
	return c, nil
}

func (s *Signer) mac(kind Kind, body string) []byte {
	mac := hmac.New(sha256.New, s.keys[kind])
	mac.Write([]byte(body))
	return mac.Sum(nil)
}

// Bind turns facts about a client (its network, its user agent) into a short
// value that ties a token to that client. The value is a keyed hash: equal
// facts give equal values, and the facts cannot be recovered from it.
func (s *Signer) Bind(facts ...string) string {
	mac := hmac.New(sha256.New, s.bindKey)
	for _, f := range facts {
		// Length-prefix each fact so ("ab","c") and ("a","bc") differ.
		mac.Write([]byte(strconv.Itoa(len(f)) + ":"))
		mac.Write([]byte(f))
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// SameBinding compares two bindings in constant time.
func SameBinding(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

// NewNonce returns a random value for a challenge, as 32 hexadecimal characters.
func NewNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// NewKey returns a fresh random key.
func NewKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

// LoadOrCreateKey reads the key stored in path. If the file does not exist it
// creates one with a fresh random key, readable by the owner only. created
// reports whether it did.
//
// The file holds the key as 64 hexadecimal characters. Keeping the key in a
// file means passes survive a restart, and several instances that share the
// file accept each other's tokens.
func LoadOrCreateKey(path string) (key []byte, created bool, err error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, err := ParseKey(string(data))
		if err != nil {
			return nil, false, fmt.Errorf("key file %s: %w", path, err)
		}
		return key, false, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, false, fmt.Errorf("key file %s cannot be read: %w", path, err)
	}

	key, err = NewKey()
	if err != nil {
		return nil, false, err
	}
	// O_EXCL: if another instance created the file in the meantime, use
	// theirs instead of overwriting it.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return LoadOrCreateKey(path)
	}
	if err != nil {
		return nil, false, fmt.Errorf("key file %s cannot be created (does the directory %s exist and is it writable?): %w",
			path, filepath.Dir(path), err)
	}
	_, werr := f.WriteString(hex.EncodeToString(key) + "\n")
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(path)
		return nil, false, fmt.Errorf("key file %s cannot be written: %w", path, errors.Join(werr, cerr))
	}
	return key, true, nil
}

// ParseKey reads a key written as 64 hexadecimal characters, ignoring
// surrounding white space.
func ParseKey(text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	key, err := hex.DecodeString(text)
	if err != nil || len(key) != KeySize {
		return nil, fmt.Errorf("it must contain exactly %d hexadecimal characters (it has %d characters); delete the file to have a new key created",
			KeySize*2, len(text))
	}
	return key, nil
}
