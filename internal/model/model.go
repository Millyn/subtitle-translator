// Package model owns the downloadable sherpa-onnx model catalogue.
package model

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Meta struct {
	ID, Name, Kind, Language, URL, Recommended string
	Description                                string
	SizeMB                                     int
	ArchiveBytes                               int64  // Exact release asset size when verified.
	SHA256                                     string // Optional; checked when supplied.
	RequiredFiles                              []string
}

// Catalog contains only archives published by the sherpa-onnx project. It is
// the single source of truth for models shown by the application; removing an
// entry never deletes a previously downloaded directory.
var Catalog = []Meta{
	{ID: "paraformer-zh", Name: "Paraformer 中文小型 INT8", Kind: "paraformer", Language: "zh/en", URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-paraformer-zh-small-2024-03-09.tar.bz2", Recommended: "推荐-低资源中文", Description: "普通话、英语及部分中文方言；速度快、资源占用低", SizeMB: 84, RequiredFiles: []string{"model.int8.onnx", "tokens.txt"}},
	{ID: "sensevoice-int8", Name: "SenseVoice Small INT8", Kind: "sensevoice", Language: "auto (zh/en/ja/ko/yue)", URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17.tar.bz2", Recommended: "推荐-多语言", Description: "自动识别中英日韩粤语，启用标点和逆文本规范化", SizeMB: 240, RequiredFiles: []string{"model.int8.onnx", "tokens.txt"}},
	{ID: "fire-red-asr2-ctc-int8", Name: "FireRedASR2 CTC zh_en INT8", Kind: "fire-red-asr-ctc", Language: "zh/en + 20多种中文方言", URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-fire-red-asr2-ctc-zh_en-int8-2026-02-25.tar.bz2", Recommended: "推荐-中文方言", Description: "CTC 中文/英语模型，覆盖粤语、四川话、上海话、闽南话等方言；CPU 推理快但模型较大", SizeMB: 497, ArchiveBytes: 520516278, RequiredFiles: []string{"model.int8.onnx", "tokens.txt"}},
	{ID: "funasr-nano-int8", Name: "FunASR Nano INT8", Kind: "funasr-nano", Language: "zh/en/ja + 中文方言", URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-funasr-nano-int8-2025-12-30.tar.bz2", Recommended: "高准确率-高资源", Description: "LLM 辅助多语言识别，支持 ITN；模型约 803 MiB，内存与计算需求明显高于 Paraformer", SizeMB: 803, ArchiveBytes: 841730611, RequiredFiles: []string{"encoder_adaptor.int8.onnx", "embedding.int8.onnx", "llm.int8.onnx", "Qwen3-0.6B/merges.txt", "Qwen3-0.6B/tokenizer.json", "Qwen3-0.6B/vocab.json"}},
}

type ModelInfo struct{ Name, Path, Type, Language string }

func Find(id string) (Meta, bool) {
	for _, m := range Catalog {
		if m.ID == id {
			return m, true
		}
	}
	return Meta{}, false
}

func ValidateInstalled(m Meta, dir string) error {
	root := filepath.Join(dir, m.ID)
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("模型 %s 未安装", m.Name)
	}
	for _, name := range m.RequiredFiles {
		st, err = os.Stat(filepath.Join(root, name))
		if err != nil || st.IsDir() || st.Size() == 0 {
			return fmt.Errorf("模型 %s 缺少或损坏文件 %s", m.Name, name)
		}
	}
	return nil
}

func Installed(m Meta, dir string) bool { return ValidateInstalled(m, dir) == nil }

// Select provides a dependency-light startup interaction that also works in
// Windows PowerShell. Download is deliberately separate so callers can retry.
func Select(in io.Reader, out io.Writer, dir string) (Meta, error) {
	for i, m := range Catalog {
		state := "未下载"
		if Installed(m, dir) {
			state = "已下载"
		}
		fmt.Fprintf(out, "%d. %s [%s] %s · %s · 约 %dMB\n", i, m.Name, state, m.Recommended, m.Language, m.SizeMB)
	}
	fmt.Fprint(out, "选择模型编号（q 取消）: ")
	s := bufio.NewScanner(in)
	if !s.Scan() {
		return Meta{}, errors.New("已取消模型选择")
	}
	v := strings.TrimSpace(s.Text())
	if strings.EqualFold(v, "q") {
		return Meta{}, errors.New("已取消模型选择")
	}
	i, err := strconv.Atoi(v)
	if err != nil || i < 0 || i >= len(Catalog) {
		return Meta{}, errors.New("无效模型编号")
	}
	return Catalog[i], nil
}

type URLInfo struct {
	Size           int64
	RangeSupported bool
	FinalURL       string
}

// ValidateURL accepts servers that reject HEAD by probing byte zero with GET.
func ValidateURLInfo(ctx context.Context, client *http.Client, url string) (URLInfo, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	probe := func(method string, ranged bool) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			return nil, err
		}
		if ranged {
			req.Header.Set("Range", "bytes=0-0")
		}
		return client.Do(req)
	}
	r, err := probe(http.MethodHead, false)
	if err == nil && r.StatusCode >= 200 && r.StatusCode < 400 {
		info := URLInfo{Size: r.ContentLength, RangeSupported: strings.EqualFold(r.Header.Get("Accept-Ranges"), "bytes"), FinalURL: r.Request.URL.String()}
		r.Body.Close()
		if info.RangeSupported {
			return info, nil
		}
		// Some CDNs support byte ranges without advertising Accept-Ranges.
		if rr, e := probe(http.MethodGet, true); e == nil {
			defer rr.Body.Close()
			if rr.StatusCode == http.StatusPartialContent {
				info.RangeSupported = true
				if total := contentRangeTotal(rr.Header.Get("Content-Range"), info.Size); total > 0 {
					info.Size = total
				}
			}
		}
		return info, nil
	}
	if r != nil {
		r.Body.Close()
	}
	r, err = probe(http.MethodGet, true)
	if err != nil {
		return URLInfo{}, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK && r.StatusCode != http.StatusPartialContent {
		return URLInfo{}, fmt.Errorf("模型链接返回 %s", r.Status)
	}
	return URLInfo{Size: contentRangeTotal(r.Header.Get("Content-Range"), r.ContentLength), RangeSupported: r.StatusCode == http.StatusPartialContent, FinalURL: r.Request.URL.String()}, nil
}

func ValidateURL(ctx context.Context, url string) error {
	_, err := ValidateURLInfo(ctx, nil, url)
	return err
}

func contentRangeTotal(v string, fallback int64) int64 {
	if i := strings.LastIndexByte(v, '/'); i >= 0 {
		if n, err := strconv.ParseInt(v[i+1:], 10, 64); err == nil {
			return n
		}
	}
	return fallback
}

func verifySHA(path, want string) error {
	if want == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("SHA-256 不匹配: 得到 %s", got)
	}
	return nil
}
