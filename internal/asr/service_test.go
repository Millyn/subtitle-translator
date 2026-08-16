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
		{"fire-red-asr-ctc", []string{"tokens.txt", "model.int8.onnx"}, "--fire-red-asr-ctc="},
		{"funasr-nano", []string{"encoder_adaptor.int8.onnx", "embedding.int8.onnx", "llm.int8.onnx", "Qwen3-0.6B/merges.txt", "Qwen3-0.6B/tokenizer.json", "Qwen3-0.6B/vocab.json"}, "--funasr-nano-encoder-adaptor="},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			d := t.TempDir()
			for _, n := range tt.files {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(d, n)), 0755); err != nil {
					t.Fatal(err)
				}
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

func TestSenseVoiceUsesAutoLanguage(t *testing.T) {
	d := t.TempDir()
	for _, n := range []string{"tokens.txt", "model.int8.onnx"} {
		if err := os.WriteFile(filepath.Join(d, n), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	args, err := (&Service{Dir: d, Kind: "sensevoice"}).args("x.wav")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--sense-voice-language=auto") || strings.Contains(joined, "--sense-voice-language=zh") {
		t.Fatalf("unexpected args: %s", joined)
	}
}

func TestWhisperIsUnsupported(t *testing.T) {
	if _, err := (&Service{Dir: t.TempDir(), Kind: "whisper"}).args("x.wav"); err == nil {
		t.Fatal("Whisper must not be supported")
	}
}

func TestNewModelArgsAreExact(t *testing.T) {
	tests := []struct {
		kind      string
		files     []string
		required  []string
		forbidden []string
	}{
		{
			kind:      "fire-red-asr-ctc",
			files:     []string{"tokens.txt", "model.int8.onnx"},
			required:  []string{"--tokens=", "--fire-red-asr-ctc=", "--num-threads=2", "--debug=0"},
			forbidden: []string{"--funasr-nano-", "--whisper-"},
		},
		{
			kind:      "funasr-nano",
			files:     []string{"encoder_adaptor.int8.onnx", "embedding.int8.onnx", "llm.int8.onnx", "Qwen3-0.6B/merges.txt", "Qwen3-0.6B/tokenizer.json", "Qwen3-0.6B/vocab.json"},
			required:  []string{"--funasr-nano-encoder-adaptor=", "--funasr-nano-embedding=", "--funasr-nano-llm=", "--funasr-nano-tokenizer=", "Qwen3-0.6B", "--funasr-nano-itn=1", "--num-threads=2", "--debug=0"},
			forbidden: []string{"--tokens=", "--whisper-", "--funasr-nano-language="},
		},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			d := t.TempDir()
			for _, name := range tt.files {
				path := filepath.Join(d, name)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			args, err := (&Service{Dir: d, Kind: tt.kind, NumThreads: 2}).args("sample.wav")
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(args, " ")
			for _, want := range tt.required {
				if !strings.Contains(joined, want) {
					t.Errorf("missing %q in %s", want, joined)
				}
			}
			for _, bad := range tt.forbidden {
				if strings.Contains(joined, bad) {
					t.Errorf("forbidden %q in %s", bad, joined)
				}
			}
		})
	}
}

func TestFunASRNanoRejectsIncompleteTokenizer(t *testing.T) {
	d := t.TempDir()
	for _, name := range []string{"encoder_adaptor.int8.onnx", "embedding.int8.onnx", "llm.int8.onnx", "Qwen3-0.6B/tokenizer.json"} {
		path := filepath.Join(d, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&Service{Dir: d, Kind: "funasr-nano"}).args("sample.wav"); err == nil {
		t.Fatal("incomplete tokenizer accepted")
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
