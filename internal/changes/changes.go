// Package changes keeps what the site owner changed in the web interface:
// presets switched on or off, and addresses let through or blocked.
//
// The changes live in one small file beside the configuration. They are
// applied on top of the configuration file, which is never rewritten.
package changes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"reflect"
	"unicode"
	"unicode/utf8"

	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/MaMoja/xibalba/internal/health"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// MaxAddresses is how many addresses and networks can be listed. Each
	// request is compared with all of them.
	MaxAddresses = 500
	// MaxNote is the longest note, in characters.
	MaxNote = 200
	// MaxLifetime is the longest time an entry can be set to last.
	MaxLifetime = 366 * 24 * time.Hour
	// maxFile is the largest changes file. The largest State that passes
	// Check is well below it: eleven rule texts, 500 entries.
	maxFile = 2 << 20
	// MaxRules is the largest text of own rules, in bytes.
	MaxRules = 32 << 10
	// MaxVersions is how many earlier versions are kept to go back to.
	MaxVersions = 10
)

// Actions an entry can have.
const (
	Allow = "allow"
	Deny  = "deny"
)

// Entry is one listed address or network.
type Entry struct {
	// Network is an address ("192.0.2.7") or a network ("192.0.2.0/24").
	Network string `json:"network"`
	// Action is "allow" or "deny".
	Action string `json:"action"`
	// Note is the owner's reminder why the entry exists.
	Note string `json:"note,omitempty"`
	// Added is when the entry was made.
	Added time.Time `json:"added"`
	// Expires is when the entry stops applying. Zero: never.
	Expires time.Time `json:"expires,omitzero"`
}

// State is everything that was changed.
type State struct {
	// Presets says for a preset whether it is on (true) or off (false),
	// whatever the configuration file lists. Presets not named follow the
	// configuration file.
	Presets map[string]bool `json:"presets,omitempty"`
	// Addresses are the listed addresses and networks.
	Addresses []Entry `json:"addresses,omitempty"`
	// Rules is the text of the rules written in the web interface, in the
	// form of a rule file. They come before the rules of the configuration.
	Rules string `json:"rules,omitempty"`
	// History holds earlier versions of Presets and Rules, newest first.
	// Addresses are not part of a version: an address taken off the list
	// must not live on in the history.
	History []Version `json:"history,omitempty"`
}

// ID names a version by its content, so that "go back to this one" still
// means this one after other changes have moved it down the list.
func (v Version) ID() string {
	h := sha256.New()
	names := make([]string, 0, len(v.Presets))
	for name := range v.Presets {
		names = append(names, name)
	}
	sort.Strings(names)
	_, _ = fmt.Fprintf(h, "%d\x00%s\x00%s\x00", v.At.Unix(), v.What, v.Rules)
	for _, name := range names {
		_, _ = fmt.Fprintf(h, "%s=%v\x00", name, v.Presets[name])
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// Version is what Presets and Rules were before a change.
type Version struct {
	// At is when this version was replaced.
	At time.Time `json:"at"`
	// What names the change that replaced it: "preset_on:<name>",
	// "preset_off:<name>", "rules" or "restore".
	What    string          `json:"what"`
	Presets map[string]bool `json:"presets,omitempty"`
	Rules   string          `json:"rules,omitempty"`
}

// A Problem is a change that cannot be accepted. Key names the reason for
// the web interface, which has the text in the reader's language.
type Problem struct {
	Key, Text string
}

func (p *Problem) Error() string { return p.Text }

func problem(key, format string, args ...any) error {
	return &Problem{Key: key, Text: fmt.Sprintf(format, args...)}
}

// ParseNetwork reads an address or network as the owner types it and
// returns it in its plain form. Networks so large that they would cover a
// good part of the internet are refused: an entry here is meant for one
// client or one organisation.
func ParseNetwork(text string) (netip.Prefix, error) {
	text = strings.TrimSpace(text)
	var prefix netip.Prefix
	if addr, err := netip.ParseAddr(text); err == nil {
		addr = addr.Unmap()
		if addr.Zone() != "" {
			return prefix, problem("network_invalid", "%q carries a zone", text)
		}
		prefix = netip.PrefixFrom(addr, addr.BitLen())
	} else {
		p, err := netip.ParsePrefix(text)
		if err != nil || len(text) > 64 {
			return prefix, problem("network_invalid", "this is not an IP address or network")
		}
		prefix = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()).Masked()
		if p.Addr().Is4In6() {
			return prefix, problem("network_invalid", "this is not an IP address or network")
		}
	}
	smallest := 32 // IPv6: nothing larger than a /32, one provider
	if prefix.Addr().Is4() {
		smallest = 16
	}
	if prefix.Bits() < smallest {
		return prefix, problem("network_too_large", "a network larger than /%d cannot be listed here; use a rule in the configuration", smallest)
	}
	return prefix, nil
}

// Check reports the first thing that is wrong with s. presets are the names
// that exist.
func (s State) Check(presets []string) error {
	known := map[string]bool{}
	for _, name := range presets {
		known[name] = true
	}
	for name := range s.Presets {
		if !known[name] {
			return problem("preset_unknown", "%q is not a preset", name)
		}
	}
	plain := func(text string) bool { // text as typed: no control characters but line break and tab
		return utf8.ValidString(text) && !strings.ContainsFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' })
	}
	if len(s.Rules) > MaxRules || !plain(s.Rules) {
		return problem("rules_too_long", "the rules are longer than %d KiB or hold characters that are not text", MaxRules>>10)
	}
	if len(s.History) > MaxVersions {
		return problem("history_invalid", "more than %d earlier versions are kept", MaxVersions)
	}
	for _, v := range s.History {
		if len(v.Rules) > MaxRules || !plain(v.Rules) || len(v.What) > 100 || len(v.Presets) > 100 {
			return problem("history_invalid", "an earlier version is not valid")
		}
	}
	if len(s.Addresses) > MaxAddresses {
		return problem("too_many", "more than %d addresses are listed", MaxAddresses)
	}
	seen := map[netip.Prefix]bool{}
	for i, e := range s.Addresses {
		prefix, err := ParseNetwork(e.Network)
		if err != nil {
			return err
		}
		if prefix.String() != e.Network && prefix.Addr().String() != e.Network {
			return problem("network_invalid", "an entry is not in its plain form")
		}
		if seen[prefix] {
			return problem("network_listed", "entry number %d is listed twice", i+1)
		}
		seen[prefix] = true
		if e.Action != Allow && e.Action != Deny {
			return problem("action_invalid", "an entry has an action that is neither allow nor deny")
		}
		if utf8.RuneCountInString(e.Note) > MaxNote || !utf8.ValidString(e.Note) || strings.ContainsFunc(e.Note, unicode.IsControl) {
			return problem("note_invalid", "a note is longer than %d characters or has a line break or control character", MaxNote)
		}
	}
	return nil
}

// Active returns the entries that apply at now, as networks by action.
func (s State) Active(now time.Time) (allow, deny []string) {
	for _, e := range s.Addresses {
		if !e.Expires.IsZero() && !now.Before(e.Expires) {
			continue
		}
		if prefix, err := ParseNetwork(e.Network); err == nil {
			switch e.Action {
			case Allow:
				allow = append(allow, prefix.String())
			case Deny:
				deny = append(deny, prefix.String())
			}
		}
	}
	return allow, deny
}

// WithoutExpired returns s without the entries whose time is over at now.
// They have no effect any more and must not stay on disk.
func (s State) WithoutExpired(now time.Time) State {
	out := s.clone()
	kept := out.Addresses[:0]
	for _, e := range out.Addresses {
		if e.Expires.IsZero() || e.Expires.After(now) {
			kept = append(kept, e)
		}
	}
	out.Addresses = kept
	return out
}

// Empty reports whether nothing was changed.
func (s State) Empty() bool {
	return len(s.Presets) == 0 && len(s.Addresses) == 0 && s.Rules == "" && len(s.History) == 0
}

func (s State) clone() State {
	out := State{Addresses: append([]Entry(nil), s.Addresses...), Rules: s.Rules, History: append([]Version(nil), s.History...)}
	if len(s.Presets) > 0 {
		out.Presets = make(map[string]bool, len(s.Presets))
		for k, v := range s.Presets {
			out.Presets[k] = v
		}
	}
	return out
}

// Load reads the file. A file that does not exist is an empty State.
func Load(path string) (State, error) {
	var s State
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil || info.IsDir() || info.Size() > maxFile {
		return s, errors.New("it cannot be read, is a directory or is larger than 2 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return s, errors.New("it cannot be read")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	_ = f.Close()
	if err != nil || len(raw) > maxFile {
		return s, errors.New("it cannot be read or is larger than 2 MiB")
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}, errors.New("it is not valid JSON")
	}
	return s, nil
}

// Save writes the file: beside it first, then moved into place, so that a
// crash never leaves half a file.
func Save(path string, s State) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // rule texts hold "<" and "&"; escaped they would take six times the room
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return err
	}
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	if len(raw) >= maxFile { // never write what Load would refuse at the next start
		return errors.New("the changes would not fit into the changes file")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".changes-*")
	if err != nil {
		return errors.New("the directory of the changes file cannot be written to")
	}
	_, err = tmp.Write(append(raw, '\n'))
	if syncErr := tmp.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o600)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return errors.New("the changes file cannot be written")
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil { // so the new name survives a power cut
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// Options configures a Store.
type Options struct {
	// Path is the file the changes are kept in.
	Path string
	// Initial is what the file held at the start.
	Initial State
	// Presets are the names of the presets that exist.
	Presets []string
	// Protected are networks that must not be listed as let through,
	// besides this machine's own addresses: the web servers in front
	// (server.trusted_proxies). If such a server's address is what
	// Xibalba sees for every visitor, letting it through would let
	// everyone through.
	Protected []netip.Prefix
	// Apply puts a State into force. If it returns an error, the change is
	// refused and nothing is written.
	Apply func(State) error
	// Now returns the current time. Tests replace it; nil means time.Now.
	Now func() time.Time
}

// Store holds the changes while Xibalba runs. A change is applied first and
// written down second; if either fails, the change did not happen.
type Store struct {
	opts Options

	mu      sync.Mutex
	state   State
	next    time.Time // when the next entry expires; zero if none
	problem string    // why the last look at expired entries failed, or ""
}

// Health reports whether expired entries could be taken out of force.
func (s *Store) Health() health.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.problem != "" {
		return health.Status{State: health.Degraded, Detail: s.problem}
	}
	return health.Status{State: health.OK}
}

// NewStore returns a Store.
func NewStore(opts Options) *Store {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &Store{opts: opts, state: opts.Initial.clone()}
	s.schedule()
	return s
}

// State returns a copy of the current changes, entries sorted by network.
func (s *Store) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.state.clone()
	sort.SliceStable(out.Addresses, func(i, j int) bool { return out.Addresses[i].Network < out.Addresses[j].Network })
	return out
}

// schedule notes when the next entry expires. The caller holds the lock,
// or is the constructor.
func (s *Store) schedule() {
	s.next = time.Time{}
	now := s.opts.Now()
	for _, e := range s.state.Addresses {
		if !e.Expires.IsZero() && e.Expires.After(now) && (s.next.IsZero() || e.Expires.Before(s.next)) {
			s.next = e.Expires
		}
	}
}

// commit checks, applies and saves next, and makes it the current state.
// The caller holds the lock.
func (s *Store) commit(next State) error { return s.commitAs(next, "") }

// commitAs is commit for a change of presets or rules: what names the
// change, and what was in force before is kept as a version to go back to.
func (s *Store) commitAs(next State, what string) error {
	if what != "" {
		before := Version{At: s.opts.Now().UTC().Truncate(time.Second), What: what, Rules: s.state.Rules}
		if len(s.state.Presets) > 0 {
			before.Presets = make(map[string]bool, len(s.state.Presets))
			for k, v := range s.state.Presets {
				before.Presets[k] = v
			}
		}
		next.History = append([]Version{before}, next.History...)
		if len(next.History) > MaxVersions {
			next.History = next.History[:MaxVersions]
		}
	}
	next = next.WithoutExpired(s.opts.Now())
	if err := next.Check(s.opts.Presets); err != nil {
		return err
	}
	if err := s.opts.Apply(next); err != nil {
		return err
	}
	if err := Save(s.opts.Path, next); err != nil {
		// Back to what is on disk: after a restart it would be that anyway.
		_ = s.opts.Apply(s.state)
		return problem("not_saved", "%v", err)
	}
	s.state = next
	s.schedule()
	return nil
}

// SetPreset switches a preset on or off.
func (s *Store) SetPreset(name string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.state.clone()
	if next.Presets == nil {
		next.Presets = map[string]bool{}
	}
	if was, changed := next.Presets[name]; changed && was == on {
		return nil // nothing to do, and no version to keep
	}
	next.Presets[name] = on
	what := "preset_off:" + name
	if on {
		what = "preset_on:" + name
	}
	return s.commitAs(next, what)
}

// SetRules replaces the rules written in the web interface.
func (s *Store) SetRules(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	text = strings.ReplaceAll(text, "\r\n", "\n") // browsers send forms with CRLF
	if text == s.state.Rules {
		return nil
	}
	next := s.state.clone()
	next.Rules = text
	return s.commitAs(next, "rules")
}

// Restore puts Presets and Rules back to the earlier version with that ID.
// What is replaced becomes a version itself, so going back can be undone.
func (s *Store) Restore(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	number := -1
	for i, v := range s.state.History {
		if v.ID() == id {
			number = i
			break
		}
	}
	if number < 0 {
		return problem("version_unknown", "this version is no longer kept")
	}
	version := s.state.History[number]
	if version.Rules == s.state.Rules && reflect.DeepEqual(nonEmpty(version.Presets), nonEmpty(s.state.Presets)) {
		return nil // it is what is in force already
	}
	next := s.state.clone()
	next.Rules, next.Presets = version.Rules, nil
	if len(version.Presets) > 0 {
		next.Presets = make(map[string]bool, len(version.Presets))
		for k, v := range version.Presets {
			next.Presets[k] = v
		}
	}
	return s.commitAs(next, "restore")
}

// Add lists an address or network. lifetime zero means no end.
func (s *Store) Add(network, action, note string, lifetime time.Duration) error {
	prefix, err := ParseNetwork(network)
	if err != nil {
		return err
	}
	if lifetime < 0 || lifetime > MaxLifetime {
		return problem("lifetime_invalid", "the entry cannot last that long")
	}
	if action == Allow {
		if prefix.Addr().IsLoopback() || prefix.Addr().IsUnspecified() {
			return problem("allow_protected", "this machine's own address cannot be let through")
		}
		for _, p := range s.opts.Protected {
			if p.Overlaps(prefix) {
				return problem("allow_protected", "the address of a web server in front (server.trusted_proxies) cannot be let through")
			}
		}
	}
	plain := prefix.String()
	if prefix.IsSingleIP() {
		plain = prefix.Addr().String()
	}
	now := s.opts.Now().UTC().Truncate(time.Second)
	entry := Entry{Network: plain, Action: action, Note: strings.TrimSpace(note), Added: now}
	if lifetime > 0 {
		entry.Expires = now.Add(lifetime)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.state.WithoutExpired(now) // entries past their time make room
	next.Addresses = append(next.Addresses, entry)
	return s.commit(next)
}

// Remove takes an address or network off the list.
func (s *Store) Remove(network string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.state.clone()
	kept := next.Addresses[:0]
	for _, e := range next.Addresses {
		if e.Network != network {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(next.Addresses) {
		return problem("network_unknown", "this entry is not on the list")
	}
	next.Addresses = kept
	return s.commit(next)
}

// Tick applies the state again if an entry has expired since the last
// look, so that it stops having effect. Call it regularly.
func (s *Store) Tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next.IsZero() || s.opts.Now().Before(s.next) {
		return
	}
	// The expired entries go out of force and off the disk.
	if err := s.commit(s.state); err != nil {
		s.problem = "an entry of the address list has expired but could not be taken out: " + err.Error()
		return // next stays: the next look tries again
	}
	s.problem = ""
}

func nonEmpty(m map[string]bool) map[string]bool {
	if len(m) == 0 {
		return nil
	}
	return m
}

// ProblemKey names the reason for the web interface.
func (p *Problem) ProblemKey() string { return p.Key }

// Watcher looks once a minute whether a listed entry has expired. It is a
// lifecycle component.
type Watcher struct {
	store  *Store
	every  time.Duration
	cancel func()
	done   chan struct{}
}

// NewWatcher returns the Watcher for store.
func NewWatcher(store *Store) *Watcher { return &Watcher{store: store, every: time.Minute} }

// Name implements lifecycle.Component.
func (w *Watcher) Name() string { return "changes" }

// Start implements lifecycle.Component.
func (w *Watcher) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel, w.done = cancel, make(chan struct{})
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.store.Tick()
			}
		}
	}()
	return nil
}

// Stop implements lifecycle.Component.
func (w *Watcher) Stop(ctx context.Context) error {
	if w.cancel == nil {
		return nil
	}
	w.cancel()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
