// Package integration tests the real xibalba binary from the outside: it is
// built, started with a configuration file, put in front of a fake website,
// talked to over HTTP and stopped with a signal, exactly as an administrator
// would run it.
package integration

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math/bits"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/MaMoja/xibalba/internal/geo/geotest"
	"github.com/MaMoja/xibalba/internal/license"
)

var binary string

// projectKey stands in for the project's license key: the test binary is
// built to trust it, so the tests can issue sponsor licenses.
var projectKey ed25519.PrivateKey

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "xibalba-it-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	projectKey = private
	binary = filepath.Join(dir, "xibalba")
	build := exec.Command("go", "build",
		"-ldflags", "-X github.com/MaMoja/xibalba/internal/license.publicKeyHex="+hex.EncodeToString(public),
		"-o", binary, "../../cmd/xibalba")
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
		// Like a real website: pages are HTML, the rest is something else.
		switch {
		case strings.HasSuffix(r.URL.Path, ".png"):
			rw.Header().Set("Content-Type", "image/png")
		case strings.HasPrefix(r.URL.Path, "/api/"):
			rw.Header().Set("Content-Type", "application/json")
		default:
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
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
	DryRun    bool              `json:"dry_run"`
	Totals    map[string]uint64 `json:"totals"`
	Challenge struct {
		Served uint64 `json:"served"`
		Passed uint64 `json:"passed"`
		Solved uint64 `json:"solved"`
		Failed uint64 `json:"failed"`
	} `json:"challenge"`
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

// challengeRules challenges everything under /wiki and lets the rest through.
const challengeRules = `rules:
  list:
    - name: challenge-wiki
      match:
        path: {prefix: "/wiki"}
      action: challenge
`

var (
	tokenRE      = regexp.MustCompile(`name="token" value="([^"]+)"`)
	returnRE     = regexp.MustCompile(`name="return" value="([^"]*)"`)
	nonceRE      = regexp.MustCompile(`data-nonce="([0-9a-f]+)"`)
	difficultyRE = regexp.MustCompile(`data-difficulty="([0-9]+)"`)
)

// visitor is a client with a cookie jar that does not follow redirects by
// itself, so every step of the challenge can be inspected.
type visitor struct {
	t      *testing.T
	client *http.Client
	agent  string
}

func newVisitor(t *testing.T) *visitor {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &visitor{t: t, agent: "Mozilla/5.0 (integration test)", client: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (v *visitor) do(method, target string, form url.Values) (*http.Response, string) {
	v.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		v.t.Fatal(err)
	}
	req.Header.Set("User-Agent", v.agent)
	req.Header.Set("Accept-Language", "en")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := v.client.Do(req)
	if err != nil {
		v.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp, string(data)
}

// task extracts the challenge from a challenge page.
type task struct {
	token, ret, nonce string
	difficulty        int
}

func parseTask(t *testing.T, page string) task {
	t.Helper()
	find := func(re *regexp.Regexp) string {
		m := re.FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("the page is not a challenge page (no match for %s):\n%s", re, page)
		}
		return html.UnescapeString(m[1])
	}
	difficulty, _ := strconv.Atoi(find(difficultyRE))
	return task{token: find(tokenRE), ret: find(returnRE), nonce: find(nonceRE), difficulty: difficulty}
}

// solve does the proof of work the way the page's script does.
func (k task) solve() string {
	for n := 0; ; n++ {
		s := strconv.Itoa(n)
		sum := sha256.Sum256([]byte(k.nonce + s))
		first := uint32(sum[0])<<24 | uint32(sum[1])<<16 | uint32(sum[2])<<8 | uint32(sum[3])
		if bits.LeadingZeros32(first) >= k.difficulty {
			return s
		}
	}
}

func (k task) answer(method, solution string) url.Values {
	return url.Values{"token": {k.token}, "return": {k.ret}, "method": {method}, "solution": {solution}}
}

func TestChallengeWithProofOfWork(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, "challenge:\n  difficulty: 10\n"+challengeRules)
	v := newVisitor(t)

	// Unchallenged paths are untouched.
	if resp, _ := v.do("GET", inst.public+"/", nil); resp.StatusCode != 200 {
		t.Fatalf("unchallenged path: status %d", resp.StatusCode)
	}

	// First visit: the challenge page, not the website.
	before := site.hitCount()
	resp, page := v.do("GET", inst.public+"/wiki/Start?x=1", nil)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, "A quick security check") {
		t.Fatalf("first visit: status %d\n%s", resp.StatusCode, page)
	}
	if site.hitCount() != before {
		t.Error("the website was contacted before the challenge was passed")
	}
	k := parseTask(t, page)
	if k.difficulty != 10 || k.ret != "/wiki/Start?x=1" {
		t.Errorf("task = %+v", k)
	}

	// A wrong answer gets a new task, no pass.
	resp, page = v.do("POST", inst.public+"/.xibalba/verify", k.answer("pow", "x"))
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, "has been started again") || len(resp.Cookies()) != 0 {
		t.Fatalf("wrong answer: status %d, %d cookies\n%s", resp.StatusCode, len(resp.Cookies()), page)
	}
	k = parseTask(t, page)

	// The right answer gets the pass and a redirect back.
	resp, _ = v.do("POST", inst.public+"/.xibalba/verify", k.answer("pow", k.solve()))
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/wiki/Start?x=1" {
		t.Fatalf("right answer: status %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "xibalba-pass" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("pass cookie: %+v", cookies)
	}

	// With the pass the website answers, and never sees the pass.
	resp, body := v.do("GET", inst.public+"/wiki/Start?x=1", nil)
	if resp.StatusCode != 200 || body != "website says hello to /wiki/Start" {
		t.Fatalf("with the pass: status %d, body %q", resp.StatusCode, body)
	}
	if got := site.lastHeader("Cookie"); got != "" {
		t.Errorf("the website received the cookie %q", got)
	}

	// Another client is not helped by this client's pass.
	other := newVisitor(t)
	other.agent = "curl/8.0"
	other.client.Jar.SetCookies(mustParse(t, inst.public), cookies)
	if resp, _ := other.do("GET", inst.public+"/wiki/Start", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a copied pass worked for another client: status %d", resp.StatusCode)
	}

	d := getDecisions(t, inst)
	if d.Challenge.Served != 2 || d.Challenge.Passed != 1 || d.Challenge.Solved != 1 || d.Challenge.Failed != 1 {
		t.Errorf("challenge counters = %+v, want served 2, passed 1, solved 1, failed 1", d.Challenge)
	}
}

func TestChallengeWithoutJavaScript(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, "challenge:\n  wait: 1s\n"+challengeRules)
	v := newVisitor(t)

	_, page := v.do("GET", inst.public+"/wiki/Start", nil)
	if !strings.Contains(page, `<button type="submit">Continue</button>`) {
		t.Fatalf("the page offers no button:\n%s", page)
	}
	k := parseTask(t, page)

	// Pressed at once: too early.
	resp, page := v.do("POST", inst.public+"/.xibalba/verify", k.answer("button", ""))
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, "a little too fast") {
		t.Fatalf("early press: status %d\n%s", resp.StatusCode, page)
	}
	k = parseTask(t, page)

	time.Sleep(1200 * time.Millisecond)
	resp, _ = v.do("POST", inst.public+"/.xibalba/verify", k.answer("button", ""))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("after waiting: status %d", resp.StatusCode)
	}
	if resp, _ := v.do("GET", inst.public+"/wiki/Start", nil); resp.StatusCode != 200 {
		t.Errorf("with the pass: status %d", resp.StatusCode)
	}
}

func TestChallengeCanRequireJavaScript(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, "challenge:\n  no_javascript: deny\n  wait: 1s\n"+challengeRules)
	v := newVisitor(t)

	_, page := v.do("GET", inst.public+"/wiki/Start", nil)
	if strings.Contains(page, "<button") || !strings.Contains(page, "JavaScript must be switched on") {
		t.Fatalf("the page should ask for JavaScript and offer no button:\n%s", page)
	}
	k := parseTask(t, page)
	time.Sleep(1200 * time.Millisecond)
	if resp, _ := v.do("POST", inst.public+"/.xibalba/verify", k.answer("button", "")); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a button answer was accepted although the button is off: status %d", resp.StatusCode)
	}
}

func TestOpenRedirectIsRefused(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, "challenge:\n  difficulty: 8\n"+challengeRules)
	v := newVisitor(t)

	_, page := v.do("GET", inst.public+"/wiki/Start", nil)
	k := parseTask(t, page)
	form := k.answer("pow", k.solve())
	form.Set("return", "https://evil.example/phish")
	resp, _ := v.do("POST", inst.public+"/.xibalba/verify", form)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Errorf("status %d, Location %q; the redirect must stay on this website", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestPassSurvivesRestartOnlyWithKeyFile(t *testing.T) {
	site := newWebsite(t)
	keyFile := filepath.Join(t.TempDir(), "xibalba.key")

	pass := func(inst *instance) []*http.Cookie {
		v := newVisitor(t)
		_, page := v.do("GET", inst.public+"/wiki/Start", nil)
		k := parseTask(t, page)
		resp, _ := v.do("POST", inst.public+"/.xibalba/verify", k.answer("pow", k.solve()))
		if len(resp.Cookies()) != 1 {
			t.Fatalf("no pass: status %d", resp.StatusCode)
		}
		return resp.Cookies()
	}
	visitWith := func(inst *instance, cookies []*http.Cookie) int {
		v := newVisitor(t)
		v.client.Jar.SetCookies(mustParse(t, inst.public), cookies)
		resp, _ := v.do("GET", inst.public+"/wiki/Start", nil)
		return resp.StatusCode
	}
	stop := func(inst *instance) {
		_ = inst.cmd.Process.Signal(syscall.SIGTERM)
		_ = inst.cmd.Wait()
	}

	t.Run("with a key file", func(t *testing.T) {
		config := fmt.Sprintf("challenge:\n  difficulty: 8\n  key_file: %q\n", keyFile) + challengeRules
		first := start(t, site.URL, config)
		cookies := pass(first)
		if !strings.Contains(first.logs.String(), "created a new signing key") {
			t.Errorf("the log does not say that a key was created:\n%s", first.logs.String())
		}
		info, err := os.Stat(keyFile)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("key file: %v, mode %v; want it created with mode 600", err, info)
		}
		stop(first)

		second := start(t, site.URL, config)
		if got := visitWith(second, cookies); got != 200 {
			t.Errorf("after a restart with the same key file: status %d, want 200", got)
		}
		if strings.Contains(second.logs.String(), "created a new signing key") {
			t.Error("the second start created a new key instead of using the stored one")
		}
	})

	t.Run("without a key file", func(t *testing.T) {
		config := "challenge:\n  difficulty: 8\n" + challengeRules
		first := start(t, site.URL, config)
		cookies := pass(first)
		if !strings.Contains(first.logs.String(), "no challenge.key_file is set") {
			t.Errorf("the log does not warn about the missing key file:\n%s", first.logs.String())
		}
		stop(first)

		second := start(t, site.URL, config)
		if got := visitWith(second, cookies); got != http.StatusForbidden {
			t.Errorf("after a restart without a key file: status %d, want a new challenge", got)
		}
	})
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
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

// licenseFile writes a sponsor license valid until the given day and
// returns its path.
func licenseFile(t *testing.T, expires string) string {
	t.Helper()
	text, err := license.Issue(projectKey, license.License{Licensee: "Stadt Musterhausen", Issued: "2026-01-01", Expires: expires})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sponsor.license")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const customPages = `pages:
  operator: "Stadt Musterhausen"
  contact: "webmaster@musterhausen.example"
  attribution: false
  texts:
    de:
      blocked_title: "Zugriff nicht möglich"
`

func TestAttributionIsShownWithoutALicense(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, "pages:\n  contact: \"webmaster@musterhausen.example\"\n"+testRules)

	_, german := get(t, inst.public+"/admin", map[string]string{"Accept-Language": "de"})
	for _, want := range []string{
		"<footer>",
		`Geschützt durch <a href="https://github.com/MaMoja/xibalba" rel="noopener noreferrer">Xibalba</a>`,
		`<a href="https://github.com/sponsors/MaMoja" rel="noopener noreferrer">Projekt unterstützen</a>`,
		"Der Betreiber dieser Website lässt Anfragen dieser Art nicht zu.",
		"Kontakt: webmaster@musterhausen.example", // the contact line is free
	} {
		if !strings.Contains(german, want) {
			t.Errorf("block page without a license is missing %q:\n%s", want, german)
		}
	}
	code, report := health(t, inst)
	if _, listed := report.Components["license"]; code != 200 || listed {
		t.Errorf("health lists a license although none is configured: %+v", report)
	}
}

func TestSponsorSettingsAreRefusedWithoutALicense(t *testing.T) {
	cmd, logs := run(t, "upstream:\n  url: http://127.0.0.1:1\n"+customPages)
	if err := cmd.Wait(); err == nil {
		t.Fatal("xibalba started with sponsor settings and no license")
	}
	for _, want := range []string{"pages.operator: this setting needs a sponsor license", "pages.attribution", "pages.texts", "docs/SPONSORS.md"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("error should contain %q:\n%s", want, logs.String())
		}
	}
}

func TestBlockPageCanBeAdaptedWithALicense(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, fmt.Sprintf("license:\n  file: %q\n", licenseFile(t, "2099-01-01"))+customPages+testRules)

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
	for _, page := range []string{german, english} {
		if strings.Contains(page, "<footer") || strings.Contains(page, "github.com") {
			t.Errorf("the Xibalba line is still shown although attribution is off:\n%s", page)
		}
	}
	_, report := health(t, inst)
	if report.Components["license"].State != "ok" {
		t.Errorf("license health = %+v, want ok", report.Components["license"])
	}
	if !strings.Contains(inst.logs.String(), `msg="sponsor license"`) || !strings.Contains(inst.logs.String(), "Stadt Musterhausen") {
		t.Errorf("the log does not name the license:\n%s", inst.logs.String())
	}
}

// A license that ran out must never take the website down. Xibalba starts,
// the pages return to their standard form, and log and health say why.
func TestExpiredLicenseFallsBackToTheStandardPages(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, fmt.Sprintf("license:\n  file: %q\n", licenseFile(t, "2026-01-31"))+customPages+testRules)

	_, page := get(t, inst.public+"/admin", map[string]string{"Accept-Language": "de"})
	for _, want := range []string{
		"<h1>Diese Anfrage wurde blockiert</h1>",
		"Der Betreiber dieser Website lässt Anfragen dieser Art nicht zu.",
		"Geschützt durch",
		"Kontakt: webmaster@musterhausen.example",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page with an expired license is missing %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "Stadt Musterhausen") || strings.Contains(page, "Zugriff nicht möglich") {
		t.Errorf("sponsor wording is still shown after the license ran out:\n%s", page)
	}

	code, report := health(t, inst)
	lic := report.Components["license"]
	if code != 200 || lic.State != "degraded" || !strings.Contains(lic.Detail, "expired on 2026-01-31") {
		t.Errorf("health = %d %+v; want 200 with the license degraded and the date named", code, lic)
	}
	for _, want := range []string{"the sponsor license has expired", "pages.operator, pages.texts, pages.attribution"} {
		if !strings.Contains(inst.logs.String(), want) {
			t.Errorf("the log should contain %q:\n%s", want, inst.logs.String())
		}
	}
}

func TestForgedLicenseIsRefused(t *testing.T) {
	_, stranger, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	text, err := license.Issue(stranger, license.License{Licensee: "Nobody", Issued: "2026-01-01", Expires: "2099-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "forged.license")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, logs := run(t, fmt.Sprintf("upstream:\n  url: http://127.0.0.1:1\nlicense:\n  file: %q\n", path))
	if err := cmd.Wait(); err == nil {
		t.Fatal("xibalba started with a license it did not issue")
	}
	if !strings.Contains(logs.String(), "license.file") || !strings.Contains(logs.String(), "not genuine") {
		t.Errorf("error should explain the license problem:\n%s", logs.String())
	}
}

// --- crawlers ---------------------------------------------------------------

// crawlerSetup starts Xibalba with two crawlers of its own whose addresses
// are published by a local server, behind a trusted proxy so the test can
// arrive from any address.
func crawlerSetup(t *testing.T, list string) (*instance, *website) {
	t.Helper()
	lists := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, list) }))
	t.Cleanup(lists.Close)
	dir := t.TempDir()
	defs := filepath.Join(dir, "crawlers.yaml")
	content := `operator: Example
source: https://example.org/bots
checked: 2026-10-03
crawlers:
  - name: ExSearch
    class: ai-search
    user_agent: ExSearch
    purpose: Test crawler.
    verify: {ranges_url: "` + lists.URL + `/list.json"}
  - name: ExTrain
    class: training
    user_agent: ExTrain
    purpose: Test crawler.
    verify: {ranges_url: "` + lists.URL + `/list.json"}
`
	if err := os.WriteFile(defs, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	site := newWebsite(t)
	inst := start(t, site.URL, `server:
  listen: PUBLIC
  trusted_proxies: ["127.0.0.1"]
crawlers:
  builtin: false
  cache_dir: `+dir+`
  files: ["`+defs+`"]
rules:
  default_action: challenge
  presets: [block-fake-crawlers, block-ai-training, allow-ai-search]
`)
	return inst, site
}

func from(addr, userAgent string) map[string]string {
	return map[string]string{"X-Forwarded-For": addr, "User-Agent": userAgent, "Accept-Language": "en"}
}

func TestVerifiedCrawlersAndImpostors(t *testing.T) {
	inst, site := crawlerSetup(t, `{"prefixes": [{"ipv4Prefix": "192.0.2.0/24"}]}`)

	// The list is downloaded in the background right after the start.
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, report := health(t, inst)
		if report.Components["crawlers"].State == "ok" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the address list did not arrive: %+v\n%s", report, inst.logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	search := "Mozilla/5.0 (compatible; ExSearch/1.0; +https://example.org/bots)"
	tests := []struct {
		name, addr, ua string
		status         int
		body           string
	}{
		{"genuine AI search crawler", "192.0.2.5", search, 200, "website says hello"},
		{"impostor with the crawler's name", "203.0.113.9", search, 403, "This request was blocked"},
		{"genuine training crawler", "192.0.2.5", "ExTrain/2.0", 403, "This request was blocked"},
		{"training crawler's name from elsewhere", "203.0.113.9", "ExTrain/2.0", 403, "This request was blocked"},
		{"browser from the crawler's network", "192.0.2.5", "Mozilla/5.0 Firefox/130.0", 403, "quick security check"},
	}
	for _, tt := range tests {
		before := site.hitCount()
		resp, body := get(t, inst.public+"/", from(tt.addr, tt.ua))
		if resp.StatusCode != tt.status || !strings.Contains(body, tt.body) {
			t.Errorf("%s: status %d, want %d with %q; body:\n%.300s", tt.name, resp.StatusCode, tt.status, tt.body, body)
		}
		if tt.status != 200 && site.hitCount() != before {
			t.Errorf("%s: the request reached the website", tt.name)
		}
	}

	d := getDecisions(t, inst)
	for source, want := range map[string]uint64{
		"rule:preset.block-fake-crawlers": 2, "rule:preset.block-ai-training": 1, "rule:preset.allow-ai-search": 1, "default": 1,
	} {
		if got := d.count(source); got != want {
			t.Errorf("%s decided %d times, want %d", source, got, want)
		}
	}

	_, body := get(t, inst.ops+"/crawlers", nil)
	var report struct {
		Crawlers []struct {
			Name      string `json:"name"`
			Addresses int    `json:"addresses"`
			Requests  struct {
				Verified   int `json:"verified"`
				Unverified int `json:"unverified"`
			} `json:"requests"`
		} `json:"crawlers"`
	}
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		t.Fatalf("/crawlers: %v\n%s", err, body)
	}
	if len(report.Crawlers) != 2 || report.Crawlers[0].Name != "ExSearch" || report.Crawlers[0].Addresses != 1 ||
		report.Crawlers[0].Requests.Verified != 1 || report.Crawlers[0].Requests.Unverified != 1 {
		t.Errorf("/crawlers = %s", body)
	}
	for _, private := range []string{"192.0.2.5", "203.0.113.9"} {
		if strings.Contains(body, private) || strings.Contains(inst.logs.String(), private) {
			t.Errorf("the client address %s appears in the report or the log", private)
		}
	}
}

// A list that would make the whole internet a "genuine crawler" is refused:
// the crawler stays unverified-so-far and Xibalba says what is wrong.
func TestHostileAddressListIsRefused(t *testing.T) {
	inst, _ := crawlerSetup(t, `{"prefixes": [{"ipv4Prefix": "0.0.0.0/0"}]}`)

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(inst.logs.String(), "could not be downloaded") {
		if time.Now().After(deadline) {
			t.Fatalf("the refusal is not logged:\n%s", inst.logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	status, report := health(t, inst)
	if c := report.Components["crawlers"]; status != 200 || c.State != "degraded" || !strings.Contains(c.Detail, "too much of the internet") {
		t.Errorf("health: %d %+v", status, report)
	}
	// Not verified, so not let through as a crawler; not refuted either, so
	// it gets what everyone else gets: the check.
	resp, body := get(t, inst.public+"/", from("192.0.2.5", "ExSearch/1.0"))
	if resp.StatusCode != 403 || !strings.Contains(body, "quick security check") {
		t.Errorf("status %d, body:\n%.300s", resp.StatusCode, body)
	}
}

// Without a crawler rule the crawler machinery does not run: no component,
// no download, but the definitions can still be looked up.
func TestCrawlersAreIdleWithoutCrawlerRules(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, testRules)
	_, report := health(t, inst)
	if _, running := report.Components["crawlers"]; running {
		t.Errorf("the crawlers component runs without a crawler rule: %+v", report)
	}
	_, body := get(t, inst.ops+"/crawlers", nil)
	for _, name := range []string{"GPTBot", "ClaudeBot", "PerplexityBot", "Googlebot"} {
		if !strings.Contains(body, `"name": "`+name+`"`) {
			t.Errorf("/crawlers does not list %s", name)
		}
	}
}

// --- request limits ---------------------------------------------------------

const limitConfig = `server:
  listen: PUBLIC
  trusted_proxies: ["127.0.0.1"]
limits:
  enabled: true
  windows:
    - {requests: 5, per: 1h, action: challenge}
    - {requests: 8, per: 2h, action: deny}
  exempt: ["198.51.100.0/24"]
rules:
  list:
    - name: allow-office
      match:
        ip: ["192.0.2.0/24"]
      action: allow
      exempt_from_limits: true
`

func TestRequestLimits(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, limitConfig)
	browser := "Mozilla/5.0 Firefox/130.0"

	statuses := func(addr string, n int) []int {
		var got []int
		for i := 0; i < n; i++ {
			resp, _ := get(t, inst.public+"/", from(addr, browser))
			got = append(got, resp.StatusCode)
		}
		return got
	}

	// Five requests pass, the next three must pass the check, then the client is refused.
	got := statuses("203.0.113.9", 10)
	want := []int{200, 200, 200, 200, 200, 403, 403, 403, 429, 429}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
	resp, body := get(t, inst.public+"/", from("203.0.113.9", browser))
	retry, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
	if resp.StatusCode != 429 || !strings.Contains(body, "Too many requests") || retry < 1 || retry > 14400 {
		t.Errorf("refused request: %d, Retry-After %q, body:\n%.300s", resp.StatusCode, resp.Header.Get("Retry-After"), body)
	}

	// Other clients are not affected; exempt addresses and requests let through by an exempt rule are never limited.
	for name, addr := range map[string]string{"another client": "203.0.113.10", "exempt address": "198.51.100.7", "allowed by a rule": "192.0.2.7"} {
		n := 4
		if name != "another client" {
			n = 12
		}
		for _, status := range statuses(addr, n) {
			if status != 200 {
				t.Errorf("%s: status %d", name, status)
				break
			}
		}
	}

	_, body = get(t, inst.ops+"/limits", nil)
	var report struct {
		Clients int    `json:"clients"`
		Exempt  uint64 `json:"exempt_requests"`
		Limits  []struct {
			Over uint64 `json:"requests_over_limit"`
		} `json:"limits"`
	}
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		t.Fatalf("/limits: %v\n%s", err, body)
	}
	if report.Clients != 2 || report.Exempt != 12 || len(report.Limits) != 2 || report.Limits[0].Over != 3 || report.Limits[1].Over != 3 {
		t.Errorf("/limits = %s", body)
	}
	if strings.Contains(body, "203.0.113") || strings.Contains(inst.logs.String(), "203.0.113") {
		t.Error("a client address appears in the report or the log")
	}
	if _, report := health(t, inst); report.State != "ok" {
		t.Errorf("health = %+v", report)
	}
}

// A client that is over a "challenge" limit solves the check once and carries on.
func TestBrowserGetsPastAChallengeLimit(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, "limits:\n  enabled: true\n  windows:\n    - {requests: 2, per: 1h, action: challenge}\nchallenge:\n  difficulty: 10\n")
	// Behind a web server without trusted_proxies everyone would share one
	// limit; Xibalba says so at start.
	if !strings.Contains(inst.logs.String(), "share one limit") {
		t.Errorf("no warning about shared limits:\n%s", inst.logs.String())
	}
	if _, report := health(t, inst); report.Components["limits"].State != "ok" {
		t.Errorf("health = %+v", report)
	}
	v := newVisitor(t)
	var page string
	for i := 0; i < 3; i++ {
		_, page = v.do("GET", inst.public+"/seite", nil)
	}
	k := parseTask(t, page)
	resp, _ := v.do("POST", inst.public+"/.xibalba/verify", k.answer("pow", k.solve()))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("answer: status %d", resp.StatusCode)
	}
	for i := 0; i < 5; i++ {
		if resp, body := v.do("GET", inst.public+"/seite", nil); resp.StatusCode != 200 || !strings.Contains(body, "website says hello") {
			t.Fatalf("request %d after passing the check: %d", i, resp.StatusCode)
		}
	}
}

// The block the documentation recommends: plain programs and wanted
// addresses pass, whatever says it is a browser is checked.
func TestRecommendedPresets(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, `crawlers:
  refresh: false
rules:
  presets: [keep-internet-working, allow-feeds, allow-git-clients, block-fake-crawlers, block-ai-training,
            allow-search-engines, allow-ai-search, allow-ai-user-fetch, challenge-browsers]
`)
	browser := "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0"
	tests := []struct {
		name, method, path, ua string
		status                 int
	}{
		{"browser on a page", "GET", "/seite", browser, 403},
		{"browser reads robots.txt", "GET", "/robots.txt", browser, 200},
		{"browser reads the icon", "GET", "/favicon.ico", browser, 200},
		{"well-known address", "GET", "/.well-known/security.txt", browser, 200},
		{"well-known is not a way round the check", "GET", "/.well-known/../seite", browser, 403},
		{"posting to robots.txt is not let through", "POST", "/robots.txt", browser, 403},
		{"feed", "GET", "/blog/index.xml", browser, 200},
		{"feed address", "GET", "/blog/feed/", browser, 200},
		{"curl", "GET", "/seite", "curl/8.5.0", 200},
		{"git fetch", "GET", "/repo.git/info/refs", "git/2.43.0", 200},
		{"git on another address", "GET", "/seite", "git/2.43.0", 200}, // says what it is, not a browser
		{"training crawler", "GET", "/seite", "Mozilla/5.0 (compatible; GPTBot/1.2)", 403},
		{"training crawler reads robots.txt", "GET", "/robots.txt", "GPTBot/1.2", 200},
		{"opera", "GET", "/seite", "Opera/9.80 (Windows NT 6.1)", 403},
	}
	for _, tt := range tests {
		req, _ := http.NewRequest(tt.method, inst.public+tt.path, nil)
		req.URL.Opaque = tt.path // send the path as written
		req.Header.Set("User-Agent", tt.ua)
		req.Header.Set("Accept-Language", "en")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tt.status {
			t.Errorf("%s: status %d, want %d", tt.name, resp.StatusCode, tt.status)
		}
	}
	d := getDecisions(t, inst)
	if d.count("rule:preset.block-ai-training") != 1 || d.count("rule:preset.challenge-browsers") != 4 {
		t.Errorf("counts: training %d, browsers %d", d.count("rule:preset.block-ai-training"), d.count("rule:preset.challenge-browsers"))
	}
}

// --- trap -------------------------------------------------------------------

var trapLinkRE = regexp.MustCompile(`<template><a href="(/\.xibalba/trap/[0-9a-f]{32})" rel="nofollow">`)

func TestTrapCatchesWhoFollowsTheHiddenLink(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, `server:
  listen: PUBLIC
  trusted_proxies: ["127.0.0.1"]
trap:
  enabled: true
rules:
  presets: [block-trapped]
  list:
    - name: check-wiki
      match:
        path: {prefix: "/wiki"}
      action: challenge
`)
	browser := "Mozilla/5.0 Firefox/130.0"

	// The page a checked client receives carries the hidden link.
	_, page := get(t, inst.public+"/wiki/a", from("203.0.113.20", browser))
	m := trapLinkRE.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no trap link in the check page:\n%s", page)
	}

	// A client that does not follow it is treated as before.
	if resp, _ := get(t, inst.public+"/", from("203.0.113.20", browser)); resp.StatusCode != 200 {
		t.Errorf("before following the link: %d", resp.StatusCode)
	}
	// A client that follows it gets "not found", never the website, and is denied from then on.
	before := site.hitCount()
	if resp, _ := get(t, inst.public+m[1], from("203.0.113.20", browser)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the trap answered %d", resp.StatusCode)
	}
	if site.hitCount() != before {
		t.Error("the trap address reached the website")
	}
	resp, body := get(t, inst.public+"/", from("203.0.113.20", browser))
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "This request was blocked") {
		t.Errorf("after following the link: %d", resp.StatusCode)
	}
	// Other clients are not affected, and cannot be made to step into the
	// trap by someone who knows a link: a link only works for its own client.
	for _, path := range []string{m[1], "/.xibalba/trap/x", "/.xibalba/trap/"} {
		if resp, _ := get(t, inst.public+path, from("203.0.113.21", browser)); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s for another client: %d", path, resp.StatusCode)
		}
	}
	if resp, _ := get(t, inst.public+"/", from("203.0.113.21", browser)); resp.StatusCode != 200 {
		t.Errorf("another client: %d", resp.StatusCode)
	}

	_, report := get(t, inst.ops+"/trap", nil)
	if !strings.Contains(report, `"hits": 1`) || !strings.Contains(report, `"ignored": 3`) || !strings.Contains(report, `"clients": 1`) || strings.Contains(report, "203.0.113") {
		t.Errorf("/trap = %s", report)
	}
	if strings.Contains(inst.logs.String(), "203.0.113") {
		t.Error("a client address appears in the log")
	}
}

func TestMazeAndTrapAreOffByDefault(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, challengeRules)
	_, page := get(t, inst.public+"/wiki/a", language)
	if strings.Contains(page, "trap") || strings.Contains(page, "<template") {
		t.Error("the check page carries a trap link although the trap is off")
	}
	// Without the trap its address is just an unknown address of Xibalba's own.
	if resp, _ := get(t, inst.public+"/.xibalba/trap/abc", language); resp.StatusCode != http.StatusNotFound {
		t.Errorf("trap address with the trap off: %d", resp.StatusCode)
	}

	maze := start(t, site.URL, "trap:\n  enabled: true\n  maze: true\n")
	// An address nobody was given is not the maze's.
	if resp, _ := get(t, maze.public+"/.xibalba/trap/abc", language); resp.StatusCode != http.StatusNotFound {
		t.Errorf("maze, unknown address: %d", resp.StatusCode)
	}
	_ = page
}

// --- countries --------------------------------------------------------------

func TestCountryRules(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "countries.mmdb")
	build := func(networks map[string]string) {
		t.Helper()
		tmp := database + ".new"
		if err := os.WriteFile(tmp, geotest.Build(networks, geotest.Options{}), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, database); err != nil {
			t.Fatal(err)
		}
	}
	build(map[string]string{"192.0.2.0/24": "DE", "198.51.100.0/24": "FR", "2001:db8::/32": "FR"})

	site := newWebsite(t)
	inst := start(t, site.URL, `server:
  listen: PUBLIC
  trusted_proxies: ["127.0.0.1"]
countries:
  database: "`+database+`"
rules:
  list:
    - name: block-abroad
      match:
        country: [FR, XX]
      action: deny
`)
	browser := "Mozilla/5.0 Firefox/130.0"
	for addr, want := range map[string]int{"192.0.2.9": 200, "198.51.100.9": 403, "2001:db8::9": 403, "203.0.113.9": 200} {
		if resp, _ := get(t, inst.public+"/", from(addr, browser)); resp.StatusCode != want {
			t.Errorf("%s: status %d, want %d", addr, resp.StatusCode, want)
		}
	}
	if _, report := health(t, inst); report.Components["countries"].State != "ok" {
		t.Errorf("health = %+v", report)
	}
	if !strings.Contains(inst.logs.String(), "country database loaded") {
		t.Errorf("the log does not say that the database was loaded:\n%s", inst.logs.String())
	}
}

// Without a rule that asks for a country the database is not even opened.
func TestCountryDatabaseIsIdleWithoutCountryRules(t *testing.T) {
	database := filepath.Join(t.TempDir(), "countries.mmdb")
	if err := os.WriteFile(database, geotest.Build(map[string]string{"192.0.2.0/24": "DE"}, geotest.Options{}), 0o644); err != nil {
		t.Fatal(err)
	}
	site := newWebsite(t)
	inst := start(t, site.URL, "countries:\n  database: \""+database+"\"\n")
	if _, report := health(t, inst); report.Components["countries"].State != "" {
		t.Errorf("the countries component runs without a country rule: %+v", report)
	}
}

func TestMazeBehindTheHiddenLink(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, "trap:\n  enabled: true\n  maze: true\n"+challengeRules)
	_, page := get(t, inst.public+"/wiki/a", language)
	m := trapLinkRE.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no trap link in the check page:\n%s", page)
	}
	resp, maze := get(t, inst.public+m[1], language)
	if resp.StatusCode != 200 || strings.Count(maze, `href="`+m[1]+`/`) != 5 || !strings.Contains(resp.Header.Get("X-Robots-Tag"), "noindex") {
		t.Fatalf("maze: %d\n%.400s", resp.StatusCode, maze)
	}
	next := regexp.MustCompile(`href="([^"]+)"`).FindStringSubmatch(maze)[1]
	if resp, deeper := get(t, inst.public+next, language); resp.StatusCode != 200 || deeper == maze {
		t.Errorf("one step deeper: %d", resp.StatusCode)
	}
	if site.hitCount() != 0 {
		t.Error("the maze reached the website")
	}
}

// A request target that is not a path gives the rules nothing to test. It
// must not be passed on.
func TestRequestTargetsThatAreNotPathsAreRefused(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, testRules)
	host := strings.TrimPrefix(inst.public, "http://")
	for _, target := range []string{"http:admin/secret", "x:robots.txt", "*"} {
		conn, err := net.Dial("tcp", host)
		if err != nil {
			t.Fatal(err)
		}
		method := "GET"
		if target == "*" {
			method = "OPTIONS"
		}
		_, _ = fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", method, target, host)
		answer, _ := io.ReadAll(conn)
		_ = conn.Close()
		// "OPTIONS *" asks about the server as a whole; Go answers it
		// itself, and it never reaches the rules or the website.
		if !strings.HasPrefix(string(answer), "HTTP/1.1 400") && target != "*" {
			t.Errorf("%s %s: %.60q", method, target, answer)
		}
	}
	if site.hitCount() != 0 {
		t.Error("such a request reached the website")
	}
}

// A client that walks through many different pages is stopped; one that
// loads the same few pages with many images is not.
func TestLimitOnDifferentPages(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, `server:
  listen: PUBLIC
  trusted_proxies: ["127.0.0.1"]
limits:
  enabled: true
  windows:
    - {requests: 20, per: 1h, action: deny, count: pages}
`)
	browser := "Mozilla/5.0 Firefox/130.0"
	for i := 0; i < 60; i++ {
		target := fmt.Sprintf("/artikel/%d", i%3)
		if i%2 == 1 {
			target = fmt.Sprintf("/bilder/%d.png", i)
		}
		if resp, _ := get(t, inst.public+target, from("203.0.113.30", browser)); resp.StatusCode != 200 {
			t.Fatalf("the reader was stopped at request %d (%s): %d", i, target, resp.StatusCode)
		}
	}
	// Answers that are not pages do not count, whatever their address looks like.
	for i := 0; i < 60; i++ {
		if resp, _ := get(t, inst.public+fmt.Sprintf("/api/suggest?q=%d", i), from("203.0.113.30", browser)); resp.StatusCode != 200 {
			t.Fatalf("the reader was stopped at data request %d: %d", i, resp.StatusCode)
		}
	}
	// Pages do, also when their address is dressed up as an image's.
	for name, pattern := range map[string]string{"plain": "/liste?seite=%d", "disguised": "/index.php/artikel/%d/x.css"} {
		client := map[string]string{"plain": "203.0.113.31", "disguised": "203.0.113.32"}[name]
		stopped := 0
		for i := 1; i <= 40 && stopped == 0; i++ {
			if resp, _ := get(t, inst.public+fmt.Sprintf(pattern, i), from(client, browser)); resp.StatusCode == http.StatusTooManyRequests {
				stopped = i
			}
		}
		if stopped < 18 || stopped > 28 {
			t.Errorf("%s: the crawler was stopped at page %d, limit 20", name, stopped)
		}
	}
}

// --- metrics ----------------------------------------------------------------

func TestMetrics(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, "trap:\n  enabled: true\nlimits:\n  enabled: true\nserver:\n  listen: PUBLIC\n  trusted_proxies: [\"127.0.0.1\"]\n"+testRules)
	get(t, inst.public+"/", from("203.0.113.40", "Mozilla/5.0 Firefox/130.0"))
	get(t, inst.public+"/admin", from("203.0.113.40", "Mozilla/5.0 Firefox/130.0"))

	resp, err := http.Get(inst.ops + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	body := string(raw)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Errorf("content type = %q", resp.Header.Get("Content-Type"))
	}
	for _, want := range []string{
		`xibalba_build_info{version="`,
		`xibalba_component_state{component="public"} 0`,
		`xibalba_component_state{component="limits"} 0`,
		`xibalba_decisions_total{source="rule:block-admin",action="deny"} 1`,
		`xibalba_decisions_total{source="default",action="allow"} 1`,
		`xibalba_challenge_total{result="served"} 0`,
		`xibalba_crawler_requests_total{crawler="GPTBot",class="training",status="verified"} 0`,
		`xibalba_limit_clients 1`,
		`xibalba_limit_over_total{per="1m0s",count="requests",action="challenge"} 0`,
		`xibalba_trap_hits_total 0`,
		"# TYPE xibalba_decisions_total counter",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %s", want)
		}
	}
	if strings.Contains(body, "203.0.113") || strings.Contains(body, "Firefox") || strings.Contains(body, "/admin") {
		t.Error("/metrics holds an address, a user agent or a path")
	}
	// Not on the public side.
	if resp, _ := get(t, inst.public+"/metrics", language); strings.Contains(resp.Header.Get("Content-Type"), "version=0.0.4") {
		t.Error("the metrics are served on the public listener")
	}
}

// With a limit on pages Xibalba looks at the website's answers. That must
// not get in the way of upgraded connections (websockets).
func TestUpgradedConnectionWithALimitOnPages(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "echo") {
			http.Error(w, "expected an upgrade", http.StatusBadRequest)
			return
		}
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		_ = rw.Flush()
		line, _ := rw.ReadString('\n')
		_, _ = rw.WriteString("echo: " + line)
		_ = rw.Flush()
	}))
	defer upstream.Close()
	inst := start(t, upstream.URL, "limits:\n  enabled: true\n  windows:\n    - {requests: 50, per: 10m, action: challenge, count: pages}\n")

	conn, err := net.Dial("tcp", strings.TrimPrefix(inst.public, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = fmt.Fprintf(conn, "GET /socket HTTP/1.1\r\nHost: example.org\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	reader := bufio.NewReader(conn)
	status, _ := reader.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("answer to the upgrade: %q", status)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil || line == "\r\n" {
			break
		}
	}
	_, _ = fmt.Fprintf(conn, "hello\n")
	if got, _ := reader.ReadString('\n'); got != "echo: hello\n" {
		t.Errorf("over the upgraded connection: %q", got)
	}
}

// --- health check for containers --------------------------------------------

func TestHealthCheckFlag(t *testing.T) {
	site := newWebsite(t)
	public, ops := freeAddr(t), freeAddr(t)
	config := filepath.Join(t.TempDir(), "xibalba.yaml")
	content := fmt.Sprintf("upstream:\n  url: %s\nserver:\n  listen: %s\nops:\n  listen: %s\n", site.URL, public, ops)
	if err := os.WriteFile(config, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// Nothing runs yet: not healthy.
	if out, err := exec.Command(binary, "-config", config, "-healthcheck").CombinedOutput(); err == nil {
		t.Errorf("health check without a running Xibalba succeeded: %s", out)
	}
	cmd := exec.Command(binary, "-config", config)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Signal(syscall.SIGTERM); _ = cmd.Wait() })
	resp := waitFor(t, "http://"+ops+"/healthz")
	_ = resp.Body.Close()
	out, err := exec.Command(binary, "-config", config, "-healthcheck").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Errorf("health check of a running Xibalba: %v, %q", err, out)
	}
}

// --- statistics -------------------------------------------------------------

type statisticsAnswer struct {
	Totals map[string]uint64 `json:"totals"`
	Hours  []struct {
		Hour   time.Time         `json:"hour"`
		Counts map[string]uint64 `json:"counts"`
	} `json:"hours"`
}

func getStatistics(t *testing.T, inst *instance) statisticsAnswer {
	t.Helper()
	_, body := get(t, inst.ops+"/statistics?hours=2", nil)
	var a statisticsAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		t.Fatalf("/statistics: %v\n%s", err, body)
	}
	return a
}

// Counts are written down when Xibalba stops and are there again after the
// next start. What is written holds no address, path or user agent.
func TestStatisticsSurviveARestart(t *testing.T) {
	site := newWebsite(t)
	dir := t.TempDir()
	config := "statistics:\n  directory: \"" + dir + "\"\n" + testRules

	inst := start(t, site.URL, config)
	for i := 0; i < 3; i++ {
		get(t, inst.public+"/admin/geheim", map[string]string{"User-Agent": "Mozilla/5.0 SecretAgent", "Accept-Language": "en"})
	}
	get(t, inst.public+"/", language)
	if _, report := health(t, inst); report.Components["statistics"].State != "ok" {
		t.Fatalf("health = %+v", report)
	}
	_ = inst.cmd.Process.Signal(syscall.SIGTERM)
	_ = inst.cmd.Wait()

	// A hard look at what is on disk.
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("nothing was written: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("a directory %q was made although counts per network are off", e.Name())
		}
		raw, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		for _, private := range []string{"127.0.0.1", "geheim", "SecretAgent", "/admin"} {
			if strings.Contains(string(raw), private) {
				t.Errorf("%s holds %q:\n%s", e.Name(), private, raw)
			}
		}
	}

	second := start(t, site.URL, config)
	get(t, second.public+"/admin", language)
	// Counts are taken up once a minute and at the stop; ask after a stop.
	_ = second.cmd.Process.Signal(syscall.SIGTERM)
	_ = second.cmd.Wait()

	third := start(t, site.URL, config)
	a := getStatistics(t, third)
	// The hour may have turned between the runs; the totals hold both.
	if a.Totals["decision|rule:block-admin|deny"] != 4 || a.Totals["decision|default|allow"] != 1 || len(a.Hours) < 1 {
		t.Errorf("totals = %v", a.Totals)
	}
}

func TestStatisticsAreOffWithoutADirectory(t *testing.T) {
	site := newWebsite(t)
	inst := start(t, site.URL, testRules)
	if resp, _ := get(t, inst.ops+"/statistics", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("/statistics without a directory: %d", resp.StatusCode)
	}
	if _, report := health(t, inst); report.Components["statistics"].State != "" {
		t.Errorf("health = %+v", report)
	}
}

// Counts per network are an option. Switched on, they name the network a
// request came from and never a single address; switched off (the default,
// see TestStatisticsSurviveARestart), nothing of the kind is written.
func TestStatisticsPerNetwork(t *testing.T) {
	site := newWebsite(t)
	dir := t.TempDir()
	config := "statistics:\n  directory: \"" + dir + "\"\n  networks: {enabled: true, top: 5, keep_days: 7}\n" + testRules

	inst := start(t, site.URL, config)
	if resp, _ := get(t, inst.public+"/admin", language); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("/admin: %d", resp.StatusCode)
	}
	get(t, inst.public+"/", language)
	get(t, inst.public+"/", language)
	if _, report := health(t, inst); report.Components["statistics-networks"].State != "ok" {
		t.Fatalf("health = %+v", report)
	}
	_ = inst.cmd.Process.Signal(syscall.SIGTERM)
	_ = inst.cmd.Wait()

	raw, err := os.ReadFile(filepath.Join(dir, "networks", "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "127.0.0.1") || !strings.Contains(string(raw), "127.0.0.0/24") {
		t.Errorf("networks/current.json:\n%s", raw)
	}

	second := start(t, site.URL, config)
	_, body := get(t, second.ops+"/statistics/networks?hours=2", nil)
	var a statisticsAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		t.Fatalf("/statistics/networks: %v\n%s", err, body)
	}
	if a.Totals["network|127.0.0.0/24|deny"] != 1 || a.Totals["network|127.0.0.0/24|allow"] != 2 {
		t.Errorf("totals = %v", a.Totals)
	}
	// The general statistics stay free of networks.
	if _, general := get(t, second.ops+"/statistics?hours=2", nil); strings.Contains(general, "network|") {
		t.Errorf("/statistics holds networks:\n%s", general)
	}
}

// The web interface is an option. Off (the default), nothing listens. On, it
// needs a password that was set with -set-password, and shows nothing
// without a login.
func TestWebInterface(t *testing.T) {
	site := newWebsite(t)

	// Off by default: no "admin" part, although a password file may exist.
	off := start(t, site.URL, testRules)
	if _, report := health(t, off); report.Components["admin"].State != "" {
		t.Errorf("the web interface runs without being switched on: %+v", report)
	}

	dir := t.TempDir()
	config := filepath.Join(dir, "xibalba.yaml")
	public, ops, ui := freeAddr(t), freeAddr(t), freeAddr(t)
	text := fmt.Sprintf("upstream:\n  url: %s\nserver:\n  listen: %s\nops:\n  listen: %s\nlog:\n  format: text\nadmin:\n  enabled: true\n  listen: %s\n",
		site.URL, public, ops, ui) + testRules
	if err := os.WriteFile(config, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	// Switched on without a password: refused, with the way out.
	out, err := exec.Command(binary, "-check", "-config", config).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "admin.password_file") || !strings.Contains(string(out), "-set-password") {
		t.Fatalf("check without a password: %v\n%s", err, out)
	}

	// A password that is too short is refused; a good one is stored, not kept.
	const password = "ein langes Passwort 42"
	short := exec.Command(binary, "-set-password", "-config", config)
	short.Stdin = strings.NewReader("kurz\n")
	if out, err := short.CombinedOutput(); err == nil {
		t.Fatalf("a short password was accepted:\n%s", out)
	}
	set := exec.Command(binary, "-set-password", "-config", config)
	set.Stdin = strings.NewReader(password + "\n")
	if out, err := set.CombinedOutput(); err != nil {
		t.Fatalf("set-password: %v\n%s", err, out)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "admin.password"))
	if err != nil || strings.Contains(string(stored), password) || !strings.HasPrefix(string(stored), "pbkdf2-sha256$") {
		t.Fatalf("stored password: %v %q", err, stored)
	}
	if info, _ := os.Stat(filepath.Join(dir, "admin.password")); info.Mode().Perm() != 0o600 {
		t.Errorf("mode of the password file: %v", info.Mode())
	}

	logs := &logBuffer{}
	cmd := exec.Command(binary, "-config", config)
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	_ = waitFor(t, "http://"+ops+"/healthz").Body.Close()
	inst := &instance{cmd: cmd, logs: logs, public: "http://" + public, ops: "http://" + ops}
	if _, report := health(t, inst); report.Components["admin"].State != "ok" {
		t.Fatalf("health = %+v", report)
	}
	get(t, inst.public+"/admin", language) // blocked by the rule: something to show

	base := "http://" + ui
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(form url.Values, cookie string) *http.Response {
		req, _ := http.NewRequest("POST", base+"/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}

	if resp, err := client.Get(base + "/"); err != nil || resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("overview without a login: %v %v", resp, err)
	}
	if resp := post(url.Values{"password": {"falsches Passwort 42"}}, ""); resp.StatusCode != http.StatusUnauthorized || len(resp.Cookies()) != 0 {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	resp := post(url.Values{"password": {password}}, "")
	if resp.StatusCode != http.StatusSeeOther || len(resp.Cookies()) != 1 {
		t.Fatalf("right password: %d", resp.StatusCode)
	}
	cookie := resp.Cookies()[0]
	req, _ := http.NewRequest("GET", base+"/", nil)
	req.AddCookie(cookie)
	page, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	_ = page.Body.Close()
	if page.StatusCode != 200 || !strings.Contains(string(body), "block-admin") {
		t.Errorf("overview: %d\n%s", page.StatusCode, body)
	}
	if text := logs.String(); strings.Contains(text, password) || strings.Contains(text, cookie.Value) || strings.Contains(text, "pbkdf2") {
		t.Errorf("the log holds a secret:\n%s", text)
	}
}

// With admin.allow_changes the web interface switches presets and lists
// addresses. A change takes effect at once, is kept in its own file, and is
// still in force after a restart.
func TestChangesInTheWebInterface(t *testing.T) {
	site := newWebsite(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "xibalba.yaml")
	public, ops, ui := freeAddr(t), freeAddr(t), freeAddr(t)
	text := fmt.Sprintf("upstream:\n  url: %s\nserver:\n  listen: %s\nops:\n  listen: %s\nlog:\n  format: text\ncrawlers:\n  refresh: false\nadmin:\n  enabled: true\n  allow_changes: true\n  listen: %s\n",
		site.URL, public, ops, ui)
	if err := os.WriteFile(config, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	const password = "ein langes Passwort 42"
	set := exec.Command(binary, "-set-password", "-config", config)
	set.Stdin = strings.NewReader(password + "\n")
	if out, err := set.CombinedOutput(); err != nil {
		t.Fatalf("set-password: %v\n%s", err, out)
	}
	startIt := func() (*exec.Cmd, *logBuffer) {
		logs := &logBuffer{}
		cmd := exec.Command(binary, "-config", config)
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
		_ = waitFor(t, "http://"+ops+"/healthz").Body.Close()
		return cmd, logs
	}
	cmd, _ := startIt()

	base := "http://" + ui
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var cookie *http.Cookie
	send := func(method, path string, form url.Values) (int, string) {
		req, _ := http.NewRequest(method, base+path, strings.NewReader(form.Encode()))
		if method == "POST" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		for _, c := range resp.Cookies() {
			cookie = c
		}
		return resp.StatusCode, string(body)
	}
	signIn := func() string {
		cookie = nil
		if code, _ := send("POST", "/login", url.Values{"password": {password}}); code != http.StatusSeeOther {
			t.Fatalf("sign-in: %d", code)
		}
		_, page := send("GET", "/settings", nil)
		m := regexp.MustCompile(`name="form" value="([^"]+)"`).FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("no form value on the settings page:\n%s", page)
		}
		return m[1]
	}
	form := signIn()
	visit := func(agent string) int {
		resp, _ := get(t, "http://"+public+"/", map[string]string{"User-Agent": agent, "Accept-Language": "en"})
		return resp.StatusCode
	}

	// A preset: off, a training crawler gets through; on, it does not.
	if code := visit("GPTBot/1.0"); code != 200 {
		t.Fatalf("GPTBot before: %d", code)
	}
	if code, body := send("POST", "/settings/preset", url.Values{"form": {form}, "name": {"block-ai-training"}, "state": {"on"}}); code != http.StatusSeeOther {
		t.Fatalf("switching the preset on: %d\n%s", code, body)
	}
	if code := visit("GPTBot/1.0"); code != http.StatusForbidden {
		t.Errorf("GPTBot after: %d", code)
	}

	// A preset that needs something that is off is refused, with the reason.
	if code, body := send("POST", "/settings/preset", url.Values{"form": {form}, "name": {"block-trapped"}, "state": {"on"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "trap") {
		t.Errorf("block-trapped without the trap: %d", code)
	}

	// An address: blocked at once, let through again when removed.
	if code := visit("curl/8"); code != 200 {
		t.Fatalf("before blocking: %d", code)
	}
	if code, body := send("POST", "/settings/address", url.Values{"form": {form}, "network": {"127.0.0.1"}, "action": {"deny"}, "lifetime": {"day"}, "note": {"test"}}); code != http.StatusSeeOther {
		t.Fatalf("blocking: %d\n%s", code, body)
	}
	if code := visit("curl/8"); code != http.StatusForbidden {
		t.Errorf("after blocking: %d", code)
	}
	// The web interface itself is not behind the rules: nobody locks themselves out of it.
	if code, _ := send("GET", "/settings", nil); code != 200 {
		t.Errorf("settings after blocking this address: %d", code)
	}

	// Kept in its own file; the configuration file is untouched.
	kept, err := os.ReadFile(filepath.Join(dir, "admin.changes.json"))
	if err != nil || !strings.Contains(string(kept), `"block-ai-training": true`) || !strings.Contains(string(kept), `"127.0.0.1"`) {
		t.Fatalf("changes file: %v\n%s", err, kept)
	}
	if now, _ := os.ReadFile(config); string(now) != text {
		t.Error("the configuration file was rewritten")
	}

	// Still in force after a restart.
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = cmd.Wait()
	_, logs := startIt()
	if code := visit("curl/8"); code != http.StatusForbidden {
		t.Errorf("after the restart: %d", code)
	}
	if !strings.Contains(logs.String(), "changes made in the web interface are in force") {
		t.Errorf("the log does not mention the changes:\n%s", logs.String())
	}
	form = signIn()
	if code, body := send("POST", "/settings/address/remove", url.Values{"form": {form}, "network": {"127.0.0.1"}}); code != http.StatusSeeOther {
		t.Fatalf("removing: %d\n%s", code, body)
	}
	if code := visit("curl/8"); code != 200 {
		t.Errorf("after removing: %d", code)
	}
	if code := visit("GPTBot/1.0"); code != http.StatusForbidden {
		t.Errorf("GPTBot after the restart: %d", code)
	}

	// Own rules: tried without saving, saved, in force at once, and taken back.
	own := "rules:\n  - name: block-secret\n    match:\n      path: {prefix: \"/geheim\"}\n    action: deny\n"
	fetch := func(path string) int {
		resp, _ := get(t, "http://"+public+path, map[string]string{"User-Agent": "curl/8", "Accept-Language": "en"})
		return resp.StatusCode
	}
	code, page := send("POST", "/settings/rules", url.Values{"form": {form}, "do": {"test"}, "rules": {own},
		"method": {"GET"}, "address": {"/geheim/akte"}, "client": {"203.0.113.9"}, "user_agent": {"curl/8"}})
	if code != 200 || !strings.Contains(page, "decided by block-secret") {
		t.Errorf("trying the draft: %d", code)
	}
	if got := fetch("/geheim/akte"); got != 200 {
		t.Errorf("trying a draft put it in force: %d", got)
	}
	code, page = send("POST", "/settings/rules", url.Values{"form": {form}, "do": {"save"}, "rules": {"rules:\n  - name: bad\n    action: fly\n"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(page, "line 3") {
		t.Errorf("saving rules that do not work: %d", code)
	}
	if code, _ := send("POST", "/settings/rules", url.Values{"form": {form}, "do": {"save"}, "rules": {own}}); code != http.StatusSeeOther {
		t.Fatalf("saving the rules: %d", code)
	}
	if got := fetch("/geheim/akte"); got != http.StatusForbidden {
		t.Errorf("after saving the rule: %d", got)
	}
	_, page = send("GET", "/settings", nil)
	newest := regexp.MustCompile(`name="version" value="([^"]+)"`).FindStringSubmatch(page)
	if newest == nil {
		t.Fatalf("no version on the settings page")
	}
	if code, _ := send("POST", "/settings/restore", url.Values{"form": {form}, "version": {newest[1]}}); code != http.StatusSeeOther {
		t.Fatalf("going back: %d", code)
	}
	if got := fetch("/geheim/akte"); got != 200 {
		t.Errorf("after going back: %d", got)
	}
	if code := visit("GPTBot/1.0"); code != http.StatusForbidden {
		t.Errorf("going back one step also undid the preset: %d", code)
	}
	if text := logs.String(); strings.Contains(text, "127.0.0.1\"") && strings.Contains(text, "addresses=127") {
		t.Errorf("the log names a listed address:\n%s", text)
	}
}
