package main

import (
	"net/http"
	"net/netip"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/MaMoja/xibalba/internal/admin"
	"github.com/MaMoja/xibalba/internal/changes"
	"github.com/MaMoja/xibalba/internal/config"
	"github.com/MaMoja/xibalba/internal/gate"
)

// settings lets the web interface see and change what internal/changes
// keeps, and try a rule set on a made-up request. It only translates
// between the parts.
type settings struct {
	cfg   *config.Config
	store *changes.Store
	gate  *gate.Gate
}

// reasons is a rule set that does not work: one line per reason.
type reasons []string

func (r reasons) Error() string   { return strings.Join(r, "; ") }
func (r reasons) Lines() []string { return r }

// keyed is a refusal the web interface has a text for.
type keyed string

func (k keyed) Error() string      { return string(k) }
func (k keyed) ProblemKey() string { return string(k) }

func (s settings) Presets() []admin.Preset {
	on := map[string]bool{}
	for _, name := range s.cfg.Rules.EffectivePresets(s.store.State()) {
		on[name] = true
	}
	var out []admin.Preset
	for _, name := range config.PresetsInOrder() {
		out = append(out, admin.Preset{Name: name, On: on[name]})
	}
	return out
}

func (s settings) SetPreset(name string, on bool) error { return s.store.SetPreset(name, on) }

func (s settings) Addresses() []admin.Address {
	var out []admin.Address
	for _, e := range s.store.State().Addresses {
		out = append(out, admin.Address{Network: e.Network, Action: e.Action, Note: e.Note, Added: e.Added, Expires: e.Expires})
	}
	return out
}

func (s settings) AddAddress(network, action, note string, lifetime time.Duration) error {
	return s.store.Add(network, action, note, lifetime)
}

func (s settings) RemoveAddress(network string) error { return s.store.Remove(network) }

func (s settings) Rules() string { return s.store.State().Rules }

func (s settings) SetRules(text string) error { return s.store.SetRules(text) }

func (s settings) Versions() []admin.Version {
	var out []admin.Version
	for _, v := range s.store.State().History {
		out = append(out, admin.Version{At: v.At, What: v.What})
	}
	return out
}

func (s settings) Restore(number int) error { return s.store.Restore(number) }

var probeMethods = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true}

// Test compiles the rule set as it would be with text as the rules written
// in the web interface, and asks it about the probe. Nothing is put in force.
func (s settings) Test(text string, probe admin.Probe) (admin.Verdict, error) {
	client, err := netip.ParseAddr(strings.TrimSpace(probe.Client))
	target, urlErr := url.ParseRequestURI(strings.TrimSpace(probe.Address))
	if err != nil || urlErr != nil || !probeMethods[probe.Method] || !strings.HasPrefix(target.Path, "/") || target.Host != "" || len(probe.Address) > 2000 {
		return admin.Verdict{}, keyed("probe_invalid")
	}
	request := &http.Request{Method: probe.Method, URL: target, Header: http.Header{}, Host: s.cfg.Upstream.Target().Host}
	if probe.UserAgent != "" {
		request.Header.Set("User-Agent", probe.UserAgent)
	}
	lines := strings.Split(probe.Headers, "\n")
	if len(lines) > 30 || len(probe.Headers) > 4000 || len(probe.UserAgent) > 500 {
		return admin.Verdict{}, keyed("probe_invalid")
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, value, found := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !found || name == "" || strings.ContainsAny(name, " \t()<>@,;\\\"/[]?={}") {
			return admin.Verdict{}, keyed("probe_invalid")
		}
		if textproto.CanonicalMIMEHeaderKey(name) == "Host" {
			request.Host = strings.TrimSpace(value)
			continue
		}
		request.Header.Add(name, strings.TrimSpace(value))
	}

	state := s.store.State()
	state.Rules = text
	if err := state.Check(config.PresetNames()); err != nil {
		return admin.Verdict{}, err
	}
	engine, problems := s.cfg.Compile(state, time.Now())
	if len(problems) > 0 {
		return admin.Verdict{}, reasons(problems)
	}
	action, source, weight, ok := s.gate.Explain(request, client.Unmap(), engine)
	if !ok {
		return admin.Verdict{}, keyed("probe_invalid")
	}
	return admin.Verdict{Action: string(action), Source: source, Weight: weight}, nil
}
