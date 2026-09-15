package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

//go:embed webui.html
var webUI []byte

type webServer struct {
	profiles []UserProfile
	agents   map[string]*Agent
}

type profilesResponse struct {
	Profiles []UserProfile `json:"profiles"`
	Prompts  []string      `json:"prompts"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный JSON: " + err.Error()})
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "после JSON есть лишние данные"})
		return false
	}
	return true
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	return false
}

func (ws *webServer) handleProfiles(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, profilesResponse{Profiles: ws.profiles, Prompts: DemoPrompts()})
}

func (ws *webServer) handleReset(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	for _, agent := range ws.agents {
		agent.ClearTranscript()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (ws *webServer) handleAsk(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	profileID := strings.TrimPrefix(r.URL.Path, "/api/profiles/")
	profileID = strings.TrimSuffix(profileID, "/ask")
	if profileID == "" || strings.Contains(profileID, "/") || r.URL.Path != "/api/profiles/"+profileID+"/ask" {
		http.NotFound(w, r)
		return
	}
	agent, ok := ws.agents[profileID]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "профиль не найден"})
		return
	}
	var request struct {
		Prompt string `json:"prompt"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "пустой запрос"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	send := func(kind, content string) error {
		data, _ := json.Marshal(map[string]string{"type": kind, "content": content})
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	_, err := agent.Ask(r.Context(), request.Prompt, func(chunk string) error {
		return send("delta", chunk)
	})
	if err != nil {
		_ = send("error", err.Error())
		return
	}
	if warning := agent.DrainMemoryWarning(); warning != "" {
		_ = send("memory_warning", warning)
	}
	if notice := agent.DrainToolNotice(); notice != "" {
		_ = send("tool", notice)
	}
	_ = send("done", "")
}

func (ws *webServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(webUI)
}

func webHandler(profiles []UserProfile, agents map[string]*Agent) http.Handler {
	ws := &webServer{profiles: profiles, agents: agents}
	mux := http.NewServeMux()
	mux.HandleFunc("/", ws.handleIndex)
	mux.HandleFunc("/api/profiles", ws.handleProfiles)
	mux.HandleFunc("/api/profiles/", ws.handleAsk)
	mux.HandleFunc("/api/reset", ws.handleReset)
	return mux
}

func runWebServer(ctx context.Context, addr string, profiles []UserProfile, agents map[string]*Agent) error {
	server := &http.Server{Addr: addr, Handler: webHandler(profiles, agents)}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	err := server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
