package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

//go:embed webui.html
var webUI []byte

// webServer mirrors comparisonView for a browser client: two panes, the same
// commands, streamed answers over SSE instead of a repainted terminal frame.
type webServer struct {
	mu       sync.Mutex
	panes    [2]*comparisonPane
	scenario *script
}

var paneSides = [2]string{"window", "facts"}

func newWebServer(left, right *Agent) *webServer {
	ws := &webServer{scenario: &script{path: messagesPath}}
	for i, agent := range []*Agent{left, right} {
		pane := &comparisonPane{agent: agent}
		for _, message := range agent.History() {
			pane.message(message.Role, message.Content)
		}
		pane.refresh()
		ws.panes[i] = pane
	}
	return ws
}

type wireBlock struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type wirePane struct {
	Label  string      `json:"label"`
	Blocks []wireBlock `json:"blocks"`
	Status string      `json:"status"`
}
type wireState struct {
	Panes [2]wirePane `json:"panes"`
}

func (ws *webServer) snapshot() wireState {
	labels := [2]string{"Sliding Window", "Sticky Facts / Key-Value Memory"}
	var state wireState
	for i, pane := range ws.panes {
		pane.mu.Lock()
		blocks := make([]wireBlock, len(pane.blocks))
		for j, block := range pane.blocks {
			blocks[j] = wireBlock{Role: block.Role, Content: block.Content}
		}
		status := pane.status
		pane.mu.Unlock()
		state.Panes[i] = wirePane{Label: labels[i], Blocks: blocks, Status: status}
	}
	return state
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// runCommand replays comparisonView.command against both panes, minus the
// scroll/exit handling that only makes sense against a fixed terminal frame.
func (ws *webServer) runCommand(line string) {
	fields := strings.Fields(line)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return
	}
	if fields[0] == "/contextManagementStrategy" {
		for _, pane := range ws.panes {
			pane.Write([]byte("Панели фиксированы для сравнения: слева Sliding Window, справа Sticky Facts.\n"))
			pane.refresh()
		}
		return
	}
	for _, pane := range ws.panes {
		switch fields[0] {
		case "/reset":
			err := pane.agent.Reset()
			pane.mu.Lock()
			pane.blocks = nil
			pane.mu.Unlock()
			if err != nil {
				pane.Write([]byte("Ошибка очистки памяти: " + err.Error() + "\n"))
			} else {
				pane.Write([]byte("Память очищена: история, ветки и checkpoints. Расход за запуск сохранён.\n"))
			}
		case "/stats", "/facts", "/memory", "/context", "/info", "/fill", "/overflow", "/checkpoint", "/addBranch", "/branch":
			if !contextCommand(pane.agent, line, pane) && !memoryCommand(pane.agent, line, pane) {
				pane.Write([]byte("Неизвестная команда. /info — справка.\n"))
			}
		default:
			pane.Write([]byte("Неизвестная команда. /info — справка.\n"))
		}
		pane.refresh()
	}
}

func (ws *webServer) handleState(w http.ResponseWriter, r *http.Request) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	writeJSON(w, ws.snapshot())
}

func (ws *webServer) handleCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Line string `json:"line"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	line := strings.TrimSpace(body.Line)
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if fields := strings.Fields(line); len(fields) > 0 && fields[0] == "/sendNext" {
		block, note := ws.scenario.next(fields[1:])
		for _, pane := range ws.panes {
			pane.Write([]byte(note + "\n"))
			pane.refresh()
		}
		writeJSON(w, struct {
			wireState
			Ask string `json:"ask,omitempty"`
		}{ws.snapshot(), block})
		return
	}
	ws.runCommand(line)
	writeJSON(w, ws.snapshot())
}

// askEvent is one line of the SSE stream sent while a turn is in flight.
type askEvent struct {
	Side    string `json:"side,omitempty"`
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	Status  string `json:"status,omitempty"`
	Info    string `json:"info,omitempty"`
}

func (ws *webServer) handleAsk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		http.Error(w, "empty prompt", http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	send := func(event askEvent) {
		data, _ := json.Marshal(event)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	ws.ask(r.Context(), prompt, send)
	send(askEvent{Type: "done"})
}

// ask mirrors comparisonView.ask: both panes run the same prompt concurrently
// and each streamed chunk is pushed to the browser as it arrives.
func (ws *webServer) ask(ctx context.Context, prompt string, send func(askEvent)) {
	done := make(chan struct{}, len(ws.panes))
	for i, pane := range ws.panes {
		pane.message("user", prompt)
		answerIndex := pane.message("assistant", "")
		go func(side string, pane *comparisonPane) {
			defer func() { done <- struct{}{} }()
			_, err := pane.agent.AskStream(ctx, prompt, func(chunk string) error {
				pane.mu.Lock()
				pane.blocks[answerIndex].Content += chunk
				content := pane.blocks[answerIndex].Content
				pane.mu.Unlock()
				send(askEvent{Side: side, Type: "answer", Content: content})
				return nil
			})
			if err != nil {
				pane.Write([]byte("Ошибка: " + err.Error() + "\n"))
			}
			if warning := pane.agent.DrainFactsWarning(); warning != "" {
				pane.Write([]byte("Facts не обновлены, ответ по прежним фактам: " + warning + "\n"))
			}
			pane.refresh()
			pane.mu.Lock()
			info := ""
			if len(pane.blocks) > 0 && pane.blocks[len(pane.blocks)-1].Role == "info" {
				info = pane.blocks[len(pane.blocks)-1].Content
			}
			status := pane.status
			pane.mu.Unlock()
			send(askEvent{Side: side, Type: "status", Status: status, Info: info})
		}(paneSides[i], pane)
	}
	for pending := 0; pending < len(ws.panes); pending++ {
		<-done
	}
}

func (ws *webServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(webUI)
}

func runWebServer(ctx context.Context, addr string, left, right *Agent) error {
	ws := newWebServer(left, right)
	mux := http.NewServeMux()
	mux.HandleFunc("/", ws.handleIndex)
	mux.HandleFunc("/api/state", ws.handleState)
	mux.HandleFunc("/api/command", ws.handleCommand)
	mux.HandleFunc("/api/ask", ws.handleAsk)
	server := &http.Server{Addr: addr, Handler: mux}
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
