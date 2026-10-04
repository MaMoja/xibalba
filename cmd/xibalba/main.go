// Command xibalba is the Xibalba server.
//
// main only wires the parts together: it loads the configuration, builds the
// components, hands them to the supervisor and waits for a signal or a
// failure. All behaviour lives in the internal packages.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/MaMoja/xibalba/internal/admin"
	"github.com/MaMoja/xibalba/internal/buildinfo"
	"github.com/MaMoja/xibalba/internal/challenge"
	"github.com/MaMoja/xibalba/internal/changes"
	"github.com/MaMoja/xibalba/internal/clientip"
	"github.com/MaMoja/xibalba/internal/config"
	"github.com/MaMoja/xibalba/internal/crawlers"
	"github.com/MaMoja/xibalba/internal/gate"
	"github.com/MaMoja/xibalba/internal/geo"
	"github.com/MaMoja/xibalba/internal/health"
	"github.com/MaMoja/xibalba/internal/httpserver"
	"github.com/MaMoja/xibalba/internal/license"
	"github.com/MaMoja/xibalba/internal/lifecycle"
	"github.com/MaMoja/xibalba/internal/limit"
	"github.com/MaMoja/xibalba/internal/logging"
	"github.com/MaMoja/xibalba/internal/metrics"
	"github.com/MaMoja/xibalba/internal/origin"
	"github.com/MaMoja/xibalba/internal/pages"
	"github.com/MaMoja/xibalba/internal/proxy"
	"github.com/MaMoja/xibalba/internal/rules"
	"github.com/MaMoja/xibalba/internal/stats"
	"github.com/MaMoja/xibalba/internal/token"
	"github.com/MaMoja/xibalba/internal/trap"
)

// Exit codes.
const (
	exitOK     = 0 // clean shutdown
	exitFailed = 1 // bad configuration, failed start, or a component failed while running
	exitUsage  = 2 // bad command line
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// licenseHealth reports the state of the sponsor license. A license past its
// term never takes Xibalba down; it shows up here as "degraded" so that
// whoever watches the health report learns about it in time.
func licenseHealth(l license.License, usableAtStart bool, now time.Time) health.Status {
	switch l.State(now) {
	case license.Valid:
		return health.Status{State: health.OK}
	case license.Grace:
		return health.Status{State: health.Degraded,
			Detail: fmt.Sprintf("the sponsor license expired on %s; it keeps working until %s, please renew it", l.Expires, l.GraceEnds())}
	default:
		detail := fmt.Sprintf("the sponsor license expired on %s; the pages use the standard wording and show the Xibalba line", l.Expires)
		if usableAtStart {
			detail = fmt.Sprintf("the sponsor license expired on %s; from the next restart the pages use the standard wording and show the Xibalba line", l.Expires)
		}
		return health.Status{State: health.Degraded, Detail: detail}
	}
}

// crawlerOf translates what the crawler registry found into what rules test.
func crawlerOf(id crawlers.Identity) rules.Crawler {
	c := rules.Crawler{Name: id.Name, Class: string(id.Class)}
	switch id.Status {
	case crawlers.NotACrawler:
		return rules.Crawler{}
	case crawlers.Verified:
		c.Status = rules.CrawlerVerified
	case crawlers.Unverified:
		c.Status = rules.CrawlerImpostor
	default: // cannot be verified, or not verified yet
		c.Status = rules.CrawlerUnknown
	}
	return c
}

// withTrap sends requests for the trap's addresses to the trap and the rest
// of Xibalba's own address space to next.
func withTrap(snare, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, trap.Prefix) {
			snare.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// route sends requests for Xibalba's own address space to own and everything
// else to site.
func route(own, site http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A request target that is not a path ("GET http:admin/x", "*")
		// has no path for the rules to test, yet would be passed on as
		// written. No browser sends one; refuse it.
		if r.URL.Opaque != "" || !strings.HasPrefix(r.URL.Path, "/") {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		if strings.HasPrefix(r.URL.Path, challenge.Prefix) {
			own.ServeHTTP(w, r)
			return
		}
		site.ServeHTTP(w, r)
	})
}

// signingKey returns the key that signs challenge tokens: the one stored in
// path, created there if it does not exist yet, or a fresh one that lives
// only as long as this process if no path is configured. note is something
// worth telling the administrator, or empty.
func signingKey(path string) (key []byte, note string, err error) {
	if path == "" {
		key, err = token.NewKey()
		return key, "", err
	}
	key, created, err := token.LoadOrCreateKey(path)
	if err != nil {
		return nil, "", err
	}
	if created {
		note = "created a new signing key"
	}
	return key, note, nil
}

// askHealth asks the operations listener of a running Xibalba for its
// health. It is what a container's health check calls, since the image
// holds no other program that could make the request.
func askHealth(listen string, stdout, stderr io.Writer) int {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + listen + "/healthz")
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "not reachable:", err)
		return exitFailed
	}
	defer func() { _ = resp.Body.Close() }()
	var report struct {
		State string `json:"state"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&report)
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(stderr, "not healthy: status %d, state %q\n", resp.StatusCode, report.State)
		return exitFailed
	}
	_, _ = fmt.Fprintln(stdout, report.State)
	return exitOK
}

// adminHosts are the names the web interface answers to: those the site
// owner listed, and the host it listens on if that is a specific one.
func adminHosts(a config.Admin) []string {
	hosts := append([]string(nil), a.Hostnames...)
	if host, _, err := net.SplitHostPort(a.Listen); err == nil && host != "" && host != "0.0.0.0" && host != "::" {
		hosts = append(hosts, host)
	}
	return hosts
}

// run is the whole program. It takes its inputs as arguments so tests can call it.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("xibalba", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "xibalba.yaml", "path to the configuration file")
	checkOnly := flags.Bool("check", false, "validate the configuration file and exit")
	showVersion := flags.Bool("version", false, "print the version and exit")
	healthCheck := flags.Bool("healthcheck", false, "ask the running Xibalba of this configuration whether it is healthy, and exit with 0 or 1")
	newPassword := flags.Bool("set-password", false, "set the password of the web interface and exit")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if *newPassword {
		return setPassword(ctx, *configPath, os.Stdin, stdout, stderr)
	}

	if *showVersion {
		_, _ = fmt.Fprintln(stdout, buildinfo.Get())
		return exitOK
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return exitFailed
	}
	if *healthCheck {
		return askHealth(cfg.Ops.Listen, stdout, stderr)
	}
	if *checkOnly {
		_, _ = fmt.Fprintf(stdout, "configuration %s is valid\n", *configPath)
		return exitOK
	}

	log := logging.New(cfg.Log, stderr)
	registry := health.NewRegistry()
	supervisor := lifecycle.New(log)

	// Operations listener: health and version. Metrics join it in a later milestone.
	opsMux := http.NewServeMux()
	opsMux.Handle("GET /healthz", registry.Handler())
	opsMux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(buildinfo.Get())
	})
	ops := httpserver.New(httpserver.Options{
		Name:      "ops",
		Addr:      cfg.Ops.Listen,
		Handler:   opsMux,
		Log:       log,
		OnFailure: supervisor.Reporter("ops"),
	})
	registry.Register(ops.Name(), ops.Health)
	supervisor.Add(ops)

	// The trap, if switched on: a hidden link in the visitor pages.
	var snare *trap.Trap
	pageOptions := cfg.PageOptions()
	if cfg.Trap.Enabled {
		snare, err = trap.New(trap.Options{Remember: cfg.Trap.Remember, MaxClients: cfg.Trap.MaxClients, Maze: cfg.Trap.Maze})
		if err != nil {
			log.Error("start-up failed", "error", err.Error(), "component", "trap")
			return exitFailed
		}
		pageOptions.TrapLink = snare.Link
		supervisor.Add(snare)
		registry.Register(snare.Name(), func() health.Status { return health.Status{State: health.OK} })
		opsMux.Handle("GET /trap", snare.ReportHandler())
	}

	// The pages Xibalba itself shows to visitors.
	page, err := pages.New(pageOptions)
	if err != nil {
		log.Error("start-up failed", "error", "visitor pages: "+err.Error())
		return exitFailed
	}

	if found := cfg.License.Info; found != nil {
		usableAtStart := cfg.License.Usable()
		registry.Register("license", func() health.Status { return licenseHealth(*found, usableAtStart, time.Now()) })
	}

	// The rule set was checked when the configuration was loaded, so
	// compiling it here cannot report problems; if it does, refuse to start.
	spec, err := cfg.RuleSpec(cfg.Admin.Changes, time.Now())
	if err != nil {
		log.Error("start-up failed", "error", err.Error())
		return exitFailed
	}
	engine, problems := rules.Compile(spec)
	// With changes allowed, a preset switched on later may ask which
	// crawler a request is or whether a client was trapped, so those parts
	// run from the start.
	mayChange := cfg.Admin.Enabled && cfg.Admin.AllowChanges
	if len(problems) > 0 {
		log.Error("start-up failed", "error", fmt.Sprintf("the rule set does not compile: %+v", problems))
		return exitFailed
	}

	// Public side. A request passes the stages in this order:
	//   client identity -> rules -> (challenge: milestone M3) -> website
	upstream := proxy.New(proxy.Options{
		Unavailable:           page.Unavailable,
		Upstream:              cfg.Upstream.Target(),
		PreserveHost:          cfg.Upstream.PreserveHost,
		DialTimeout:           cfg.Upstream.DialTimeout,
		ResponseHeaderTimeout: cfg.Upstream.ResponseHeaderTimeout,
		Log:                   log,
	})
	defer upstream.Close()
	registry.Register("upstream", upstream.Health)

	// The security check. Its tokens are signed with a key that is kept in a
	// file if one is configured, so passes survive a restart.
	key, keyNote, err := signingKey(cfg.Challenge.KeyPath)
	if err != nil {
		log.Error("start-up failed", "error", err.Error(), "component", "challenge")
		return exitFailed
	}
	signer, err := token.NewSigner(key)
	if err != nil {
		log.Error("start-up failed", "error", err.Error(), "component", "challenge")
		return exitFailed
	}
	check := challenge.New(challenge.Options{
		Signer:            signer,
		Difficulty:        cfg.Challenge.Difficulty,
		AllowButton:       cfg.Challenge.NoJavaScript == "button",
		Wait:              cfg.Challenge.Wait,
		ChallengeLifetime: cfg.Challenge.ChallengeLifetime,
		PassLifetime:      cfg.Challenge.PassLifetime,
		BindNetwork:       cfg.Challenge.BindNetwork,
		CookieName:        cfg.Challenge.CookieName,
		Page: func(w http.ResponseWriter, r *http.Request, v challenge.View) {
			page.Challenge(w, r, pages.ChallengeView{
				Action: v.Action, Token: v.Token, Return: v.Return,
				Nonce: v.Nonce, Difficulty: v.Difficulty,
				AllowButton: v.AllowButton, Notice: string(v.Message),
			})
		},
		Log: log,
	})

	// Crawler identity. The definitions are always known and listed; the
	// background work (address lists, DNS) only runs if a rule asks which
	// crawler a request is, so an installation without such rules makes no
	// outgoing connection.
	known := crawlers.New(crawlers.Options{
		Definitions:     cfg.Crawlers.Definitions,
		Refresh:         cfg.Crawlers.Refresh,
		RefreshInterval: cfg.Crawlers.RefreshInterval,
		CacheDir:        cfg.Crawlers.CachePath,
		UserAgent:       "Xibalba/" + buildinfo.Get().Version + " (+https://github.com/MaMoja/xibalba)",
		Log:             log,
	})
	opsMux.Handle("GET /crawlers", known.Handler())
	var identify func(string, netip.Addr) rules.Crawler
	if engine.UsesCrawlers() || mayChange {
		registry.Register(known.Name(), known.Health)
		supervisor.Add(known)
		identify = func(userAgent string, client netip.Addr) rules.Crawler {
			return crawlerOf(known.Identify(userAgent, client))
		}
	}

	// Request limits, if switched on.
	var limitFn func(netip.Addr) (bool, bool, time.Duration)
	var pageFn func(netip.Addr, string, string)
	var limiter *limit.Limiter
	if cfg.Limits.Enabled {
		limiter = limit.New(cfg.Limits.Options())
		supervisor.Add(limiter)
		registry.Register(limiter.Name(), func() health.Status { return health.Status{State: health.OK} })
		opsMux.Handle("GET /limits", limiter.Handler())
		for _, w := range cfg.Limits.Windows {
			if w.Count == "pages" { // answers are only looked at if a limit counts pages
				pageFn = limiter.Page
			}
		}
		limitFn = func(client netip.Addr) (bool, bool, time.Duration) {
			v := limiter.Count(client)
			return v.Over, v.Action == "deny", v.RetryAfter
		}
	}

	// Countries. The database is only loaded, and only downloaded, if a
	// rule asks for a country.
	var country func(netip.Addr) ([2]byte, bool)
	if engine.UsesCountries() {
		locator := geo.New(geo.Options{
			Path:        cfg.Countries.Path,
			Download:    cfg.Countries.Download,
			DownloadURL: cfg.Countries.DownloadURL,
			UserAgent:   "Xibalba/" + buildinfo.Get().Version + " (+https://github.com/MaMoja/xibalba)",
			Log:         log,
		})
		registry.Register(locator.Name(), locator.Health)
		supervisor.Add(locator)
		country = func(client netip.Addr) ([2]byte, bool) { return locator.Country(client), locator.Loaded() }
	}

	var trapped func(netip.Addr) bool
	own := check.Handler()
	if snare != nil {
		if engine.UsesTrap() || mayChange { // nobody asks otherwise; spare every request the lookup
			trapped = snare.Caught
		}
		own = withTrap(snare.Handler(), own)
	}

	// Counts per network of origin, if the site owner switched them on.
	var networks *origin.Counter
	var originFn func(netip.Addr, string)
	if cfg.Statistics.Networks.Enabled && cfg.Statistics.Path != "" {
		networks = origin.New(origin.MaxNetworks)
		originFn = networks.Note
	}

	decisions := gate.New(gate.Options{
		Origin:      originFn,
		Trapped:     trapped,
		Country:     country,
		Engine:      engine,
		Identify:    identify,
		Limit:       limitFn,
		Page:        pageFn,
		Limited:     page.Limited,
		DryRun:      cfg.Rules.DryRun,
		FailOpen:    cfg.Rules.OnError == "allow",
		Challenge:   check,
		Next:        check.StripPass(upstream), // the website never sees the pass cookie
		Blocked:     page.Blocked,
		Unavailable: page.Unavailable,
		Log:         log,
	})
	registry.Register("rules", decisions.Health)
	opsMux.Handle("GET /decisions", decisions.Handler())

	// The same numbers for a monitoring system.
	numbers := metrics.New()
	sources := parts{started: time.Now(), health: registry, decisions: decisions, crawlers: known, limiter: limiter, snare: snare}
	collect(numbers, sources)
	opsMux.Handle("GET /metrics", numbers.Handler())

	// And kept on disk by the hour, if a directory is configured.
	var history func(from, to time.Time) []admin.Hour
	if cfg.Statistics.Path != "" {
		kept := stats.New(stats.Options{
			Dir:      cfg.Statistics.Path,
			KeepDays: cfg.Statistics.KeepDays,
			Collect:  func() map[string]uint64 { return totals(sources) },
			Log:      log,
		})
		registry.Register(kept.Name(), kept.Health)
		supervisor.Add(kept)
		opsMux.Handle("GET /statistics", kept.Handler())
		history = func(from, to time.Time) []admin.Hour {
			hours := kept.Hours(from, to)
			out := make([]admin.Hour, len(hours))
			for i, h := range hours {
				out[i] = admin.Hour{Start: h.Hour, Counts: h.Counts}
			}
			return out
		}
		if networks == nil && stats.Remove(cfg.Statistics.NetworksPath()) {
			log.Info("counts per network are switched off; those kept from earlier were removed", "component", "statistics")
		}
		if networks != nil {
			top := cfg.Statistics.Networks.Top
			perNetwork := stats.New(stats.Options{
				Name:     "statistics-networks",
				Dir:      cfg.Statistics.NetworksPath(),
				Create:   true,
				KeepDays: cfg.Statistics.Networks.KeepDays,
				Added:    networks.Drain,
				Reduce:   func(counts map[string]uint64) { origin.Top(counts, top) },
				ReduceAt: 3 * top * len(origin.Actions),
				Log:      log,
			})
			registry.Register(perNetwork.Name(), perNetwork.Health)
			supervisor.Add(perNetwork)
			opsMux.Handle("GET /statistics/networks", perNetwork.Handler())
		}
	}

	// Changes made in the web interface: applied on top of the
	// configuration, kept in their own file, in force as long as it exists.
	changed := changes.NewStore(changes.Options{
		Path:      cfg.Admin.ChangesPath,
		Initial:   cfg.Admin.Changes,
		Presets:   config.PresetNames(),
		Protected: cfg.Server.TrustedPrefixes(),
		Apply: func(state changes.State) error {
			spec, err := cfg.RuleSpec(state, time.Now())
			if err != nil {
				return err
			}
			next, problems := rules.Compile(spec)
			if len(problems) > 0 {
				return errors.New(problems[0].Message)
			}
			decisions.Swap(next)
			allow, deny := state.Active(time.Now())
			log.Info("rule set replaced", "component", "changes", "rules", next.Len(),
				"presets", strings.Join(cfg.Rules.EffectivePresets(state), ","), "addresses_allowed", len(allow), "addresses_blocked", len(deny))
			return nil
		},
	})
	if !cfg.Admin.Changes.Empty() || mayChange {
		registry.Register("changes", changed.Health)
		supervisor.Add(changes.NewWatcher(changed))
	}
	if !cfg.Admin.Changes.Empty() {
		log.Info("changes made in the web interface are in force", "component", "changes", "file", cfg.Admin.ChangesFile,
			"presets_changed", len(cfg.Admin.Changes.Presets), "addresses", len(cfg.Admin.Changes.Addresses))
	}
	var editable admin.Settings
	if mayChange {
		editable = settings{cfg: &cfg, store: changed}
	}

	// The web interface, if the site owner switched it on. Off, none of
	// it exists: no listener, no templates in memory.
	if cfg.Admin.Enabled {
		ui, err := admin.New(admin.Options{
			Password:        cfg.Admin.Password,
			SessionLifetime: cfg.Admin.SessionLifetime,
			Hosts:           adminHosts(cfg.Admin),
			SecureCookie:    cfg.Admin.SecureCookie,
			Settings:        editable,
			History:         history,
			Live:            func() map[string]uint64 { return totals(sources) },
			Health:          registry.Report,
			Version:         buildinfo.Get().Version,
			DryRun:          cfg.Rules.DryRun,
			Log:             log,
		})
		if err != nil {
			log.Error("start-up failed", "error", "web interface: "+err.Error(), "component", "admin")
			return exitFailed
		}
		adminServer := httpserver.New(httpserver.Options{
			Name:      "admin",
			Addr:      cfg.Admin.Listen,
			Handler:   httpserver.Recover(log, ui.Handler()),
			Log:       log,
			OnFailure: supervisor.Reporter("admin"),
		})
		if cfg.Admin.PasswordOpen {
			log.Warn("the password file of the web interface can be read by other users of this machine; restrict it: chmod 600",
				"component", "admin", "file", cfg.Admin.PasswordFile)
		}
		registry.Register(adminServer.Name(), adminServer.Health)
		supervisor.Add(adminServer)
		if host, _, err := net.SplitHostPort(cfg.Admin.Listen); err == nil && host != "localhost" {
			if addr, err := netip.ParseAddr(host); err != nil || !addr.IsLoopback() {
				log.Warn("the web interface is reachable from other machines and its connection is not encrypted: "+
					"put a web server with HTTPS in front, or reach it through an SSH tunnel", "component", "admin", "listen", cfg.Admin.Listen)
			}
		}
	}

	resolver := clientip.New(cfg.Server.TrustedPrefixes())
	public := httpserver.New(httpserver.Options{
		Name:              "public",
		Addr:              cfg.Server.Listen,
		Handler:           clientip.Middleware(resolver, route(own, decisions)),
		Log:               log,
		OnFailure:         supervisor.Reporter("public"),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		// Downloads, uploads, streams and websockets have no natural upper
		// bound, so the whole-request timeouts are off for this listener.
		ReadTimeout:  httpserver.NoTimeout,
		WriteTimeout: httpserver.NoTimeout,
	})
	registry.Register(public.Name(), public.Health)
	supervisor.Add(public)

	if err := supervisor.Start(ctx); err != nil {
		log.Error("start-up failed", "error", err.Error())
		return exitFailed
	}
	log.Info("xibalba started",
		"version", buildinfo.Get().Version,
		"public", public.Addr(),
		"ops", ops.Addr(),
		"upstream", cfg.Upstream.Target().Redacted(),
		"trusted_proxies", len(cfg.Server.TrustedProxies),
		"rules", engine.Len(),
		"dry_run", cfg.Rules.DryRun,
		"crawlers", len(cfg.Crawlers.Definitions),
		"limits", cfg.Limits.Enabled,
	)
	if found := cfg.License.Info; found != nil {
		switch cfg.License.State {
		case license.Valid:
			log.Info("sponsor license", "component", "license", "licensee", found.Licensee, "valid_until", found.Expires)
		case license.Grace:
			log.Warn("the sponsor license has expired; it keeps working for a grace period, please renew it",
				"component", "license", "licensee", found.Licensee, "expired", found.Expires, "works_until", found.GraceEnds())
		default:
			log.Warn("the sponsor license has expired: the pages use the standard wording and show the Xibalba line",
				"component", "license", "licensee", found.Licensee, "expired", found.Expires,
				"settings_not_applied", strings.Join(cfg.SponsorSettings(), ", "))
		}
	}
	if host, _, err := net.SplitHostPort(cfg.Ops.Listen); err == nil && host != "localhost" {
		if addr, err := netip.ParseAddr(host); err != nil || !addr.IsLoopback() {
			log.Warn("the operations listener is reachable from other machines and has no login: "+
				"make sure only your monitoring can reach it", "component", "ops", "listen", cfg.Ops.Listen)
		}
	}
	if (cfg.Limits.Enabled || cfg.Trap.Enabled || cfg.Statistics.Networks.Enabled) && len(cfg.Server.TrustedProxies) == 0 {
		if host, _, err := net.SplitHostPort(cfg.Server.Listen); err == nil {
			addr, err := netip.ParseAddr(host)
			internal := host == "localhost" || (err == nil && (addr.IsLoopback() || addr.IsPrivate()))
			if internal || host == "" || (err == nil && addr.IsUnspecified()) {
				log.Warn("request limits, the trap or counts per network are on and server.trusted_proxies is empty: "+
					"if a web server stands in front, all visitors appear as that one address, share one limit, are caught together and count as one network",
					"component", "public", "listen", cfg.Server.Listen)
			}
		}
	}
	if cfg.Rules.DryRun {
		log.Warn("dry run: decisions are counted but nothing is blocked", "component", "rules")
	}
	switch {
	case keyNote != "":
		log.Info(keyNote, "component", "challenge", "key_file", cfg.Challenge.KeyPath)
	case cfg.Challenge.KeyPath == "" && engine.Uses(rules.Challenge):
		log.Warn("no challenge.key_file is set: the signing key is new at every start, so every visitor is checked again after a restart",
			"component", "challenge")
	}

	code := exitOK
	select {
	case <-ctx.Done():
		log.Info("shutdown requested")
	case failure := <-supervisor.Failures():
		log.Error("component failed, shutting down", "component", failure.Component, "error", failure.Err.Error())
		code = exitFailed
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := supervisor.Stop(stopCtx); err != nil {
		log.Error("shutdown was not clean", "error", err.Error())
		code = exitFailed
	}
	log.Info("xibalba stopped")
	return code
}
