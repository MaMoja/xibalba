package config

import "fmt"

// Verdict holds the settings of the mode in which the web server in front
// asks Xibalba about each request instead of passing it on (nginx
// auth_request, Caddy forward_auth, Traefik forwardAuth). It is documented
// in docs/VERDICT.md and implemented in internal/verdict.
type Verdict struct {
	// Enabled answers the web server's checks. With it, upstream.url may
	// be left empty: Xibalba then only gives verdicts.
	Enabled bool `yaml:"enabled"`
}

func (c Config) checkVerdict(add func(path, message, hint string)) {
	if !c.Verdict.Enabled {
		return
	}
	if len(c.Server.TrustedProxies) == 0 {
		add("verdict.enabled", "checks are answered only for a trusted proxy, and server.trusted_proxies is empty",
			`name the web server that asks, for example server.trusted_proxies: ["127.0.0.1"]`)
	}
	if c.Upstream.URL != "" {
		return
	}
	if c.Previews.Enabled && len(c.Previews.Tags) == 0 {
		add("previews.enabled", "link previews are fetched from the website, and upstream.url is empty",
			"set upstream.url, or give fixed tags in previews.tags")
	}
	if c.Limits.Enabled {
		for i, w := range c.Limits.Windows {
			if w.Count == "pages" {
				add(fmt.Sprintf("limits.windows[%d].count", i), "pages can only be counted in answers that pass through Xibalba, and upstream.url is empty",
					`use count: requests, or set upstream.url and let Xibalba pass the requests on`)
			}
		}
	}
}
