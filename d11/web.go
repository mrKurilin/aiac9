package main

import (
	"bytes"
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

type webServer struct{ agent *Agent }

type webState struct {
	ShortTerm  map[string]string `json:"short_term"`
	Working    map[string]string `json:"working"`
	LongTerm   map[string]string `json:"long_term"`
	Transcript []Message         `json:"transcript"`
	Limits     map[string]int    `json:"limits"`
}

type webCommandResponse struct {
	webState
	Output string `json:"output"`
}

func (ws *webServer) state() webState {
	layers := ws.agent.Snapshot()
	return webState{ShortTerm: layers.ShortTerm, Working: layers.Working, LongTerm: layers.LongTerm, Transcript: ws.agent.Transcript(), Limits: map[string]int{"dialog_messages": dialogMessageLimit}}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
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

func (ws *webServer) handleState(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, ws.state())
}

func (ws *webServer) handleTranscriptReset(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	ws.agent.ClearTranscript()
	writeJSON(w, http.StatusOK, ws.state())
}

func (ws *webServer) handleMemory(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var request struct {
		Action string `json:"action"`
		Layer  string `json:"layer"`
		Key    string `json:"key,omitempty"`
		Value  string `json:"value,omitempty"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	var err error
	switch request.Action {
	case "remember":
		err = ws.agent.Remember(request.Layer, request.Key, request.Value)
	case "forget":
		err = ws.agent.Forget(request.Layer, request.Key)
	case "clear":
		err = ws.agent.Clear(request.Layer)
	default:
		err = fmt.Errorf("action: remember, forget или clear")
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ws.state())
}

func (ws *webServer) handleCommand(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var request struct {
		Line string `json:"line"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	line := strings.TrimSpace(request.Line)
	if line == "" || !strings.HasPrefix(line, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ожидалась команда, начинающаяся с /"})
		return
	}
	var output bytes.Buffer
	if line == "/exit" || line == "/quit" {
		fmt.Fprintln(&output, "В веб-режиме закройте вкладку; сервер останавливается Ctrl+C в терминале.")
	} else {
		handled, _ := runMemoryCommand(ws.agent, line, &output)
		if !handled {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неизвестная команда; /info — справка"})
			return
		}
	}
	writeJSON(w, http.StatusOK, webCommandResponse{webState: ws.state(), Output: strings.TrimSpace(output.String())})
}

func (ws *webServer) handleAsk(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
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
	_, err := ws.agent.Ask(r.Context(), request.Prompt, func(chunk string) error { return send("delta", chunk) })
	if err != nil {
		_ = send("error", err.Error())
		return
	}
	if warning := ws.agent.DrainMemoryWarning(); warning != "" {
		_ = send("memory_warning", warning)
	}
	if notice := ws.agent.DrainToolNotice(); notice != "" {
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

func webHandler(agent *Agent) http.Handler {
	ws := &webServer{agent: agent}
	mux := http.NewServeMux()
	mux.HandleFunc("/", ws.handleIndex)
	mux.HandleFunc("/api/state", ws.handleState)
	mux.HandleFunc("/api/transcript/reset", ws.handleTranscriptReset)
	mux.HandleFunc("/api/memory", ws.handleMemory)
	mux.HandleFunc("/api/command", ws.handleCommand)
	mux.HandleFunc("/api/ask", ws.handleAsk)
	return mux
}

func runWebServer(ctx context.Context, addr string, agent *Agent) error {
	server := &http.Server{Addr: addr, Handler: webHandler(agent)}
	go func() { <-ctx.Done(); _ = server.Close() }()
	err := server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
