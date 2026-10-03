package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func keys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return public, private
}

var sample = License{Licensee: "Stadt Musterhausen", Sponsor: "musterhausen", Issued: "2026-10-03", Expires: "2027-10-03"}

func day(s string) time.Time {
	d, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestIssueAndParse(t *testing.T) {
	public, private := keys(t)
	text, err := Issue(private, sample)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	for _, want := range []string{"# Issued to: Stadt Musterhausen", "# Valid until: 2027-10-03", Format + "."} {
		if !strings.Contains(text, want) {
			t.Errorf("license file is missing %q:\n%s", want, text)
		}
	}
	got, err := Parse(text, public)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Licensee != sample.Licensee || got.Sponsor != sample.Sponsor || got.Expires != sample.Expires || got.Issued != sample.Issued || len(got.ID) != 16 {
		t.Errorf("got %+v", got)
	}
	// White space and Windows line endings around the text do no harm.
	if _, err := Parse("\r\n  "+strings.ReplaceAll(text, "\n", "\r\n")+"\r\n\r\n", public); err != nil {
		t.Errorf("Parse with CRLF and padding: %v", err)
	}
}

func TestParseRejects(t *testing.T) {
	public, private := keys(t)
	otherPublic, otherPrivate := keys(t)
	_ = otherPublic
	text, _ := Issue(private, sample)
	token := strings.TrimSpace(text[strings.LastIndex(strings.TrimSpace(text), "\n")+1:])
	parts := strings.Split(token, ".")

	// The same statement with a later expiry, keeping the old signature.
	forged := base64.RawURLEncoding.EncodeToString([]byte(`{"id":"x","licensee":"Stadt Musterhausen","issued":"2026-10-03","expires":"2099-01-01"}`))
	byOther, _ := Issue(otherPrivate, sample)

	tests := []struct {
		name string
		text string
		want string
	}{
		{"empty", "", "holds no license"},
		{"only comments", "# nothing here\n\n", "holds no license"},
		{"not a license", "hello world", "not a license"},
		{"wrong version", "xibalba-license-v9." + parts[1] + "." + parts[2], "not a license"},
		{"two parts", parts[0] + "." + parts[1], "not a license"},
		{"two licenses", token + "\n" + token, "more than one line"},
		{"signature unreadable", parts[0] + "." + parts[1] + ".!!!", "damaged"},
		{"signature cut short", token[:len(token)-10], "damaged"},
		{"statement changed", parts[0] + "." + forged + "." + parts[2], "not genuine"},
		{"signed with another key", byOther, "not genuine"},
		{"one character changed", strings.Replace(token, parts[1][:4], "AAAA", 1), "not genuine"},
		{"huge", strings.Repeat("a", maxLength+1), "too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.text, public)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Parse = %v, want an error containing %q", err, tt.want)
			}
		})
	}

	t.Run("no key", func(t *testing.T) {
		if _, err := Parse(text, nil); err == nil {
			t.Error("a license was accepted without a key to check it against")
		}
	})
}

func TestState(t *testing.T) {
	l := License{Licensee: "x", Issued: "2026-10-03", Expires: "2027-10-03"}
	tests := []struct {
		at     string
		want   State
		usable bool
	}{
		{"2026-10-03 00:00", Valid, true},
		{"2027-10-03 23:59", Valid, true}, // the expiry day itself still counts
		{"2027-10-04 00:00", Grace, true}, // the day after: grace begins
		{"2027-11-02 23:59", Grace, true}, // last minute of the 30 days
		{"2027-11-03 00:00", Expired, false},
		{"2030-01-01 00:00", Expired, false},
	}
	for _, tt := range tests {
		if got := l.State(day(tt.at)); got != tt.want || l.Usable(day(tt.at)) != tt.usable {
			t.Errorf("at %s: state %s, usable %v; want %s, %v", tt.at, got, l.Usable(day(tt.at)), tt.want, tt.usable)
		}
	}
}

func TestIssueRejects(t *testing.T) {
	_, private := keys(t)
	tests := map[string]License{
		"no licensee":          {Issued: "2026-10-03", Expires: "2027-10-03"},
		"bad date":             {Licensee: "x", Issued: "3.10.2026", Expires: "2027-10-03"},
		"expires before issue": {Licensee: "x", Issued: "2026-10-03", Expires: "2026-01-01"},
	}
	for name, l := range tests {
		if _, err := Issue(private, l); err == nil {
			t.Errorf("%s: a license was issued", name)
		}
	}
	if _, err := Issue(private[:10], sample); err == nil {
		t.Error("a license was issued with a malformed private key")
	}
}

func TestKeyPairRoundTrip(t *testing.T) {
	publicHex, privateHex, err := NewKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	public, err := ParsePublicKey(publicHex + "\n")
	if err != nil {
		t.Fatal(err)
	}
	private, err := ParsePrivateKey(privateHex + "\n")
	if err != nil {
		t.Fatal(err)
	}
	text, err := Issue(private, sample)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(text, public); err != nil {
		t.Errorf("a license issued with a new key pair does not verify: %v", err)
	}
	if _, err := ParsePublicKey("abc"); err == nil {
		t.Error("a short public key was accepted")
	}
	if _, err := ParsePrivateKey("abc"); err == nil {
		t.Error("a short private key was accepted")
	}
}

// The key built into the program must be a well-formed key, and the
// placeholder of all zeros must never ship.
func TestBuiltInPublicKey(t *testing.T) {
	key, err := PublicKey()
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}
	if strings.Trim(publicKeyHex, "0") == "" {
		t.Error("the built-in public key is still the all-zero placeholder")
	}
	// A license signed with some other key must not verify against it.
	_, private := keys(t)
	text, _ := Issue(private, sample)
	if _, err := Parse(text, key); err == nil {
		t.Error("a license signed with a random key verified against the built-in key")
	}
}
