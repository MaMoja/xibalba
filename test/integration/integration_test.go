// Package integration tests the real xibalba binary from the outside: it is
// built, started with a configuration file, put in front of a fake website,
// talked to over HTTP and stopped with a signal, exactly as an administrator
// would run it.
package integration

import (
	"bytes"
	"crypto/sha256"
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
