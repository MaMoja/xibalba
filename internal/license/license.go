// Package license verifies sponsor licenses.
//
// Xibalba is free software and stays fully functional without a license.
// What a license changes is appearance: sponsors may remove the "Protected by
// Xibalba" line from the pages visitors see and put their own name and
// wording on them.
//
// A license is a short text file: a statement (who it was issued to, until
// when) signed with the project's private key. Xibalba checks the signature
// with the public key built into the program. The check happens on the
// machine Xibalba runs on; nothing is sent anywhere.
//
// This is a courtesy lock, not copy protection. The source is open, and
// anyone can build a version without the check. The license exists so that
// organisations who want to do the right thing have a simple way to do it.
package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Format is the first part of every license and names its version.
const Format = "xibalba-license-v1"

// GracePeriod is how long after its expiry date a license keeps working, so
// that a late renewal does not change a website's pages overnight.
const GracePeriod = 30 * 24 * time.Hour

// dateLayout is how dates are written in a license.
const dateLayout = "2006-01-02"

// maxLength bounds the text Parse is willing to look at.
const maxLength = 4096

// publicKeyHex is the project's public key. A license is valid only if it
// was signed with the matching private key, which the project keeps to
// itself. The integration tests replace this value at build time with a key
// of their own (-ldflags -X); it is never changed while the program runs.
var publicKeyHex = "73024cd75cf4accf70b9cfcccc693b043e1fae849a4b1f91ded482771c9e0ae6"

// PublicKey returns the project's public key.
func PublicKey() (ed25519.PublicKey, error) {
	key, err := hex.DecodeString(publicKeyHex)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("license: the public key built into this program is malformed")
	}
	return ed25519.PublicKey(key), nil
}

// License is what a license states.
type License struct {
	// ID identifies this license.
	ID string `json:"id"`
	// Licensee is the organisation the license was issued to.
	Licensee string `json:"licensee"`
	// Sponsor is the sponsoring account, for the project's own records.
	Sponsor string `json:"sponsor,omitempty"`
	// Issued is the day the license was issued (YYYY-MM-DD).
	Issued string `json:"issued"`
	// Expires is the last day the license is valid (YYYY-MM-DD).
	Expires string `json:"expires"`
}

// State says whether a license can be used at a given moment.
type State string

const (
	// Valid: within its term.
	Valid State = "valid"
	// Grace: past its expiry date but within the grace period. It still works.
	Grace State = "grace"
	// Expired: past the grace period. It no longer unlocks anything.
	Expired State = "expired"
)

// ExpiresAt returns the moment the license's term ends: the end of its
// expiry day in UTC.
func (l License) ExpiresAt() time.Time {
	day, err := time.Parse(dateLayout, l.Expires)
	if err != nil {
		return time.Time{} // Parse has checked the date; this cannot happen for a parsed license
	}
	return day.Add(24 * time.Hour)
}

// State reports whether the license can be used at now.
func (l License) State(now time.Time) State {
	end := l.ExpiresAt()
	switch {
	case now.Before(end):
		return Valid
	case now.Before(end.Add(GracePeriod)):
		return Grace
	default:
		return Expired
	}
}

// GraceEnds returns the last day the license still works, written YYYY-MM-DD.
func (l License) GraceEnds() string {
	return l.ExpiresAt().Add(GracePeriod - time.Second).Format(dateLayout)
}

// Usable reports whether the license unlocks the sponsor features at now.
func (l License) Usable(now time.Time) bool { return l.State(now) != Expired }

// Parse reads a license file and checks its signature against key. Lines
// starting with "#" and blank lines are ignored, so a license file can carry
// a readable note about what it is.
//
// Parse does not look at the expiry date; an expired license is still a
// genuine one. Use State for that.
func Parse(text string, key ed25519.PublicKey) (License, error) {
	if len(text) > maxLength {
		return License{}, errors.New("the file is too large to be a license")
	}
	var token string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if token != "" {
			return License{}, errors.New("the file holds more than one line of license text")
		}
		token = line
	}
	if token == "" {
		return License{}, errors.New("the file holds no license")
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != Format {
		return License{}, fmt.Errorf("the text is not a license (it must start with %q)", Format)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize {
		return License{}, errors.New("the license is damaged (its signature cannot be read); copy the whole file again")
	}
	// The signature is checked before anything in the license is believed.
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), signature) {
		return License{}, errors.New("the license is not genuine or was changed (the signature does not match)")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return License{}, errors.New("the license is damaged")
	}
	var l License
	if err := json.Unmarshal(payload, &l); err != nil {
		return License{}, errors.New("the license is damaged")
	}
	if strings.TrimSpace(l.Licensee) == "" {
		return License{}, errors.New("the license names no licensee")
	}
	for _, date := range []string{l.Issued, l.Expires} {
		if _, err := time.Parse(dateLayout, date); err != nil {
			return License{}, errors.New("the license has a malformed date")
		}
	}
	return l, nil
}

// Issue signs a license with the project's private key and returns the text
// of the license file, with a readable note in front.
func Issue(private ed25519.PrivateKey, l License) (string, error) {
	if len(private) != ed25519.PrivateKeySize {
		return "", errors.New("license: the private key is malformed")
	}
	if strings.TrimSpace(l.Licensee) == "" {
		return "", errors.New("license: a licensee is required")
	}
	for name, date := range map[string]string{"issued": l.Issued, "expires": l.Expires} {
		if _, err := time.Parse(dateLayout, date); err != nil {
			return "", fmt.Errorf("license: the %s date %q must be written YYYY-MM-DD", name, date)
		}
	}
	if l.Expires < l.Issued {
		return "", errors.New("license: the expiry date is before the issue date")
	}
	if l.ID == "" {
		id := make([]byte, 8)
		if _, err := rand.Read(id); err != nil {
			return "", err
		}
		l.ID = hex.EncodeToString(id)
	}
	payload, err := json.Marshal(l)
	if err != nil {
		return "", err
	}
	body := Format + "." + base64.RawURLEncoding.EncodeToString(payload)
	token := body + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(body)))
	note := fmt.Sprintf("# Xibalba sponsor license\n# Issued to: %s\n# Valid until: %s\n# Thank you for supporting Xibalba.\n",
		strings.NewReplacer("\n", " ", "\r", " ").Replace(l.Licensee), l.Expires)
	return note + token + "\n", nil
}

// NewKeyPair returns a fresh key pair for signing licenses, both as
// hexadecimal text. The private key must be kept secret by the project; the
// public key is built into the program.
func NewKeyPair() (publicHex, privateHex string, err error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return hex.EncodeToString(public), hex.EncodeToString(private), nil
}

// ParsePrivateKey reads a private key written as hexadecimal text.
func ParsePrivateKey(text string) (ed25519.PrivateKey, error) {
	key, err := hex.DecodeString(strings.TrimSpace(text))
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("the private key must be %d hexadecimal characters", ed25519.PrivateKeySize*2)
	}
	return ed25519.PrivateKey(key), nil
}

// ParsePublicKey reads a public key written as hexadecimal text.
func ParsePublicKey(text string) (ed25519.PublicKey, error) {
	key, err := hex.DecodeString(strings.TrimSpace(text))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("the public key must be %d hexadecimal characters", ed25519.PublicKeySize*2)
	}
	return ed25519.PublicKey(key), nil
}
