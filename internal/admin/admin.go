// Package admin serves the web interface: a login and an overview of what
// Xibalba counted. It is optional and only reads; it changes nothing.
//
// The pages are rendered on the server from embedded templates. There is no
// script, no external file and no build chain. A page costs nothing while
// nobody looks at it.
package admin

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MaMoja/xibalba/internal/health"
)

//go:embed assets
var assets embed.FS

var languages = []string{"en", "de"}

const (
	cookieName  = "xibalba_admin"
	maxSessions = 64
	// After freeFailures wrong passwords from one address, that address
	// has to wait: firstWait, doubling up to maxWait.
	freeFailures = 5
	firstWait    = 30 * time.Second
	maxWait      = 15 * time.Minute
	maxThrottled = 1024
	topRules     = 15
	csp          = "default-src 'none'; style-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"
)

// Hour is what was counted in one hour, by name. The names are those of the
// statistics: "decision|<source>|<action>", "challenge|served", and so on.
type Hour struct {
	Start  time.Time
	Counts map[string]uint64
}

// Options configures the interface.
type Options struct {
	// Password is the stored password.
	Password Hash
	// SessionLifetime is how long a login lasts.
	SessionLifetime time.Duration
	// History returns the hours kept on disk between from and to. Nil if
	// nothing is kept; the overview then shows the counts since the start.
	History func(from, to time.Time) []Hour
	// Live returns the counts since the start, by name.
	Live func() map[string]uint64
	// Health returns the state of the parts.
	Health func() health.Report
	// Version is the version of the running program.
	Version string
	// DryRun says that nothing is being blocked.
	DryRun bool
	// Log receives messages. A password or session is never logged.
	Log *slog.Logger
	// Now returns the current time. Tests replace it; nil means time.Now.
	Now func() time.Time
}

// Admin is the web interface. Use Handler to serve it.
type Admin struct {
	opts  Options
	log   *slog.Logger
	pages *template.Template
	texts map[string]map[string]string
	style []byte

	checking sync.Mutex // one password check at a time: a check is costly on purpose

	mu       sync.Mutex
	sessions map[string]time.Time // session -> end
	failures map[string]*failure  // address -> wrong passwords
}

type failure struct {
	count int
	until time.Time // no attempt before this moment
	last  time.Time
}

// New builds the interface.
func New(opts Options) (*Admin, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.SessionLifetime <= 0 {
		opts.SessionLifetime = 12 * time.Hour
	}
	a := &Admin{opts: opts, log: opts.Log.With("component", "admin"), texts: map[string]map[string]string{},
		sessions: map[string]time.Time{}, failures: map[string]*failure{}}
	var err error
	if a.pages, err = template.ParseFS(assets, "assets/*.html"); err != nil {
		return nil, err
	}
	if a.style, err = assets.ReadFile("assets/style.css"); err != nil {
		return nil, err
	}
	for _, lang := range languages {
		raw, err := assets.ReadFile("assets/locales/" + lang + ".json")
		if err != nil {
			return nil, err
		}
		var t map[string]string
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("texts %s: %w", lang, err)
		}
		a.texts[lang] = t
	}
	for key := range a.texts["en"] {
		for _, lang := range languages {
			if a.texts[lang][key] == "" {
				return nil, fmt.Errorf("texts %s: %q is missing", lang, key)
			}
		}
	}
	return a, nil
}

// Handler serves the interface.
func (a *Admin) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /assets/style.css", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "max-age=3600")
		_, _ = w.Write(a.style)
	})
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		if a.signedIn(r) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		a.login(w, r, http.StatusOK, "")
	})
	mux.HandleFunc("POST /login", a.signIn)
	mux.HandleFunc("POST /logout", a.signOut)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if !a.signedIn(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		a.overview(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		// same-origin, not no-referrer: with no-referrer a browser sends
		// "Origin: null" with the forms, and they could not be told from a
		// request of another site.
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

// sameOrigin reports whether a request that changes something comes from
// the interface's own pages.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host
	}
	return true
}

func clientOf(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *Admin) signIn(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	client, now := clientOf(r), a.opts.Now()
	t := a.texts[pickLanguage(r.Header.Get("Accept-Language"))]

	a.mu.Lock()
	var until time.Time
	for _, key := range []string{client, ""} { // "" is shared by all when the table is full
		if f := a.failures[key]; f != nil && f.until.After(until) {
			until = f.until
		}
	}
	a.mu.Unlock()
	if now.Before(until) {
		w.Header().Set("Retry-After", strconv.Itoa(int(until.Sub(now).Seconds())+1))
		a.login(w, r, http.StatusTooManyRequests, t["too_many"])
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	password := r.PostFormValue("password")
	a.checking.Lock()
	ok := a.opts.Password.Matches(password)
	a.checking.Unlock()

	if !ok {
		a.mu.Lock()
		f := a.failures[client]
		if f == nil {
			if len(a.failures) >= maxThrottled {
				for k, old := range a.failures { // make room: forget those whose wait is over
					if now.After(old.until) && now.Sub(old.last) > maxWait {
						delete(a.failures, k)
					}
				}
			}
			if len(a.failures) >= maxThrottled { // still full: everyone new shares one entry
				client = ""
			}
			if f = a.failures[client]; f == nil {
				f = &failure{}
				a.failures[client] = f
			}
		}
		if now.Sub(f.last) > maxWait {
			f.count = 0
		}
		f.count++
		f.last = now
		if over := f.count - freeFailures; over >= 0 {
			wait := maxWait
			if over < 6 {
				wait = min(firstWait<<over, maxWait)
			}
			f.until = now.Add(wait)
		}
		a.mu.Unlock()
		a.log.Warn("sign-in with a wrong password")
		a.login(w, r, http.StatusUnauthorized, t["wrong_password"])
		return
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	session := base64.RawURLEncoding.EncodeToString(raw)
	a.mu.Lock()
	delete(a.failures, client)
	for id, end := range a.sessions {
		if now.After(end) {
			delete(a.sessions, id)
		}
	}
	for len(a.sessions) >= maxSessions { // drop the one that ends first
		first := ""
		for id, end := range a.sessions {
			if first == "" || end.Before(a.sessions[first]) {
				first = id
			}
		}
		delete(a.sessions, first)
	}
	a.sessions[session] = now.Add(a.opts.SessionLifetime)
	a.mu.Unlock()

	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: session, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: int(a.opts.SessionLifetime.Seconds())})
	a.log.Info("signed in")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *Admin) signOut(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if c, err := r.Cookie(cookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *Admin) signedIn(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return false
	}
	a.mu.Lock()
	end, ok := a.sessions[c.Value] // a map lookup: the session is 256 random bits, timing tells nothing useful
	if ok && a.opts.Now().After(end) {
		delete(a.sessions, c.Value)
		ok = false
	}
	a.mu.Unlock()
	return ok
}

func (a *Admin) login(w http.ResponseWriter, r *http.Request, status int, message string) {
	lang := pickLanguage(r.Header.Get("Accept-Language"))
	a.render(w, status, "login.html", map[string]any{"Lang": lang, "T": a.texts[lang], "Error": message})
}

func (a *Admin) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := a.pages.ExecuteTemplate(&buf, name, data); err != nil {
		a.log.Error("a page could not be built", "page", name, "error", err.Error())
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// pickLanguage chooses German if the browser lists it before English.
func pickLanguage(header string) string {
	for i, part := range strings.Split(header, ",") {
		if i >= 16 {
			break
		}
		tag, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		base, _, _ := strings.Cut(strings.ToLower(tag), "-")
		for _, lang := range languages {
			if base == lang {
				return lang
			}
		}
	}
	return languages[0]
}

// The overview.

type rangeLink struct {
	ID, Label string
	Current   bool
}

type ruleRow struct{ Name, Action, Count string }

type crawlerRow struct {
	Name                          string
	Verified, Impostor, Unchecked string
	total                         uint64
}

type otherRow struct{ Name, Count string }

type partRow struct{ Name, State, Detail string }

type bar struct{ Class, X, Y, W, H string }

type chartRow struct{ Label, Allowed, Challenged, Denied string }

type chart struct {
	Width, Height int
	Bars          []bar
	Rows          []chartRow
}

var ranges = []struct {
	id    string
	hours int
	daily bool
}{{"day", 24, false}, {"week", 7 * 24, true}, {"month", 30 * 24, true}}

func (a *Admin) overview(w http.ResponseWriter, r *http.Request) {
	lang := pickLanguage(r.Header.Get("Accept-Language"))
	t := a.texts[lang]
	n := func(v uint64) string { return number(v, lang) }
	now := a.opts.Now().UTC()

	chosen := ranges[0]
	for _, candidate := range ranges {
		if r.URL.Query().Get("range") == candidate.id {
			chosen = candidate
		}
	}

	data := map[string]any{"Lang": lang, "T": t, "Version": a.opts.Version, "DryRun": a.opts.DryRun,
		"History": a.opts.History != nil, "RangeLabel": t["since_start"]}
	totals := map[string]uint64{}
	if a.opts.History == nil {
		if a.opts.Live != nil {
			totals = a.opts.Live()
		}
	} else {
		var links []rangeLink
		for _, candidate := range ranges {
			links = append(links, rangeLink{ID: candidate.id, Label: t["range_"+candidate.id], Current: candidate.id == chosen.id})
		}
		data["Ranges"], data["RangeLabel"] = links, t["range_"+chosen.id]

		to := now.Truncate(time.Hour)
		from := to.Add(-time.Duration(chosen.hours-1) * time.Hour)
		step, layout, unit := time.Hour, "02.01. 15:04", t["unit_hour"]
		if chosen.daily {
			from = to.Truncate(24*time.Hour).AddDate(0, 0, -(chosen.hours/24 - 1))
			step, layout, unit = 24*time.Hour, "02.01.2006", t["unit_day"]
		}
		slots := make([][3]uint64, int(to.Sub(from)/step)+1)
		for _, hour := range a.opts.History(from, to) {
			slot := int(hour.Start.Sub(from) / step)
			if slot < 0 || slot >= len(slots) {
				continue
			}
			for name, count := range hour.Counts {
				totals[name] += count
				if _, action, ok := decision(name); ok {
					switch action {
					case "allow":
						slots[slot][0] += count
					case "challenge":
						slots[slot][1] += count
					case "deny":
						slots[slot][2] += count
					}
				}
			}
		}
		data["Chart"] = draw(slots, from, step, layout, n)
		data["ChartHint"] = strings.ReplaceAll(t["chart_hint"], "{unit}", unit)
	}

	var allowed, challenged, denied uint64
	type rule struct {
		name, action string
		count        uint64
	}
	var rules []rule
	crawlers := map[string]*crawlerRow{}
	counts := map[string]*[3]uint64{}
	var other []otherRow
	names := make([]string, 0, len(totals))
	for name := range totals {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		count := totals[name]
		if source, action, ok := decision(name); ok {
			switch action {
			case "allow":
				allowed += count
			case "challenge":
				challenged += count
			case "deny":
				denied += count
			}
			if count > 0 {
				if source == "default" {
					source = t["source_default"]
				}
				label := t["action_"+action]
				if label == "" {
					label = action
				}
				rules = append(rules, rule{strings.TrimPrefix(source, "rule:"), label, count})
			}
			continue
		}
		parts := strings.Split(name, "|")
		switch {
		case parts[0] == "crawler" && len(parts) == 3:
			if crawlers[parts[1]] == nil {
				crawlers[parts[1]], counts[parts[1]] = &crawlerRow{Name: parts[1]}, &[3]uint64{}
			}
			c := counts[parts[1]]
			switch parts[2] {
			case "verified":
				c[0] += count
			case "impostor":
				c[1] += count
			default:
				c[2] += count
			}
			crawlers[parts[1]].total += count
		case parts[0] == "limit" && len(parts) == 4 && count > 0:
			label := parts[1]
			if parts[2] == "pages" {
				label += ", " + parts[2]
			}
			other = append(other, otherRow{strings.ReplaceAll(t["over_limit"], "{name}", label+" ("+parts[3]+")"), n(count)})
		case name == "trap|hits" && count > 0:
			other = append(other, otherRow{t["trap_hits"], n(count)})
		}
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].count > rules[j].count })
	if len(rules) > topRules {
		rules = rules[:topRules]
	}
	var ruleRows []ruleRow
	for _, rule := range rules {
		ruleRows = append(ruleRows, ruleRow{rule.name, rule.action, n(rule.count)})
	}
	var crawlerRows []crawlerRow
	for name, row := range crawlers {
		if row.total == 0 {
			continue
		}
		c := counts[name]
		row.Verified, row.Impostor, row.Unchecked = n(c[0]), n(c[1]), n(c[2])
		crawlerRows = append(crawlerRows, *row)
	}
	sort.Slice(crawlerRows, func(i, j int) bool {
		if crawlerRows[i].total != crawlerRows[j].total {
			return crawlerRows[i].total > crawlerRows[j].total
		}
		return crawlerRows[i].Name < crawlerRows[j].Name
	})

	var parts []partRow
	if a.opts.Health != nil {
		report := a.opts.Health()
		for name, status := range report.Components {
			parts = append(parts, partRow{name, t["state_"+string(status.State)], status.Detail})
		}
		sort.Slice(parts, func(i, j int) bool { return parts[i].Name < parts[j].Name })
	}

	data["Allowed"], data["Challenged"], data["Denied"] = n(allowed), n(challenged), n(denied)
	data["Rules"], data["Crawlers"], data["Other"], data["Parts"] = ruleRows, crawlerRows, other, parts
	data["Check"] = map[string]string{"Served": n(totals["challenge|served"]), "Solved": n(totals["challenge|solved"]),
		"Failed": n(totals["challenge|failed"]), "Passed": n(totals["challenge|passed"])}
	a.render(w, http.StatusOK, "overview.html", data)
}

// decision takes "decision|<source>|<action>" apart. A rule name may hold
// a "|" itself; the action is what follows the last one.
func decision(name string) (source, action string, ok bool) {
	rest, found := strings.CutPrefix(name, "decision|")
	i := strings.LastIndexByte(rest, '|')
	if !found || i < 0 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

// draw turns the slots into stacked bars and the rows of the table.
func draw(slots [][3]uint64, from time.Time, step time.Duration, layout string, n func(uint64) string) *chart {
	const width, height, pad = 720, 200, 8
	c := &chart{Width: width, Height: height}
	var highest uint64
	for _, s := range slots {
		highest = max(highest, s[0]+s[1]+s[2])
	}
	slotWidth := float64(width-2*pad) / float64(len(slots))
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
	for i, s := range slots {
		c.Rows = append(c.Rows, chartRow{from.Add(time.Duration(i) * step).Format(layout), n(s[0]), n(s[1]), n(s[2])})
		if highest == 0 {
			continue
		}
		y := float64(height - pad)
		for k, class := range [3]string{"allow", "check", "deny"} {
			if s[k] == 0 {
				continue
			}
			h := max(float64(s[k])/float64(highest)*float64(height-2*pad), 1) // something counted is always visible
			y -= h
			c.Bars = append(c.Bars, bar{class, f(float64(pad) + float64(i)*slotWidth + slotWidth*0.1), f(y), f(slotWidth * 0.8), f(h)})
		}
	}
	return c
}

// number writes v with thousands separators as the language expects.
func number(v uint64, lang string) string {
	s := strconv.FormatUint(v, 10)
	sep := ","
	if lang == "de" {
		sep = "."
	}
	var b strings.Builder
	for i, digit := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(sep)
		}
		b.WriteRune(digit)
	}
	return b.String()
}
