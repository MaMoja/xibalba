// Package changes keeps what the site owner changed in the web interface:
// presets switched on or off, and addresses let through or blocked.
//
// The changes live in one small file beside the configuration. They are
// applied on top of the configuration file, which is never rewritten.
package changes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// MaxNote is the longest note, in bytes.
	MaxNote = 200
	// MaxLifetime is the longest time an entry can be set to last.
	MaxLifetime = 366 * 24 * time.Hour
	maxFile     = 1 << 20
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
	if len(s.Addresses) > MaxAddresses {
		return problem("too_many", "more than %d addresses are listed", MaxAddresses)
	}
	seen := map[netip.Prefix]bool{}
	for _, e := range s.Addresses {
		prefix, err := ParseNetwork(e.Network)
		if err != nil {
			return err
		}
		if prefix.String() != e.Network && prefix.Addr().String() != e.Network {
			return problem("network_invalid", "an entry is not in its plain form")
		}
		if seen[prefix] {
			return problem("network_listed", "%s is listed twice", prefix)
		}
		seen[prefix] = true
		if e.Action != Allow && e.Action != Deny {
			return problem("action_invalid", "an entry has an action that is neither allow nor deny")
		}
		if len(e.Note) > MaxNote || strings.ContainsAny(e.Note, "\r\n\x00") {
			return problem("note_invalid", "a note is longer than %d bytes or has a line break", MaxNote)
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

// Empty reports whether nothing was changed.
func (s State) Empty() bool { return len(s.Presets) == 0 && len(s.Addresses) == 0 }

func (s State) clone() State {
	out := State{Addresses: append([]Entry(nil), s.Addresses...)}
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
		return s, errors.New("it cannot be read, is a directory or is larger than 1 MiB")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return s, errors.New("it cannot be read")
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}, errors.New("it is not valid JSON")
	}
	return s, nil
}

// Save writes the file: beside it first, then moved into place, so that a
// crash never leaves half a file.
func Save(path string, s State) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
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

	mu    sync.Mutex
	state State
	next  time.Time // when the next entry expires; zero if none
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
func (s *Store) commit(next State) error {
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
	next.Presets[name] = on
	return s.commit(next)
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
	next := s.state.clone()
	kept := next.Addresses[:0]
	for _, e := range next.Addresses { // entries past their time make room
		if e.Expires.IsZero() || e.Expires.After(now) {
			kept = append(kept, e)
		}
	}
	next.Addresses = append(kept, entry)
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
	_ = s.opts.Apply(s.state) // expired entries are left out by whoever applies
	s.schedule()
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
