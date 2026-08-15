package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	ModelDir   string         `json:"model_dir"`
	RuntimeDir string         `json:"runtime_dir"`
	Listen     string         `json:"listen"`
	GOMAXPROCS int            `json:"gomaxprocs"`
	Debug      bool           `json:"debug"`
	Audio      AudioConfig    `json:"audio"`
	ASR        ASRConfig      `json:"asr"`
	DeepSeek   DeepSeekConfig `json:"deepseek"`
	Subtitle   SubtitleConfig `json:"subtitle"`
}

type AudioConfig struct {
	DeviceIndex     int `json:"device_index"`
	SilenceMS       int `json:"silence_ms"`
	MinSpeechMS     int `json:"min_speech_ms"`
	MaxSpeechSecond int `json:"max_speech_seconds"`
}

type ASRConfig struct {
	ModelID    string `json:"model_id"`
	NumThreads int    `json:"num_threads"`
}

type DeepSeekConfig struct {
	APIKey    string `json:"api_key"`
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model"`
	TimeoutMS int    `json:"timeout_ms"`
	Retries   int    `json:"retries"`
}

type SubtitleConfig struct {
	Mode            string `json:"mode"`
	HideAfterMS     int    `json:"hide_after_ms"`
	EnglishFontSize int    `json:"english_font_size"`
	ChineseFontSize int    `json:"chinese_font_size"`
	PositionX       int    `json:"position_x_percent"`
	PositionY       int    `json:"position_y_percent"`
	MaxWidth        int    `json:"max_width_percent"`
	EnglishColor    string `json:"english_color"`
	ChineseColor    string `json:"chinese_color"`
	StrokeColor     string `json:"stroke_color"`
	Background      string `json:"background"`
	FontFamily      string `json:"font_family"`
}

func Defaults() Config {
	return Config{
		ModelDir: "models", RuntimeDir: "runtime", Listen: ":8765", GOMAXPROCS: 3,
		Audio:    AudioConfig{DeviceIndex: -1, SilenceMS: 500, MinSpeechMS: 300, MaxSpeechSecond: 10},
		ASR:      ASRConfig{ModelID: "", NumThreads: 2},
		DeepSeek: DeepSeekConfig{Endpoint: "https://api.deepseek.com/chat/completions", Model: "deepseek-chat", TimeoutMS: 5000, Retries: 2},
		Subtitle: SubtitleConfig{Mode: "bilingual", HideAfterMS: 12000, EnglishFontSize: 56, ChineseFontSize: 30, PositionX: 50, PositionY: 88, MaxWidth: 90, EnglishColor: "#ffffff", ChineseColor: "#f0f0f0", StrokeColor: "#000000", Background: "rgba(0,0,0,0.48)", FontFamily: "Segoe UI, Microsoft YaHei, sans-serif"},
	}
}

func Load(path string) (Config, error) {
	c := Defaults()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("读取配置 %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	if c.ModelDir == "" {
		c.ModelDir = "models"
	}
	if c.RuntimeDir == "" {
		c.RuntimeDir = "runtime"
	}
	if c.Listen == "" {
		c.Listen = ":8765"
	}
	if c.GOMAXPROCS < 1 {
		c.GOMAXPROCS = 3
	}
	if c.ASR.NumThreads < 1 {
		c.ASR.NumThreads = 2
	}
	if c.DeepSeek.TimeoutMS < 1 {
		c.DeepSeek.TimeoutMS = 5000
	}
	defaults := Defaults().Subtitle
	if c.Subtitle.Mode == "" {
		c.Subtitle = defaults
	}
	if c.Subtitle.HideAfterMS < 0 {
		c.Subtitle.HideAfterMS = 0
	}
	if c.Subtitle.EnglishFontSize < 12 {
		c.Subtitle.EnglishFontSize = defaults.EnglishFontSize
	}
	if c.Subtitle.ChineseFontSize < 10 {
		c.Subtitle.ChineseFontSize = defaults.ChineseFontSize
	}
	if c.Subtitle.PositionX < 0 || c.Subtitle.PositionX > 100 {
		c.Subtitle.PositionX = defaults.PositionX
	}
	if c.Subtitle.PositionY < 0 || c.Subtitle.PositionY > 100 {
		c.Subtitle.PositionY = defaults.PositionY
	}
	if c.Subtitle.MaxWidth < 20 || c.Subtitle.MaxWidth > 100 {
		c.Subtitle.MaxWidth = defaults.MaxWidth
	}
	if c.Subtitle.EnglishColor == "" {
		c.Subtitle.EnglishColor = defaults.EnglishColor
	}
	if c.Subtitle.ChineseColor == "" {
		c.Subtitle.ChineseColor = defaults.ChineseColor
	}
	if c.Subtitle.StrokeColor == "" {
		c.Subtitle.StrokeColor = defaults.StrokeColor
	}
	if c.Subtitle.Background == "" {
		c.Subtitle.Background = defaults.Background
	}
	if c.Subtitle.FontFamily == "" {
		c.Subtitle.FontFamily = defaults.FontFamily
	}
	if c.DeepSeek.APIKey == "" {
		c.DeepSeek.APIKey = os.Getenv("DEEPSEEK_API_KEY")
	}
	base, _ := filepath.Abs(filepath.Dir(path))
	if !filepath.IsAbs(c.ModelDir) {
		c.ModelDir = filepath.Join(base, c.ModelDir)
	}
	if !filepath.IsAbs(c.RuntimeDir) {
		c.RuntimeDir = filepath.Join(base, c.RuntimeDir)
	}
	return c, nil
}

func (c Config) Validate(requireKey bool) error {
	if requireKey && (strings.TrimSpace(c.DeepSeek.APIKey) == "" || strings.Contains(c.DeepSeek.APIKey, "请在")) {
		return errors.New("请在 config.json 的 deepseek.api_key 中填写密钥")
	}
	if c.ASR.NumThreads > 4 {
		return errors.New("asr.num_threads 不应超过 4，以免影响游戏")
	}
	if c.Subtitle.Mode != "english" && c.Subtitle.Mode != "bilingual" {
		return errors.New("subtitle.mode 只能是 english 或 bilingual")
	}
	return nil
}

func (c Config) TranslationTimeout() time.Duration {
	return time.Duration(c.DeepSeek.TimeoutMS) * time.Millisecond
}
