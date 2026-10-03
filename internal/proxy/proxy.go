// Package proxy forwards allowed requests to the protected website.
//
// It is the last stage of the request pipeline. It does not decide anything:
// by the time a request reaches it, the earlier stages have allowed it. Its
// job is to pass the request on faithfully, tell the website who the real
// client is, and answer in a defined way when the website cannot be reached.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/MaMoja/xibalba/internal/clientip"
	"github.com/MaMoja/xibalba/internal/health"
)

// Options configures a Proxy.
type Options struct {
	// Upstream is the base URL of the protected website.
	Upstream *url.URL
	// PreserveHost sends the visitor's Host header upstream instead of the
	// host of Upstream.
	PreserveHost bool
	// DialTimeout bounds connecting to the website.
	DialTimeout time.Duration
	// ResponseHeaderTimeout bounds the wait for the website's response headers.
	ResponseHeaderTimeout time.Duration
	// Unavailable writes the page a visitor sees when the website cannot
	// be reached. If nil, a plain-text status line is sent.
	Unavailable func(w http.ResponseWriter, r *http.Request, status int)
	// Log receives the proxy's messages.
	Log *slog.Logger
}

// Proxy is an http.Handler that forwards requests to one upstream website.
type Proxy struct {
	opts      Options
	log       *slog.Logger
	transport *http.Transport
	handler   *httputil.ReverseProxy

	mu      sync.RWMutex
	lastErr string // why the most recent request to the website failed; "" if it worked
}

// New returns a Proxy for opts.Upstream.
func New(opts Options) *Proxy {
	p := &Proxy{opts: opts, log: opts.Log.With("component", "upstream")}
	p.transport = &http.Transport{
		// The website is reached directly. Proxy settings from the
		// environment (HTTP_PROXY and friends) must never apply here.
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   opts.DialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: opts.ResponseHeaderTimeout,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
	}
	p.handler = &httputil.ReverseProxy{
		Rewrite:        p.rewrite,
		Transport:      p.transport,
		ModifyResponse: p.onResponse,
		ErrorHandler:   p.onError,
		ErrorLog:       slog.NewLogLogger(p.log.Handler(), slog.LevelWarn),
	}
	return p
}

// ServeHTTP forwards the request to the website.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.handler.ServeHTTP(w, r)
}

// Close releases idle connections to the website.
func (p *Proxy) Close() { p.transport.CloseIdleConnections() }

// rewrite prepares the request that is sent to the website.
//
// The reverse proxy has already removed every forwarding header the client
// sent (Forwarded, X-Forwarded-For, -Host, -Proto) before this runs. They are
// rebuilt here from what Xibalba knows, so the website never sees a value a
// client made up.
func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	pr.SetURL(p.opts.Upstream)
	if p.opts.PreserveHost {
		pr.Out.Host = pr.In.Host
	}

	info, _ := clientip.FromContext(pr.In.Context())

	// A trusted proxy in front of Xibalba has already recorded the path the
	// request took. Keep that record and append to it. From anyone else the
	// record starts here.
	if info.PeerTrusted {
		pr.Out.Header["X-Forwarded-For"] = pr.In.Header["X-Forwarded-For"]
	}
	pr.SetXForwarded()
	if info.PeerTrusted {
		// The trusted proxy terminated TLS and saw the original host; its
		// account of both is better than what this hop can observe.
		for _, name := range []string{"X-Forwarded-Proto", "X-Forwarded-Host"} {
			if value := pr.In.Header.Get(name); value != "" {
				pr.Out.Header.Set(name, value)
			}
		}
	}

	// X-Real-IP is always set by Xibalba and never passed through.
	if info.Client.IsValid() {
		pr.Out.Header.Set("X-Real-IP", info.Client.String())
	} else {
		pr.Out.Header.Del("X-Real-IP")
	}
}

// onResponse runs for every response from the website, whatever its status.
// Getting any response means the website is reachable.
func (p *Proxy) onResponse(*http.Response) error {
	if previous := p.setLastErr(""); previous != "" {
		p.log.Info("the website answers again")
	}
	return nil
}

// onError runs when the website could not be reached or did not answer.
func (p *Proxy) onError(w http.ResponseWriter, r *http.Request, err error) {
	// The visitor closed the connection. Nothing is wrong with the website
	// and nobody is left to answer.
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		p.log.Debug("client went away before the website answered")
		return
	}

	status := http.StatusBadGateway
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		status = http.StatusGatewayTimeout
	}
	// One error line when the website stops answering, not one per request:
	// during an outage under load the log must stay readable. Which request
	// failed is not recorded.
	if previous := p.setLastErr(err.Error()); previous == "" {
		p.log.Error("the website stopped answering", "error", err.Error(), "status", status)
	} else {
		p.log.Debug("request to the website failed", "error", err.Error(), "status", status)
	}
	if p.opts.Unavailable != nil {
		p.opts.Unavailable(w, r, status)
		return
	}
	http.Error(w, http.StatusText(status), status)
}

// setLastErr records the outcome of the most recent request to the website
// ("" for success) and returns the outcome before it.
func (p *Proxy) setLastErr(msg string) (previous string) {
	p.mu.RLock()
	previous = p.lastErr
	p.mu.RUnlock()
	if previous == msg {
		return previous // the common case: still healthy, no write lock needed
	}
	p.mu.Lock()
	previous = p.lastErr
	p.lastErr = msg
	p.mu.Unlock()
	return previous
}

// Health reports whether the website answered the most recent request. An
// unreachable website is "degraded", not "down": Xibalba itself works and
// restarting it would not help.
func (p *Proxy) Health() health.Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.lastErr == "" {
		return health.Status{State: health.OK}
	}
	return health.Status{
		State:  health.Degraded,
		Detail: fmt.Sprintf("the last request to %s failed: %s", p.opts.Upstream.Host, p.lastErr),
	}
}
