package ws

import (
	"bytes"
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
	for _, path := range []string{"/subtitle", "/editor", "/control", "/debug"} {
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

func TestControlAPIAndValidation(t *testing.T) {
	s := New("")
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	state := ControlState{Profile: "minecraft", CorrectionMode: "conservative", ChineseSource: "compare", ContextSize: 5, CustomPrompt: "creeper", Terms: []Term{{Source: "苦力怕", Target: "Creeper"}}}
	body, _ := json.Marshal(state)
	req, _ := http.NewRequest(http.MethodPut, h.URL+"/api/control", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got ControlState
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || resp.StatusCode != http.StatusOK || got.Profile != "minecraft" || len(got.Terms) != 1 || got.UpdatedAt == 0 {
		t.Fatalf("status=%d state=%+v err=%v", resp.StatusCode, got, err)
	}

	bad := []byte(`{"profile":"unknown","correctionMode":"raw","contextSize":0,"terms":[]}`)
	req, _ = http.NewRequest(http.MethodPut, h.URL+"/api/control", bytes.NewReader(bad))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad status=%d", resp.StatusCode)
	}
}

func TestControlCallbackReceivesNilTermsOnProfileSwitch(t *testing.T) {
	s := New("")
	called := false
	s.SetControlCallbacks(ControlCallbacks{Apply: func(_ context.Context, state ControlState) (ControlState, error) {
		called = true
		if state.Terms != nil {
			t.Fatalf("profile switch terms=%#v, want nil", state.Terms)
		}
		state.Terms = []Term{{Source: "弯道", Target: "corner"}}
		return state, nil
	}})
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	body := []byte(`{"profile":"iracing","customPrompt":"","correctionMode":"conservative","chineseSource":"corrected","contextSize":3,"terms":null}`)
	req, _ := http.NewRequest(http.MethodPut, h.URL+"/api/control", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got ControlState
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if !called || resp.StatusCode != http.StatusOK || len(got.Terms) != 1 {
		t.Fatalf("called=%v status=%d got=%+v", called, resp.StatusCode, got)
	}
}

func TestLocalAccessPolicy(t *testing.T) {
	s := NewWithPage("", []byte("page"), PageConfig{})
	s.SetLocalAccessPolicy(func(*http.Request) bool { return false })
	for _, path := range []string{"/control", "/debug", "/api/control", "/debug-ws"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d", path, w.Code)
		}
	}
	// OBS remains remotely accessible.
	req := httptest.NewRequest(http.MethodGet, "/subtitle", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("subtitle status=%d", w.Code)
	}
}

func TestBroadcastDebug(t *testing.T) {
	s := New("")
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	url := "ws" + strings.TrimPrefix(h.URL, "http") + "/debug-ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	deadline := time.Now().Add(time.Second)
	for s.ClientCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := s.BroadcastDebug(DebugEvent{Raw: "泥豪", Corrected: "你好", English: "Hello", Profile: "general", Latencies: map[string]float64{"total": 321}, Tokens: 12, CacheHit: true}); err != nil {
		t.Fatal(err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var msg Message
	if err := json.Unmarshal(payload, &msg); err != nil || msg.Type != "debug" || msg.Debug == nil || msg.Debug.Corrected != "你好" || msg.Debug.TS == 0 {
		t.Fatalf("msg=%+v err=%v", msg, err)
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
