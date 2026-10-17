package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type telegramClient struct {
	base string
	http *http.Client
}
type telegramError struct{ code, retry int }

func (e telegramError) Error() string { return fmt.Sprintf("Telegram: код %d", e.code) }
func (t *telegramClient) call(ctx context.Context, method string, payload any, out any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("Telegram: ошибка запроса")
	}
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/"+method, bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("Telegram: неверный адрес")
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := t.http.Do(req)
		if err != nil {
			return fmt.Errorf("Telegram: соединение не удалось")
		}
		var body struct {
			OK         bool            `json:"ok"`
			Result     json.RawMessage `json:"result"`
			Code       int             `json:"error_code"`
			Parameters struct {
				Retry int `json:"retry_after"`
			} `json:"parameters"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("Telegram: неверный ответ HTTP %d", resp.StatusCode)
		}
		if !body.OK {
			if body.Code == 0 {
				body.Code = resp.StatusCode
			}
			if body.Code == 429 && body.Parameters.Retry > 0 && body.Parameters.Retry <= 60 && attempt < 2 {
				if !waitContext(ctx, time.Duration(body.Parameters.Retry)*time.Second) {
					return ctx.Err()
				}
				continue
			}
			return telegramError{body.Code, body.Parameters.Retry}
		}
		if out != nil {
			if err := json.Unmarshal(body.Result, out); err != nil {
				return fmt.Errorf("Telegram: неверный результат")
			}
		}
		return nil
	}
	return fmt.Errorf("Telegram: превышено число попыток")
}
func waitContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func (t *telegramClient) send(ctx context.Context, id int64, text string) error {
	runes := []rune(text)
	// Plain text avoids Markdown injection; 1800 runes is under Telegram's UTF-16 limit.
	for len(runes) > 0 {
		n := min(len(runes), 1800)
		if err := t.call(ctx, "sendMessage", map[string]any{"chat_id": id, "text": string(runes[:n]), "link_preview_options": map[string]bool{"is_disabled": true}}, nil); err != nil {
			return err
		}
		runes = runes[n:]
	}
	return nil
}

type update struct {
	ID      int64            `json:"update_id"`
	Message *telegramMessage `json:"message"`
}
type telegramMessage struct {
	Text string `json:"text"`
	From struct {
		ID  int64 `json:"id"`
		Bot bool  `json:"is_bot"`
	} `json:"from"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
}
type chatSession struct {
	conditions searchPlan
	history    []message
	cancel     context.CancelFunc
	generation int
	last       time.Time
	day        string
	count      int
}
type bot struct {
	tg       *telegramClient
	template *agent
	allowed  map[int64]bool
	mu       sync.Mutex
	sessions map[int64]*chatSession
	slots    chan struct{}
	workers  sync.WaitGroup
	day      string
	calls    int
}

func (b *bot) reply(ctx context.Context, id int64, text string) {
	if err := b.tg.send(ctx, id, text); err != nil && ctx.Err() == nil {
		log.Printf("Ответ не доставлен: %v", err)
	}
}
func (b *bot) handle(ctx context.Context, m *telegramMessage) {
	if m == nil || m.Chat.Type != "private" || m.From.Bot || m.Text == "" {
		return
	}
	if strings.TrimSpace(m.Text) == "" {
		return
	}
	id := m.Chat.ID
	command := strings.Fields(m.Text)[0]
	command = strings.Split(command, "@")[0]
	if command == "/id" {
		b.reply(ctx, id, fmt.Sprintf("Ваш Telegram ID: %d", m.From.ID))
		return
	}
	if len(b.allowed) > 0 && !b.allowed[m.From.ID] {
		b.reply(ctx, id, "Доступ ограничен владельцем бота. /id — ваш ID.")
		return
	}
	if len([]rune(m.Text)) > 2000 {
		b.reply(ctx, id, "Сократите запрос до 2000 символов.")
		return
	}
	if command == "/help" || command == "/start" {
		b.reply(ctx, id, helpText)
		return
	}
	b.mu.Lock()
	now := time.Now()
	s := b.sessions[id]
	if s == nil {
		for key, v := range b.sessions {
			if v.cancel == nil && now.Sub(v.last) > 24*time.Hour {
				delete(b.sessions, key)
			}
		}
		if len(b.sessions) >= 1000 {
			b.mu.Unlock()
			b.reply(ctx, id, "Сервис занят. Попробуйте позже.")
			return
		}
		s = &chatSession{}
		b.sessions[id] = s
	}
	if command == "/reset" || command == "/clear" || command == "/cancel" {
		if s.cancel != nil {
			s.cancel()
		}
		s.generation++
		s.cancel = nil
		if command != "/cancel" {
			s.history = nil
			s.conditions = searchPlan{}
		}
		b.mu.Unlock()
		b.reply(ctx, id, "Операция отменена. "+map[bool]string{true: "Начат новый поиск.", false: "Условия предыдущего поиска сохранены."}[command != "/cancel"])
		return
	}
	if strings.HasPrefix(command, "/") {
		b.mu.Unlock()
		b.reply(ctx, id, "Неизвестная команда. /help")
		return
	}
	if s.cancel != nil {
		b.mu.Unlock()
		b.reply(ctx, id, "Ещё выполняется предыдущий запрос. /cancel — отменить.")
		return
	}
	day := now.UTC().Format("2006-01-02")
	if b.day != day {
		b.day = day
		b.calls = 0
	}
	if s.day != day {
		s.day = day
		s.count = 0
	}
	if s.count >= 40 || b.calls >= 500 || (!s.last.IsZero() && now.Sub(s.last) < 10*time.Second) {
		b.mu.Unlock()
		b.reply(ctx, id, "Лимит запросов: не чаще раза в 10 секунд, 40 в день на пользователя и 500 на бота. Попробуйте позже.")
		return
	}
	select {
	case b.slots <- struct{}{}:
	default:
		b.mu.Unlock()
		b.reply(ctx, id, "Все обработчики заняты. Попробуйте позже.")
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	s.cancel = cancel
	s.generation++
	generation := s.generation
	s.last = now
	s.count++
	b.calls++
	a := *b.template
	a.history = append([]message(nil), s.history...)
	a.conditions = s.conditions
	b.mu.Unlock()
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		defer func() { <-b.slots }()
		defer cancel()
		b.reply(opCtx, id, "Разбираю условия и проверяю кэш Aviasales…")
		answer, err := a.answer(opCtx, m.Text, func(stage string) { log.Print(stage) })
		b.mu.Lock()
		if s.generation != generation {
			b.mu.Unlock()
			return
		}
		b.mu.Unlock()
		defer func() {
			b.mu.Lock()
			if s.generation == generation {
				s.cancel = nil
			}
			b.mu.Unlock()
		}()
		if opCtx.Err() != nil {
			if ctx.Err() == nil {
				b.reply(ctx, id, "Время поиска истекло. Попробуйте ещё раз.")
			}
			return
		}
		if err != nil {
			log.Printf("Поиск завершился ошибкой: %v", err)
			b.reply(ctx, id, "Не удалось выполнить запрос. "+err.Error()+". Попробуйте позже или уточните условия.")
			return
		}
		b.mu.Lock()
		if s.generation != generation {
			b.mu.Unlock()
			return
		}
		s.history = a.history
		s.conditions = a.conditions
		b.mu.Unlock()
		// The operation context is cancelled by reset, including an in-flight send.
		b.reply(opCtx, id, answer)
	}()
}
func atomicState(path, text string) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return fmt.Errorf("не удалось создать файл состояния")
	}
	defer os.Remove(file.Name())
	if _, err = file.WriteString(text); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("не удалось сохранить состояние")
	}
	return nil
}
func runTelegram(ctx context.Context, a *agent) error {
	value := os.Getenv("TELEGRAM_BOT_TOKEN")
	if value == "" {
		return fmt.Errorf("задайте TELEGRAM_BOT_TOKEN")
	}
	allowed, err := parseAllowed(os.Getenv("ALLOWED_TELEGRAM_IDS"))
	if err != nil {
		return err
	}
	tg := &telegramClient{base: "https://api.telegram.org/bot" + value, http: newHTTP(45 * time.Second)}
	return serveTelegram(ctx, a, tg, allowed, stateDir())
}
func serveTelegram(ctx context.Context, a *agent, tg *telegramClient, allowed map[int64]bool, dir string) error {
	var me struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if err := tg.call(ctx, "getMe", map[string]any{}, &me); err != nil {
		return err
	}
	if me.ID <= 0 {
		return fmt.Errorf("Telegram: getMe не вернул ID бота")
	}
	var webhook struct {
		URL string `json:"url"`
	}
	if err := tg.call(ctx, "getWebhookInfo", map[string]any{}, &webhook); err != nil {
		return err
	}
	if webhook.URL != "" {
		return fmt.Errorf("у бота настроен webhook: используйте нового бота или удалите webhook перед запуском")
	}
	// Validate the Travelpayouts credential without a paid LLM request.
	var probe struct {
		Success bool `json:"success"`
	}
	if err := a.travel.get(ctx, "/aviasales/v3/prices_for_dates", url.Values{"origin": {"MOW"}, "currency": {"rub"}, "limit": {"1"}, "departure_at": {a.now().Format("2006-01")}}, &probe, true); err != nil {
		return err
	}
	if !probe.Success {
		return fmt.Errorf("Aviasales: проверка доступа не прошла")
	}
	menu := []map[string]string{{"command": "start", "description": "Начать"}, {"command": "help", "description": "Условия поиска"}, {"command": "reset", "description": "Новый поиск"}, {"command": "clear", "description": "Новый поиск"}, {"command": "cancel", "description": "Отменить запрос"}, {"command": "id", "description": "Мой Telegram ID"}}
	if err := tg.call(ctx, "setMyCommands", map[string]any{"commands": menu}, nil); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("нет доступа к каталогу данных")
	}
	offsetPath := filepath.Join(dir, "offset")
	identityPath := filepath.Join(dir, "bot-id")
	identity := strconv.FormatInt(me.ID, 10)
	previousIdentity, identityErr := os.ReadFile(identityPath)
	if identityErr != nil && !os.IsNotExist(identityErr) {
		return fmt.Errorf("не удалось прочитать ID бота")
	}
	if string(previousIdentity) != identity {
		// update_id belongs to a particular bot, not to a deployment or token string.
		if err := atomicState(offsetPath, "0"); err != nil {
			return err
		}
		if err := atomicState(identityPath, identity); err != nil {
			return err
		}
	}
	offset := int64(0)
	data, err := os.ReadFile(offsetPath)
	if err == nil {
		offset, err = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if err != nil || offset < 0 {
			return fmt.Errorf("повреждён offset")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("не удалось прочитать offset")
	}
	b := &bot{tg: tg, template: a, allowed: allowed, sessions: map[int64]*chatSession{}, slots: make(chan struct{}, 4)}
	workerCtx, stop := context.WithCancel(ctx)
	defer func() { stop(); b.workers.Wait() }()
	log.Print("Telegram: бот готов; DeepSeek будет вызван только при сообщении пользователя")
	backoff := time.Second
	for ctx.Err() == nil {
		var updates []update
		err := tg.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "limit": 20, "allowed_updates": []string{"message"}}, &updates)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var apiErr telegramError
			if errors.As(err, &apiErr) && (apiErr.code == 401 || apiErr.code == 404 || apiErr.code == 409) {
				return err
			}
			log.Printf("Telegram: опрос не удался; повтор через %s (%v)", backoff, err)
			if !waitContext(ctx, backoff) {
				return nil
			}
			backoff = min(30*time.Second, backoff*2)
			continue
		}
		backoff = time.Second
		if err := atomicState(filepath.Join(dir, "heartbeat"), "ready\n"); err != nil {
			return err
		}
		for _, u := range updates {
			if u.ID < offset {
				continue
			}
			offset = u.ID + 1
			// At-most-once dispatch: don't replay paid operations after a process crash.
			if err := atomicState(offsetPath, strconv.FormatInt(offset, 10)); err != nil {
				return err
			}
			b.handle(workerCtx, u.Message)
		}
	}
	return nil
}
