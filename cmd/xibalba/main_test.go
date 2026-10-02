package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// upstream is the one required setting; nothing in these tests connects to it.
const upstream = "upstream:\n  url: http://127.0.0.1:1\n"

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "xibalba.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunFlags(t *testing.T) {
	valid := writeConfig(t, "log:\n  level: info\n"+upstream)
	invalid := writeConfig(t, "log:\n  level: loud\n"+upstream)

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"version", []string{"-version"}, exitOK, "xibalba ", ""},
		{"check valid", []string{"-check", "-config", valid}, exitOK, "is valid", ""},
		{"check invalid", []string{"-check", "-config", invalid}, exitFailed, "", "log.level"},
		{"missing config", []string{"-config", "/nonexistent/x.yaml"}, exitFailed, "", "does not exist"},
		{"unknown flag", []string{"-nope"}, exitUsage, "", "flag provided but not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d\nstderr: %s", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestRunStopsCleanlyWhenContextEnds(t *testing.T) {
	cfg := writeConfig(t, "ops:\n  listen: 127.0.0.1:0\nserver:\n  listen: 127.0.0.1:0\n"+upstream)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // shutdown is requested before start-up finishes: must still exit cleanly

	var stdout, stderr bytes.Buffer
	if code := run(ctx, []string{"-config", cfg}, &stdout, &stderr); code != exitOK {
		t.Errorf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr.String())
	}
	for _, want := range []string{"xibalba started", "xibalba stopped"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("log is missing %q:\n%s", want, stderr.String())
		}
	}
}
