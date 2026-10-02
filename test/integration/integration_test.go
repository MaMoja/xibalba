// Package integration tests the real xibalba binary from the outside: it is
// built, started with a configuration file, put in front of a fake website,
// talked to over HTTP and stopped with a signal, exactly as an administrator
// would run it.
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

// website is a fake upstream that records the last request it received.
type website struct {
	*httptest.Server
	mu   sync.Mutex
	last http.Header
	host string
}

func newWebsite(t *testing.T) *website {
	t.Helper()
	w := &website{}
	w.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.last, w.host = r.Header.Clone(), r.Host
		w.mu.Unlock()
		rw.Header().Set("X-From-Website", "yes")
		_, _ = fmt.Fprintf(rw, "website says hello to %s", r.URL.Path)
	}))
	t.Cleanup(w.Close)
	return w
}

func (w *website) lastHeader(name string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.last.Values(name), " | ")
}

// logBuffer collects the process's log. The process writes to it from another
// goroutine while tests read it, so access is guarded.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// instance is one running xibalba process.
type instance struct {
	cmd    *exec.Cmd
	logs   *logBuffer
	public string // base URL of the public listener
	ops    string // base URL of the operations listener
}

// start runs the binary in front of upstreamURL. extra is appended to the
// configuration file as further top-level YAML.
func start(t *testing.T, upstreamURL, extra string) *instance {
	t.Helper()
	public, ops := freeAddr(t), freeAddr(t)
	config := fmt.Sprintf("upstream:\n  url: %s\nops:\n  listen: %s\nlog:\n  format: text\n", upstreamURL, ops)
	if !strings.Contains(extra, "server:") {
		config += fmt.Sprintf("server:\n  listen: %s\n", public)
	}
	config += strings.ReplaceAll(extra, "PUBLIC", public)

	inst := &instance{public: "http://" + public, ops: "http://" + ops}
	inst.cmd, inst.logs = run(t, config)
	resp := waitFor(t, inst.ops+"/healthz")
	_ = resp.Body.Close()
	return inst
}

// run starts the binary with the given configuration text.
func run(t *testing.T, config string) (*exec.Cmd, *logBuffer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "xibalba.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := &logBuffer{}
	cmd := exec.Command(binary, "-config", path)
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return cmd, logs
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

type healthReport struct {
	State      string `json:"state"`
	Components map[string]struct {
		State  string `json:"state"`
		Detail string `json:"detail"`
	} `json:"components"`
}

func health(t *testing.T, inst *instance) (int, healthReport) {
	t.Helper()
	resp, err := http.Get(inst.ops + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var report healthReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("/healthz is not JSON: %v", err)
	}
	return resp.StatusCode, report
}

func get(t *testing.T, url string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestProxiesToTheWebsite(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, "")

	resp, body := get(t, inst.public+"/some/page", nil)
	if resp.StatusCode != http.StatusOK || body != "website says hello to /some/page" {
		t.Errorf("got %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("X-From-Website") != "yes" {
		t.Error("the website's response header was lost")
	}

	code, report := health(t, inst)
	if code != http.StatusOK || report.State != "ok" {
		t.Errorf("health = %d %+v, want everything ok", code, report)
	}
	for _, name := range []string{"ops", "public", "upstream"} {
		if report.Components[name].State != "ok" {
			t.Errorf("component %q = %+v, want ok", name, report.Components[name])
		}
	}
}

func TestSpoofedForwardingHeadersAreNotBelieved(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, "")

	get(t, inst.public+"/", map[string]string{"X-Forwarded-For": "198.51.100.7", "X-Real-IP": "198.51.100.7"})
	if got := site.lastHeader("X-Forwarded-For"); got != "127.0.0.1" {
		t.Errorf("website saw X-Forwarded-For = %q, want only the real peer 127.0.0.1", got)
	}
	if got := site.lastHeader("X-Real-Ip"); got != "127.0.0.1" {
		t.Errorf("website saw X-Real-IP = %q, want 127.0.0.1", got)
	}
}

func TestTrustedProxyHeadersAreBelieved(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, "server:\n  listen: PUBLIC\n  trusted_proxies:\n    - 127.0.0.1\n")

	get(t, inst.public+"/", map[string]string{"X-Forwarded-For": "198.51.100.7"})
	if got := site.lastHeader("X-Forwarded-For"); got != "198.51.100.7, 127.0.0.1" {
		t.Errorf("website saw X-Forwarded-For = %q", got)
	}
	if got := site.lastHeader("X-Real-Ip"); got != "198.51.100.7" {
		t.Errorf("website saw X-Real-IP = %q, want the real client", got)
	}
}

func TestWebsiteDownIsReportedButXibalbaStaysUp(t *testing.T) {
	dead := freeAddr(t) // nothing listens here
	inst := start(t, "http://"+dead, "")

	resp, body := get(t, inst.public+"/", nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	if strings.Contains(body, dead) {
		t.Errorf("the error page reveals the internal address:\n%s", body)
	}

	code, report := health(t, inst)
	if code != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200: Xibalba itself still works", code)
	}
	up := report.Components["upstream"]
	if report.State != "degraded" || up.State != "degraded" || !strings.Contains(up.Detail, dead) {
		t.Errorf("health = %+v, want degraded with the upstream named", report)
	}
	if report.Components["public"].State != "ok" {
		t.Errorf("public listener = %+v, want ok", report.Components["public"])
	}
	if !strings.Contains(inst.logs.String(), "component=upstream") {
		t.Errorf("the log does not attribute the failure to the upstream component:\n%s", inst.logs.String())
	}
}

func TestVersionAndGracefulShutdown(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, "")

	_, body := get(t, inst.ops+"/version", nil)
	var version struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(body), &version); err != nil || version.Version == "" {
		t.Errorf("/version did not return a version: %q (err %v)", body, err)
	}

	if err := inst.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- inst.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("exit after SIGTERM: %v\nlogs:\n%s", err, inst.logs.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("xibalba did not exit after SIGTERM\nlogs:\n%s", inst.logs.String())
	}
	if !strings.Contains(inst.logs.String(), "xibalba stopped") {
		t.Errorf("no clean-stop log line:\n%s", inst.logs.String())
	}
}

func TestStartFailureNamesTheComponent(t *testing.T) {
	// Occupy a port, then tell xibalba to use it for the public listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	config := fmt.Sprintf("upstream:\n  url: http://127.0.0.1:1\nserver:\n  listen: %s\nops:\n  listen: %s\nlog:\n  format: text\n",
		ln.Addr(), freeAddr(t))
	cmd, logs := run(t, config)
	if err := cmd.Wait(); err == nil {
		t.Fatal("xibalba started although its port was taken")
	}
	out := logs.String()
	for _, want := range []string{"start-up failed", `component \"public\"`, ln.Addr().String()} {
		if !strings.Contains(out, want) {
			t.Errorf("log should contain %q so the cause is obvious:\n%s", want, out)
		}
	}
	// The operations listener had already started and must have been stopped again.
	if !strings.Contains(out, "component stopped") {
		t.Errorf("components that had started were not rolled back:\n%s", out)
	}
}

func TestInvalidConfigIsRejectedWithLineNumbers(t *testing.T) {
	cmd, logs := run(t, "log:\n  level: loud\nupstream:\n  url: http://127.0.0.1:1\n")
	if err := cmd.Wait(); err == nil {
		t.Fatal("xibalba started with an invalid configuration")
	}
	if !strings.Contains(logs.String(), "line 2, log.level") {
		t.Errorf("error should point at the line and setting:\n%s", logs.String())
	}
}
