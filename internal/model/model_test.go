package model

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateURLHeadAndRange(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "10")
			return
		}
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("x"))
			return
		}
		t.Fatal("unexpected request")
	}))
	defer s.Close()
	info, err := ValidateURLInfo(context.Background(), s.Client(), s.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !info.RangeSupported || info.Size != 10 {
		t.Fatalf("%+v", info)
	}
}

func TestFindInstalledAndSHA(t *testing.T) {
	m, ok := Find("paraformer-zh")
	if !ok || m.Kind != "paraformer" {
		t.Fatalf("find: %+v %v", m, ok)
	}
	if _, ok := Find("missing"); ok {
		t.Fatal("unexpected model")
	}
	d := t.TempDir()
	if Installed(m, d) {
		t.Fatal("missing model installed")
	}
	root := filepath.Join(d, m.ID)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	for _, n := range m.RequiredFiles {
		if err := os.WriteFile(filepath.Join(root, n), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateInstalled(m, d); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "sum")
	if err := os.WriteFile(p, []byte("abc"), 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("abc"))
	if err := verifySHA(p, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if err := verifySHA(p, strings.Repeat("0", 64)); err == nil {
		t.Fatal("bad checksum")
	}
	if err := verifySHA(filepath.Join(d, "none"), "x"); err == nil {
		t.Fatal("missing checksum file")
	}
}

func TestSelectErrorsAndInstalledDisplay(t *testing.T) {
	d := t.TempDir()
	m := Catalog[0]
	root := filepath.Join(d, m.ID)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	for _, n := range m.RequiredFiles {
		if err := os.WriteFile(filepath.Join(root, n), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var out strings.Builder
	if _, err := Select(strings.NewReader("0\n"), &out, d); err != nil || out.Len() == 0 {
		t.Fatalf("%v %q", err, out.String())
	}
	for _, input := range []string{"q\n", "bad\n", "99\n", ""} {
		if _, err := Select(strings.NewReader(input), io.Discard, d); err == nil {
			t.Fatalf("input %q", input)
		}
	}
}

func TestValidateURLFailuresAndContentRange(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer s.Close()
	if _, err := ValidateURLInfo(context.Background(), s.Client(), s.URL); err == nil {
		t.Fatal("404 accepted")
	}
	if n := contentRangeTotal("bytes 0-0/123", 4); n != 123 {
		t.Fatal(n)
	}
	if n := contentRangeTotal("invalid", 4); n != 4 {
		t.Fatal(n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ValidateURLInfo(ctx, s.Client(), s.URL); err == nil {
		t.Fatal("cancelled request")
	}
}

func TestDownloadAttemptFreshResumeAndIncomplete(t *testing.T) {
	body := []byte("0123456789")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := 0
		if r.Header.Get("Range") == "bytes=4-" {
			start = 4
			w.WriteHeader(http.StatusPartialContent)
		}
		_, _ = w.Write(body[start:])
	}))
	defer s.Close()
	p := filepath.Join(t.TempDir(), "part")
	if err := downloadAttempt(context.Background(), s.Client(), s.URL, p, URLInfo{Size: int64(len(body))}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); !bytes.Equal(got, body) {
		t.Fatalf("%q", got)
	}
	if err := os.WriteFile(p, body[:4], 0644); err != nil {
		t.Fatal(err)
	}
	var progressCalls int
	if err := downloadAttempt(context.Background(), s.Client(), s.URL, p, URLInfo{Size: 10, RangeSupported: true}, func(Progress) { progressCalls++ }); err != nil {
		t.Fatal(err)
	}
	if progressCalls == 0 {
		t.Fatal("no progress")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("x")) }))
	defer bad.Close()
	if err := downloadAttempt(context.Background(), bad.Client(), bad.URL, filepath.Join(t.TempDir(), "p"), URLInfo{Size: 2}, nil); err == nil {
		t.Fatal("incomplete accepted")
	}
}

func TestExtractTarRejectsUnsafeAndEmpty(t *testing.T) {
	d := t.TempDir()
	if err := extractTar(tar.NewReader(bytes.NewReader(makeArchive(t, map[string]string{"single": "x"}))), filepath.Join(d, "empty")); err == nil {
		t.Fatal("root-only archive accepted")
	}
	target := filepath.Join(d, "exists")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	good := makeArchive(t, map[string]string{"root/a": "x"})
	if err := extractTar(tar.NewReader(bytes.NewReader(good)), target); err == nil {
		t.Fatal("existing target")
	}
}

func TestValidateURLFallsBackWhenHeadRejected(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("x"))
	}))
	defer s.Close()
	if err := ValidateURL(context.Background(), s.URL); err != nil {
		t.Fatal(err)
	}
}

func TestSelect(t *testing.T) {
	m, err := Select(strings.NewReader("2\n"), io.Discard, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "fire-red-asr2-ctc-int8" {
		t.Fatal(m.ID)
	}
}

func TestCatalogIsCompleteAndHasNoWhisper(t *testing.T) {
	if len(Catalog) != 7 {
		t.Fatalf("catalog has %d models, want 7", len(Catalog))
	}
	want := map[string]struct {
		kind  string
		bytes int64
	}{
		"paraformer-zh":                    {kind: "paraformer"},
		"sensevoice-int8":                  {kind: "sensevoice"},
		"fire-red-asr2-ctc-int8":           {kind: "fire-red-asr-ctc", bytes: 520516278},
		"funasr-nano-int8":                 {kind: "funasr-nano", bytes: 841730611},
		"cohere-transcribe-14-lang-int8":   {kind: "cohere-transcribe"},
		"qwen3-asr-0.6B-int8":             {kind: "qwen3-asr"},
		"nemo-parakeet-tdt-v3-int8":        {kind: "nemo-transducer"},
	}
	for _, m := range Catalog {
		expect, ok := want[m.ID]
		if !ok {
			t.Fatalf("unexpected model %q", m.ID)
		}
		if strings.Contains(strings.ToLower(m.ID+m.Kind+m.Name), "whisper") {
			t.Fatalf("Whisper leaked into catalog: %+v", m)
		}
		if m.Kind != expect.kind || m.Name == "" || m.Language == "" || m.URL == "" || m.Description == "" || m.Recommended == "" || m.SizeMB <= 0 || len(m.RequiredFiles) == 0 {
			t.Fatalf("incomplete model metadata: %+v", m)
		}
		if expect.bytes != 0 && m.ArchiveBytes != expect.bytes {
			t.Fatalf("%s archive bytes=%d, want %d", m.ID, m.ArchiveBytes, expect.bytes)
		}
	}
}

func TestDownloadRejectsChangedOfficialAssetSize(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "3")
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("abc"))
		}
	}))
	defer ts.Close()
	m := Meta{ID: "changed", Name: "changed", URL: ts.URL, ArchiveBytes: 4}
	err := DownloadWithOptions(context.Background(), m, t.TempDir(), DownloadOptions{Client: ts.Client()})
	if err == nil || !strings.Contains(err.Error(), "预期 4") {
		t.Fatalf("size mismatch error=%v", err)
	}
}

func TestFunASRNanoRequiredFilesAreStrict(t *testing.T) {
	m, ok := Find("funasr-nano-int8")
	if !ok {
		t.Fatal("FunASR Nano missing")
	}
	d := t.TempDir()
	root := filepath.Join(d, m.ID)
	for _, name := range m.RequiredFiles {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateInstalled(m, d); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "Qwen3-0.6B", "vocab.json")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInstalled(m, d); err == nil {
		t.Fatal("missing tokenizer vocabulary accepted")
	}
}

func TestExtractAtomicAndTraversal(t *testing.T) {
	d := t.TempDir()
	good := makeArchive(t, map[string]string{"root/model.int8.onnx": "model", "root/tokens.txt": "tokens"})
	target := filepath.Join(d, "model")
	if err := extractTar(tar.NewReader(bytes.NewReader(good)), target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "tokens.txt")); err != nil {
		t.Fatal(err)
	}
	bad := makeArchive(t, map[string]string{"root/../../escaped": "bad"})
	if err := extractTar(tar.NewReader(bytes.NewReader(bad)), filepath.Join(d, "bad")); err == nil {
		t.Fatal("wanted traversal error")
	}
}

func TestDeleteModel(t *testing.T) {
	d := t.TempDir()
	m := Catalog[0]
	root := filepath.Join(d, m.ID)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	for _, n := range m.RequiredFiles {
		if err := os.WriteFile(filepath.Join(root, n), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if !Installed(m, d) {
		t.Fatal("expected installed before delete")
	}
	if err := Delete(m, d); err != nil {
		t.Fatal(err)
	}
	if Installed(m, d) {
		t.Fatal("expected not installed after delete")
	}
	// Deleting again should not error.
	if err := Delete(m, d); err != nil {
		t.Fatal(err)
	}
}

func makeArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	var err error
	for name, value := range files {
		if err = tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(value))}); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
