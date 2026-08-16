package liveconfig

import (
	"os"
	"path/filepath"
	"testing"

	"subtitle-translator/internal/glossary"
)

func testStore(t *testing.T) *glossary.Store {
	t.Helper()
	d := t.TempDir()
	b := []byte(`{"name":"iracing","entries":[{"source":"安全评级","target":"Safety Rating","aliases":["SR"]}]}`)
	if err := os.WriteFile(filepath.Join(d, "iracing.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := glossary.LoadDir(d)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSnapshotApplyAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	m, err := New(path, Settings{ActiveProfile: "auto", CorrectionMode: "conservative", ContextSentences: 2}, testStore(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Snapshot("我的 SR 掉了"); len(got.Terms) != 1 {
		t.Fatalf("auto match: %+v", got)
	}
	next, err := m.Apply(Settings{ActiveProfile: "iracing", CorrectionMode: "off", ContextSentences: 5, CustomPrompt: "stream"}, []glossary.Entry{{Source: "维修站", Target: "pit lane"}})
	if err != nil || next.ActiveProfile != "iracing" {
		t.Fatalf("apply: %+v %v", next, err)
	}
	if got := m.Snapshot(""); len(got.Terms) != 1 {
		t.Fatalf("profile terms: %+v", got.Terms)
	}
	m2, err := New(path, Settings{}, testStore(t))
	if err != nil || m2.Current().CustomPrompt != "stream" || len(m2.Terms("iracing")) != 1 {
		t.Fatalf("reload: %+v %v", m2.Current(), err)
	}
	if _, err := m2.ResetProfile("iracing"); err != nil || len(m2.Terms("iracing")) != 1 || m2.Terms("iracing")[0].Source != "安全评级" {
		t.Fatalf("reset profile: %+v %v", m2.Terms("iracing"), err)
	}
}

func TestSwitchWithNilTermsDoesNotChangeGlossary(t *testing.T) {
	m, err := New("", Settings{ActiveProfile: "iracing"}, testStore(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Apply(Settings{ActiveProfile: "general", CorrectionMode: "conservative"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(m.Terms("iracing")) != 1 {
		t.Fatal("profile switch changed glossary")
	}
}
