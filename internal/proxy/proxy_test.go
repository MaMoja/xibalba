package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MaMoja/xibalba/internal/clientip"
	"github.com/MaMoja/xibalba/internal/health"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// seen is what the fake website reports back about the request it received.
type seen struct {
	Method string
	Path   string
	Query  string
	Host   string
	Body   string
	Header http.Header
}

// echo is a fake website that answers with a description of the request.
func echo() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-From-Website", "yes")
		w.WriteHeader(http.StatusTeapot)
		_ = json.NewEncoder(w).Encode(seen{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
			Host: r.Host, Body: string(body), Header: r.Header,
		})
	})
}

type setup struct {
	proxy *Proxy
	front *httptest.Server // Xibalba's public side
}

// newSetup puts a Proxy in front of website, with the client-identity stage
// before it exactly as in the real program.
func newSetup(t *testing.T, website http.Handler, trusted []string, change func(*Options)) setup {
	t.Helper()
	back := httptest.NewServer(website)
	t.Cleanup(back.Close)
	return newSetupFor(t, back.URL, trusted, change)
}

func newSetupFor(t *testing.T, websiteURL string, trusted []string, change func(*Options)) setup {
	t.Helper()
	target, err := url.Parse(websiteURL)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Upstream:              target,
		PreserveHost:          true,
		DialTimeout:           2 * time.Second,
		ResponseHeaderTimeout: 2 * time.Second,
		Log:                   quiet(),
	}
	if change != nil {
		change(&opts)
	}
	p := New(opts)
	t.Cleanup(p.Close)

	var prefixes []netip.Prefix
	for _, s := range trusted {
		prefixes = append(prefixes, netip.MustParsePrefix(s))
	}
	front := httptest.NewServer(clientip.Middleware(clientip.New(prefixes), p))
	t.Cleanup(front.Close)
	return setup{proxy: p, front: front}
}

func do(t *testing.T, req *http.Request) (*http.Response, seen) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var s seen
	if resp.Header.Get("Content-Type") == "application/json" {
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			t.Fatalf("decoding the website's answer: %v", err)
		}
	}
	return resp, s
}

func TestForwardsRequestAndResponseUnchanged(t *testing.T) {
	s := newSetup(t, echo(), nil, nil)

	req, _ := http.NewRequest(http.MethodPost, s.front.URL+"/shop/cart?item=7&q=a%20b", strings.NewReader("payload"))
	req.Header.Set("X-Custom", "kept")
	resp, got := do(t, req)

	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want the website's 418", resp.StatusCode)
	}
	if resp.Header.Get("X-From-Website") != "yes" {
		t.Error("the website's response header was lost")
	}
	if got.Method != http.MethodPost || got.Path != "/shop/cart" || got.Query != "item=7&q=a%20b" || got.Body != "payload" {
		t.Errorf("website saw %+v", got)
	}
	if got.Header.Get("X-Custom") != "kept" {
		t.Error("the request header was lost")
	}
}

func TestHostHeader(t *testing.T) {
	t.Run("preserved by default", func(t *testing.T) {
		s := newSetup(t, echo(), nil, nil)
		req, _ := http.NewRequest(http.MethodGet, s.front.URL+"/", nil)
		req.Host = "www.example.org"
		if _, got := do(t, req); got.Host != "www.example.org" {
			t.Errorf("website saw Host %q, want www.example.org", got.Host)
		}
	})
	t.Run("replaced when preserve_host is off", func(t *testing.T) {
		s := newSetup(t, echo(), nil, func(o *Options) { o.PreserveHost = false })
		req, _ := http.NewRequest(http.MethodGet, s.front.URL+"/", nil)
		req.Host = "www.example.org"
		_, got := do(t, req)
		if got.Host == "www.example.org" || !strings.HasPrefix(got.Host, "127.0.0.1:") {
			t.Errorf("website saw Host %q, want the upstream's own host", got.Host)
		}
	})
}

func TestForwardingHeaders(t *testing.T) {
	spoofed := map[string]string{
		"X-Forwarded-For":   "198.51.100.7",
		"X-Forwarded-Proto": "https",
		"X-Forwarded-Host":  "portal.example.org",
		"X-Real-IP":         "6.6.6.6",
		"Forwarded":         "for=6.6.6.6",
	}

	t.Run("untrusted client cannot set them", func(t *testing.T) {
		s := newSetup(t, echo(), nil, nil)
		req, _ := http.NewRequest(http.MethodGet, s.front.URL+"/", nil)
		for k, v := range spoofed {
			req.Header.Set(k, v)
		}
		_, got := do(t, req)

		want := map[string]string{
			"X-Forwarded-For":   "127.0.0.1",
			"X-Forwarded-Proto": "http",
			"X-Forwarded-Host":  req.URL.Host,
			"X-Real-Ip":         "127.0.0.1",
			"Forwarded":         "",
		}
		for name, value := range want {
			if all := strings.Join(got.Header.Values(name), " | "); all != value {
				t.Errorf("website saw %s = %q, want %q", name, all, value)
			}
		}
	})

	t.Run("trusted proxy's account is kept and extended", func(t *testing.T) {
		s := newSetup(t, echo(), []string{"127.0.0.1/32"}, nil)
		req, _ := http.NewRequest(http.MethodGet, s.front.URL+"/", nil)
		for k, v := range spoofed {
			req.Header.Set(k, v)
		}
		_, got := do(t, req)

		want := map[string]string{
			"X-Forwarded-For":   "198.51.100.7, 127.0.0.1",
			"X-Forwarded-Proto": "https",
			"X-Forwarded-Host":  "portal.example.org",
			"X-Real-Ip":         "198.51.100.7", // from the resolver, never the incoming header
			"Forwarded":         "",
		}
		for name, value := range want {
			if all := strings.Join(got.Header.Values(name), " | "); all != value {
				t.Errorf("website saw %s = %q, want %q", name, all, value)
			}
		}
	})
}

func TestHopByHopHeadersAreNotForwarded(t *testing.T) {
	s := newSetup(t, echo(), nil, nil)
	req, _ := http.NewRequest(http.MethodGet, s.front.URL+"/", nil)
	req.Header.Set("Connection", "X-Internal-Hint")
	req.Header.Set("X-Internal-Hint", "drop me")
	req.Header.Set("Proxy-Authorization", "Basic c2VjcmV0")
	_, got := do(t, req)
	for _, name := range []string{"X-Internal-Hint", "Proxy-Authorization"} {
		if v := got.Header.Get(name); v != "" {
			t.Errorf("website received hop-by-hop header %s = %q", name, v)
		}
	}
}

func TestWebsiteDownGives502AndDegradedHealth(t *testing.T) {
	// A listener that is closed again: nothing answers on this address.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	s := newSetupFor(t, "http://"+addr, nil, nil)
	if got := s.proxy.Health().State; got != health.OK {
		t.Fatalf("health before any request = %q, want ok", got)
	}

	resp, err := http.Get(s.front.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	if strings.Contains(string(body), addr) || strings.Contains(string(body), "dial") {
		t.Errorf("the error page reveals internals:\n%s", body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("the error page must not be cached")
	}
	status := s.proxy.Health()
	if status.State != health.Degraded || !strings.Contains(status.Detail, addr) {
		t.Errorf("health = %+v, want degraded naming the website", status)
	}
}

func TestSlowWebsiteGives504ThenRecovers(t *testing.T) {
	release := make(chan struct{})
	website := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		_, _ = io.WriteString(w, "fine")
	})
	s := newSetup(t, website, nil, func(o *Options) { o.ResponseHeaderTimeout = 150 * time.Millisecond })
	defer close(release)

	resp, err := http.Get(s.front.URL + "/slow")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", resp.StatusCode)
	}
	if got := s.proxy.Health().State; got != health.Degraded {
		t.Errorf("health after a timeout = %q, want degraded", got)
	}

	resp, err = http.Get(s.front.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := s.proxy.Health().State; got != health.OK {
		t.Errorf("health after the website answered again = %q, want ok", got)
	}
}

func TestWebsiteErrorStatusIsNotAProxyFailure(t *testing.T) {
	website := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "the website's own error", http.StatusInternalServerError)
	})
	s := newSetup(t, website, nil, nil)

	resp, err := http.Get(s.front.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "the website's own error") {
		t.Errorf("got %d %q, want the website's 500 passed through", resp.StatusCode, body)
	}
	if got := s.proxy.Health().State; got != health.OK {
		t.Errorf("health = %q, want ok: the website answered", got)
	}
}

func TestClientGoingAwayIsNotAWebsiteFailure(t *testing.T) {
	arrived := make(chan struct{})
	website := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-r.Context().Done()
	})
	s := newSetup(t, website, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.front.URL+"/", nil)
	done := make(chan struct{})
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		close(done)
	}()
	<-arrived
	cancel()
	<-done

	// Give the proxy a moment to notice the closed connection.
	time.Sleep(300 * time.Millisecond)
	if got := s.proxy.Health(); got.State != health.OK {
		t.Errorf("health = %+v, want ok: only the visitor left", got)
	}
}

func TestStreamingResponseArrivesInPieces(t *testing.T) {
	release := make(chan struct{})
	website := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "first\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "second\n")
	})
	s := newSetup(t, website, nil, nil)

	resp, err := http.Get(s.front.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := bufio.NewReader(resp.Body)

	got := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		if line != "first\n" {
			t.Errorf("first piece = %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first piece was held back until the response finished")
	}

	close(release)
	if line, _ := reader.ReadString('\n'); line != "second\n" {
		t.Errorf("second piece = %q", line)
	}
}

// Websockets and other upgraded connections must pass through in both
// directions. The test speaks a tiny line-echo protocol over an upgraded
// connection so it needs no websocket library.
func TestUpgradedConnectionPassesThrough(t *testing.T) {
	website := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "echo") {
			http.Error(w, "expected an upgrade", http.StatusBadRequest)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		_ = rw.Flush()
		for {
			line, err := rw.ReadString('\n')
			if err != nil {
				return
			}
			_, _ = rw.WriteString("echo: " + line)
			_ = rw.Flush()
		}
	})
	s := newSetup(t, website, nil, nil)

	conn, err := net.Dial("tcp", strings.TrimPrefix(s.front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	_, _ = fmt.Fprintf(conn, "GET /socket HTTP/1.1\r\nHost: example.org\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading the upgrade response: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}

	for _, msg := range []string{"hello\n", "again\n"} {
		_, _ = io.WriteString(conn, msg)
		line, err := reader.ReadString('\n')
		if err != nil || line != "echo: "+msg {
			t.Fatalf("sent %q, got %q (err %v)", msg, line, err)
		}
	}
}

func TestEnvironmentProxySettingsAreIgnored(t *testing.T) {
	target, _ := url.Parse("http://127.0.0.1:1")
	p := New(Options{Upstream: target, Log: quiet(), DialTimeout: time.Second, ResponseHeaderTimeout: time.Second})
	defer p.Close()
	if p.transport.Proxy != nil {
		t.Error("the upstream transport would honour HTTP_PROXY; the website must be reached directly")
	}
}

func TestUnavailablePageIsSelfContainedAndAccessible(t *testing.T) {
	rec := httptest.NewRecorder()
	writeUnavailable(rec, http.StatusBadGateway)
	page := rec.Body.String()

	for _, want := range []string{`<html lang="de">`, `lang="en"`, "<title>", `name="viewport"`, "<main>"} {
		if !strings.Contains(page, want) {
			t.Errorf("page is missing %s", want)
		}
	}
	if n := strings.Count(page, "<h1"); n != 1 {
		t.Errorf("page has %d h1 headings, want exactly one", n)
	}
	for _, banned := range []string{"<script", "http://", "https://", "src=", "@import", "url("} {
		if strings.Contains(page, banned) {
			t.Errorf("page contains %q: it must load nothing and run nothing", banned)
		}
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
}
