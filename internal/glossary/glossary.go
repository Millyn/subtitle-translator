// Package glossary loads game terminology profiles and merges user overrides.
package glossary

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DuplicateProfileError is returned when two glossary JSON files share the same
// profile name and auto-deduplication is disabled.
type DuplicateProfileError struct {
	Name    string
	First   string
	Second  string
}

func (e *DuplicateProfileError) Error() string {
	return fmt.Sprintf("duplicate glossary profile %q: first=%s second=%s", e.Name, e.First, e.Second)
}

type Entry struct {
	Source    string   `json:"source"`
	Target    string   `json:"target"`
	Aliases   []string `json:"aliases,omitempty"`
	Category  string   `json:"category,omitempty"`
	Protected bool     `json:"protected,omitempty"`
}

type Profile struct {
	Name        string  `json:"name"`
	DisplayName string  `json:"display_name"`
	Entries     []Entry `json:"entries"`
}

type Store struct{ profiles map[string]Profile }

func LoadDir(dir string, autoDeduplicate bool) (*Store, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no glossary JSON files in %s", dir)
	}
	s := &Store{profiles: make(map[string]Profile)}
	// Track which file each profile came from for error messages.
	profileSource := make(map[string]string)
	for _, path := range matches {
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		var p Profile
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, fmt.Errorf("decode glossary %s: %w", path, err)
		}
		if err := validate(p); err != nil {
			return nil, fmt.Errorf("glossary %s: %w", path, err)
		}
		key := strings.ToLower(p.Name)
		if prev, exists := profileSource[key]; exists {
			if !autoDeduplicate {
				return nil, &DuplicateProfileError{Name: p.Name, First: prev, Second: path}
			}
			// autoDeduplicate is true: keep the first profile, skip the duplicate.
			continue
		}
		profileSource[key] = path
		s.profiles[key] = clone(p)
	}
	return s, nil
}

func validate(p Profile) error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("profile name is empty")
	}
	seen := make(map[string]bool)
	for i, e := range p.Entries {
		key := strings.ToLower(strings.TrimSpace(e.Source))
		if key == "" || strings.TrimSpace(e.Target) == "" {
			return fmt.Errorf("entry %d has empty source or target", i)
		}
		if seen[key] {
			return fmt.Errorf("duplicate source %q", e.Source)
		}
		seen[key] = true
	}
	return nil
}

func clone(p Profile) Profile {
	p.Entries = append([]Entry(nil), p.Entries...)
	for i := range p.Entries {
		p.Entries[i].Aliases = append([]string(nil), p.Entries[i].Aliases...)
	}
	return p
}

func (s *Store) Profile(name string) (Profile, bool) {
	if s == nil {
		return Profile{}, false
	}
	p, ok := s.profiles[strings.ToLower(strings.TrimSpace(name))]
	return clone(p), ok
}

func (s *Store) Names() []string {
	if s == nil {
		return nil
	}
	n := make([]string, 0, len(s.profiles))
	for k := range s.profiles {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// Merge applies user entries to a profile. Matching source terms are replaced.
func (s *Store) Merge(profile string, user []Entry) error {
	key := strings.ToLower(strings.TrimSpace(profile))
	p, ok := s.profiles[key]
	if !ok {
		return fmt.Errorf("unknown glossary profile %q", profile)
	}
	index := make(map[string]int, len(p.Entries))
	for i, e := range p.Entries {
		index[strings.ToLower(strings.TrimSpace(e.Source))] = i
	}
	for _, e := range user {
		if err := validate(Profile{Name: "user", Entries: []Entry{e}}); err != nil {
			return err
		}
		k := strings.ToLower(strings.TrimSpace(e.Source))
		if i, exists := index[k]; exists {
			p.Entries[i] = e
		} else {
			index[k] = len(p.Entries)
			p.Entries = append(p.Entries, e)
		}
	}
	s.profiles[key] = clone(p)
	return nil
}

// Replace atomically replaces a profile's entries after validation. It is
// used by the live editor so deleting a row has real runtime effect.
func (s *Store) Replace(profile string, entries []Entry) error {
	key := strings.ToLower(strings.TrimSpace(profile))
	p, ok := s.profiles[key]
	if !ok {
		return fmt.Errorf("unknown glossary profile %q", profile)
	}
	next := Profile{Name: p.Name, DisplayName: p.DisplayName, Entries: clone(Profile{Entries: entries}).Entries}
	if err := validate(next); err != nil {
		return err
	}
	s.profiles[key] = next
	return nil
}

// Match returns entries whose source or alias occurs in transcript text.
func (s *Store) Match(profile, text string) ([]Entry, error) {
	p, ok := s.Profile(profile)
	if !ok {
		return nil, fmt.Errorf("unknown glossary profile %q", profile)
	}
	lower := strings.ToLower(text)
	var out []Entry
	for _, e := range p.Entries {
		terms := append([]string{e.Source}, e.Aliases...)
		for _, term := range terms {
			if term != "" && strings.Contains(lower, strings.ToLower(term)) {
				out = append(out, e)
				break
			}
		}
	}
	return out, nil
}
