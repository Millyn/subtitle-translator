package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadAndEnvironmentFallback(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "from-env")
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"listen":":9999","deepseek":{"timeout_ms":1000}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9999" || c.DeepSeek.APIKey != "from-env" || c.ModelDir == "models" {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestSubtitleDefaultsAndValidation(t *testing.T) {
	c := Defaults()
	if c.Subtitle.Mode != "bilingual" || c.Subtitle.HideAfterMS != 12000 {
		t.Fatalf("%+v", c.Subtitle)
	}
	c.Subtitle.Mode = "invalid"
	if c.Validate(false) == nil {
		t.Fatal("expected invalid mode")
	}
	c = Defaults()
	if c.Audio.PreRollMS != 250 || c.Audio.SilenceMS != 700 || c.Translation.ActiveProfile != "auto" || c.Translation.ContextSentences != 2 || c.Subtitle.ChineseSource != "corrected" {
		t.Fatalf("new defaults: %+v", c)
	}
}

func TestLoadDefaultsMissingAndNormalization(t *testing.T) {
	p := filepath.Join(t.TempDir(), "missing.json")
	c, err := Load(p)
	if err != nil || c.Listen != ":8765" || c.Audio.DeviceIndex != -1 {
		t.Fatalf("defaults: %+v, %v", c, err)
	}
	p = filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"model_dir":"","runtime_dir":"","listen":"","gomaxprocs":0,"asr":{"num_threads":0},"deepseek":{"timeout_ms":0}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(c.ModelDir) || !filepath.IsAbs(c.RuntimeDir) || c.GOMAXPROCS != 3 || c.ASR.NumThreads != 2 || c.TranslationTimeout() != 5*time.Second {
		t.Fatalf("normalized: %+v", c)
	}
}

func TestLoadAndValidateErrors(t *testing.T) {
	d := t.TempDir()
	if _, err := Load(d); err == nil {
		t.Fatal("directory should not load")
	}
	p := filepath.Join(d, "bad.json")
	if err := os.WriteFile(p, []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("malformed JSON should fail")
	}
	c := Defaults()
	if err := c.Validate(false); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(true); err == nil {
		t.Fatal("missing API key should fail")
	}
	c.DeepSeek.APIKey = "ok"
	c.ASR.NumThreads = 5
	if err := c.Validate(true); err == nil || !strings.Contains(err.Error(), "4") {
		t.Fatalf("thread validation: %v", err)
	}
	c = Defaults()
	c.Translation.ActiveProfile = "other"
	if err := c.Validate(false); err == nil {
		t.Fatal("invalid profile should fail")
	}
	c = Defaults()
	c.Subtitle.ChineseSource = "unknown"
	if err := c.Validate(false); err == nil {
		t.Fatal("invalid chinese source should fail")
	}
}
