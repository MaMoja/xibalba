// Package integration tests the real xibalba binary from the outside: it is
// built, started with a configuration file, talked to over HTTP and stopped
// with a signal, exactly as an administrator would run it.
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "xibalba-it-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "xibalba")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/xibalba")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building xibalba failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// freeAddr returns a loopback address with a port that is free right now.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

// start runs the binary with the given configuration and returns the process
// and its log output.
func start(t *testing.T, config string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "xibalba.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	cmd := exec.Command(binary, "-config", path)
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return cmd, &logs
}

func waitFor(t *testing.T, url string) *http.Response {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(url)
		if err == nil {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not answer in time: %v", url, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestHealthVersionAndGracefulShutdown(t *testing.T) {
	addr := freeAddr(t)
	cmd, logs := start(t, fmt.Sprintf("ops:\n  listen: %s\n", addr))

	resp := waitFor(t, "http://"+addr+"/healthz")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", resp.StatusCode)
	}
	var report struct {
		State      string `json:"state"`
		Components map[string]struct {
			State string `json:"state"`
		} `json:"components"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("/healthz is not JSON: %v", err)
	}
	if report.State != "ok" || report.Components["ops"].State != "ok" {
		t.Errorf("health report = %+v, want everything ok", report)
	}

	vresp, err := http.Get("http://" + addr + "/version")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vresp.Body.Close() }()
	var version struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(vresp.Body).Decode(&version); err != nil || version.Version == "" {
		t.Errorf("/version did not return a version (err %v, body %+v)", err, version)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("exit after SIGTERM: %v\nlogs:\n%s", err, logs.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("xibalba did not exit after SIGTERM\nlogs:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "xibalba stopped") {
		t.Errorf("no clean-stop log line:\n%s", logs.String())
	}
}

func TestStartFailureNamesTheComponent(t *testing.T) {
	// Occupy a port, then tell xibalba to use it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	cmd, logs := start(t, fmt.Sprintf("ops:\n  listen: %s\nlog:\n  format: text\n", ln.Addr()))
	err = cmd.Wait()
	if err == nil {
		t.Fatal("xibalba started although its port was taken")
	}
	out := logs.String()
	for _, want := range []string{"start-up failed", "ops", ln.Addr().String()} {
		if !strings.Contains(out, want) {
			t.Errorf("log should contain %q so the cause is obvious:\n%s", want, out)
		}
	}
}

func TestInvalidConfigIsRejectedWithLineNumbers(t *testing.T) {
	cmd, logs := start(t, "log:\n  level: loud\n")
	if err := cmd.Wait(); err == nil {
		t.Fatal("xibalba started with an invalid configuration")
	}
	if !strings.Contains(logs.String(), "line 2, log.level") {
		t.Errorf("error should point at the line and setting:\n%s", logs.String())
	}
}
