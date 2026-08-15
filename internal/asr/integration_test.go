package asr

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistentServerIntegration(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	exe := filepath.Join(root, "runtime", "sherpa-onnx-v1.13.4", "bin", "sherpa-onnx-offline.exe")
	modelDir := filepath.Join(root, "models", "paraformer-zh")
	wav := filepath.Join(modelDir, "test_wavs", "0.wav")
	if _, err := os.Stat(exe); err != nil {
		t.Skip("runtime not installed")
	}
	b, err := os.ReadFile(wav)
	if err != nil || len(b) <= 44 {
		t.Skip("test model not installed")
	}
	s, err := New(exe, modelDir, "paraformer")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	started := time.Now()
	text, err := s.Transcribe(b[44:])
	if err != nil {
		t.Fatal(err)
	}
	if text == "" {
		t.Fatal("empty transcription")
	}
	if text[0] == '{' {
		t.Fatalf("server JSON was not decoded: %s", text)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("persistent inference too slow: %v", elapsed)
	}
	t.Logf("persistent inference %v: %s", time.Since(started), text)
}
