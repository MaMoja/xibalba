package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MaMoja/xibalba/internal/license"
)

// project is a stand-in for the Xibalba project in tests: it has a key pair
// of its own, so tests can issue licenses without the real private key.
type project struct {
	env     Env
	private ed25519.PrivateKey
}

func newProject(t *testing.T) project {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return project{
		env:     Env{Now: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), LicenseKey: public},
		private: private,
	}
}

// issue returns a license file valid until the given day.
func (p project) issue(t *testing.T, expires string) string {
	t.Helper()
	text, err := license.Issue(p.private, license.License{Licensee: "Stadt Musterhausen", Issued: "2026-01-01", Expires: expires})
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// loadLicensed loads content as a configuration file next to a valid license.
// The license setting is appended, so line numbers in content stay as written.
func loadLicensed(t *testing.T, content string) (Config, error) {
	t.Helper()
	p := newProject(t)
	path := writeFiles(t, map[string]string{
		"xibalba.yaml":    content + "\nlicense:\n  file: sponsor.license\n",
		"sponsor.license": p.issue(t, "2027-10-03"),
	})
	return LoadWith(path, p.env)
}

const sponsorPages = `
pages:
  operator: "Stadt Musterhausen"
  contact: "webmaster@musterhausen.example"
  default_language: en
  attribution: false
  texts:
    de:
      blocked_title: "Zugriff nicht möglich"
`

func TestSponsorSettingsNeedALicense(t *testing.T) {
	_, err := Parse("test.yaml", []byte(minimal+sponsorPages))
	if err == nil {
		t.Fatal("sponsor settings were accepted without a license")
	}
	msg := err.Error()
	for _, want := range []string{
		"3 problems",
		"line 5, pages.operator: this setting needs a sponsor license",
		"line 8, pages.attribution: this setting needs a sponsor license",
		"line 9, pages.texts: this setting needs a sponsor license",
		"docs/SPONSORS.md",
		"remove this setting",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error is missing %q:\n%s", want, msg)
		}
	}
	// The contact line and the default language are free.
	for _, free := range []string{"pages.contact", "pages.default_language"} {
		if strings.Contains(msg, free) {
			t.Errorf("%s was reported as needing a license:\n%s", free, msg)
		}
	}
}

func TestFreeSettingsWorkWithoutALicense(t *testing.T) {
	cfg, err := Parse("test.yaml", []byte(minimal+"pages:\n  contact: \"webmaster@example.org\"\n  default_language: en\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	opts := cfg.PageOptions()
	if opts.Contact != "webmaster@example.org" || opts.DefaultLanguage != "en" || opts.HideAttribution {
		t.Errorf("page options = %+v", opts)
	}
	if cfg.License.Usable() || cfg.License.Info != nil {
		t.Errorf("a license appeared from nowhere: %+v", cfg.License)
	}
}

func TestSponsorSettingsWithALicense(t *testing.T) {
	cfg, err := loadLicensed(t, minimal+sponsorPages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.License.Info == nil || cfg.License.Info.Licensee != "Stadt Musterhausen" || cfg.License.State != license.Valid || !cfg.License.Usable() {
		t.Fatalf("license = %+v", cfg.License)
	}
	opts := cfg.PageOptions()
	if opts.Operator != "Stadt Musterhausen" || !opts.HideAttribution || opts.Texts["de"]["blocked_title"] != "Zugriff nicht möglich" ||
		opts.Contact != "webmaster@musterhausen.example" || opts.DefaultLanguage != "en" {
		t.Errorf("page options = %+v", opts)
	}
}

// A license past its term must never stop a website. Within the grace period
// it still works; after that the pages return to their standard form.
func TestExpiredLicense(t *testing.T) {
	tests := []struct {
		name       string
		expires    string
		wantState  license.State
		wantUsable bool
	}{
		{"valid", "2026-12-31", license.Valid, true},
		{"expires today", "2026-10-03", license.Valid, true},
		{"expired last week: grace", "2026-09-26", license.Grace, true},
		{"expired two months ago", "2026-08-01", license.Expired, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProject(t)
			path := writeFiles(t, map[string]string{
				"xibalba.yaml":    minimal + sponsorPages + "license:\n  file: sponsor.license\n",
				"sponsor.license": p.issue(t, tt.expires),
			})
			cfg, err := LoadWith(path, p.env)
			if err != nil {
				t.Fatalf("an expired license must not be a configuration error: %v", err)
			}
			if cfg.License.State != tt.wantState || cfg.License.Usable() != tt.wantUsable {
				t.Errorf("state %q, usable %v; want %q, %v", cfg.License.State, cfg.License.Usable(), tt.wantState, tt.wantUsable)
			}
			opts := cfg.PageOptions()
			if tt.wantUsable {
				if opts.Operator == "" || !opts.HideAttribution || len(opts.Texts) == 0 {
					t.Errorf("sponsor settings not applied with a usable license: %+v", opts)
				}
			} else {
				if opts.Operator != "" || opts.HideAttribution || len(opts.Texts) != 0 {
					t.Errorf("sponsor settings still applied after the license ran out: %+v", opts)
				}
				if opts.Contact == "" || opts.DefaultLanguage != "en" {
					t.Errorf("free settings were lost with the license: %+v", opts)
				}
			}
		})
	}
}

func TestLicenseFileProblems(t *testing.T) {
	p := newProject(t)
	other := newProject(t)
	good := p.issue(t, "2027-10-03")

	tests := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"missing file", nil, []string{"line 4, license.file", `"sponsor.license"`, "does not exist", "To run without a license"}},
		{"not a license", map[string]string{"sponsor.license": "hello\n"}, []string{"line 4, license.file", "not a usable license", "not a license"}},
		{"signed by someone else", map[string]string{"sponsor.license": other.issue(t, "2027-10-03")}, []string{"license.file", "not genuine"}},
		{"changed by hand", map[string]string{"sponsor.license": strings.Replace(good, "eyJ", "eyK", 1)}, []string{"license.file", "not genuine"}},
		{"a directory", map[string]string{"sponsor.license/x": "y"}, []string{"license.file", "not a regular file"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"xibalba.yaml": minimal + "license:\n  file: sponsor.license\n"}
			for name, content := range tt.files {
				files[name] = content
			}
			_, err := LoadWith(writeFiles(t, files), p.env)
			if err == nil {
				t.Fatal("expected an error, got none")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error is missing %q:\n%v", w, err)
				}
			}
		})
	}
}

// A broken license file is reported once, at license.file, and not again for
// every setting that would have needed it.
func TestBrokenLicenseIsReportedOnce(t *testing.T) {
	p := newProject(t)
	path := writeFiles(t, map[string]string{
		"xibalba.yaml":    minimal + sponsorPages + "license:\n  file: sponsor.license\n",
		"sponsor.license": "garbage\n",
	})
	_, err := LoadWith(path, p.env)
	if err == nil || !strings.Contains(err.Error(), "1 problem") || !strings.Contains(err.Error(), "license.file") {
		t.Errorf("got %v, want exactly one problem at license.file", err)
	}
}

func TestLicenseWithoutSponsorSettingsIsFine(t *testing.T) {
	cfg, err := loadLicensed(t, minimal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts := cfg.PageOptions(); opts.HideAttribution || opts.Operator != "" {
		t.Errorf("a license alone changed the pages: %+v", opts)
	}
	if filepath.Base(cfg.License.Path) != "sponsor.license" {
		t.Errorf("license path = %q", cfg.License.Path)
	}
}

// The real program checks licenses against the key built into it. A license
// from a made-up project must not pass that check.
func TestDefaultEnvUsesTheBuiltInKey(t *testing.T) {
	p := newProject(t)
	path := writeFiles(t, map[string]string{
		"xibalba.yaml":    minimal + "license:\n  file: sponsor.license\n",
		"sponsor.license": p.issue(t, "2099-01-01"),
	})
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "not genuine") {
		t.Errorf("got %v, want the license rejected by the built-in key", err)
	}
}
