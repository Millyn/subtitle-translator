package ws

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBroadcastMultipleClients(t *testing.T) {
	s := New("")
	httpServer := httptest.NewServer(s.Handler())
	defer httpServer.Close()
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"
	clients := make([]*websocket.Conn, 2)
	for i := range clients {
		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatal(err)
		}
		clients[i] = conn
		defer conn.Close()
	}
	deadline := time.Now().Add(time.Second)
	for s.ClientCount() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s.Broadcast("hello")
	for i := range clients {
		_, b, err := clients[i].ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var msg Message
		if err := json.Unmarshal(b, &msg); err != nil || msg.Type != "" || msg.Text != "hello" || msg.TS == 0 {
			t.Fatalf("message = %+v, err = %v", msg, err)
		}
	}
}

func TestBroadcastMessageDefaultsAndClientRemoval(t *testing.T) {
	s := New("")
	if err := s.BroadcastMessage(Message{Type: "subtitle", Text: "none"}); err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	url := "ws" + strings.TrimPrefix(h.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for s.ClientCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	_ = conn.Close()
	for s.ClientCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.ClientCount() != 0 {
		t.Fatalf("clients=%d", s.ClientCount())
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPageAndConfigEndpoints(t *testing.T) {
	s := NewWithPage("", []byte("<html>subtitle editor</html>"), PageConfig{Mode: "bilingual", HideAfterMS: 12000})
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	for _, path := range []string{"/subtitle", "/editor"} {
		resp, err := http.Get(h.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), "subtitle editor") {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
	resp, err := http.Get(h.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var cfg PageConfig
	if err = json.NewDecoder(resp.Body).Decode(&cfg); err != nil || cfg.Mode != "bilingual" || cfg.HideAfterMS != 12000 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestBroadcastSubtitleIncludesBothLanguages(t *testing.T) {
	s := New("")
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	url := "ws" + strings.TrimPrefix(h.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	deadline := time.Now().Add(time.Second)
	for s.ClientCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s.BroadcastSubtitle("你好", "Hello")
	_, b, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var msg Message
	if err = json.Unmarshal(b, &msg); err != nil || msg.Source != "你好" || msg.Text != "Hello" {
		t.Fatalf("msg=%+v err=%v", msg, err)
	}
}

func TestStartContextCancellation(t *testing.T) {
	s := New("127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.StartContext(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown timeout")
	}
}

func BenchmarkBroadcastNoClients(b *testing.B) {
	s := New("")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s.Broadcast("subtitle")
	}
}

func TestConcurrentBroadcast(t *testing.T) {
	s := New("")
	httpServer := httptest.NewServer(s.Handler())
	defer httpServer.Close()
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() {
		for i := 0; i < 20; i++ {
			_, _, _ = conn.ReadMessage()
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Broadcast("x") }()
	}
	wg.Wait()
}
