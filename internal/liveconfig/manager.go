// Package liveconfig owns translation settings that can change while the
// microphone, ASR model, HTTP server, and OBS connection keep running.
package liveconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"subtitle-translator/internal/glossary"
)

type Settings struct {
	ActiveProfile    string                      `json:"active_profile"`
	CorrectionMode   string                      `json:"correction_mode"`
	ChineseSource    string                      `json:"chinese_source"`
	ContextSentences int                         `json:"context_sentences"`
	CustomPrompt     string                      `json:"custom_prompt"`
	UserTerms        map[string][]glossary.Entry `json:"user_terms,omitempty"`
}

type Snapshot struct {
	Settings
	Terms []glossary.Entry
}

type Manager struct {
	mu       sync.RWMutex
	path     string
	store    *glossary.Store
	defaults map[string][]glossary.Entry
	settings Settings
}

func New(path string, initial Settings, store *glossary.Store) (*Manager, error) {
	if store == nil {
		return nil, errors.New("glossary store is nil")
	}
	initial = normalize(initial)
	m := &Manager{path: path, store: store, settings: initial, defaults: make(map[string][]glossary.Entry)}
	for _, name := range store.Names() {
		if p, ok := store.Profile(name); ok {
			m.defaults[name] = cloneEntries(p.Entries)
		}
	}
	if b, err := os.ReadFile(path); err == nil {
		var saved Settings
		if err := json.Unmarshal(b, &saved); err != nil {
			return nil, fmt.Errorf("decode live translation settings: %w", err)
		}
		saved = normalize(saved)
		// An older settings file may not yet contain the prompt. Preserve the
		// configured default in that case.
		if saved.CustomPrompt == "" {
			saved.CustomPrompt = initial.CustomPrompt
		}
		m.settings = saved
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read live translation settings: %w", err)
	}
	for profile, entries := range m.settings.UserTerms {
		if _, ok := store.Profile(profile); ok {
			if err := store.Replace(profile, entries); err != nil {
				return nil, fmt.Errorf("merge user glossary %s: %w", profile, err)
			}
		}
	}
	return m, nil
}

func normalize(s Settings) Settings {
	validProfile := map[string]bool{"auto": true, "general": true, "iracing": true, "minecraft": true, "project_zomboid": true, "disabled": true}
	s.ActiveProfile = strings.ToLower(strings.TrimSpace(s.ActiveProfile))
	if !validProfile[s.ActiveProfile] {
		s.ActiveProfile = "auto"
	}
	if s.CorrectionMode != "off" && s.CorrectionMode != "conservative" {
		s.CorrectionMode = "conservative"
	}
	if s.ChineseSource != "corrected" && s.ChineseSource != "raw" && s.ChineseSource != "compare" {
		s.ChineseSource = "corrected"
	}
	if s.ContextSentences < 0 {
		s.ContextSentences = 0
	}
	if s.ContextSentences > 5 {
		s.ContextSentences = 5
	}
	if s.UserTerms == nil {
		s.UserTerms = make(map[string][]glossary.Entry)
	}
	return s
}

func (m *Manager) Current() Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneSettings(m.settings)
}

func cloneSettings(s Settings) Settings {
	out := s
	out.UserTerms = make(map[string][]glossary.Entry, len(s.UserTerms))
	for profile, entries := range s.UserTerms {
		out.UserTerms[profile] = cloneEntries(entries)
	}
	return out
}

func cloneEntries(in []glossary.Entry) []glossary.Entry {
	out := append([]glossary.Entry(nil), in...)
	for i := range out {
		out[i].Aliases = append([]string(nil), out[i].Aliases...)
	}
	return out
}

// Snapshot returns a stable settings-and-terms view for one translation. In
// auto mode only terms mentioned in the ASR text are sent, avoiding a large
// cross-game prompt. A selected game sends its complete glossary so homophone
// correction has enough information to recover a damaged term.
func (m *Manager) Snapshot(source string) Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := cloneSettings(m.settings)
	var entries []glossary.Entry
	switch s.ActiveProfile {
	case "disabled", "general":
	case "auto":
		for _, name := range m.store.Names() {
			matched, _ := m.store.Match(name, source)
			entries = append(entries, matched...)
		}
	default:
		if p, ok := m.store.Profile(s.ActiveProfile); ok {
			entries = p.Entries
		}
	}
	return Snapshot{Settings: s, Terms: cloneEntries(entries)}
}

func (m *Manager) Terms(profile string) []glossary.Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if p, ok := m.store.Profile(profile); ok {
		return cloneEntries(p.Entries)
	}
	return nil
}

// Apply changes behavior immediately. Passing nil terms switches profiles
// without changing either profile's glossary.
func (m *Manager) Apply(next Settings, terms []glossary.Entry) (Settings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next.UserTerms = m.settings.UserTerms
	next = normalize(next)
	if terms != nil && next.ActiveProfile != "auto" && next.ActiveProfile != "general" && next.ActiveProfile != "disabled" {
		existing := make(map[string]glossary.Entry)
		if p, ok := m.store.Profile(next.ActiveProfile); ok {
			for _, entry := range p.Entries {
				existing[strings.ToLower(strings.TrimSpace(entry.Source))] = entry
			}
		}
		for i := range terms {
			terms[i].Category = next.ActiveProfile
			if old, ok := existing[strings.ToLower(strings.TrimSpace(terms[i].Source))]; ok {
				terms[i].Aliases = append([]string(nil), old.Aliases...)
				terms[i].Protected = old.Protected
			}
		}
		if err := m.store.Replace(next.ActiveProfile, terms); err != nil {
			return Settings{}, err
		}
		next.UserTerms[next.ActiveProfile] = cloneEntries(terms)
	}
	m.settings = next
	if err := m.saveLocked(); err != nil {
		return Settings{}, err
	}
	return cloneSettings(m.settings), nil
}

func (m *Manager) ResetProfile(profile string) (Settings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	defaults, ok := m.defaults[profile]
	if !ok {
		return Settings{}, fmt.Errorf("unknown glossary profile %q", profile)
	}
	if err := m.store.Replace(profile, cloneEntries(defaults)); err != nil {
		return Settings{}, err
	}
	delete(m.settings.UserTerms, profile)
	if err := m.saveLocked(); err != nil {
		return Settings{}, err
	}
	return cloneSettings(m.settings), nil
}

func (m *Manager) saveLocked() error {
	if m.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m.settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, append(b, '\n'), 0600)
}
