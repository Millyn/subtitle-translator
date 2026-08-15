package ws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const writeTimeout = time.Second

type Message struct {
	Type   string  `json:"type,omitempty"`
	Text   string  `json:"text"`
	Source string  `json:"source,omitempty"`
	TS     float64 `json:"ts"`
}

type PageConfig struct {
	Mode            string `json:"mode"`
	HideAfterMS     int    `json:"hideAfterMS"`
	EnglishFontSize int    `json:"englishFontSize"`
	ChineseFontSize int    `json:"chineseFontSize"`
	PositionX       int    `json:"positionX"`
	PositionY       int    `json:"positionY"`
	MaxWidth        int    `json:"maxWidth"`
	EnglishColor    string `json:"englishColor"`
	ChineseColor    string `json:"chineseColor"`
	StrokeColor     string `json:"strokeColor"`
	Background      string `json:"background"`
	FontFamily      string `json:"fontFamily"`
}

type client struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

type Server struct {
	mu      sync.RWMutex
	clients map[*client]struct{}
	srv     *http.Server
	page    []byte
	config  PageConfig
}

func New(addr string) *Server {
	return NewWithPage(addr, nil, PageConfig{})
}

func NewWithPage(addr string, page []byte, cfg PageConfig) *Server {
	s := &Server{clients: make(map[*client]struct{}), page: append([]byte(nil), page...), config: cfg}
	s.srv = &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handle)
	mux.HandleFunc("/config", s.handleConfig)
	mux.HandleFunc("/", s.handlePage)
	return mux
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/subtitle" && r.URL.Path != "/editor" {
		http.NotFound(w, r)
		return
	}
	if len(s.page) == 0 {
		http.Error(w, "subtitle page is not configured", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(s.page)
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(s.config)
}

var upgrader = websocket.Upgrader{
	HandshakeTimeout: 5 * time.Second,
	CheckOrigin:      func(*http.Request) bool { return true }, // OBS browser sources may send a file:// origin.
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{conn: conn}
	conn.SetReadLimit(1024)
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	defer s.remove(c)
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (s *Server) remove(c *client) {
	s.mu.Lock()
	if _, exists := s.clients[c]; exists {
		delete(s.clients, c)
		_ = c.conn.Close()
	}
	s.mu.Unlock()
}

func (s *Server) Start() error {
	err := s.srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// StartContext starts the server and shuts it down when ctx is cancelled.
func (s *Server) StartContext(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = s.Shutdown(shutdownCtx)
		case <-done:
		}
	}()
	err := s.Start()
	close(done)
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	err := s.srv.Shutdown(ctx)
	s.mu.Lock()
	clients := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
		delete(s.clients, c)
	}
	s.mu.Unlock()
	for _, c := range clients {
		c.mu.Lock()
		_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutdown"), time.Now().Add(writeTimeout))
		_ = c.conn.Close()
		c.mu.Unlock()
	}
	return err
}

func (s *Server) Broadcast(text string) {
	_ = s.BroadcastMessage(Message{Text: text, TS: float64(time.Now().UnixMilli()) / 1000})
}

func (s *Server) BroadcastSubtitle(source, translation string) {
	_ = s.BroadcastMessage(Message{Type: "subtitle", Text: translation, Source: source, TS: float64(time.Now().UnixMilli()) / 1000})
}

func (s *Server) BroadcastMessage(msg Message) error {
	if msg.TS == 0 {
		msg.TS = float64(time.Now().UnixMilli()) / 1000
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	s.mu.RLock()
	clients := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.RUnlock()
	for _, c := range clients {
		c.mu.Lock()
		_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		err := c.conn.WriteMessage(websocket.TextMessage, payload)
		c.mu.Unlock()
		if err != nil {
			s.remove(c)
		}
	}
	return nil
}

func (s *Server) ClientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}
