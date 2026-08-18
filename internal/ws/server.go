package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"subtitle-translator/internal/model"
	"subtitle-translator/internal/translate"
)

const writeTimeout = time.Second

type Message struct {
	Type          string      `json:"type,omitempty"`
	Text          string      `json:"text,omitempty"`
	Source        string      `json:"source,omitempty"`
	Raw           string      `json:"raw,omitempty"`
	Corrected     string      `json:"corrected,omitempty"`
	English       string      `json:"english,omitempty"`
	Profile       string      `json:"profile,omitempty"`
	ChineseSource string      `json:"chineseSource,omitempty"`
	Debug         *DebugEvent `json:"debug,omitempty"`
	TS            float64     `json:"ts"`
}

type DebugEvent struct {
	SegmentID     uint64             `json:"segmentId,omitempty"`
	DurationMS    int64              `json:"durationMs,omitempty"`
	SegmentReason string             `json:"segmentReason,omitempty"`
	ASRModel      string             `json:"asrModel,omitempty"`
	Raw           string             `json:"raw,omitempty"`
	Corrected     string             `json:"corrected,omitempty"`
	English       string             `json:"english,omitempty"`
	Diff          string             `json:"diff,omitempty"`
	Profile       string             `json:"profile,omitempty"`
	Terms         []string           `json:"terms,omitempty"`
	Context       []string           `json:"context,omitempty"`
	Latencies     map[string]float64 `json:"latencies,omitempty"`
	Tokens        int                `json:"tokens,omitempty"`
	TokenUsage    map[string]int     `json:"tokenUsage,omitempty"`
	CacheHit      bool               `json:"cacheHit,omitempty"`
	Retries       int                `json:"retries,omitempty"`
	Error         string             `json:"error,omitempty"`
	TS            float64            `json:"ts,omitempty"`
	RequestBody   string             `json:"requestBody,omitempty"`
	ResponseBody  string             `json:"responseBody,omitempty"`
}

type Term struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type ControlState struct {
	Profile        string  `json:"profile"`
	CustomPrompt   string  `json:"customPrompt"`
	SystemPrompt   string  `json:"systemPrompt,omitempty"`
	CorrectionMode string  `json:"correctionMode"`
	ChineseSource  string  `json:"chineseSource"`
	ContextSize    int     `json:"contextSize"`
	Terms          []Term  `json:"terms"`
	ResetTerms     bool    `json:"resetTerms,omitempty"`
	UpdatedAt      float64 `json:"updatedAt"`
}

type ControlCallbacks struct {
	Get   func(context.Context) (ControlState, error)
	Apply func(context.Context, ControlState) (ControlState, error)
}

type AccessPolicy func(*http.Request) bool

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
	ChineseSource   string `json:"chineseSource"`
}

type client struct {
	conn  *websocket.Conn
	mu    sync.Mutex
	debug bool
}

type Server struct {
	mu             sync.RWMutex
	clients        map[*client]struct{}
	srv            *http.Server
	page           []byte
	modelsPage     []byte
	dashboardPage  []byte
	editorPage     []byte
	debugPage      []byte
	promptPage     []byte
	modelDir       string
	currentModelID string
	debugEnabled   atomic.Bool
	config         PageConfig
	control        ControlState
	callbacks      ControlCallbacks
	access         AccessPolicy
}

func New(addr string) *Server {
	return NewWithPage(addr, nil, PageConfig{})
}

func NewWithPage(addr string, page []byte, cfg PageConfig) *Server {
	s := &Server{
		clients: make(map[*client]struct{}), page: append([]byte(nil), page...), config: cfg,
		control: ControlState{Profile: "auto", CorrectionMode: "conservative", ChineseSource: "corrected", ContextSize: 2, Terms: []Term{}},
		access:  LocalOnly,
	}
	s.srv = &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	return s
}

// SetModelsPage sets the HTML page served for the model management UI.
func (s *Server) SetModelsPage(p []byte) {
	s.mu.Lock()
	s.modelsPage = append([]byte(nil), p...)
	s.mu.Unlock()
}

// SetDashboardPage sets the HTML page served for the unified dashboard.
func (s *Server) SetDashboardPage(p []byte) {
	s.mu.Lock()
	s.dashboardPage = append([]byte(nil), p...)
	s.mu.Unlock()
}

// SetEditorPage sets the HTML page served for the subtitle style editor.
func (s *Server) SetEditorPage(p []byte) {
	s.mu.Lock()
	s.editorPage = append([]byte(nil), p...)
	s.mu.Unlock()
}

// SetDebugPage sets the HTML page served for the debug panel.
func (s *Server) SetDebugPage(p []byte) {
	s.mu.Lock()
	s.debugPage = append([]byte(nil), p...)
	s.mu.Unlock()
}

// SetPromptPage sets the HTML page served for the prompt management UI.
func (s *Server) SetPromptPage(p []byte) {
	s.mu.Lock()
	s.promptPage = append([]byte(nil), p...)
	s.mu.Unlock()
}

// SetModelDir sets the filesystem path where models are stored.
func (s *Server) SetModelDir(dir string) {
	s.mu.Lock()
	s.modelDir = dir
	s.mu.Unlock()
}

// SetCurrentModel sets the ID of the currently active ASR model.
func (s *Server) SetCurrentModel(id string) {
	s.mu.Lock()
	s.currentModelID = id
	s.mu.Unlock()
}

// SetDebugEnabled sets the debug mode state.
func (s *Server) SetDebugEnabled(enabled bool) {
	s.debugEnabled.Store(enabled)
}

// IsDebugEnabled returns the current debug mode state.
func (s *Server) IsDebugEnabled() bool {
	return s.debugEnabled.Load()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) { s.handle(w, r, false) })
	mux.HandleFunc("/debug-ws", func(w http.ResponseWriter, r *http.Request) {
		if !s.allowLocal(w, r) {
			return
		}
		s.handle(w, r, true)
	})
	mux.HandleFunc("/api/control", s.handleControl)
	mux.HandleFunc("/api/models", s.handleModels)
	mux.HandleFunc("/api/models/download", s.handleModelDownload)
	mux.HandleFunc("/api/models/delete", s.handleModelDelete)
	mux.HandleFunc("/api/models/remote", s.handleRemoteModels)
	mux.HandleFunc("/api/debug", s.handleDebugToggle)
	mux.HandleFunc("/api/prompt", s.handlePrompt)
	mux.HandleFunc("/config", s.handleConfig)
	mux.HandleFunc("/", s.handlePage)
	return mux
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/models" {
		s.mu.RLock()
		p := s.modelsPage
		s.mu.RUnlock()
		if len(p) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(p)
		return
	}
	if r.URL.Path == "/dashboard" {
		if !s.allowLocal(w, r) {
			return
		}
		s.mu.RLock()
		p := s.dashboardPage
		s.mu.RUnlock()
		if len(p) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(p)
		return
	}
	if r.URL.Path == "/editor" {
		s.mu.RLock()
		p := s.editorPage
		s.mu.RUnlock()
		if len(p) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(p)
		return
	}
	if r.URL.Path == "/debug" {
		if !s.allowLocal(w, r) {
			return
		}
		s.mu.RLock()
		p := s.debugPage
		s.mu.RUnlock()
		if len(p) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(p)
		return
	}
	if r.URL.Path == "/prompt" {
		if !s.allowLocal(w, r) {
			return
		}
		s.mu.RLock()
		p := s.promptPage
		s.mu.RUnlock()
		if len(p) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(p)
		return
	}
	if r.URL.Path != "/" && r.URL.Path != "/subtitle" && r.URL.Path != "/control" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/control" && !s.allowLocal(w, r) {
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
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()
	_ = json.NewEncoder(w).Encode(cfg)
}

func (s *Server) SetChineseSource(source string) {
	if !slices.Contains(chineseSources, source) {
		return
	}
	s.mu.Lock()
	s.config.ChineseSource = source
	s.mu.Unlock()
}

var upgrader = websocket.Upgrader{
	HandshakeTimeout: 5 * time.Second,
	CheckOrigin:      func(*http.Request) bool { return true }, // OBS browser sources may send a file:// origin.
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request, debug bool) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{conn: conn, debug: debug}
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

func LocalOnly(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (s *Server) SetControlCallbacks(callbacks ControlCallbacks) {
	s.mu.Lock()
	s.callbacks = callbacks
	s.mu.Unlock()
}

func (s *Server) SetLocalAccessPolicy(policy AccessPolicy) {
	if policy == nil {
		policy = LocalOnly
	}
	s.mu.Lock()
	s.access = policy
	s.mu.Unlock()
}

func (s *Server) allowLocal(w http.ResponseWriter, r *http.Request) bool {
	s.mu.RLock()
	policy := s.access
	s.mu.RUnlock()
	if policy == nil || policy(r) {
		return true
	}
	http.Error(w, "local access only", http.StatusForbidden)
	return false
}

var profiles = []string{"auto", "general", "iracing", "minecraft", "project_zomboid", "disabled"}
var correctionModes = []string{"off", "conservative"}
var chineseSources = []string{"corrected", "raw", "compare"}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	if !s.allowLocal(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	s.mu.RLock()
	callbacks, state := s.callbacks, cloneControlState(s.control)
	s.mu.RUnlock()
	previous := cloneControlState(state)
	switch r.Method {
	case http.MethodGet:
		if callbacks.Get != nil {
			var err error
			state, err = callbacks.Get(r.Context())
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, err)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(state)
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&state); err != nil {
			writeAPIError(w, http.StatusBadRequest, fmt.Errorf("invalid control state: %w", err))
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeAPIError(w, http.StatusBadRequest, errors.New("request must contain one JSON object"))
			return
		}
		// Accept control payloads produced by v1.1, where correctionMode was
		// the subtitle Chinese source rather than the AI correction policy.
		if state.ChineseSource == "" {
			if slices.Contains(chineseSources, state.CorrectionMode) {
				state.ChineseSource = state.CorrectionMode
				state.CorrectionMode = "conservative"
			} else {
				state.ChineseSource = "corrected"
			}
		}
		if err := validateControlState(state); err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		state.UpdatedAt = float64(time.Now().UnixMilli()) / 1000
		if callbacks.Apply != nil {
			var err error
			state, err = callbacks.Apply(r.Context(), cloneControlState(state))
			if err != nil {
				writeAPIError(w, http.StatusConflict, err)
				return
			}
			if state.Terms == nil {
				state.Terms = []Term{}
			}
		} else if state.Terms == nil {
			if state.Profile == previous.Profile {
				state.Terms = previous.Terms
			} else {
				state.Terms = []Term{}
			}
		}
		s.mu.Lock()
		s.control = cloneControlState(state)
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(state)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeAPIError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func validateControlState(state ControlState) error {
	if !slices.Contains(profiles, state.Profile) {
		return fmt.Errorf("unknown profile %q", state.Profile)
	}
	if !slices.Contains(correctionModes, state.CorrectionMode) {
		return fmt.Errorf("unknown correction mode %q", state.CorrectionMode)
	}
	if !slices.Contains(chineseSources, state.ChineseSource) {
		return fmt.Errorf("unknown Chinese source %q", state.ChineseSource)
	}
	if state.ContextSize < 0 || state.ContextSize > 5 {
		return errors.New("contextSize must be between 0 and 5")
	}
	if len(state.CustomPrompt) > 8000 {
		return errors.New("customPrompt is too long")
	}
	if len(state.Terms) > 5000 {
		return errors.New("too many terms")
	}
	for _, term := range state.Terms {
		if strings.TrimSpace(term.Source) == "" || len(term.Source) > 256 || len(term.Target) > 256 {
			return errors.New("invalid term")
		}
	}
	return nil
}

func cloneControlState(state ControlState) ControlState {
	state.Terms = append([]Term(nil), state.Terms...)
	return state
}

// ---------- Model management API ----------

type modelEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Language    string `json:"language"`
	Description string `json:"description"`
	Recommended string `json:"recommended"`
	SizeMB      int    `json:"sizeMB"`
	Installed   bool   `json:"installed"`
	IsCurrent   bool   `json:"isCurrent"`
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeAPIError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	s.mu.RLock()
	dir := s.modelDir
	currentID := s.currentModelID
	s.mu.RUnlock()
	entries := make([]modelEntry, 0, len(model.Catalog))
	for _, m := range model.Catalog {
		entries = append(entries, modelEntry{
			ID: m.ID, Name: m.Name, Kind: m.Kind, Language: m.Language,
			Description: m.Description, Recommended: m.Recommended, SizeMB: m.SizeMB,
			Installed: model.Installed(m, dir),
			IsCurrent: m.ID == currentID,
		})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(entries)
}

func (s *Server) handleModelDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if !s.allowLocal(w, r) {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeAPIError(w, http.StatusBadRequest, errors.New("missing id parameter"))
		return
	}
	// Check if model is in local Catalog
	m, ok := model.Find(id)
	// If not in Catalog, check if it's a remote model with URL parameter
	remoteURL := r.URL.Query().Get("url")
	if !ok && remoteURL == "" {
		writeAPIError(w, http.StatusNotFound, fmt.Errorf("model %q not found in catalog and no url provided", id))
		return
	}
	s.mu.RLock()
	dir := s.modelDir
	s.mu.RUnlock()
	if dir == "" {
		writeAPIError(w, http.StatusInternalServerError, errors.New("model directory not configured"))
		return
	}
	// Check if already installed
	if ok && model.Installed(m, dir) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "already_installed", "id": id})
		return
	}
	// For remote models, check if directory exists
	if !ok {
		modelDir := filepath.Join(dir, id)
		if st, err := os.Stat(modelDir); err == nil && st.IsDir() {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "already_installed", "id": id})
			return
		}
	}
	// SSE response for download progress.
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, canFlush := w.(http.Flusher)
	send := func(event string, data any) {
		payload, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
		if canFlush {
			flusher.Flush()
		}
	}
	ctx := r.Context()
	var err error
	if ok {
		// Download from local Catalog
		err = model.DownloadWithOptions(ctx, m, dir, model.DownloadOptions{
			Retries: 2,
			Progress: func(p model.Progress) {
				send("progress", map[string]any{
					"downloaded":     p.Downloaded,
					"total":          p.Total,
					"bytesPerSecond": p.BytesPerSecond,
				})
			},
		})
	} else {
		// Download remote model
		err = model.DownloadRemote(ctx, id, remoteURL, dir, model.DownloadOptions{
			Retries: 2,
			Progress: func(p model.Progress) {
				send("progress", map[string]any{
					"downloaded":     p.Downloaded,
					"total":          p.Total,
					"bytesPerSecond": p.BytesPerSecond,
				})
			},
		})
	}
	if err != nil {
		if ctx.Err() != nil {
			send("cancelled", map[string]string{"id": id})
		} else {
			send("error", map[string]string{"id": id, "error": err.Error()})
		}
		return
	}
	send("done", map[string]string{"id": id})
}

func (s *Server) handleModelDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "POST, DELETE")
		writeAPIError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if !s.allowLocal(w, r) {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeAPIError(w, http.StatusBadRequest, errors.New("missing id parameter"))
		return
	}
	m, ok := model.Find(id)
	if !ok {
		writeAPIError(w, http.StatusNotFound, fmt.Errorf("model %q not found", id))
		return
	}
	s.mu.RLock()
	dir := s.modelDir
	s.mu.RUnlock()
	if err := model.Delete(m, dir); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "status": "deleted"})
}

func writeAPIError(w http.ResponseWriter, status int, err error) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
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
	_ = s.BroadcastSubtitleDetail(source, source, translation)
}

func (s *Server) BroadcastSubtitleDetail(raw, corrected, english string) error {
	s.mu.RLock()
	chineseSource := s.config.ChineseSource
	s.mu.RUnlock()
	return s.BroadcastMessage(Message{
		Type: "subtitle", Text: english, Source: corrected,
		Raw: raw, Corrected: corrected, English: english, ChineseSource: chineseSource,
		TS: float64(time.Now().UnixMilli()) / 1000,
	})
}

func (s *Server) BroadcastDebug(event DebugEvent) error {
	if event.TS == 0 {
		event.TS = float64(time.Now().UnixMilli()) / 1000
	}
	return s.broadcast(Message{Type: "debug", Debug: &event, TS: event.TS}, true)
}

func (s *Server) BroadcastMessage(msg Message) error {
	return s.broadcast(msg, false)
}

func (s *Server) broadcast(msg Message, debugOnly bool) error {
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
		if !debugOnly || c.debug {
			clients = append(clients, c)
		}
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

func (s *Server) handleDebugToggle(w http.ResponseWriter, r *http.Request) {
	if !s.allowLocal(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]bool{"enabled": s.debugEnabled.Load()})
	case http.MethodPut:
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		s.debugEnabled.Store(req.Enabled)
		_ = json.NewEncoder(w).Encode(map[string]bool{"enabled": req.Enabled})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeAPIError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	if !s.allowLocal(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		s.mu.RLock()
		state := s.control
		s.mu.RUnlock()
		// Use saved system prompt if available, otherwise use default
		systemPrompt := state.SystemPrompt
		if systemPrompt == "" {
			systemPrompt = translate.BaseSystemPrompt()
		}
		resp := struct {
			SystemPrompt     string `json:"systemPrompt"`
			BackgroundPrompt string `json:"backgroundPrompt"`
			DefaultSystemPrompt  string `json:"defaultSystemPrompt"`
			DefaultBackgroundPrompt string `json:"defaultBackgroundPrompt"`
		}{
			SystemPrompt:              systemPrompt,
			BackgroundPrompt:          state.CustomPrompt,
			DefaultSystemPrompt:       translate.BaseSystemPrompt(),
			DefaultBackgroundPrompt:   translate.DefaultCustomPrompt,
		}
		_ = json.NewEncoder(w).Encode(resp)
	case http.MethodPut:
		var req struct {
			SystemPrompt     string `json:"systemPrompt"`
			BackgroundPrompt string `json:"backgroundPrompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		if len(req.SystemPrompt) > 16000 {
			writeAPIError(w, http.StatusBadRequest, errors.New("system prompt too long"))
			return
		}
		if len(req.BackgroundPrompt) > 8000 {
			writeAPIError(w, http.StatusBadRequest, errors.New("background prompt too long"))
			return
		}
		s.mu.Lock()
		if req.SystemPrompt != "" {
			s.control.SystemPrompt = req.SystemPrompt
		}
		if req.BackgroundPrompt != "" {
			s.control.CustomPrompt = req.BackgroundPrompt
		}
		s.mu.Unlock()
		s.mu.RLock()
		callbacks := s.callbacks
		s.mu.RUnlock()
		if callbacks.Apply != nil {
			state := cloneControlState(s.control)
			updated, err := callbacks.Apply(r.Context(), state)
			if err != nil {
				writeAPIError(w, http.StatusConflict, err)
				return
			}
			s.mu.Lock()
			s.control = updated
			s.mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeAPIError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (s *Server) handleRemoteModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeAPIError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if !s.allowLocal(w, r) {
		return
	}
	ctx := r.Context()
	remoteModels, err := model.FetchRemoteModels(ctx)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, fmt.Errorf("获取远程模型列表失败: %w", err))
		return
	}

	s.mu.RLock()
	dir := s.modelDir
	s.mu.RUnlock()

	type remoteEntry struct {
		model.RemoteMeta
		Installed bool   `json:"installed"`
		InCatalog bool   `json:"inCatalog"`
		CatalogID string `json:"catalogId,omitempty"`
	}

	// Build URL -> Catalog ID mapping for matching
	catalogByURL := make(map[string]string)
	for _, m := range model.Catalog {
		catalogByURL[m.URL] = m.ID
	}

	entries := make([]remoteEntry, 0, len(remoteModels))
	for _, rm := range remoteModels {
		catalogID := catalogByURL[rm.DownloadURL]
		inCatalog := catalogID != ""
		// Use catalog ID for installation check if available
		checkID := rm.ID
		if inCatalog {
			checkID = catalogID
		}
		m := model.Meta{ID: checkID, URL: rm.DownloadURL, RequiredFiles: []string{}}
		entry := remoteEntry{
			RemoteMeta: rm,
			Installed:  model.Installed(m, dir),
			InCatalog:  inCatalog,
		}
		if inCatalog {
			entry.CatalogID = catalogID
		}
		entries = append(entries, entry)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(entries)
}
