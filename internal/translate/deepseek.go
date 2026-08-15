package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
)

const defaultEndpoint = "https://api.deepseek.com/chat/completions"

type Config struct {
	APIKey   string
	Endpoint string
	Model    string
	Timeout  time.Duration
	Retries  int
}

type Client struct {
	Key      string // Kept for source compatibility; prefer Config.APIKey.
	Endpoint string
	Model    string
	Retries  int
	Timeout  time.Duration
	HTTP     *http.Client
}

func New(key string) *Client { return NewWithConfig(Config{APIKey: key, Retries: 2}) }

func NewWithConfig(cfg Config) *Client {
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultEndpoint
	}
	if cfg.Model == "" {
		cfg.Model = "deepseek-chat"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.Retries < 0 {
		cfg.Retries = 0
	}
	return &Client{Key: cfg.APIKey, Endpoint: cfg.Endpoint, Model: cfg.Model, Retries: cfg.Retries, Timeout: cfg.Timeout, HTTP: &http.Client{Timeout: cfg.Timeout}}
}

func translatable(s string) bool {
	if len([]rune(strings.TrimSpace(s))) < 2 {
		return false
	}
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func (c *Client) Translate(ctx context.Context, source string) (string, error) {
	source = strings.TrimSpace(source)
	if !translatable(source) {
		return "", nil
	}
	if strings.TrimSpace(c.Key) == "" {
		return "", errors.New("DeepSeek API key is empty")
	}
	body := map[string]any{
		"model":       c.Model,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": "You are a professional translator. Translate the given Chinese text into English. Output only the English translation, no explanations."},
			{"role": "user", "content": source},
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	var last error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt) * 100 * time.Millisecond
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(b))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+c.Key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			last = err
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			last = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			last = fmt.Errorf("DeepSeek returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
			// Authentication and ordinary request errors will not improve on retry.
			if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				break
			}
			continue
		}
		var out struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(data, &out); err != nil {
			last = fmt.Errorf("decode DeepSeek response: %w", err)
			continue
		}
		if len(out.Choices) == 0 {
			last = errors.New("DeepSeek response contains no choices")
			continue
		}
		return strings.TrimSpace(out.Choices[0].Message.Content), nil
	}
	if last == nil {
		last = errors.New("DeepSeek request failed")
	}
	return "", last
}
