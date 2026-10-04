package main

import (
	"time"

	"github.com/MaMoja/xibalba/internal/admin"
	"github.com/MaMoja/xibalba/internal/changes"
	"github.com/MaMoja/xibalba/internal/config"
)

// settings lets the web interface see and change what internal/changes
// keeps. It only translates between the two.
type settings struct {
	cfg   *config.Config
	store *changes.Store
}

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
