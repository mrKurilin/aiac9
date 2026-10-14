package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

//go:embed web/*
var websiteFiles embed.FS

type crmHTTP struct {
	store         *crmStore
	adminPassword string
	serviceKey    string
	supportURL    string
	client        *http.Client
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func apiError(w http.ResponseWriter, status int, text string) {
	writeJSON(w, status, map[string]string{"error": text})
}

func decodeBody(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("неверный JSON запроса")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("лишние данные в запросе")
	}
	return nil
}

func sameOrigin(r *http.Request) bool {
	if r.Header.Get("Origin") == "" {
		return true
	}
	origin, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && (origin.Scheme == "http" || origin.Scheme == "https") && origin.Host == r.Host
}

func constantEqual(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

func (s *crmHTTP) handler() http.Handler {
	assets, _ := fs.Sub(websiteFiles, "web")
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		path := r.URL.Path
		if path == "/healthz" {
			writeJSON(w, 200, map[string]string{"status": "ok"})
			return
		}
		if path == "/mcp" || strings.HasPrefix(path, "/internal/") {
			if s.serviceKey == "" || !constantEqual(r.Header.Get("X-Service-Key"), s.serviceKey) || !sameOrigin(r) {
				apiError(w, 403, "доступ запрещён")
				return
			}
			if path == "/mcp" {
				s.handleMCP(w, r)
			} else {
				s.handleInternal(w, r)
			}
			return
		}
		if path == "/crm" || path == "/crm/" || strings.HasPrefix(path, "/api/crm/") {
			w.Header().Set("Cache-Control", "no-store")
			user, password, ok := r.BasicAuth()
			if !ok || user != "admin" || s.adminPassword == "" || !constantEqual(password, s.adminPassword) {
				w.Header().Set("WWW-Authenticate", `Basic realm="Garage 33 CRM", charset="UTF-8"`)
				apiError(w, 401, "войдите в CRM под учётной записью admin")
				return
			}
			if !sameOrigin(r) {
				apiError(w, 403, "доступ запрещён")
				return
			}
			if strings.HasPrefix(path, "/api/crm/") {
				s.handleTasks(w, r)
				return
			}
			r.URL.Path = "/crm.html"
			files.ServeHTTP(w, r)
			return
		}
		if path == "/api/chat" {
			w.Header().Set("Cache-Control", "no-store")
			if !sameOrigin(r) {
				apiError(w, 403, "доступ запрещён")
				return
			}
			s.proxyChat(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			apiError(w, 405, "метод не поддерживается")
			return
		}
		if path != "/" && path != "/style.css" && path != "/site.js" && path != "/crm.js" {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func sessionFor(w http.ResponseWriter, r *http.Request) (string, error) {
	if cookie, err := r.Cookie("garage_chat"); err == nil && validSession(cookie.Value) {
		return cookie.Value, nil
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(bytes[:])
	http.SetCookie(w, &http.Cookie{Name: "garage_chat", Value: id, Path: "/api/chat", HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteStrictMode, MaxAge: 86400 * 30})
	return id, nil
}

func (s *crmHTTP) proxyChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
		apiError(w, 405, "метод не поддерживается")
		return
	}
	session, err := sessionFor(w, r)
	if err != nil {
		apiError(w, 500, "не удалось создать диалог")
		return
	}
	var body io.Reader
	if r.Method == http.MethodPost {
		var input struct {
			Message string `json:"message"`
		}
		if decodeBody(w, r, &input) != nil || strings.TrimSpace(input.Message) == "" || len([]rune(input.Message)) > 2000 {
			apiError(w, 400, "сообщение должно содержать от 1 до 2000 символов")
			return
		}
		payload, _ := json.Marshal(input)
		body = strings.NewReader(string(payload))
	}
	if r.Method == http.MethodDelete {
		// A fresh cookie clears the active conversation without deleting tasks or records.
		http.SetCookie(w, &http.Cookie{Name: "garage_chat", Path: "/api/chat", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		writeJSON(w, 200, map[string]bool{"reset": true})
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, strings.TrimRight(s.supportURL, "/")+"/chat", body)
	if err != nil {
		apiError(w, 503, "поддержка временно недоступна")
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Service-Key", s.serviceKey)
	request.Header.Set("X-Chat-Session", session)
	response, err := s.client.Do(request)
	if err != nil {
		apiError(w, 503, "поддержка временно недоступна; попробуйте позже")
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, 512<<10))
}

func (s *crmHTTP) handleTasks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.URL.Path == "/api/crm/tasks" {
		switch r.Method {
		case http.MethodGet:
			tasks, err := s.store.tasks(ctx)
			if err != nil {
				apiError(w, 500, "не удалось загрузить задачи")
				return
			}
			writeJSON(w, 200, tasks)
		case http.MethodPost:
			var input struct {
				Title  string `json:"title"`
				Reason string `json:"reason"`
			}
			if decodeBody(w, r, &input) != nil {
				apiError(w, 400, "неверный запрос")
				return
			}
			task, err := s.store.createTask(ctx, "", input.Title, "general", "", input.Reason, nil)
			if err != nil {
				apiError(w, 400, "не удалось создать задачу: проверьте заголовок и описание")
				return
			}
			writeJSON(w, 201, task)
		default:
			apiError(w, 405, "метод не поддерживается")
		}
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/crm/tasks/"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPatch {
		var input struct {
			Status string `json:"status"`
			Title  string `json:"title"`
			Reason string `json:"reason"`
		}
		if decodeBody(w, r, &input) != nil {
			apiError(w, 400, "неверный запрос")
			return
		}
		if err := s.store.updateTask(ctx, id, input.Status, input.Title, input.Reason); err != nil {
			if err == sql.ErrNoRows {
				apiError(w, 404, "задача не найдена")
			} else {
				apiError(w, 409, "не удалось сохранить задачу; проверьте статус и заголовок")
			}
			return
		}
	} else if r.Method != http.MethodGet {
		apiError(w, 405, "метод не поддерживается")
		return
	}
	task, err := s.store.task(ctx, id)
	if err != nil {
		apiError(w, 404, "задача не найдена")
		return
	}
	writeJSON(w, 200, task)
}

func (s *crmHTTP) handleInternal(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/internal/messages" {
		http.NotFound(w, r)
		return
	}
	session := r.Header.Get("X-Chat-Session")
	if !validSession(session) {
		apiError(w, 400, "неверный идентификатор диалога")
		return
	}
	switch r.Method {
	case http.MethodGet:
		records, err := s.store.conversation(r.Context(), session)
		if err != nil {
			apiError(w, 500, "не удалось загрузить диалог")
			return
		}
		writeJSON(w, 200, records)
	case http.MethodPost:
		var input struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if decodeBody(w, r, &input) != nil || (input.Role != "user" && input.Role != "assistant") || len([]rune(input.Content)) > 12000 {
			apiError(w, 400, "неверное сообщение")
			return
		}
		if err := s.store.appendMessage(r.Context(), session, input.Role, input.Content); err != nil {
			apiError(w, 500, "не удалось сохранить сообщение")
			return
		}
		writeJSON(w, 201, map[string]bool{"saved": true})
	default:
		apiError(w, 405, "метод не поддерживается")
	}
}

func crmTools() []mcpTool {
	return []mcpTool{
		{"inventory_list", "Получить актуальный ассортимент, остатки и цены автосервиса. Наличие нельзя угадывать по FAQ.", json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
		{"access_check", "Проверить доступ к личному кабинету по указанному пользователем телефону, даты ограничения и необходимость расследования.", json.RawMessage(`{"type":"object","properties":{"phone":{"type":"string"}},"required":["phone"],"additionalProperties":false}`)},
		{"create_support_task", "Создать задачу TODO в CRM для нерешённого вопроса, со всем контекстом диалога и указателями по коду. Не создавай задачу для известного ограничения только на сегодня или известного нулевого остатка.", json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"category":{"type":"string","enum":["access","inventory","general"]},"phone":{"type":"string"},"reason":{"type":"string"},"sources":{"type":"array","items":{"type":"string"}}},"required":["title","category","reason"],"additionalProperties":false}`)},
	}
}

func (s *crmHTTP) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, 405, "используйте POST")
		return
	}
	var request rpcMessage
	if decodeBody(w, r, &request) != nil || request.JSONRPC != "2.0" {
		apiError(w, 400, "неверный JSON-RPC")
		return
	}
	if request.ID == nil {
		if request.Method != "notifications/initialized" {
			apiError(w, 400, "неподдерживаемое уведомление")
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	response := rpcMessage{JSONRPC: "2.0", ID: request.ID}
	var result any
	switch request.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "garage-crm", "version": "d33"}}
	case "tools/list":
		result = map[string]any{"tools": crmTools()}
	case "tools/call":
		if r.Header.Get("MCP-Protocol-Version") != protocolVersion {
			response.Error = &rpcError{-32602, "неподдерживаемая версия MCP"}
			break
		}
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(request.Params, &params) != nil {
			response.Error = &rpcError{-32602, "неверные параметры"}
			break
		}
		value, err := s.executeTool(r.Context(), r.Header.Get("X-Chat-Session"), params.Name, params.Arguments)
		content := ""
		if err != nil {
			content = err.Error()
		} else {
			data, _ := json.Marshal(value)
			content = string(data)
		}
		result = map[string]any{"isError": err != nil, "content": []map[string]string{{"type": "text", "text": content}}}
	default:
		response.Error = &rpcError{-32601, "метод не поддерживается"}
	}
	if response.Error == nil {
		response.Result, _ = json.Marshal(result)
	}
	writeJSON(w, 200, response)
}

func (s *crmHTTP) executeTool(ctx context.Context, session, name string, args json.RawMessage) (any, error) {
	if !validSession(session) {
		return nil, fmt.Errorf("неверный идентификатор диалога")
	}
	decoder := json.NewDecoder(strings.NewReader(string(args)))
	decoder.DisallowUnknownFields()
	switch name {
	case "inventory_list":
		var input struct{}
		if decoder.Decode(&input) != nil {
			return nil, fmt.Errorf("inventory_list не принимает параметры")
		}
		items, err := s.store.inventory(ctx)
		if err != nil {
			return nil, fmt.Errorf("склад временно недоступен")
		}
		return map[string]any{"items": items, "checked_at": s.store.now().Format(time.RFC3339)}, nil
	case "access_check":
		var input struct {
			Phone string `json:"phone"`
		}
		if decoder.Decode(&input) != nil {
			return nil, fmt.Errorf("нужен номер телефона")
		}
		return s.store.lookupAccess(ctx, input.Phone)
	case "create_support_task":
		var input struct {
			Title    string   `json:"title"`
			Category string   `json:"category"`
			Phone    string   `json:"phone"`
			Reason   string   `json:"reason"`
			Sources  []string `json:"sources"`
		}
		if decoder.Decode(&input) != nil {
			return nil, fmt.Errorf("неверные параметры задачи")
		}
		if input.Category == "access" && input.Phone == "" {
			return nil, fmt.Errorf("для расследования доступа нужен номер телефона пользователя")
		}
		if input.Category == "access" && input.Phone != "" {
			account, err := s.store.lookupAccess(ctx, input.Phone)
			if err != nil {
				return nil, err
			}
			if account.BlockedDate != "" {
				return nil, fmt.Errorf("причина подтверждена: ограничение только на сегодня, задача на расследование не требуется")
			}
		}
		if len(input.Sources) > 10 {
			return nil, fmt.Errorf("слишком много источников")
		}
		for _, source := range input.Sources {
			if len(source) > 200 {
				return nil, fmt.Errorf("неверный источник")
			}
		}
		task, err := s.store.createTask(ctx, session, input.Title, input.Category, input.Phone, input.Reason, input.Sources)
		if err != nil {
			return nil, fmt.Errorf("задачу не удалось сохранить")
		}
		return map[string]any{"task_id": task.ID, "status": task.Status, "title": task.Title}, nil
	default:
		return nil, fmt.Errorf("неизвестный инструмент")
	}
}
