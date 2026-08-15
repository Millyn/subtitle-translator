package translate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPurePunctuationIsIgnored(t *testing.T) {
	for _, input := range []string{"。！？…", " ,?!  ", "——"} {
		v, err := New("").Translate(context.Background(), input)
		if err != nil || v != "" {
			t.Fatalf("%q: got %q, %v", input, v, err)
		}
	}
}

func TestTranslateUsesConfig(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q", got)
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "custom-model" {
			t.Errorf("model = %q", body.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":" Hello! "}}]}`))
	}))
	defer ts.Close()

	c := NewWithConfig(Config{APIKey: "secret", Endpoint: ts.URL, Model: "custom-model", Retries: -1})
	got, err := c.Translate(context.Background(), "你好")
	if err != nil || got != "Hello!" || calls.Load() != 1 {
		t.Fatalf("got %q, calls %d, err %v", got, calls.Load(), err)
	}
}

func TestRetryServerError(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer ts.Close()
	c := NewWithConfig(Config{APIKey: "k", Endpoint: ts.URL, Retries: 1})
	got, err := c.Translate(context.Background(), "测试")
	if err != nil || got != "ok" || calls.Load() != 2 {
		t.Fatalf("got %q, calls %d, err %v", got, calls.Load(), err)
	}
}

func TestRetriesCanBeDisabled(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "temporary", http.StatusServiceUnavailable)
	}))
	defer ts.Close()
	c := NewWithConfig(Config{APIKey: "k", Endpoint: ts.URL, Retries: 0})
	_, _ = c.Translate(context.Background(), "测试")
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}
