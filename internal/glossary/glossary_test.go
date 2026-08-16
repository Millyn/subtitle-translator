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
	s, err := LoadDir(d)
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
	if _, err := LoadDir(t.TempDir()); err == nil {
		t.Fatal("empty directory")
	}
	d := t.TempDir()
	_ = os.WriteFile(filepath.Join(d, "bad.json"), []byte(`{"name":"x","entries":[{"source":"","target":"x"}]}`), 0644)
	if _, err := LoadDir(d); err == nil {
		t.Fatal("invalid entry")
	}
}

func TestBundledGameGlossaries(t *testing.T) {
	s, err := LoadDir(filepath.Join("..", "..", "glossaries"))
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
