package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

//go:embed index.html
var webFiles embed.FS

type webApp struct {
	client           ChatClient
	system, password string
	mu               sync.Mutex
	agents           map[string]*Agent
}

func newWebApp(client ChatClient, system, password string) *webApp {
	return &webApp{client: client, system: system, password: password, agents: make(map[string]*Agent)}
}

func (a *webApp) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", a.index)
	mux.HandleFunc("POST /api/chat", a.chat)
	mux.HandleFunc("POST /api/reset", a.reset)
	if a.password == "" {
		return mux
	}
	return a.basicAuth(mux)
}

func (a *webApp) basicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, ok := r.BasicAuth()
		valid := subtle.ConstantTimeCompare([]byte(password), []byte(a.password)) == 1
		if !ok || !valid {
			w.Header().Set("WWW-Authenticate", `Basic realm="AI chat", charset="UTF-8"`)
			http.Error(w, "Требуется пароль", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *webApp) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := webFiles.ReadFile("index.html")
	if err != nil {
		http.Error(w, "interface unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(page)
}

func (a *webApp) chat(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Message string `json:"message"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.Message) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Введите сообщение"})
		return
	}
	answer, err := a.agentFor(w, r).Ask(r.Context(), input.Message)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"answer": answer})
}

func (a *webApp) reset(w http.ResponseWriter, r *http.Request) {
	a.agentFor(w, r).Reset()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *webApp) agentFor(w http.ResponseWriter, r *http.Request) *Agent {
	const cookieName = "d6_session"
	id := ""
	if cookie, err := r.Cookie(cookieName); err == nil {
		id = cookie.Value
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if agent := a.agents[id]; id != "" && agent != nil {
		return agent
	}
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		panic(err)
	}
	id = hex.EncodeToString(random)
	agent := NewAgent(a.client, a.system)
	a.agents[id] = agent
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	return agent
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
