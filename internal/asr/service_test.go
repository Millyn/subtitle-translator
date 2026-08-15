package asr

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteWAV(t *testing.T) {
	var b bytes.Buffer
	pcm := make([]byte, 9600)
	if err := writeWAV(&b, pcm); err != nil {
		t.Fatal(err)
	}
	if got := b.Len(); got != 44+len(pcm) {
		t.Fatalf("size=%d", got)
	}
	if string(b.Bytes()[:4]) != "RIFF" {
		t.Fatal("missing RIFF")
	}
	if n := binary.LittleEndian.Uint32(b.Bytes()[24:28]); n != 16000 {
		t.Fatalf("rate=%d", n)
	}
}

func TestArgsForAllModels(t *testing.T) {
	tests := []struct {
		kind  string
		files []string
		want  string
	}{
		{"paraformer", []string{"tokens.txt", "model.int8.onnx"}, "--paraformer="},
		{"sensevoice", []string{"tokens.txt", "model.int8.onnx"}, "--sense-voice-model="},
		{"whisper", []string{"base-tokens.txt", "base-encoder.int8.onnx", "base-decoder.int8.onnx"}, "--whisper-encoder="},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			d := t.TempDir()
			for _, n := range tt.files {
				if err := os.WriteFile(filepath.Join(d, n), []byte("x"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			s := &Service{Dir: d, Kind: tt.kind, NumThreads: 2}
			args, err := s.args("x.wav")
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, tt.want) || !strings.Contains(joined, "--num-threads=2") {
				t.Fatal(joined)
			}
		})
	}
}

func TestArgsRejectMissingFile(t *testing.T) {
	s := &Service{Dir: t.TempDir(), Kind: "paraformer"}
	if _, err := s.args("x.wav"); err == nil {
		t.Fatal("wanted error")
	}
}
func TestParseOutput(t *testing.T) {
	got := parseOutput("elapsed: 0.2\nclip.wav: 你 好 世 界\n", "C:/x/clip.wav")
	if got != "你好世界" {
		t.Fatalf("%q", got)
	}
}

func TestParseJSONOutput(t *testing.T) {
	out := "Started\n{\"lang\":\"\",\"text\":\"你好 世界\",\"tokens\":[]}\n----\nnum threads: 2\nElapsed seconds: 0.1\n"
	if got := parseOutput(out, "sample.wav"); got != "你好世界" {
		t.Fatalf("got %q", got)
	}
}
