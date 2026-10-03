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
	hits int
}

func newWebsite(t *testing.T) *website {
	t.Helper()
	w := &website{}
	w.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.last, w.host = r.Header.Clone(), r.Host
		w.hits++
		w.mu.Unlock()
		rw.Header().Set("X-From-Website", "yes")
		_, _ = fmt.Fprintf(rw, "website says hello to %s", r.URL.Path)
	}))
	t.Cleanup(w.Close)
	return w
}

func (w *website) hitCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hits
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
	for _, name := range []string{"ops", "public", "rules", "upstream"} {
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

// testRules is a small rule set used by the tests below. "rules:" must be the
// only top-level key in it.
const testRules = `rules:
  thresholds:
    - {weight: 10, action: challenge}
  list:
    - name: allow-office
      match:
        ip: ["192.0.2.0/24"]
      action: allow
    - name: block-example-bot
      match:
        user_agent: {contains: "ExampleBot"}
      action: deny
    - name: block-admin
      match:
        path: {prefix: "/admin"}
      action: deny
    - name: weigh-no-language
      match:
        header:
          Accept-Language: {present: false}
      action: weigh
      weight: 10
`

type decisions struct {
	DryRun  bool              `json:"dry_run"`
	Totals  map[string]uint64 `json:"totals"`
	Sources []struct {
		Source    string `json:"source"`
		Action    string `json:"action"`
		Reference string `json:"reference"`
		Count     uint64 `json:"count"`
	} `json:"sources"`
}

func getDecisions(t *testing.T, inst *instance) decisions {
	t.Helper()
	_, body := get(t, inst.ops+"/decisions", nil)
	var d decisions
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("/decisions is not JSON: %v\n%s", err, body)
	}
	return d
}

func (d decisions) count(source string) uint64 {
	for _, s := range d.Sources {
		if s.Source == source {
			return s.Count
		}
	}
	return 0
}

func (d decisions) reference(source string) string {
	for _, s := range d.Sources {
		if s.Source == source {
			return s.Reference
		}
	}
	return ""
}

var language = map[string]string{"Accept-Language": "en"}

func TestRulesBlockAndCount(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, testRules)

	// An ordinary visitor reaches the website.
	if resp, body := get(t, inst.public+"/", language); resp.StatusCode != 200 || !strings.Contains(body, "website says hello") {
		t.Errorf("ordinary visitor: %d %q", resp.StatusCode, body)
	}

	// A denied client gets the block page and never reaches the website.
	before := site.hitCount()
	resp, body := get(t, inst.public+"/", map[string]string{"User-Agent": "Mozilla/5.0 (compatible; ExampleBot/1.0)", "Accept-Language": "en"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("denied client: status = %d, want 403", resp.StatusCode)
	}
	if site.hitCount() != before {
		t.Error("a denied request reached the website")
	}
	if !strings.Contains(body, "This request was blocked") || resp.Header.Get("Content-Security-Policy") == "" {
		t.Errorf("denied client did not get the block page:\n%s", body)
	}

	// Differently spelled paths are blocked too.
	for _, path := range []string{"/admin", "/ADMIN/users", "//admin", "/x/../admin", "/%61dmin", "/public/..;/admin"} {
		if resp, _ := get(t, inst.public+path, language); resp.StatusCode != http.StatusForbidden {
			t.Errorf("path %s: status = %d, want 403", path, resp.StatusCode)
		}
	}

	// A forged address must not turn the client into an "office" client.
	if resp, _ := get(t, inst.public+"/admin", map[string]string{"X-Forwarded-For": "192.0.2.10", "Accept-Language": "en"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("forged X-Forwarded-For got past the allow rule: status = %d", resp.StatusCode)
	}

	d := getDecisions(t, inst)
	if d.DryRun {
		t.Error("dry_run reported although it is off")
	}
	if got := d.count("rule:block-example-bot"); got != 1 {
		t.Errorf("block-example-bot count = %d, want 1", got)
	}
	if got := d.count("rule:block-admin"); got != 7 {
		t.Errorf("block-admin count = %d, want 7", got)
	}
	if got := d.count("rule:allow-office"); got != 0 {
		t.Errorf("allow-office count = %d, want 0", got)
	}
	if d.Totals["allow"] != 1 || d.Totals["deny"] != 8 {
		t.Errorf("totals = %+v, want 1 allowed and 8 denied", d.Totals)
	}

	// The reference on the block page leads to the rule.
	if ref := d.reference("rule:block-example-bot"); ref == "" || !strings.Contains(body, ref) {
		t.Errorf("the block page does not show the reference %q of the deciding rule", ref)
	}
}

func TestBlockPageFollowsTheVisitorsLanguage(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, testRules)

	_, german := get(t, inst.public+"/admin", map[string]string{"Accept-Language": "de-DE,de;q=0.9"})
	_, english := get(t, inst.public+"/admin", map[string]string{"Accept-Language": "en-GB"})
	if !strings.Contains(german, "<h1>Diese Anfrage wurde blockiert</h1>") {
		t.Errorf("German visitor did not get the German page:\n%s", german)
	}
	if !strings.Contains(english, "<h1>This request was blocked</h1>") {
		t.Errorf("English visitor did not get the English page:\n%s", english)
	}
}

func TestTrustedProxyAddressReachesTheRules(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, "server:\n  listen: PUBLIC\n  trusted_proxies: [\"127.0.0.1\"]\n"+testRules)

	resp, _ := get(t, inst.public+"/admin", map[string]string{"X-Forwarded-For": "192.0.2.10", "Accept-Language": "en"})
	if resp.StatusCode != 200 {
		t.Errorf("office client behind the trusted proxy: status = %d, want 200", resp.StatusCode)
	}
	if got := getDecisions(t, inst).count("rule:allow-office"); got != 1 {
		t.Errorf("allow-office count = %d, want 1", got)
	}
}

func TestDryRunBlocksNothing(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, strings.Replace(testRules, "rules:\n", "rules:\n  dry_run: true\n", 1))

	for _, path := range []string{"/", "/admin", "/admin/users"} {
		if resp, _ := get(t, inst.public+path, language); resp.StatusCode != 200 {
			t.Errorf("%s: status = %d, want 200 in dry run", path, resp.StatusCode)
		}
	}
	d := getDecisions(t, inst)
	if !d.DryRun || d.Totals["deny"] != 2 || site.hitCount() != 3 {
		t.Errorf("dry run: %+v, website hits = %d; want 2 would-be denials counted, 3 requests passed", d, site.hitCount())
	}
	if !strings.Contains(inst.logs.String(), "dry run") {
		t.Errorf("the log does not say that dry run is on:\n%s", inst.logs.String())
	}
}

func TestChallengeIsCountedAndPassedOnForNow(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, testRules)

	// No Accept-Language: weight 10 reaches the challenge threshold.
	if resp, _ := get(t, inst.public+"/", nil); resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200 until the challenge exists", resp.StatusCode)
	}
	if got := getDecisions(t, inst).count("threshold:10"); got != 1 {
		t.Errorf("threshold:10 count = %d, want 1", got)
	}
	if !strings.Contains(inst.logs.String(), "challenge is not available") {
		t.Errorf("the log does not warn that the challenge is missing:\n%s", inst.logs.String())
	}
}

func TestImportedRuleFile(t *testing.T) {
	site := newWebsite(t)
	dir := t.TempDir()
	rulesPath := filepath.Join(dir, "extra.yaml")
	extra := "rules:\n  - name: block-private\n    match:\n      path: {prefix: \"/private\"}\n    action: deny\n"
	if err := os.WriteFile(rulesPath, []byte(extra), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := start(t, site.URL, fmt.Sprintf("rules:\n  files: [%q]\n", rulesPath))

	if resp, _ := get(t, inst.public+"/private/x", language); resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 from the imported rule", resp.StatusCode)
	}
	if resp, _ := get(t, inst.public+"/public", language); resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestInvalidRuleIsRejectedWithLineNumbers(t *testing.T) {
	config := "upstream:\n  url: http://127.0.0.1:1\nrules:\n  list:\n    - name: broken\n      match:\n        path: {regex: \"(unclosed\"}\n      action: deny\n"
	cmd, logs := run(t, config)
	if err := cmd.Wait(); err == nil {
		t.Fatal("xibalba started with an invalid rule")
	}
	for _, want := range []string{"line 7, rules.list[0].match.path.regex", "not valid", "fix:"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("error should contain %q:\n%s", want, logs.String())
		}
	}
}

func TestBlockPageCanBeAdapted(t *testing.T) {
	site := newWebsite(t)
	custom := `pages:
  operator: "Stadt Musterhausen"
  contact: "webmaster@musterhausen.example"
  texts:
    de:
      blocked_title: "Zugriff nicht möglich"
` + testRules
	inst := start(t, site.URL, custom)

	_, german := get(t, inst.public+"/admin", map[string]string{"Accept-Language": "de"})
	for _, want := range []string{
		"<h1>Zugriff nicht möglich</h1>",
		"Stadt Musterhausen lässt Anfragen dieser Art nicht zu.",
		"Kontakt: webmaster@musterhausen.example",
	} {
		if !strings.Contains(german, want) {
			t.Errorf("German block page is missing %q:\n%s", want, german)
		}
	}
	_, english := get(t, inst.public+"/admin", map[string]string{"Accept-Language": "en"})
	for _, want := range []string{
		"<h1>This request was blocked</h1>",
		"Stadt Musterhausen does not allow requests of this kind.",
		"Contact: webmaster@musterhausen.example",
	} {
		if !strings.Contains(english, want) {
			t.Errorf("English block page is missing %q:\n%s", want, english)
		}
	}
}
