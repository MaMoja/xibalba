package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	for _, input := range []string{"", "   \n", "# only a comment\n"} {
		cfg, err := Parse("test.yaml", []byte(input))
		if err != nil {
			t.Fatalf("Parse(%q) returned error: %v", input, err)
		}
		if cfg != Default() {
			t.Errorf("Parse(%q) = %+v, want defaults %+v", input, cfg, Default())
		}
	}
}

func TestParseValid(t *testing.T) {
	input := `
log:
  level: debug
  format: text
ops:
  listen: "[::1]:9191"
shutdown_timeout: 30s
`
	cfg, err := Parse("test.yaml", []byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Config{
		Log:             Log{Level: "debug", Format: "text"},
		Ops:             Ops{Listen: "[::1]:9191"},
		ShutdownTimeout: 30 * time.Second,
	}
	if cfg != want {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

func TestParsePartialKeepsDefaults(t *testing.T) {
	cfg, err := Parse("test.yaml", []byte("log:\n  level: warn\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("level = %q, want warn", cfg.Log.Level)
	}
	if cfg.Log.Format != "json" || cfg.Ops.Listen != "127.0.0.1:9090" {
		t.Errorf("settings left out lost their defaults: %+v", cfg)
	}
}

// A section that is present but empty ("log:" with nothing under it) keeps the
// defaults of everything inside it.
func TestParseEmptySectionKeepsDefaults(t *testing.T) {
	cfg, err := Parse("test.yaml", []byte("log:\nops:\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != Default() {
		t.Errorf("got %+v, want defaults %+v", cfg, Default())
	}
}

func TestParseProblems(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string // substrings that must appear in the error
	}{
		{
			name:  "bad level with line",
			input: "log:\n  level: loud\n",
			want:  []string{"line 2, log.level", `"loud"`, "debug, info, warn, error"},
		},
		{
			name:  "bad format",
			input: "log:\n  format: xml\n",
			want:  []string{"line 2, log.format", "json, text"},
		},
		{
			name:  "listen without port",
			input: "ops:\n  listen: localhost\n",
			want:  []string{"line 2, ops.listen", "host:port"},
		},
		{
			name:  "listen with bad port",
			input: "ops:\n  listen: localhost:99999\n",
			want:  []string{"ops.listen", "0 to 65535"},
		},
		{
			name:  "zero timeout",
			input: "shutdown_timeout: 0s\n",
			want:  []string{"line 1, shutdown_timeout", "greater than zero"},
		},
		{
			name:  "unknown setting",
			input: "logg:\n  level: info\n",
			want:  []string{"not valid", "logg", "docs/CONFIGURATION.md"},
		},
		{
			name:  "wrong type",
			input: "shutdown_timeout: [1, 2]\n",
			want:  []string{"not valid"},
		},
		{
			name:  "broken yaml",
			input: "log: [unclosed\n",
			want:  []string{"not valid"},
		},
		{
			name:  "all problems reported together",
			input: "log:\n  level: loud\n  format: xml\nshutdown_timeout: -1s\n",
			want:  []string{"3 problems", "log.level", "log.format", "shutdown_timeout"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("test.yaml", []byte(tt.input))
			if err == nil {
				t.Fatal("expected an error, got none")
			}
			var cfgErr *Error
			if !errors.As(err, &cfgErr) {
				t.Fatalf("error is %T, want *config.Error", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "test.yaml") {
				t.Errorf("error does not name the file:\n%s", msg)
			}
			for _, w := range tt.want {
				if !strings.Contains(msg, w) {
					t.Errorf("error is missing %q:\n%s", w, msg)
				}
			}
		})
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		_, err := Load(filepath.Join(dir, "nope.yaml"))
		if err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("got %v, want a 'does not exist' error", err)
		}
		if !strings.Contains(err.Error(), "xibalba.example.yaml") {
			t.Errorf("error should tell the user what to do: %v", err)
		}
	})

	t.Run("existing file", func(t *testing.T) {
		path := filepath.Join(dir, "ok.yaml")
		if err := os.WriteFile(path, []byte("log:\n  level: error\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Log.Level != "error" {
			t.Errorf("level = %q, want error", cfg.Log.Level)
		}
	})
}

// The example file shipped in the repository must always be valid and must
// spell out the defaults, so it never drifts from the code.
func TestExampleFileMatchesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "xibalba.example.yaml"))
	if err != nil {
		t.Fatalf("xibalba.example.yaml is not valid: %v", err)
	}
	if cfg != Default() {
		t.Errorf("xibalba.example.yaml = %+v, want the defaults %+v", cfg, Default())
	}
}
