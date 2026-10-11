package week6

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Service struct {
	Agent         *Agent
	Bind          string
	ChatLogger    *slog.Logger
	authDigest    [sha256.Size]byte
	requiresAuth  bool
	MaxInputChars int
	RateLimit     int
	sem           chan struct{}
	mu            sync.Mutex
	window        time.Time
	requests      int
	sessions      map[string]*chatSession
	server        *http.Server
	listener      net.Listener
}

type chatSession struct {
	messages []Message
	updated  time.Time
}

func NewService(agent *Agent, bind, token string) (*Service, error) {
	if bind == "" {
		bind = "127.0.0.1:8780"
	}
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return nil, fmt.Errorf("неверный SERVICE_BIND: %w", err)
	}
	ip := net.ParseIP(host)
	loopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	if !loopback && token == "" {
		return nil, errors.New("для сетевого SERVICE_BIND задайте SERVICE_API_TOKEN")
	}
	return &Service{Agent: agent, Bind: bind, authDigest: sha256.Sum256([]byte(token)), requiresAuth: token != "", MaxInputChars: 12000, RateLimit: 12, sem: make(chan struct{}, 1), sessions: make(map[string]*chatSession)}, nil
}

func (s *Service) allowRequest() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.window) >= time.Minute {
		s.window, s.requests = now, 0
	}
	if s.requests >= s.RateLimit {
		return false
	}
	s.requests++
	return true
}

func (s *Service) authorized(r *http.Request) bool {
	if !s.requiresAuth {
		return true
	}
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	provided := sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer ")))
	return subtle.ConstantTimeCompare(provided[:], s.authDigest[:]) == 1
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "model": s.Agent.Model})
	})
	mux.HandleFunc("/v1/chat", s.chat)
	mux.HandleFunc("/v1/reset", s.reset)
	return mux
}

func validSessionID(id string) bool {
	if len(id) < 16 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Service) session(id string) *chatSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, item := range s.sessions {
		if now.Sub(item.updated) > time.Hour {
			delete(s.sessions, key)
		}
	}
	if item := s.sessions[id]; item != nil {
		item.updated = now
		return item
	}
	if len(s.sessions) >= 64 {
		var oldestID string
		var oldest time.Time
		for key, item := range s.sessions {
			if oldestID == "" || item.updated.Before(oldest) {
				oldestID, oldest = key, item.updated
			}
		}
		delete(s.sessions, oldestID)
	}
	item := &chatSession{updated: now}
	s.sessions[id] = item
	return item
}

func (s *Service) reset(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		SessionID string `json:"session_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || !validSessionID(request.SessionID) {
		http.Error(w, "invalid session_id", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	delete(s.sessions, request.SessionID)
	s.mu.Unlock()
	if s.ChatLogger != nil {
		s.ChatLogger.Info("chat reset", "session_id", request.SessionID)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Service) chat(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.allowRequest() {
		http.Error(w, "rate limit", http.StatusTooManyRequests)
		return
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		http.Error(w, "model busy", http.StatusTooManyRequests)
		return
	}
	var request struct {
		Messages  []Message `json:"messages"`
		SessionID string    `json:"session_id"`
		Message   string    `json:"message"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}
	var session *chatSession
	if request.SessionID != "" || request.Message != "" {
		if !validSessionID(request.SessionID) || request.Message == "" || len(request.Messages) != 0 {
			http.Error(w, "invalid session request", http.StatusBadRequest)
			return
		}
		if len([]rune(request.Message)) > s.MaxInputChars {
			http.Error(w, "input too long", http.StatusRequestEntityTooLarge)
			return
		}
		session = s.session(request.SessionID)
		request.Messages = append(append([]Message(nil), session.messages...), Message{Role: "user", Content: request.Message})
		for len(request.Messages) > 1 && (len(request.Messages) > 12 || messageChars(request.Messages) > s.MaxInputChars) {
			request.Messages = request.Messages[2:]
		}
	}
	if len(request.Messages) == 0 || len(request.Messages) > 12 || request.Messages[len(request.Messages)-1].Role != "user" {
		http.Error(w, "expected 1–12 messages ending with user", http.StatusBadRequest)
		return
	}
	for _, message := range request.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			http.Error(w, "invalid role", http.StatusBadRequest)
			return
		}
	}
	if messageChars(request.Messages) > s.MaxInputChars {
		http.Error(w, "input too long", http.StatusRequestEntityTooLarge)
		return
	}
	if s.ChatLogger != nil {
		s.ChatLogger.Info("chat request", "session_id", request.SessionID, "message", request.Messages[len(request.Messages)-1].Content)
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	result, err := s.Agent.ServiceChat(ctx, request.Messages)
	if err != nil {
		status, message := http.StatusBadGateway, "model unavailable"
		if errors.Is(err, context.DeadlineExceeded) {
			status, message = http.StatusGatewayTimeout, "model timeout"
		}
		if s.ChatLogger != nil {
			s.ChatLogger.Error("chat response", "session_id", request.SessionID, "status", status, "duration_ms", time.Since(started).Milliseconds())
		}
		http.Error(w, message, status)
		return
	}
	if session != nil {
		s.mu.Lock()
		if s.sessions[request.SessionID] == session {
			session.messages = append(request.Messages, Message{Role: "assistant", Content: result.Text})
			session.updated = time.Now()
		}
		s.mu.Unlock()
	}
	if s.ChatLogger != nil {
		s.ChatLogger.Info("chat response", "session_id", request.SessionID, "status", http.StatusOK, "message", result.Text, "duration_ms", time.Since(started).Milliseconds())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"message":       Message{Role: "assistant", Content: result.Text},
		"prompt_tokens": result.PromptTokens, "output_tokens": result.OutputTokens,
		"duration_ms": result.Duration.Milliseconds(),
	})
}

func messageChars(messages []Message) int {
	chars := 0
	for _, message := range messages {
		chars += len([]rune(message.Content))
	}
	return chars
}

func (a *Agent) ServiceChat(ctx context.Context, messages []Message) (ChatResult, error) {
	conversation := make([]Message, 0, len(messages)+1)
	conversation = append(conversation, Message{Role: "system", Content: Economical.Prompt})
	conversation = append(conversation, messages...)
	options := Economical.Options
	return a.Client.Chat(ctx, a.Model, conversation, options)
}

func (s *Service) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		return errors.New("сервис уже запущен")
	}
	listener, err := net.Listen("tcp", s.Bind)
	if err != nil {
		return err
	}
	s.listener = listener
	s.server = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 4 * time.Minute, MaxHeaderBytes: 8 << 10}
	go func() { _ = s.server.Serve(listener) }()
	return nil
}

func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	server := s.server
	s.server, s.listener = nil, nil
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

func (s *Service) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.server != nil
}

func (s *Service) Check(ctx context.Context, token string) (string, error) {
	s.mu.Lock()
	listener := s.listener
	s.mu.Unlock()
	if listener == nil {
		return "", errors.New("сначала выполните /server start")
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return "", err
	}
	url := "http://" + net.JoinHostPort("127.0.0.1", port) + "/v1/chat"
	client := &http.Client{Timeout: 4 * time.Minute}
	var out strings.Builder
	for i := 1; i <= 3; i++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, EncodeChatRequest([]Message{{Role: "user", Content: "Ответь одним словом: OK"}}))
		if err != nil {
			return out.String(), err
		}
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		started := time.Now()
		response, err := client.Do(request)
		if err != nil {
			return out.String(), err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		fmt.Fprintf(&out, "%d/3: HTTP %d, %s\n", i, response.StatusCode, time.Since(started).Round(time.Millisecond))
		if response.StatusCode != http.StatusOK {
			return out.String(), fmt.Errorf("сервис вернул HTTP %d", response.StatusCode)
		}
	}
	return strings.TrimSpace(out.String()), nil
}

// EncodeChatRequest is used by the local smoke check, keeping the wire format in one place.
func EncodeChatRequest(messages []Message) io.Reader {
	data, _ := json.Marshal(map[string]any{"messages": messages})
	return strings.NewReader(string(data))
}
