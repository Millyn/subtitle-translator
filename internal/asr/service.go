// Package asr exposes a model-independent, concurrency-safe speech recognizer.
package asr

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
)

var ErrClosed = errors.New("ASR 服务已关闭")

type Transcriber interface {
	Transcribe([]byte) (string, error)
	Close() error
}

type Service struct {
	Exe, Dir, Kind string
	NumThreads     int
	mu             sync.Mutex
	closed         bool
	cmd            *exec.Cmd
	conn           *websocket.Conn
}

// New validates both the official sherpa-onnx-offline executable and every
// required model file. If exe is empty, SHERPA_ONNX_EXE and PATH are searched.
func New(exe, dir, kind string) (*Service, error) {
	return NewWithThreads(exe, dir, kind, 2)
}

func NewWithThreads(exe, dir, kind string, numThreads int) (*Service, error) {
	resolved, err := resolveExecutable(exe)
	if err != nil {
		return nil, err
	}
	if numThreads < 1 {
		numThreads = 2
	}
	s := &Service{Exe: resolved, Dir: dir, Kind: strings.ToLower(kind), NumThreads: numThreads}
	if _, err = s.args("probe.wav"); err != nil {
		return nil, err
	}
	if err = s.startServer(); err != nil {
		return nil, err
	}
	return s, nil
}

func resolveExecutable(exe string) (string, error) {
	choices := []string{exe, os.Getenv("SHERPA_ONNX_EXE")}
	for _, p := range choices {
		if p == "" {
			continue
		}
		a, err := filepath.Abs(p)
		if err == nil {
			if st, e := os.Stat(a); e == nil && !st.IsDir() {
				return a, nil
			}
		}
	}
	for _, name := range []string{"sherpa-onnx-offline.exe", "sherpa-onnx-offline"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("找不到 sherpa-onnx-offline；请用 --sherpa-exe、SHERPA_ONNX_EXE 或 PATH 指定官方运行程序")
}

func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.conn != nil {
		_ = s.conn.WriteMessage(websocket.TextMessage, []byte("Done"))
		_ = s.conn.Close()
		s.conn = nil
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
		s.cmd = nil
	}
	return nil
}

func (s *Service) Transcribe(pcm []byte) (string, error) {
	if len(pcm) < 2 || len(pcm)%2 != 0 {
		return "", nil
	}
	// Ignore segments below the documented 0.3-second minimum.
	if len(pcm) < 16000*2*3/10 {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", ErrClosed
	}
	if s.conn == nil {
		return "", errors.New("sherpa-onnx ASR 服务未连接")
	}
	floats := make([]byte, len(pcm)*2)
	for i := 0; i < len(pcm); i += 2 {
		v := int16(binary.LittleEndian.Uint16(pcm[i:]))
		binary.LittleEndian.PutUint32(floats[i*2:], math.Float32bits(float32(v)/32768))
	}
	message := make([]byte, 8+len(floats))
	binary.LittleEndian.PutUint32(message, 16000)
	binary.LittleEndian.PutUint32(message[4:], uint32(len(floats)))
	copy(message[8:], floats)
	_ = s.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if err := s.conn.WriteMessage(websocket.BinaryMessage, message); err != nil {
		return "", fmt.Errorf("发送 ASR 音频: %w", err)
	}
	_ = s.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, out, err := s.conn.ReadMessage()
	if err != nil {
		return "", fmt.Errorf("接收 ASR 结果: %w", err)
	}
	text := strings.TrimSpace(string(out))
	if text == "<EMPTY>" {
		return "", nil
	}
	if strings.HasPrefix(text, "{") {
		var result struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(out, &result) == nil {
			text = result.Text
		}
	}
	return normalizeText(text), nil
	/* legacy one-shot fallback retained below for reference
	f, err := os.CreateTemp("", "subtitle-asr-*.wav")
	if err != nil {
		return "", err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = writeWAV(f, pcm); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	args, err := s.args(name)
	if err != nil {
		return "", err
	}
	cmd := exec.Command(s.Exe, args...)
	cmd.Dir = s.Dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("sherpa-onnx-offline 识别失败: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return parseOutput(string(out), name), nil */
}

func (s *Service) startServer() error {
	serverExe := filepath.Join(filepath.Dir(s.Exe), "sherpa-onnx-offline-websocket-server.exe")
	if st, err := os.Stat(serverExe); err != nil || st.IsDir() {
		return fmt.Errorf("ASR 运行库缺少 %s", serverExe)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	base, err := s.args("unused.wav")
	if err != nil {
		return err
	}
	base = base[:len(base)-1]
	args := append(base, "--port="+strconv.Itoa(port), "--num-io-threads=1", "--num-work-threads=1", "--max-batch-size=1")
	cmd := exec.Command(serverExe, args...)
	cmd.Dir = filepath.Dir(serverExe)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("启动 ASR 服务: %w", err)
	}
	s.cmd = cmd
	url := "ws://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, _, dialErr := websocket.DefaultDialer.Dial(url, nil)
		if dialErr == nil {
			s.conn = conn
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	s.cmd = nil
	return errors.New("sherpa-onnx ASR 服务启动超时")
}

func (s *Service) args(wav string) ([]string, error) {
	require := func(names ...string) (string, error) {
		for _, n := range names {
			p := filepath.Join(s.Dir, n)
			if st, e := os.Stat(p); e == nil && !st.IsDir() && st.Size() > 0 {
				return p, nil
			}
		}
		return "", fmt.Errorf("%s 模型目录缺少文件（候选: %s）", s.Kind, strings.Join(names, ", "))
	}
	threads := s.NumThreads
	if threads <= 0 {
		threads = 2
	}
	common := []string{fmt.Sprintf("--num-threads=%d", threads), "--debug=0"}
	switch s.Kind {
	case "paraformer":
		tokens, e := require("tokens.txt")
		if e != nil {
			return nil, e
		}
		m, e := require("model.int8.onnx", "model.onnx")
		if e != nil {
			return nil, e
		}
		return append([]string{"--tokens=" + tokens, "--paraformer=" + m}, append(common, wav)...), nil
	case "sensevoice", "sense-voice":
		tokens, e := require("tokens.txt")
		if e != nil {
			return nil, e
		}
		m, e := require("model.int8.onnx", "model.onnx")
		if e != nil {
			return nil, e
		}
		return append([]string{"--tokens=" + tokens, "--sense-voice-model=" + m, "--sense-voice-language=auto", "--sense-voice-use-itn=1"}, append(common, wav)...), nil
	case "fire-red-asr-ctc", "fireredasr2-ctc":
		tokens, e := require("tokens.txt")
		if e != nil {
			return nil, e
		}
		m, e := require("model.int8.onnx", "model.onnx")
		if e != nil {
			return nil, e
		}
		return append([]string{"--tokens=" + tokens, "--fire-red-asr-ctc=" + m}, append(common, wav)...), nil
	case "funasr-nano":
		encoder, e := require("encoder_adaptor.int8.onnx")
		if e != nil {
			return nil, e
		}
		embedding, e := require("embedding.int8.onnx")
		if e != nil {
			return nil, e
		}
		llm, e := require("llm.int8.onnx")
		if e != nil {
			return nil, e
		}
		tokenizer := filepath.Join(s.Dir, "Qwen3-0.6B")
		for _, name := range []string{"merges.txt", "tokenizer.json", "vocab.json"} {
			if _, e = require(filepath.Join("Qwen3-0.6B", name)); e != nil {
				return nil, e
			}
		}
		return append([]string{"--funasr-nano-encoder-adaptor=" + encoder, "--funasr-nano-embedding=" + embedding, "--funasr-nano-llm=" + llm, "--funasr-nano-tokenizer=" + tokenizer, "--funasr-nano-itn=1"}, append(common, wav)...), nil
	default:
		return nil, fmt.Errorf("不支持模型类型 %q", s.Kind)
	}
}

var timingLine = regexp.MustCompile(`(?i)^(elapsed|real time|wave duration|rtf|filename|creating|started|done)[: =]`)

func parseOutput(out, wav string) string {
	var candidates []string
	scanner := bufio.NewScanner(strings.NewReader(out))
	base := filepath.Base(wav)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "{") {
			var result struct {
				Text string `json:"text"`
			}
			if json.Unmarshal([]byte(line), &result) == nil && strings.TrimSpace(result.Text) != "" {
				return normalizeText(result.Text)
			}
		}
		if line == "" || timingLine.MatchString(line) {
			continue
		}
		if i := strings.Index(line, base); i >= 0 {
			line = strings.TrimSpace(strings.TrimLeft(line[i+len(base):], ":=> \t"))
		}
		if line != "" {
			candidates = append(candidates, line)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return normalizeText(candidates[len(candidates)-1])
}

func normalizeText(v string) string {
	v = strings.TrimSpace(v)
	var b strings.Builder
	runes := []rune(v)
	for i, r := range runes {
		if unicode.IsSpace(r) {
			if i > 0 && i+1 < len(runes) && isCJK(runes[i-1]) && isCJK(runes[i+1]) {
				continue
			}
			if b.Len() > 0 && !strings.HasSuffix(b.String(), " ") {
				b.WriteByte(' ')
			}
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
}

func writeWAV(w io.Writer, pcm []byte) error {
	b := new(bytes.Buffer)
	b.WriteString("RIFF")
	_ = binary.Write(b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(b, binary.LittleEndian, uint32(16))
	_ = binary.Write(b, binary.LittleEndian, uint16(1))
	_ = binary.Write(b, binary.LittleEndian, uint16(1))
	_ = binary.Write(b, binary.LittleEndian, uint32(16000))
	_ = binary.Write(b, binary.LittleEndian, uint32(32000))
	_ = binary.Write(b, binary.LittleEndian, uint16(2))
	_ = binary.Write(b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	_ = binary.Write(b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	_, err := w.Write(b.Bytes())
	return err
}
