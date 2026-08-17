package glossary

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMergeProfileAndMatch(t *testing.T) {
	d := t.TempDir()
	b := []byte(`{"name":"game","display_name":"Game","entries":[{"source":"维修站","target":"pit lane","aliases":["维修区"],"category":"track","protected":true}]}`)
	if err := os.WriteFile(filepath.Join(d, "game.json"), b, 0644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadDir(d, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Names(); len(got) != 1 || got[0] != "game" {
		t.Fatal(got)
	}
	if err := s.Merge("game", []Entry{{Source: "维修站", Target: "the pits", Protected: true}, {Source: "方格旗", Target: "checkered flag"}}); err != nil {
		t.Fatal(err)
	}
	p, ok := s.Profile("GAME")
	if !ok || len(p.Entries) != 2 || p.Entries[0].Target != "the pits" {
		t.Fatalf("%+v", p)
	}
	hits, err := s.Match("game", "进入维修站然后看方格旗")
	if err != nil || len(hits) != 2 {
		t.Fatalf("%+v %v", hits, err)
	}
	p.Entries[0].Target = "mutated"
	again, _ := s.Profile("game")
	if again.Entries[0].Target == "mutated" {
		t.Fatal("profile leaked mutable state")
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := LoadDir(t.TempDir(), true); err == nil {
		t.Fatal("empty directory")
	}
	d := t.TempDir()
	_ = os.WriteFile(filepath.Join(d, "bad.json"), []byte(`{"name":"x","entries":[{"source":"","target":"x"}]}`), 0644)
	if _, err := LoadDir(d, true); err == nil {
		t.Fatal("invalid entry")
	}
}

func TestBundledGameGlossaries(t *testing.T) {
	s, err := LoadDir(filepath.Join("..", "..", "glossaries"), true)
	if err != nil {
		t.Fatal(err)
	}
	minimum := map[string]int{"iracing": 50, "minecraft": 45, "project_zomboid": 45}
	for name, want := range minimum {
		p, ok := s.Profile(name)
		if !ok || len(p.Entries) < want {
			t.Fatalf("%s: found=%v terms=%d, want at least %d", name, ok, len(p.Entries), want)
		}
	}
}

func TestDuplicateProfiles_AutoDeduplicate(t *testing.T) {
	d := t.TempDir()
	a := []byte(`{"name":"game","display_name":"Game A","entries":[{"source":"a","target":"b"}]}`)
	b := []byte(`{"name":"game","display_name":"Game B","entries":[{"source":"c","target":"d"}]}`)
	if err := os.WriteFile(filepath.Join(d, "a.json"), a, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "b.json"), b, 0644); err != nil {
		t.Fatal(err)
	}
	// autoDeduplicate=true: should not error, keep the first profile.
	s, err := LoadDir(d, true)
	if err != nil {
		t.Fatalf("autoDeduplicate=true should not error, got: %v", err)
	}
	if len(s.Names()) != 1 {
		t.Fatalf("expected 1 profile, got %d: %v", len(s.Names()), s.Names())
	}
	p, ok := s.Profile("game")
	if !ok {
		t.Fatal("expected profile 'game' to exist")
	}
	// Keep the first profile loaded (depends on glob order, but both have same name so either is fine).
	if len(p.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(p.Entries))
	}
}

func TestDuplicateProfiles_AutoDeduplicateKeepsFirst(t *testing.T) {
	d := t.TempDir()
	first := []byte(`{"name":"dup","display_name":"First","entries":[{"source":"x","target":"y"}]}`)
	second := []byte(`{"name":"dup","display_name":"Second","entries":[{"source":"a","target":"b"}]}`)
	// Use filenames that ensure deterministic glob ordering: a.json < b.json
	if err := os.WriteFile(filepath.Join(d, "a.json"), first, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "b.json"), second, 0644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadDir(d, true)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := s.Profile("dup")
	if !ok {
		t.Fatal("expected profile 'dup'")
	}
	if p.DisplayName != "First" {
		t.Fatalf("expected DisplayName='First', got %q", p.DisplayName)
	}
}

func TestDuplicateProfiles_ErrorWhenDisabled(t *testing.T) {
	d := t.TempDir()
	a := []byte(`{"name":"game","display_name":"Game A","entries":[{"source":"a","target":"b"}]}`)
	b := []byte(`{"name":"game","display_name":"Game B","entries":[{"source":"c","target":"d"}]}`)
	if err := os.WriteFile(filepath.Join(d, "a.json"), a, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "b.json"), b, 0644); err != nil {
		t.Fatal(err)
	}
	// autoDeduplicate=false: should return a DuplicateProfileError.
	_, err := LoadDir(d, false)
	if err == nil {
		t.Fatal("expected error for duplicate profiles when autoDeduplicate=false")
	}
	var dupErr *DuplicateProfileError
	if !errorAs(err, &dupErr) {
		t.Fatalf("expected *DuplicateProfileError, got %T: %v", err, err)
	}
	if dupErr.Name != "game" {
		t.Fatalf("expected duplicate name 'game', got %q", dupErr.Name)
	}
}

func TestDuplicateProfiles_CaseInsensitive(t *testing.T) {
	d := t.TempDir()
	a := []byte(`{"name":"Game","display_name":"Upper","entries":[{"source":"a","target":"b"}]}`)
	b := []byte(`{"name":"game","display_name":"Lower","entries":[{"source":"c","target":"d"}]}`)
	if err := os.WriteFile(filepath.Join(d, "a.json"), a, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "b.json"), b, 0644); err != nil {
		t.Fatal(err)
	}
	// Should detect as duplicate even with different casing.
	_, err := LoadDir(d, false)
	if err == nil {
		t.Fatal("expected error for case-insensitive duplicate profiles")
	}
	var dupErr *DuplicateProfileError
	if !errorAs(err, &dupErr) {
		t.Fatalf("expected *DuplicateProfileError, got %T: %v", err, err)
	}
}

// errorAs is a helper that mirrors errors.As for tests.
func errorAs(err error, target interface{}) bool {
	switch t := target.(type) {
	case **DuplicateProfileError:
		if e, ok := err.(*DuplicateProfileError); ok {
			*t = e
			return true
		}
	}
	return false
}
