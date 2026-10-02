package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MaMoja/xibalba/internal/config"
)

func TestJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	New(config.Log{Level: "info", Format: "json"}, &buf).Info("hello", "component", "test")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, buf.String())
	}
	if line["msg"] != "hello" || line["component"] != "test" {
		t.Errorf("line = %v", line)
	}
}

func TestTextFormat(t *testing.T) {
	var buf bytes.Buffer
	New(config.Log{Level: "info", Format: "text"}, &buf).Info("hello")
	if !strings.Contains(buf.String(), "msg=hello") {
		t.Errorf("output = %q, want text format", buf.String())
	}
}

func TestLevelFilters(t *testing.T) {
	var buf bytes.Buffer
	log := New(config.Log{Level: "warn", Format: "text"}, &buf)
	log.Info("quiet")
	log.Warn("loud")
	out := buf.String()
	if strings.Contains(out, "quiet") || !strings.Contains(out, "loud") {
		t.Errorf("level warn let through the wrong lines: %q", out)
	}
}
